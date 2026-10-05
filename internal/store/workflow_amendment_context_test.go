package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
)

// seedWorkflowAmendmentFanout seeds enough accepted refiners that the two
// mandated roots hold more than 32 qualifying one-hop edges together, the
// workflow page shape the 32-edge total bound must hold. The seed also
// fills the home to the corpus dataset multiplier (1000 distinct laws), the
// synthetic acceptance population the PM1 latency and query-plan evidence
// must measure against; the filler laws carry no relations toward the
// paged roots, so the workflow page shape stays deterministic.
func seedWorkflowAmendmentFanout(t *testing.T, s *Store, home KnowledgeHome, commit string) {
	t.Helper()
	ctx := context.Background()
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		id := fmt.Sprintf("CD-9001%02d", i)
		if _, err := tx.ExecContext(ctx, `INSERT INTO law_subjects(home_project_id,home_locator_id,law_id,kind,status,path,title,content_hash,scanned_commit_oid) VALUES(?,?,?,?,?,?,?,?,?)`,
			home.HomeProjectID, home.HomeLocatorID, id, "decision", "accepted", ".concord/docs/decisions/"+id+".md", "Fanout refiner "+id, "sha256:"+strings.Repeat("a", 64), commit); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO law_relations(home_project_id,home_locator_id,source_law_id,kind,target_law_id,scanned_commit_oid) VALUES(?,?,?,?,?,?)`,
			home.HomeProjectID, home.HomeLocatorID, id, "refines", "CD-0017", commit); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 18; i++ {
		id := fmt.Sprintf("CD-9101%02d", i)
		if _, err := tx.ExecContext(ctx, `INSERT INTO law_subjects(home_project_id,home_locator_id,law_id,kind,status,path,title,content_hash,scanned_commit_oid) VALUES(?,?,?,?,?,?,?,?,?)`,
			home.HomeProjectID, home.HomeLocatorID, id, "decision", "accepted", ".concord/docs/decisions/"+id+".md", "Fanout refiner "+id, "sha256:"+strings.Repeat("b", 64), commit); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO law_relations(home_project_id,home_locator_id,source_law_id,kind,target_law_id,scanned_commit_oid) VALUES(?,?,?,?,?,?)`,
			home.HomeProjectID, home.HomeLocatorID, id, "refines", "CD-0054", commit); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < knowledgeBodyBenchmarkRows-42; i++ {
		id := fmt.Sprintf("CD-F%04d", i)
		if _, err := tx.ExecContext(ctx, `INSERT INTO law_subjects(home_project_id,home_locator_id,law_id,kind,status,path,title,content_hash,scanned_commit_oid) VALUES(?,?,?,?,?,?,?,?,?)`,
			home.HomeProjectID, home.HomeLocatorID, id, "decision", "accepted", ".concord/docs/decisions/"+id+".md", "Acceptance-scale filler "+id, "sha256:"+strings.Repeat("c", 64), commit); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM fold_guard WHERE active=1`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

// The workflow law context reuses the PM1 Q10 amendment-context query for
// its explicitly mandated roots over a pool-verified live source snapshot:
// one page totals at most 32 edges across roots, names incomplete roots,
// and continues inside the same transaction through the tx-scoped core.
func TestWorkflowAmendmentContextPagesThirtyTwoEdgesAcrossRoots(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, home := refinementTestStore(t)
	defer s.Close()
	commit := firstCommitOID(t, s, home)
	seedWorkflowAmendmentFanout(t, s, home, commit)
	workID := "workflow-amendment-fanout"
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1);
		INSERT INTO work_items(id,kind,title,lifecycle,priority,version,created_at,updated_at) VALUES(?,'task','Amendment fanout','needed',1,1,'now','now');
		INSERT INTO work_projects(work_id,project_id,role) VALUES(?, 'refinement-project','primary');
		DELETE FROM fold_guard`, workID, workID); err != nil {
		t.Fatal(err)
	}
	amendment := verifyWorkflowLawContextSources(ctx, s.DatabaseForTesting(), nil, workID)
	if amendment == nil || len(amendment.sources) != 1 || amendment.sources[0].HomeLocatorID != home.HomeLocatorID {
		t.Fatalf("pool verification did not resolve the live source set: %+v", amendment)
	}
	if amendment.verification.degraded || len(amendment.verification.omissions) != 0 {
		t.Fatalf("live source verified degraded: %+v", amendment.verification)
	}
	tx, err := s.DatabaseForTesting().BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	page, err := queryKnowledgeRefinementContext(ctx, tx, KnowledgeRefinementContextRequest{}, amendment.sources, []string{"CD-0017", "CD-0054"}, refinementContextMaxLimit, amendment.verification)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Edges) != 32 {
		t.Fatalf("workflow page returned %d edges, want the 32-edge total bound", len(page.Edges))
	}
	// Ordering is endpoint-first, so root CD-0017's 22 incoming edges fit
	// inside the page while CD-0054 loses 10 of its 20: only the root whose
	// edges the page cut is named incomplete.
	if len(page.IncompleteRoots) != 1 || page.IncompleteRoots[0] != "CD-0054" {
		t.Fatalf("incomplete roots = %v, want [CD-0054]", page.IncompleteRoots)
	}
	if page.ResultMeta.NextCursor == nil || *page.ResultMeta.NextCursor == "" {
		t.Fatal("workflow page carried no continuation cursor")
	}
	rest, err := queryKnowledgeRefinementContext(ctx, tx, KnowledgeRefinementContextRequest{Cursor: *page.ResultMeta.NextCursor}, amendment.sources, []string{"CD-0017", "CD-0054"}, refinementContextMaxLimit, amendment.verification)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	total := 0
	for _, pages := range []KnowledgeRefinementContextResult{page, rest} {
		for _, edge := range pages.Edges {
			key := edge.RootID + "\x00" + edge.EndpointLawID + "\x00" + edge.Direction
			if seen[key] {
				t.Fatalf("edge %v repeated across pages", key)
			}
			seen[key] = true
			total++
		}
	}
	// 20 fanout + 3 authored incoming for CD-0017, 18 fanout + 1 authored
	// incoming for CD-0054, plus the declared outgoing edges of both roots.
	if total < 42 {
		t.Fatalf("pages carried %d distinct edges, want at least 42 across the two roots", total)
	}
}

// A mandated root whose pool verification could not resolve a source set
// never reads as an authoritative empty graph: the section names the
// unverified source as a degraded omission.
func TestWorkflowAmendmentContextUnverifiedSourceIsDegraded(t *testing.T) {
	t.Parallel()
	s, _ := refinementTestStore(t)
	defer s.Close()
	amendment := verifyWorkflowLawContextSources(context.Background(), s.DatabaseForTesting(), nil, "work-without-a-home")
	if amendment == nil || len(amendment.sources) != 0 || !amendment.verification.degraded {
		t.Fatalf("unresolved work produced %+v, want a degraded snapshot with no sources", amendment)
	}
	tx, err := s.DatabaseForTesting().BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	page, err := queryKnowledgeRefinementContext(context.Background(), tx, KnowledgeRefinementContextRequest{}, amendment.sources, []string{"CD-0017"}, refinementContextMaxLimit, amendment.verification)
	if err != nil {
		t.Fatal(err)
	}
	if page.ResultMeta.Authority != "degraded" {
		t.Fatalf("authority = %s, want degraded", page.ResultMeta.Authority)
	}
	if len(page.ResultMeta.Omissions) == 0 || page.ResultMeta.Omissions[0] != "knowledge_source_unverified" {
		t.Fatalf("omissions = %v, want the named unverified source", page.ResultMeta.Omissions)
	}
}

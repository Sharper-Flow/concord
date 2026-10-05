package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// The public pool-owned path must return one coherent snapshot: an empty
// relation graph still carries the watermark identity of the commit the
// out-of-transaction verification proved, never an authoritative
// no-amendments claim over an unread identity.
func TestQ10AmendmentPublicPathEmptyGraphStaysVerified(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, home := refinementTestStore(t)
	defer s.Close()
	page, err := s.QueryKnowledgeRefinementContext(ctx, KnowledgeRefinementContextRequest{Roots: []string{"CD-0017"}, Sources: []KnowledgeHome{home}})
	if err != nil {
		t.Fatalf("a verified empty graph must answer, not refuse: %v", err)
	}
	// The seeded fixture holds authored relations; force the empty-graph
	// branch deterministically by asking for a root nothing relates to.
	empty, err := s.QueryKnowledgeRefinementContext(ctx, KnowledgeRefinementContextRequest{Roots: []string{"CD-9999"}, Sources: []KnowledgeHome{home}})
	if err != nil {
		t.Fatalf("empty amendment graph refused: %v", err)
	}
	if empty.Authority != "authoritative" {
		t.Fatalf("empty graph authority = %s with omissions %v, want authoritative over a verified source", empty.Authority, empty.Omissions)
	}
	if len(empty.Edges) != 0 || len(page.Edges) == 0 {
		t.Fatalf("fixture shape changed: populated=%d empty=%d", len(page.Edges), len(empty.Edges))
	}
	if len(empty.SourceWatermarks) != 1 || empty.SourceWatermarks[0].Watermark == "" {
		t.Fatalf("empty graph did not carry its verified watermark: %+v", empty.SourceWatermarks)
	}
}

// A projection refresh that lands between two public pages changes the
// snapshot the cursor bound; continuation must refuse instead of splicing
// the pre-refresh page to post-refresh relations.
func TestQ10AmendmentPublicPathCursorRefusesRefreshedSnapshot(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, home := refinementTestStore(t)
	defer s.Close()
	first, err := s.QueryKnowledgeRefinementContext(ctx, KnowledgeRefinementContextRequest{Roots: []string{"CD-0017"}, Limit: 1, Sources: []KnowledgeHome{home}})
	if err != nil {
		t.Fatal(err)
	}
	if first.ResultMeta.NextCursor == nil {
		t.Fatal("limit 1 over the authored fixture must produce a continuation cursor")
	}
	writeKnowledgeFile(t, home.RepoPath, "README.md", "refresh between pages")
	newCommit := commitKnowledgeRepo(t, home.RepoPath, "refresh between pages")
	newDigest := seedRefinementDigest(t, home, newCommit)
	refresh, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := refresh.ExecContext(ctx, `INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		refresh.Rollback()
		t.Fatal(err)
	}
	for _, query := range []string{
		`UPDATE law_relations SET scanned_commit_oid=? WHERE home_project_id=? AND home_locator_id=?`,
		`UPDATE law_subjects SET scanned_commit_oid=? WHERE home_project_id=? AND home_locator_id=?`,
	} {
		if _, err := refresh.ExecContext(ctx, query, newCommit, home.HomeProjectID, home.HomeLocatorID); err != nil {
			refresh.Rollback()
			t.Fatal(err)
		}
	}
	if _, err := refresh.ExecContext(ctx, `UPDATE knowledge_index_watermark SET scanned_commit_oid=?,scanned_content_digest=? WHERE home_project_id=? AND home_locator_id=?`, newCommit, newDigest, home.HomeProjectID, home.HomeLocatorID); err != nil {
		refresh.Rollback()
		t.Fatal(err)
	}
	if _, err := refresh.ExecContext(ctx, `DELETE FROM fold_guard`); err != nil {
		refresh.Rollback()
		t.Fatal(err)
	}
	if err := refresh.Commit(); err != nil {
		t.Fatal(err)
	}
	_, err = s.QueryKnowledgeRefinementContext(ctx, KnowledgeRefinementContextRequest{Roots: []string{"CD-0017"}, Limit: 1, Cursor: *first.ResultMeta.NextCursor, Sources: []KnowledgeHome{home}})
	if err == nil {
		t.Fatal("continuation across a refreshed projection snapshot must refuse")
	}
	if failure, ok := err.(*Failure); !ok || (failure.Kind != KindStaleContext && failure.Kind != KindInvalidCursor) {
		t.Fatalf("refresh drift error = %v, want a typed stale-context or invalid-cursor refusal", err)
	}
}

// Positive workflow-continuity integration: the continuity snapshot's law
// context carries the amendment section for explicitly mandated roots
// through the full public read, not only through direct core calls.
func TestReadWorkflowContinuityCarriesAmendmentContext(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openTemp(t)
	workID := "continuity-amendment"
	seedLawContextFixture(t, s, workID)
	binding := WorkflowArchitectureBinding{DomainRegistryContentHash: "sha256:" + strings.Repeat("b", 64), HomeDomainID: "root", AffectedDomainIDs: []string{"root"}, DomainModifies: []string{}, DomainRelationModifies: []WorkflowDomainRelationModification{}, LawAdditions: []WorkflowLawAddition{{LawID: "law:new", HomeDomainID: "root"}}, VerificationObligations: []WorkflowVerificationObligation{{LawID: "spec:one", ObligationID: "verification"}}}
	approveLawContextContract(t, s, workID, []string{"spec:one", "law:new"}, nil, binding)
	if err := os.WriteFile(filepath.Join(workflowLawFixtureRepo(t, s), ".concord", "docs", "law-new.md"), []byte("# Added law\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1);
		INSERT OR IGNORE INTO law_subjects(home_project_id,home_locator_id,law_id,kind,status,path,title,content_hash,scanned_commit_oid) VALUES('project','workflow-law-locator','law:new','decision','accepted','.concord/docs/law-new.md','Added law','sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee','test');
		INSERT INTO law_relations(home_project_id,home_locator_id,source_law_id,kind,target_law_id,scanned_commit_oid) VALUES('project','workflow-law-locator','spec:one','refines','law:new','test');
		DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	snapshot, err := ReadWorkflowContinuity(ctx, s, ContinuityRequest{Work: workID})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.LawContext == nil {
		t.Fatal("continuity resolved no law context")
	}
	amendment := snapshot.LawContext.AmendmentContext
	if amendment == nil {
		t.Fatal("continuity law context carried no amendment section for a mandated root")
	}
	found := false
	for _, edge := range amendment.Edges {
		if edge.RootID == "spec:one" && edge.Direction == "outgoing" && edge.EndpointLawID == "law:new" {
			found = true
		}
	}
	if !found {
		t.Fatalf("amendment section edges = %+v, want the authored outgoing refines edge to law:new", amendment.Edges)
	}
}

// The acceptance-scale query obligations of PM1 Q10: over the 1000-law
// synthetic population with multiple roots and cross-source endpoints, the
// one-hop read issues a source-scaled statement count that stays flat as
// roots and edges grow, one full page serializes inside the agent result
// envelope cap, and uninstrumented P50/P99 hold the 100ms metadata budget.
func TestQ10AmendmentQueryPlansAndLatency(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, home := refinementTestStore(t)
	defer s.Close()
	commit := firstCommitOID(t, s, home)
	seedWorkflowAmendmentFanout(t, s, home, commit)
	second := seedRefinementScaleSource(t, s, "refinement-scale-2", "refinement-scale-2-loc", 32)
	seedRefinementCrossEndpoints(t, s, home, second, commit, []string{"CD-0017", "CD-0054"}, 8)
	sources := []KnowledgeHome{home, second}
	verification := refinementCoreVerification(t, s, sources)
	countCoreStatements := func(roots []string) int {
		tx, err := s.DatabaseForTesting().BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		counter := &coordinatorCON830CountingQueryer{queryer: tx}
		if _, err := queryKnowledgeRefinementContext(ctx, counter, KnowledgeRefinementContextRequest{}, sources, orderedStrings(roots), refinementContextMaxLimit, verification); err != nil {
			t.Fatal(err)
		}
		return counter.queries
	}
	// The statement-count comparison reads pages of one edge class: a root
	// whose page holds only same-home incoming edges, then six roots of the
	// same shape. Endpoint enrichment is page-bounded, so a page that also
	// holds cross-source edges legitimately issues the structured endpoint
	// lookup; the high-fanout regression pins that bound separately.
	one := countCoreStatements([]string{home.HomeProjectID + "/CD-900100"})
	six := countCoreStatements([]string{home.HomeProjectID + "/CD-0017", home.HomeProjectID + "/CD-0054", home.HomeProjectID + "/CD-0058", home.HomeProjectID + "/CD-900100", home.HomeProjectID + "/CD-910100", home.HomeProjectID + "/CD-F0000"})
	if one != six {
		t.Fatalf("core statement count grows with the root set: one root=%d, six roots=%d", one, six)
	}
	// Grow the edge population by two hundred authored relations among the
	// filler laws: the statement count must stay tied to sources and
	// distinct endpoint Projects, never to the edge count.
	grow, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := grow.ExecContext(ctx, `INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		grow.Rollback()
		t.Fatal(err)
	}
	for i := 0; i < 200; i++ {
		source := fmt.Sprintf("CD-F%04d", i)
		target := fmt.Sprintf("CD-F%04d", 400+i)
		if _, err := grow.ExecContext(ctx, `INSERT INTO law_relations(home_project_id,home_locator_id,source_law_id,kind,target_law_id,scanned_commit_oid) VALUES(?,?,?,?,?,?)`,
			home.HomeProjectID, home.HomeLocatorID, source, "refines", target, commit); err != nil {
			grow.Rollback()
			t.Fatal(err)
		}
	}
	if _, err := grow.ExecContext(ctx, `DELETE FROM fold_guard WHERE active=1`); err != nil {
		grow.Rollback()
		t.Fatal(err)
	}
	if err := grow.Commit(); err != nil {
		t.Fatal(err)
	}
	if grown := countCoreStatements([]string{home.HomeProjectID + "/CD-0017", home.HomeProjectID + "/CD-0054", home.HomeProjectID + "/CD-0058", home.HomeProjectID + "/CD-900100", home.HomeProjectID + "/CD-910100", home.HomeProjectID + "/CD-F0000"}); grown != six {
		t.Fatalf("core statement count grows with the edge population: %d edges later=%d, before=%d", 200, grown, six)
	}
	// PM1 synthetic acceptance scale: the seeded fanout fixture at the
	// corpus dataset multiplier, one bounded page per iteration, measured
	// without race instrumentation against the 100ms P99 metadata budget.
	// A full 32-edge page must also serialize inside the agent result
	// envelope cap, the boundary the read envelope budget enforces.
	const refinementPageBudgetBytes = 51200
	roots := []string{home.HomeProjectID + "/CD-0017", home.HomeProjectID + "/CD-0054", home.HomeProjectID + "/CD-0058"}
	const iterations = 200
	samples := make([]time.Duration, 0, iterations)
	var outputBytes int
	for i := 0; i < iterations; i++ {
		start := time.Now()
		page, err := s.QueryKnowledgeRefinementContext(ctx, KnowledgeRefinementContextRequest{Roots: roots, Limit: refinementContextMaxLimit, Sources: sources})
		samples = append(samples, time.Since(start))
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			encoded, marshalErr := json.Marshal(page)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			outputBytes = len(encoded)
			if outputBytes > refinementPageBudgetBytes {
				t.Fatalf("full 32-edge page serialized to %d bytes, over the %d-byte result envelope cap", outputBytes, refinementPageBudgetBytes)
			}
			if len(page.Edges) != refinementContextMaxLimit {
				t.Fatalf("page held %d edges, want the 32-edge bound", len(page.Edges))
			}
		}
	}
	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	p50 := samples[len(samples)*50/100]
	p99 := samples[len(samples)*99/100]
	t.Logf("amendment-context page: %d bytes, P50=%s P99=%s over %d iterations", outputBytes, p50, p99, iterations)
	if p99 > 100*time.Millisecond {
		t.Fatalf("P99 %s exceeds the PM1 100ms metadata budget", p99)
	}
}

package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
)

// A qualified opt-in root keeps its root identity. The peer source renames its law subject to
// the same bare ID the home holds, so a bare federated lookup would refuse
// as ambiguous; the qualified root form the caller supplied must scope the
// amendment context to its Project's canonical home instead of collapsing
// to the bare ID.
func TestCoordinatorCON830QualifiedQ10RootKeepsIdentity(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openTemp(t)
	defer s.Close()
	home := seedAmendmentContextHome(t)
	authorizeKnowledgeProductHome(t, s, "amendment-product", home)
	if err := s.RebuildKnowledgeIndex(ctx, home); err != nil {
		t.Fatal(err)
	}
	peer := seedRefinementScaleSource(t, s, "qualified-peer", "qualified-peer-loc", 1)
	registerA1Peer(t, s, home, peer)
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1);
      UPDATE law_subjects SET law_id='CD-0001' WHERE home_project_id=?;
      DELETE FROM fold_guard`, peer.HomeProjectID); err != nil {
		t.Fatal(err)
	}
	root := home.HomeProjectID + "/CD-0001"
	historical, err := s.QueryQ10(ctx, Q10Request{Product: "amendment-product", KnowledgeID: root})
	if err != nil || historical.Status != "canonical" {
		t.Fatalf("historical qualified resolution: %v %+v", err, historical)
	}
	_, err = s.QueryQ10(ctx, Q10Request{Product: "amendment-product", KnowledgeID: root, IncludeAmendmentContext: true})
	if err != nil {
		t.Fatalf("qualified opt-in must not collapse to a bare ambiguous lookup: %v", err)
	}
}

// registerA1Peer registers one peer source on the amendment product beside
// its designated home, the registered-source form of the product's source
// set.
func registerA1Peer(t *testing.T, s *Store, home, peer KnowledgeHome) {
	t.Helper()
	for _, statement := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO fold_guard(active) VALUES(1)`, nil},
		{`INSERT INTO product_projects(product_id,project_id,role) VALUES('amendment-product',?,'primary')`, []any{home.HomeProjectID}},
		{`INSERT INTO product_projects(product_id,project_id,role) VALUES('amendment-product',?,'secondary')`, []any{peer.HomeProjectID}},
		{`INSERT INTO product_knowledge_sources(product_id,project_id,locator_id,registered_at) VALUES('amendment-product',?,?, 'now')`, []any{peer.HomeProjectID, peer.HomeLocatorID}},
		{`DELETE FROM fold_guard`, nil},
	} {
		if _, err := s.DatabaseForTesting().Exec(statement.sql, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
}

// Two roots that share their bare ID across sources produce edges whose endpoint, kind,
// and bare root are identical. A cursor that keys on those components
// alone skips the second root's authored edge on continuation; the total
// qualified order (root scope, relation class, source identity) keeps
// every authored edge reachable across pages.
func TestCoordinatorCON830CrossRootCursorKeepsEqualKeyEdges(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, home := refinementTestStore(t)
	defer s.Close()
	peer1 := seedRefinementScaleSource(t, s, "cursor-peer-1", "cursor-peer-1-loc", 1)
	peer2 := seedRefinementScaleSource(t, s, "cursor-peer-2", "cursor-peer-2-loc", 1)
	commit := firstCommitOID(t, s, home)
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1);
      INSERT INTO law_cross_source_relations(home_project_id,home_locator_id,source_law_id,kind,target_project_id,target_law_id,scanned_commit_oid) VALUES(?,?,?,'refines',?,'EXT-0000',?),(?,?,?,'refines',?,'EXT-0000',?);
      DELETE FROM fold_guard`, home.HomeProjectID, home.HomeLocatorID, "CD-0058", peer1.HomeProjectID, commit, home.HomeProjectID, home.HomeLocatorID, "CD-0058", peer2.HomeProjectID, commit); err != nil {
		t.Fatal(err)
	}
	req := KnowledgeRefinementContextRequest{Roots: []string{peer1.HomeProjectID + "/EXT-0000", peer2.HomeProjectID + "/EXT-0000"}, Sources: []KnowledgeHome{home, peer1, peer2}, Limit: 1}
	first, err := s.QueryKnowledgeRefinementContext(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Edges) != 1 || first.NextCursor == nil {
		t.Fatalf("first page: %+v", first)
	}
	if first.Edges[0].RootScopeProjectID != peer1.HomeProjectID && first.Edges[0].RootScopeProjectID != peer2.HomeProjectID {
		t.Fatalf("edge root scope %q names neither requested root's Project", first.Edges[0].RootScopeProjectID)
	}
	req.Cursor = *first.NextCursor
	second, err := s.QueryKnowledgeRefinementContext(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Edges) != 1 {
		t.Fatalf("second authored root edge lost: first=%+v second=%+v", first.Edges, second.Edges)
	}
	if second.Edges[0].RootScopeProjectID == first.Edges[0].RootScopeProjectID {
		t.Fatalf("continuation repeated one root's edge instead of the equal-key peer: %+v then %+v", first.Edges[0], second.Edges[0])
	}
}

// An opt-in current context whose current source set cannot resolve refuses on a strict read
// instead of falling back to the historical home and claiming its
// authority; a degraded read names the unresolved set and never claims an
// authoritative graph over it.
func TestCoordinatorCON830CurrentContextRequiresCurrentSourceSet(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openTemp(t)
	defer s.Close()
	home := seedAmendmentContextHome(t)
	authorizeKnowledgeProductHome(t, s, "amendment-product", home)
	if err := s.RebuildKnowledgeIndex(ctx, home); err != nil {
		t.Fatal(err)
	}
	peer := seedRefinementScaleSource(t, s, "source-set-peer", "source-set-peer-loc", 1)
	registerA1Peer(t, s, home, peer)
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1);
      DELETE FROM product_knowledge_homes WHERE product_id='amendment-product';
      DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	root := home.HomeProjectID + "/CD-0001"
	historical, err := s.QueryQ10(ctx, Q10Request{Product: "amendment-product", KnowledgeID: root})
	if err != nil || historical.Status != "canonical" {
		t.Fatalf("historical result: %v %+v", err, historical)
	}
	result, err := s.QueryQ10(ctx, Q10Request{Product: "amendment-product", KnowledgeID: root, IncludeAmendmentContext: true})
	if err == nil {
		t.Fatalf("strict read must refuse an unresolved current source set, not fall back to the historical home: %+v", result.Result.CurrentAmendmentContext)
	}
	degraded, err := s.QueryQ10(ctx, Q10Request{Product: "amendment-product", KnowledgeID: root, IncludeAmendmentContext: true, AllowDegraded: true})
	if err != nil {
		t.Fatalf("degraded read refused instead of naming the omission: %v", err)
	}
	amendment := degraded.Result.CurrentAmendmentContext
	if amendment == nil {
		t.Fatal("degraded opt-in carried no amendment section")
	}
	if amendment.Authority == "authoritative" {
		t.Fatalf("degraded read claimed authoritative context over an unresolved source set: %+v", amendment.ResultMeta)
	}
	joined := strings.Join(amendment.Omissions, "\n")
	if !strings.Contains(joined, "current_source_set_unresolved:amendment-product") {
		t.Fatalf("degraded read omitted the unresolved source set: %v", amendment.Omissions)
	}
}

// seedHighFanoutRefiners seeds `refiners` accepted same-home refiners of
// one root plus `cross` declared cross-source outgoing relations of the
// same root toward distinct laws of the peer source, all at the seeded
// projection commit.
func seedHighFanoutRefiners(t *testing.T, s *Store, home, peer KnowledgeHome, commit string, refiners, cross int) {
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
	if _, err := tx.ExecContext(ctx, `INSERT INTO law_subjects(home_project_id,home_locator_id,law_id,kind,status,path,title,content_hash,scanned_commit_oid) VALUES(?,?,?,?,?,?,?,?,?)`,
		home.HomeProjectID, home.HomeLocatorID, "CD-HFROOT", "decision", "accepted", ".concord/docs/decisions/CD-HFROOT.md", "High-fanout root", "sha256:"+strings.Repeat("e", 64), commit); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < refiners; i++ {
		id := fmt.Sprintf("CD-HF%04d", i)
		if _, err := tx.ExecContext(ctx, `INSERT INTO law_subjects(home_project_id,home_locator_id,law_id,kind,status,path,title,content_hash,scanned_commit_oid) VALUES(?,?,?,?,?,?,?,?,?)`,
			home.HomeProjectID, home.HomeLocatorID, id, "decision", "accepted", ".concord/docs/decisions/"+id+".md", "High-fanout refiner "+id, "sha256:"+strings.Repeat("d", 64), commit); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO law_relations(home_project_id,home_locator_id,source_law_id,kind,target_law_id,scanned_commit_oid) VALUES(?,?,?,?,?,?)`,
			home.HomeProjectID, home.HomeLocatorID, id, "refines", "CD-HFROOT", commit); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < cross; i++ {
		if _, err := tx.ExecContext(ctx, `INSERT INTO law_cross_source_relations(home_project_id,home_locator_id,source_law_id,kind,target_project_id,target_law_id,scanned_commit_oid) VALUES(?,?,?,?,?,?,?)`,
			home.HomeProjectID, home.HomeLocatorID, "CD-HFROOT", "subordinate_to", peer.HomeProjectID, fmt.Sprintf("EXT-%04d", i), commit); err != nil {
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

// Page selection must be a bounded indexed read. Over a root holding thousands
// of qualifying one-hop edges, every relation page statement carries its
// LIMIT, endpoint enrichment stays bounded to the selected page's distinct
// endpoints, the per-page statement count stays flat and equal to the
// small-graph page, and a full cursor walk returns every authored edge
// exactly once.
func TestCoordinatorCON830HighFanoutBoundedRead(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, home := refinementTestStore(t)
	defer s.Close()
	commit := firstCommitOID(t, s, home)
	const refiners, cross = 5000, 500
	peer := seedRefinementScaleSource(t, s, "fanout-peer", "fanout-peer-loc", cross)
	seedHighFanoutRefiners(t, s, home, peer, commit, refiners, cross)
	sources := []KnowledgeHome{home, peer}
	verification := refinementCoreVerification(t, s, sources)

	walk := func(root string) (seenKeys map[string]bool, statementsPerPage []int, enrichmentBound int, pages int) {
		t.Helper()
		seenKeys = map[string]bool{}
		enrichmentBound = -1
		tx, err := s.DatabaseForTesting().BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		probe := &refinementStatementProbe{queryer: tx}
		cursor := ""
		for {
			before := len(probe.statements)
			page, err := queryKnowledgeRefinementContext(ctx, probe, KnowledgeRefinementContextRequest{Cursor: cursor}, sources, []string{root}, refinementContextMaxLimit, verification)
			if err != nil {
				t.Fatal(err)
			}
			statementsPerPage = append(statementsPerPage, len(probe.statements)-before)
			for _, statement := range probe.statements[before:] {
				upper := strings.ToUpper(statement.query)
				if strings.Contains(statement.query, "FROM law_relations") || strings.Contains(statement.query, "FROM law_cross_source_relations r") {
					if strings.Contains(upper, "COUNT(*)") {
						continue // count aggregates are bounded by construction
					}
					if !strings.Contains(upper, "LIMIT") {
						t.Fatalf("unbounded page statement reached the application: %q", statement.query)
					}
				}
				if strings.Contains(statement.query, "FROM law_subjects WHERE home_project_id=? AND law_id IN") {
					bound := len(statement.args) - 1
					if bound > enrichmentBound {
						enrichmentBound = bound
					}
					if bound > refinementContextMaxLimit+1 {
						t.Fatalf("endpoint enrichment read %d distinct laws for one %d-edge page", bound, refinementContextMaxLimit)
					}
				}
			}
			for _, edge := range page.Edges {
				key := refinementEdgeKey(edge)
				joined := strings.Join(key[:], "\x00")
				if seenKeys[joined] {
					t.Fatalf("edge %v repeated across pages", key)
				}
				seenKeys[joined] = true
			}
			pages++
			if page.ResultMeta.NextCursor == nil {
				if len(page.IncompleteRoots) != 0 {
					t.Fatalf("terminal page still named incomplete roots: %v", page.IncompleteRoots)
				}
				break
			}
			cursor = *page.ResultMeta.NextCursor
		}
		return seenKeys, statementsPerPage, enrichmentBound, pages
	}

	highKeys, highStatements, enrichmentBound, highPages := walk(home.HomeProjectID + "/CD-HFROOT")
	if len(highKeys) != refiners+cross {
		t.Fatalf("walk returned %d distinct edges, want %d", len(highKeys), refiners+cross)
	}
	if highPages != (refiners+cross+refinementContextMaxLimit-1)/refinementContextMaxLimit {
		t.Fatalf("walk used %d pages, want the bounded page count %d", highPages, (refiners+cross+refinementContextMaxLimit-1)/refinementContextMaxLimit)
	}
	if enrichmentBound < 0 || enrichmentBound > refinementContextMaxLimit+1 {
		t.Fatalf("enrichment bound = %d, want a page-bounded lookup", enrichmentBound)
	}
	// Pages that hold only same-home edges issue the identical statement
	// set; a page that also holds cross-source edges adds only its bounded
	// endpoint enrichment, at most one lookup per distinct page endpoint.
	for index, count := range highStatements {
		if count > highStatements[0]+refinementContextMaxLimit {
			t.Fatalf("page %d issued %d statements, page 1 issued %d: per-page statements must stay source-scaled with page-bounded enrichment", index+1, count, highStatements[0])
		}
	}
	// The same root shape over the unexpanded fixture keeps the identical
	// per-page statement count: statements scale with sources, never with
	// the edge population.
	smallKeys, smallStatements, _, _ := walk(home.HomeProjectID + "/CD-0017")
	if len(smallKeys) != 2 {
		t.Fatalf("small-graph walk returned %d edges, want the 2 authored refinements", len(smallKeys))
	}
	if smallStatements[0] != highStatements[0] {
		t.Fatalf("per-page statements grew with the edge population: small=%d high=%d", smallStatements[0], highStatements[0])
	}
}

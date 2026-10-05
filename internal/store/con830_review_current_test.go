package store

import (
	"context"
	"database/sql"
	"os"
	"testing"
)

func TestReviewCurrentSourceSetDrift(t *testing.T) {
	ctx := context.Background()
	s, _ := refinementTestStore(t)
	defer s.Close()
	peer := seedRefinementScaleSource(t, s, "review-new-peer", "review-new-peer-loc", 1)
	const work = "review-source-set-drift"
	_, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1);
 INSERT INTO work_items(id,kind,title,lifecycle,priority,version,created_at,updated_at) VALUES(?,'task','source-set race','needed',1,1,'now','now');
 INSERT INTO work_projects(work_id,project_id,role) VALUES(?,'refinement-project','primary');
 DELETE FROM fold_guard`, work, work)
	if err != nil {
		t.Fatal(err)
	}
	proof := verifyWorkflowLawContextSources(ctx, s.DatabaseForTesting(), work)
	if proof.verification.degraded || len(proof.sources) != 1 {
		t.Fatalf("bad initial proof: %+v", proof)
	}
	for _, stmt := range []refine4ProbeStatement{
		{"INSERT INTO fold_guard(active) VALUES(1)", nil},
		{"INSERT INTO product_projects(product_id,project_id,role) VALUES('refinement-product',?,'secondary')", []any{peer.HomeProjectID}},
		{"INSERT INTO product_knowledge_sources(product_id,project_id,locator_id,registered_at) VALUES('refinement-product',?,?,'now')", []any{peer.HomeProjectID, peer.HomeLocatorID}},
		{"DELETE FROM fold_guard", nil},
	} {
		if _, err := s.DatabaseForTesting().Exec(stmt.sql, stmt.args...); err != nil {
			t.Fatalf("%s: %v", stmt.sql, err)
		}
	}
	tx, err := s.DatabaseForTesting().BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	page, err := queryKnowledgeRefinementContext(ctx, tx, KnowledgeRefinementContextRequest{Product: "refinement-product"}, proof.sources, []string{"CD-0017"}, 32, proof.verification)
	if err == nil && page.Authority == "authoritative" {
		t.Fatalf("source-set gained an unverified required peer after verification, but context remains authoritative: sources=%+v edges=%+v", page.SourceWatermarks, page.Edges)
	}
}

// The contextual degradation flag must never suppress a historical proof
// failure: a corrupted recorded commit refuses the read even when the
// caller explicitly allowed current-context degradation, while the
// historical AllowDegraded keeps its own degraded-missing behavior.
func TestReviewContextDegradationNeverSuppressesHistoricalProof(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	defer s.Close()
	home := seedAmendmentContextHome(t)
	authorizeKnowledgeProductHome(t, s, "amendment-product", home, home.HomeProjectID)
	if err := s.RebuildKnowledgeIndex(ctx, home); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1);
 UPDATE archived_work SET commit_oid='0000000000000000000000000000000000000000' WHERE id='CD-0001';
 DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	strict, err := s.QueryQ10(ctx, Q10Request{Product: "amendment-product", KnowledgeID: "CD-0001", IncludeAmendmentContext: true})
	if err == nil {
		t.Fatalf("strict read survived a broken historical proof: %+v", strict)
	}
	degraded, err := s.QueryQ10(ctx, Q10Request{Product: "amendment-product", KnowledgeID: "CD-0001", IncludeAmendmentContext: true, AmendmentContextAllowDegraded: true})
	if err == nil {
		t.Fatalf("current-context degradation suppressed a historical proof failure: %+v", degraded)
	}
	historical, err := s.QueryQ10(ctx, Q10Request{Product: "amendment-product", KnowledgeID: "CD-0001", AllowDegraded: true})
	if err != nil || historical.Status != "missing" {
		t.Fatalf("historical AllowDegraded lost its own degraded-missing behavior: %v %+v", err, historical)
	}
}

func TestReviewDegradedBareQ10(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	defer s.Close()
	home := seedAmendmentContextHome(t)
	authorizeKnowledgeProductHome(t, s, "amendment-product", home)
	if err := s.RebuildKnowledgeIndex(ctx, home); err != nil {
		t.Fatal(err)
	}
	peer := seedRefinementScaleSource(t, s, "review-degraded-peer", "review-degraded-peer-loc", 1)
	registerA1Peer(t, s, home, peer)
	if err := os.Rename(peer.RepoPath, peer.RepoPath+"-unreachable"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Rename(peer.RepoPath+"-unreachable", peer.RepoPath); err != nil {
			t.Errorf("restore peer repository: %v", err)
		}
	})
	// Current population degradation permits a proven historical locator;
	// it does not permit a failed historical commit or blob proof.
	result, err := s.QueryQ10(ctx, Q10Request{Product: "amendment-product", KnowledgeID: "CD-0001", IncludeAmendmentContext: true, AmendmentContextAllowDegraded: true})
	if err != nil {
		t.Fatalf("explicit degraded opt-in fails before producing historical locator and current omissions: %v", err)
	}
	if result.Result.CurrentAmendmentContext == nil || result.Result.CurrentAmendmentContext.Authority == "authoritative" {
		t.Fatalf("degraded context missing: %+v", result)
	}
}

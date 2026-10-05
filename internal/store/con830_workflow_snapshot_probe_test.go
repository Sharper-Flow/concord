package store

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

// A projection refresh between pool verification and BeginTx must not pair
// the old verified source watermark with new relation rows as authoritative.
func TestCoordinatorCON830WorkflowSnapshotDrift(t *testing.T) {
	ctx := context.Background()
	s, home := refinementTestStore(t)
	defer s.Close()
	const workID = "coordinator-amendment-snapshot"
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1);
		INSERT INTO work_items(id,kind,title,lifecycle,priority,version,created_at,updated_at) VALUES(?,'task','Snapshot interleaving','needed',1,1,'now','now');
		INSERT INTO work_projects(work_id,project_id,role) VALUES(?, 'refinement-project','primary');
		DELETE FROM fold_guard`, workID, workID); err != nil {
		t.Fatal(err)
	}
	proof := verifyWorkflowLawContextSources(ctx, s.DatabaseForTesting(), workID)
	if proof == nil || proof.verification.degraded || len(proof.verification.watermarks) != 1 {
		t.Fatalf("initial verification must be authoritative: %+v", proof)
	}
	oldCommit := proof.verification.watermarks[0].Watermark
	writeKnowledgeFile(t, home.RepoPath, "README.md", "next verified projection fixture")
	newCommit := commitKnowledgeRepo(t, home.RepoPath, "next projection")
	if newCommit == oldCommit {
		t.Fatal("fixture did not advance source HEAD")
	}
	newDigest := seedRefinementDigest(t, home, newCommit)
	refresh, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer refresh.Rollback()
	if _, err := refresh.ExecContext(ctx, `INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		`UPDATE law_relations SET scanned_commit_oid=? WHERE home_project_id=? AND home_locator_id=?`,
		`UPDATE law_subjects SET scanned_commit_oid=? WHERE home_project_id=? AND home_locator_id=?`,
	} {
		if _, err := refresh.ExecContext(ctx, query, newCommit, home.HomeProjectID, home.HomeLocatorID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := refresh.ExecContext(ctx, `UPDATE law_subjects SET content_hash=? WHERE home_project_id=? AND home_locator_id=? AND law_id='CD-0054'`, "sha256:"+strings.Repeat("b", 64), home.HomeProjectID, home.HomeLocatorID); err != nil {
		t.Fatal(err)
	}
	if _, err := refresh.ExecContext(ctx, `UPDATE knowledge_index_watermark SET scanned_commit_oid=?,scanned_content_digest=? WHERE home_project_id=? AND home_locator_id=?`, newCommit, newDigest, home.HomeProjectID, home.HomeLocatorID); err != nil {
		t.Fatal(err)
	}
	if _, err := refresh.ExecContext(ctx, `DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	if err := refresh.Commit(); err != nil {
		t.Fatal(err)
	}
	tx, err := s.DatabaseForTesting().BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	page, err := queryKnowledgeRefinementContext(ctx, tx, KnowledgeRefinementContextRequest{}, proof.sources, []string{"CD-0017"}, refinementContextMaxLimit, proof.verification)
	if err != nil {
		return // Refusal preserves the proof boundary.
	}
	if page.Authority != "authoritative" {
		return // Explicitly incomplete context cannot claim verified coverage.
	}
	for _, edge := range page.Edges {
		if edge.ScannedCommitOID != oldCommit {
			t.Fatalf("authoritative graph joins pre-transaction proof %s to refreshed relation %s (endpoint %s); snapshot drift must refuse or degrade", oldCommit, edge.ScannedCommitOID, edge.EndpointLawID)
		}
	}
}

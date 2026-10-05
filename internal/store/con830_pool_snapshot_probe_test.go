package store

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

// The Q10 pool path verifies sources on the pool, then reads inside one
// read transaction. A projection refresh that commits in that window must
// not pair the verified source proof with the refreshed relations: the
// transaction's opening watermark check refuses the drift. This is the
// deterministic pool-path reproduction of the interleaving the coordinator
// drove mid-core (obs:c2e3626761586ce5); the core no longer accepts a
// nontransactional queryer, so the refresh lands between verification and
// BeginTx, exactly where a pool caller cannot hold a snapshot open.
func TestCoordinatorCON830PoolSnapshotDriftRefuses(t *testing.T) {
	ctx := context.Background()
	s, home := refinementTestStore(t)
	defer s.Close()
	db := s.DatabaseForTesting()
	oldCommit, authority, err := validateKnowledgeHomeForQueryCore(ctx, db, home, false, "PM1.Q10.amendment_context")
	if err != nil || authority != "authoritative" {
		t.Fatalf("verify initial source: commit=%s authority=%s err=%v", oldCommit, authority, err)
	}
	proof := refinementSourceVerification{
		scanned:    []string{home.HomeProjectID + "/" + home.HomeLocatorID + "@" + oldCommit},
		watermarks: []KnowledgeSourceWatermark{{ProjectID: home.HomeProjectID, LocatorID: home.HomeLocatorID, Watermark: oldCommit, Authority: authority}},
	}
	writeKnowledgeFile(t, home.RepoPath, "README.md", "next projection fixture")
	newCommit := commitKnowledgeRepo(t, home.RepoPath, "next projection")
	newDigest := seedRefinementDigest(t, home, newCommit)
	refresh, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := refresh.ExecContext(ctx, `INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		refresh.Rollback()
		t.Fatal(err)
	}
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`DELETE FROM law_relations WHERE home_project_id=? AND home_locator_id=?`, []any{home.HomeProjectID, home.HomeLocatorID}},
		{`UPDATE law_subjects SET scanned_commit_oid=? WHERE home_project_id=? AND home_locator_id=?`, []any{newCommit, home.HomeProjectID, home.HomeLocatorID}},
		{`UPDATE knowledge_index_watermark SET scanned_commit_oid=?,scanned_content_digest=? WHERE home_project_id=? AND home_locator_id=?`, []any{newCommit, newDigest, home.HomeProjectID, home.HomeLocatorID}},
	} {
		if _, err := refresh.ExecContext(ctx, statement.query, statement.args...); err != nil {
			refresh.Rollback()
			t.Fatal(err)
		}
	}
	if _, err := refresh.ExecContext(ctx, `DELETE FROM fold_guard`); err != nil {
		refresh.Rollback()
		t.Fatal(err)
	}
	if err := refresh.Commit(); err != nil {
		t.Fatal(err)
	}
	if newCommit == oldCommit {
		t.Fatal("fixture did not advance the projection identity")
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	page, err := queryKnowledgeRefinementContext(ctx, tx, KnowledgeRefinementContextRequest{}, []KnowledgeHome{home}, []string{"CD-0017"}, refinementContextMaxLimit, proof)
	if err == nil {
		if page.ResultMeta.Authority == "authoritative" {
			t.Fatalf("authoritative page from projection %s carries verified source proof %s; the read transaction must refuse the drift", newCommit, oldCommit)
		}
		joined := strings.Join(page.ResultMeta.Omissions, "\n")
		if !strings.Contains(joined, "source_snapshot_drift:"+home.HomeProjectID+"/"+home.HomeLocatorID+":"+oldCommit+"->"+newCommit) {
			t.Fatalf("degraded page omitted the named drift: %v", page.ResultMeta.Omissions)
		}
		return
	}
	failure, ok := err.(*Failure)
	if !ok || failure.Kind != KindStaleContext {
		t.Fatalf("drift error = %v, want a typed stale-context refusal", err)
	}
}

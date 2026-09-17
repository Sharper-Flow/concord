package store

import (
	"context"
	"testing"
)

// A watermark written by a newer binary must make this binary refuse the
// rebuild instead of overwriting the projection. The refusal is the
// knowledge-index form of the schema manifest's rule: a database written by
// a newer binary is not this binary's to rewrite.
func TestStaleBinaryRefusesToRebuildNewerProjectionWatermark(t *testing.T) {
	ctx := context.Background()
	repo := initKnowledgeRepo(t)
	writeKnowledgeFile(t, repo, "docs/work/2026-09-13-newer-projection.md", canonicalWorkNote("work-newer", "2026-09-13T00:00:00Z"))
	commitKnowledgeRepo(t, repo, "projected content")
	s := openTemp(t)
	home := KnowledgeHome{HomeProjectID: "proj-stale", HomeLocatorID: "loc-stale", RepoPath: repo, HeadRef: "HEAD"}
	authorizeKnowledgeLocator(t, s, home)
	if err := s.RebuildKnowledgeIndex(ctx, home); err != nil {
		t.Fatal(err)
	}
	db := s.DatabaseForTesting()
	var archivedBefore int
	if err := db.QueryRow(`SELECT COUNT(*) FROM archived_work WHERE home_project_id = ? AND home_locator_id = ?`, home.HomeProjectID, home.HomeLocatorID).Scan(&archivedBefore); err != nil {
		t.Fatal(err)
	}
	var scannedBefore string
	if err := db.QueryRow(`SELECT scanned_commit_oid FROM knowledge_index_watermark WHERE home_project_id = ? AND home_locator_id = ? AND head_ref = ?`, home.HomeProjectID, home.HomeLocatorID, home.HeadRef).Scan(&scannedBefore); err != nil {
		t.Fatal(err)
	}
	// Simulate the row a newer binary wrote: a projection version above this
	// binary's and a digest only its algorithm produces. The current binary
	// must see the row as not its own and refuse, not rebuild over it.
	if _, err := db.Exec(`INSERT INTO fold_guard(active) VALUES (1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE knowledge_index_watermark SET projection_version = ?, scanned_content_digest = 'sha256:future-algorithm'`, knowledgeProjectionVersion+1); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	assertFailureKind(t, s.EnsureKnowledgeIndexFresh(ctx, home), KindSchemaUnsupported)
	assertFailureKind(t, s.RebuildKnowledgeIndex(ctx, home), KindSchemaUnsupported)
	var scannedAfter, digestAfter string
	var versionAfter int
	if err := db.QueryRow(`SELECT scanned_commit_oid, scanned_content_digest, projection_version FROM knowledge_index_watermark WHERE home_project_id = ? AND home_locator_id = ? AND head_ref = ?`, home.HomeProjectID, home.HomeLocatorID, home.HeadRef).Scan(&scannedAfter, &digestAfter, &versionAfter); err != nil {
		t.Fatal(err)
	}
	if scannedAfter != scannedBefore || digestAfter != "sha256:future-algorithm" || versionAfter != knowledgeProjectionVersion+1 {
		t.Fatalf("refused rebuild rewrote the watermark: scanned=%q digest=%q version=%d", scannedAfter, digestAfter, versionAfter)
	}
	var archivedAfter int
	if err := db.QueryRow(`SELECT COUNT(*) FROM archived_work WHERE home_project_id = ? AND home_locator_id = ?`, home.HomeProjectID, home.HomeLocatorID).Scan(&archivedAfter); err != nil {
		t.Fatal(err)
	}
	if archivedAfter != archivedBefore {
		t.Fatalf("refused rebuild changed the projection: before=%d after=%d", archivedBefore, archivedAfter)
	}
}

// An older watermark still rebuilds: the version comparison only protects a
// newer binary's row, so upgrades keep the demand-driven rebuild path.
func TestOlderProjectionWatermarkStillRebuilds(t *testing.T) {
	ctx := context.Background()
	repo := initKnowledgeRepo(t)
	writeKnowledgeFile(t, repo, "docs/work/2026-09-13-older-projection.md", canonicalWorkNote("work-older", "2026-09-13T00:00:00Z"))
	commitKnowledgeRepo(t, repo, "projected content")
	s := openTemp(t)
	home := KnowledgeHome{HomeProjectID: "proj-older", HomeLocatorID: "loc-older", RepoPath: repo, HeadRef: "HEAD"}
	authorizeKnowledgeLocator(t, s, home)
	if err := s.RebuildKnowledgeIndex(ctx, home); err != nil {
		t.Fatal(err)
	}
	db := s.DatabaseForTesting()
	if _, err := db.Exec(`INSERT INTO fold_guard(active) VALUES (1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE knowledge_index_watermark SET projection_version = ?, scanned_content_digest = ''`, knowledgeProjectionVersion-1); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	if err := s.EnsureKnowledgeIndexFresh(ctx, home); err != nil {
		t.Fatalf("older watermark must still rebuild: %v", err)
	}
	var versionAfter int
	if err := db.QueryRow(`SELECT projection_version FROM knowledge_index_watermark WHERE home_project_id = ? AND home_locator_id = ? AND head_ref = ?`, home.HomeProjectID, home.HomeLocatorID, home.HeadRef).Scan(&versionAfter); err != nil {
		t.Fatal(err)
	}
	if versionAfter != knowledgeProjectionVersion {
		t.Fatalf("rebuild stamped version %d, want %d", versionAfter, knowledgeProjectionVersion)
	}
}

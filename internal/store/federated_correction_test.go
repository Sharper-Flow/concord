package store

import (
	"context"
	"testing"
)

// A superseded source law may name its successor in another registered
// source through the qualified project_id/law_id form (CD-0200
// source-qualified identity): the external successor is not copied into the
// declaring source, so the source's rebuild projects the qualified
// declaration instead of refusing it as an undeclared successor.
func TestCrossSourceSuccessorProjectsWhenDeclaredQualified(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, home, source := seedCrossSourceBoundaryProduct(t, "external-successor")
	shards, err := readKnowledgeShardsWorkingTree(source.RepoPath)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := composeKnowledgeManifest(shards, manifestRegisteredSourceRole)
	if err != nil {
		t.Fatal(err)
	}
	manifest.Records[0].Status = "superseded"
	manifest.Records[0].Successor = home.HomeProjectID + "/HOME-LAW"
	writeSourceManifest(t, source.RepoPath, manifest)
	commitKnowledgeRepo(t, source.RepoPath, "qualified external successor")
	if err := s.RebuildKnowledgeIndex(ctx, source); err != nil {
		t.Fatalf("external successor cannot project without a forbidden local copy: %v", err)
	}
	var successor string
	if err := s.DatabaseForTesting().QueryRow(`SELECT successor_work_id FROM archived_work WHERE home_project_id=? AND home_locator_id=? AND id='SRC-LAW'`, source.HomeProjectID, source.HomeLocatorID).Scan(&successor); err != nil {
		t.Fatal(err)
	}
	if successor != home.HomeProjectID+"/HOME-LAW" {
		t.Fatalf("the qualified successor declaration did not project: %q", successor)
	}
}

// CD-0015's exact supersession agreement is source-qualified: a shared-home
// law cannot capture the agreement of a superseded source law whose own
// successor declaration names a distinct local law holding the same bare ID.
// The declaring home's rebuild refuses, and the consequential boundary
// refuses with it.
func TestCrossSourceSupersedesRefusesBareLocalSuccessorCapture(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, home, source := seedCrossSourceBoundaryProduct(t, "successor-collision")
	shards, err := readKnowledgeShardsWorkingTree(source.RepoPath)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := composeKnowledgeManifest(shards, manifestRegisteredSourceRole)
	if err != nil {
		t.Fatal(err)
	}
	local := manifest.Records[0]
	local.ID = "HOME-LAW"
	local.Path = ".concord/docs/decisions/CD-0999-local-next.md"
	local.Title = "Local successor, distinct from shared law"
	local.LawRelations = []KnowledgeRelation{{Kind: "supersedes", TargetID: "SRC-LAW"}}
	writeKnowledgeFile(t, source.RepoPath, local.Path, "durable SRC-LAW blob\n")
	manifest.Records[0].Status = "superseded"
	manifest.Records[0].Successor = "HOME-LAW"
	manifest.Records = append(manifest.Records, local)
	writeSourceManifest(t, source.RepoPath, manifest)
	commitKnowledgeRepo(t, source.RepoPath, "local supersession")
	if err := s.RebuildKnowledgeIndex(ctx, source); err != nil {
		t.Fatal(err)
	}
	shards, err = readKnowledgeShardsWorkingTree(home.RepoPath)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err = composeKnowledgeManifest(shards, manifestSharedHomeRole)
	if err != nil {
		t.Fatal(err)
	}
	manifest.Records[0].LawRelations = []KnowledgeRelation{{Kind: "supersedes", TargetID: "SRC-LAW", SourceProjectID: source.HomeProjectID}}
	writeManifestShards(t, home.RepoPath, manifest)
	commitKnowledgeRepo(t, home.RepoPath, "incorrect external supersession")
	if err := s.RebuildKnowledgeIndex(ctx, home); err != nil {
		t.Logf("correct rebuild refusal: %v", err)
		return
	}
	if err := s.CheckMandatedLawsAtHome(ctx, home.HomeProjectID, home.HomeLocatorID, []string{home.HomeProjectID + "/HOME-LAW"}, nil, false); err != nil {
		t.Logf("correct boundary refusal: %v", err)
		return
	}
	t.Fatal("rebuild and consequential boundary accepted shared HOME-LAW as successor even though SRC-LAW names the distinct local HOME-LAW")
}

// CD-0200 D5: a declared cross-source relation validates over the verified
// source set, so a declaring home's rebuild refuses when the target source's
// watermark no longer verifies against its git head — a target law removed
// at the peer's head without a rebuild cannot admit a new edge over rows the
// target no longer authors.
func TestRebuildRefusesCrossSourceRelationOverStalePeer(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, home, source := seedCrossSourceBoundaryProduct(t, "stale-peer")
	shards, err := readKnowledgeShardsWorkingTree(source.RepoPath)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := composeKnowledgeManifest(shards, manifestRegisteredSourceRole)
	if err != nil {
		t.Fatal(err)
	}
	manifest.Records = nil
	writeSourceManifest(t, source.RepoPath, manifest)
	commitKnowledgeRepo(t, source.RepoPath, "remove source law without rebuilding")
	if err := s.CheckMandatedLawsAtHome(ctx, home.HomeProjectID, home.HomeLocatorID, []string{"SRC-LAW"}, nil, false); err == nil {
		t.Fatal("stale setup unexpectedly authoritative")
	}
	shards, err = readKnowledgeShardsWorkingTree(home.RepoPath)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err = composeKnowledgeManifest(shards, manifestSharedHomeRole)
	if err != nil {
		t.Fatal(err)
	}
	manifest.Records[0].LawRelations = []KnowledgeRelation{{Kind: "refines", TargetID: "SRC-LAW", SourceProjectID: source.HomeProjectID}}
	writeManifestShards(t, home.RepoPath, manifest)
	commitKnowledgeRepo(t, home.RepoPath, "refine missing source law")
	if err := s.RebuildKnowledgeIndex(ctx, home); err == nil {
		t.Fatal("rebuild accepted a cross-source relation against stale target rows after target law disappeared from Git HEAD")
	}
}

// A source-set proof binds each registered source's resolved canonical
// location with its identity: a locator update between the verification and
// the transaction moves the canonical authority while every verified
// revision still matches the old repository's partition, so the
// transaction-scoped mandated-law boundary refuses instead of answering from
// the previous repository's projected law.
func TestSourceSetProofRefusesLocatorMove(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, home, source := seedCrossSourceBoundaryProduct(t, "locator-move")
	seedKnowledgeWork(t, s, "locator-move-work", "Move source work")
	if _, err := s.db.Exec(`INSERT INTO fold_guard(active) VALUES(1); UPDATE work_projects SET project_id=? WHERE work_id='locator-move-work' AND role='primary'; DELETE FROM fold_guard`, home.HomeProjectID); err != nil {
		t.Fatal(err)
	}
	verified, err := s.EstablishKnowledgeSourceSetProof(ctx, "locator-move-work")
	if err != nil {
		t.Fatal(err)
	}
	replacement := initKnowledgeRepo(t)
	seedFederatedSource(t, replacement, "OTHER-LAW", ".concord/docs/decisions/CD-0908-other.md", "Different corpus")
	commitKnowledgeRepo(t, replacement, "replacement canonical authority")
	var version int64
	if err := s.db.QueryRow(`SELECT version FROM projects WHERE id=?`, source.HomeProjectID).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateProjectLocator(ctx, source.HomeProjectID, ProjectLocator{ID: source.HomeLocatorID, Kind: LocatorCanonicalPath, Value: replacement}, version); err != nil {
		t.Fatal(err)
	}
	// The public entry sees the new canonical authority, proving the move
	// really took effect.
	if err := s.CheckMandatedLawsAtHome(ctx, home.HomeProjectID, home.HomeLocatorID, []string{"SRC-LAW"}, nil, false); err == nil {
		t.Fatal("new canonical authority unexpectedly contains old projected law")
	}
	tx, err := s.db.BeginTx(verified, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := checkMandatedLawsTxAtHome(verified, tx, home.HomeProjectID, home.HomeLocatorID, []string{"SRC-LAW"}, nil, false); err == nil {
		t.Fatal("transaction admitted old law using the old repository after project.locator_updated changed the canonical source path")
	}
}

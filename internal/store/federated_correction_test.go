package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A superseded law's successor must live in the declaring manifest: the
// qualified project_id/law_id successor form is refused fail closed
// (CD-0200), because the cross-source supersedes edge it implies has no
// admitted declaration. The successor is not copied into the declaring
// source either — the refusal, not a projection, answers.
func TestCrossSourceSuccessorRefusesQualifiedForm(t *testing.T) {
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
	err = s.RebuildKnowledgeIndex(ctx, source)
	var failure *Failure
	if !errors.As(err, &failure) || !strings.Contains(failure.Detail, "supersede within the declaring source or amend through the shared home") {
		t.Fatalf("qualified external successor rebuild error = %v, want the fail-closed supersedes refusal", err)
	}
}

// Cross-source supersedes is refused fail closed even when the declaring
// source's manifest carries a valid local supersession: the edge itself has
// no admitted declaration, so the shared home cannot capture a source law's
// supersession whatever the target's own successor declaration says
// (CD-0200).
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
	err = s.RebuildKnowledgeIndex(ctx, home)
	var failure *Failure
	if !errors.As(err, &failure) || !strings.Contains(failure.Detail, "supersede within the declaring source or amend through the shared home") {
		t.Fatalf("cross-source supersedes rebuild error = %v, want the fail-closed refusal", err)
	}
	if err := s.CheckMandatedLawsAtHome(ctx, home.HomeProjectID, home.HomeLocatorID, []string{home.HomeProjectID + "/HOME-LAW"}, nil, false); err == nil {
		t.Fatal("the mandated-law boundary answered for a law whose manifest declares a refused cross-source supersedes edge")
	}
}

// A shared-home rebuild resolves an unprojected peer's relation endpoint
// from the peer's verified git head, and verification means the manifest's
// declaration plus its blob proof: the named law blob must exist at that
// head and match its authored sha256 (CD-0200 verified-Git reconstruction),
// exactly as the peer's own rebuild verifies every record. An absent,
// mutated, or undeclared law refuses the admission instead of entering the
// rebuild as verified Git evidence.
func TestPeerEndpointBlobProofAtUnprojectedGitHead(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, mode := range []string{"valid", "missing-blob", "hash-mismatch", "missing-manifest"} {
		t.Run(mode, func(t *testing.T) {
			_, home, source := seedCrossSourceBoundaryProduct(t, "peer-proof")
			shards, err := readKnowledgeShardsWorkingTree(home.RepoPath)
			if err != nil {
				t.Fatal(err)
			}
			manifest, err := composeKnowledgeManifest(shards, manifestSharedHomeRole)
			if err != nil {
				t.Fatal(err)
			}
			manifest.Records[0].LawRelations = []KnowledgeRelation{{Kind: "refines", TargetID: "SRC-LAW", SourceProjectID: source.HomeProjectID}}
			writeManifestShards(t, home.RepoPath, manifest)
			commitKnowledgeRepo(t, home.RepoPath, "reference peer law")
			switch mode {
			case "missing-blob":
				if err := os.Remove(filepath.Join(source.RepoPath, ".concord/docs/decisions/CD-0902-src.md")); err != nil {
					t.Fatal(err)
				}
			case "hash-mismatch":
				writeKnowledgeFile(t, source.RepoPath, ".concord/docs/decisions/CD-0902-src.md", "different body without updated manifest hash\n")
			case "missing-manifest":
				if err := os.RemoveAll(filepath.Join(source.RepoPath, ".concord/docs/knowledge")); err != nil {
					t.Fatal(err)
				}
			}
			if mode != "valid" {
				commitKnowledgeRepo(t, source.RepoPath, "invalidate peer evidence")
			}
			// A clean store leaves both homes unprojected, so the shared
			// home's rebuild resolves the peer endpoint from the peer's git
			// head rather than from projected rows.
			restored := openTemp(t)
			authorizeKnowledgeProductHome(t, restored, "peer-proof-product", home, home.HomeProjectID, source.HomeProjectID)
			authorizeSourceLocator(t, restored, source)
			if _, err := restored.RegisterProductKnowledgeSource(ctx, ProductKnowledgeSourceRegistration{ProductID: "peer-proof-product", ProjectID: source.HomeProjectID, LocatorID: source.HomeLocatorID, ExpectedVersion: 1, Reason: "restore sources"}); err != nil {
				t.Fatal(err)
			}
			err = restored.RebuildKnowledgeIndex(ctx, home)
			if mode == "valid" {
				if err != nil {
					t.Fatalf("valid peer law refused: %v", err)
				}
				return
			}
			var failure *Failure
			if !errors.As(err, &failure) {
				t.Fatalf("shared-home rebuild error = %v, want the typed peer-endpoint refusal", err)
			}
			wantDetail := map[string]string{
				"missing-blob":     "manifest record blob is missing or not regular",
				"hash-mismatch":    "manifest record hash does not match blob",
				"missing-manifest": "no knowledge manifest",
			}[mode]
			if !strings.Contains(failure.Detail, wantDetail) {
				t.Fatalf("shared-home rebuild refused with %q, want it to name %q", failure.Detail, wantDetail)
			}
			if mode == "missing-blob" || mode == "hash-mismatch" {
				peerErr := restored.RebuildKnowledgeIndex(ctx, source)
				if peerErr == nil {
					t.Fatal("peer rebuild unexpectedly admitted invalid law blob")
				}
				if !strings.Contains(peerErr.Error(), "manifest record") {
					t.Fatalf("unexpected peer failure: %v", peerErr)
				}
			}
		})
	}
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

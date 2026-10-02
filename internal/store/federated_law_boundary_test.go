package store

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// A law_modifies reference resolves through the same source-qualified
// federated identity as the mandated-law boundary (CD-0200 D6): a derived
// law a registered source holds is revisable in-contract by its bare ID and
// by its project_id/law_id form, and the tier reads from the source's own
// projection.
func TestDerivedSourceLawResolvesForLawModifies(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, home, source := seedCrossSourceBoundaryProduct(t, "derived-modify")
	shards, err := readKnowledgeShardsWorkingTree(source.RepoPath)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := composeKnowledgeManifest(shards, manifestRegisteredSourceRole)
	if err != nil {
		t.Fatal(err)
	}
	manifest.Records[0].Authority = KnowledgeAuthority{Tier: "derived"}
	writeSourceManifest(t, source.RepoPath, manifest)
	commitKnowledgeRepo(t, source.RepoPath, "derived source law")
	if err := s.RebuildKnowledgeIndex(ctx, source); err != nil {
		t.Fatal(err)
	}
	seedKnowledgeWork(t, s, "derived-modify-work", "Derived source work")
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1); UPDATE work_projects SET project_id=? WHERE work_id='derived-modify-work' AND role='primary'; DELETE FROM fold_guard`, home.HomeProjectID); err != nil {
		t.Fatal(err)
	}
	for _, reference := range []string{"SRC-LAW", source.HomeProjectID + "/SRC-LAW"} {
		if err := s.CheckMandatedLawsAtHome(ctx, home.HomeProjectID, home.HomeLocatorID, []string{reference}, []string{reference}, true); err != nil {
			t.Fatal(err)
		}
		if err := validateDerivedLawModification(ctx, s.db, "derived-modify-work", []string{reference}); err != nil {
			t.Errorf("accepted derived source law %s refused by contract product guard: %v", reference, err)
		}
	}
}

// Cross-source supersedes is refused fail closed (CD-0200): whatever the
// target law's own state, the declaring rebuild refuses the edge, so no
// agreement check ever admits a supersession between sources.
func TestCrossSourceSupersedesRefusesFailClosed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, home, source := seedCrossSourceBoundaryProduct(t, "cross-supersedes")
	shards, err := readKnowledgeShardsWorkingTree(home.RepoPath)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := composeKnowledgeManifest(shards, manifestSharedHomeRole)
	if err != nil {
		t.Fatal(err)
	}
	manifest.Records[0].LawRelations = []KnowledgeRelation{{Kind: "supersedes", TargetID: "SRC-LAW", SourceProjectID: source.HomeProjectID}}
	writeManifestShards(t, home.RepoPath, manifest)
	commitKnowledgeRepo(t, home.RepoPath, "inconsistent supersession")
	err = s.RebuildKnowledgeIndex(ctx, home)
	var failure *Failure
	if !errors.As(err, &failure) || !strings.Contains(failure.Detail, "supersede within the declaring source or amend through the shared home") {
		t.Fatalf("cross-source supersedes rebuild error = %v, want the fail-closed refusal", err)
	}
}

// The federated law graph is acyclic over source-qualified nodes (CD-0015,
// CD-0200 D5): two sources declaring A refines B and B refines A refuse the
// second rebuild, and a consequential law boundary refuses any persisted
// cycle instead of answering from it.
func TestCrossSourceHierarchicalCycleRefusesRebuildAndBoundary(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, home, a := seedCrossSourceBoundaryProduct(t, "cross-cycle")
	b := KnowledgeHome{HomeProjectID: "cross-cycle-b", HomeLocatorID: "cross-cycle-b-loc", RepoPath: initKnowledgeRepo(t), HeadRef: "HEAD"}
	seedFederatedSource(t, b.RepoPath, "B-LAW", ".concord/docs/decisions/CD-0950-b.md", "B law")
	commitKnowledgeRepo(t, b.RepoPath, "B law")
	authorizeKnowledgeProductHome(t, s, "cross-cycle-product", home, b.HomeProjectID)
	authorizeSourceLocator(t, s, b)
	if _, err := s.RegisterProductKnowledgeSource(ctx, ProductKnowledgeSourceRegistration{ProductID: "cross-cycle-product", ProjectID: b.HomeProjectID, LocatorID: b.HomeLocatorID, ExpectedVersion: 2, Reason: "cycle test"}); err != nil {
		t.Fatal(err)
	}
	if err := s.RebuildKnowledgeIndex(ctx, b); err != nil {
		t.Fatal(err)
	}
	writeSourceRelations(t, a.RepoPath, []KnowledgeRelation{{Kind: "refines", TargetID: "B-LAW", SourceProjectID: b.HomeProjectID}})
	commitKnowledgeRepo(t, a.RepoPath, "A refines B")
	if err := s.RebuildKnowledgeIndex(ctx, a); err != nil {
		t.Fatal(err)
	}
	shards, err := readKnowledgeShardsWorkingTree(b.RepoPath)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := composeKnowledgeManifest(shards, manifestRegisteredSourceRole)
	if err != nil {
		t.Fatal(err)
	}
	manifest.Records[0].LawRelations = []KnowledgeRelation{{Kind: "refines", TargetID: "SRC-LAW", SourceProjectID: a.HomeProjectID}}
	writeSourceManifest(t, b.RepoPath, manifest)
	commitKnowledgeRepo(t, b.RepoPath, "B refines A")
	if err := s.RebuildKnowledgeIndex(ctx, b); err == nil {
		if err := s.CheckMandatedLawsAtHome(ctx, home.HomeProjectID, home.HomeLocatorID, []string{"SRC-LAW", "B-LAW"}, nil, false); err == nil {
			t.Error("rebuild and consequential law boundary accepted cross-source A -> B -> A refinement cycle")
		}
	}
}

// The source-set proof keeps PM1 §3.3's two revisions apart: a commit that
// touches no projected content before the verification is part of the
// verified head, so the transaction-scoped mandated-law boundary admits the
// content-fresh projection instead of refusing it as a moved source.
func TestSourceSetProofSeparatesVerifiedHeadFromScannedRevision(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, home, source := seedCrossSourceBoundaryProduct(t, "proof-code-only")
	seedKnowledgeWork(t, s, "proof-code-only-work", "Code-only work")
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1); UPDATE work_projects SET project_id=? WHERE work_id='proof-code-only-work' AND role='primary'; DELETE FROM fold_guard`, home.HomeProjectID); err != nil {
		t.Fatal(err)
	}
	writeKnowledgeFile(t, source.RepoPath, "main.go", "package main\n")
	commitKnowledgeRepo(t, source.RepoPath, "code only")
	if err := s.CheckMandatedLawsAtHome(ctx, home.HomeProjectID, home.HomeLocatorID, []string{"SRC-LAW"}, nil, false); err != nil {
		t.Fatalf("read freshness positive control: %v", err)
	}
	verified, err := s.EstablishKnowledgeSourceSetProof(ctx, "proof-code-only-work")
	if err != nil {
		t.Fatal(err)
	}
	tx, err := s.db.BeginTx(verified, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := checkMandatedLawsTxAtHome(verified, tx, home.HomeProjectID, home.HomeLocatorID, []string{"SRC-LAW"}, nil, false); err != nil {
		t.Errorf("fresh verified source refused after a code-only commit before proof establishment: %v", err)
	}
}

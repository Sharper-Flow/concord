package store

import (
	"context"
	"strings"
	"testing"
)

// seedCrossSourceBoundaryProduct is the federated fixture the consequential
// boundary tests share: one designated shared-law home, one registered
// source, each rebuilt, with the resolved shared home returned.
func seedCrossSourceBoundaryProduct(t *testing.T, label string) (*Store, KnowledgeHome, KnowledgeHome) {
	t.Helper()
	home := KnowledgeHome{HomeProjectID: label + "-home", HomeLocatorID: label + "-home-loc", HeadRef: "HEAD"}
	source := KnowledgeHome{HomeProjectID: label + "-src", HomeLocatorID: label + "-src-loc", HeadRef: "HEAD"}
	s, source := seedFederatedProduct(t, label+"-product", home, source, "SRC-LAW", ".concord/docs/decisions/CD-0902-src.md", "Source law")
	resolved, err := s.ResolveKnowledgeQueryHome(context.Background(), label+"-product", "", KnowledgeHome{}, "test")
	if err != nil {
		t.Fatal(err)
	}
	return s, resolved, source
}

// The transaction-scoped mandated-law boundary refuses over a registered
// source set that carries no proof: the completion and approval gates run it
// inside their transaction, and a git probe can never run there (CD-0195
// D2). The public read entry refuses a stale source on its own verification;
// the transaction entry must refuse the same stale rows instead of trusting
// them, and it must admit a verified fresh set.
func TestTransactionalLawBoundaryRequiresSourceSetProof(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, home, source := seedCrossSourceBoundaryProduct(t, "proof-stale")

	// Positive control: a fresh source set with an established proof passes
	// the transaction-scoped check.
	seedKnowledgeWork(t, s, "proof-stale-work", "Proof stale work")
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1); UPDATE work_projects SET project_id=? WHERE work_id='proof-stale-work' AND role='primary'; DELETE FROM fold_guard`, home.HomeProjectID); err != nil {
		t.Fatal(err)
	}
	verifiedCtx, err := s.EstablishKnowledgeSourceSetProof(ctx, "proof-stale-work")
	if err != nil {
		t.Fatal(err)
	}
	tx, err := s.DatabaseForTesting().BeginTx(verifiedCtx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := checkMandatedLawsTxAtHome(verifiedCtx, tx, home.HomeProjectID, home.HomeLocatorID, []string{"HOME-LAW", "SRC-LAW"}, nil, false); err != nil {
		tx.Rollback()
		t.Fatalf("verified source set refused inside the transaction: %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}

	// A committed cross-source conflict leaves the source stale: the rebuild
	// refuses, the public read entry refuses, and the transaction entry
	// refuses the stale rows it can no longer prove fresh.
	writeSourceRelations(t, source.RepoPath, []KnowledgeRelation{{Kind: "conflicts_with", TargetID: "HOME-LAW", SourceProjectID: home.HomeProjectID}})
	commitKnowledgeRepo(t, source.RepoPath, "cross-source conflict")
	if err := s.RebuildKnowledgeIndex(ctx, source); err == nil {
		t.Fatal("setup: conflict rebuild succeeded")
	}
	if err := s.CheckMandatedLawsAtHome(ctx, home.HomeProjectID, home.HomeLocatorID, []string{"HOME-LAW", "SRC-LAW"}, nil, false); err == nil {
		t.Fatal("setup: public read did not reject stale source")
	}
	tx, err = s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := checkMandatedLawsTxAtHome(ctx, tx, home.HomeProjectID, home.HomeLocatorID, []string{"HOME-LAW", "SRC-LAW"}, nil, false); err == nil {
		t.Fatal("transactional completion-law boundary accepted stale rows after a committed cross-source conflict")
	}
}

// A cross-source relation persists with its target's source identity, and
// every consequential boundary revalidates its endpoints over the verified
// current source set: removing the registered target source leaves the
// declaring law unresolved, and the boundary refuses instead of answering
// from a rebuild-time verdict a later removal stranded.
func TestCrossSourceRelationTargetRemovalRefusesBoundary(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, home, source := seedCrossSourceBoundaryProduct(t, "relation-remove")
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
	commitKnowledgeRepo(t, home.RepoPath, "cross-source refinement")
	if err := s.RebuildKnowledgeIndex(ctx, home); err != nil {
		t.Fatal(err)
	}
	var persisted int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM law_cross_source_relations WHERE home_project_id=? AND home_locator_id=? AND source_law_id='HOME-LAW' AND kind='refines' AND target_project_id=? AND target_law_id='SRC-LAW'`, home.HomeProjectID, home.HomeLocatorID, source.HomeProjectID).Scan(&persisted); err != nil {
		t.Fatal(err)
	}
	if persisted != 1 {
		t.Fatalf("cross-source refinement did not project with its target source: %d rows", persisted)
	}
	if _, err := s.RemoveProductKnowledgeSource(ctx, ProductKnowledgeSourceRegistration{ProductID: "relation-remove-product", ProjectID: source.HomeProjectID, LocatorID: source.HomeLocatorID, ExpectedVersion: 2, Reason: "remove target"}); err != nil {
		t.Fatal(err)
	}
	if err := s.CheckMandatedLawsAtHome(ctx, home.HomeProjectID, home.HomeLocatorID, []string{"HOME-LAW"}, nil, false); err == nil {
		t.Fatal("consequential boundary accepted HOME-LAW after its cross-source relation target left the source set")
	}
}

// A registered source keeps the shared home's mandatory Domain law rules at
// its own rebuild, against the registry the shared home projected: an
// accepted law with no authored home refuses, and an applicability set with
// no home refuses. A dangling applicability domain refuses as before.
func TestSourceRebuildRequiresDomainHomeAndApplicability(t *testing.T) {
	for _, mode := range []string{"missing-home", "applicability-without-home", "dangling-applicability"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			s, _, source := seedCrossSourceBoundaryProduct(t, "domain-"+mode)
			shards, err := readKnowledgeShardsWorkingTree(source.RepoPath)
			if err != nil {
				t.Fatal(err)
			}
			manifest, err := composeKnowledgeManifest(shards, manifestRegisteredSourceRole)
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "missing-home":
				manifest.Records[0].HomeDomainID = ""
				manifest.Records[0].homeDomainPresent = false
			case "applicability-without-home":
				manifest.Records[0].HomeDomainID = ""
				manifest.Records[0].homeDomainPresent = false
				manifest.Records[0].AppliesToDomainIDs = []string{"fixture-law"}
			case "dangling-applicability":
				manifest.Records[0].AppliesToDomainIDs = []string{"nonexistent-domain"}
			}
			writeSourceManifest(t, source.RepoPath, manifest)
			commitKnowledgeRepo(t, source.RepoPath, "invalid source Domain declaration")
			if err := s.RebuildKnowledgeIndex(ctx, source); err == nil {
				t.Fatalf("source rebuild accepted invalid Domain declaration: %s", mode)
			}
		})
	}
}

// A source-qualified mandate resolves its supersession through the parsed
// bare target ID — relation rows store bare IDs — and the recovery payload
// names the accepted successor with its source qualification.
func TestQualifiedSupersessionResolvesBareTargetAndQualifiesSuccessor(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, home, source := seedCrossSourceBoundaryProduct(t, "successor")
	shards, err := readKnowledgeShardsWorkingTree(source.RepoPath)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := composeKnowledgeManifest(shards, manifestRegisteredSourceRole)
	if err != nil {
		t.Fatal(err)
	}
	next := manifest.Records[0]
	next.ID = "SRC-NEXT"
	next.Path = ".concord/docs/decisions/CD-0903-next.md"
	next.Title = "Next source law"
	next.LawRelations = []KnowledgeRelation{{Kind: "supersedes", TargetID: "SRC-LAW"}}
	writeKnowledgeFile(t, source.RepoPath, next.Path, "durable SRC-LAW blob\n")
	manifest.Records[0].Status = "superseded"
	manifest.Records[0].Successor = "SRC-NEXT"
	manifest.Records = append(manifest.Records, next)
	writeSourceManifest(t, source.RepoPath, manifest)
	commitKnowledgeRepo(t, source.RepoPath, "supersede source law")
	if err := s.RebuildKnowledgeIndex(ctx, source); err != nil {
		t.Fatal(err)
	}
	bare, err := findStaleWorkflowLawRevision(ctx, s.db, home.HomeProjectID, home.HomeLocatorID, "successor-work", 1, []string{"SRC-LAW"})
	if err != nil || bare == nil {
		t.Fatalf("setup: bare successor = %+v, %v", bare, err)
	}
	qualified, err := findStaleWorkflowLawRevision(ctx, s.db, home.HomeProjectID, home.HomeLocatorID, "successor-work", 1, []string{source.HomeProjectID + "/SRC-LAW"})
	if err != nil || qualified == nil {
		t.Fatalf("qualified successor lookup failed: result=%+v err=%v", qualified, err)
	}
	if qualified.AcceptedSuccessorLawID != source.HomeProjectID+"/SRC-NEXT" {
		t.Fatalf("successor lost source qualification: %+v", qualified)
	}
}

// A bare-ID Q10 answer asserts uniqueness over the registered source set, so
// the set verifies first: a registered source that has never indexed holds an
// invisible duplicate, Q9 refuses, and Q10 refuses with it instead of
// reporting a unique authoritative locator over an unverified population.
func TestQ10RefusesBareIDOverUnverifiedSourceSet(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _, _ := seedCrossSourceBoundaryProduct(t, "q10-verify")
	repo := initKnowledgeRepo(t)
	duplicate := KnowledgeHome{HomeProjectID: "q10-verify-dup", HomeLocatorID: "q10-verify-dup-loc", RepoPath: repo, HeadRef: "HEAD"}
	seedFederatedSource(t, repo, "SRC-LAW", ".concord/docs/decisions/CD-0904-dup.md", "Duplicate source law")
	commitKnowledgeRepo(t, repo, "duplicate law")
	home, err := s.ResolveKnowledgeQueryHome(ctx, "q10-verify-product", "", KnowledgeHome{}, "test")
	if err != nil {
		t.Fatal(err)
	}
	authorizeKnowledgeProductHome(t, s, "q10-verify-product", home, duplicate.HomeProjectID)
	authorizeSourceLocator(t, s, duplicate)
	if _, err := s.RegisterProductKnowledgeSource(ctx, ProductKnowledgeSourceRegistration{ProductID: "q10-verify-product", ProjectID: duplicate.HomeProjectID, LocatorID: duplicate.HomeLocatorID, ExpectedVersion: 2, Reason: "register duplicate"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.QueryQ9(ctx, Q9Request{Product: "q10-verify-product"}); err == nil {
		t.Fatal("setup: Q9 accepted unindexed source")
	}
	result, err := s.QueryQ10(ctx, Q10Request{Product: "q10-verify-product", KnowledgeID: "SRC-LAW"})
	if err == nil && result.Status == "canonical" && result.Authority == "authoritative" {
		t.Fatal("Q10 reported a unique authoritative bare-ID locator while a registered unindexed source holds the same ID")
	}
}

// One source's rebuild clears only its own scope rows: the same work id can
// project in more than one registered source, and an unqualified work_id
// match erased the other source's scopes.
func TestRebuildKeepsOtherSourceScopeRows(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repoA := initKnowledgeRepo(t)
	repoB := initKnowledgeRepo(t)
	noteA := strings.Replace(canonicalWorkNote("scope-shared-work", "2026-09-21T00:00:00Z"), "product_ids: [prod-alpha]", "product_ids: [prod-scope]", 1)
	noteB := strings.Replace(canonicalWorkNote("scope-shared-work", "2026-09-21T00:00:00Z"), "product_ids: [prod-alpha]", "product_ids: [prod-scope]", 1)
	writeKnowledgeFile(t, repoA, ".concord/docs/work/2026-09-21-scope-a.md", noteA)
	writeKnowledgeFile(t, repoB, ".concord/docs/work/2026-09-21-scope-b.md", noteB)
	commitKnowledgeRepo(t, repoA, "home work note")
	commitKnowledgeRepo(t, repoB, "source work note")
	s := openTemp(t)
	home := KnowledgeHome{HomeProjectID: "scope-home", HomeLocatorID: "scope-home-loc", RepoPath: repoA, HeadRef: "HEAD"}
	source := KnowledgeHome{HomeProjectID: "scope-src", HomeLocatorID: "scope-src-loc", RepoPath: repoB, HeadRef: "HEAD"}
	authorizeKnowledgeProductHome(t, s, "scope-product", home, home.HomeProjectID, source.HomeProjectID)
	authorizeSourceLocator(t, s, source)
	if _, err := s.RegisterProductKnowledgeSource(ctx, ProductKnowledgeSourceRegistration{ProductID: "scope-product", ProjectID: source.HomeProjectID, LocatorID: source.HomeLocatorID, Reason: "scope fixture", ExpectedVersion: 1}); err != nil {
		t.Fatal(err)
	}
	// archived_work is the compaction archive: a work note projects only
	// when a compaction link names its ID, so both fixtures link theirs.
	for _, eventID := range []string{"scope-link-home", "scope-link-source"} {
		if _, err := s.DatabaseForTesting().Exec(`INSERT INTO domain_events(event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload) VALUES(?,'compaction_link.published','work_item','scope-shared-work','operator','2026-09-21T00:00:00Z',1,'{}')`, eventID); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.RebuildKnowledgeIndex(ctx, home); err != nil {
		t.Fatal(err)
	}
	if err := s.RebuildKnowledgeIndex(ctx, source); err != nil {
		t.Fatal(err)
	}
	countScopes := func(t *testing.T) int {
		t.Helper()
		var n int
		if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM archived_work_products WHERE work_id='scope-shared-work'`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if got := countScopes(t); got != 2 {
		t.Fatalf("setup: expected one scope row per source, got %d", got)
	}
	if err := s.RebuildKnowledgeIndex(ctx, home); err != nil {
		t.Fatal(err)
	}
	if got := countScopes(t); got != 2 {
		t.Fatalf("the home rebuild erased the registered source's scope rows: %d remain", got)
	}
}

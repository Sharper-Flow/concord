package store

import (
	"context"
	"os"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
)

// seedFederatedSource writes a registered-source manifest (no Domain
// registry) carrying one accepted decision, and commits it.
func seedFederatedSource(t *testing.T, repo, lawID, path, title string) {
	t.Helper()
	body := "durable " + lawID + " blob\n"
	writeKnowledgeFile(t, repo, path, body)
	sum := sha256.Sum256([]byte(body))
	manifest := KnowledgeManifest{
		SchemaVersion:  "1.2",
		SupportedKinds: []string{"decision"},
		IndexedKinds:   []string{"decision"},
		Records: []KnowledgeRecord{{
			ID: lawID, Kind: "decision", Path: path, Status: "accepted",
			Date: "2026-09-20T00:00:00Z", Title: title, Summary: "A source rule", Tags: []string{},
			Authority: KnowledgeAuthority{Tier: "legislated", LegislatedBy: "fixture-authority", ContractVersion: 1},
			Scopes:    homeScope(), HomeDomainID: "fixture-law", SHA256: "sha256:" + hex.EncodeToString(sum[:]),
		}},
	}
	writeSourceManifest(t, repo, manifest)
}

// marshalManifestWithoutRegistry encodes a source manifest without the
// domain_registry key: the zero-value field marshals with the key present,
// and the key's presence is what the source-role validation refuses.
func writeSourceManifest(t *testing.T, repo string, manifest KnowledgeManifest) {
	t.Helper()
	if err := os.Remove(filepath.Join(repo, filepath.FromSlash(knowledgeRegistryPath))); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	writeAggregateAsShards(t, repo, marshalManifestWithoutRegistry(t, manifest))
}

func marshalManifestWithoutRegistry(t *testing.T, manifest KnowledgeManifest) []byte {
	t.Helper()
	encoded, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	var head map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &head); err != nil {
		t.Fatal(err)
	}
	delete(head, "domain_registry")
	stripped, err := json.Marshal(head)
	if err != nil {
		t.Fatal(err)
	}
	return stripped
}

// authorizeSourceLocator inserts the Project and its canonical-path locator
// without the anchor Product home the shared fixture helper creates: a
// registered source must resolve to the registered-source role, never to a
// Product-home designation.
func authorizeSourceLocator(t *testing.T, s *Store, home KnowledgeHome) {
	t.Helper()
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := s.DatabaseForTesting().Exec(`DELETE FROM fold_guard`); err != nil {
			t.Errorf("remove fold guard: %v", err)
		}
	}()
	if _, err := s.DatabaseForTesting().Exec(`INSERT OR IGNORE INTO projects(id,display_name,version,created_at,updated_at) VALUES(?, ?, 1, 'now', 'now')`, home.HomeProjectID, home.HomeProjectID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT OR IGNORE INTO project_locators(locator_id,project_id,kind,locator_value,normalized_value,created_at,updated_at) VALUES(?, ?, 'canonical_path', ?, ?, 'now', 'now')`, home.HomeLocatorID, home.HomeProjectID, home.RepoPath, home.RepoPath); err != nil {
		t.Fatal(err)
	}
}

// seedFederatedProduct designates homeHome as the Product's shared-law home,
// rebuilds it, registers sourceHome as a member Project knowledge source,
// rebuilds the source, and returns the store plus the resolved source home
// with its repository path. The source repo holds one accepted decision
// named lawID at lawPath.
func seedFederatedProduct(t *testing.T, productID string, homeHome, sourceHome KnowledgeHome, lawID, lawPath, lawTitle string) (*Store, KnowledgeHome) {
	t.Helper()
	ctx := context.Background()
	repo := initKnowledgeRepo(t)
	homeHome.RepoPath = repo
	writeManifestFixture(t, repo, manifestFixture{
		ID: "HOME-LAW", Kind: "decision", Path: ".concord/docs/decisions/CD-0901-home-law.md",
		Status: "accepted", Date: "2026-09-20T00:00:00Z", Title: "Home law", Summary: "A shared rule",
		Scopes: homeScope(),
	})
	commitKnowledgeRepo(t, repo, "shared law")
	sourceRepo := initKnowledgeRepo(t)
	sourceHome.RepoPath = sourceRepo
	seedFederatedSource(t, sourceRepo, lawID, lawPath, lawTitle)
	commitKnowledgeRepo(t, sourceRepo, "source law")
	s := openTemp(t)
	authorizeKnowledgeProductHome(t, s, productID, homeHome, homeHome.HomeProjectID, sourceHome.HomeProjectID)
	authorizeSourceLocator(t, s, sourceHome)
	if _, err := s.RegisterProductKnowledgeSource(ctx, ProductKnowledgeSourceRegistration{
		ProductID: productID, ProjectID: sourceHome.HomeProjectID, LocatorID: sourceHome.HomeLocatorID,
		Reason: "federated fixture", ExpectedVersion: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.RebuildKnowledgeIndex(ctx, homeHome); err != nil {
		t.Fatal(err)
	}
	if err := s.RebuildKnowledgeIndex(ctx, sourceHome); err != nil {
		t.Fatal(err)
	}
	return s, sourceHome
}

func TestProductKnowledgeSourceRegistrationRefusals(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := initKnowledgeRepo(t)
	sourceRepo := initKnowledgeRepo(t)
	home := KnowledgeHome{HomeProjectID: "reg-home", HomeLocatorID: "reg-home-loc", RepoPath: repo, HeadRef: "HEAD"}
	source := KnowledgeHome{HomeProjectID: "reg-src", HomeLocatorID: "reg-src-loc", RepoPath: sourceRepo, HeadRef: "HEAD"}
	s := openTemp(t)
	authorizeKnowledgeProductHome(t, s, "reg-product", home, "reg-home", "reg-src")
	authorizeSourceLocator(t, s, source)
	register := func(project, locator string) error {
		_, err := s.RegisterProductKnowledgeSource(ctx, ProductKnowledgeSourceRegistration{
			ProductID: "reg-product", ProjectID: project, LocatorID: locator, Reason: "test", ExpectedVersion: 1,
		})
		return err
	}
	assertKind := func(name string, err error, want FailureKind) {
		t.Helper()
		var failure *Failure
		if !errors.As(err, &failure) || failure.Kind != want {
			t.Fatalf("%s: kind = %v, want %v (err=%v)", name, err, want, err)
		}
	}
	assertKind("designated home", register("reg-home", "reg-home-loc"), KindProjectionConflict)
	assertKind("unknown locator", register("reg-src", "missing-loc"), KindProjectionNotFound)
	assertKind("foreign project", register("reg-home", "reg-src-loc"), KindProjectionConflict)
	if err := register("reg-src", "reg-src-loc"); err != nil {
		t.Fatalf("register source: %v", err)
	}
	duplicate := func() error {
		_, err := s.RegisterProductKnowledgeSource(ctx, ProductKnowledgeSourceRegistration{
			ProductID: "reg-product", ProjectID: "reg-src", LocatorID: "reg-src-loc", Reason: "test", ExpectedVersion: 2,
		})
		return err
	}
	assertKind("duplicate", duplicate(), KindProjectionConflict)
	if _, err := s.RegisterProductKnowledgeSource(ctx, ProductKnowledgeSourceRegistration{
		ProductID: "reg-product", ProjectID: "reg-src", LocatorID: "reg-src-loc", Reason: "test", ExpectedVersion: 2,
	}); err == nil {
		t.Fatal("stale expected version accepted")
	}
	if _, err := s.RemoveProductKnowledgeSource(ctx, ProductKnowledgeSourceRegistration{
		ProductID: "reg-product", ProjectID: "reg-src", LocatorID: "reg-src-loc", Reason: "test", ExpectedVersion: 2,
	}); err != nil {
		t.Fatalf("remove source: %v", err)
	}
	_, err := s.RemoveProductKnowledgeSource(ctx, ProductKnowledgeSourceRegistration{
		ProductID: "reg-product", ProjectID: "reg-src", LocatorID: "reg-src-loc", Reason: "test", ExpectedVersion: 3,
	})
	assertKind("remove unregistered", err, KindProjectionNotFound)
}

func TestFederatedQ9IteratesRegisteredSourceSet(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	home := KnowledgeHome{HomeProjectID: "fed-home", HomeLocatorID: "fed-home-loc", HeadRef: "HEAD"}
	source := KnowledgeHome{HomeProjectID: "fed-src", HomeLocatorID: "fed-src-loc", HeadRef: "HEAD"}
	s, source := seedFederatedProduct(t, "fed-product", home, source,
		"SRC-LAW", ".concord/docs/decisions/CD-0902-src-law.md", "Source law")
	result, err := s.QueryQ9(ctx, Q9Request{Product: "fed-product", Text: "law", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 2 {
		t.Fatalf("federated items = %d, want 2: %#v", len(result.Items), result.Items)
	}
	seen := map[string]string{}
	for _, item := range result.Items {
		seen[item.ID] = item.HomeProjectID
	}
	if seen["HOME-LAW"] != "fed-home" || seen["SRC-LAW"] != "fed-src" {
		t.Fatalf("items lost their source identity: %#v", seen)
	}
	if len(result.SourceWatermarks) != 2 {
		t.Fatalf("source watermarks = %#v", result.SourceWatermarks)
	}
	if result.SourceWatermarks[0].ProjectID != "fed-home" || result.SourceWatermarks[1].ProjectID != "fed-src" {
		t.Fatalf("watermark order = %#v", result.SourceWatermarks)
	}
	for _, mark := range result.SourceWatermarks {
		if mark.Authority != "authoritative" || mark.Watermark == "" {
			t.Fatalf("watermark verdict = %#v", mark)
		}
	}
	if result.ResultMeta.Authority != "authoritative" {
		t.Fatalf("meta = %#v", result.ResultMeta)
	}
	for _, omission := range result.ResultMeta.Omissions {
		if len(omission) >= len("knowledge_source_degraded:") && omission[:len("knowledge_source_degraded:")] == "knowledge_source_degraded:" {
			t.Fatalf("authoritative answer carries a degraded omission: %q", omission)
		}
	}
}

func TestFederatedQ9CursorBindsToSourceSetDigest(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	home := KnowledgeHome{HomeProjectID: "cur-home", HomeLocatorID: "cur-home-loc", HeadRef: "HEAD"}
	source := KnowledgeHome{HomeProjectID: "cur-src", HomeLocatorID: "cur-src-loc", HeadRef: "HEAD"}
	s, _ := seedFederatedProduct(t, "cur-product", home, source,
		"SRC-LAW", ".concord/docs/decisions/CD-0902-src-law.md", "Cursor source law")
	result, err := s.QueryQ9(ctx, Q9Request{Product: "cur-product", Text: "law", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if result.NextCursor == nil {
		t.Fatal("federated page returned no cursor")
	}
	cursor := *result.NextCursor
	if _, err := s.RemoveProductKnowledgeSource(ctx, ProductKnowledgeSourceRegistration{
		ProductID: "cur-product", ProjectID: "cur-src", LocatorID: "cur-src-loc", Reason: "digest change", ExpectedVersion: 2,
	}); err != nil {
		t.Fatal(err)
	}
	_, err = s.QueryQ9(ctx, Q9Request{Product: "cur-product", Text: "law", Limit: 1, Cursor: cursor})
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindInvalidCursor {
		t.Fatalf("stale cursor error = %v, want %v", err, KindInvalidCursor)
	}
}

func TestFederatedQ9DegradedSourceRefusesAndOmits(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	home := KnowledgeHome{HomeProjectID: "deg-home", HomeLocatorID: "deg-home-loc", HeadRef: "HEAD"}
	source := KnowledgeHome{HomeProjectID: "deg-src", HomeLocatorID: "deg-src-loc", HeadRef: "HEAD"}
	s, source := seedFederatedProduct(t, "deg-product", home, source,
		"SRC-LAW", ".concord/docs/decisions/CD-0902-src-law.md", "Degraded source law")
	// Advance the source head past its watermark: the source is stale.
	writeKnowledgeFile(t, source.RepoPath, ".concord/docs/work/unindexed.md", "later\n")
	commitKnowledgeRepo(t, source.RepoPath, "stale head")
	_, err := s.QueryQ9(ctx, Q9Request{Product: "deg-product", Text: "law", Limit: 10})
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindIndexDegraded {
		t.Fatalf("stale source error = %v, want %v", err, KindIndexDegraded)
	}
	degraded, err := s.QueryQ9(ctx, Q9Request{Product: "deg-product", Text: "law", Limit: 10, AllowDegraded: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(degraded.Items) != 1 || degraded.Items[0].ID != "HOME-LAW" {
		t.Fatalf("degraded items = %#v", degraded.Items)
	}
	if degraded.ResultMeta.Authority != "degraded" {
		t.Fatalf("degraded authority = %q", degraded.ResultMeta.Authority)
	}
	found := false
	for _, omission := range degraded.ResultMeta.Omissions {
		if omission == "knowledge_source_degraded:deg-src/deg-src-loc" {
			found = true
		}
	}
	if !found {
		t.Fatalf("omissions name no degraded source: %#v", degraded.ResultMeta.Omissions)
	}
}

func TestSourceRebuildRefusesRegistryPrecedenceAndConflict(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	home := KnowledgeHome{HomeProjectID: "xsrc-home", HomeLocatorID: "xsrc-home-loc", HeadRef: "HEAD"}
	source := KnowledgeHome{HomeProjectID: "xsrc-a", HomeLocatorID: "xsrc-a-loc", HeadRef: "HEAD"}
	s, source := seedFederatedProduct(t, "xsrc-product", home, source,
		"SRC-LAW", ".concord/docs/decisions/CD-0903-src-law.md", "Precedence source law")
	assertDetail := func(name string, err error, wantKind FailureKind) {
		t.Helper()
		var failure *Failure
		if !errors.As(err, &failure) || failure.Kind != wantKind {
			t.Fatalf("%s: kind = %v, want %v (err=%v)", name, err, wantKind, err)
		}
	}
	// Non-home precedence toward shared-home law refuses at rebuild.
	writeSourceRelations(t, source.RepoPath, "SRC-LAW", []KnowledgeRelation{{Kind: "supersedes", TargetID: "HOME-LAW", SourceProjectID: "xsrc-home"}})
	commitKnowledgeRepo(t, source.RepoPath, "precedence declaration")
	assertDetail("precedence", s.RebuildKnowledgeIndex(ctx, source), KindRelationConflict)
	// Cross-source conflicts_with refuses at rebuild.
	writeSourceRelations(t, source.RepoPath, "SRC-LAW", []KnowledgeRelation{{Kind: "conflicts_with", TargetID: "HOME-LAW", SourceProjectID: "xsrc-home"}})
	commitKnowledgeRepo(t, source.RepoPath, "conflict declaration")
	assertDetail("cross-source conflict", s.RebuildKnowledgeIndex(ctx, source), KindRelationConflict)
	// A source manifest that ships the registry refuses at rebuild.
	shards, err := readKnowledgeShardsWorkingTree(source.RepoPath)
	if err != nil {
		t.Fatal(err)
	}
	withRegistry, err := composeKnowledgeManifest(shards, manifestRegisteredSourceRole)
	if err != nil {
		t.Fatal(err)
	}
	withRegistry.DomainRegistry = KnowledgeDomainRegistry{
		SchemaVersion: "1.0", ProductKey: "xsrc", RootDomainID: "product-root:xsrc",
		Domains: []KnowledgeDomain{
			{DomainID: "product-root:xsrc", Name: "xsrc", Purpose: "registry", Status: "current", ArchitectureRelations: []KnowledgeArchitectureRelation{}},
			{DomainID: "fixture-law", Name: "fixture-law", Purpose: "fixture law home", ParentDomainID: "product-root:xsrc", Status: "current", ArchitectureRelations: []KnowledgeArchitectureRelation{}},
		},
	}
	writeManifestShards(t, source.RepoPath, withRegistry)
	commitKnowledgeRepo(t, source.RepoPath, "registry declaration")
	assertDetail("registry in source manifest", s.RebuildKnowledgeIndex(ctx, source), KindInvalidNoteProof)
}

func TestSourceRebuildAcceptsCrossSourceReferenceBetweenSources(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	home := KnowledgeHome{HomeProjectID: "ref-home", HomeLocatorID: "ref-home-loc", HeadRef: "HEAD"}
	sourceA := KnowledgeHome{HomeProjectID: "ref-src-a", HomeLocatorID: "ref-src-a-loc", HeadRef: "HEAD"}
	sourceB := KnowledgeHome{HomeProjectID: "ref-src-b", HomeLocatorID: "ref-src-b-loc", HeadRef: "HEAD"}
	s, sourceA := seedFederatedProduct(t, "ref-product", home, sourceA,
		"SRC-LAW", ".concord/docs/decisions/CD-0906-a-law.md", "Refining source law")
	repoB := initKnowledgeRepo(t)
	sourceB.RepoPath = repoB
	seedFederatedSource(t, repoB, "SRCB-LAW", ".concord/docs/decisions/CD-0905-b-law.md", "Target source law")
	commitKnowledgeRepo(t, repoB, "target source law")
	authorizeKnowledgeProductHome(t, s, "ref-product", home, "ref-src-b")
	authorizeSourceLocator(t, s, sourceB)
	if _, err := s.RegisterProductKnowledgeSource(ctx, ProductKnowledgeSourceRegistration{
		ProductID: "ref-product", ProjectID: "ref-src-b", LocatorID: "ref-src-b-loc", Reason: "second source", ExpectedVersion: 2,
	}); err != nil {
		t.Fatal(err)
	}
	writeSourceRelations(t, sourceA.RepoPath, "SRC-LAW", []KnowledgeRelation{{Kind: "refines", TargetID: "SRCB-LAW", SourceProjectID: "ref-src-b"}})
	commitKnowledgeRepo(t, sourceA.RepoPath, "cross-source refinement")
	// The target source is registered but not yet rebuilt: the declaring
	// source's rebuild refuses the unresolved relation.
	unresolvedErr := s.RebuildKnowledgeIndex(ctx, sourceA)
	var unresolved *Failure
	if !errors.As(unresolvedErr, &unresolved) || unresolved.Kind != KindProjectionNotFound {
		t.Fatalf("unresolved target error = %v, want %v", unresolvedErr, KindProjectionNotFound)
	}
	if err := s.RebuildKnowledgeIndex(ctx, sourceB); err != nil {
		t.Fatal(err)
	}
	if err := s.RebuildKnowledgeIndex(ctx, sourceA); err != nil {
		t.Fatal(err)
	}
	var relations int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM law_relations WHERE home_project_id='ref-src-a' AND home_locator_id='ref-src-a-loc'`).Scan(&relations); err != nil {
		t.Fatal(err)
	}
	if relations != 0 {
		t.Fatalf("cross-source relation projected %d same-home rows, want 0", relations)
	}
}

func TestQualifiedAndAmbiguousLawIdentity(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	home := KnowledgeHome{HomeProjectID: "qid-home", HomeLocatorID: "qid-home-loc", HeadRef: "HEAD"}
	source := KnowledgeHome{HomeProjectID: "qid-src", HomeLocatorID: "qid-src-loc", HeadRef: "HEAD"}
	s, _ := seedFederatedProduct(t, "qid-product", home, source,
		"SRC-LAW", ".concord/docs/decisions/CD-0907-src-law.md", "Qualified source law")
	result, err := s.QueryQ10(ctx, Q10Request{KnowledgeID: "qid-src/SRC-LAW"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "canonical" || result.Note == nil || result.Note.HomeProjectID != "qid-src" {
		t.Fatalf("qualified resolution = %#v", result)
	}
	_, err = s.QueryQ10(ctx, Q10Request{KnowledgeID: "qid-src/a/b"})
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindInvalidFilter {
		t.Fatalf("malformed qualified ID error = %v, want %v", err, KindInvalidFilter)
	}
	// A bare ID held by two sources refuses as ambiguous at the law boundary.
	dupSource := KnowledgeHome{HomeProjectID: "qid-dup", HomeLocatorID: "qid-dup-loc", HeadRef: "HEAD"}
	dupRepo := initKnowledgeRepo(t)
	dupSource.RepoPath = dupRepo
	seedFederatedSource(t, dupRepo, "SRC-LAW", ".concord/docs/decisions/CD-0908-dup-law.md", "Duplicate source law")
	commitKnowledgeRepo(t, dupRepo, "duplicate law")
	authorizeKnowledgeProductHome(t, s, "qid-product", home, "qid-dup")
	authorizeSourceLocator(t, s, dupSource)
	if _, err := s.RegisterProductKnowledgeSource(ctx, ProductKnowledgeSourceRegistration{
		ProductID: "qid-product", ProjectID: "qid-dup", LocatorID: "qid-dup-loc", Reason: "duplicate holder", ExpectedVersion: 2,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.RebuildKnowledgeIndex(ctx, dupSource); err != nil {
		t.Fatal(err)
	}
	q10, err := s.QueryQ10(ctx, Q10Request{KnowledgeID: "SRC-LAW"})
	if err != nil {
		t.Fatal(err)
	}
	if q10.Status != "ambiguous" {
		t.Fatalf("ambiguous bare ID = %#v, want ambiguous", q10)
	}
	mandateErr := s.CheckMandatedLawsAtHome(ctx, home.HomeProjectID, home.HomeLocatorID, []string{"SRC-LAW"}, nil, false)
	var mandateFailure *Failure
	if !errors.As(mandateErr, &mandateFailure) || mandateFailure.Kind != KindKnowledgeAmbiguous {
		t.Fatalf("ambiguous mandate error = %v, want %v", mandateErr, KindKnowledgeAmbiguous)
	}
}

// writeSourceRelations rewrites the source fixture manifest so lawID carries
// exactly the given law relations, and leaves the tree uncommitted.
func writeSourceRelations(t *testing.T, repo, lawID string, relations []KnowledgeRelation) {
	t.Helper()
	shards, err := readKnowledgeShardsWorkingTree(repo)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := composeKnowledgeManifest(shards, manifestRegisteredSourceRole)
	if err != nil {
		t.Fatal(err)
	}
	for index := range manifest.Records {
		if manifest.Records[index].ID == lawID {
			manifest.Records[index].LawRelations = relations
		}
	}
	writeSourceManifest(t, repo, manifest)
}

func TestSingleSourceProductQ9OutputUnchanged(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := initKnowledgeRepo(t)
	home := KnowledgeHome{HomeProjectID: "solo-home", HomeLocatorID: "solo-home-loc", RepoPath: repo, HeadRef: "HEAD"}
	writeManifestFixture(t, repo, manifestFixture{
		ID: "HOME-LAW", Kind: "decision", Path: ".concord/docs/decisions/CD-0909-home-law.md",
		Status: "accepted", Date: "2026-09-20T00:00:00Z", Title: "Solo home law", Summary: "A single source rule",
		Scopes: homeScope(),
	})
	commitKnowledgeRepo(t, repo, "solo law")
	s := openTemp(t)
	authorizeKnowledgeProductHome(t, s, "solo-product", home, "solo-home")
	if err := s.RebuildKnowledgeIndex(ctx, home); err != nil {
		t.Fatal(err)
	}
	result, err := s.QueryQ9(ctx, Q9Request{Product: "solo-product", Text: "rule", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 1 || result.Items[0].ID != "HOME-LAW" {
		t.Fatalf("single-source items = %#v", result.Items)
	}
	if result.SourceWatermarks != nil {
		t.Fatalf("single-source watermarks = %#v, want none", result.SourceWatermarks)
	}
	if result.ResultMeta.Authority != "authoritative" {
		t.Fatalf("single-source authority = %q", result.ResultMeta.Authority)
	}
	_, err = s.RegisterProductKnowledgeSource(ctx, ProductKnowledgeSourceRegistration{
		ProductID: "solo-product", ProjectID: "solo-home", LocatorID: "solo-home-loc", Reason: "home is a source", ExpectedVersion: 1,
	})
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindProjectionConflict {
		t.Fatalf("home registration error = %v, want %v", err, KindProjectionConflict)
	}
}

func TestMandatedLawsResolveAcrossSources(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	home := KnowledgeHome{HomeProjectID: "man-home", HomeLocatorID: "man-home-loc", HeadRef: "HEAD"}
	source := KnowledgeHome{HomeProjectID: "man-src", HomeLocatorID: "man-src-loc", HeadRef: "HEAD"}
	s, _ := seedFederatedProduct(t, "man-product", home, source,
		"SRC-LAW", ".concord/docs/decisions/CD-0910-src-law.md", "Mandated source law")
	if err := s.CheckMandatedLawsAtHome(ctx, home.HomeProjectID, home.HomeLocatorID, []string{"HOME-LAW", "SRC-LAW"}, nil, false); err != nil {
		t.Fatalf("cross-source mandate refused: %v", err)
	}
	if err := s.CheckMandatedLawsAtHome(ctx, home.HomeProjectID, home.HomeLocatorID, []string{"MISSING-LAW"}, nil, false); err == nil {
		t.Fatal("unknown mandate accepted")
	}
}

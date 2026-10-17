package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"sort"
	"strings"
	"testing"
)

// replacementRegistryTables names the four Product registry-derived tables
// the designated home's rebuild owns.
var replacementRegistryTables = [4]string{
	"domain_registries",
	"domains",
	"domain_architecture_relations",
	"domain_relation_governing_laws",
}

// copyFixtureRepo makes a second checkout of a fixture repository: same
// commits, same manifest bytes, same registry content hash and product key.
func copyFixtureRepo(t *testing.T, src string) string {
	t.Helper()
	dst := t.TempDir()
	if err := os.CopyFS(dst, os.DirFS(src)); err != nil {
		t.Fatal(err)
	}
	return dst
}

// replacementHomeFixture builds a fixture repository whose registry declares
// the root Domain, the fixture-law Domain, and the concord-shared-scope
// Domain, and whose fixture-law Domain carries a depends_on relation on the
// shared scope Domain governed by CD-0001.
func replacementHomeFixture(t *testing.T, productID string) string {
	t.Helper()
	repo := initKnowledgeRepo(t)
	writeManifestFixture(t, repo,
		manifestFixture{ID: "CD-0001", Kind: "decision", Path: ".concord/docs/decisions/CD-0001-alpha.md", Status: "accepted", Date: "2026-08-10T00:00:00Z", Title: "Alpha law", Summary: "Governs alpha", Scopes: homeScope()},
		manifestFixture{ID: "CD-0002", Kind: "decision", Path: ".concord/docs/decisions/CD-0002-shared.md", Status: "accepted", Date: "2026-08-11T00:00:00Z", Title: "Shared scope law", Summary: "Declares the shared scope Domain", Scopes: KnowledgeRecordScopes{Mode: "explicit", ProductIDs: []string{productID}, ProjectIDs: []string{}, DomainIDs: []string{"concord-shared-scope"}, TagIDs: []string{}}},
	)
	setManifestGoverningLaw(t, repo, "fixture-law", "concord-shared-scope", "CD-0001")
	commitKnowledgeRepo(t, repo, "two fixture laws and a governed relation")
	return repo
}

// seedReplacementHome seeds one Product whose designated home A holds the
// replacement home fixture, plus home B: a second canonical-path locator on
// the same member Project checking out the same repository, not yet
// designated and not registered.
func seedReplacementHome(t *testing.T) (*Store, KnowledgeHome, KnowledgeHome) {
	t.Helper()
	repoA := replacementHomeFixture(t, "repl-product")
	repoB := copyFixtureRepo(t, repoA)
	s := openTemp(t)
	homeA := KnowledgeHome{HomeProjectID: "repl-proj", HomeLocatorID: "repl-loc-a", RepoPath: repoA, HeadRef: "HEAD"}
	homeB := KnowledgeHome{HomeProjectID: "repl-proj", HomeLocatorID: "repl-loc-b", RepoPath: repoB, HeadRef: "HEAD"}
	authorizeKnowledgeProductHome(t, s, "repl-product", homeA, homeA.HomeProjectID)
	return s, homeA, homeB
}

// addReplacementLocator adds the second checkout's canonical-path locator to
// the Project through the locator API.
func addReplacementLocator(t *testing.T, s *Store, homeB KnowledgeHome) {
	t.Helper()
	normalized, err := NormalizeProjectLocator(LocatorCanonicalPath, homeB.RepoPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddProjectLocator(context.Background(), homeB.HomeProjectID, ProjectLocator{
		ID: homeB.HomeLocatorID, ProjectID: homeB.HomeProjectID, Kind: LocatorCanonicalPath,
		Value: homeB.RepoPath, NormalizedValue: normalized,
	}, 1); err != nil {
		t.Fatal(err)
	}
}

func designateReplacementHome(t *testing.T, s *Store, productID string, homeB KnowledgeHome, expectedVersion int64) {
	t.Helper()
	if _, err := s.DesignateProductKnowledgeHome(context.Background(), ProductKnowledgeHomeDesignation{
		ProductID: productID, ProjectID: homeB.HomeProjectID, LocatorID: homeB.HomeLocatorID,
		Reason: "replacement checkout takes over", ExpectedVersion: expectedVersion,
	}); err != nil {
		t.Fatalf("designate the replacement home: %v", err)
	}
}

// writeRegisteredSourceLawFixture writes a registered-source manifest (no
// Domain registry) whose one accepted law homes on the shared registry's
// fixture-law Domain and applies to the concord-shared-scope Domain.
func writeRegisteredSourceLawFixture(t *testing.T, repo, lawID, title string) {
	t.Helper()
	path := ".concord/docs/decisions/CD-0900-" + strings.ToLower(lawID) + ".md"
	body := "durable " + lawID + " blob\n"
	writeKnowledgeFile(t, repo, path, body)
	sum := sha256.Sum256([]byte(body))
	manifest := KnowledgeManifest{
		SchemaVersion:  "1.2",
		SupportedKinds: []string{"decision"},
		IndexedKinds:   []string{"decision"},
		Records: []KnowledgeRecord{{
			ID: lawID, Kind: "decision", Path: path, Status: "accepted",
			Date: "2026-08-12T00:00:00Z", Title: title, Summary: "A source rule", Tags: []string{},
			Authority: KnowledgeAuthority{Tier: "legislated", LegislatedBy: "work-0f1e2d3c4b5a6978", ContractVersion: 1},
			Scopes:    homeScope(), HomeDomainID: "fixture-law",
			AppliesToDomainIDs: []string{"concord-shared-scope"},
			SHA256:             "sha256:" + hex.EncodeToString(sum[:]),
		}},
	}
	writeSourceManifest(t, repo, manifest)
}

type replacementFederation struct {
	store      *Store
	reader     *sql.DB
	homeA      KnowledgeHome
	homeB      KnowledgeHome
	sourceHome KnowledgeHome
}

// seedReplacementFederation seeds one Product with designated home A holding
// the replacement home fixture, a registered source whose law references the
// shared registry Domains, both rebuilt, and home B: a second checkout of
// home A's repository on the same Project. The reader is a separate
// connection on the same database file, so a refused rebuild's failed COMMIT
// cannot affect the durable-state reads.
func seedReplacementFederation(t *testing.T) replacementFederation {
	t.Helper()
	ctx := context.Background()
	repoA := replacementHomeFixture(t, "fed-product")
	sourceRepo := initKnowledgeRepo(t)
	writeRegisteredSourceLawFixture(t, sourceRepo, "SRC-LAW", "Source law")
	commitKnowledgeRepo(t, sourceRepo, "source law")
	dbPath := copyTestDatabase(t)
	store, err := Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	reader, err := sql.Open(driverName, dataSourceName(dbPath))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reader.Close() })
	fed := replacementFederation{
		store:      store,
		reader:     reader,
		homeA:      KnowledgeHome{HomeProjectID: "fed-proj", HomeLocatorID: "fed-loc-a", RepoPath: repoA, HeadRef: "HEAD"},
		homeB:      KnowledgeHome{HomeProjectID: "fed-proj", HomeLocatorID: "fed-loc-b", RepoPath: copyFixtureRepo(t, repoA), HeadRef: "HEAD"},
		sourceHome: KnowledgeHome{HomeProjectID: "fed-src", HomeLocatorID: "fed-loc-s", RepoPath: sourceRepo, HeadRef: "HEAD"},
	}
	authorizeKnowledgeProductHome(t, store, "fed-product", fed.homeA, fed.homeA.HomeProjectID, fed.sourceHome.HomeProjectID)
	authorizeSourceLocator(t, store, fed.sourceHome)
	if _, err := store.RegisterProductKnowledgeSource(ctx, ProductKnowledgeSourceRegistration{
		ProductID: "fed-product", ProjectID: fed.sourceHome.HomeProjectID, LocatorID: fed.sourceHome.HomeLocatorID,
		Reason: "replacement federation fixture", ExpectedVersion: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.RebuildKnowledgeIndex(ctx, fed.homeA); err != nil {
		t.Fatalf("designated home rebuild: %v", err)
	}
	if err := store.RebuildKnowledgeIndex(ctx, fed.sourceHome); err != nil {
		t.Fatalf("registered source rebuild: %v", err)
	}
	return fed
}

// knowledgeRowSet preserves SQL NULLs and byte slices in sorted JSON tuples.
func knowledgeRowSet(t *testing.T, db *sql.DB, query string, args ...any) []string {
	t.Helper()
	rows, err := db.Query(query, args...)
	if err != nil {
		t.Fatalf("row set query failed: %v", err)
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, 0)
	for rows.Next() {
		values := make([]any, len(columns))
		dest := make([]any, len(columns))
		for i := range values {
			dest[i] = &values[i]
		}
		if err := rows.Scan(dest...); err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(values)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, string(encoded))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	sort.Strings(out)
	return out
}

// knowledgeDerivedSnapshot reads every row and column of every git-derived
// table the rebuild clears.
func knowledgeDerivedSnapshot(t *testing.T, db *sql.DB) string {
	t.Helper()
	out := make(map[string][]string, len(derivedKnowledgeClearOrder))
	for _, table := range derivedKnowledgeClearOrder {
		out[table] = knowledgeRowSet(t, db, `SELECT * FROM "`+table+`"`) //nolint:gosec // table comes from the production derivedKnowledgeClearOrder literal.
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

// replacementSourceLawRows reads the law_domain_homes and
// law_domain_applicability rows of one home pair.
func replacementSourceLawRows(t *testing.T, db *sql.DB, home KnowledgeHome) []string {
	t.Helper()
	var out []string
	for _, row := range knowledgeRowSet(t, db, `SELECT * FROM law_domain_homes WHERE home_project_id=? AND home_locator_id=?`, home.HomeProjectID, home.HomeLocatorID) {
		out = append(out, "law_domain_homes\x1f"+row)
	}
	for _, row := range knowledgeRowSet(t, db, `SELECT * FROM law_domain_applicability WHERE home_project_id=? AND home_locator_id=?`, home.HomeProjectID, home.HomeLocatorID) {
		out = append(out, "law_domain_applicability\x1f"+row)
	}
	sort.Strings(out)
	return out
}

func replacementPairRowCount(t *testing.T, s *Store, table string, home KnowledgeHome) int {
	t.Helper()
	var count int
	if err := s.DatabaseForTesting().QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE home_project_id=? AND home_locator_id=?`, home.HomeProjectID, home.HomeLocatorID).Scan(&count); err != nil { //nolint:gosec // table comes from the closed replacementRegistryTables literal.
		t.Fatalf("count %s rows: %v", table, err)
	}
	return count
}

func requireFailureKind(t *testing.T, err error, want FailureKind, label string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: expected a refusal, got success", label)
	}
	var failure *Failure
	if !errors.As(err, &failure) {
		t.Fatalf("%s: error is not a typed Failure: %v", label, err)
	}
	if failure.Kind != want {
		t.Fatalf("%s: failure kind = %v, want %v (err=%v)", label, failure.Kind, want, err)
	}
}

func TestKnowledgeHomeReplacementDuringRebuildRefusesStaleContext(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fed := seedReplacementFederation(t)
	home := fed.sourceHome
	productID, designated, err := resolveKnowledgeSourceRole(ctx, fed.store.DatabaseForTesting(), home)
	if err != nil {
		t.Fatal(err)
	}
	if productID != "fed-product" || designated {
		t.Fatalf("fixture is not a registered, non-designated source: %q, %v", productID, designated)
	}
	commit, err := resolveKnowledgeHead(ctx, home)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := knowledgeContentDigest(ctx, home, commit)
	if err != nil {
		t.Fatal(err)
	}
	manifest, missing, err := readKnowledgeManifest(ctx, home.RepoPath, commit, manifestRegisteredSourceRole)
	if err != nil || missing {
		t.Fatalf("read source manifest: missing=%v, err=%v", missing, err)
	}
	notes := make([]VerifiedNote, 0, len(manifest.Records))
	laws := make([]indexedLaw, 0, len(manifest.Records))
	for _, record := range manifest.Records {
		body, err := verifyManifestBlob(ctx, home.RepoPath, commit, record)
		if err != nil {
			t.Fatal(err)
		}
		notes = append(notes, manifestRecordNote(record, commit, manifest.SchemaVersion))
		laws = append(laws, indexedLaw{record: record, body: body})
	}
	// Move the designation after preparation but before the write transaction.
	designateReplacementHome(t, fed.store, "fed-product", home, 2)
	before := knowledgeDerivedSnapshot(t, fed.reader)
	tx, err := beginOrdinaryTx(ctx, fed.store.DatabaseForTesting())
	if err != nil {
		t.Fatal(err)
	}
	err = rebuildKnowledgeIndexTx(ctx, tx, home, commit, digest, notes, laws, false, manifest, productID, designated)
	if err == nil {
		err = tx.Commit()
	}
	_ = tx.Rollback()
	requireFailureKind(t, err, KindStaleContext, "source became designated during rebuild")
	var failure *Failure
	if !errors.As(err, &failure) || !failure.RetrySafe || failure.EffectPossible {
		t.Fatalf("stale-context refusal must be retry-safe with no possible effect: %v", err)
	}
	if after := knowledgeDerivedSnapshot(t, fed.reader); after != before {
		t.Fatalf("the stale rebuild disturbed the prior projection:\nbefore=%s\nafter=%s", before, after)
	}
}

func TestKnowledgeHomeValidationUsesPinnedRoleAcrossDesignationFlipAndBack(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fed := seedReplacementFederation(t)
	home := fed.homeA
	addReplacementLocator(t, fed.store, fed.homeB)
	productID, designated, err := resolveKnowledgeSourceRole(ctx, fed.store.DatabaseForTesting(), home)
	if err != nil {
		t.Fatal(err)
	}
	if productID != "fed-product" || !designated {
		t.Fatalf("fixture is not the designated home: %q, %v", productID, designated)
	}
	commit, err := resolveKnowledgeHead(ctx, home)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := knowledgeContentDigest(ctx, home, commit)
	if err != nil {
		t.Fatal(err)
	}
	manifest, missing, err := readKnowledgeManifest(ctx, home.RepoPath, commit, manifestSharedHomeRole)
	if err != nil || missing {
		t.Fatalf("read home manifest: missing=%v, err=%v", missing, err)
	}
	notes := make([]VerifiedNote, 0, len(manifest.Records))
	laws := make([]indexedLaw, 0, len(manifest.Records))
	for _, record := range manifest.Records {
		body, err := verifyManifestBlob(ctx, home.RepoPath, commit, record)
		if err != nil {
			t.Fatal(err)
		}
		notes = append(notes, manifestRecordNote(record, commit, manifest.SchemaVersion))
		if manifestLawBearingKinds[record.Kind] {
			laws = append(laws, indexedLaw{record: record, body: body})
		}
	}
	before := knowledgeDerivedSnapshot(t, fed.reader)
	designateReplacementHome(t, fed.store, productID, fed.homeB, 2)
	validationErr := validateFederatedSourceManifest(ctx, fed.store.DatabaseForTesting(), home, manifest, productID, designated)
	designateReplacementHome(t, fed.store, productID, home, 3)
	restoredProductID, restoredDesignated, err := resolveKnowledgeSourceRole(ctx, fed.store.DatabaseForTesting(), home)
	if err != nil {
		t.Fatal(err)
	}
	if restoredProductID != productID || restoredDesignated != designated {
		t.Fatalf("designation did not return to the prepared role: (%q, %v), want (%q, %v)", restoredProductID, restoredDesignated, productID, designated)
	}
	if validationErr != nil {
		t.Fatalf("validation discarded the pinned role during a designation flip and return: %v", validationErr)
	}
	tx, err := beginOrdinaryTx(ctx, fed.store.DatabaseForTesting())
	if err != nil {
		t.Fatal(err)
	}
	if err := rebuildKnowledgeIndexTx(ctx, tx, home, commit, digest, notes, laws, false, manifest, productID, designated); err != nil {
		_ = tx.Rollback()
		t.Fatalf("rebuild with the restored prepared role: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit rebuild with the restored prepared role: %v", err)
	}
	if after := knowledgeDerivedSnapshot(t, fed.reader); after != before {
		t.Fatalf("the flip-and-return rebuild changed the prior registry or source-law rows:\nbefore=%s\nafter=%s", before, after)
	}
}

func TestKnowledgeHomeReplacementMissingManifestKeepsPriorRegistry(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fed := seedReplacementFederation(t)
	fed.homeB.RepoPath = initKnowledgeRepo(t)
	writeKnowledgeFile(t, fed.homeB.RepoPath, "README.md", "A repository without a knowledge manifest.\n")
	commit := commitKnowledgeRepo(t, fed.homeB.RepoPath, "repository without a manifest")
	if _, missing, err := readKnowledgeManifest(ctx, fed.homeB.RepoPath, commit, manifestSharedHomeRole); err != nil || !missing {
		t.Fatalf("fixture must have no committed manifest: missing=%v, err=%v", missing, err)
	}
	addReplacementLocator(t, fed.store, fed.homeB)
	designateReplacementHome(t, fed.store, "fed-product", fed.homeB, 2)
	before := knowledgeDerivedSnapshot(t, fed.reader)
	if err := fed.store.RebuildKnowledgeIndex(ctx, fed.homeB); err != nil {
		t.Fatalf("missing-manifest replacement rebuild: %v", err)
	}
	if after := knowledgeDerivedSnapshot(t, fed.reader); after != before {
		t.Fatalf("the missing-manifest rebuild cleared another home's registry or law rows:\nbefore=%s\nafter=%s", before, after)
	}
}

// TestKnowledgeHomeReplacementTakesOverRegistry proves the designated home's
// rebuild takes over the Product registry: after the designation moves to the
// second checkout, both rebuilds succeed, exactly one domain_registries row
// survives for the Product, every registry-derived table carries the
// replacement pair with its full prior row count, and the replaced pair keeps
// nothing.
func TestKnowledgeHomeReplacementTakesOverRegistry(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, homeA, homeB := seedReplacementHome(t)
	if err := s.RebuildKnowledgeIndex(ctx, homeA); err != nil {
		t.Fatalf("first rebuild of the designated home: %v", err)
	}
	before := map[string]int{}
	for _, table := range replacementRegistryTables {
		count := replacementPairRowCount(t, s, table, homeA)
		if count == 0 {
			t.Fatalf("fixture premise: %s holds no rows for the designated home after its rebuild", table)
		}
		if replacementPairRowCount(t, s, table, homeB) != 0 {
			t.Fatalf("fixture premise: %s already holds rows for the replacement pair", table)
		}
		before[table] = count
	}
	addReplacementLocator(t, s, homeB)
	designateReplacementHome(t, s, "repl-product", homeB, 1)
	if err := s.RebuildKnowledgeIndex(ctx, homeB); err != nil {
		t.Fatalf("replacement rebuild of the newly designated home: %v", err)
	}
	if err := s.RebuildKnowledgeIndex(ctx, homeB); err != nil {
		t.Fatalf("second replacement rebuild: %v", err)
	}
	for _, table := range replacementRegistryTables {
		if moved := replacementPairRowCount(t, s, table, homeB); moved != before[table] {
			t.Fatalf("%s rows carried by the replacement pair = %d, want the %d rows the replaced home held", table, moved, before[table])
		}
		if leftover := replacementPairRowCount(t, s, table, homeA); leftover != 0 {
			t.Fatalf("%s still holds %d rows for the replaced home pair, want none", table, leftover)
		}
	}
	var registries int
	if err := s.DatabaseForTesting().QueryRow(`SELECT COUNT(*) FROM domain_registries WHERE product_id='repl-product'`).Scan(&registries); err != nil {
		t.Fatal(err)
	}
	if registries != 1 {
		t.Fatalf("domain_registries rows for the Product = %d, want exactly 1", registries)
	}
}

// TestKnowledgeHomeReplacementStrictProductSearchReturnsEachLawOnce proves
// the replacement leaves the Product's strict search whole: the
// Product-scoped search answers from the replacement pair and returns each
// law exactly once.
func TestKnowledgeHomeReplacementStrictProductSearchReturnsEachLawOnce(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, homeA, homeB := seedReplacementHome(t)
	if err := s.RebuildKnowledgeIndex(ctx, homeA); err != nil {
		t.Fatalf("first rebuild of the designated home: %v", err)
	}
	addReplacementLocator(t, s, homeB)
	designateReplacementHome(t, s, "repl-product", homeB, 1)
	if err := s.RebuildKnowledgeIndex(ctx, homeB); err != nil {
		t.Fatalf("replacement rebuild of the newly designated home: %v", err)
	}
	result, err := s.QueryQ9(ctx, Q9Request{Product: "repl-product", Text: "law"})
	if err != nil {
		t.Fatalf("strict Product search after replacement: %v", err)
	}
	seen := map[string]int{}
	for _, item := range result.Items {
		seen[item.ID]++
	}
	for _, lawID := range []string{"CD-0001", "CD-0002"} {
		if seen[lawID] != 1 {
			t.Fatalf("strict Product search returned law %s %d times, want exactly once (items=%#v)", lawID, seen[lawID], result.Items)
		}
	}
	if len(result.Items) != 2 {
		t.Fatalf("strict Product search returned %d items, want 2: %#v", len(result.Items), result.Items)
	}
}

// TestKnowledgeHomeReplacementKeepsRegisteredSourceRows proves the
// replacement rebuild succeeds with a registered source present and leaves
// the registered source's law Domain rows untouched. Only the registered
// source's pair is compared: the replacement pair legitimately gains rows.
func TestKnowledgeHomeReplacementKeepsRegisteredSourceRows(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fed := seedReplacementFederation(t)
	before := replacementSourceLawRows(t, fed.store.DatabaseForTesting(), fed.sourceHome)
	if len(before) < 2 {
		t.Fatalf("fixture premise: the registered source projected no law Domain rows: %v", before)
	}
	addReplacementLocator(t, fed.store, fed.homeB)
	designateReplacementHome(t, fed.store, "fed-product", fed.homeB, 2)
	if err := fed.store.RebuildKnowledgeIndex(ctx, fed.homeB); err != nil {
		t.Fatalf("replacement rebuild with a registered source present: %v", err)
	}
	after := replacementSourceLawRows(t, fed.store.DatabaseForTesting(), fed.sourceHome)
	if strings.Join(after, "\n") != strings.Join(before, "\n") {
		t.Fatalf("registered source law Domain rows changed across the replacement rebuild:\nbefore=%v\nafter=%v", before, after)
	}
}

// TestKnowledgeHomeReplacementRefusalKeepsPriorProjection proves the refused
// replacement stays atomic: when the new registry drops a Domain that a
// registered source's law references, the rebuild refuses and every row and
// column of every git-derived table, including the replaced home's registry,
// is exactly what it was before the call.
func TestKnowledgeHomeReplacementRefusalKeepsPriorProjection(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fed := seedReplacementFederation(t)
	manifest := composeWorkingTreeManifest(t, fed.homeB.RepoPath)
	kept := make([]KnowledgeDomain, 0, len(manifest.DomainRegistry.Domains))
	for _, domain := range manifest.DomainRegistry.Domains {
		if domain.DomainID == "fixture-law" {
			continue
		}
		relations := make([]KnowledgeArchitectureRelation, 0, len(domain.ArchitectureRelations))
		for _, relation := range domain.ArchitectureRelations {
			if relation.TargetDomainID != "fixture-law" {
				relations = append(relations, relation)
			}
		}
		domain.ArchitectureRelations = relations
		kept = append(kept, domain)
	}
	manifest.DomainRegistry.Domains = kept
	for i := range manifest.Records {
		if manifest.Records[i].HomeDomainID == "fixture-law" {
			manifest.Records[i].HomeDomainID = manifest.DomainRegistry.RootDomainID
			manifest.Records[i].ProductWideRationale = "Replacement registry homes the shared law on the root."
		}
	}
	writeManifestShards(t, fed.homeB.RepoPath, manifest)
	commitKnowledgeRepo(t, fed.homeB.RepoPath, "replacement registry drops the shared law Domain")
	addReplacementLocator(t, fed.store, fed.homeB)
	designateReplacementHome(t, fed.store, "fed-product", fed.homeB, 2)
	before := knowledgeDerivedSnapshot(t, fed.reader)
	if err := fed.store.RebuildKnowledgeIndex(ctx, fed.homeB); err == nil {
		t.Fatal("a replacement dropping a Domain that a registered source's law references succeeded")
	}
	after := knowledgeDerivedSnapshot(t, fed.reader)
	if after != before {
		t.Fatalf("the refused replacement disturbed the prior projection:\nbefore=%s\nafter=%s", before, after)
	}
}

// TestKnowledgeHomeReplacementUndesignatedHomeRefusesBeforeWrite pins
// the existing undesignated-home guard: a member home with no designation and
// no registration refuses its rebuild with KindKnowledgeAmbiguous before any
// write, and the designated home's rows are unchanged.
func TestKnowledgeHomeReplacementUndesignatedHomeRefusesBeforeWrite(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, homeA, homeB := seedReplacementHome(t)
	if err := s.RebuildKnowledgeIndex(ctx, homeA); err != nil {
		t.Fatalf("first rebuild of the designated home: %v", err)
	}
	addReplacementLocator(t, s, homeB)
	before := knowledgeDerivedSnapshot(t, s.DatabaseForTesting())
	err := s.RebuildKnowledgeIndex(ctx, homeB)
	requireFailureKind(t, err, KindKnowledgeAmbiguous, "undesignated, unregistered home rebuild")
	after := knowledgeDerivedSnapshot(t, s.DatabaseForTesting())
	if after != before {
		t.Fatalf("the refused rebuild disturbed the designated home's rows:\nbefore=%s\nafter=%s", before, after)
	}
}

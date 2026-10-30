package store

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const footprintProduct = "foot-product"

// footprintFixtureRepo commits one law of every footprint basis: CD-0003 is
// the law document a caller names; CD-0001 (child-homed) and CD-0002
// (root-homed) name internal/alpha/a.go; CD-0004 matches the behavior
// phrase; CD-0007 is homed in the reached Domain; CD-0005 is homed elsewhere
// and applies to it. Per-law coverage shards are retired (CD-0249 D1) and
// name no fixture member.
func footprintFixtureRepo(t *testing.T, withMap bool) string {
	t.Helper()
	repo := initKnowledgeRepo(t)
	scopes := KnowledgeRecordScopes{Mode: "explicit", DomainIDs: []string{"fixture-law", "fixture-other"}}
	law := func(id, slug, title string, evidence ...string) manifestFixture {
		return manifestFixture{ID: id, Kind: "decision", Path: ".concord/docs/decisions/" + id + "-" + slug + ".md", Status: "accepted", Date: "2026-08-10T00:00:00Z", Title: title, Summary: title, Scopes: scopes, Evidence: evidence}
	}
	root := law("CD-0002", "root", "Root law", "internal/alpha/a.go")
	root.RootHomed = true
	writeManifestFixture(t, repo,
		law("CD-0001", "alpha", "Alpha law", "internal/alpha/a.go"),
		root,
		law("CD-0003", "beta", "Beta law"),
		law("CD-0004", "rocket", "Rocket ignition timing"),
		law("CD-0005", "applies", "Applies law"),
		law("CD-0007", "context", "Context law"),
	)
	manifest := composeWorkingTreeManifest(t, repo)
	for index := range manifest.Records {
		if manifest.Records[index].ID == "CD-0005" {
			manifest.Records[index].HomeDomainID = "fixture-other"
			manifest.Records[index].AppliesToDomainIDs = []string{"fixture-law"}
		}
	}
	writeManifestShards(t, repo, manifest)
	if withMap {
		writeKnowledgeFile(t, repo, knowledgePackageMapPath, architectureFixtureMap)
	}
	commitKnowledgeRepo(t, repo, "footprint fixture")
	return repo
}

func footprintFixtureStore(t *testing.T, withMap bool) (*Store, KnowledgeHome, string) {
	t.Helper()
	repo := footprintFixtureRepo(t, withMap)
	s := openTemp(t)
	home := KnowledgeHome{HomeProjectID: "foot-project", HomeLocatorID: "foot-locator", RepoPath: repo, HeadRef: "HEAD"}
	authorizeKnowledgeProductHome(t, s, footprintProduct, home)
	if err := s.RebuildKnowledgeIndex(context.Background(), home); err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	return s, home, repo
}

type footprintLawView struct {
	LawID   string
	Basis   string
	Matched string
}

func footprintLawViews(laws []FootprintLaw) []footprintLawView {
	views := make([]footprintLawView, 0, len(laws))
	for _, law := range laws {
		views = append(views, footprintLawView{LawID: law.LawID, Basis: law.Basis, Matched: strings.Join(law.MatchedPaths, ",")})
	}
	return views
}

var footprintFixtureRequest = DomainFootprintRequest{
	Product:  footprintProduct,
	Paths:    []string{"internal/alpha/a.go", ".concord/docs/decisions/CD-0003-beta.md", "README.md"},
	Behavior: "rocket ignition",
}

// TestDomainFootprintJoinsPackageMapLawEvidence proves the footprint join
// and its order (CD-0222 D2): each law once under its strongest basis, the
// reached Domains, the package checks, and the unmapped flag. Per-law
// coverage is retired (CD-0249 D1) and no law carries checks.
func TestDomainFootprintJoinsPackageMapLawEvidence(t *testing.T) {
	t.Parallel()
	s, _, repo := footprintFixtureStore(t, true)
	result, err := s.QueryDomainFootprint(context.Background(), footprintFixtureRequest)
	if err != nil {
		t.Fatal(err)
	}
	doc := ".concord/docs/decisions/CD-0003-beta.md"
	want := []footprintLawView{
		{"CD-0003", FootprintIsLawDocument, doc},
		{"CD-0001", FootprintNamesPath, "internal/alpha/a.go"},
		{"CD-0002", FootprintNamesPath, "internal/alpha/a.go"},
		{"CD-0004", FootprintBehaviorMatch, ""},
		{"CD-0007", FootprintHomedInDomain, ""},
		{"CD-0005", FootprintAppliesToDomain, ""},
	}
	if got := footprintLawViews(result.Laws); !reflect.DeepEqual(got, want) {
		t.Fatalf("laws:\n%+v\nwant:\n%+v", got, want)
	}
	var lawHash, homeDomain string
	if err := s.DatabaseForTesting().QueryRow(`SELECT s.content_hash,h.domain_id FROM law_subjects s JOIN law_domain_homes h USING(home_project_id,home_locator_id,law_id) WHERE s.law_id='CD-0001'`).Scan(&lawHash, &homeDomain); err != nil {
		t.Fatal(err)
	}
	if result.Laws[1].ContentHash != lawHash || result.Laws[1].HomeDomainID != homeDomain || result.Laws[1].Kind != "decision" || result.Laws[1].Title != "Alpha law" {
		t.Fatalf("law revision = %+v, want hash %s home %s", result.Laws[1], lawHash, homeDomain)
	}
	wantPaths := []FootprintPath{
		{Path: doc, DomainIDs: []string{}},
		{Path: "README.md", DomainIDs: []string{}, Unmapped: true},
		{Path: "internal/alpha/a.go", Package: "internal/alpha", DomainIDs: []string{"fixture-law"}},
	}
	if !reflect.DeepEqual(result.Paths, wantPaths) {
		t.Fatalf("paths = %+v, want %+v", result.Paths, wantPaths)
	}
	if len(result.Domains) != 2 || result.Domains[0].DomainID != "fixture-law" || !strings.HasPrefix(result.Domains[1].DomainID, "product-root:") || result.Domains[0].Status != "current" {
		t.Fatalf("domains = %+v", result.Domains)
	}
	if want := []FootprintPackageCheck{{Package: "internal/alpha", Kind: "go_package", Value: "./internal/alpha"}}; !reflect.DeepEqual(result.PackageChecks, want) {
		t.Fatalf("package checks = %+v", result.PackageChecks)
	}
	if result.KnowledgeCommit != headCommit(t, repo) || result.PackageMapContentHash == nil || !strings.HasPrefix(*result.PackageMapContentHash, "sha256:") || result.NextCursor != nil {
		t.Fatalf("result header = commit %s map %v cursor %v", result.KnowledgeCommit, result.PackageMapContentHash, result.NextCursor)
	}
	var registryHash string
	if err := s.DatabaseForTesting().QueryRow(`SELECT content_hash FROM domain_registries WHERE product_id=?`, footprintProduct).Scan(&registryHash); err != nil || result.RegistryContentHash != registryHash {
		t.Fatalf("registry hash = %s, want %s (%v)", result.RegistryContentHash, registryHash, err)
	}
	if result.QueryID != footprintQueryID || result.Authority != "authoritative" {
		t.Fatalf("meta = %+v", result.ResultMeta)
	}
}

// TestDomainFootprintPagesAreStableAndCursorsBindTheirRequest proves a page
// boundary, a captured-page shrink, and the refusal of a cursor replayed
// against other paths or behavior, or whose knowledge commit has moved.
func TestDomainFootprintPagesAreStableAndCursorsBindTheirRequest(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, home, repo := footprintFixtureStore(t, true)
	full, err := s.QueryDomainFootprint(ctx, footprintFixtureRequest)
	if err != nil {
		t.Fatal(err)
	}
	var walked []FootprintLaw
	req := footprintFixtureRequest
	req.Limit = 4
	for page := 0; ; page++ {
		result, err := s.QueryDomainFootprint(ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		walked = append(walked, result.Laws...)
		if result.NextCursor == nil {
			break
		}
		if page > 2 {
			t.Fatal("pagination does not terminate")
		}
		req.Cursor = *result.NextCursor
	}
	if !reflect.DeepEqual(footprintLawViews(walked), footprintLawViews(full.Laws)) {
		t.Fatalf("walked pages differ from one page:\n%+v\n%+v", footprintLawViews(walked), footprintLawViews(full.Laws))
	}

	shrunk, err := full.LimitPage(footprintFixtureRequest, 3)
	if err != nil || len(shrunk.Laws) != 3 || shrunk.NextCursor == nil {
		t.Fatalf("shrunk page = %d laws, cursor %v, err %v", len(shrunk.Laws), shrunk.NextCursor, err)
	}
	rest := footprintFixtureRequest
	rest.Cursor = *shrunk.NextCursor
	tail, err := s.QueryDomainFootprint(ctx, rest)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(footprintLawViews(append(shrunk.Laws, tail.Laws...)), footprintLawViews(full.Laws)) {
		t.Fatal("the shrunk page's cursor does not continue where the page stopped")
	}

	foreignPaths := rest
	foreignPaths.Paths = []string{"internal/alpha/a.go"}
	_, err = s.QueryDomainFootprint(ctx, foreignPaths)
	assertFailureKind(t, err, KindInvalidCursor)
	foreignBehavior := rest
	foreignBehavior.Behavior = ""
	_, err = s.QueryDomainFootprint(ctx, foreignBehavior)
	assertFailureKind(t, err, KindInvalidCursor)

	writeKnowledgeFile(t, repo, "README.md", "moved\n")
	writeKnowledgeFile(t, repo, knowledgePackageMapPath, `{"packages":[{"domain_ids":["fixture-law"],"path":"internal/alpha"},{"domain_ids":["fixture-law"],"path":"internal/alpha/nested"}],"schema_version":"1.0"}`+"\n")
	commitKnowledgeRepo(t, repo, "knowledge moves")
	if err := s.EnsureKnowledgeIndexFresh(ctx, home); err != nil {
		t.Fatal(err)
	}
	_, err = s.QueryDomainFootprint(ctx, rest)
	assertFailureKind(t, err, KindStaleContext)
}

func TestDomainFootprintWithoutPackageMapFlagsCodePathsUnmapped(t *testing.T) {
	t.Parallel()
	s, _, _ := footprintFixtureStore(t, false)
	result, err := s.QueryDomainFootprint(context.Background(), DomainFootprintRequest{Product: footprintProduct, Paths: []string{"internal/alpha/a.go"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.PackageMapContentHash != nil || len(result.PackageChecks) != 0 || !result.Paths[0].Unmapped || result.Paths[0].Package != "" {
		t.Fatalf("absent-map footprint = %+v", result)
	}
	// Law evidence still names the file, so the governing law is found.
	if len(result.Laws) == 0 || result.Laws[0].LawID != "CD-0001" || result.Laws[0].Basis != FootprintNamesPath {
		t.Fatalf("absent-map laws = %+v", footprintLawViews(result.Laws))
	}
}

func TestDomainFootprintRefusesMalformedSelectors(t *testing.T) {
	t.Parallel()
	s, _, _ := footprintFixtureStore(t, true)
	tooMany := make([]string, footprintMaxPaths+1)
	for index := range tooMany {
		tooMany[index] = "internal/alpha/f" + strings.Repeat("x", index) + ".go"
	}
	for name, req := range map[string]DomainFootprintRequest{
		"empty":     {Product: footprintProduct},
		"blank":     {Product: footprintProduct, Behavior: "   "},
		"absolute":  {Product: footprintProduct, Paths: []string{"/etc/passwd"}},
		"traversal": {Product: footprintProduct, Paths: []string{"../outside.go"}},
		"dot":       {Product: footprintProduct, Paths: []string{"internal/./alpha/a.go"}},
		"repeat":    {Product: footprintProduct, Paths: []string{"a.go", "a.go"}},
		"too many":  {Product: footprintProduct, Paths: tooMany},
		"long":      {Product: footprintProduct, Behavior: strings.Repeat("b", footprintMaxBehavior+1)},
	} {
		_, err := s.QueryDomainFootprint(context.Background(), req)
		var failure *Failure
		if !failureAs(err, &failure) || failure.Kind != KindInvalidInput {
			t.Errorf("%s: err = %v, want invalid_input", name, err)
		}
	}
	_, err := s.QueryDomainFootprint(context.Background(), DomainFootprintRequest{Product: "no-such-product", Paths: []string{"a.go"}})
	assertFailureKind(t, err, KindUnknownScope)

	// The published schema bounds paths and behavior in characters, so a
	// multibyte selector at the bound is admitted, not refused for its bytes.
	_, err = s.QueryDomainFootprint(context.Background(), DomainFootprintRequest{
		Product:  footprintProduct,
		Paths:    []string{strings.Repeat("é", footprintMaxPath)},
		Behavior: strings.Repeat("é", footprintMaxBehavior),
	})
	var failure *Failure
	if failureAs(err, &failure) && failure.Kind == KindInvalidInput {
		t.Fatalf("multibyte selectors at the character bound refused: %v", err)
	}
}

// TestCommittedCorpusFeedsTheFootprintOfWorkflowDesign proves the
// repository's own inputs carry the case the footprint read answers: the
// package map resolves internal/store/workflow_design.go, and CD-0128 and
// CD-0133 name it as evidence.
func TestCommittedCorpusFeedsTheFootprintOfWorkflowDesign(t *testing.T) {
	t.Parallel()
	root := repositoryRootForTest(t)
	manifest := composeWorkingTreeManifest(t, root)
	content, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(knowledgePackageMapPath)))
	if err != nil {
		t.Fatal(err)
	}
	shards := knowledgeShards{packageMap: content, packageMapPresent: true}
	architecture, err := prepareKnowledgeArchitecture(shards, manifest, true)
	if err != nil {
		t.Fatal(err)
	}
	roots := []DomainShapeRoot{}
	for _, entry := range architecture.Packages {
		roots = append(roots, DomainShapeRoot{Path: entry.Path, Role: "mixed", DomainIDs: entry.DomainIDs})
	}
	const file = "internal/store/workflow_design.go"
	if owner := domainShapeOwningRoot(roots, file); owner != "internal/store" {
		t.Fatalf("%s resolves to %q", file, owner)
	}
	naming := map[string]bool{}
	for _, record := range manifest.Records {
		for _, evidence := range record.Evidence {
			if evidence == file && record.Status == "accepted" {
				naming[record.ID] = true
			}
		}
	}
	if !naming["CD-0128"] || !naming["CD-0133"] {
		t.Fatalf("laws naming %s = %v", file, naming)
	}
}

// TestFootprintCursorBindingSeparatesFields proves no split of one request's
// bytes across behavior and paths reproduces another request's binding.
func TestFootprintCursorBindingSeparatesFields(t *testing.T) {
	t.Parallel()
	joined := footprintCursorBinding(DomainFootprintRequest{Product: "p", Behavior: "a\x00b"}, []string{"c"})
	split := footprintCursorBinding(DomainFootprintRequest{Product: "p", Behavior: "a"}, []string{"b", "c"})
	if joined == split {
		t.Fatal("distinct requests share one cursor binding")
	}
}

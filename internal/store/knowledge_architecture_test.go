package store

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

const architectureFixtureMap = `{
  "packages": [
    {"domain_ids": ["fixture-law"], "path": "internal/alpha"},
    {"domain_ids": ["fixture-law"], "path": "internal/alpha/nested"}
  ],
  "schema_version": "1.0"
}
`

const architectureFixtureCoverage = `{
  "evidence": [
    {"kind": "go_test", "value": "internal/alpha.TestAlpha"},
    {"kind": "validator", "value": "scripts/check-alpha.py"}
  ],
  "id": "CD-0001",
  "state": "satisfied"
}
`

const architectureFixtureLessonCoverage = `{
  "id": "LES-0001",
  "reason": "Lessons are published guidance, not executable law.",
  "state": "out_of_scope"
}
`

// architectureFixtureRepo commits one decision whose evidence names two code
// files (one twice), a lesson, the package map, and both records' coverage.
func architectureFixtureRepo(t *testing.T) string {
	t.Helper()
	repo := initKnowledgeRepo(t)
	writeManifestFixture(t, repo,
		manifestFixture{ID: "CD-0001", Kind: "decision", Path: ".concord/docs/decisions/CD-0001-alpha.md", Status: "accepted", Date: "2026-08-10T00:00:00Z", Title: "Alpha law", Summary: "Governs alpha", Scopes: architectureFixtureScopes(), Evidence: []string{"internal/alpha/a.go", "internal/alpha/nested/b.go", "internal/alpha/a.go"}},
		manifestFixture{ID: "LES-0001", Kind: "lesson", Path: ".concord/docs/lessons/LES-0001-alpha.md", Status: "published", Date: "2026-08-10T00:00:00Z", Title: "Alpha lesson", Summary: "A lesson", Scopes: architectureFixtureScopes()},
	)
	writeKnowledgeFile(t, repo, knowledgePackageMapPath, architectureFixtureMap)
	writeKnowledgeFile(t, repo, knowledgeCoverageTreeDir+"CD-0001.json", architectureFixtureCoverage)
	writeKnowledgeFile(t, repo, knowledgeCoverageTreeDir+"LES-0001.json", architectureFixtureLessonCoverage)
	commitKnowledgeRepo(t, repo, "architecture fixture")
	return repo
}

func architectureFixtureScopes() KnowledgeRecordScopes {
	return KnowledgeRecordScopes{Mode: "explicit", DomainIDs: []string{"fixture-law"}}
}

func architectureFixtureStore(t *testing.T, repo string) (*Store, KnowledgeHome) {
	t.Helper()
	s := openTemp(t)
	home := KnowledgeHome{HomeProjectID: "arch-project", HomeLocatorID: "arch-locator", RepoPath: repo, HeadRef: "HEAD"}
	authorizeKnowledgeProductHome(t, s, "arch-product", home)
	if err := s.RebuildKnowledgeIndex(context.Background(), home); err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	return s, home
}

// architectureSnapshot renders every projected architecture row of one home,
// so a refused rebuild can be compared with the projection before it.
func architectureSnapshot(t *testing.T, s *Store, home KnowledgeHome) string {
	t.Helper()
	queries := []string{
		`SELECT package_path||'='||domain_id||'#'||map_content_hash||'@'||scanned_commit_oid FROM knowledge_package_domains WHERE home_project_id=? AND home_locator_id=? ORDER BY package_path, domain_id`,
		`SELECT law_id||':'||path||'@'||scanned_commit_oid FROM law_evidence_paths WHERE home_project_id=? AND home_locator_id=? ORDER BY law_id, path`,
		`SELECT law_id||'#'||ordinal||'='||kind||':'||value||'@'||scanned_commit_oid FROM law_coverage_anchors WHERE home_project_id=? AND home_locator_id=? ORDER BY law_id, ordinal`,
	}
	var parts []string
	for _, query := range queries {
		rows, err := s.DatabaseForTesting().Query(query, home.HomeProjectID, home.HomeLocatorID)
		if err != nil {
			t.Fatal(err)
		}
		var values []string
		for rows.Next() {
			var value string
			if err := rows.Scan(&value); err != nil {
				t.Fatal(err)
			}
			values = append(values, value)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		_ = rows.Close()
		parts = append(parts, strings.Join(values, ","))
	}
	return strings.Join(parts, "\n")
}

func headCommit(t *testing.T, repo string) string {
	t.Helper()
	commit, err := resolveKnowledgeHead(context.Background(), KnowledgeHome{HomeProjectID: "head", HomeLocatorID: "head", RepoPath: repo, HeadRef: "HEAD"})
	if err != nil {
		t.Fatal(err)
	}
	return commit
}

// TestKnowledgeIndexProjectsPackageMapEvidenceAndCoverageAtTheScannedCommit
// proves the rebuild reads the map and coverage shards at the indexed commit:
// the map's Domains, each law's distinct evidence paths, and a satisfied
// law's anchors in authored order project; an out-of-scope record projects
// none; and a later commit's map replaces the earlier one.
func TestKnowledgeIndexProjectsPackageMapEvidenceAndCoverageAtTheScannedCommit(t *testing.T) {
	t.Parallel()
	repo := architectureFixtureRepo(t)
	s, home := architectureFixtureStore(t, repo)
	first := headCommit(t, repo)
	document, err := parseKnowledgePackageMap([]byte(architectureFixtureMap), KnowledgeDomainRegistry{Domains: []KnowledgeDomain{{DomainID: "fixture-law"}}})
	if err != nil {
		t.Fatal(err)
	}
	firstHash := knowledgePackageMapContentHash(document)
	want := strings.Join([]string{
		"internal/alpha=fixture-law#" + firstHash + "@" + first + ",internal/alpha/nested=fixture-law#" + firstHash + "@" + first,
		"CD-0001:internal/alpha/a.go@" + first + ",CD-0001:internal/alpha/nested/b.go@" + first,
		"CD-0001#0=go_test:internal/alpha.TestAlpha@" + first + ",CD-0001#1=validator:scripts/check-alpha.py@" + first,
	}, "\n")
	if got := architectureSnapshot(t, s, home); got != want {
		t.Fatalf("architecture projection:\n%s\nwant:\n%s", got, want)
	}

	writeKnowledgeFile(t, repo, knowledgePackageMapPath, `{"packages":[{"domain_ids":["fixture-law"],"path":"internal/beta"}],"schema_version":"1.0"}`+"\n")
	commitKnowledgeRepo(t, repo, "map moves to beta")
	second := headCommit(t, repo)
	if err := s.EnsureKnowledgeIndexFresh(context.Background(), home); err != nil {
		t.Fatal(err)
	}
	got := architectureSnapshot(t, s, home)
	if !strings.Contains(got, "internal/beta=fixture-law#sha256:") || !strings.Contains(got, "@"+second) || strings.Contains(got, "internal/alpha=") || strings.Contains(got, firstHash) {
		t.Fatalf("second commit's map did not replace the first:\n%s", got)
	}
}

// TestKnowledgeIndexRefusesMalformedPackageMapOrCoverageWithoutPartialProjection
// proves each malformed shape refuses the whole rebuild and leaves the
// previous projection, law rows included, exactly as it was.
func TestKnowledgeIndexRefusesMalformedPackageMapOrCoverageWithoutPartialProjection(t *testing.T) {
	t.Parallel()
	anchors := make([]string, 33)
	for index := range anchors {
		anchors[index] = fmt.Sprintf(`{"kind":"go_test","value":"internal/alpha.Test%d"}`, index)
	}
	cases := []struct {
		name, path, content string
		kind                FailureKind
	}{
		{"map unknown field", knowledgePackageMapPath, `{"packages":[{"domain_ids":["fixture-law"],"path":"internal/alpha"}],"schema_version":"1.0","files":[]}`, KindInvalidNoteProof},
		{"map duplicate key", knowledgePackageMapPath, `{"packages":[],"packages":[{"domain_ids":["fixture-law"],"path":"internal/alpha"}],"schema_version":"1.0"}`, KindInvalidNoteProof},
		{"map wrong schema", knowledgePackageMapPath, `{"packages":[{"domain_ids":["fixture-law"],"path":"internal/alpha"}],"schema_version":"2.0"}`, KindInvalidNoteProof},
		{"map no packages", knowledgePackageMapPath, `{"packages":[],"schema_version":"1.0"}`, KindInvalidNoteProof},
		{"map glob path", knowledgePackageMapPath, `{"packages":[{"domain_ids":["fixture-law"],"path":"internal/*"}],"schema_version":"1.0"}`, KindInvalidNoteProof},
		{"map traversal path", knowledgePackageMapPath, `{"packages":[{"domain_ids":["fixture-law"],"path":"../internal"}],"schema_version":"1.0"}`, KindInvalidNoteProof},
		{"map trailing slash", knowledgePackageMapPath, `{"packages":[{"domain_ids":["fixture-law"],"path":"internal/alpha/"}],"schema_version":"1.0"}`, KindInvalidNoteProof},
		{"map duplicate path", knowledgePackageMapPath, `{"packages":[{"domain_ids":["fixture-law"],"path":"internal/alpha"},{"domain_ids":["fixture-law"],"path":"internal/alpha"}],"schema_version":"1.0"}`, KindInvalidNoteProof},
		{"map repeated Domain", knowledgePackageMapPath, `{"packages":[{"domain_ids":["fixture-law","fixture-law"],"path":"internal/alpha"}],"schema_version":"1.0"}`, KindInvalidNoteProof},
		{"map unknown Domain", knowledgePackageMapPath, `{"packages":[{"domain_ids":["nowhere"],"path":"internal/alpha"}],"schema_version":"1.0"}`, KindUnknownScope},
		{"coverage unknown kind", knowledgeCoverageTreeDir + "CD-0001.json", `{"evidence":[{"kind":"path","value":"internal/alpha/a.go"}],"id":"CD-0001","state":"satisfied"}`, KindInvalidNoteProof},
		{"coverage too many anchors", knowledgeCoverageTreeDir + "CD-0001.json", `{"evidence":[` + strings.Join(anchors, ",") + `],"id":"CD-0001","state":"satisfied"}`, KindInvalidNoteProof},
		{"coverage unknown field", knowledgeCoverageTreeDir + "CD-0001.json", `{"evidence":[{"kind":"go_test","value":"internal/alpha.TestAlpha"}],"id":"CD-0001","state":"satisfied","note":"x"}`, KindInvalidNoteProof},
		{"coverage obligation mismatch", knowledgeCoverageTreeDir + "CD-0001.json", `{"evidence":[{"kind":"go_test","value":"internal/alpha.TestAlpha"}],"id":"CD-0001","reason":"twice justified here","state":"satisfied"}`, KindInvalidNoteProof},
		{"coverage unknown state", knowledgeCoverageTreeDir + "CD-0001.json", `{"id":"CD-0001","reason":"not a coverage state","state":"pending"}`, KindInvalidNoteProof},
		{"coverage misnamed", knowledgeCoverageTreeDir + "CD-0002.json", `{"id":"CD-0001","reason":"named for another record","state":"unmeasured"}`, KindInvalidNoteProof},
		{"coverage names no record", knowledgeCoverageTreeDir + "CD-0099.json", `{"id":"CD-0099","reason":"no such record exists","state":"unmeasured"}`, KindInvalidNoteProof},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			repo := architectureFixtureRepo(t)
			s, home := architectureFixtureStore(t, repo)
			before := architectureSnapshot(t, s, home) + "\n" + domainProjectionIdentitySnapshot(t, s)
			var laws int
			if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM law_subjects WHERE home_project_id=?`, home.HomeProjectID).Scan(&laws); err != nil || laws != 1 {
				t.Fatalf("fixture premise: law rows=%d err=%v", laws, err)
			}
			writeKnowledgeFile(t, repo, tc.path, tc.content+"\n")
			commitKnowledgeRepo(t, repo, tc.name)
			err := s.RebuildKnowledgeIndex(context.Background(), home)
			if err == nil {
				t.Fatal("malformed architecture data rebuilt")
			}
			assertFailureKind(t, err, tc.kind)
			if after := architectureSnapshot(t, s, home) + "\n" + domainProjectionIdentitySnapshot(t, s); after != before {
				t.Fatalf("refused rebuild changed the projection:\nbefore:\n%s\nafter:\n%s", before, after)
			}
		})
	}
}

// TestKnowledgeIndexWithoutPackageMapProjectsNoPackages proves an absent map
// is not an error: no package row and so no map hash projects, while
// evidence paths and anchors still project.
func TestKnowledgeIndexWithoutPackageMapProjectsNoPackages(t *testing.T) {
	t.Parallel()
	repo := architectureFixtureRepo(t)
	if err := os.Remove(filepath.Join(repo, filepath.FromSlash(knowledgePackageMapPath))); err != nil {
		t.Fatal(err)
	}
	commitKnowledgeRepo(t, repo, "no package map")
	s, home := architectureFixtureStore(t, repo)
	lines := strings.Split(architectureSnapshot(t, s, home), "\n")
	if len(lines) != 3 || lines[0] != "" || lines[1] == "" || lines[2] == "" {
		t.Fatalf("absent-map projection = %q", lines)
	}
}

// TestFreshWatermarkOverAnOlderWritersPackageMapRebuilds proves a watermark
// an older binary stamped over the same commit, without the architecture
// rows, reads stale and rebuilds on demand.
func TestFreshWatermarkOverAnOlderWritersPackageMapRebuilds(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := architectureFixtureRepo(t)
	s, home := architectureFixtureStore(t, repo)
	want := architectureSnapshot(t, s, home)
	db := s.DatabaseForTesting()
	for _, statement := range []string{`INSERT INTO write_guard(active) VALUES(1)`, `DELETE FROM knowledge_package_domains`, `DELETE FROM law_coverage_anchors`, `DELETE FROM law_evidence_paths`, `UPDATE knowledge_index_watermark SET projection_version=2`, `DELETE FROM write_guard`} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	watermark, err := readKnowledgeWatermark(ctx, db, home, headCommit(t, repo))
	if err != nil || watermark.Fresh {
		t.Fatalf("an older binary's watermark read fresh: watermark=%+v err=%v", watermark, err)
	}
	if err := s.EnsureKnowledgeIndexFresh(ctx, home); err != nil {
		t.Fatal(err)
	}
	if got := architectureSnapshot(t, s, home); got != want {
		t.Fatalf("demand rebuild did not restore the projection:\n%s\nwant:\n%s", got, want)
	}
}

// TestKnowledgeArchitectureIsReadOnlyForTheDesignatedHome proves a home that
// is not its Product's designated home parses neither file, so it gains no
// rebuild refusal from them.
func TestKnowledgeArchitectureIsReadOnlyForTheDesignatedHome(t *testing.T) {
	t.Parallel()
	shards := knowledgeShards{packageMap: []byte("{"), packageMapPresent: true, coverage: map[string][]byte{"X.json": []byte("{")}}
	architecture, err := prepareKnowledgeArchitecture(shards, KnowledgeManifest{}, false)
	if err != nil || architecture.MapHash != "" || len(architecture.Packages) != 0 || len(architecture.Anchors) != 0 {
		t.Fatalf("non-designated architecture = %+v err=%v", architecture, err)
	}
	if _, err := prepareKnowledgeArchitecture(shards, KnowledgeManifest{}, true); err == nil {
		t.Fatal("control: the designated home accepted a malformed map")
	}
}

// TestCommittedPackageMapAndCoverageParseUnderTheStoreRules proves the
// repository's own map and every committed coverage shard pass the strict
// rebuild parsers against the composed manifest, so the Product's index
// rebuilds.
func TestCommittedPackageMapAndCoverageParseUnderTheStoreRules(t *testing.T) {
	t.Parallel()
	root := repositoryRootForTest(t)
	manifest := composeWorkingTreeManifest(t, root)
	shards := knowledgeShards{coverage: map[string][]byte{}}
	content, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(knowledgePackageMapPath)))
	if err != nil {
		t.Fatal(err)
	}
	if len(content) > maxKnowledgeRecord {
		t.Fatalf("package map is %d bytes; the shard archive admits at most %d", len(content), maxKnowledgeRecord)
	}
	shards.packageMap, shards.packageMapPresent = content, true
	dir := filepath.Join(root, filepath.FromSlash(knowledgeCoverageTreeDir))
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		shards.coverage[entry.Name()] = data
	}
	architecture, err := prepareKnowledgeArchitecture(shards, manifest, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(architecture.Packages) == 0 || len(architecture.Anchors) == 0 {
		t.Fatalf("committed architecture projected %d packages and %d anchored records", len(architecture.Packages), len(architecture.Anchors))
	}
	paths := make([]string, 0, len(architecture.Packages))
	for _, entry := range architecture.Packages {
		paths = append(paths, entry.Path)
	}
	if !sort.StringsAreSorted(paths) {
		t.Fatal("prepared packages are not sorted by path")
	}
}

// TestCoverageShardReasonCountsCharacters proves the store bounds a coverage
// reason in characters, as scripts/coverage_state.py does, so a reason the
// validator admits never refuses the index rebuild.
func TestCoverageShardReasonCountsCharacters(t *testing.T) {
	t.Parallel()
	shard := func(reason string) []byte {
		data, err := json.Marshal(map[string]any{"id": "CD-0001", "state": "unmeasured", "reason": reason})
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	if _, _, err := parseKnowledgeCoverageShard("CD-0001.json", shard(strings.Repeat("é", maxCoverageReason))); err != nil {
		t.Fatalf("reason of %d multibyte characters refused: %v", maxCoverageReason, err)
	}
	if _, _, err := parseKnowledgeCoverageShard("CD-0001.json", shard(strings.Repeat("é", maxCoverageReason+1))); err == nil {
		t.Fatalf("reason of %d characters admitted", maxCoverageReason+1)
	}
}

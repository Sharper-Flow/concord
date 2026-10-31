package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Shared committed-git-home test fixtures. Every test that reads law through
// the committed law folder builds its synthetic home through these helpers:
// the repo starts on main, so HEAD is the default branch the loader resolves.

func initKnowledgeRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	runKnowledgeGit(t, repo, "init", "--initial-branch=main")
	runKnowledgeGit(t, repo, "config", "user.email", "test@example.invalid")
	runKnowledgeGit(t, repo, "config", "user.name", "Concord Test")
	return repo
}

func writeKnowledgeFile(t *testing.T, repo, path, content string) {
	t.Helper()
	full := filepath.Join(repo, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func commitKnowledgeRepo(t *testing.T, repo, message string) string {
	t.Helper()
	runKnowledgeGit(t, repo, "add", "--", ".")
	runKnowledgeGit(t, repo, "commit", "--quiet", "--date", "2026-08-07T00:00:00Z", "-m", message, "--author", "Concord Test <test@example.invalid>")
	return strings.TrimSpace(runKnowledgeGit(t, repo, "rev-parse", "HEAD"))
}

func runKnowledgeGit(t *testing.T, repo string, args ...string) string {
	t.Helper()
	cmd, err := newTestGitCommand(repo, args...)
	if err != nil {
		t.Fatal(err)
	}
	cmd.Env = append(cmd.Env, "GIT_AUTHOR_DATE=2026-08-07T00:00:00Z", "GIT_COMMITTER_DATE=2026-08-07T00:00:00Z")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// fixtureLawFolderRegistry is the minimal valid Domain registry a synthetic
// home commits: the Product root, the law fixture's home Domain, and the
// root/child/child-alpha Domain identities the workflow binding fixtures
// name.
const fixtureLawFolderRegistry = `{"schema_version":"1.0","product_key":"product","root_domain_id":"product-root:product","domains":[{"domain_id":"product-root:product","name":"Product","purpose":"Governs the synthetic fixture Product.","status":"current","architecture_relations":[]},{"domain_id":"product-root:fixture","name":"Law fixture home","purpose":"The committed spec fixture's home Domain.","status":"current","parent_domain_id":"product-root:product","architecture_relations":[]},{"domain_id":"root","name":"Root","purpose":"Fixture root child Domain.","status":"current","parent_domain_id":"product-root:product","architecture_relations":[]},{"domain_id":"child","name":"Child","purpose":"Fixture child Domain.","status":"current","parent_domain_id":"product-root:product","architecture_relations":[]},{"domain_id":"child-alpha","name":"Affected child","purpose":"The approved affected child.","status":"current","parent_domain_id":"product-root:product","architecture_relations":[]}]}` + "\n"

// fixtureLawFolderLaw is one minimal committed law homed at the fixture
// registry's root child Domain — the Domain every workflow binding fixture
// affects, matching the law_domain_homes row the retired projection seeded. A
// child-homed law states no product-wide rationale; only root-homed law does.
func fixtureLawFolderLaw(id string) string {
	return "---\n" +
		`{"id":"` + id + `","title":"Fixture folder law ` + id + `","authority":{"tier":"derived"},"home_domain_id":"root","applies_to_domain_ids":[]}` + "\n" +
		"---\n\nSynthetic fixture law body.\n"
}

// writeFixtureLawSource commits a minimal valid law folder and Domain registry
// into a synthetic home, so a later pre-transaction verification reads a
// real committed snapshot from it instead of a seeded projection watermark.
// Laws land under decisions/.
func writeFixtureLawSource(t *testing.T, home KnowledgeHome, lawIDs ...string) string {
	t.Helper()
	return writeFixtureTierLawSource(t, home, "decisions", lawIDs...)
}

// writeFixtureSpecLawSource commits the fixture laws under the specs tier.
func writeFixtureSpecLawSource(t *testing.T, home KnowledgeHome, lawIDs ...string) string {
	t.Helper()
	return writeFixtureTierLawSource(t, home, "specs", lawIDs...)
}

func writeFixtureTierLawSource(t *testing.T, home KnowledgeHome, tier string, lawIDs ...string) string {
	t.Helper()
	writeKnowledgeFile(t, home.RepoPath, ".concord/docs/knowledge/domain-registry.json", fixtureLawFolderRegistry)
	for _, lawID := range lawIDs {
		writeKnowledgeFile(t, home.RepoPath, ".concord/law/"+tier+"/"+strings.ReplaceAll(strings.ReplaceAll(lawID, ":", "-"), "/", "-")+"-fixture.md", fixtureLawFolderLaw(lawID))
	}
	return commitKnowledgeRepo(t, home.RepoPath, "fixture law folder source")
}

func authorizeKnowledgeLocator(t *testing.T, s *Store, home KnowledgeHome) {
	t.Helper()
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := s.DatabaseForTesting().Exec(`DELETE FROM write_guard`); err != nil {
			t.Errorf("remove write guard: %v", err)
		}
	}()
	if _, err := s.DatabaseForTesting().Exec(`INSERT OR IGNORE INTO projects(id,display_name,version,created_at,updated_at) VALUES(?, ?, 1, 'now', 'now')`, home.HomeProjectID, home.HomeProjectID); err != nil {
		t.Fatal(err)
	}
	anchorProductID := "anchor-product-" + home.HomeProjectID
	if _, err := s.DatabaseForTesting().Exec(`INSERT OR IGNORE INTO products(id,display_name,stage_maturity,stage_audience_commitment,version,created_at,updated_at) VALUES(?, ?, 'prototype', 'operator_only', 1, 'now', 'now')`, anchorProductID, anchorProductID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT OR IGNORE INTO product_projects(product_id,project_id,role) VALUES(?,?,'secondary')`, anchorProductID, home.HomeProjectID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT OR IGNORE INTO project_locators(locator_id,project_id,kind,locator_value,normalized_value,created_at,updated_at) VALUES(?, ?, 'canonical_path', ?, ?, 'now', 'now')`, home.HomeLocatorID, home.HomeProjectID, home.RepoPath, home.RepoPath); err != nil {
		t.Fatal(err)
	}
}

func authorizeKnowledgeProductHome(t *testing.T, s *Store, productID string, home KnowledgeHome, membershipProjects ...string) {
	t.Helper()
	authorizeKnowledgeLocator(t, s, home)
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := s.DatabaseForTesting().Exec(`DELETE FROM write_guard`); err != nil {
			t.Errorf("remove write guard: %v", err)
		}
	}()
	if _, err := s.DatabaseForTesting().Exec(`INSERT OR IGNORE INTO products(id,display_name,stage_maturity,stage_audience_commitment,version,created_at,updated_at) VALUES(?, ?, 'prototype', 'operator_only', 1, 'now', 'now')`, productID, productID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT OR IGNORE INTO product_knowledge_homes(product_id,project_id,locator_id) VALUES(?, ?, ?)`, productID, home.HomeProjectID, home.HomeLocatorID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().Exec(`DELETE FROM write_guard`); err != nil {
		t.Fatal(err)
	}
	for _, projectID := range membershipProjects {
		if _, err := s.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1)`); err != nil {
			t.Fatal(err)
		}
		if _, err := s.DatabaseForTesting().Exec(`INSERT OR IGNORE INTO projects(id,display_name,version,created_at,updated_at) VALUES(?, ?, 1, 'now', 'now')`, projectID, projectID); err != nil {
			t.Fatal(err)
		}
		if _, err := s.DatabaseForTesting().Exec(`INSERT OR IGNORE INTO product_projects(product_id,project_id,role) VALUES(?, ?, 'secondary')`, productID, projectID); err != nil {
			t.Fatal(err)
		}
		if _, err := s.DatabaseForTesting().Exec(`DELETE FROM write_guard`); err != nil {
			t.Fatal(err)
		}
	}
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

func canonicalWorkNote(id, completed string) string {
	return "---\n" +
		"concord_work_id: " + id + "\n" +
		"work_type: implementation\n" +
		"title: Auth release\n" +
		"completed_at: " + completed + "\n" +
		"outcome_tag: shipped\n" +
		"lesson_tags: [sqlite, state-authority]\n" +
		"terminal_state: completed\n" +
		"priority: 2\n" +
		"summary: Bounded summary\n" +
		"product_ids: [prod-alpha]\n" +
		"project_ids: [proj-web]\n" +
		"domain_ids: [auth]\n" +
		"tag_ids: [auth, release]\n" +
		"---\n\nDurable note.\n"
}

// seedQueryFixture seeds one Product, Project, membership, two work items, and
// a blocks relation, the shared read-projection fixture.
func seedQueryFixture(t *testing.T) *Store {
	t.Helper()
	s := openTemp(t)
	ctx := context.Background()
	if err := ApplyOperation(ctx, s, Operation{
		Events: []Event{
			operationEvent("q-product", "product.created", SubjectProduct, "prod", map[string]any{
				"display_name": "Product", "stage_maturity": "prototype", "stage_audience_commitment": "operator_only",
			}),
			operationEvent("q-project", "project.created", SubjectProject, "proj", map[string]any{"display_name": "Project"}),
			operationEvent("q-membership", "product_project.added", SubjectProduct, "prod", map[string]any{
				"product_id": "prod", "project_id": "proj", "role": "primary", "reason": "fixture", "expected_version": 1, "resulting_version": 2,
			}),
		},
		ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProduct, "prod"): 0, VersionRef(SubjectProject, "proj"): 0},
	}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"blocker", "blocked"} {
		if err := ApplyOperation(ctx, s, Operation{
			Events: []Event{
				workCreatedEvent(id, "q-create-"+id),
				operationEvent("q-project-"+id, "work_project.added", SubjectWorkItem, id, map[string]any{
					"work_id": id, "project_id": "proj", "role": "primary", "reason": "fixture", "expected_version": 1, "resulting_version": 2,
				}),
			},
			ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, id): 0},
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{
		relationAddedEvent("q-blocks", "blocks", "blocker", "blocked", 2, 3),
	}}); err != nil {
		t.Fatal(err)
	}
	return s
}

// seedEventDerivedLocator seeds one Product, Project, and canonical-path
// locator through the event route, the standalone-home fixture anchor.
func seedEventDerivedLocator(t *testing.T, s *Store, projectID, locatorID, path string) {
	t.Helper()
	ctx := context.Background()
	if _, err := s.CreateProductWithProject(ctx, ProductCreation{
		ProductID: "anchor-product-" + projectID, DisplayName: "Anchor Product",
		StageMaturity: "prototype", StageAudienceCommitment: "operator_only",
		ProjectID: projectID, ProjectDisplayName: "Anchor Project", Role: "primary",
	}); err != nil {
		t.Fatal(err)
	}
	normalized, err := NormalizeProjectLocator(LocatorCanonicalPath, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddProjectLocator(ctx, projectID, ProjectLocator{
		ID: locatorID, ProjectID: projectID, Kind: LocatorCanonicalPath,
		Value: path, NormalizedValue: normalized,
	}, 1); err != nil {
		t.Fatal(err)
	}
}

// seedKnowledgeWorkLifecycle seeds the shared terminal-cleanup fixture
// lifecycle: prod-alpha/proj-web with one work item, optionally completed.
func seedKnowledgeWorkLifecycle(t *testing.T, s *Store, id, title string, completed bool) {
	t.Helper()
	ctx := context.Background()
	events := []Event{
		{EventID: "create-prod-" + id, Kind: "product.created", SubjectType: SubjectProduct, SubjectID: "prod-alpha", Actor: "operator", OccurredAt: time.Now().UTC(), PayloadVersion: 1, Payload: []byte(`{"display_name":"Alpha","stage_maturity":"production","stage_audience_commitment":"operator_only"}`)},
		{EventID: "create-proj-" + id, Kind: "project.created", SubjectType: SubjectProject, SubjectID: "proj-web", Actor: "operator", OccurredAt: time.Now().UTC(), PayloadVersion: eventKindRegistry["project.created"].CurrentVersion, Payload: []byte(`{"display_name":"Web"}`)},
		{EventID: "product-project-" + id, Kind: "product_project.added", SubjectType: SubjectProduct, SubjectID: "prod-alpha", Actor: "operator", OccurredAt: time.Now().UTC(), PayloadVersion: 1, Payload: []byte(`{"product_id":"prod-alpha","project_id":"proj-web","role":"primary","reason":"fixture","expected_version":1,"resulting_version":2}`)},
		{EventID: "create-work-" + id, Kind: "work.created", SubjectType: SubjectWorkItem, SubjectID: id, Actor: "operator", OccurredAt: time.Now().UTC(), PayloadVersion: workCreatedCurrentVersion(), Payload: []byte(`{"work_kind":"task","title":"` + title + `","priority":2}`)},
		{EventID: "work-project-" + id, Kind: "work_project.added", SubjectType: SubjectWorkItem, SubjectID: id, Actor: "operator", OccurredAt: time.Now().UTC(), PayloadVersion: 1, Payload: []byte(`{"work_id":"` + id + `","project_id":"proj-web","role":"primary","reason":"fixture","expected_version":1,"resulting_version":2}`)},
	}
	if err := ApplyOperation(ctx, s, Operation{Events: events, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProduct, "prod-alpha"): 0, VersionRef(SubjectProject, "proj-web"): 0, VersionRef(SubjectWorkItem, id): 0}}); err != nil {
		t.Fatal(err)
	}
	if completed {
		if err := ApplyOperation(ctx, s, Operation{Events: []Event{{EventID: "complete-" + id, Kind: "work.transitioned", SubjectType: SubjectWorkItem, SubjectID: id, Actor: "operator", OccurredAt: time.Now().UTC(), PayloadVersion: 1, Payload: []byte(`{"from":"needed","to":"completed","reason":"fixture","expected_version":2,"resulting_version":3}`)}}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, id): 2}}); err != nil {
			t.Fatal(err)
		}
	}
}

// seedArchivedWorkNote writes the archived linkage rows the compaction-link
// fold writes for one committed note, from the note's own parsed metadata.
// The retired note rebuild used to write these rows from the same bytes; the
// compaction publication route is the retained writer.
func seedArchivedWorkNote(t *testing.T, s *Store, home KnowledgeHome, commitOID, notePath, content string) string {
	t.Helper()
	note, err := parseKnowledgeNote([]byte(content))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(content))
	hash := "sha256:" + hex.EncodeToString(sum[:])
	db := s.DatabaseForTesting()
	memberMigrationExec(t, db, `INSERT INTO write_guard(active) VALUES(1)`)
	defer memberMigrationExec(t, db, `DELETE FROM write_guard`)
	memberMigrationExec(t, db, `INSERT OR REPLACE INTO archived_work(id,type,title,completed_at,outcome_tag,lesson_tags,terminal_state,priority,summary,successor_work_id,home_project_id,home_locator_id,note_path,commit_oid,content_hash,scope_mode,manifest_schema_version) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		note.ID, note.Kind, note.Title, note.CompletedAt, note.OutcomeTag, marshalStrings(note.LessonTags), note.TerminalState, note.Priority, note.Summary, nullableText(note.SuccessorID), home.HomeProjectID, home.HomeLocatorID, notePath, commitOID, hash, note.ScopeMode, "")
	for table, values := range map[string][]string{"archived_work_products": note.ProductIDs, "archived_work_projects": note.ProjectIDs, "archived_work_domains": note.DomainIDs, "archived_work_tags": note.TagIDs} {
		column := map[string]string{"archived_work_products": "product_id", "archived_work_projects": "project_id", "archived_work_domains": "domain_id", "archived_work_tags": "tag_id"}[table]
		for _, value := range values {
			memberMigrationExec(t, db, "INSERT INTO "+table+" (home_project_id, home_locator_id, work_id, "+column+") VALUES (?, ?, ?, ?)", home.HomeProjectID, home.HomeLocatorID, note.ID, value) //nolint:gosec // table and column come from the closed scope maps and all values stay parameter-bound.
		}
	}
	return hash
}

// seedKnowledgeWork seeds one work item in the shared prod-alpha/proj-web
// fixture, completed when the identity is work-done.
func seedKnowledgeWork(t *testing.T, s *Store, id, title string) {
	t.Helper()
	seedKnowledgeWorkLifecycle(t, s, id, title, id == "work-done")
}

// seedKnowledgeHomeProduct seeds one Product with one committed law-folder
// home carrying the fixture law. The legacy path and title arguments are
// accepted for call-site stability; folder law derives placement and title
// from the committed file.
func seedKnowledgeHomeProduct(t *testing.T, productID string, home KnowledgeHome, lawID, _, _ string) (*Store, KnowledgeHome) {
	t.Helper()
	home.RepoPath = initKnowledgeRepo(t)
	writeFixtureLawSource(t, home, lawID)
	s := openTemp(t)
	authorizeKnowledgeProductHome(t, s, productID, home, home.HomeProjectID)
	return s, home
}

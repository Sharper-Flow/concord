package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
	"github.com/sharper-flow/concord/internal/version"
)

// The guarded law-index retirement (schema 174) keeps the concord upgrade
// route's mandatory verified snapshot: the route must write and verify a
// snapshot of the pre-upgrade v173 store before the drop, the live store must
// lose the legacy law-index objects only through a folder-accounted upgrade,
// and a snapshot failure must apply no migration. These tests drive the real
// concord upgrade route over a v173 store whose legacy law rows anchor a
// synthetic committed Git home.

var cliLawIndexFixtureTables = []string{
	"law_subjects", "law_bodies", "law_domain_homes", "knowledge_index_watermark",
	"projects", "project_locators", "products", "product_projects",
	"product_knowledge_homes", "work_items", "work_projects",
}

// cliLawIndexRetiredPrefixes mirrors the store-side retired-object contract.
var cliLawIndexRetiredPrefixes = []string{
	"law_subjects", "law_relations", "law_bodies", "law_domain_homes",
	"law_domain_applicability", "domain_relation_governing_laws",
	"knowledge_kind_coverage", "knowledge_index_watermark",
	"knowledge_publication_observations",
}

const (
	cliLawIndexProduct  = "law-index-product"
	cliLawIndexProject  = "law-index-project"
	cliLawIndexLocator  = "law-home-locator"
	cliLawIndexWork     = "law-index-work"
	cliLawIndexLawID    = "CD-0305"
	cliLawIndexLawPath  = ".concord/law/decisions/CD-0305-current.md"
	cliLawIndexRegistry = `{"schema_version":"1.0","product_key":"fixture","root_domain_id":"product-root:fixture","domains":[{"domain_id":"product-root:fixture","name":"Fixture","purpose":"Governs the synthetic fixture Product.","status":"current","architecture_relations":[]}]}` + "\n"
)

func cliLawIndexLawContent() string {
	return "---\n" +
		`{"id":"` + cliLawIndexLawID + `","title":"CLI law index retirement law","authority":{"tier":"derived"},"home_domain_id":"product-root:fixture","applies_to_domain_ids":[],"product_wide_rationale":"This synthetic law governs the whole fixture Product."}` + "\n" +
		"---\n\nSynthetic fixture law body.\n"
}

func cliLawIndexContentHash(content string) string {
	sum := sha256.Sum256([]byte(content))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func runLawIndexGit(t *testing.T, repo string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", repo}, args...)...) //nolint:gosec // this fixture passes code-owned git argv values directly without a shell.
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_DATE=2026-08-07T00:00:00Z", "GIT_COMMITTER_DATE=2026-08-07T00:00:00Z")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// seedLawIndexCLIStore builds a v173 store whose legacy law rows anchor a
// synthetic committed Git home: the Product home binding resolves through the
// canonical-path locator to a repo whose committed law folder carries the
// seeded law under its recorded path with the row's whole-file digest.
func seedLawIndexCLIStore(t *testing.T, path string) string {
	t.Helper()
	seedCLIMigrationPrefix(t, path, 173)

	repo := t.TempDir()
	runLawIndexGit(t, repo, "init", "--initial-branch=main")
	runLawIndexGit(t, repo, "config", "user.email", "test@example.invalid")
	runLawIndexGit(t, repo, "config", "user.name", "Concord Test")
	for _, file := range []struct{ path, content string }{
		{".concord/docs/knowledge/domain-registry.json", cliLawIndexRegistry},
		{cliLawIndexLawPath, cliLawIndexLawContent()},
	} {
		full := filepath.Join(repo, filepath.FromSlash(file.path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil { //nolint:gosec // fixture repositories contain public Git content with repository-standard directory permissions.
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(file.content), 0o644); err != nil { //nolint:gosec // fixture repositories contain public Git content with repository-standard file permissions.
			t.Fatal(err)
		}
	}
	runLawIndexGit(t, repo, "add", "--", ".")
	runLawIndexGit(t, repo, "commit", "--quiet", "--date", "2026-08-07T00:00:00Z", "-m", "synthetic law index retirement home", "--author", "Concord Test <test@example.invalid>")
	commit := strings.TrimSpace(runLawIndexGit(t, repo, "rev-parse", "HEAD"))

	db, err := sql.Open("sqlite", store.SQLiteFileURI(path)+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	hash := cliLawIndexContentHash(cliLawIndexLawContent())
	// One explicit transaction for the whole seed window: the Domain registry
	// rows form a deferred-FK cycle (domain_registries.root_domain_id ->
	// domains, domains.registry_content_hash -> domain_registries), so the
	// cycle only validates at commit and per-statement autocommit inserts
	// would fail it.
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, statement := range []string{
		`INSERT INTO write_guard(active) VALUES(1)`,
		`INSERT INTO projects(id,display_name,version,created_at,updated_at) VALUES('` + cliLawIndexProject + `','Law Index Project',1,'2026-10-29T00:00:00Z','2026-10-29T00:00:00Z')`,
		`INSERT INTO project_locators(locator_id,project_id,kind,locator_value,normalized_value,created_at,updated_at) VALUES('` + cliLawIndexLocator + `','` + cliLawIndexProject + `','canonical_path','` + repo + `','` + repo + `','2026-10-29T00:00:00Z','2026-10-29T00:00:00Z')`,
		`INSERT INTO products(id,display_name,stage_maturity,stage_audience_commitment,version,created_at,updated_at) VALUES('` + cliLawIndexProduct + `','Law Index Product','prototype','operator_only',1,'2026-10-29T00:00:00Z','2026-10-29T00:00:00Z')`,
		`INSERT INTO product_projects(product_id,project_id,role) VALUES('` + cliLawIndexProduct + `','` + cliLawIndexProject + `','primary')`,
		`INSERT INTO work_items(id,kind,title,lifecycle,priority,version,created_at,updated_at,terminal_time) VALUES('` + cliLawIndexWork + `','task','Law index retirement witness','needed',1,1,'2026-10-29T00:00:00Z','2026-10-29T00:00:00Z',NULL)`,
		`INSERT INTO work_projects(work_id,project_id,role) VALUES('` + cliLawIndexWork + `','` + cliLawIndexProject + `','primary')`,
		`INSERT INTO product_knowledge_homes(product_id,project_id,locator_id) VALUES('` + cliLawIndexProduct + `','` + cliLawIndexProject + `','` + cliLawIndexLocator + `')`,
		`INSERT INTO domain_registries(product_id,home_project_id,home_locator_id,product_key,root_domain_id,schema_version,content_hash,scanned_commit_oid) VALUES('` + cliLawIndexProduct + `','` + cliLawIndexProject + `','` + cliLawIndexLocator + `','fixture','product-root:fixture','1.0','sha256:` + repeatCLI64() + `','commit')`,
		`INSERT INTO domains(home_project_id,home_locator_id,product_id,domain_id,name,purpose,status,registry_content_hash,scanned_commit_oid) VALUES('` + cliLawIndexProject + `','` + cliLawIndexLocator + `','` + cliLawIndexProduct + `','product-root:fixture','Fixture','Governs the synthetic fixture Product.','current','sha256:` + repeatCLI64() + `','commit')`,
		`INSERT INTO law_subjects(home_project_id,home_locator_id,law_id,kind,status,path,title,content_hash,scanned_commit_oid) VALUES('` + cliLawIndexProject + `','` + cliLawIndexLocator + `','` + cliLawIndexLawID + `','decision','accepted','` + cliLawIndexLawPath + `','CLI law index retirement law','` + hash + `','` + commit + `')`,
		`INSERT INTO law_bodies(home_project_id,home_locator_id,law_id,body,content_hash,scanned_commit_oid) VALUES('` + cliLawIndexProject + `','` + cliLawIndexLocator + `','` + cliLawIndexLawID + `','Synthetic fixture law body.','` + hash + `','` + commit + `')`,
		`INSERT INTO law_domain_homes(home_project_id,home_locator_id,law_id,product_id,domain_id,law_content_hash,scanned_commit_oid) VALUES('` + cliLawIndexProject + `','` + cliLawIndexLocator + `','` + cliLawIndexLawID + `','` + cliLawIndexProduct + `','product-root:fixture','` + hash + `','` + commit + `')`,
		`INSERT INTO knowledge_index_watermark(home_project_id,home_locator_id,head_ref,scanned_commit_oid,scanned_at,complete) VALUES('` + cliLawIndexProject + `','` + cliLawIndexLocator + `','HEAD','` + commit + `','2026-08-07T12:00:00Z',1)`,
		`INSERT INTO knowledge_kind_coverage(home_project_id,home_locator_id,head_ref,kind,coverage,reason,scanned_commit_oid) VALUES('` + cliLawIndexProject + `','` + cliLawIndexLocator + `','HEAD','work_note','indexed','canonical .concord/docs/work directory scanned','commit')`,
		`INSERT INTO knowledge_publication_observations(project_id,locator_id,source_binding,latest_record,success_record) VALUES('` + cliLawIndexProject + `','` + cliLawIndexLocator + `','sha256:` + repeatCLI64() + `','{"publication":"observed"}','')`,
		`DELETE FROM write_guard`,
	} {
		if _, err := tx.Exec(statement); err != nil {
			t.Fatalf("seed v173 law index storage (%s): %v", statement, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return repo
}

// cliLawIndexRows renders every row of one table, so the live store and the
// verified snapshot compare by content.
func cliLawIndexRows(t *testing.T, db *sql.DB, table string) []string {
	t.Helper()
	rows, err := db.Query(`SELECT * FROM "` + strings.ReplaceAll(table, `"`, `""`) + `" ORDER BY rowid`)
	if err != nil {
		t.Fatalf("read %s: %v", table, err)
	}
	defer func() { _ = rows.Close() }()
	columns, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	var rendered []string
	for rows.Next() {
		values := make([]any, len(columns))
		pointers := make([]any, len(columns))
		for i := range values {
			pointers[i] = &values[i]
		}
		if err := rows.Scan(pointers...); err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(values)
		if err != nil {
			t.Fatal(err)
		}
		rendered = append(rendered, string(encoded))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return rendered
}

func cliLawIndexState(t *testing.T, db *sql.DB) map[string][]string {
	t.Helper()
	state := make(map[string][]string, len(cliLawIndexFixtureTables))
	for _, table := range cliLawIndexFixtureTables {
		state[table] = cliLawIndexRows(t, db, table)
	}
	return state
}

// TestUpgradeRetiringLawIndexSnapshotsTheLawInventoryBeforeTheDrop runs the
// concord upgrade route over a v173 store with populated law-index rows and a
// committed folder home: the route must write a verified snapshot holding the
// pre-drop law inventory, the live store must lose the legacy law-index
// objects, and the preserved current state must not move.
func TestUpgradeRetiringLawIndexSnapshotsTheLawInventoryBeforeTheDrop(t *testing.T) {
	previous := version.Value
	version.Value = "v-test"
	t.Cleanup(func() { version.Value = previous })
	path, root := cliStoreRoot(t)
	seedLawIndexCLIStore(t, path)

	beforeDB := openRawSQLite(t, path)
	before := cliLawIndexState(t, beforeDB)
	if len(before["law_subjects"]) != 1 {
		t.Fatalf("fixture premise: law rows = %d, want 1", len(before["law_subjects"]))
	}
	if err := beforeDB.Close(); err != nil {
		t.Fatal(err)
	}

	code, out, errOut := runUpgradeStdin(t, `{}`)
	if code != 0 {
		t.Fatalf("the v173 to current upgrade must apply: %d %s", code, errOut)
	}
	var report struct {
		Applied       []int  `json:"applied"`
		SchemaVersion int    `json:"schema_version"`
		Backup        string `json:"backup"`
	}
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("upgrade output is not JSON: %v: %s", err, out)
	}
	var expectedApplied []int
	for schema := 174; schema <= store.CurrentSchemaVersion(); schema++ {
		expectedApplied = append(expectedApplied, schema)
	}
	if !reflect.DeepEqual(report.Applied, expectedApplied) || report.SchemaVersion != store.CurrentSchemaVersion() {
		t.Fatalf("applied = %+v schema = %d, want %v", report.Applied, report.SchemaVersion, expectedApplied)
	}
	if !strings.HasPrefix(report.Backup, filepath.Join(root, "backups")+string(filepath.Separator)) {
		t.Fatalf("the report must name a snapshot under the data root: %+v", report)
	}

	manifest, err := store.VerifyBackup(t.Context(), report.Backup)
	if err != nil {
		t.Fatalf("the pre-upgrade snapshot must verify: %v", err)
	}
	if manifest.SchemaVersion != 173 {
		t.Fatalf("snapshot manifest schema = %d, want the unmigrated 173", manifest.SchemaVersion)
	}

	snapshotDB := openRawSQLite(t, report.Backup)
	snapshot := cliLawIndexState(t, snapshotDB)
	if !reflect.DeepEqual(snapshot, before) {
		t.Fatalf("snapshot changed pre-upgrade rows:\nbefore: %v\nsnapshot: %v", before, snapshot)
	}
	if err := snapshotDB.Close(); err != nil {
		t.Fatal(err)
	}

	liveDB := openRawSQLite(t, path)
	for _, prefix := range cliLawIndexRetiredPrefixes {
		var objects int
		if err := liveDB.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE name LIKE ?`, prefix+"%").Scan(&objects); err != nil {
			t.Fatal(err)
		}
		if objects != 0 {
			t.Fatalf("the live store kept %d %s objects", objects, prefix)
		}
	}
	var head int
	if err := liveDB.QueryRow(`SELECT max(version) FROM schema_migrations`).Scan(&head); err != nil {
		t.Fatal(err)
	}
	if head != store.CurrentSchemaVersion() {
		t.Fatalf("live schema head = %d, want %d", head, store.CurrentSchemaVersion())
	}
	if after := cliLawIndexState(t, liveDB); !reflect.DeepEqual(after, before) {
		t.Fatalf("the upgrade changed preserved rows:\nbefore: %v\nafter: %v", before, after)
	}
	if err := liveDB.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestUpgradeSnapshotFailurePreventsTheLawIndexDrop overrides the route's
// snapshot seam to fail and proves the refusal: no migration applies, the
// legacy law-index rows survive, and the manifest stays at 173.
func TestUpgradeSnapshotFailurePreventsTheLawIndexDrop(t *testing.T) {
	previous := version.Value
	version.Value = "v-test"
	t.Cleanup(func() { version.Value = previous })
	path, root := cliStoreRoot(t)
	seedLawIndexCLIStore(t, path)

	beforeDB := openRawSQLite(t, path)
	before := cliLawIndexState(t, beforeDB)
	if err := beforeDB.Close(); err != nil {
		t.Fatal(err)
	}

	backup := snapshotDatabase
	t.Cleanup(func() { snapshotDatabase = backup })
	snapshotDatabase = func(ctx context.Context, source, destination string) (store.BackupManifest, error) {
		return store.BackupManifest{}, errors.New("synthetic snapshot failure")
	}

	code, _, errOut := runUpgradeStdin(t, `{}`)
	if code != 1 {
		t.Fatalf("a failed snapshot must fail the upgrade: %d", code)
	}
	if !strings.Contains(errOut, "no migration was applied") {
		t.Fatalf("a failed snapshot must refuse before any migration: %s", errOut)
	}

	liveDB := openRawSQLite(t, path)
	var head int
	if err := liveDB.QueryRow(`SELECT max(version) FROM schema_migrations`).Scan(&head); err != nil {
		t.Fatal(err)
	}
	if head != 173 {
		t.Fatalf("live schema head = %d, want the untouched 173", head)
	}
	if after := cliLawIndexState(t, liveDB); !reflect.DeepEqual(after, before) {
		t.Fatalf("the refused upgrade changed pre-upgrade rows:\nbefore: %v\nafter: %v", before, after)
	}
	var laws int
	if err := liveDB.QueryRow(`SELECT count(*) FROM law_subjects`).Scan(&laws); err != nil {
		t.Fatalf("the law tables must survive a failed snapshot: %v", err)
	}
	if laws != len(before["law_subjects"]) {
		t.Fatalf("law rows = %d, want the seeded %d", laws, len(before["law_subjects"]))
	}
	if err := liveDB.Close(); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(root, "backups"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("a failed snapshot step must leave no snapshot behind: %v", entries)
	}
}

package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// Schema 174 retires the legacy SQL law index under a guarded upgrade: before
// its SQLite transaction the upgrade compares the legacy inventory against the
// immutable committed law-folder snapshot of each canonical Product home, and
// revalidates the home, its source content, and the inventory before the drop.
// The folder is the authority: a row whose recorded path went stale is
// admitted when a committed law file carries its stable ID, and an inventory
// row the folder cannot account for refuses the upgrade as a retry-safe
// upgrade_blocked naming the Product, the home, and the files, changing
// nothing. Preserved state survives the drop unchanged: archived_work with
// its scope children, the Product home binding, observations, live work, and
// the verified PM10 predecessor snapshot.

const (
	lawIndexRetirementLocator = "law-home-locator"

	lawIndexCurrentLawID = "CD-0301"
	lawIndexMovedLawID   = "CD-0302"
	lawIndexMissingLawID = "CD-0303"
	lawIndexBrokenLawID  = "CD-0304"

	// lawIndexCurrentPath is the recorded path of a law the folder still
	// holds at the same place. lawIndexMovedOldPath is the pre-relocation
	// path a legacy row still records after its file moved into the law
	// folder: the folder placement plus the stable ID admit it, proving the
	// recorded path is not the authority.
	lawIndexCurrentPath    = ".concord/law/decisions/CD-0301-folder-authority.md"
	lawIndexMovedPath      = ".concord/law/decisions/CD-0302-relocated.md"
	lawIndexMovedOldPath   = ".concord/docs/decisions/CD-0302-relocated.md"
	lawIndexMissingOldPath = ".concord/docs/decisions/CD-0303-lost.md"
	lawIndexMissingNewPath = ".concord/law/decisions/CD-0303-lost.md"
	lawIndexBrokenPath     = ".concord/law/decisions/CD-0304-broken.md"
)

// lawIndexRetiredPrefixes covers every retired table plus the guards and
// indexes that belong to it. law_cross_source_relations and
// compaction_publications left the schema at migrations 164 and 153; their
// continued absence stays part of this contract so a conditional drop of
// them cannot fail the retirement on an already-missing table.
var lawIndexRetiredPrefixes = []string{
	"law_subjects", "law_relations", "law_cross_source_relations", "law_bodies",
	"law_domain_homes", "law_domain_applicability", "domain_relation_governing_laws",
	"knowledge_kind_coverage", "knowledge_index_watermark", "knowledge_publication_observations",
	"compaction_publications",
}

// lawIndexPreservedTables is the current state the retirement must not touch.
var lawIndexPreservedTables = []string{
	"projects", "project_locators", "products", "product_projects", "product_knowledge_homes",
	"work_items", "work_projects", "external_observations", "workflow_overlap_resolutions",
	"archived_work", "archived_work_products", "archived_work_projects",
	"archived_work_domains", "archived_work_tags",
}

// lawIndexSubjectSeed is one legacy law row set: the stable ID, the path the
// row records (which may be stale), and the committed content whose whole-file
// digest the row carries.
type lawIndexSubjectSeed struct {
	lawID      string
	recordPath string
	content    string
}

func lawIndexContentHash(content string) string {
	sum := sha256.Sum256([]byte(content))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// writeLawIndexHome commits the independent Domain registry and the given law
// files into a synthetic Git home and returns the resolved HEAD commit.
func writeLawIndexHome(t *testing.T, repo string, files map[string]string) string {
	t.Helper()
	writeKnowledgeFile(t, repo, knowledgeRegistryPath, fixtureLawFolderRegistry)
	for path, content := range files {
		writeKnowledgeFile(t, repo, path, content)
	}
	return commitKnowledgeRepo(t, repo, "synthetic law index retirement home")
}

// lawIndexRetirementFixture seeds the stamped v150 retirement corpus forward
// through schema 173 and writes the legacy law-index inventory of one
// synthetic committed Git home onto it: the Product home binding already held
// by the corpus gains the canonical-path locator, the Domain registry rows the
// law projections reference, one legacy row set per seeded law, and the
// watermark, coverage, and publication-observation rows the retirement drops.
// The home pair also gains an archived work witness with its scope children,
// which the retirement must preserve.
func lawIndexRetirementFixture(t *testing.T, repo, homePath, commit string, subjects []lawIndexSubjectSeed) (*sql.DB, string) {
	t.Helper()
	db, path := retirementUpgradeFixture(t)
	advanceHistoricalPrefix(t, db, 173)
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
	seedExec := func(query string, args ...any) {
		t.Helper()
		if _, err := tx.Exec(query, args...); err != nil {
			t.Fatalf("fixture seed %s: %v", query, err)
		}
	}
	seedExec(`INSERT INTO write_guard(active) VALUES(1)`)
	seedExec(
		`INSERT INTO project_locators(locator_id,project_id,kind,locator_value,normalized_value,created_at,updated_at) VALUES(?, 'project-upgrade', 'canonical_path', ?, ?, '2026-08-07T12:00:00Z', '2026-08-07T12:00:00Z')`,
		lawIndexRetirementLocator, homePath, homePath)
	seedExec(
		`INSERT INTO product_knowledge_homes(product_id,project_id,locator_id) VALUES('product-upgrade','project-upgrade',?)`,
		lawIndexRetirementLocator)
	seedExec(
		`INSERT INTO domain_registries(product_id,home_project_id,home_locator_id,product_key,root_domain_id,schema_version,content_hash,scanned_commit_oid) VALUES('product-upgrade','project-upgrade',?,'product','product-root:product','1.0','sha256:`+repeatA64()+`','commit')`,
		lawIndexRetirementLocator)
	seedExec(
		`INSERT INTO domains(home_project_id,home_locator_id,product_id,domain_id,name,purpose,status,registry_content_hash,scanned_commit_oid) VALUES('project-upgrade',?, 'product-upgrade','product-root:product','Product','Governs the synthetic fixture Product.','current','sha256:`+repeatA64()+`','commit')`,
		lawIndexRetirementLocator)
	seedExec(
		`INSERT INTO domains(home_project_id,home_locator_id,product_id,domain_id,name,purpose,parent_domain_id,status,registry_content_hash,scanned_commit_oid) VALUES('project-upgrade',?, 'product-upgrade','product-root:fixture','Law fixture home','The committed law fixture''s home Domain.','product-root:product','current','sha256:`+repeatA64()+`','commit')`,
		lawIndexRetirementLocator)
	seedExec(
		`INSERT INTO domains(home_project_id,home_locator_id,product_id,domain_id,name,purpose,parent_domain_id,status,registry_content_hash,scanned_commit_oid) VALUES('project-upgrade',?, 'product-upgrade','child','Child','Fixture child Domain.','product-root:product','current','sha256:`+repeatA64()+`','commit')`,
		lawIndexRetirementLocator)
	seedExec(
		`INSERT INTO domain_architecture_relations(home_project_id,home_locator_id,product_id,source_domain_id,kind,target_domain_id,registry_content_hash,scanned_commit_oid) VALUES('project-upgrade',?, 'product-upgrade','child','depends_on','product-root:product','sha256:`+repeatA64()+`','commit')`,
		lawIndexRetirementLocator)
	for _, subject := range subjects {
		hash := lawIndexContentHash(subject.content)
		seedExec(
			`INSERT INTO law_subjects(home_project_id,home_locator_id,law_id,kind,status,path,title,content_hash,scanned_commit_oid) VALUES('project-upgrade',?, ?, 'decision','accepted', ?, ?, ?, ?)`,
			lawIndexRetirementLocator, subject.lawID, subject.recordPath, "Fixture folder law "+subject.lawID, hash, commit)
		seedExec(
			`INSERT INTO law_bodies(home_project_id,home_locator_id,law_id,body,content_hash,scanned_commit_oid) VALUES('project-upgrade',?, ?, 'Synthetic fixture law body.', ?, ?)`,
			lawIndexRetirementLocator, subject.lawID, hash, commit)
		seedExec(
			`INSERT INTO law_domain_homes(home_project_id,home_locator_id,law_id,product_id,domain_id,law_content_hash,scanned_commit_oid) VALUES('project-upgrade',?, ?, 'product-upgrade','product-root:fixture', ?, ?)`,
			lawIndexRetirementLocator, subject.lawID, hash, commit)
		seedExec(
			`INSERT INTO law_domain_applicability(home_project_id,home_locator_id,law_id,product_id,domain_id,scanned_commit_oid) VALUES('project-upgrade',?, ?, 'product-upgrade','child', ?)`,
			lawIndexRetirementLocator, subject.lawID, commit)
	}
	if len(subjects) >= 2 {
		seedExec(
			`INSERT INTO law_relations(home_project_id,home_locator_id,source_law_id,kind,target_law_id,scanned_commit_oid) VALUES('project-upgrade',?, ?, 'refines', ?, ?)`,
			lawIndexRetirementLocator, subjects[0].lawID, subjects[1].lawID, commit)
	}
	seedExec(
		`INSERT INTO domain_relation_governing_laws(home_project_id,home_locator_id,product_id,source_domain_id,kind,target_domain_id,law_id,law_content_hash) VALUES('project-upgrade',?, 'product-upgrade','child','depends_on','product-root:product', ?, ?)`,
		lawIndexRetirementLocator, subjects[0].lawID, lawIndexContentHash(subjects[0].content))
	seedExec(
		`INSERT INTO knowledge_index_watermark(home_project_id,home_locator_id,head_ref,scanned_commit_oid,scanned_at,complete) VALUES('project-upgrade',?, 'HEAD', ?, '2026-08-07T12:00:00Z', 1)`,
		lawIndexRetirementLocator, commit)
	seedExec(
		`INSERT INTO knowledge_kind_coverage(home_project_id,home_locator_id,head_ref,kind,coverage,reason,scanned_commit_oid) VALUES('project-upgrade',?, 'HEAD','work_note','indexed','canonical .concord/docs/work directory scanned','commit')`,
		lawIndexRetirementLocator)
	seedExec(
		`INSERT INTO knowledge_publication_observations(project_id,locator_id,source_binding,latest_record,success_record) VALUES('project-upgrade',?, 'sha256:`+repeatA64()+`','{"publication":"observed"}','')`,
		lawIndexRetirementLocator)
	seedExec(
		`INSERT INTO archived_work(id,type,title,completed_at,outcome_tag,lesson_tags,terminal_state,priority,summary,home_project_id,home_locator_id,note_path,commit_oid,content_hash) VALUES('law-index-archive','lesson','Law index archive witness','2026-08-07T12:00:00Z','published','[]','completed',0,'Archive and scope children survive the law index retirement.','project-upgrade',?, '.concord/docs/lessons/law-index.md', ?, 'sha256:`+repeatA64()+`')`,
		lawIndexRetirementLocator, commit)
	for table, scope := range map[string][2]string{
		"archived_work_products": {"product_id", "product-upgrade"},
		"archived_work_projects": {"project_id", "project-upgrade"},
		"archived_work_domains":  {"domain_id", "product-root:fixture"},
		"archived_work_tags":     {"tag_id", "law-index"},
	} {
		seedExec(
			`INSERT INTO `+table+`(home_project_id, home_locator_id, work_id, `+scope[0]+`) VALUES('project-upgrade', ?, 'law-index-archive', ?)`, //nolint:gosec // table and column come from the closed scope map above and every value stays parameter-bound.
			lawIndexRetirementLocator, scope[1])
	}
	seedExec(`DELETE FROM write_guard`)
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	retirementUpgradeHealthy(t, db)
	return db, path
}

func assertLawIndexRetired(t *testing.T, db *sql.DB) {
	t.Helper()
	clauses := make([]string, 0, len(lawIndexRetiredPrefixes))
	args := make([]any, 0, len(lawIndexRetiredPrefixes))
	for _, prefix := range lawIndexRetiredPrefixes {
		clauses = append(clauses, `name LIKE ?`)
		args = append(args, prefix+"%")
	}
	if got := retirementUpgradeRows(t, db, `SELECT type,name FROM sqlite_schema WHERE `+strings.Join(clauses, " OR "), args...); len(got) != 0 {
		t.Errorf("retired law-index objects survive: %v", got)
	}
}

// assertLawIndexRetirementRefusal demands the guarded refusal shape: a typed,
// retry-safe upgrade_blocked whose detail and recovery action name the
// Product, the home, and the offending files or law identities.
func assertLawIndexRetirementRefusal(t *testing.T, err error, fragments ...string) {
	t.Helper()
	if err == nil {
		t.Fatal("the guarded upgrade admitted a law index its home folder cannot account for")
	}
	var failure *Failure
	if !errors.As(err, &failure) {
		t.Fatalf("upgrade error is not a typed Failure: %v", err)
	}
	if failure.Kind != KindUpgradeBlocked || !failure.RetrySafe {
		t.Fatalf("upgrade refusal = %v, want a retry-safe upgrade_blocked", failure)
	}
	surface := failure.Detail + " " + failure.RecoveryAction
	for _, fragment := range fragments {
		if !strings.Contains(surface, fragment) {
			t.Errorf("upgrade refusal %+v lacks actionable fragment %q", failure, fragment)
		}
	}
}

// The guarded upgrade over a v173 store whose legacy rows the committed
// folder fully accounts for, one at its recorded path and one through a stale
// pre-relocation path. Both admit through folder authority; the drop removes
// the tables, guards, and indexes while every preserved table, the manifest
// breaking record, and the verified predecessor snapshot keep the pre-drop
// state, and a second upgrade applies nothing.
func TestLawIndexRetirementUpgradeAdmitsFolderAuthorityAndPreservesCurrentState(t *testing.T) {
	useStampedBuild(t)
	repo := initKnowledgeRepo(t)
	commit := writeLawIndexHome(t, repo, map[string]string{
		lawIndexCurrentPath: fixtureLawFolderLaw(lawIndexCurrentLawID),
		lawIndexMovedPath:   fixtureLawFolderLaw(lawIndexMovedLawID),
	})
	db, path := lawIndexRetirementFixture(t, repo, repo, commit,
		[]lawIndexSubjectSeed{
			{lawID: lawIndexCurrentLawID, recordPath: lawIndexCurrentPath, content: fixtureLawFolderLaw(lawIndexCurrentLawID)},
			{lawID: lawIndexMovedLawID, recordPath: lawIndexMovedOldPath, content: fixtureLawFolderLaw(lawIndexMovedLawID)},
		},
	)
	before := retirementUpgradeSnapshot(t, db)
	preservedBefore := catalogUpgradeRows(t, db, lawIndexPreservedTables...)

	s, err := Open(t.Context(), path)
	if err == nil {
		_ = s.Close()
		t.Fatal("ordinary open crossed the guarded law-index retirement")
	}
	assertFailureKind(t, err, KindUpgradeRequired)
	if got := retirementUpgradeSnapshot(t, db); !reflect.DeepEqual(got, before) {
		t.Fatal("ordinary open changed the predecessor")
	}

	snapshot := filepath.Join(t.TempDir(), "law-index-predecessor.db")
	manifest, err := BackupDatabase(t.Context(), path, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if verified, err := VerifyBackup(t.Context(), snapshot); err != nil || verified.DBChecksum != manifest.DBChecksum || verified.SchemaVersion != 173 {
		t.Fatalf("verified predecessor = %+v, %v", verified, err)
	}

	report, err := Upgrade(t.Context(), path, nil)
	if err != nil {
		t.Fatalf("guarded upgrade refused a folder-accounted law index: %v", err)
	}
	if !reflect.DeepEqual(report.Applied, []int{174}) {
		t.Fatalf("applied migrations = %v, want [174]", report.Applied)
	}
	assertLawIndexRetired(t, db)
	if got := retirementUpgradeRows(t, db, `SELECT count(*) FROM schema_migrations WHERE version=174 AND breaking=1`); !reflect.DeepEqual(got, [][]any{{int64(1)}}) {
		t.Fatalf("law index retirement manifest record = %v, want one breaking row for schema 174", got)
	}
	if after := catalogUpgradeRows(t, db, lawIndexPreservedTables...); !reflect.DeepEqual(preservedBefore, after) {
		t.Fatal("the retirement changed preserved current state")
	}

	// The verified predecessor snapshot still holds the full pre-drop
	// schema and rows, including every populated retired table, and the
	// upgrade left its checksum untouched.
	backup, err := sql.Open(driverName, readOnlyDataSource(snapshot))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = backup.Close() }()
	if got := retirementUpgradeSnapshot(t, backup); !reflect.DeepEqual(got, before) {
		t.Fatal("predecessor snapshot lost schema or rows")
	}
	for _, prefix := range lawIndexRetiredPrefixes {
		if prefix == "law_cross_source_relations" || prefix == "compaction_publications" {
			continue
		}
		if len(before[prefix]) == 0 {
			t.Errorf("snapshot did not exercise populated %s", prefix)
		}
	}
	if verified, err := VerifyBackup(t.Context(), snapshot); err != nil || verified.DBChecksum != manifest.DBChecksum {
		t.Fatalf("upgrade damaged the predecessor snapshot: %+v, %v", verified, err)
	}
	retirementUpgradeHealthy(t, db)
	if _, err := PlanUpgradeReadiness(t.Context(), path); err != nil {
		t.Fatalf("post-retirement readiness: %v", err)
	}
	second, err := Upgrade(t.Context(), path, nil)
	if err != nil {
		t.Fatalf("second upgrade refused: %v", err)
	}
	if len(second.Applied) != 0 {
		t.Fatalf("second upgrade applied %v, want nothing", second.Applied)
	}
	assertLawIndexRetired(t, db)
}

// A legacy row whose recorded path the committed folder cannot account for
// (the file is absent) refuses the upgrade with the Product, home, and file
// details, and the ordinary source-migration recovery; after the source
// owner moves the file into the folder, the upgrade admits and drops.
func TestLawIndexRetirementRefusesMissingFolderLawAndAdmitsAfterMigration(t *testing.T) {
	useStampedBuild(t)
	repo := initKnowledgeRepo(t)
	commit := writeLawIndexHome(t, repo, map[string]string{
		lawIndexCurrentPath: fixtureLawFolderLaw(lawIndexCurrentLawID),
	})
	db, path := lawIndexRetirementFixture(t, repo, repo, commit,
		[]lawIndexSubjectSeed{
			{lawID: lawIndexCurrentLawID, recordPath: lawIndexCurrentPath, content: fixtureLawFolderLaw(lawIndexCurrentLawID)},
			{lawID: lawIndexMissingLawID, recordPath: lawIndexMissingOldPath, content: fixtureLawFolderLaw(lawIndexMissingLawID)},
		},
	)
	before := retirementUpgradeSnapshot(t, db)

	_, err := Upgrade(t.Context(), path, nil)
	assertLawIndexRetirementRefusal(t, err, "product-upgrade", repo, lawIndexMissingOldPath, lawIndexMissingLawID)
	if got := retirementUpgradeSnapshot(t, db); !reflect.DeepEqual(got, before) {
		t.Fatal("refused upgrade changed schema, manifest, retired rows, or preserved state")
	}
	retirementUpgradeHealthy(t, db)
	writeKnowledgeFile(t, repo, lawIndexMissingNewPath, fixtureLawFolderLaw(lawIndexMissingLawID))
	moved := commitKnowledgeRepo(t, repo, "move the missing law into the folder")
	if moved == commit {
		t.Fatal("the folder move produced no new commit")
	}
	if _, err := Upgrade(t.Context(), path, nil); err != nil {
		t.Fatalf("upgrade after the source migration refused: %v", err)
	}
	assertLawIndexRetired(t, db)
}

// A legacy row whose recorded content no longer matches any committed law
// file refuses the upgrade as a retry-safe upgrade_blocked; after the source
// owner repairs the folder file, the upgrade admits and drops.
func TestLawIndexRetirementRefusesInvalidFolderLawAndAdmitsAfterRepair(t *testing.T) {
	useStampedBuild(t)
	repo := initKnowledgeRepo(t)
	broken := "---\n" + lawIndexBrokenLawID + ": this frontmatter is not strict JSON\n---\n\nBroken law body.\n"
	commit := writeLawIndexHome(t, repo, map[string]string{lawIndexBrokenPath: broken})
	db, path := lawIndexRetirementFixture(t, repo, repo, commit,
		[]lawIndexSubjectSeed{
			{lawID: lawIndexBrokenLawID, recordPath: lawIndexBrokenPath, content: fixtureLawFolderLaw(lawIndexBrokenLawID)},
		},
	)
	before := retirementUpgradeSnapshot(t, db)

	_, err := Upgrade(t.Context(), path, nil)
	assertLawIndexRetirementRefusal(t, err, "product-upgrade", repo, lawIndexBrokenPath, lawIndexBrokenLawID)
	if got := retirementUpgradeSnapshot(t, db); !reflect.DeepEqual(got, before) {
		t.Fatal("refused upgrade changed schema, manifest, retired rows, or preserved state")
	}
	retirementUpgradeHealthy(t, db)
	writeKnowledgeFile(t, repo, lawIndexBrokenPath, fixtureLawFolderLaw(lawIndexBrokenLawID))
	repaired := commitKnowledgeRepo(t, repo, "repair the broken folder law")
	if repaired == commit {
		t.Fatal("the folder repair produced no new commit")
	}
	if _, err := Upgrade(t.Context(), path, nil); err != nil {
		t.Fatalf("upgrade after the folder repair refused: %v", err)
	}
	assertLawIndexRetired(t, db)
}

// A Product home binding whose canonical path holds no readable committed
// home refuses the upgrade naming the Product, the unreachable home, and the
// stranded law, and changes nothing.
func TestLawIndexRetirementRefusesUnreachableHomeWithoutChanges(t *testing.T) {
	useStampedBuild(t)
	repo := initKnowledgeRepo(t)
	commit := writeLawIndexHome(t, repo, map[string]string{
		lawIndexCurrentPath: fixtureLawFolderLaw(lawIndexCurrentLawID),
	})
	absent := filepath.Join(repo, "absent-home")
	db, path := lawIndexRetirementFixture(t, repo, absent, commit,
		[]lawIndexSubjectSeed{
			{lawID: lawIndexCurrentLawID, recordPath: lawIndexCurrentPath, content: fixtureLawFolderLaw(lawIndexCurrentLawID)},
		},
	)
	before := retirementUpgradeSnapshot(t, db)

	_, err := Upgrade(t.Context(), path, nil)
	assertLawIndexRetirementRefusal(t, err, "product-upgrade", absent, lawIndexCurrentLawID)
	if got := retirementUpgradeSnapshot(t, db); !reflect.DeepEqual(got, before) {
		t.Fatal("refused upgrade changed schema, manifest, retired rows, or preserved state")
	}
	retirementUpgradeHealthy(t, db)
}

// The live-session gate stays ahead of the folder guard: a held schema
// predating the pending breaking retirement refuses as upgrade_blocked and
// writes nothing, even with a folder-accounted law index.
func TestLawIndexRetirementHeldReleaseRefusesWithoutChanges(t *testing.T) {
	useStampedBuild(t)
	repo := initKnowledgeRepo(t)
	commit := writeLawIndexHome(t, repo, map[string]string{
		lawIndexCurrentPath: fixtureLawFolderLaw(lawIndexCurrentLawID),
	})
	db, path := lawIndexRetirementFixture(t, repo, repo, commit,
		[]lawIndexSubjectSeed{
			{lawID: lawIndexCurrentLawID, recordPath: lawIndexCurrentPath, content: fixtureLawFolderLaw(lawIndexCurrentLawID)},
		},
	)
	before := retirementUpgradeSnapshot(t, db)
	_, err := Upgrade(t.Context(), path, []HeldSchema{{PID: 123, ReleaseRoot: "/synthetic/release", SchemaVersion: 173}})
	assertFailureKind(t, err, KindUpgradeBlocked)
	if got := retirementUpgradeSnapshot(t, db); !reflect.DeepEqual(got, before) {
		t.Fatal("held release refusal changed predecessor state")
	}
}

// A foreign key into law_subjects blocks the drop: the failed migration must
// restore the full predecessor — schema, manifest, retired rows, blocker, and
// preserved state — and a retry after the blocker leaves applies the
// retirement.
func TestLawIndexRetirementFailedDropRollsBackAndRetries(t *testing.T) {
	useStampedBuild(t)
	repo := initKnowledgeRepo(t)
	commit := writeLawIndexHome(t, repo, map[string]string{
		lawIndexCurrentPath: fixtureLawFolderLaw(lawIndexCurrentLawID),
		lawIndexMovedPath:   fixtureLawFolderLaw(lawIndexMovedLawID),
	})
	db, path := lawIndexRetirementFixture(t, repo, repo, commit,
		[]lawIndexSubjectSeed{
			{lawID: lawIndexCurrentLawID, recordPath: lawIndexCurrentPath, content: fixtureLawFolderLaw(lawIndexCurrentLawID)},
			{lawID: lawIndexMovedLawID, recordPath: lawIndexMovedOldPath, content: fixtureLawFolderLaw(lawIndexMovedLawID)},
		},
	)
	retirementUpgradeExec(t, db, `CREATE TABLE law_index_drop_blocker(home_project_id TEXT NOT NULL, home_locator_id TEXT NOT NULL, law_id TEXT NOT NULL, FOREIGN KEY(home_project_id, home_locator_id, law_id) REFERENCES law_subjects(home_project_id, home_locator_id, law_id) ON DELETE RESTRICT);
INSERT INTO law_index_drop_blocker VALUES('project-upgrade','law-home-locator','CD-0301')`)
	before := retirementUpgradeSnapshot(t, db)

	_, err := Upgrade(t.Context(), path, nil)
	if err == nil || !strings.Contains(err.Error(), "migration 174") || !strings.Contains(strings.ToLower(err.Error()), "foreign key") {
		t.Fatalf("upgrade must fail on the induced foreign key against migration 174: %v", err)
	}
	if got := retirementUpgradeSnapshot(t, db); !reflect.DeepEqual(got, before) {
		t.Fatal("failed upgrade changed schema, manifest, retired rows, blocker, or preserved state")
	}
	retirementUpgradeHealthy(t, db)

	retirementUpgradeExec(t, db, `DROP TABLE law_index_drop_blocker`)
	report, err := Upgrade(t.Context(), path, nil)
	if err != nil {
		t.Fatalf("retry after removing the drop blocker: %v", err)
	}
	if !reflect.DeepEqual(report.Applied, []int{174}) {
		t.Fatalf("applied migrations = %v, want [174]", report.Applied)
	}
	assertLawIndexRetired(t, db)
	retirementUpgradeHealthy(t, db)
}

// A fresh store migrates to the current schema head, which still creates the
// law-index tables until schema 174 retires them: the absence assertion fails
// until the retirement exists and holds against later reintroduction.
func TestLawIndexRetirementFreshAbsence(t *testing.T) {
	s := openTemp(t)
	assertLawIndexRetired(t, s.db)
	retirementUpgradeHealthy(t, s.db)
}

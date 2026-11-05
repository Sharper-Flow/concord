package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/store/internal/sqlitedb"
	"github.com/sharper-flow/concord/internal/version"
)

func outcomeRetirementFixture(t *testing.T) (string, *sql.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "source.db")
	db := openMigratedTo(t, path, migrationPrefixBefore(t, "outcome_contract_retirement"))
	actor := DeriveWorkflowActorRef("principal:one", "client:one", "agent:one", "session:one")
	_, err := db.Exec(`INSERT INTO write_guard(active) VALUES(1);
INSERT INTO products(id,display_name,stage_maturity,stage_audience_commitment,version,created_at,updated_at) VALUES('product-one','Product','prototype','operator_only',1,'t','t');
INSERT INTO projects(id,display_name,version,created_at,updated_at) VALUES('project-one','Project',1,'t','t');
INSERT INTO product_projects(product_id,project_id,role) VALUES('product-one','project-one','primary');
INSERT INTO work_items(id,kind,title,lifecycle,priority,version,created_at,updated_at) VALUES('work-one','task','Fixture','needed',0,1,'t','t');
INSERT INTO work_projects(work_id,project_id,role) VALUES('work-one','project-one','primary');
INSERT INTO products(id,display_name,stage_maturity,stage_audience_commitment,version,created_at,updated_at) VALUES('product-two','Product Two','prototype','operator_only',1,'t','t');
INSERT INTO projects(id,display_name,version,created_at,updated_at) VALUES('project-two','Project Two',1,'t','t');
INSERT INTO product_projects(product_id,project_id,role) VALUES('product-two','project-two','primary');
INSERT INTO work_items(id,kind,title,lifecycle,priority,version,created_at,updated_at) VALUES('work-two','task','Fixture Two','needed',0,1,'t','t');
INSERT INTO work_projects(work_id,project_id,role) VALUES('work-two','project-two','primary');
INSERT INTO workflow_actors(actor_ref,principal_ref,client_ref,agent_ref,session_ref,actor_class,first_seen_at) VALUES(?,'principal:one','client:one','agent:one','session:one','agent','t');
INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,changes_product_truth) VALUES
 ('work-one',1,'First','internal_sqlite','[]','[]','t',?,'[]',1),
 ('work-one',2,'Second','internal_sqlite','[]','[]','t',?,'["spec:read"]',1),
 ('work-two',1,'Other product','internal_sqlite','[]','[]','t',?,'[]',1);
DELETE FROM write_guard;`, actor, actor, actor, actor)
	if err != nil {
		t.Fatal(err)
	}
	return path, db
}

func seedRetirementPredicate(t *testing.T, db *sql.DB, version, ordinal int, kind string, subjects []string) {
	t.Helper()
	seedRetirementWorkPredicate(t, db, "work-one", version, ordinal, kind, subjects)
}

func seedRetirementWorkPredicate(t *testing.T, db *sql.DB, work string, version, ordinal int, kind string, subjects []string) {
	t.Helper()
	value := map[string]any{"kind": kind, "surface": "fixture:source", "subjects": subjects}
	if kind == "absent" {
		value["distinguish_from"] = []string{"archived"}
	}
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	seedRetirementRawPredicate(t, db, work, version, ordinal, kind, string(payload))
}

func seedRetirementRawPredicate(t *testing.T, db *sql.DB, work string, version, ordinal int, kind, payload string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO write_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO workflow_contract_predicates(work_id,contract_version,predicate_id,ordinal,outcome_kind,outcome_payload) VALUES(?,?,?,?,?,?)`, work, version, fmt.Sprintf("predicate:item-%d", ordinal), ordinal, kind, payload); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM write_guard`); err != nil {
		t.Fatal(err)
	}
}

func seedRetirementRetainedFacts(t *testing.T, db *sql.DB) {
	t.Helper()
	actor := DeriveWorkflowActorRef("principal:one", "client:one", "agent:one", "session:one")
	_, err := db.Exec(`INSERT INTO write_guard(active) VALUES(1);
UPDATE workflow_contracts SET superseded_by=2, route_conventions='["route:one"]', spec_mandate='["spec:mandate"]',
 rigor_class='production_public', law_modifies='["law:one"]', law_boundary_version=1,
 self_repair_json='{"reason":"fixture"}', supersession_seq=17, last_healthy_seq=12,
 decision_domains='["legal","security"]', changes_product_truth=0 WHERE work_id='work-one' AND contract_version=1;
INSERT INTO workflow_candidate_sets VALUES('work-one',1,'work_item','candidate:one','include','fixture scope','candidate-time',?1);
INSERT INTO workflow_premise_confirmations VALUES('work-one',1,?1,'confirmed-time');
INSERT INTO workflow_architecture_bindings(work_id,contract_version,product_id,home_domain_id,projection_hash) VALUES('work-one',1,'product-one','domain:home',?2);
INSERT INTO workflow_contract_affected_domains VALUES('work-one',1,'domain:affected');
INSERT INTO workflow_contract_domain_modifications VALUES('work-one',1,'domain:modified');
INSERT INTO workflow_contract_domain_relation_modifications VALUES('work-one',1,'domain:home','depends_on','domain:other');
INSERT INTO workflow_law_addition_reservations VALUES('product-one','law:new','work-one',1,'domain:home');
INSERT INTO workflow_contract_law_additions VALUES('work-one',1,'product-one','law:new','domain:home','work-one',1);
INSERT INTO workflow_contract_verification_obligations VALUES('work-one',1,'law:new','obligation:one');
INSERT INTO workflow_contract_law_modifications VALUES('work-one',1,'law:existing');
INSERT INTO effect_receipts(event_id,effect_seq,kind,subject_type,subject_id,actor,payload_version) VALUES('event:overlap',41,'workflow.overlap','work_item','work-one',?1,1);
INSERT INTO workflow_overlap_resolutions(resolution_id,event_seq,product_id,from_work_id,to_work_id,from_contract_version,to_contract_version,resolution_kind,reason,created_at) VALUES('resolution:one',41,'product-one','work-one','work-two',1,1,'compatible_with','fixture reason','overlap-time');
INSERT INTO relations(work_id_from,work_id_to,kind,created_at,resolution_id,reason,record_version) VALUES('work-one','work-two','compatible_with','overlap-time','resolution:one','fixture reason',1);
DELETE FROM write_guard;`, actor, "sha256:"+strings.Repeat("b", 64))
	if err != nil {
		t.Fatal(err)
	}
}

func retainedContractRows(t *testing.T, db *sql.DB) map[string][][]any {
	t.Helper()
	rows := map[string][][]any{}
	rows["workflow_contracts"] = retirementUpgradeRows(t, db, `SELECT work_id,contract_version,premise,consequence_class,route_conventions,approved_at,approved_by,superseded_by,spec_mandate,rigor_class,law_modifies,law_boundary_version,self_repair_json,supersession_seq,last_healthy_seq,decision_domains,changes_product_truth FROM workflow_contracts ORDER BY work_id,contract_version`)
	for _, table := range []string{"workflow_candidate_sets", "workflow_premise_confirmations", "workflow_architecture_bindings", "workflow_contract_affected_domains", "workflow_contract_domain_modifications", "workflow_contract_domain_relation_modifications", "workflow_law_addition_reservations", "workflow_contract_law_additions", "workflow_contract_verification_obligations", "workflow_contract_law_modifications", "workflow_overlap_resolutions", "relations"} {
		rows[table] = retirementUpgradeRows(t, db, `SELECT * FROM `+table+` ORDER BY rowid`)
		if len(rows[table]) == 0 {
			t.Fatalf("fixture missing retained rows in %s", table)
		}
	}
	return rows
}

func assertRetirementContractGuards(t *testing.T, db *sql.DB) {
	t.Helper()
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	insert := `INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,route_conventions,approved_at,approved_by,spec_mandate,rigor_class,decision_domains) SELECT work_id,3,'Guard fixture',consequence_class,route_conventions,approved_at,approved_by,spec_mandate,?,? FROM workflow_contracts WHERE work_id='work-one' AND contract_version=2`
	for _, op := range []struct {
		query string
		args  []any
	}{
		{insert, []any{"prototype_internal", `["legal"]`}},
		{`UPDATE workflow_contracts SET premise='changed' WHERE work_id='work-one' AND contract_version=2`, nil},
		{`DELETE FROM workflow_contracts WHERE work_id='work-one' AND contract_version=2`, nil},
	} {
		if _, err := tx.Exec(op.query, op.args...); err == nil || !strings.Contains(err.Error(), "write-seam-only") {
			t.Fatalf("unguarded operation %s: %v", op.query, err)
		}
	}
	if err := enterWrite(t.Context(), tx); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		query   string
		args    []any
		message string
	}{
		{insert, []any{"invalid", `["legal"]`}, "rigor class"},
		{insert, []any{"prototype_internal", `["security","legal"]`}, "decision_domains"},
		{`UPDATE workflow_contracts SET rigor_class='invalid' WHERE work_id='work-one' AND contract_version=2`, nil, "rigor class"},
		{`UPDATE workflow_contracts SET decision_domains='["security","legal"]' WHERE work_id='work-one' AND contract_version=2`, nil, "decision_domains"},
	} {
		if _, err := tx.Exec(tc.query, tc.args...); err == nil || !strings.Contains(err.Error(), tc.message) {
			t.Fatalf("invalid guarded operation %s: %v", tc.query, err)
		}
	}
	if _, err := tx.Exec(insert, "production_public", `["legal","security"]`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`UPDATE workflow_contracts SET rigor_class='critical_public', decision_domains='["product"]' WHERE work_id='work-one' AND contract_version=3`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`DELETE FROM workflow_contracts WHERE work_id='work-one' AND contract_version=3`); err != nil {
		t.Fatal(err)
	}
	if err := leaveWrite(t.Context(), tx); err != nil {
		t.Fatal(err)
	}
}

func TestOutcomeRetirementPreservesAllContractVersions(t *testing.T) {
	previous := version.Value
	version.Value = "v-test"
	t.Cleanup(func() { version.Value = previous })
	path, db := outcomeRetirementFixture(t)
	var want []string
	for predicate := 0; predicate < 8; predicate++ {
		var subjects []string
		for i := 0; i < 100; i++ {
			subjects = append(subjects, fmt.Sprintf("subject:%03d", predicate*100+i))
		}
		want = append(want, subjects...)
		seedRetirementPredicate(t, db, 1, predicate, "exists", subjects)
	}
	seedRetirementPredicate(t, db, 2, 0, "absent", []string{"spec:second", "decision:second", "constitution:second"})
	seedRetirementWorkPredicate(t, db, "work-two", 1, 0, "exists", []string{"spec:other", "decision:other", "constitution:other"})
	seedRetirementRetainedFacts(t, db)
	retained := retainedContractRows(t, db)
	before := retirementUpgradeSnapshot(t, db)
	if _, err := Upgrade(t.Context(), path, nil); !hasFailureKind(err, KindUpgradeBlocked) || !strings.Contains(err.Error(), "outcome retirement requires a verified snapshot") {
		t.Fatalf("upgrade without snapshot: %v", err)
	}
	if after := retirementUpgradeSnapshot(t, db); !reflect.DeepEqual(before, after) {
		t.Fatal("snapshot refusal changed predecessor rows, schema or applied migrations")
	}
	retirementUpgradeHealthy(t, db)
	snapshot := filepath.Join(t.TempDir(), "snapshot.db")
	if _, err := BackupDatabase(t.Context(), path, snapshot); err != nil {
		t.Fatal(err)
	}
	report, err := Upgrade(t.Context(), path, nil, UpgradeOptions{SnapshotPath: snapshot})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(report.Applied, []int{185, 186}) {
		t.Fatalf("applied = %v", report.Applied)
	}
	if after := retainedContractRows(t, db); !reflect.DeepEqual(retained, after) {
		t.Fatalf("retained contract facts changed: before=%#v after=%#v", retained, after)
	}
	retirementUpgradeHealthy(t, db)
	assertRetirementContractGuards(t, db)
	var encoded string
	if err := db.QueryRow(`SELECT legislative_subjects FROM workflow_contracts WHERE work_id='work-one' AND contract_version=1`).Scan(&encoded); err != nil {
		t.Fatal(err)
	}
	var got []string
	if err := json.Unmarshal([]byte(encoded), &got); err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("historical subjects = %v, error %v", got, err)
	}
	if err := db.QueryRow(`SELECT legislative_subjects FROM workflow_contracts WHERE work_id='work-one' AND contract_version=2`).Scan(&encoded); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(encoded), &got); err != nil || !reflect.DeepEqual(got, []string{"constitution:second", "decision:second", "spec:second"}) {
		t.Fatalf("second version subjects = %v, error %v", got, err)
	}
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"spec", "decision", "constitution"} {
		for _, subject := range []struct {
			work, suffix string
			version      int64
		}{{"work-one", "second", 2}, {"work-two", "other", 1}} {
			record := KnowledgeRecord{ID: kind + ":" + subject.suffix, Kind: kind, Authority: KnowledgeAuthority{Tier: "legislated", LegislatedBy: subject.work, ContractVersion: subject.version}}
			unverified, err := resolveKnowledgeLegislatedAuthorityTx(t.Context(), tx, record)
			if err != nil || unverified {
				t.Fatalf("predicate-only %s %s: unverified=%v error=%v", kind, subject.work, unverified, err)
			}
		}
		record := KnowledgeRecord{ID: kind + ":second", Kind: kind, Authority: KnowledgeAuthority{Tier: "legislated", LegislatedBy: "work-one", ContractVersion: 1}}
		if _, err := resolveKnowledgeLegislatedAuthorityTx(t.Context(), tx, record); !hasFailureKind(err, KindInvalidNoteProof) {
			t.Fatalf("subject leaked into another version for %s: %v", kind, err)
		}
	}
	for _, record := range []KnowledgeRecord{
		{ID: "spec:read", Kind: "spec", Authority: KnowledgeAuthority{Tier: "legislated", LegislatedBy: "work-one", ContractVersion: 2}},
		{ID: "spec:other", Kind: "spec", Authority: KnowledgeAuthority{Tier: "legislated", LegislatedBy: "work-one", ContractVersion: 2}},
		{ID: "spec:second", Kind: "spec", Authority: KnowledgeAuthority{Tier: "legislated", LegislatedBy: "work-one", ContractVersion: 3}},
	} {
		if _, err := resolveKnowledgeLegislatedAuthorityTx(t.Context(), tx, record); !hasFailureKind(err, KindInvalidNoteProof) {
			t.Fatalf("held negative admitted: %+v: %v", record, err)
		}
	}
	missing := KnowledgeRecord{ID: "spec:missing", Kind: "spec", Authority: KnowledgeAuthority{Tier: "legislated", LegislatedBy: "work-missing", ContractVersion: 1}}
	if unverified, err := resolveKnowledgeLegislatedAuthorityTx(t.Context(), tx, missing); err != nil || !unverified {
		t.Fatalf("missing work: unverified=%v error=%v", unverified, err)
	}
	payload := workflowContractApprovedPayload{
		ContractVersion: 3, Premise: "Future contract", ConsequenceClass: "internal_sqlite", RigorClass: "prototype_internal",
		RouteConventions: []string{}, SpecMandate: []string{}, LawModifies: []string{}, DecisionDomains: []string{},
		LegislativeSubjects: []string{"spec:future", "decision:future", "constitution:future"},
		ArchitectureBinding: &WorkflowArchitectureBinding{HomeDomainID: "domain:fixture"},
	}
	if err := normalizeWorkflowContractApproval(&payload); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO write_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	event := Event{SubjectID: "work-one", Actor: DeriveWorkflowActorRef("principal:one", "client:one", "agent:one", "session:one"), OccurredAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	if err := persistWorkflowContractApprovalTx(t.Context(), tx, event, WorkflowStepPlanChange, payload); err != nil {
		t.Fatal(err)
	}
	contract, err := readWorkflowContractVersion(t.Context(), tx, "work-one", 3)
	if err != nil || !reflect.DeepEqual(contract.LegislativeSubjects, payload.LegislativeSubjects) {
		t.Fatalf("future approved list = %v: %v", contract.LegislativeSubjects, err)
	}
	for _, kind := range []string{"spec", "decision", "constitution"} {
		record := KnowledgeRecord{ID: kind + ":future", Kind: kind, Authority: KnowledgeAuthority{Tier: "legislated", LegislatedBy: "work-one", ContractVersion: 3}}
		if unverified, err := resolveKnowledgeLegislatedAuthorityTx(t.Context(), tx, record); err != nil || unverified {
			t.Fatalf("future %s: unverified=%v error=%v", kind, unverified, err)
		}
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err := reopened.db.QueryRow(`SELECT legislative_subjects FROM workflow_contracts WHERE work_id='work-one' AND contract_version=1`).Scan(&encoded); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(encoded), &got); err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("reopened subjects changed: %v", err)
	}
}

// verifiedOutcomeRetirementContext stands in for the snapshot proof the
// migration transaction verifies before it applies migration 185.
func verifiedOutcomeRetirementContext(t *testing.T) context.Context {
	return context.WithValue(t.Context(), outcomeRetirementProofKey{}, &outcomeRetirementProof{verified: true})
}

// beforeOutcomeRetirement is the last schema version that still holds
// predicate rows. Tests of earlier migrations stop here because their
// placeholder predicates are not decodable outcome contracts.
const beforeOutcomeRetirement = 184

// outcomeSnapshotContext records the snapshot proof concord upgrade supplies
// before a historical fixture with outcome rows crosses migration 185: the
// copy's checksum and the store's current-state digest. Call it after the
// fixture's last write; the migration rechecks both before applying any step.
// It copies with VACUUM INTO rather than Backup, whose restore gate refuses an
// old store under the unstamped test build.
func outcomeSnapshotContext(t *testing.T, ctx context.Context, db *sql.DB) context.Context {
	t.Helper()
	snapshot := filepath.Join(t.TempDir(), "outcome-retirement.db")
	if _, err := db.ExecContext(ctx, `VACUUM INTO ?`, snapshot); err != nil {
		t.Fatal(err)
	}
	checksum, err := fileSHA256(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := sqlitedb.CurrentStateDigest(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	return context.WithValue(ctx, outcomeRetirementProofKey{}, &outcomeRetirementProof{snapshot: snapshot, checksum: checksum, digest: digest})
}

// outcomeSnapshotOption takes the verified backup concord upgrade passes to
// Upgrade. Backup's restore gate needs a stamped build.
func outcomeSnapshotOption(t *testing.T, ctx context.Context, path string) UpgradeOptions {
	t.Helper()
	snapshot := filepath.Join(t.TempDir(), "outcome-retirement.db")
	if _, err := BackupDatabase(ctx, path, snapshot); err != nil {
		t.Fatal(err)
	}
	return UpgradeOptions{SnapshotPath: snapshot}
}

func TestOutcomeRetirementMalformedSourceRollsBack(t *testing.T) {
	_, db := outcomeRetirementFixture(t)
	seedRetirementPredicate(t, db, 1, 0, "exists", []string{"spec:valid"})
	if _, err := db.Exec(`INSERT INTO write_guard(active) VALUES(1); UPDATE workflow_contract_predicates SET outcome_payload='{"kind":"exists","subjects":null}' WHERE contract_version=1; DELETE FROM write_guard`); err != nil {
		t.Fatal(err)
	}
	assertOutcomeTransferRollback(t, db, "malformed historical predicate")
}

func TestOutcomeRetirementFailedTransferRollsBack(t *testing.T) {
	_, db := outcomeRetirementFixture(t)
	seedRetirementPredicate(t, db, 1, 0, "exists", []string{"spec:valid"})
	if _, err := db.Exec(`CREATE TRIGGER reject_retirement_transfer BEFORE UPDATE ON workflow_contracts BEGIN SELECT RAISE(ABORT,'transfer refused'); END`); err != nil {
		t.Fatal(err)
	}
	assertOutcomeTransferRollback(t, db, "transfer refused")
}

func TestOutcomeRetirementFailedRebuildRollsBack(t *testing.T) {
	_, db := outcomeRetirementFixture(t)
	seedRetirementPredicate(t, db, 1, 0, "exists", []string{"spec:valid"})
	seedRetirementRetainedFacts(t, db)
	if _, err := db.Exec(`CREATE TRIGGER reject_retirement_rebuild BEFORE DELETE ON workflow_contract_affected_domains BEGIN SELECT RAISE(ABORT,'rebuild refused'); END`); err != nil {
		t.Fatal(err)
	}
	before := retirementUpgradeSnapshot(t, db)
	ctx := outcomeSnapshotContext(t, t.Context(), db)
	if err := Migrate(ctx, db); err == nil || !strings.Contains(err.Error(), "rebuild refused") {
		t.Fatalf("failed rebuild = %v", err)
	}
	if after := retirementUpgradeSnapshot(t, db); !reflect.DeepEqual(before, after) {
		t.Fatal("failed rebuild changed predecessor schema, rows or applied migrations")
	}
	retirementUpgradeHealthy(t, db)
}

func assertOutcomeTransferRollback(t *testing.T, db *sql.DB, cause string) {
	t.Helper()
	before := retirementUpgradeSnapshot(t, db)
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := applyMigration(verifiedOutcomeRetirementContext(t), tx, migrations[migrationPrefixBefore(t, "outcome_contract_retirement")]); err == nil || !strings.Contains(err.Error(), cause) {
		t.Fatalf("failed transfer = %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM workflow_contract_predicates`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("source not preserved: count=%d error=%v", count, err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM pragma_table_info('workflow_contracts') WHERE name='legislative_subjects'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("column survived rollback: count=%d error=%v", count, err)
	}
	if after := retirementUpgradeSnapshot(t, db); !reflect.DeepEqual(before, after) {
		t.Fatal("failed transfer changed predecessor schema, rows or applied migrations")
	}
}

func TestOutcomeRetirementEvidenceDeclarationsRequireSnapshot(t *testing.T) {
	previous := version.Value
	version.Value = "v-test"
	t.Cleanup(func() { version.Value = previous })
	path, db := outcomeRetirementFixture(t)
	if _, err := db.Exec(`INSERT INTO write_guard(active) VALUES(1); UPDATE workflow_contracts SET required_evidence='["artifact"]' WHERE work_id='work-one' AND contract_version=1; DELETE FROM write_guard`); err != nil {
		t.Fatal(err)
	}
	before := retirementUpgradeSnapshot(t, db)
	if _, err := Upgrade(t.Context(), path, nil); !hasFailureKind(err, KindUpgradeBlocked) || !strings.Contains(err.Error(), "outcome retirement requires a verified snapshot") {
		t.Fatalf("evidence declarations without snapshot: %v", err)
	}
	if after := retirementUpgradeSnapshot(t, db); !reflect.DeepEqual(before, after) {
		t.Fatal("snapshot refusal changed predecessor schema, rows or applied migrations")
	}
	retirementUpgradeHealthy(t, db)
	report, err := Upgrade(t.Context(), path, nil, outcomeSnapshotOption(t, t.Context(), path))
	if err != nil {
		t.Fatalf("upgrade of contracts without outcome rows: %v", err)
	}
	if !reflect.DeepEqual(report.Applied, []int{185, 186}) {
		t.Fatalf("applied = %v", report.Applied)
	}
	var subjects string
	if err := db.QueryRow(`SELECT group_concat(legislative_subjects) FROM workflow_contracts`).Scan(&subjects); err != nil || subjects != "[],[],[]" {
		t.Fatalf("contracts after upgrade = %q, %v", subjects, err)
	}
}

func TestOutcomeRetirementSnapshotDriftRefused(t *testing.T) {
	previous := version.Value
	version.Value = "v-test"
	t.Cleanup(func() { version.Value = previous })
	for _, mode := range []string{"source", "snapshot", "unneeded"} {
		t.Run(mode, func(t *testing.T) {
			path, db := outcomeRetirementFixture(t)
			if mode != "unneeded" {
				seedRetirementPredicate(t, db, 1, 0, "exists", []string{"spec:valid"})
			}
			snapshot := filepath.Join(t.TempDir(), "snapshot.db")
			if _, err := BackupDatabase(t.Context(), path, snapshot); err != nil {
				t.Fatal(err)
			}
			ctx, err := prepareOutcomeRetirementSnapshot(t.Context(), snapshot)
			if err != nil {
				t.Fatal(err)
			}
			if mode != "snapshot" {
				if _, err := db.Exec(`INSERT INTO write_guard(active) VALUES(1); UPDATE work_items SET title='Changed' WHERE id='work-one'; DELETE FROM write_guard`); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(snapshot, []byte("tampered"), 0o600); err != nil {
				t.Fatal(err)
			}
			// Drift blocks only a migration with rows the snapshot must preserve.
			if mode == "unneeded" {
				if err := Migrate(ctx, db); err != nil {
					t.Fatalf("drift without outcome rows refused: %v", err)
				}
				return
			}
			if err := Migrate(ctx, db); err == nil || !strings.Contains(err.Error(), "changed") {
				t.Fatalf("drift admitted: %v", err)
			}
			var count int
			if err := db.QueryRow(`SELECT count(*) FROM workflow_contract_predicates`).Scan(&count); err != nil || count != 1 {
				t.Fatalf("source not preserved: count=%d error=%v", count, err)
			}
			if err := db.QueryRow(`SELECT count(*) FROM schema_migrations WHERE version=185`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("migration recorded despite refusal: count=%d error=%v", count, err)
			}
		})
	}
}

// A migration attempt retried after lock contention verifies the snapshot
// again; an earlier attempt's match never admits a store that changed since.
func TestOutcomeRetirementSnapshotReverifiedOnRetry(t *testing.T) {
	previous := version.Value
	version.Value = "v-test"
	t.Cleanup(func() { version.Value = previous })
	path, db := outcomeRetirementFixture(t)
	seedRetirementPredicate(t, db, 1, 0, "exists", []string{"spec:valid"})
	snapshot := filepath.Join(t.TempDir(), "snapshot.db")
	if _, err := BackupDatabase(t.Context(), path, snapshot); err != nil {
		t.Fatal(err)
	}
	ctx, err := prepareOutcomeRetirementSnapshot(t.Context(), snapshot)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	applied, err := legacyManifestMigrations(ctx, tx)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyOutcomeRetirementSnapshot(ctx, tx, migrateForUpgrade, applied); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO write_guard(active) VALUES(1); UPDATE work_items SET title='Changed' WHERE id='work-one'; DELETE FROM write_guard`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, db); err == nil || !strings.Contains(err.Error(), "store changed after the verified snapshot") {
		t.Fatalf("drift after an earlier verified attempt admitted: %v", err)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM schema_migrations WHERE version=185`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("migration recorded despite refusal: count=%d error=%v", count, err)
	}
}

func TestOutcomeRetirementDigestTransactionMatchesSnapshot(t *testing.T) {
	_, db := outcomeRetirementFixture(t)
	want, err := sqlitedb.CurrentStateDigest(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE store_revision SET revision=revision+1 WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	got, err := sqlitedb.CurrentStateDigestTx(t.Context(), tx)
	if err != nil || got != want {
		t.Fatalf("transaction digest = %q, want %q: %v", got, want, err)
	}
}

func historicalDecisionRecord(t *testing.T, question string, extra map[string]any) string {
	t.Helper()
	record := map[string]any{
		"question": question, "options_considered": []string{"keep", "retire"}, "decision": "accepted_decision",
		"rationale": "Fixture rationale", "consequences": []string{"Fixture consequence"}, "inputs": []string{"fixture:input"},
		"poc_findings": "Fixture finding", "supersedes": nil, "superseded_by": nil, "unknowns": []string{}, "required_to_decide": []string{},
		"reviewer_actor_ref": "actor:" + strings.Repeat("a", 64),
	}
	for key, value := range extra {
		record[key] = value
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func paddedHistoricalPresence(size int) string {
	head := `{"kind":"exists","surface":"fixture:source","subjects":["spec:valid"]`
	return head + strings.Repeat(" ", size-len(head)-1) + `}`
}

// Each case states whether some pre-185 writer could have stored the same
// bytes; migration 185 admits exactly those.
func TestOutcomeRetirementHistoricalDecoderParity(t *testing.T) {
	check := func(subject string) string {
		return `{"kind":"check","check_ref":"check:ci/build","immutable_subject_ref":"` + subject + `","expected_result":"pass"}`
	}
	outcome := func(record string) string {
		return `{"kind":"outcome","allowed":["accepted_decision"],"decision_record":` + record + `}`
	}
	for _, test := range []struct {
		name, kind, payload string
		want                []string
		refused             bool
	}{
		{name: "presence at the 64 KiB bound", kind: "exists", payload: paddedHistoricalPresence(64 * 1024), want: []string{"spec:valid"}},
		{name: "presence above the 64 KiB bound", kind: "exists", payload: paddedHistoricalPresence(64*1024 + 1), refused: true},
		{name: "absent presence", kind: "absent", payload: `{"kind":"absent","surface":"fixture:source","subjects":["spec:gone"],"distinguish_from":["renamed"]}`, want: []string{"spec:gone"}},
		{name: "exists with absent-only field", kind: "exists", payload: `{"kind":"exists","surface":"fixture:source","subjects":["spec:valid"],"distinguish_from":["renamed"]}`, refused: true},
		{name: "stored kind differs from payload", kind: "absent", payload: `{"kind":"exists","surface":"fixture:source","subjects":["spec:valid"]}`, refused: true},
		{name: "duplicate field", kind: "exists", payload: `{"kind":"exists","surface":"fixture:source","subjects":["spec:valid"],"subjects":["spec:other"]}`, refused: true},
		{name: "outcome without decision record", kind: "outcome", payload: `{"kind":"outcome","allowed":["completed"]}`},
		{name: "outcome with null decision record", kind: "outcome", payload: outcome("null")},
		{name: "decision question at 4096 bytes", kind: "outcome", payload: outcome(historicalDecisionRecord(t, strings.Repeat("é", 2048), nil))},
		{name: "decision question above 4096 bytes within 4096 code points", kind: "outcome", payload: outcome(historicalDecisionRecord(t, strings.Repeat("é", 2049), nil)), refused: true},
		{name: "decision record with approval ref written before challenge retirement", kind: "outcome", payload: outcome(historicalDecisionRecord(t, "Which?", map[string]any{"operator_approval_ref": "approval:operator-in-session"}))},
		{name: "decision record with malformed approval ref", kind: "outcome", payload: outcome(historicalDecisionRecord(t, "Which?", map[string]any{"operator_approval_ref": "operator-in-session"})), refused: true},
		{name: "decision record with null approval ref", kind: "outcome", payload: outcome(historicalDecisionRecord(t, "Which?", map[string]any{"operator_approval_ref": nil})), refused: true},
		{name: "decision record with non-string approval ref", kind: "outcome", payload: outcome(historicalDecisionRecord(t, "Which?", map[string]any{"operator_approval_ref": 7})), refused: true},
		{name: "decision record with approval ref and case-folded null", kind: "outcome", payload: outcome(strings.TrimSuffix(historicalDecisionRecord(t, "Which?", nil), "}") + `,"operator_approval_ref":"approval:operator-in-session","Operator_Approval_Ref":null}`)},
		{name: "decision record with non-string approval ref before case-folded ref", kind: "outcome", payload: outcome(strings.TrimSuffix(historicalDecisionRecord(t, "Which?", nil), "}") + `,"operator_approval_ref":7,"Operator_Approval_Ref":"approval:operator-in-session"}`), refused: true},
		{name: "decision contradicting its only allowed token", kind: "outcome", payload: outcome(historicalDecisionRecord(t, "Which?", map[string]any{"decision": "insufficient_evidence", "unknowns": []string{"u"}, "required_to_decide": []string{"r"}})), refused: true},
		{name: "check subject at 128 characters", kind: "check", payload: check("spec:" + strings.Repeat("a", 123))},
		{name: "check subject above 128 characters", kind: "check", payload: check("spec:" + strings.Repeat("a", 124)), refused: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := historicalOutcomeSubjects(test.kind, []byte(test.payload))
			if test.refused {
				if err == nil {
					t.Fatalf("historically refused predicate admitted with subjects %v", got)
				}
				return
			}
			if err != nil || !reflect.DeepEqual(got, test.want) {
				t.Fatalf("historically admitted predicate = %v: %v", got, err)
			}
		})
	}
}

// The pre-185 resolver admitted a law only when an exists or absent predicate
// at the exact work and version named it; outcome and check predicates named
// nothing, and a non-Product-changing contract admitted nothing.
func TestOutcomeRetirementTransfersExactPresenceAdmission(t *testing.T) {
	previous := version.Value
	version.Value = "v-test"
	t.Cleanup(func() { version.Value = previous })
	path, db := outcomeRetirementFixture(t)
	actor := DeriveWorkflowActorRef("principal:one", "client:one", "agent:one", "session:one")
	if _, err := db.Exec(`INSERT INTO write_guard(active) VALUES(1);
INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,changes_product_truth) VALUES('work-two',2,'Research','internal_sqlite','[]','[]','t',?,'[]',0);
DELETE FROM write_guard;`, actor); err != nil {
		t.Fatal(err)
	}
	seedRetirementWorkPredicate(t, db, "work-one", 1, 0, "exists", []string{"spec:alpha", "decision:alpha"})
	seedRetirementRawPredicate(t, db, "work-one", 1, 1, "outcome", `{"kind":"outcome","allowed":["accepted_decision"],"decision_record":`+historicalDecisionRecord(t, "Which?", nil)+`}`)
	seedRetirementRawPredicate(t, db, "work-one", 1, 2, "check", `{"kind":"check","check_ref":"check:ci/build","immutable_subject_ref":"spec:checked","expected_result":"pass"}`)
	seedRetirementWorkPredicate(t, db, "work-one", 2, 0, "absent", []string{"constitution:beta", "spec:beta"})
	seedRetirementRawPredicate(t, db, "work-one", 2, 1, "outcome", `{"kind":"outcome","allowed":["completed"],"decision_record":null}`)
	seedRetirementWorkPredicate(t, db, "work-two", 1, 0, "exists", []string{"spec:other"})
	seedRetirementRawPredicate(t, db, "work-two", 1, 1, "check", `{"kind":"check","check_ref":"check:ci/build","immutable_subject_ref":"decision:checked","expected_result":"pass"}`)
	seedRetirementWorkPredicate(t, db, "work-two", 2, 0, "exists", []string{"spec:research"})
	snapshot := filepath.Join(t.TempDir(), "snapshot.db")
	if _, err := BackupDatabase(t.Context(), path, snapshot); err != nil {
		t.Fatal(err)
	}
	if _, err := Upgrade(t.Context(), path, nil, UpgradeOptions{SnapshotPath: snapshot}); err != nil {
		t.Fatal(err)
	}
	admitted := map[historicalOutcomeContract][]string{
		{"work-one", 1}: {"spec:alpha", "decision:alpha"},
		{"work-one", 2}: {"constitution:beta", "spec:beta"},
		{"work-two", 1}: {"spec:other"},
	}
	ids := []string{"spec:alpha", "decision:alpha", "spec:checked", "constitution:beta", "spec:beta", "spec:other", "decision:checked", "spec:research"}
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for _, contract := range []historicalOutcomeContract{{"work-one", 1}, {"work-one", 2}, {"work-two", 1}, {"work-two", 2}} {
		for _, id := range ids {
			kind, _, _ := strings.Cut(id, ":")
			record := KnowledgeRecord{ID: id, Kind: kind, Authority: KnowledgeAuthority{Tier: "legislated", LegislatedBy: contract.work, ContractVersion: contract.version}}
			unverified, err := resolveKnowledgeLegislatedAuthorityTx(t.Context(), tx, record)
			if slices.Contains(admitted[contract], id) {
				if err != nil || unverified {
					t.Fatalf("%s at %s v%d not admitted: unverified=%v error=%v", id, contract.work, contract.version, unverified, err)
				}
			} else if !hasFailureKind(err, KindInvalidNoteProof) {
				t.Fatalf("%s at %s v%d not held negative: unverified=%v error=%v", id, contract.work, contract.version, unverified, err)
			}
		}
	}
}

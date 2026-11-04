package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
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
	if _, err := db.Exec(`INSERT INTO write_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO workflow_contract_predicates(work_id,contract_version,predicate_id,ordinal,outcome_kind,outcome_payload) VALUES(?,?,?,?,?,?)`, work, version, fmt.Sprintf("predicate:item-%d", ordinal), ordinal, kind, string(payload)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM write_guard`); err != nil {
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
	if _, err := Upgrade(t.Context(), path, nil); err == nil || !strings.Contains(err.Error(), "verified snapshot") {
		t.Fatalf("upgrade without snapshot: %v", err)
	}
	snapshot := filepath.Join(t.TempDir(), "snapshot.db")
	if _, err := BackupDatabase(t.Context(), path, snapshot); err != nil {
		t.Fatal(err)
	}
	report, err := Upgrade(t.Context(), path, nil, UpgradeOptions{SnapshotPath: snapshot})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(report.Applied, []int{185}) {
		t.Fatalf("applied = %v", report.Applied)
	}
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

func TestOutcomeRetirementMalformedSourceRollsBack(t *testing.T) {
	_, db := outcomeRetirementFixture(t)
	seedRetirementPredicate(t, db, 1, 0, "exists", []string{"spec:valid"})
	if _, err := db.Exec(`INSERT INTO write_guard(active) VALUES(1); UPDATE workflow_contract_predicates SET outcome_payload='{"kind":"exists","subjects":null}' WHERE contract_version=1; DELETE FROM write_guard`); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := applyMigration(t.Context(), tx, migrations[migrationPrefixBefore(t, "outcome_contract_retirement")]); err == nil {
		t.Fatal("malformed source was accepted")
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
}

func TestOutcomeRetirementFailedTransferRollsBack(t *testing.T) {
	_, db := outcomeRetirementFixture(t)
	seedRetirementPredicate(t, db, 1, 0, "exists", []string{"spec:valid"})
	if _, err := db.Exec(`CREATE TRIGGER reject_retirement_transfer BEFORE UPDATE ON workflow_contracts BEGIN SELECT RAISE(ABORT,'transfer refused'); END`); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := applyMigration(t.Context(), tx, migrations[migrationPrefixBefore(t, "outcome_contract_retirement")]); err == nil || !strings.Contains(err.Error(), "transfer refused") {
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
}

func TestOutcomeRetirementSnapshotDriftRefused(t *testing.T) {
	previous := version.Value
	version.Value = "v-test"
	t.Cleanup(func() { version.Value = previous })
	for _, mode := range []string{"source", "snapshot"} {
		t.Run(mode, func(t *testing.T) {
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
			if mode == "source" {
				if _, err := db.Exec(`INSERT INTO write_guard(active) VALUES(1); UPDATE work_items SET title='Changed' WHERE id='work-one'; DELETE FROM write_guard`); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(snapshot, []byte("tampered"), 0o600); err != nil {
				t.Fatal(err)
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

func TestOutcomeRetirementHistoricalPayloadNotSizeLimited(t *testing.T) {
	payload := `{"kind":"exists","surface":"fixture:source","subjects":["spec:valid"]` + strings.Repeat(" ", 70*1024) + `}`
	got, err := historicalOutcomeSubjects("exists", []byte(payload))
	if err != nil || !reflect.DeepEqual(got, []string{"spec:valid"}) {
		t.Fatalf("large valid historical predicate = %v: %v", got, err)
	}
}

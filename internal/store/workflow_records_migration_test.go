package store

import (
	"database/sql"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func ordinaryRecordsMigrationFixture(t *testing.T, receipt bool, artifact string) (*sql.DB, string) {
	t.Helper()
	db, path := terminalStepsMigrationFixture(t)
	advanceHistoricalPrefix(t, db, 180)
	retirementUpgradeExec(t, db, `INSERT INTO write_guard VALUES(1)`)
	correction := workflowJSON(map[string]any{"target_event_id": "event-upgrade-4", "delivery_artifact": artifact, "delivery_state": "asserted"})
	retirementUpgradeExec(t, db, `UPDATE workflow_delivery_state SET correction_id='delivery:replacement',correction_occurred_at=?,correction_payload=? WHERE work_id='work-upgrade-from'`, retirementUpgradeTime, correction)
	if receipt {
		retirementUpgradeExec(t, db, `INSERT INTO effect_receipts(event_id,effect_seq,kind,subject_type,subject_id,actor,payload_version,payload_digest) VALUES('delivery:replacement',200,'workflow.delivery_corrected','work_item','work-upgrade-from','operator',2,?)`, historicalFixturePayloadDigest(t, correction))
	}
	retirementUpgradeExec(t, db, `INSERT INTO workflow_review_state(work_id,state) VALUES('work-upgrade-from','{}')`)
	retirementUpgradeExec(t, db, `DELETE FROM write_guard`)
	return db, path
}

func TestOrdinaryRecordsMigrationPreservesEffectiveDelivery(t *testing.T) {
	useStampedBuild(t)
	db, path := ordinaryRecordsMigrationFixture(t, true, "https://example.com/change/current")
	receipts := retirementUpgradeRows(t, db, `SELECT * FROM effect_receipts ORDER BY effect_seq`)
	retries := retirementUpgradeRows(t, db, `SELECT * FROM idempotency_records ORDER BY rowid`)
	if len(retries) == 0 {
		t.Fatal("missing successful retry fixture")
	}
	snapshot := filepath.Join(t.TempDir(), "before-ordinary-records.db")
	manifest, err := BackupDatabase(t.Context(), path, snapshot)
	if err != nil || manifest.SchemaVersion != 180 {
		t.Fatalf("verified snapshot=%+v err=%v", manifest, err)
	}
	if _, err := Upgrade(t.Context(), path, nil); err != nil {
		t.Fatal(err)
	}
	current, err := workflowDeliveryAssertionRead(t.Context(), db, "work-upgrade-from")
	if err != nil || current == nil || current.Artifact != "https://example.com/change/current" || current.State != "asserted" || current.EventID != "delivery:replacement" || current.Seq != 200 {
		t.Fatalf("current delivery=%+v err=%v", current, err)
	}
	var payloadVersion int
	if err := db.QueryRow(`SELECT payload_version FROM workflow_delivery_state WHERE work_id='work-upgrade-from'`).Scan(&payloadVersion); err != nil || payloadVersion != 2 {
		t.Fatalf("corrected record payload version=%d err=%v", payloadVersion, err)
	}
	var payload, digest string
	if err := db.QueryRow(`SELECT d.payload,r.payload_digest FROM workflow_delivery_state d JOIN effect_receipts r ON r.event_id=d.assertion_id WHERE d.work_id='work-upgrade-from'`).Scan(&payload, &digest); err != nil || historicalFixturePayloadDigest(t, payload) != digest {
		t.Fatalf("effective delivery is not bound to its receipt: payload=%s digest=%s err=%v", payload, digest, err)
	}
	if got := retirementUpgradeRows(t, db, `SELECT * FROM effect_receipts ORDER BY effect_seq`); !reflect.DeepEqual(got, receipts) {
		t.Fatal("upgrade changed immutable receipts")
	}
	if got := retirementUpgradeRows(t, db, `SELECT * FROM idempotency_records ORDER BY rowid`); !reflect.DeepEqual(got, retries) {
		t.Fatal("upgrade changed successful retry receipts")
	}
	if rows := retirementUpgradeRows(t, db, `SELECT name FROM sqlite_schema WHERE name IN ('workflow_correction_state','workflow_artifact_actions')`); len(rows) != 0 {
		t.Fatalf("retired tables=%v", rows)
	}
	if rows := retirementUpgradeRows(t, db, `SELECT name FROM pragma_table_info('workflow_delivery_state') WHERE name LIKE 'correction_%'`); len(rows) != 0 {
		t.Fatalf("retired overlay columns=%v", rows)
	}
	if verified, err := VerifyBackup(t.Context(), snapshot); err != nil || verified.DBChecksum != manifest.DBChecksum {
		t.Fatalf("predecessor snapshot changed: %+v, %v", verified, err)
	}
	retirementUpgradeHealthy(t, db)
}

func TestOrdinaryRecordsMigrationRefusesUnverifiableDelivery(t *testing.T) {
	useStampedBuild(t)
	for _, tc := range []struct {
		name     string
		receipt  bool
		artifact string
	}{
		{"missing_receipt", false, "artifact:current"},
		{"invalid_reference", true, "artifact:contains whitespace"},
		{"oversized_reference", true, "artifact:" + strings.Repeat("a", 129)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, path := ordinaryRecordsMigrationFixture(t, tc.receipt, tc.artifact)
			before := retirementUpgradeSnapshot(t, db)
			_, err := Upgrade(t.Context(), path, nil)
			if err == nil || !strings.Contains(err.Error(), "verified snapshot") {
				t.Fatalf("unverifiable delivery upgrade=%v, want verified-snapshot refusal", err)
			}
			if after := retirementUpgradeSnapshot(t, db); !reflect.DeepEqual(before, after) {
				t.Fatal("refused migration changed predecessor rows or manifest")
			}
		})
	}
}

func TestOrdinaryRecordsMigrationRollsBackDestructiveFold(t *testing.T) {
	useStampedBuild(t)
	db, path := ordinaryRecordsMigrationFixture(t, true, "artifact:current")
	retirementUpgradeExec(t, db, `CREATE TRIGGER ordinary_records_failure BEFORE UPDATE ON workflow_review_state BEGIN SELECT RAISE(ABORT,'ordinary records rollback witness'); END`)
	before := retirementUpgradeSnapshot(t, db)
	_, err := Upgrade(t.Context(), path, nil)
	if err == nil || !strings.Contains(err.Error(), "ordinary records rollback witness") {
		t.Fatalf("destructive rollback witness=%v", err)
	}
	if after := retirementUpgradeSnapshot(t, db); !reflect.DeepEqual(before, after) {
		t.Fatal("failed migration changed predecessor rows or manifest")
	}
	retirementUpgradeExec(t, db, `DROP TRIGGER ordinary_records_failure`)
	if _, err := Upgrade(t.Context(), path, nil); err != nil {
		t.Fatal(err)
	}
	retirementUpgradeHealthy(t, db)
}

func TestOrdinaryRecordsMigrationRefusesMismatchedReceipt(t *testing.T) {
	useStampedBuild(t)
	db, path := ordinaryRecordsMigrationFixture(t, true, "artifact:current")
	retirementUpgradeExec(t, db, `INSERT INTO write_guard VALUES(1); UPDATE workflow_delivery_state SET correction_payload=json_set(correction_payload,'$.delivery_artifact','artifact:tampered') WHERE work_id='work-upgrade-from'; DELETE FROM write_guard`)
	before := retirementUpgradeSnapshot(t, db)
	_, err := Upgrade(t.Context(), path, nil)
	if err == nil || !strings.Contains(err.Error(), "verified snapshot") {
		t.Fatalf("mismatched receipt upgrade=%v", err)
	}
	if after := retirementUpgradeSnapshot(t, db); !reflect.DeepEqual(before, after) {
		t.Fatal("receipt mismatch changed predecessor")
	}
}

func TestOrdinaryRecordsMigrationRefusesPartialOverlay(t *testing.T) {
	useStampedBuild(t)
	db, path := ordinaryRecordsMigrationFixture(t, true, "artifact:current")
	retirementUpgradeExec(t, db, `INSERT INTO write_guard VALUES(1); UPDATE workflow_delivery_state SET correction_id=NULL WHERE work_id='work-upgrade-from'; DELETE FROM write_guard`)
	before := retirementUpgradeSnapshot(t, db)
	_, err := Upgrade(t.Context(), path, nil)
	if err == nil || !strings.Contains(err.Error(), "verified snapshot") {
		t.Fatalf("partial overlay upgrade=%v", err)
	}
	if after := retirementUpgradeSnapshot(t, db); !reflect.DeepEqual(before, after) {
		t.Fatal("partial overlay refusal changed predecessor")
	}
}

func TestOrdinaryRecordsDiscoverRetainedReviewWithoutExit(t *testing.T) {
	db, _, _, _ := lawPinUpgradeFixtureAt(t, 175)
	work := "upgrade-review-1-true"
	records, err := workflowCurrentRecords(t.Context(), db, work)
	if err != nil {
		t.Fatal(err)
	}
	if !records["record_review"] {
		t.Fatalf("retained Review missing from current records: %v", records)
	}
}

func TestOrdinaryRecordsMigrationPreservesConsumedReview(t *testing.T) {
	useStampedBuild(t)
	db, path := ordinaryRecordsMigrationFixture(t, true, "artifact:current")
	retirementUpgradeExec(t, db, `INSERT INTO write_guard VALUES(1); UPDATE workflow_review_state SET state=json_object('accepted',json_object('report_ref','review:one','report',json_object('disposition','correction_required')),'correction_funding',json_object('report_ref','review:one','correction_ref','correction:one')) WHERE work_id='work-upgrade-from'; DELETE FROM write_guard`)
	before := retirementUpgradeRows(t, db, `SELECT json_extract(state,'$.accepted') FROM workflow_review_state`)
	if _, err := Upgrade(t.Context(), path, nil); err != nil {
		t.Fatal(err)
	}
	after := retirementUpgradeRows(t, db, `SELECT json_extract(state,'$.accepted') FROM workflow_review_state`)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("accepted Review changed: before=%v after=%v", before, after)
	}
}

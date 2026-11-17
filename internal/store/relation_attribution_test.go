package store

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func attributionFixture(t *testing.T) (*Store, string, AttributionManifest) {
	t.Helper()
	s := openTemp(t)
	for _, id := range []string{"repair-a", "repair-b", "repair-c", "repair-d", "repair-e"} {
		seedWork(t, s, id)
	}
	_, err := s.db.Exec(`INSERT INTO write_guard(active) VALUES(1);
INSERT INTO effect_receipts(event_id,effect_seq,kind,subject_type,subject_id,actor,payload_version,relation_seq) VALUES
('repair-one',(SELECT MAX(effect_seq)+1 FROM effect_receipts),'relation.added','work_item','repair-a','operator',1,9876541);
INSERT INTO effect_receipts(event_id,effect_seq,kind,subject_type,subject_id,actor,payload_version,relation_seq) VALUES
('repair-two',(SELECT MAX(effect_seq)+1 FROM effect_receipts),'relation.added','work_item','repair-a','operator',1,9876542);
INSERT INTO effect_receipts(event_id,effect_seq,kind,subject_type,subject_id,actor,payload_version,relation_seq) VALUES
('repair-future',(SELECT MAX(effect_seq)+1 FROM effect_receipts),'relation.added','work_item','repair-a','operator',1,9876543);
INSERT INTO relations(id,work_id_from,work_id_to,kind,created_at,resolution_id,reason,record_version) VALUES
(9876541,'repair-a','repair-b','depends_on','2025-01-01T00:00:00Z',NULL,'quote '' 雪',2),
(9876542,'repair-a','repair-c','depends_on','2025-01-01T00:00:00Z',NULL,'second',3),
(9876543,'repair-a','repair-d','depends_on','2027-01-01T00:00:00Z',NULL,'future',4),
(9876544,'repair-a','repair-e','depends_on','2025-01-01T00:00:00Z','', 'hidden',5);
DELETE FROM write_guard`)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "snapshot.sqlite")
	if _, err := Backup(t.Context(), s, path); err != nil {
		t.Fatal(err)
	}
	m, err := InspectRelationAttribution(t.Context(), path, "2026-01-01T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Candidates) != 2 || m.Candidates[0].ID != 9876541 || m.Candidates[1].ID != 9876542 {
		t.Fatalf("population: %+v", m.Candidates)
	}
	return s, path, m
}

func TestRelationAttributionClearUndo(t *testing.T) {
	s, path, m := attributionFixture(t)
	ctx := t.Context()
	req := AttributionRequest{BackupPath: path, Manifest: m, IdempotencyKey: "clear-one"}
	if _, err := s.VerifyRelationAttribution(ctx, req); err == nil {
		t.Fatal("verification accepted missing clear")
	}
	receiptsBefore := captureEvidenceRows(t, s.db, `SELECT * FROM effect_receipts ORDER BY event_id`)
	beforeRows, err := s.db.Query(`SELECT ` + attributionColumns + ` FROM relations ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	before, err := scanAttributionRows(beforeRows)
	if err != nil {
		t.Fatal(err)
	}
	q8, err := s.QueryQ8(ctx, Q8Request{Work: "repair-a", Direction: "outgoing"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.ClearRelationAttribution(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.Treated, []int64{9876541, 9876542}) || result.GraphDigest != m.GraphDigest || result.ReceiptDigest != m.ReceiptDigest {
		t.Fatalf("clear: %+v", result)
	}
	q8After, err := s.QueryQ8(ctx, Q8Request{Work: "repair-a", Direction: "outgoing"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(q8.Edges, q8After.Edges) {
		t.Fatal("graph changed")
	}
	afterRows, err := s.db.Query(`SELECT ` + attributionColumns + ` FROM relations ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	after, err := scanAttributionRows(afterRows)
	if err != nil {
		t.Fatal(err)
	}
	for i := range before {
		if before[i].ID == 9876541 || before[i].ID == 9876542 {
			before[i].Reason = ""
			before[i].Version = 0
			before[i].Types[6], before[i].Types[7] = "text", "integer"
		}
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("non-treatment relation fields changed: before=%+v after=%+v", before, after)
	}
	if got := captureEvidenceRows(t, s.db, `SELECT * FROM effect_receipts ORDER BY event_id`); !reflect.DeepEqual(receiptsBefore, got) {
		t.Fatal("receipt bytes changed")
	}
	history, err := s.QueryQ7(ctx, Q7Request{Work: "repair-a", RecordKinds: []string{"relation"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(history.Entries) != 1 || history.Entries[0].EntryID != "relation:9876543" {
		t.Fatalf("history: %+v", history.Entries)
	}
	replay, err := s.ClearRelationAttribution(ctx, req)
	if err != nil || !reflect.DeepEqual(result, replay) {
		t.Fatalf("clear replay: %+v %v", replay, err)
	}
	bad := req
	bad.Manifest.Cutoff = "2026-02-01T00:00:00Z"
	if _, err := s.ClearRelationAttribution(ctx, bad); err == nil {
		t.Fatal("changed clear request accepted")
	}
	verify, err := s.VerifyRelationAttribution(ctx, req)
	if err != nil || !reflect.DeepEqual(verify.Treated, result.Treated) || !verify.GraphPreserved || !verify.ReceiptsPreserved {
		t.Fatalf("verify: %+v %v", verify, err)
	}
	undo := AttributionUndoRequest{AttributionRequest: AttributionRequest{BackupPath: path, Manifest: m, IdempotencyKey: "undo-one"}, ClearKey: req.IdempotencyKey}
	restored, err := s.UndoRelationAttribution(ctx, undo)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(restored.Restored, result.Treated) {
		t.Fatalf("undo: %+v", restored)
	}
	verified, err := s.VerifyRelationAttribution(ctx, req)
	if err != nil || !reflect.DeepEqual(verified.Restored, result.Treated) || verified.GraphDigest != m.GraphDigest || verified.ReceiptDigest != m.ReceiptDigest || !verified.GraphPreserved || !verified.ReceiptsPreserved {
		t.Fatalf("restored verification: %+v %v", verified, err)
	}
	replay, err = s.UndoRelationAttribution(ctx, undo)
	if err != nil || !reflect.DeepEqual(restored, replay) {
		t.Fatalf("undo replay: %+v %v", replay, err)
	}
	var reason string
	var version int
	if err := s.db.QueryRow(`SELECT reason,record_version FROM relations WHERE id=9876541`).Scan(&reason, &version); err != nil || reason != "quote ' 雪" || version != 2 {
		t.Fatalf("restored %q %d %v", reason, version, err)
	}
	badUndo := undo
	badUndo.ClearKey = "other"
	if _, err := s.UndoRelationAttribution(ctx, badUndo); err == nil {
		t.Fatal("changed undo request accepted")
	}
	collision := req
	collision.IdempotencyKey = undo.IdempotencyKey
	if _, err := s.ClearRelationAttribution(ctx, collision); err == nil {
		t.Fatal("clear reused undo key")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if replay, err := s.ClearRelationAttribution(ctx, req); err != nil || !reflect.DeepEqual(replay, result) {
		t.Fatalf("clear replay without backup: %+v %v", replay, err)
	}
	if replay, err := s.UndoRelationAttribution(ctx, undo); err != nil || !reflect.DeepEqual(replay, restored) {
		t.Fatalf("undo replay without backup: %+v %v", replay, err)
	}
}

func TestRelationAttributionUndoRefusesTamperedBackup(t *testing.T) {
	s, path, m := attributionFixture(t)
	ctx := t.Context()
	req := AttributionRequest{BackupPath: path, Manifest: m, IdempotencyKey: "clear-tamper"}
	if _, err := s.ClearRelationAttribution(ctx, req); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("tampered"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	undo := AttributionUndoRequest{AttributionRequest: AttributionRequest{BackupPath: path, Manifest: m, IdempotencyKey: "undo-tamper"}, ClearKey: req.IdempotencyKey}
	if _, err := s.UndoRelationAttribution(ctx, undo); err == nil {
		t.Fatal("undo accepted tampered backup")
	}
	var version int
	if err := s.db.QueryRow(`SELECT record_version FROM relations WHERE id=9876541`).Scan(&version); err != nil || version != 0 {
		t.Fatalf("undo partially restored: %d %v", version, err)
	}
}

func TestRelationAttributionMutationRollsBack(t *testing.T) {
	s, path, m := attributionFixture(t)
	ctx := t.Context()
	req := AttributionRequest{BackupPath: path, Manifest: m, IdempotencyKey: "rollback-clear"}
	trigger := `CREATE TRIGGER attribution_test_failure BEFORE UPDATE ON relations WHEN NEW.id=9876542 BEGIN SELECT RAISE(ABORT,'synthetic failure'); END`
	if _, err := s.db.Exec(trigger); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClearRelationAttribution(ctx, req); err == nil {
		t.Fatal("failed clear committed")
	}
	var version int
	if err := s.db.QueryRow(`SELECT record_version FROM relations WHERE id=9876541`).Scan(&version); err != nil || version != 2 {
		t.Fatalf("clear did not roll back: %d %v", version, err)
	}
	if _, found, err := s.LookupMutationIdempotency(ctx, attributionKey("clear", req.IdempotencyKey)); err != nil || found {
		t.Fatalf("failed clear recorded: %v %v", found, err)
	}
	if _, err := s.db.Exec(`DROP TRIGGER attribution_test_failure`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClearRelationAttribution(ctx, req); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(trigger); err != nil {
		t.Fatal(err)
	}
	undo := AttributionUndoRequest{AttributionRequest: AttributionRequest{BackupPath: path, Manifest: m, IdempotencyKey: "rollback-undo"}, ClearKey: req.IdempotencyKey}
	if _, err := s.UndoRelationAttribution(ctx, undo); err == nil {
		t.Fatal("failed undo committed")
	}
	if err := s.db.QueryRow(`SELECT record_version FROM relations WHERE id=9876541`).Scan(&version); err != nil || version != 0 {
		t.Fatalf("undo did not roll back: %d %v", version, err)
	}
	if _, found, err := s.LookupMutationIdempotency(ctx, attributionKey("undo", undo.IdempotencyKey)); err != nil || found {
		t.Fatalf("failed undo recorded: %v %v", found, err)
	}
}

func TestRelationAttributionPreservesBlobReason(t *testing.T) {
	s, _, _ := attributionFixture(t)
	ctx := t.Context()
	_, err := s.db.Exec(`INSERT INTO write_guard(active) VALUES(1); UPDATE relations SET reason=CAST('second' AS BLOB) WHERE id=9876542; DELETE FROM write_guard`)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "blob.sqlite")
	if _, err := Backup(ctx, s, path); err != nil {
		t.Fatal(err)
	}
	m, err := InspectRelationAttribution(ctx, path, "2026-01-01T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	req := AttributionRequest{BackupPath: path, Manifest: m, IdempotencyKey: "blob-clear"}
	_, err = s.db.Exec(`INSERT INTO write_guard(active) VALUES(1); UPDATE relations SET reason='second' WHERE id=9876542; DELETE FROM write_guard`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClearRelationAttribution(ctx, req); err == nil {
		t.Fatal("text accepted for expected blob")
	}
	_, err = s.db.Exec(`INSERT INTO write_guard(active) VALUES(1); UPDATE relations SET reason=CAST('second' AS BLOB) WHERE id=9876542; DELETE FROM write_guard`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClearRelationAttribution(ctx, req); err != nil {
		t.Fatal(err)
	}
	undo := AttributionUndoRequest{AttributionRequest: AttributionRequest{BackupPath: path, Manifest: m, IdempotencyKey: "blob-undo"}, ClearKey: req.IdempotencyKey}
	if _, err := s.UndoRelationAttribution(ctx, undo); err != nil {
		t.Fatal(err)
	}
	var kind, value string
	if err := s.db.QueryRow(`SELECT typeof(reason),CAST(reason AS TEXT) FROM relations WHERE id=9876542`).Scan(&kind, &value); err != nil || kind != "blob" || value != "second" {
		t.Fatalf("blob restore: %q %q %v", kind, value, err)
	}
}

func TestRelationAttributionRefusalAndSkippedUndo(t *testing.T) {
	s, path, m := attributionFixture(t)
	ctx := t.Context()
	req := AttributionRequest{BackupPath: path, Manifest: m, IdempotencyKey: "clear-two"}
	_, err := s.db.Exec(`INSERT INTO write_guard(active) VALUES(1); UPDATE relations SET reason='changed' WHERE id=9876542; DELETE FROM write_guard`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClearRelationAttribution(ctx, req); err == nil {
		t.Fatal("changed fingerprint accepted")
	}
	var version int
	if err := s.db.QueryRow(`SELECT record_version FROM relations WHERE id=9876541`).Scan(&version); err != nil || version != 2 {
		t.Fatalf("partial clear: %d %v", version, err)
	}
	_, err = s.db.Exec(`INSERT INTO write_guard(active) VALUES(1); UPDATE relations SET reason='second' WHERE id=9876542; DELETE FROM write_guard`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClearRelationAttribution(ctx, req); err != nil {
		t.Fatal(err)
	}
	_, err = s.db.Exec(`INSERT INTO write_guard(active) VALUES(1); UPDATE relations SET resolution_id='' WHERE id=9876541; DELETE FROM relations WHERE id=9876542; DELETE FROM write_guard`)
	if err != nil {
		t.Fatal(err)
	}
	undo := AttributionUndoRequest{AttributionRequest: AttributionRequest{BackupPath: path, Manifest: m, IdempotencyKey: "undo-two"}, ClearKey: req.IdempotencyKey}
	result, err := s.UndoRelationAttribution(ctx, undo)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.Gone, []int64{9876542}) || !reflect.DeepEqual(result.FingerprintChanged, []int64{9876541}) || len(result.Restored) != 0 {
		t.Fatalf("skips: %+v", result)
	}
	verify, err := s.VerifyRelationAttribution(ctx, req)
	if err != nil || verify.GraphPreserved || !verify.ReceiptsPreserved {
		t.Fatalf("changed graph verification: %+v %v", verify, err)
	}
}

func TestRelationAttributionManifestAndBackupRefusal(t *testing.T) {
	s, path, m := attributionFixture(t)
	ctx := t.Context()
	req := AttributionRequest{BackupPath: path, Manifest: m, IdempotencyKey: "refuse"}
	for _, mutation := range []string{
		`UPDATE relations SET record_version=0 WHERE id=9876542`,
		`UPDATE relations SET record_version=7 WHERE id=9876542`,
		`UPDATE relations SET resolution_id='' WHERE id=9876542`,
	} {
		_, err := s.db.Exec(`INSERT INTO write_guard(active) VALUES(1); ` + mutation + `; DELETE FROM write_guard`)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.ClearRelationAttribution(ctx, req); err == nil {
			t.Fatalf("accepted mismatch %s", mutation)
		}
		var version int
		if err := s.db.QueryRow(`SELECT record_version FROM relations WHERE id=9876541`).Scan(&version); err != nil || version != 2 {
			t.Fatalf("partial clear: %d %v", version, err)
		}
		_, err = s.db.Exec(`INSERT INTO write_guard(active) VALUES(1); UPDATE relations SET record_version=3,resolution_id=NULL WHERE id=9876542; DELETE FROM write_guard`)
		if err != nil {
			t.Fatal(err)
		}
	}
	bad := req
	bad.Manifest.Candidates = append([]AttributionCandidate(nil), m.Candidates[:1]...)
	if _, err := s.ClearRelationAttribution(ctx, bad); err == nil {
		t.Fatal("incomplete manifest accepted")
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("tampered"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClearRelationAttribution(ctx, req); err == nil {
		t.Fatal("tampered backup accepted")
	}
	var version int
	if err := s.db.QueryRow(`SELECT record_version FROM relations WHERE id=9876541`).Scan(&version); err != nil || version != 2 {
		t.Fatalf("tamper caused partial clear: %d %v", version, err)
	}
}

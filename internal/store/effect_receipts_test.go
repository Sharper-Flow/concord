package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"testing"
)

// The read fixture deliberately has no projection guards or unrelated schema.
// Each probe must answer from current facts with no log or replacement view.
func effectReceiptReadFixture(t *testing.T) *Store {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec(`
CREATE TABLE effect_receipts(event_id TEXT PRIMARY KEY,effect_seq INTEGER UNIQUE,kind TEXT,subject_type TEXT,subject_id TEXT,actor TEXT,payload_version INTEGER,payload_digest TEXT);
`)
	if err != nil {
		t.Fatal(err)
	}
	return &Store{db: db}
}

func seedEffectReceiptRead(t *testing.T, s *Store, seq int64, event Event) {
	t.Helper()
	canonical, err := canonicalJSON(event.Payload)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(canonical)
	if _, err := s.db.Exec(`INSERT INTO effect_receipts VALUES(?,?,?,?,?,?,?,?)`, event.EventID, seq, event.Kind, event.SubjectType, event.SubjectID, event.Actor, event.PayloadVersion, "sha256:"+hex.EncodeToString(sum[:])); err != nil {
		t.Fatal(err)
	}
}

func hideEffectLog(t *testing.T, s *Store) {
	t.Helper()
	for _, schema := range []string{"main", "temp"} {
		var count int
		if err := s.db.QueryRow(`SELECT count(*) FROM ` + schema + `.sqlite_schema WHERE name IN ('domain_events','hidden_domain_events','domain_events_advance_effect_sequence','domain_events_insert')`).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s retained a log object: count=%d, err=%v", schema, count, err)
		}
	}
}

func TestEffectReceiptConflictLogBlind(t *testing.T) {
	s := effectReceiptReadFixture(t)
	e := testEvent()
	seedEffectReceiptRead(t, s, 7, e)
	hideEffectLog(t, s)
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	assertFailureKind(t, classifyEventIDConflict(t.Context(), tx, e, fmt.Errorf("unique collision")), KindDuplicateEvent)
	e.Payload = []byte(`{"display_name":"different"}`)
	assertFailureKind(t, classifyEventIDConflict(t.Context(), tx, e, fmt.Errorf("unique collision")), KindIdempotencyConflict)
}

func TestEffectReceiptAppend(t *testing.T) {
	s := openTemp(t)
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	e := testEvent()
	e.Payload = []byte(`{}`)
	seq, err := appendPreparedEvent(t.Context(), tx, e)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var storedSeq int64
	var kind, subjectType, subjectID, actor, digest string
	var version int
	if err := s.db.QueryRow(`SELECT effect_seq,kind,subject_type,subject_id,actor,payload_version,payload_digest FROM effect_receipts WHERE event_id=?`, e.EventID).Scan(&storedSeq, &kind, &subjectType, &subjectID, &actor, &version, &digest); err != nil {
		t.Fatal(err)
	}
	if storedSeq != seq || kind != e.Kind || subjectType != string(e.SubjectType) || subjectID != e.SubjectID || actor != e.Actor || version != e.PayloadVersion || digest != "sha256:44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a" {
		t.Fatalf("receipt = %d/%s/%s/%s/%s/%d/%s", storedSeq, kind, subjectType, subjectID, actor, version, digest)
	}
	hideEffectLog(t, s)
}

func TestEffectReceiptRollsBackWithAppend(t *testing.T) {
	s := openTemp(t)
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AppendEvent(t.Context(), tx, testEvent()); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := s.db.QueryRow(`SELECT count(*) FROM effect_receipts`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("rollback left %d rows: %v", count, err)
	}
	hideEffectLog(t, s)
}

package store

import "testing"

func TestAdmissionEffectSequenceLogBlind(t *testing.T) {
	s := openTemp(t)
	seedWork(t, s, "admission-work")
	var eventID string
	var want int64
	if err := s.db.QueryRow(`SELECT event_id,effect_seq FROM effect_receipts WHERE subject_type='work_item' AND subject_id='admission-work' ORDER BY effect_seq DESC LIMIT 1`).Scan(&eventID, &want); err != nil {
		t.Fatal(err)
	}
	var historyTables int
	if err := s.db.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE name='domain_events'`).Scan(&historyTables); err != nil || historyTables != 0 {
		t.Fatalf("retired event-log tables=%d, want zero: %v", historyTables, err)
	}
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	got, err := eventSequenceTx(t.Context(), tx, eventID)
	if err != nil || got != want {
		t.Fatalf("effect sequence without the log = %d, %v; want %d", got, err, want)
	}
	_, err = eventSequenceTx(t.Context(), tx, "missing-effect")
	assertFailureKind(t, err, KindUnavailable)
}

package store

import (
	"database/sql"
	"errors"
	"reflect"
	"testing"
)

func TestEffectReceiptReadPreservesHeaderScan(t *testing.T) {
	s := effectReceiptReadFixture(t)
	e := testEvent()
	seedEffectReceiptRead(t, s, 7, e)
	for _, tc := range []struct{ name, update, id string }{
		{"complete", "", e.EventID},
		{"missing", "", "missing"},
		{"null subject type", "UPDATE effect_receipts SET subject_type=NULL", e.EventID},
		{"invalid payload version", "UPDATE effect_receipts SET subject_type='product',payload_version='invalid'", e.EventID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.update != "" {
				if _, err := s.db.Exec(tc.update); err != nil {
					t.Fatal(err)
				}
			}
			var want Event
			var digest string
			want.EventID = tc.id
			wantErr := s.db.QueryRow(`SELECT effect_seq,kind,subject_type,subject_id,actor,payload_version,payload_digest FROM effect_receipts WHERE event_id=?`, tc.id).
				Scan(&want.Seq, &want.Kind, &want.SubjectType, &want.SubjectID, &want.Actor, &want.PayloadVersion, &digest)
			wantFound := wantErr == nil
			if wantErr == sql.ErrNoRows {
				want, digest, wantErr = Event{}, "", nil
			}
			got, found, err := readEffectReceipt(t.Context(), s.db, tc.id)
			if !reflect.DeepEqual(got.event(), want) || got.PayloadDigest != digest || found != wantFound {
				t.Fatalf("receipt/header scan differ: got=%+v found=%t want=%+v found=%t", got, found, want, wantFound)
			}
			if (err == nil) != (wantErr == nil) || (err != nil && err.Error() != wantErr.Error()) {
				t.Fatalf("scan errors differ: got=%v want=%v", err, wantErr)
			}
		})
	}
}

func TestStoreSequenceFailurePreservesEnvelope(t *testing.T) {
	s := effectReceiptReadFixture(t)
	if _, err := s.db.Exec(`CREATE TABLE store_sequences(name TEXT PRIMARY KEY,last INTEGER NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	seq, err := allocateStoreSequence(t.Context(), tx, "absent")
	var failure *Failure
	if seq != 0 || !errors.As(err, &failure) || failure.Kind != KindInvariantViolation || failure.Op != "append_event" || failure.Detail != "cannot advance the store sequence" || failure.RetrySafe || failure.EffectPossible || failure.RecoveryAction != "restore the store's sequence state" || !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("changed store sequence failure/cause: seq=%d err=%v", seq, err)
	}
}

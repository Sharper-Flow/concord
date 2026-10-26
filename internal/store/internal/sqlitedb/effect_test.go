package sqlitedb

import (
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
)

func receiptDatabase(t *testing.T) *sql.DB {
	t.Helper()
	db, err := Open(DSN(filepath.Join(t.TempDir(), "receipts.db")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`CREATE TABLE store_sequences(name TEXT PRIMARY KEY,last INTEGER NOT NULL);
INSERT INTO store_sequences VALUES('effect',0),('relation',0);
CREATE TABLE effect_receipts(event_id TEXT PRIMARY KEY,effect_seq INTEGER UNIQUE,kind TEXT,subject_type TEXT,subject_id TEXT,actor TEXT,payload_version INTEGER,payload_digest TEXT,delivery_assertion INTEGER,relation_seq INTEGER);`); err != nil {
		t.Fatal(err)
	}
	return db
}

func receiptIdentity() EffectReceipt {
	return EffectReceipt{EventID: "receipt-1", Seq: 1, Kind: "synthetic.kind", SubjectType: "synthetic", SubjectID: "subject-雪", Actor: "actor", PayloadVersion: 7, PayloadDigest: "sha256:synthetic"}
}

func TestEffectReceiptRoundTripAndOrdering(t *testing.T) {
	db := receiptDatabase(t)
	ctx := t.Context()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for _, asserted := range []bool{false, true} {
		want := receiptIdentity()
		want.Seq, err = AllocateSequence(ctx, tx, "effect")
		if err != nil {
			t.Fatal(err)
		}
		var relation any
		if asserted {
			want.EventID = "receipt-2"
			relation, err = AllocateSequence(ctx, tx, "relation")
			if err != nil {
				t.Fatal(err)
			}
		}
		if err := AppendReceipt(ctx, tx, want, asserted, relation); err != nil {
			t.Fatal(err)
		}
		got, found, err := ReadReceipt(ctx, tx, want.EventID)
		if err != nil || !found || !reflect.DeepEqual(got, want) {
			t.Fatalf("receipt=%+v found=%t err=%v want=%+v", got, found, err, want)
		}
		var storedAssertion bool
		var storedRelation sql.NullInt64
		if err := tx.QueryRow(`SELECT delivery_assertion,relation_seq FROM effect_receipts WHERE event_id=?`, want.EventID).Scan(&storedAssertion, &storedRelation); err != nil {
			t.Fatal(err)
		}
		if storedAssertion != asserted || storedRelation.Valid != asserted || (asserted && (storedRelation.Int64 != 1 || want.Seq != 2)) {
			t.Fatalf("assertion/order/null changed: assertion=%t relation=%+v seq=%d", storedAssertion, storedRelation, want.Seq)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, found, err := ReadReceipt(ctx, db, "receipt-2"); err != nil || !found {
		t.Fatalf("committed receipt: found=%t err=%v", found, err)
	}
}

func TestEffectReceiptSequenceRollback(t *testing.T) {
	db := receiptDatabase(t)
	ctx := t.Context()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	r := receiptIdentity()
	r.Seq, err = AllocateSequence(ctx, tx, "effect")
	if err != nil {
		t.Fatal(err)
	}
	relationSeq, err := AllocateSequence(ctx, tx, "relation")
	if err != nil {
		t.Fatal(err)
	}
	if err := AppendReceipt(ctx, tx, r, false, relationSeq); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if got, found, err := ReadReceipt(ctx, db, r.EventID); err != nil || found || got != (EffectReceipt{}) {
		t.Fatalf("rolled-back receipt: %+v found=%t err=%v", got, found, err)
	}
	for _, name := range []string{"effect", "relation"} {
		var last int64
		if err := db.QueryRow(`SELECT last FROM store_sequences WHERE name=?`, name).Scan(&last); err != nil || last != 0 {
			t.Fatalf("rollback consumed %s sequence: %d err=%v", name, last, err)
		}
	}
}

func TestEffectReceiptRawErrors(t *testing.T) {
	db := receiptDatabase(t)
	ctx := t.Context()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	r := receiptIdentity()
	if err := AppendReceipt(ctx, tx, r, false, nil); err != nil {
		t.Fatal(err)
	}
	err = AppendReceipt(ctx, tx, r, false, nil)
	var wrapped *Error
	if err == nil || errors.As(err, &wrapped) {
		t.Fatalf("insert conflict was lost or wrapped: %v", err)
	}
	if _, found, err := ReadReceipt(ctx, tx, r.EventID); err != nil || !found {
		t.Fatalf("insert failure made the caller transaction unreadable: found=%t err=%v", found, err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := AppendReceipt(ctx, tx, r, false, nil); err != sql.ErrTxDone {
		t.Fatalf("changed raw insert cause: %v", err)
	}
	got, found, err := ReadReceipt(ctx, tx, r.EventID)
	if err != sql.ErrTxDone || found || got != (EffectReceipt{EventID: r.EventID}) {
		t.Fatalf("changed raw read/partial result: %+v found=%t err=%v", got, found, err)
	}
}

func TestEffectSequenceFailureFields(t *testing.T) {
	db := receiptDatabase(t)
	ctx := t.Context()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	seq, err := AllocateSequence(ctx, tx, "absent")
	var failure *Error
	if seq != 0 || !errors.As(err, &failure) || failure.Kind != InvariantViolation || failure.Op != "append_event" || failure.Detail != "cannot advance the store sequence" || failure.RetrySafe || failure.EffectPossible || failure.RecoveryAction != "restore the store's sequence state" || !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("changed sequence failure/cause: seq=%d err=%v", seq, err)
	}
}

func TestEffectReceiptHeaderIdentity(t *testing.T) {
	r := receiptIdentity()
	for _, tc := range []struct {
		field  string
		mutate func(*EffectReceipt)
	}{
		{"kind", func(r *EffectReceipt) { r.Kind += "-changed" }},
		{"subject_type", func(r *EffectReceipt) { r.SubjectType += "-changed" }},
		{"subject_id", func(r *EffectReceipt) { r.SubjectID += "-changed" }},
		{"actor", func(r *EffectReceipt) { r.Actor += "-changed" }},
		{"payload_version", func(r *EffectReceipt) { r.PayloadVersion++ }},
	} {
		t.Run(tc.field, func(t *testing.T) {
			attempted := r
			tc.mutate(&attempted)
			if got := r.HeaderDifferences(attempted); !reflect.DeepEqual(got, []string{tc.field}) {
				t.Fatalf("changed identity dimension: %v", got)
			}
		})
	}
	all := EffectReceipt{}
	if got := r.HeaderDifferences(all); !reflect.DeepEqual(got, []string{"kind", "subject_type", "subject_id", "actor", "payload_version"}) {
		t.Fatalf("changed difference order: %v", got)
	}
	r.EventID, r.Seq, r.PayloadDigest = "different-key", 99, "different-digest"
	if got := r.HeaderDifferences(receiptIdentity()); got != nil {
		t.Fatalf("header identity acquired lookup/order/digest policy: %v", got)
	}
}

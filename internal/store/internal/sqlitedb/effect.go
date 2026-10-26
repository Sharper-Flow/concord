package sqlitedb

import (
	"context"
	"database/sql"
)

// EffectReceipt records a write's durable identity and order, without its
// payload or occurrence time. Callers own payload canonicalization and policy.
type EffectReceipt struct {
	EventID        string
	Seq            int64
	Kind           string
	SubjectType    string
	SubjectID      string
	Actor          string
	PayloadVersion int
	PayloadDigest  string
}

func AllocateSequence(ctx context.Context, tx *sql.Tx, name string) (int64, error) {
	var seq int64
	err := tx.QueryRowContext(ctx, `UPDATE store_sequences SET last=last+1 WHERE name=? RETURNING last`, name).Scan(&seq)
	if err != nil {
		return 0, failure(InvariantViolation, "append_event", "cannot advance the store sequence", false, "restore the store's sequence state", err)
	}
	return seq, nil
}

// AppendReceipt leaves raw SQL errors intact for the caller's conflict policy.
// relationSeq is the caller's allocated int64 or nil, preserving SQL NULL.
func AppendReceipt(ctx context.Context, tx *sql.Tx, receipt EffectReceipt, deliveryAssertion bool, relationSeq any) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO effect_receipts(event_id,effect_seq,kind,subject_type,subject_id,actor,payload_version,payload_digest,delivery_assertion,relation_seq) VALUES(?,?,?,?,?,?,?,?,?,?)`, receipt.EventID, receipt.Seq, receipt.Kind, receipt.SubjectType, receipt.SubjectID, receipt.Actor, receipt.PayloadVersion, receipt.PayloadDigest, deliveryAssertion, relationSeq)
	return err
}

func ReadReceipt(ctx context.Context, q Queryer, eventID string) (EffectReceipt, bool, error) {
	var receipt EffectReceipt
	receipt.EventID = eventID
	err := q.QueryRowContext(ctx, `SELECT effect_seq,kind,subject_type,subject_id,actor,payload_version,payload_digest FROM effect_receipts WHERE event_id=?`, eventID).
		Scan(&receipt.Seq, &receipt.Kind, &receipt.SubjectType, &receipt.SubjectID, &receipt.Actor, &receipt.PayloadVersion, &receipt.PayloadDigest)
	if err == sql.ErrNoRows {
		return EffectReceipt{}, false, nil
	}
	return receipt, err == nil, err
}

// HeaderDifferences compares the retry identity fields, not the allocated
// sequence or lookup key. The caller separately checks the payload digest.
func (r EffectReceipt) HeaderDifferences(attempted EffectReceipt) []string {
	var diffs []string
	if attempted.Kind != r.Kind {
		diffs = append(diffs, "kind")
	}
	if attempted.SubjectType != r.SubjectType {
		diffs = append(diffs, "subject_type")
	}
	if attempted.SubjectID != r.SubjectID {
		diffs = append(diffs, "subject_id")
	}
	if attempted.Actor != r.Actor {
		diffs = append(diffs, "actor")
	}
	if attempted.PayloadVersion != r.PayloadVersion {
		diffs = append(diffs, "payload_version")
	}
	return diffs
}

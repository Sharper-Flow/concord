package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
)

// An effect receipt fences retries without retaining a payload or fold input.
type effectReceipt struct {
	event  Event
	digest string
}

func effectPayloadDigest(payload []byte) (string, error) {
	canonical, err := canonicalJSON(payload)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func readEffectReceipt(ctx context.Context, q queryer, eventID string) (effectReceipt, bool, error) {
	var receipt effectReceipt
	receipt.event.EventID = eventID
	err := q.QueryRowContext(ctx, `SELECT effect_seq,kind,subject_type,subject_id,actor,payload_version,payload_digest FROM effect_receipts WHERE event_id=?`, eventID).
		Scan(&receipt.event.Seq, &receipt.event.Kind, &receipt.event.SubjectType, &receipt.event.SubjectID, &receipt.event.Actor, &receipt.event.PayloadVersion, &receipt.digest)
	if err == sql.ErrNoRows {
		return effectReceipt{}, false, nil
	}
	return receipt, err == nil, err
}

func (r effectReceipt) identityDifferences(attempted Event) []string {
	diffs := eventHeaderDifferences(attempted, r.event)
	digest, err := effectPayloadDigest(attempted.Payload)
	if err != nil || digest != r.digest {
		diffs = append(diffs, "payload")
	}
	return diffs
}

// Backfill is part of the schema transaction. Close the cursor before writes
// so no live read handle competes with migration updates on the one connection.
func backfillEffectReceiptDigests(ctx context.Context, tx migrationRunner) error {
	rows, err := tx.QueryContext(ctx, `SELECT r.event_id,e.payload,r.payload_digest FROM effect_receipts r JOIN domain_events e ON e.event_id=r.event_id ORDER BY r.effect_seq`)
	if err != nil {
		return err
	}
	type digestRow struct{ id, digest string }
	var updates []digestRow
	for rows.Next() {
		var id string
		var payload []byte
		var stored sql.NullString
		if err := rows.Scan(&id, &payload, &stored); err != nil {
			_ = rows.Close()
			return err
		}
		digest, err := effectPayloadDigest(payload)
		if err != nil {
			_ = rows.Close()
			return wrapFailure(KindInvalidPayload, "migrate", "cannot digest effect receipt "+id, false, "repair the stored payload before retrying the upgrade", err)
		}
		if stored.Valid {
			if stored.String != digest {
				_ = rows.Close()
				return newFailure(KindInvariantViolation, "migrate", "effect receipt digest differs from its source payload", false, "restore a verified snapshot before retrying the upgrade")
			}
			continue
		}
		updates = append(updates, digestRow{id, digest})
	}
	iterationErr := rows.Err()
	closeErr := rows.Close()
	if iterationErr != nil {
		return iterationErr
	}
	if closeErr != nil {
		return closeErr
	}
	for _, row := range updates {
		if _, err := tx.ExecContext(ctx, `UPDATE effect_receipts SET payload_digest=? WHERE event_id=?`, row.digest, row.id); err != nil {
			return err
		}
	}
	var missing bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM effect_receipts WHERE payload_digest IS NULL OR payload_digest='')`).Scan(&missing); err != nil {
		return err
	}
	if missing {
		return newFailure(KindInvariantViolation, "migrate", "effect receipt backfill left a missing payload digest", false, "repair the stored effect identities before retrying the upgrade")
	}
	return nil
}

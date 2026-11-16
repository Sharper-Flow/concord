package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"

	"github.com/sharper-flow/concord/internal/store/internal/sqlitedb"
)

// An effect receipt fences retries without retaining a payload or fold input.
type effectReceipt struct {
	sqlitedb.EffectReceipt
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
	receipt, found, err := sqlitedb.ReadReceipt(ctx, q, eventID)
	return effectReceipt{receipt}, found, err
}

func effectReceiptHeader(event Event) sqlitedb.EffectReceipt {
	return sqlitedb.EffectReceipt{
		EventID: event.EventID, Seq: event.Seq, Kind: event.Kind, SubjectType: string(event.SubjectType),
		SubjectID: event.SubjectID, Actor: event.Actor, PayloadVersion: event.PayloadVersion,
	}
}

func (r effectReceipt) identityDifferences(attempted Event) []string {
	diffs := r.HeaderDifferences(effectReceiptHeader(attempted))
	digest, err := effectPayloadDigest(attempted.Payload)
	if err != nil || digest != r.PayloadDigest {
		diffs = append(diffs, "payload")
	}
	return diffs
}

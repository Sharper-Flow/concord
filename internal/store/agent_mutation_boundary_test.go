package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestMutationIdempotencyBoundaryRoundTrip(t *testing.T) {
	s := openTemp(t)
	defer s.Close()
	ctx := context.Background()
	key := MutationIdempotencyKey{PrincipalRef: "principal-1", Tool: "concord_work_define", OperationKind: "work_define", IdempotencyKey: "mutation-key"}
	now := time.Unix(10, 0).UTC()
	if err := s.InsertMutationIdempotency(ctx, MutationIdempotencyInsert{
		Key: key, CanonicalDigest: "sha256:digest", OperationID: "mutation-op", ResultEventIDs: "[]",
		ResultPayload: `{"ok":true}`, ChangedRefs: `[]`, AuthorizedScopeSnapshot: `{}`, ObservedAt: now,
	}); err != nil {
		t.Fatalf("insert idempotency record: %v", err)
	}
	record, found, err := s.LookupMutationIdempotency(ctx, key)
	if err != nil || !found {
		t.Fatalf("lookup idempotency record = (%+v, %t, %v)", record, found, err)
	}
	if record.CanonicalDigest != "sha256:digest" || record.OperationID != "mutation-op" {
		t.Fatalf("lookup idempotency record = %+v", record)
	}
	if err := s.TouchMutationIdempotency(ctx, key, now.Add(time.Second)); err != nil {
		t.Fatalf("touch idempotency record: %v", err)
	}
	if err := s.UpdateMutationResult(ctx, MutationResultUpdate{Key: key, ResultEventIDs: `["event-1"]`, ResultPayload: `{"done":true}`, ChangedRefs: `[{"id":"work-1"}]`, ObservedAt: now.Add(2 * time.Second)}); err != nil {
		t.Fatalf("update mutation result: %v", err)
	}
	record, found, err = s.LookupMutationIdempotency(ctx, key)
	if err != nil || !found || record.ResultPayload != `{"done":true}` || record.ChangedRefs != `[{"id":"work-1"}]` {
		t.Fatalf("updated idempotency record = (%+v, %t, %v)", record, found, err)
	}
	var replayed int
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT replayed_count FROM idempotency_records WHERE principal_ref=? AND tool=? AND operation_kind=? AND idempotency_key=?`, key.PrincipalRef, key.Tool, key.OperationKind, key.IdempotencyKey).Scan(&replayed); err != nil {
		t.Fatalf("read replay count: %v", err)
	}
	if replayed != 1 {
		t.Fatalf("replayed count = %d, want 1", replayed)
	}
}

func TestTransactRollsBackCallbackFailure(t *testing.T) {
	s := openTemp(t)
	defer s.Close()
	errSentinel := errors.New("abort")
	err := s.Transact(context.Background(), func(tx *Transaction) error {
		raw, err := transactionSQL(tx, "test_rollback")
		if err != nil {
			return err
		}
		_, err = raw.Exec(`INSERT INTO domain_events(event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload) VALUES(?,?,?,?,?,?,?,?)`, "rollback-event", "test.event", "product", "product-1", "test", time.Now().UTC().Format(time.RFC3339Nano), 1, `{}`)
		if err != nil {
			return err
		}
		return errSentinel
	})
	if !errors.Is(err, errSentinel) {
		t.Fatalf("Transact error = %v, want sentinel", err)
	}
	var count int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE event_id=?`, "rollback-event").Scan(&count); err != nil {
		t.Fatalf("read rolled back event: %v", err)
	}
	if count != 0 {
		t.Fatalf("rolled back event count = %d, want 0", count)
	}
}

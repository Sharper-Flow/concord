package store

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"
)

func TestMutationIdempotencyBoundaryRoundTrip(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	defer s.Close()
	ctx := context.Background()
	key := MutationIdempotencyKey{PrincipalRef: "principal-1", Tool: "concord_work_define", OperationKind: "work_define", IdempotencyKey: "mutation-key"}
	now := time.Unix(10, 0).UTC()
	if err := s.Transact(ctx, func(tx *Transaction) error {
		return InsertMutationIdempotencyTx(ctx, tx, MutationIdempotencyInsert{
			Key: key, CanonicalDigest: "sha256:digest", OperationID: "mutation-op", ResultEventIDs: "[]",
			ResultPayload: `{"ok":true}`, ChangedRefs: `[]`, AuthorizedScopeSnapshot: `{}`, ObservedAt: now,
		})
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
	if err := s.Transact(ctx, func(tx *Transaction) error {
		return TouchMutationIdempotencyTx(ctx, tx, key, now.Add(time.Second))
	}); err != nil {
		t.Fatalf("touch idempotency record: %v", err)
	}
	record, found, err = s.LookupMutationIdempotency(ctx, key)
	if err != nil || !found || record.ResultPayload != `{"ok":true}` || record.ChangedRefs != `[]` {
		t.Fatalf("touched idempotency record = (%+v, %t, %v)", record, found, err)
	}
	var events, observed string
	if err := s.DatabaseForTesting().QueryRow(`SELECT result_event_ids,last_observed_at FROM idempotency_records WHERE idempotency_key='mutation-key'`).Scan(&events, &observed); err != nil {
		t.Fatal(err)
	}
	if events != `[]` || observed != now.Add(time.Second).Format(time.RFC3339Nano) {
		t.Fatalf("touched record=(%s, %s)", events, observed)
	}
	var replayed int
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT replayed_count FROM idempotency_records WHERE principal_ref=? AND tool=? AND operation_kind=? AND idempotency_key=?`, key.PrincipalRef, key.Tool, key.OperationKind, key.IdempotencyKey).Scan(&replayed); err != nil {
		t.Fatalf("read replay count: %v", err)
	}
	if replayed != 1 {
		t.Fatalf("replayed count = %d, want 1", replayed)
	}
}

func TestReplayNamespacesRemainPartitioned(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	defer s.Close()
	ctx := context.Background()
	identities := []MutationIdempotencyKey{
		{PrincipalRef: "principal-1", Tool: "concord_work_transition", OperationKind: "workflow_action", IdempotencyKey: "shared-key"},
		{PrincipalRef: "principal-2", Tool: "concord_work_transition", OperationKind: "workflow_action", IdempotencyKey: "shared-key"},
		{PrincipalRef: "principal-1", Tool: "concord_work_transition", OperationKind: "lifecycle", IdempotencyKey: "shared-key"},
		{PrincipalRef: "principal-1", Tool: "concord_work_define", OperationKind: "capture", IdempotencyKey: "shared-key"},
		{PrincipalRef: "principal-1", Tool: "concord_work_define", OperationKind: "research_retire", IdempotencyKey: "shared-key"},
		{PrincipalRef: "principal-1", Tool: "concord_work_relate", OperationKind: "resolve_overlap", IdempotencyKey: "shared-key"},
		{PrincipalRef: "principal-1", Tool: "concord_domain", OperationKind: "observation_record", IdempotencyKey: "shared-key"},
		{PrincipalRef: "principal-1", Tool: "concord_work_compact", OperationKind: "claim", IdempotencyKey: "shared-key"},
		{PrincipalRef: "principal-1", Tool: "concord_work_compact", OperationKind: "lesson_publish", IdempotencyKey: "shared-key"},
		{PrincipalRef: "principal-1", Tool: "concord_work_compact", OperationKind: "reconcile", IdempotencyKey: "shared-key"},
		{PrincipalRef: "principal-1", Tool: "direct-research", OperationKind: "create", IdempotencyKey: "shared-key"},
		{PrincipalRef: "principal-1", Tool: "concord_work_transition", OperationKind: "session_vacate", IdempotencyKey: "shared-key"},
		{PrincipalRef: "principal-1", Tool: "concord_work_transition", OperationKind: "project_handoff_consume", IdempotencyKey: "shared-key"},
		{PrincipalRef: "principal-1", Tool: "concord_work_transition", OperationKind: "worktree_verify", IdempotencyKey: "shared-key"},
	}
	records := make([]MutationIdempotencyInsert, len(identities))
	for i, key := range identities {
		id := "namespace-" + strconv.Itoa(i)
		events := `["` + id + `"]`
		if key.Tool == "direct-research" {
			events = `{"pack_id":"pack-namespace","revision":1}`
		}
		records[i] = MutationIdempotencyInsert{Key: key, CanonicalDigest: testDigest(id), OperationID: id, ResultEventIDs: events, ResultPayload: `{"owner":"` + id + `"}`, ChangedRefs: "[]", AuthorizedScopeSnapshot: `{"product_id":"product-namespace"}`, ObservedAt: time.Unix(10, 0).UTC()}
		if err := s.Transact(ctx, func(tx *Transaction) error {
			return InsertMutationIdempotencyTx(ctx, tx, records[i])
		}); err != nil {
			t.Fatal(err)
		}
	}
	before := make([]string, len(identities))
	for i, key := range identities {
		before[i] = replayNamespaceRow(t, s, key)
	}
	if err := s.Transact(ctx, func(tx *Transaction) error {
		return TouchMutationIdempotencyTx(ctx, tx, identities[0], time.Unix(11, 0).UTC())
	}); err != nil {
		t.Fatal(err)
	}
	for i, key := range identities {
		record, found, err := s.LookupMutationIdempotency(ctx, key)
		if err != nil || !found || record.CanonicalDigest != records[i].CanonicalDigest || record.OperationID != records[i].OperationID || record.ResultEventIDs != records[i].ResultEventIDs || record.ResultPayload != records[i].ResultPayload || record.ChangedRefs != records[i].ChangedRefs || record.AuthorizedScopeSnapshot != records[i].AuthorizedScopeSnapshot {
			t.Fatalf("namespace %d lookup=%+v found=%t err=%v", i, record, found, err)
		}
		if i != 0 && replayNamespaceRow(t, s, key) != before[i] {
			t.Fatalf("workflow replay touch changed another namespace: %+v", key)
		}
	}
	if replayNamespaceRow(t, s, identities[0]) == before[0] {
		t.Fatal("workflow replay touch did not reach its exact namespace")
	}
	if got := countRows(t, s, "idempotency_records"); got != len(identities) {
		t.Fatalf("partitioned row count=%d, want %d", got, len(identities))
	}
}

func replayNamespaceRow(t *testing.T, s *Store, key MutationIdempotencyKey) string {
	t.Helper()
	var row string
	if err := s.DatabaseForTesting().QueryRow(`SELECT json_array(principal_ref,tool,operation_kind,idempotency_key,canonical_digest,op_id,result_event_ids,result_payload,changed_refs,authorized_scope_snapshot,first_observed_at,last_observed_at,replayed_count) FROM idempotency_records WHERE principal_ref=? AND tool=? AND operation_kind=? AND idempotency_key=?`, key.PrincipalRef, key.Tool, key.OperationKind, key.IdempotencyKey).Scan(&row); err != nil {
		t.Fatal(err)
	}
	return row
}

func TestTransactRollsBackCallbackFailure(t *testing.T) {
	t.Parallel()
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

package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestIdempotentResponseCompletionIsAtomicAndIdentityBound(t *testing.T) {
	s := openTemp(t)
	defer s.Close()
	ctx := context.Background()
	key := MutationIdempotencyKey{PrincipalRef: "principal-1", Tool: "concord_work_transition", OperationKind: "workflow_action", IdempotencyKey: "response"}
	reservation := MutationIdempotencyInsert{Key: key, CanonicalDigest: "sha256:original", OperationID: "original-operation", ResultEventIDs: "[]", ResultPayload: "{}", ChangedRefs: "[]", AuthorizedScopeSnapshot: `{"project_ids":["project-1"]}`, ObservedAt: time.Unix(10, 0)}
	if err := s.Transact(ctx, func(tx *Transaction) error { return InsertMutationIdempotencyTx(ctx, tx, reservation) }); err != nil {
		t.Fatal(err)
	}
	completed := reservation
	completed.ResultPayload = `{"subject":"original-commit"}`
	completed.ChangedRefs = `[{"entity_kind":"work_item","id":"work-1","version":"3"}]`
	completed.AuthorizedScopeSnapshot = `{"project_ids":["project-1"],"mutation_response":{"subject":"original-commit"}}`
	for _, alter := range []func(*MutationIdempotencyInsert){
		func(in *MutationIdempotencyInsert) { in.Key.PrincipalRef = "foreign" },
		func(in *MutationIdempotencyInsert) { in.Key.OperationKind = "other" },
		func(in *MutationIdempotencyInsert) { in.CanonicalDigest = "sha256:changed" },
		func(in *MutationIdempotencyInsert) { in.OperationID = "foreign-operation" },
		func(in *MutationIdempotencyInsert) { in.ResultPayload = strings.Repeat("x", (1<<20)+1) },
	} {
		wrong := completed
		alter(&wrong)
		if err := s.Transact(ctx, func(tx *Transaction) error { return CompleteMutationResponseTx(ctx, tx, wrong) }); err == nil {
			t.Fatal("changed response identity or over-limit payload was accepted")
		}
	}
	stop := errors.New("rollback after response capture")
	err := s.Transact(ctx, func(tx *Transaction) error {
		if err := CompleteMutationResponseTx(ctx, tx, completed); err != nil {
			return err
		}
		return stop
	})
	if !errors.Is(err, stop) {
		t.Fatal(err)
	}
	prior, found, err := s.LookupMutationIdempotency(ctx, key)
	if err != nil || !found || prior.ResultPayload != "{}" {
		t.Fatalf("rolled back response persisted: %+v found=%t err=%v", prior, found, err)
	}
	if err := s.Transact(ctx, func(tx *Transaction) error { return CompleteMutationResponseTx(ctx, tx, completed) }); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().Exec(`DROP TABLE domain_events`); err != nil {
		t.Fatal(err)
	}
	prior, found, err = s.LookupMutationIdempotency(ctx, key)
	if err != nil || !found || prior.ResultPayload != completed.ResultPayload || prior.ChangedRefs != completed.ChangedRefs || prior.AuthorizedScopeSnapshot != completed.AuthorizedScopeSnapshot || prior.CanonicalDigest != reservation.CanonicalDigest || prior.OperationID != reservation.OperationID {
		t.Fatalf("completed response lost identity without history: %+v found=%t err=%v", prior, found, err)
	}
	if err := s.Transact(ctx, func(tx *Transaction) error { return CompleteMutationResponseTx(ctx, tx, completed) }); err == nil {
		t.Fatal("completed response was overwritten")
	}
}

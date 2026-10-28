package agent

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/store"
)

// A receipt-shaped mutation whose effect reports no auxiliary changed
// references (worker_reconcile, worker_abandon) commits an explicit empty
// list. Its retained response replays once per retry without re-running the
// effect, through both the early and the transactional replay owner.
func TestRetainedReplayKeepsCommittedEmptyChangedRefs(t *testing.T) {
	ctx := context.Background()
	s, service, grant, _ := tiersRepoFixture(t)
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	r := runtime{Store: s, Authority: service, Envelope: mutationEnvelope(grant, scopeVersion), Tool: "concord_work_transition", Operation: "worker_reconcile"}
	op, _ := agentcontract.ValidateContractOperation(r.Tool, r.Operation)
	raw, _ := json.Marshal(map[string]any{"work_id": "work-1", "attempt_id": "attempt-retained", "task_part_id": "part-retained", "idempotency_key": "retained-reconcile"})
	digest := mutationDigest(r.Tool, r.Operation, r.Envelope, raw)
	scope := map[string]any{"product_id": "product-1", "project_ids": []string{"project-1"}, "work_ids": []string{"work-1"}}
	effects := 0
	execute := func(request string) agentcontract.Envelope {
		response, err := r.executeMutation(ctx, agentcontract.NewBase(request, r.Tool, r.Operation), raw, digest, scope, nil, "", "", false, nil, nil, nil, func(context.Context, *store.Transaction, Authority) (json.RawMessage, []string, []agentcontract.ChangedRef, error) {
			effects++
			return json.RawMessage(`{"changed_refs":[],"next_valid_intents":[]}`), nil, nil, nil
		}, nil, nil)
		if err != nil {
			t.Fatalf("%s: %v", request, err)
		}
		return response
	}
	first := execute("first")
	if first.Outcome != agentcontract.OutcomeOK || first.Replayed || effects != 1 {
		t.Fatalf("first outcome=%s replayed=%t effects=%d error=%+v", first.Outcome, first.Replayed, effects, first.Error)
	}
	key := store.MutationIdempotencyKey{PrincipalRef: grant.PrincipalRef, Tool: r.Tool, OperationKind: r.Operation, IdempotencyKey: "retained-reconcile"}
	prior, found, err := s.LookupMutationIdempotency(ctx, key)
	if err != nil || !found || prior.ChangedRefs != "[]" || prior.ResultPayload != string(first.Result) {
		t.Fatalf("committed response not retained as reported: found=%t err=%v record=%+v", found, err, prior)
	}
	same := func(label string, replay agentcontract.Envelope) {
		t.Helper()
		if replay.Outcome != agentcontract.OutcomeOK || !replay.Replayed {
			t.Fatalf("%s outcome=%s replayed=%t error=%+v", label, replay.Outcome, replay.Replayed, replay.Error)
		}
		if string(replay.Result) != string(first.Result) || !reflect.DeepEqual(replay.ChangedRefs, first.ChangedRefs) || !reflect.DeepEqual(replay.NextValidIntents, first.NextValidIntents) || !reflect.DeepEqual(replay.ResolvedScope, first.ResolvedScope) {
			t.Fatalf("%s changed the committed response\nfirst=%+v\nreplay=%+v", label, first, replay)
		}
	}
	for attempt := 0; attempt < 2; attempt++ {
		early, handled, err := r.replayMutationBeforeScope(ctx, agentcontract.NewBase("early", r.Tool, r.Operation), raw, grant, op)
		if err != nil || !handled {
			t.Fatalf("early replay handled=%t err=%v", handled, err)
		}
		same("early replay", early)
		same("transactional replay", execute("transactional"))
	}
	if effects != 1 {
		t.Fatalf("retained replay repeated the effect: effects=%d", effects)
	}
}

// Only a captured empty list is a truthful empty result. A null or absent
// list is a record the producer never completed; it stays an explicit
// possible-effect reconciliation, never a successful empty placeholder.
func TestRetainedReplayRefusesUncapturedChangedRefs(t *testing.T) {
	r := runtime{Tool: "concord_work_transition", Operation: "worker_reconcile"}
	base := agentcontract.NewBase("uncaptured", r.Tool, r.Operation)
	complete := agentcontract.NewOKMutation(base, mutationPayload(nil, nil), nil, nil)
	scopeJSON, _ := json.Marshal(mutationResponseScope(nil, complete, nil, false))
	captured := r.recordedMutationResult(base, store.MutationIdempotencyRecord{ResultPayload: string(mutationPayload(nil, nil)), ChangedRefs: `[]`, AuthorizedScopeSnapshot: string(scopeJSON)})
	if captured.Outcome != agentcontract.OutcomeOK || !captured.Replayed || captured.ChangedRefs == nil || len(*captured.ChangedRefs) != 0 {
		t.Fatalf("captured empty list did not replay: %+v", captured)
	}
	for _, changed := range []string{`null`, ``, `{}`} {
		response := r.recordedMutationResult(base, store.MutationIdempotencyRecord{ResultPayload: string(mutationPayload(nil, nil)), ChangedRefs: changed, AuthorizedScopeSnapshot: string(scopeJSON)})
		if response.Outcome != agentcontract.OutcomeError || response.Error == nil || response.Error.Kind != "operation_conflict" || response.Error.EffectState != agentcontract.EffectPossible || response.Error.RecoveryAction.Kind != "reconcile_operation" || response.Error.RetrySafe {
			t.Fatalf("changed_refs=%q became success or a no-effect retry: %+v", changed, response)
		}
	}
}

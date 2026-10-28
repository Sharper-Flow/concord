package agent

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/store"
)

func TestRetainedReconcileInvokeAcrossManifestChange(t *testing.T) {
	ctx := t.Context()
	s, service, grant, _ := mutationDispatchFixture(t, []agentcontract.Capability{"work_transition"})
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	tool, operation := "concord_work_transition", "worker_reconcile"
	raw := retryJSON(map[string]any{"work_id": "work-1", "attempt_id": "attempt-retained", "task_part_id": "part-retained", "idempotency_key": "retained-reconcile"})
	base := agentcontract.NewBase("original", tool, operation)
	packetDigest := "sha256:" + strings.Repeat("a", 64)
	payload := retryJSON(map[string]any{
		"changed_refs": []any{}, "next_valid_intents": []any{},
		"worker_recovery": map[string]any{
			"packet_digest": packetDigest, "worker_worktree": "/synthetic/worker", "coordinator_session": "original-session",
			"attempt_epoch": 9, "dispatch_event_id": "original-dispatch", "lifecycle_state": "dispatched",
			"packet_schema_version": "2.0", "report_schema_version": "2.0",
			"dispatch": map[string]any{
				"attempt_id": "attempt-retained", "lane_id": "concord-review", "lane_digest": packetDigest, "capability_class": "review",
				"packet_schema_version": "2.0", "report_schema_version": "2.0", "packet_digest": packetDigest, "readback_model": "synthetic/model",
				"host_provenance": map[string]any{"digest": packetDigest, "sources": []any{map[string]any{"kind": "agent_definition", "path": "synthetic-agent.md", "sha256": packetDigest}}},
			},
		},
	})
	first := agentcontract.NewOKMutation(base, payload, nil, nil)
	first.ResolvedScope = &agentcontract.Scope{ProductID: "product-1", ProjectIDs: []string{"project-1"}}
	scope := mutationResponseScope(map[string]any{"product_id": "product-1", "project_ids": []string{"project-1"}, "work_ids": []string{"work-1"}}, first, nil, false)
	snapshot := scope["mutation_response"].(mutationResponseSnapshot)
	snapshot.ManifestDigest = "sha256:" + strings.Repeat("0", 64)
	scope["mutation_response"] = snapshot
	digest := mutationDigest(tool, operation, env, raw)
	key := store.MutationIdempotencyKey{PrincipalRef: grant.PrincipalRef, Tool: tool, OperationKind: operation, IdempotencyKey: "retained-reconcile"}
	if err := s.Transact(ctx, func(tx *store.Transaction) error {
		return store.InsertMutationIdempotencyTx(ctx, tx, store.MutationIdempotencyInsert{Key: key, CanonicalDigest: digest, OperationID: "mutation-" + digest[7:31], ResultPayload: string(first.Result), ChangedRefs: "[]", AuthorizedScopeSnapshot: string(retryJSON(scope)), ResultEventIDs: "[]", ObservedAt: fixedTime()})
	}); err != nil {
		t.Fatal(err)
	}
	before := mutationProjectionState(t, s)
	for attempt := 0; attempt < 2; attempt++ {
		response, err := Invoke(ctx, s, service, mustMarshalInvoke(t, InvokeRequest{Tool: tool, Operation: operation, Input: raw}, env))
		if err != nil || response.Outcome != agentcontract.OutcomeOK || !response.Replayed {
			t.Fatalf("retained current-operation response refused: err=%v response=%+v error=%+v", err, response, response.Error)
		}
		if string(response.Result) != string(first.Result) || !reflect.DeepEqual(response.ChangedRefs, first.ChangedRefs) || !reflect.DeepEqual(response.NextValidIntents, first.NextValidIntents) || !reflect.DeepEqual(response.ResolvedScope, first.ResolvedScope) || !reflect.DeepEqual(response.Omissions, first.Omissions) {
			t.Fatalf("replay replaced original response: first=%+v replay=%+v", first, response)
		}
		if response.ManifestDigest != agentcontract.ManifestDigest {
			t.Fatal("replay envelope did not bind the current invocation manifest")
		}
	}
	after := mutationProjectionState(t, s)
	delete(before, "idempotency_observations")
	delete(after, "idempotency_observations")
	if !reflect.DeepEqual(before, after) {
		t.Fatal("replay repeated a business effect")
	}
	for _, vector := range []struct {
		name    string
		payload string
		changed string
		scope   map[string]any
	}{
		{"missing metadata", string(first.Result), "[]", map[string]any{"product_id": "product-1", "project_ids": []string{"project-1"}}},
		{"incompatible result", `{"unexpected":"field"}`, "[]", scope},
		{"uncaptured references", string(first.Result), "null", scope},
	} {
		t.Run(vector.name, func(t *testing.T) {
			invalidKey := key
			invalidKey.IdempotencyKey = "invalid-" + strings.ReplaceAll(vector.name, " ", "-")
			if err := s.Transact(ctx, func(tx *store.Transaction) error {
				return store.InsertMutationIdempotencyTx(ctx, tx, store.MutationIdempotencyInsert{Key: invalidKey, CanonicalDigest: digest, OperationID: "mutation-" + invalidKey.IdempotencyKey, ResultPayload: vector.payload, ChangedRefs: vector.changed, AuthorizedScopeSnapshot: string(retryJSON(vector.scope)), ResultEventIDs: "[]", ObservedAt: fixedTime()})
			}); err != nil {
				t.Fatal(err)
			}
			invalid := retryJSON(map[string]any{"work_id": "work-1", "attempt_id": "attempt-retained", "task_part_id": "part-retained", "idempotency_key": invalidKey.IdempotencyKey})
			before := mutationProjectionState(t, s)
			response, err := Invoke(ctx, s, service, mustMarshalInvoke(t, InvokeRequest{Tool: tool, Operation: operation, Input: invalid}, env))
			if err != nil || response.Error == nil || response.Error.Kind != "malformed_response" || response.Error.EffectState != agentcontract.EffectPossible || response.Error.RecoveryAction.Kind != "contact_operator" || response.Error.RetrySafe || !response.Replayed {
				t.Fatalf("unrecoverable response lost effect truth or named an unreachable retry: err=%v response=%+v error=%+v", err, response, response.Error)
			}
			if !reflect.DeepEqual(before, mutationProjectionState(t, s)) {
				t.Fatal("refused recovery changed state")
			}
		})
	}
	changedInput := retryJSON(map[string]any{"work_id": "work-1", "attempt_id": "different-attempt", "task_part_id": "part-retained", "idempotency_key": key.IdempotencyKey})
	conflict, err := Invoke(ctx, s, service, mustMarshalInvoke(t, InvokeRequest{Tool: tool, Operation: operation, Input: changedInput}, env))
	if err != nil || conflict.Error == nil || conflict.Error.Kind != "idempotency_conflict" {
		t.Fatalf("changed intent received retained response: err=%v error=%+v", err, conflict.Error)
	}
	env.ManifestDigest = snapshot.ManifestDigest
	mismatched, err := Invoke(ctx, s, service, mustMarshalInvoke(t, InvokeRequest{Tool: tool, Operation: operation, Input: raw}, env))
	if err != nil || mismatched.Error == nil || mismatched.Error.EffectState != agentcontract.EffectNone || mismatched.Replayed {
		t.Fatalf("mismatched invocation reached replay: err=%v error=%+v", err, mismatched.Error)
	}
}

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
	if _, err := s.DatabaseForTesting().Exec(`UPDATE idempotency_records SET authorized_scope_snapshot=? WHERE idempotency_key=?`, strings.ReplaceAll(prior.AuthorizedScopeSnapshot, agentcontract.ManifestDigest, "sha256:"+strings.Repeat("0", 64)), key.IdempotencyKey); err != nil {
		t.Fatal(err)
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
		if response.Outcome != agentcontract.OutcomeError || response.Error == nil || response.Error.Kind != "malformed_response" || response.Error.EffectState != agentcontract.EffectPossible || response.Error.RecoveryAction.Kind != "contact_operator" || response.Error.RetrySafe {
			t.Fatalf("changed_refs=%q became success or a no-effect retry: %+v", changed, response)
		}
	}
}

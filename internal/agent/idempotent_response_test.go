package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

func TestIdempotentResponseKeepsCommittedSubjectWithoutHistory(t *testing.T) {
	s, service, grant, root := tiersRepoFixture(t)
	initializeMutationPinWorkflow(t, s, "work-1", store.BuiltinWorkflowDefinitions()[0])
	_, before := readWorkFromStore(t, s, "work-1")
	input := map[string]any{"host_pid": os.Getpid(), "work_id": "work-1", "project_id": "project-1", "base_sha": gitRun(t, root, "rev-parse", "HEAD"), "expected_version": before, "idempotency_key": "response-claim"}
	first := tiersInvoke(t, s, service, grant, "concord_work_transition", "worktree_claim", input)
	if first.Outcome != OutcomeOK {
		t.Fatalf("first outcome=%s error=%+v", first.Outcome, first.Error)
	}
	_, committed := readWorkFromStore(t, s, "work-1")
	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	r := runtime{Store: s, Authority: service, Envelope: mutationEnvelope(grant, scopeVersion), Tool: "concord_work_transition", Operation: "worktree_claim"}
	op, _ := ValidateContractOperation(r.Tool, r.Operation)
	retry := func() Envelope {
		raw, _ := json.Marshal(input)
		response, handled, err := r.replayMutationBeforeScope(context.Background(), NewBase("retry", r.Tool, r.Operation), raw, grant, op)
		if err != nil || !handled {
			t.Fatalf("retry handled=%t error=%v", handled, err)
		}
		return response
	}
	driftWorkPinVersion(t, s, "work-1")
	if _, err := s.DatabaseForTesting().Exec(`DROP TABLE domain_events`); err != nil {
		t.Fatal(err)
	}
	replay := retry()
	if replay.Outcome != OutcomeOK || !replay.Replayed {
		t.Fatalf("replay outcome=%s replayed=%t error=%+v", replay.Outcome, replay.Replayed, replay.Error)
	}
	if string(replay.Result) != string(first.Result) || !reflect.DeepEqual(replay.ChangedRefs, first.ChangedRefs) || !reflect.DeepEqual(replay.NextValidIntents, first.NextValidIntents) {
		t.Fatalf("replay changed the committed result\nfirst=%s\nreplay=%s", first.Result, replay.Result)
	}
	requireMutationPinVersion(t, replay, "work-1", committed)
	_, after := readWorkFromStore(t, s, "work-1")
	if after != committed+1 {
		t.Fatalf("replay repeated an effect: version=%d want=%d", after, committed+1)
	}
	raw, _ := json.Marshal(input)
	narrow := grant
	narrow.ProjectScope = nil
	denied, handled, err := r.replayMutationBeforeScope(context.Background(), NewBase("scope", r.Tool, r.Operation), raw, narrow, op)
	if err != nil || !handled || denied.Error == nil || denied.Error.Kind != "unauthorized" {
		t.Fatalf("foreign scope replay=%+v handled=%t err=%v", denied.Error, handled, err)
	}
	foreign := grant
	foreign.PrincipalRef = "another-principal"
	_, handled, err = r.replayMutationBeforeScope(context.Background(), NewBase("actor", r.Tool, r.Operation), raw, foreign, op)
	if err != nil || handled {
		t.Fatalf("foreign principal received retained response: handled=%t err=%v", handled, err)
	}
	key := store.MutationIdempotencyKey{PrincipalRef: grant.PrincipalRef, Tool: r.Tool, OperationKind: r.Operation, IdempotencyKey: "response-claim"}
	prior, found, err := s.LookupMutationIdempotency(context.Background(), key)
	if err != nil || !found {
		t.Fatalf("retained record found=%t err=%v", found, err)
	}
	wrongSubject := prior
	wrongSubject.ResultPayload = strings.ReplaceAll(prior.ResultPayload, "work-1", "foreign-work")
	refused := r.recordedMutationResult(NewBase("subject", r.Tool, r.Operation), wrongSubject)
	if refused.Outcome != OutcomeError || refused.Error == nil || refused.Error.EffectState != EffectPossible {
		t.Fatalf("response changed the committed subject: %+v", refused)
	}
	err = s.Transact(context.Background(), func(tx *store.Transaction) error {
		response, rejected, err := r.replayCachedMutationTx(context.Background(), tx, NewBase("transaction", r.Tool, r.Operation), prior, grant, key)
		if err != nil || rejected || string(response.Result) != string(first.Result) {
			t.Fatalf("transaction replay rejected=%t err=%v response=%+v", rejected, err, response.Error)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	effects, finalizations := 0, 0
	response, err := r.executeMutation(context.Background(), NewBase("cached-effect", r.Tool, r.Operation), raw, mutationDigest(r.Tool, r.Operation, r.Envelope, raw), map[string]any{"product_id": "product-1", "project_ids": []string{"project-1"}, "work_ids": []string{"work-1"}}, nil, "", "", false, nil, nil, nil,
		func(context.Context, *store.Transaction, Authority) (json.RawMessage, []string, []ChangedRef, error) {
			effects++
			return nil, nil, nil, nil
		}, nil, func(context.Context) error { finalizations++; return nil })
	if err != nil || response.Outcome != OutcomeOK || !response.Replayed || effects != 0 || finalizations != 0 {
		t.Fatalf("cached replay repeated native effect: response=%+v err=%v effects=%d finalizations=%d", response.Error, err, effects, finalizations)
	}
	input["base_sha"] = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	conflict := retry()
	if conflict.Error == nil || conflict.Error.Kind != "idempotency_conflict" {
		t.Fatalf("changed input did not conflict: %+v", conflict.Error)
	}
}

func TestIdempotentResponseMissingFactsKeepPossibleEffects(t *testing.T) {
	r := runtime{Tool: "concord_work_relate", Operation: "resource_release"}
	base := NewBase("incomplete", r.Tool, r.Operation)
	complete := NewOKMutation(base, mutationPayload(nil, nil), nil, nil)
	scopeJSON, _ := json.Marshal(mutationResponseScope(nil, complete, nil, false))
	for _, prior := range []store.MutationIdempotencyRecord{
		{ResultPayload: `{}`, ChangedRefs: `[]`, AuthorizedScopeSnapshot: `{}`},
		{ResultPayload: `{}`, ChangedRefs: `not-json`, AuthorizedScopeSnapshot: string(scopeJSON)},
		{ResultPayload: `{}`, ChangedRefs: `[]`, AuthorizedScopeSnapshot: `{"mutation_response":{"manifest_digest":"` + ManifestDigest + `"}}`},
	} {
		response := r.recordedMutationResult(base, prior)
		if response.Outcome != OutcomeError || response.Error == nil || response.Error.EffectState != EffectPossible || response.Error.RecoveryAction.Kind != "reconcile_operation" || response.Error.RetrySafe {
			t.Fatalf("missing facts became success or a no-effect retry: %+v", response)
		}
	}
}

func TestIdempotentResponseCompactionCannotInventAnOmission(t *testing.T) {
	changed := []ChangedRef{{EntityKind: "work_item", ID: "work-1", Version: "3"}}
	r := runtime{Tool: "concord_work_compact", Operation: "publish"}
	_, _, _, err := r.replayMutationPayload(mutationPayload(changed, nil), changed)
	if err == nil {
		t.Fatal("a missing retained pin became a successful omission")
	}
	response := failureEnvelope(NewBase("compaction", r.Tool, r.Operation), err)
	if response.Error == nil || response.Error.Kind != "operation_conflict" || response.Error.EffectState != EffectPossible || response.Error.RecoveryAction.Kind != "reconcile_operation" || response.Error.RetrySafe {
		t.Fatalf("missing omission fact lost effect truth: %+v", response)
	}
}

func TestIdempotentResponseCompactionReplaysItsRetainedOmission(t *testing.T) {
	changed := []ChangedRef{{EntityKind: "work_item", ID: "work-1", Version: "3"}}
	r := runtime{Tool: "concord_work_compact", Operation: "publish"}
	wire := mutationPayload(changed, nil)
	replayed, _, pinless, err := r.replayMutationPayload(retainCompactionPinless(wire, []string{"work-1"}), changed)
	if err != nil || !reflect.DeepEqual(pinless, []string{"work-1"}) {
		t.Fatalf("retained omission did not replay: pinless=%v err=%v", pinless, err)
	}
	if !bytes.Equal(replayed, wire) {
		t.Fatalf("replay changed the wire payload or leaked the retained fact: %s", replayed)
	}
	other := []ChangedRef{{EntityKind: "work_item", ID: "work-2", Version: "3"}}
	if _, _, _, err := r.replayMutationPayload(retainCompactionPinless(mutationPayload(other, nil), []string{"work-1"}), other); err == nil {
		t.Fatal("another item's retained omission covered a missing pin")
	}
}

func TestIdempotentResponseWorkflowUnresolvedClassification(t *testing.T) {
	r := runtime{Tool: "concord_work_transition", Operation: "workflow_action", Authority: &Service{}}
	for _, kind := range []store.ResultKind{store.ResultPending, store.ResultPartial, store.ResultFailed, store.ResultFailedStale} {
		response, err := r.replayWorkflowAction(NewBase("unresolved", r.Tool, r.Operation), store.FenceResult{OpID: "retained-operation", AttemptEpoch: 1, StepID: "execute", ResultKind: kind}, store.MutationIdempotencyRecord{})
		if err != nil || response.Outcome == OutcomeOK || response.OperationRef == nil || response.OperationRef.ID != "retained-operation" {
			t.Fatalf("kind=%s response=%+v err=%v", kind, response, err)
		}
	}
}

func TestIdempotentResponseWorkflowCapturesTransactionResult(t *testing.T) {
	r, grant, raw, first := committedWorkflowReplay(t)
	ctx := context.Background()
	op, _ := ValidateContractOperation(r.Tool, r.Operation)
	key := store.MutationIdempotencyKey{PrincipalRef: grant.PrincipalRef, Tool: r.Tool, OperationKind: r.Operation, IdempotencyKey: "retained-replay"}
	prior, found, err := r.Store.LookupMutationIdempotency(ctx, key)
	if err != nil || !found || prior.ResultPayload != string(first.Result) {
		t.Fatalf("completed response not captured: record=%+v found=%t err=%v", prior, found, err)
	}
	driftWorkPinVersion(t, r.Store, "work-1")
	if _, err := r.Store.DatabaseForTesting().Exec(`DROP TABLE domain_events`); err != nil {
		t.Fatal(err)
	}
	replay, handled, err := r.replayMutationBeforeScope(ctx, NewBase("workflow-retry", r.Tool, r.Operation), raw, grant, op)
	if err != nil || !handled || replay.Outcome != OutcomeOK || !replay.Replayed || string(replay.Result) != string(first.Result) {
		t.Fatalf("workflow replay lost transaction result: handled=%t err=%v response=%+v", handled, err, replay)
	}
	err = r.Store.Transact(ctx, func(tx *store.Transaction) error {
		replay, err := r.replayWorkflowActionTx(ctx, tx, NewBase("workflow-tx-retry", r.Tool, r.Operation), prior, map[string]any{"product_id": "product-1"})
		if err != nil || replay.Outcome != OutcomeOK || string(replay.Result) != string(first.Result) {
			t.Fatalf("workflow transaction replay lost result: err=%v response=%+v", err, replay)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestIdempotentResponseRoutineClaimDoesNotReadHistory(t *testing.T) {
	r := runtime{Tool: "concord_work_relate", Operation: "resolve_overlap", Envelope: CallEnvelope{SelectedProductID: "product-1"}}
	raw := json.RawMessage(`{"resolution_kind":"compatible_with","approval":{"approval_ref":"not-consumed"}}`)
	prior := store.MutationIdempotencyRecord{ResultEventIDs: `["absent-event"]`, AuthorizedScopeSnapshot: `{"product_ids":["product-1"]}`}
	response, refused, err := r.replayRoutineClaimAdmission(NewBase("claim", r.Tool, r.Operation), raw, prior)
	if err != nil || !refused || response.Error == nil || response.Error.Kind != "operation_conflict" || response.Error.EffectState != EffectPossible {
		t.Fatalf("missing retained consent must be an explicit current-record dependency: refused=%t response=%+v err=%v", refused, response.Error, err)
	}
}

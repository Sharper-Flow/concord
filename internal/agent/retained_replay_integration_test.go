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

func TestLegacyWorkflowReplayUsesRetainedIndexResult(t *testing.T) {
	ctx := t.Context()
	r, grant, raw, first := committedWorkflowReplay(t)
	opID := "workflow-" + mutationDigest(r.Tool, r.Operation, r.Envelope, raw)[7:31]
	db := r.Store.DatabaseForTesting()
	owner, err := readOperationResult(ctx, r.Store, opID)
	if err != nil {
		t.Fatal(err)
	}
	var ownerPayload struct {
		Pins []store.WorkPin `json:"work_pins"`
	}
	if err := json.Unmarshal([]byte(owner.ResultPayload), &ownerPayload); err != nil || len(ownerPayload.Pins) != 0 {
		t.Fatalf("durable owner must lack original pin: owner=%+v err=%v", owner, err)
	}
	result, err := db.Exec(`UPDATE idempotency_records SET authorized_scope_snapshot=json_remove(authorized_scope_snapshot,'$.mutation_response') WHERE op_id=?`, opID)
	if err != nil {
		t.Fatal(err)
	}
	if count, err := result.RowsAffected(); err != nil || count != 1 {
		t.Fatalf("legacy fixture rows=%d err=%v", count, err)
	}
	key := store.MutationIdempotencyKey{PrincipalRef: grant.PrincipalRef, Tool: r.Tool, OperationKind: r.Operation, IdempotencyKey: "retained-replay"}
	prior, found, err := r.Store.LookupMutationIdempotency(ctx, key)
	if err != nil || !found || prior.ResultPayload != string(first.Result) || prior.ChangedRefs == "[]" {
		t.Fatalf("index missing committed result: found=%t prior=%+v err=%v", found, prior, err)
	}
	driftWorkPinVersion(t, r.Store, "work-1")
	before, index := workflowReplayEffectCounts(t, r.Store), mutationProjectionState(t, r.Store)["idempotency"]
	public, err := Dispatch(ctx, r.Store, r.Authority, InvokeRequest{Tool: r.Tool, Operation: r.Operation, Input: raw}, r.Envelope)
	if err != nil {
		t.Fatal(err)
	}
	transaction := workflowTransactionReplay(t, r, grant, raw)
	for name, replay := range map[string]agentcontract.Envelope{"public": public, "transaction": transaction} {
		if replay.Outcome != agentcontract.OutcomeOK || !replay.Replayed || string(replay.Result) != string(first.Result) || !reflect.DeepEqual(replay.ChangedRefs, first.ChangedRefs) || !reflect.DeepEqual(replay.NextValidIntents, first.NextValidIntents) {
			t.Errorf("%s did not replay original index result: response=%+v error=%+v", name, replay, replay.Error)
		}
	}
	if workflowReplayEffectCounts(t, r.Store) != before || mutationProjectionState(t, r.Store)["idempotency"] != index {
		t.Error("replay changed effects or immutable index content")
	}
	after, err := readOperationResult(ctx, r.Store, opID)
	if err != nil || !reflect.DeepEqual(owner, after) {
		t.Errorf("replay changed durable owner: before=%+v after=%+v err=%v", owner, after, err)
	}
}

func TestLegacyWorkflowReplayRefusesCorruptIndex(t *testing.T) {
	for _, vector := range []struct {
		name, payload, refs         string
		invalidOwner, unpinnedIndex bool
	}{
		{name: "invalid document", payload: `{"unexpected":true}`},
		{name: "null document", payload: `null`},
		{name: "invalid references", refs: `null`},
		{name: "mismatched references", refs: `[]`},
		{name: "invalid durable owner", invalidOwner: true},
		{name: "unpinned index with pinned owner", unpinnedIndex: true},
	} {
		t.Run(vector.name, func(t *testing.T) {
			ctx := t.Context()
			r, grant, raw, first := committedWorkflowReplay(t)
			opID := "workflow-" + mutationDigest(r.Tool, r.Operation, r.Envelope, raw)[7:31]
			db := r.Store.DatabaseForTesting()
			originalOwner, err := readOperationResult(ctx, r.Store, opID)
			if err != nil {
				t.Fatal(err)
			}
			ownerPayload := string(first.Result)
			if vector.invalidOwner {
				ownerPayload = `{}`
			}
			if _, err := db.Exec(`UPDATE durable_operations SET result_payload=? WHERE op_id=?`, ownerPayload, opID); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`UPDATE idempotency_records SET authorized_scope_snapshot=json_remove(authorized_scope_snapshot,'$.mutation_response') WHERE op_id=?`, opID); err != nil {
				t.Fatal(err)
			}
			if vector.unpinnedIndex {
				if _, err := db.Exec(`UPDATE idempotency_records SET result_payload=? WHERE op_id=?`, originalOwner.ResultPayload, opID); err != nil {
					t.Fatal(err)
				}
			}
			if vector.payload != "" {
				if _, err := db.Exec(`UPDATE idempotency_records SET result_payload=? WHERE op_id=?`, vector.payload, opID); err != nil {
					t.Fatal(err)
				}
			}
			if vector.refs != "" {
				if _, err := db.Exec(`UPDATE idempotency_records SET changed_refs=? WHERE op_id=?`, vector.refs, opID); err != nil {
					t.Fatal(err)
				}
			}
			before, index := workflowReplayEffectCounts(t, r.Store), mutationProjectionState(t, r.Store)["idempotency"]
			public, err := Dispatch(ctx, r.Store, r.Authority, InvokeRequest{Tool: r.Tool, Operation: r.Operation, Input: raw}, r.Envelope)
			if err != nil {
				t.Fatal(err)
			}
			transaction := workflowTransactionReplay(t, r, grant, raw)
			for _, replay := range []agentcontract.Envelope{public, transaction} {
				if replay.Outcome != agentcontract.OutcomeError || replay.Error == nil || replay.Error.Kind != "malformed_response" || replay.Error.EffectState != agentcontract.EffectPossible || replay.Error.RetrySafe {
					t.Errorf("corrupt index became success: response=%+v error=%+v", replay, replay.Error)
				}
				if vector.unpinnedIndex && replay.Error != nil && strings.Contains(replay.Error.Message, "original WorkPin was not retained") {
					t.Errorf("refusal ignored the pinned owner: %+v", replay.Error)
				}
			}
			if workflowReplayEffectCounts(t, r.Store) != before || mutationProjectionState(t, r.Store)["idempotency"] != index {
				t.Error("corrupt index replay changed effects or index")
			}
		})
	}
}

func TestLegacyWorkflowReplayUsesDurableOwner(t *testing.T) {
	ctx := t.Context()
	r, grant, raw, _ := committedWorkflowReplay(t)
	opID := "workflow-" + mutationDigest(r.Tool, r.Operation, r.Envelope, raw)[7:31]
	db := r.Store.DatabaseForTesting()
	if _, err := db.Exec(`UPDATE idempotency_records SET authorized_scope_snapshot=json_remove(authorized_scope_snapshot,'$.mutation_response'),result_payload='{}',changed_refs='[]' WHERE op_id=?`, opID); err != nil {
		t.Fatal(err)
	}
	owner, err := readOperationResult(ctx, r.Store, opID)
	if err != nil || len(owner.ResultPayload) == 0 || len(owner.ChangedRefs) == 0 {
		t.Fatalf("durable fixture missing result: owner=%+v err=%v", owner, err)
	}
	driftWorkPinVersion(t, r.Store, "work-1")
	before := workflowReplayEffectCounts(t, r.Store)
	index := mutationProjectionState(t, r.Store)["idempotency"]
	public, err := Dispatch(ctx, r.Store, r.Authority, InvokeRequest{Tool: r.Tool, Operation: r.Operation, Input: raw}, r.Envelope)
	if err != nil {
		t.Fatal(err)
	}
	transaction := workflowTransactionReplay(t, r, grant, raw)
	for name, replay := range map[string]agentcontract.Envelope{"public": public, "transaction": transaction} {
		if replay.Outcome != agentcontract.OutcomeError || !replay.Replayed || replay.Error == nil || replay.Error.Kind != "malformed_response" || replay.Error.RecoveryAction.Kind != "contact_operator" || replay.Error.EffectState != agentcontract.EffectPossible || replay.Error.RetrySafe || !strings.Contains(replay.Error.Message, "original WorkPin was not retained") || !strings.Contains(replay.Error.Message, `"work-1"`) || !strings.Contains(replay.Error.Message, "concord_work_trace.resume") || !strings.Contains(replay.Error.Message, "concord_work_browse.list") || !strings.Contains(replay.Error.Message, "if the work was removed, no current work state is available") {
			t.Errorf("%s legacy replay did not name the missing original WorkPin and current-state recovery: error=%+v", name, replay.Error)
		}
	}
	if workflowReplayEffectCounts(t, r.Store) != before {
		t.Error("legacy workflow replay repeated an effect or changed later work")
	}
	if mutationProjectionState(t, r.Store)["idempotency"] != index {
		t.Error("legacy workflow replay changed immutable retained row content")
	}
	after, err := readOperationResult(ctx, r.Store, opID)
	if err != nil || !reflect.DeepEqual(owner, after) {
		t.Errorf("legacy workflow replay changed durable owner: before=%+v after=%+v err=%v", owner, after, err)
	}
}

func TestLegacyWorkflowReplayOwnerValidation(t *testing.T) {
	for _, tc := range []struct {
		name, scope, payload, evidence string
		missing, success, mismatch     bool
	}{
		{name: "complete", success: true},
		{name: "default owner evidence", evidence: "default", success: true},
		{name: "owner reference mismatch", mismatch: true},
		{name: "missing owner", missing: true},
		{name: "invalid owner", payload: `{"unexpected":true}`},
		{name: "null snapshot", scope: `null`},
		{name: "partial snapshot", scope: `{"manifest_digest":"incomplete"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			r, grant, raw, first := committedWorkflowReplay(t)
			opID := "workflow-" + mutationDigest(r.Tool, r.Operation, r.Envelope, raw)[7:31]
			db := r.Store.DatabaseForTesting()
			payload := string(first.Result)
			if tc.payload != "" {
				payload = tc.payload
			}
			evidence := []string{}
			if tc.evidence == "default" {
				owner, err := readOperationResult(ctx, r.Store, opID)
				if err != nil || !reflect.DeepEqual(owner.EvidenceRefs, []string{"evidence:" + opID}) {
					t.Fatalf("default durable evidence=%+v err=%v", owner.EvidenceRefs, err)
				}
				evidence = owner.EvidenceRefs
			} else if tc.evidence != "" {
				if err := json.Unmarshal([]byte(tc.evidence), &evidence); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := db.Exec(`UPDATE durable_operations SET result_payload=?,evidence_refs=? WHERE op_id=?`, payload, string(retryJSON(evidence)), opID); err != nil {
				t.Fatal(err)
			}
			if tc.mismatch {
				if _, err := db.Exec(`UPDATE durable_operations SET changed_refs='[]' WHERE op_id=?`, opID); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := db.Exec(`UPDATE idempotency_records SET authorized_scope_snapshot=json_remove(authorized_scope_snapshot,'$.mutation_response'),result_payload='{}',changed_refs='[]' WHERE op_id=?`, opID); err != nil {
				t.Fatal(err)
			}
			if tc.scope != "" {
				if _, err := db.Exec(`UPDATE idempotency_records SET authorized_scope_snapshot=json_set(authorized_scope_snapshot,'$.mutation_response',json(?)) WHERE op_id=?`, tc.scope, opID); err != nil {
					t.Fatal(err)
				}
			}
			if tc.missing {
				if _, err := db.Exec(`DELETE FROM durable_operations WHERE op_id=?`, opID); err != nil {
					t.Fatal(err)
				}
			}
			driftWorkPinVersion(t, r.Store, "work-1")
			before, index := workflowReplayEffectCounts(t, r.Store), mutationProjectionState(t, r.Store)["idempotency"]
			var evidenceBefore string
			if !tc.missing {
				if err := db.QueryRow(`SELECT evidence_refs FROM durable_operations WHERE op_id=?`, opID).Scan(&evidenceBefore); err != nil {
					t.Fatal(err)
				}
			}
			public, err := Dispatch(ctx, r.Store, r.Authority, InvokeRequest{Tool: r.Tool, Operation: r.Operation, Input: raw}, r.Envelope)
			if err != nil {
				t.Fatal(err)
			}
			transaction := workflowTransactionReplay(t, r, grant, raw)
			for _, replay := range []agentcontract.Envelope{public, transaction} {
				if tc.success {
					if replay.Outcome != agentcontract.OutcomeOK || !replay.Replayed || string(replay.Result) != payload || !reflect.DeepEqual(replay.ChangedRefs, first.ChangedRefs) || len(replay.EvidenceRefs) != 0 {
						t.Errorf("complete owner replay=%+v error=%+v", replay, replay.Error)
					}
				} else if replay.Outcome != agentcontract.OutcomeError || replay.Error == nil || replay.Error.EffectState != agentcontract.EffectPossible || replay.Error.RetrySafe {
					t.Errorf("incomplete owner became success or safe retry: %+v", replay)
				}
			}
			if workflowReplayEffectCounts(t, r.Store) != before || mutationProjectionState(t, r.Store)["idempotency"] != index {
				t.Error("replay changed effects or immutable retained content")
			}
			if !tc.missing {
				var evidenceAfter string
				if err := db.QueryRow(`SELECT evidence_refs FROM durable_operations WHERE op_id=?`, opID).Scan(&evidenceAfter); err != nil || evidenceBefore != evidenceAfter {
					t.Errorf("durable evidence changed: before=%s after=%s err=%v", evidenceBefore, evidenceAfter, err)
				}
			}
		})
	}
}

func TestRetainedWorkflowInvokeAcrossManifestChange(t *testing.T) {
	ctx := t.Context()
	r, grant, raw, first := committedWorkflowReplay(t)
	opID := "workflow-" + mutationDigest(r.Tool, r.Operation, r.Envelope, raw)[7:31]
	producerDigest := "sha256:" + strings.Repeat("0", 64)
	if _, err := r.Store.DatabaseForTesting().Exec(`UPDATE durable_operations SET contract_digest=? WHERE op_id=?`, producerDigest, opID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Store.DatabaseForTesting().Exec(`UPDATE idempotency_records SET authorized_scope_snapshot=json_set(authorized_scope_snapshot,'$.mutation_response.manifest_digest',?) WHERE op_id=?`, producerDigest, opID); err != nil {
		t.Fatal(err)
	}
	driftWorkPinVersion(t, r.Store, "work-1")
	before := workflowReplayEffectCounts(t, r.Store)
	ownerBefore, err := readOperationResult(ctx, r.Store, opID)
	if err != nil {
		t.Fatal(err)
	}
	commits := r.Store.DurableCommits()
	response, err := Invoke(ctx, r.Store, r.Authority, mustMarshalInvoke(t, InvokeRequest{Tool: r.Tool, Operation: r.Operation, Input: raw}, r.Envelope))
	if err != nil || response.Outcome != agentcontract.OutcomeOK || !response.Replayed {
		t.Fatalf("retained workflow response refused: err=%v response=%+v error=%+v", err, response, response.Error)
	}
	requireReplayCommit(t, r.Store, "retained-replay", commits, 0, true)
	transaction := workflowTransactionReplay(t, r, grant, raw)
	for _, replay := range []agentcontract.Envelope{response, transaction} {
		if replay.Outcome != agentcontract.OutcomeOK || !replay.Replayed || string(replay.Result) != string(first.Result) || !reflect.DeepEqual(replay.ChangedRefs, first.ChangedRefs) || !reflect.DeepEqual(replay.NextValidIntents, first.NextValidIntents) || !reflect.DeepEqual(replay.ResolvedScope, first.ResolvedScope) || !reflect.DeepEqual(replay.Omissions, first.Omissions) || !reflect.DeepEqual(replay.EvidenceRefs, first.EvidenceRefs) {
			t.Fatalf("replay replaced original workflow facts: first=%+v replay=%+v", first, replay)
		}
		if replay.ManifestDigest != agentcontract.ManifestDigest {
			t.Fatal("replay envelope did not bind the current invocation manifest")
		}
	}
	requireReplayCommit(t, r.Store, "retained-replay", commits+1, 1, true)
	if workflowReplayEffectCounts(t, r.Store) != before {
		t.Fatal("workflow replay repeated effects or replaced the later work state")
	}
	ownerAfter, err := readOperationResult(ctx, r.Store, opID)
	if err != nil || !reflect.DeepEqual(ownerBefore, ownerAfter) {
		t.Fatalf("replay rewrote producer facts: before=%+v after=%+v err=%v", ownerBefore, ownerAfter, err)
	}
	commits = r.Store.DurableCommits()
	conflicting := json.RawMessage(strings.Replace(string(raw), `"outcome":"proceed"`, `"outcome":"decline"`, 1))
	conflict, err := Invoke(ctx, r.Store, r.Authority, mustMarshalInvoke(t, InvokeRequest{Tool: r.Tool, Operation: r.Operation, Input: conflicting}, r.Envelope))
	if err != nil || conflict.Error == nil || conflict.Error.Kind != "idempotency_conflict" {
		t.Fatalf("changed workflow intent received retained response: err=%v error=%+v", err, conflict.Error)
	}
	mismatchedEnv := r.Envelope
	mismatchedEnv.ManifestDigest = producerDigest
	mismatched, err := Invoke(ctx, r.Store, r.Authority, mustMarshalInvoke(t, InvokeRequest{Tool: r.Tool, Operation: r.Operation, Input: raw}, mismatchedEnv))
	if err != nil || mismatched.Error == nil || mismatched.Error.EffectState != agentcontract.EffectNone || mismatched.Replayed {
		t.Fatalf("mismatched invocation reached workflow replay: err=%v error=%+v", err, mismatched.Error)
	}
	if r.Store.DurableCommits() != commits || workflowReplayEffectCounts(t, r.Store) != before {
		t.Fatal("refused workflow retry changed state")
	}
}

func TestRetainedMutationInvokeAcrossManifestChange(t *testing.T) {
	ctx := t.Context()
	s, service, grant, _ := mutationDispatchFixture(t, []agentcontract.Capability{"work_transition"})
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	tool, operation := "concord_work_transition", "lifecycle"
	lifecycleInput := func(key string, version int) json.RawMessage {
		return retryJSON(map[string]any{"work_id": "work-1", "target": "in_progress", "reason": "retain response fixture", "expected_version": version, "idempotency_key": key})
	}
	raw := lifecycleInput("retained-reconcile", 1)
	base := agentcontract.NewBase("original", tool, operation)
	payload := retryJSON(map[string]any{"changed_refs": []any{}, "next_valid_intents": []any{}})
	first := agentcontract.NewOKMutation(base, payload, nil, nil)
	first.ResolvedScope = &agentcontract.Scope{ProductID: "product-1", ProjectIDs: []string{"project-1"}}
	scope := mutationResponseScope(map[string]any{"product_id": "product-1", "project_ids": []string{"project-1"}, "work_ids": []string{"work-1"}}, first, nil)
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
	if !reflect.DeepEqual(before, after) {
		t.Fatal("ordinary replay changed retained state or replay metadata")
	}
	legacyKey := key
	legacyKey.IdempotencyKey = "legacy-missing-metadata"
	if err := s.Transact(ctx, func(tx *store.Transaction) error {
		legacyScope := map[string]any{"product_id": "product-1", "project_ids": []string{"project-1"}}
		return store.InsertMutationIdempotencyTx(ctx, tx, store.MutationIdempotencyInsert{Key: legacyKey, CanonicalDigest: digest, OperationID: "mutation-legacy-missing-metadata", ResultPayload: string(first.Result), ChangedRefs: "[]", AuthorizedScopeSnapshot: string(retryJSON(legacyScope)), ResultEventIDs: "[]", ObservedAt: fixedTime()})
	}); err != nil {
		t.Fatal(err)
	}
	legacyBefore := mutationProjectionState(t, s)
	legacy, err := Invoke(ctx, s, service, mustMarshalInvoke(t, InvokeRequest{Tool: tool, Operation: operation, Input: lifecycleInput(legacyKey.IdempotencyKey, 1)}, env))
	if err != nil || legacy.Outcome != agentcontract.OutcomeOK || !legacy.Replayed || string(legacy.Result) != string(first.Result) || !reflect.DeepEqual(legacy.ChangedRefs, first.ChangedRefs) {
		t.Fatalf("legacy missing metadata replay: err=%v response=%+v", err, legacy)
	}
	if !reflect.DeepEqual(legacyBefore, mutationProjectionState(t, s)) {
		t.Fatal("legacy replay changed retained state")
	}
	for _, vector := range []struct {
		name    string
		payload string
		changed string
		scope   map[string]any
	}{
		{"null metadata", string(first.Result), "[]", map[string]any{"product_id": "product-1", "project_ids": []string{"project-1"}, "mutation_response": nil}},
		{"partial metadata", string(first.Result), "[]", map[string]any{"product_id": "product-1", "project_ids": []string{"project-1"}, "mutation_response": map[string]any{"manifest_digest": agentcontract.ManifestDigest}}},
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
			invalid := lifecycleInput(invalidKey.IdempotencyKey, 1)
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
	changedInput := lifecycleInput(key.IdempotencyKey, 2)
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

// A mutation whose effect reports no auxiliary changed references commits an
// explicit empty list. Its retained response replays once per retry without re-running the
// effect, through both the early and the transactional replay owner.
func TestRetainedReplayKeepsCommittedEmptyChangedRefs(t *testing.T) {
	ctx := context.Background()
	s, service, grant := tiersRepoFixture(t)
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	r := runtime{Store: s, Authority: service, Envelope: mutationEnvelope(grant, scopeVersion), Tool: "concord_work_transition", Operation: "lifecycle"}
	op, _ := agentcontract.ValidateContractOperation(r.Tool, r.Operation)
	raw, _ := json.Marshal(map[string]any{"work_id": "work-1", "target": "in_progress", "reason": "retain response fixture", "expected_version": 1, "idempotency_key": "retained-reconcile"})
	digest := mutationDigest(r.Tool, r.Operation, r.Envelope, raw)
	scope := map[string]any{"product_id": "product-1", "project_ids": []string{"project-1"}, "work_ids": []string{"work-1"}}
	effects := 0
	execute := func(request string) agentcontract.Envelope {
		response, err := r.executeMutation(ctx, agentcontract.NewBase(request, r.Tool, r.Operation), raw, digest, scope, nil, nil, func(context.Context, *store.Transaction, Authority) (json.RawMessage, []string, []agentcontract.ChangedRef, error) {
			effects++
			return json.RawMessage(`{"changed_refs":[],"next_valid_intents":[]}`), nil, nil, nil
		})
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
	r := runtime{Tool: "concord_work_transition", Operation: "lifecycle"}
	base := agentcontract.NewBase("uncaptured", r.Tool, r.Operation)
	complete := agentcontract.NewOKMutation(base, mutationPayload(nil, nil), nil, nil)
	scopeJSON, _ := json.Marshal(mutationResponseScope(nil, complete, nil))
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

func mutationProjectionState(t *testing.T, s *store.Store) map[string]string {
	t.Helper()
	state := premiseConsentSnapshot(t, s)
	for name, query := range map[string]string{
		"idempotency":              `SELECT json_group_array(json_array(principal_ref,tool,operation_kind,idempotency_key,canonical_digest,op_id,result_payload,changed_refs,authorized_scope_snapshot)) FROM (SELECT * FROM idempotency_records ORDER BY principal_ref,tool,operation_kind,idempotency_key)`,
		"idempotency_observations": `SELECT json_group_array(json_array(idempotency_key,replayed_count,last_observed_at)) FROM (SELECT * FROM idempotency_records ORDER BY principal_ref,tool,operation_kind,idempotency_key)`,
	} {
		var value string
		if err := s.DatabaseForTesting().QueryRow(query).Scan(&value); err != nil {
			t.Fatal(err)
		}
		state[name] = value
	}
	return state
}

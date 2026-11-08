package agent

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/store"
)

func TestLegacyGenericReplayRestoresReadIntentQueryID(t *testing.T) {
	r := runtime{Tool: "concord_work_transition", Operation: "project_handoff_record"}
	prior := store.MutationIdempotencyRecord{AuthorizedScopeSnapshot: `{}`, ChangedRefs: `[]`, ResultPayload: `{"changed_refs":[],"next_valid_intents":[{"tool":"concord_work_trace","operation":"project_retirement","reason_code":"verify_handoff_recorded","required_fields":["work_id"]}],"work_id":"work-1","handoff_id":"handoff-1","already_recorded":false}`}
	replay := r.recordedMutationResult(agentcontract.NewBase("retry", r.Tool, r.Operation), prior)
	if replay.Outcome != agentcontract.OutcomeOK || !replay.Replayed || replay.NextValidIntents == nil || len(*replay.NextValidIntents) != 1 || (*replay.NextValidIntents)[0].QueryID != "CD-0182.R1" {
		t.Fatalf("read intent replay=%+v error=%+v", replay, replay.Error)
	}
}

func TestLegacyLifecycleCompletionReplayKeepsEvidence(t *testing.T) {
	s, service, grant := agentJobsMutationPM1Fixture(t)
	initializeMutationPinWorkflow(t, s, "work-cross")
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1); UPDATE workflow_instances SET instance_state='completed' WHERE work_id=?; DELETE FROM write_guard`, "work-cross"); err != nil {
		t.Fatal(err)
	}
	env := agentJobsMutationEnvelope(t, s, grant)
	_, version := readWorkFromStore(t, s, "work-cross")
	input := map[string]any{
		"work_id": "work-cross", "expected_version": version, "target": "completed", "reason": "complete with verification", "idempotency_key": "legacy-completion-evidence",
		"evidence": []agentcontract.EvidenceRef{{Kind: "verification", Authority: "agent-verifier", LocatorKind: "test", Locator: "verification-pass"}},
	}
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	request := InvokeRequest{Tool: "concord_work_transition", Operation: "lifecycle", Input: raw}
	first := dispatchMutation(t, s, service, request, env)
	if first.Outcome != agentcontract.OutcomeOK || len(first.EvidenceRefs) != 1 {
		t.Fatalf("first completion=%+v error=%+v", first, first.Error)
	}
	_, committed := readWorkFromStore(t, s, "work-cross")
	var snapshot string
	if err := s.DatabaseForTesting().QueryRow(`SELECT authorized_scope_snapshot FROM idempotency_records WHERE idempotency_key=?`, "legacy-completion-evidence").Scan(&snapshot); err != nil {
		t.Fatal(err)
	}
	retainedEvidence := []agentcontract.EvidenceRef{{Kind: "verification", Authority: "retained-authority", LocatorKind: "test", Locator: "retained-evidence"}}
	encodedEvidence, _ := json.Marshal(retainedEvidence)
	if _, err := s.DatabaseForTesting().Exec(`UPDATE idempotency_records SET authorized_scope_snapshot=json_set(authorized_scope_snapshot,'$.mutation_response.evidence_refs',json(?)) WHERE idempotency_key=?`, string(encodedEvidence), "legacy-completion-evidence"); err != nil {
		t.Fatal(err)
	}
	snapshotReplay := dispatchMutation(t, s, service, request, env)
	if snapshotReplay.Outcome != agentcontract.OutcomeOK || !reflect.DeepEqual(snapshotReplay.EvidenceRefs, retainedEvidence) {
		t.Errorf("current snapshot evidence was replaced: evidence=%+v error=%+v", snapshotReplay.EvidenceRefs, snapshotReplay.Error)
	}
	if _, err := s.DatabaseForTesting().Exec(`UPDATE idempotency_records SET authorized_scope_snapshot=? WHERE idempotency_key=?`, snapshot, "legacy-completion-evidence"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().Exec(`UPDATE idempotency_records SET authorized_scope_snapshot=json_remove(authorized_scope_snapshot,'$.mutation_response') WHERE idempotency_key=?`, "legacy-completion-evidence"); err != nil {
		t.Fatal(err)
	}
	key := store.MutationIdempotencyKey{PrincipalRef: grant.PrincipalRef, Tool: request.Tool, OperationKind: request.Operation, IdempotencyKey: "legacy-completion-evidence"}
	prior, found, err := s.LookupMutationIdempotency(context.Background(), key)
	if err != nil || !found {
		t.Fatalf("legacy record found=%t err=%v", found, err)
	}
	driftWorkPinVersion(t, s, "work-cross")
	before := mutationProjectionState(t, s)["idempotency"]
	replay := dispatchMutation(t, s, service, request, env)
	if replay.Outcome != agentcontract.OutcomeOK || !replay.Replayed || !reflect.DeepEqual(replay.EvidenceRefs, first.EvidenceRefs) || string(replay.Result) != string(first.Result) {
		t.Errorf("legacy completion replay lost original evidence: first=%+v replay=%+v error=%+v", first.EvidenceRefs, replay.EvidenceRefs, replay.Error)
	}
	r := runtime{Store: s, Authority: service, Envelope: env, Tool: request.Tool, Operation: request.Operation}
	base := agentcontract.NewBase("transaction", r.Tool, r.Operation)
	base.EvidenceRefs = append([]agentcontract.EvidenceRef{}, first.EvidenceRefs...)
	err = s.Transact(context.Background(), func(tx *store.Transaction) error {
		transaction, rejected, err := r.replayCachedMutationTx(context.Background(), tx, base, prior, grant, key)
		if err != nil || rejected || !reflect.DeepEqual(transaction.EvidenceRefs, first.EvidenceRefs) || string(transaction.Result) != string(first.Result) {
			t.Errorf("transaction replay rejected=%t err=%v response=%+v", rejected, err, transaction)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, after := readWorkFromStore(t, s, "work-cross"); after != committed+1 {
		t.Errorf("replay repeated effect: version=%d want=%d", after, committed+1)
	}
	if mutationProjectionState(t, s)["idempotency"] != before {
		t.Error("replay changed immutable retained records")
	}
	op, _ := agentcontract.ValidateContractOperation(r.Tool, r.Operation)
	other := grant
	other.PrincipalRef = "another-principal"
	_, handled, err := r.replayMutationBeforeScope(context.Background(), agentcontract.NewBase("principal", r.Tool, r.Operation), raw, other, op)
	if err != nil || handled {
		t.Errorf("foreign principal replay handled=%t err=%v", handled, err)
	}
	narrow := grant
	narrow.ProjectScope = nil
	denied, handled, err := r.replayMutationBeforeScope(context.Background(), agentcontract.NewBase("scope", r.Tool, r.Operation), raw, narrow, op)
	if err != nil || !handled || denied.Error == nil || denied.Error.Kind != "unauthorized" {
		t.Errorf("foreign scope handled=%t err=%v response=%+v", handled, err, denied.Error)
	}
	input["evidence"] = []agentcontract.EvidenceRef{{Kind: "verification", Authority: "agent-verifier", LocatorKind: "test", Locator: "different"}}
	changed, _ := json.Marshal(input)
	conflict, handled, err := r.replayMutationBeforeScope(context.Background(), agentcontract.NewBase("digest", r.Tool, r.Operation), changed, grant, op)
	if err != nil || !handled || conflict.Error == nil || conflict.Error.Kind != "idempotency_conflict" {
		t.Errorf("changed evidence handled=%t err=%v response=%+v", handled, err, conflict.Error)
	}
}

func TestLegacyGenericReplayNamesMissingOriginalPin(t *testing.T) {
	r := runtime{Tool: "concord_work_transition", Operation: "lifecycle"}
	prior := store.MutationIdempotencyRecord{AuthorizedScopeSnapshot: `{}`, ChangedRefs: `[{"entity_kind":"work_item","id":"work-1","version":"3"}]`, ResultPayload: `{"changed_refs":[{"entity_kind":"work_item","id":"work-1","version":3}],"next_valid_intents":[]}`}
	replay := r.recordedMutationResult(agentcontract.NewBase("retry", r.Tool, r.Operation), prior)
	if replay.Error == nil || replay.Error.Kind != "malformed_response" || replay.Error.EffectState != agentcontract.EffectPossible || replay.Error.RetrySafe || replay.Error.RecoveryAction.Kind != "contact_operator" || !strings.Contains(replay.Error.Message, "original WorkPin") || !strings.Contains(replay.Error.Message, "permitted omission") || !strings.Contains(replay.Error.Message, "concord_work_trace.resume") || !strings.Contains(replay.Error.Message, "concord_work_browse.list") {
		t.Fatalf("missing pin refusal=%+v", replay.Error)
	}
	overlap := runtime{Tool: "concord_work_relate", Operation: "resolve_overlap"}
	both := store.MutationIdempotencyRecord{AuthorizedScopeSnapshot: `{}`, ChangedRefs: `[{"entity_kind":"work_item","id":"work-1","version":"3"},{"entity_kind":"work_item","id":"work-2","version":"5"}]`, ResultPayload: `{"changed_refs":[{"entity_kind":"work_item","id":"work-1","version":3},{"entity_kind":"work_item","id":"work-2","version":5}],"next_valid_intents":[]}`}
	refused := overlap.recordedMutationResult(agentcontract.NewBase("retry", overlap.Tool, overlap.Operation), both)
	if refused.Error == nil || refused.Error.Kind != "malformed_response" || !strings.Contains(refused.Error.Message, `"work-1"`) || !strings.Contains(refused.Error.Message, `"work-2"`) {
		t.Fatalf("missing pin refusal did not name every pinless work item: %+v", refused.Error)
	}
}

func TestLegacyRemovalReplayKeepsPermittedPinOmission(t *testing.T) {
	r := runtime{Tool: "concord_work_transition", Operation: "remove"}
	changed := []agentcontract.ChangedRef{{EntityKind: "work_item", ID: "work-1", Version: "4"}}
	refs, err := json.Marshal(changed)
	if err != nil {
		t.Fatal(err)
	}
	prior := store.MutationIdempotencyRecord{AuthorizedScopeSnapshot: `{}`, ChangedRefs: string(refs), ResultPayload: string(mutationPayload(changed, nil))}
	replay := r.recordedMutationResult(agentcontract.NewBase("retry", r.Tool, r.Operation), prior)
	if replay.Outcome != agentcontract.OutcomeOK || !replay.Replayed || string(replay.Result) != prior.ResultPayload || len(replay.Omissions) != 1 || replay.Omissions[0].Kind != "work_pin_unavailable" || replay.Omissions[0].Count != 1 {
		t.Fatalf("legacy removal replay=%+v error=%+v", replay, replay.Error)
	}
	changed = append(changed, agentcontract.ChangedRef{EntityKind: "work_item", ID: "work-2", Version: "2"})
	refs, err = json.Marshal(changed)
	if err != nil {
		t.Fatal(err)
	}
	extra := r.recordedMutationResult(agentcontract.NewBase("retry", r.Tool, r.Operation), store.MutationIdempotencyRecord{AuthorizedScopeSnapshot: `{}`, ChangedRefs: string(refs), ResultPayload: string(mutationPayload(changed, nil))})
	if extra.Error == nil || extra.Error.Kind != "malformed_response" || extra.Error.EffectState != agentcontract.EffectPossible || !strings.Contains(extra.Error.Message, "permitted omission") {
		t.Fatalf("removal listing another pinless item became success: %+v", extra)
	}
}

func TestWorkflowReplayPropagatesCancelledOwnerRead(t *testing.T) {
	r, grant, _, _ := committedWorkflowReplay(t)
	key := store.MutationIdempotencyKey{PrincipalRef: grant.PrincipalRef, Tool: r.Tool, OperationKind: r.Operation, IdempotencyKey: "retained-replay"}
	prior, found, err := r.Store.LookupMutationIdempotency(context.Background(), key)
	if err != nil || !found {
		t.Fatalf("record found=%t err=%v", found, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = r.Store.Transact(context.Background(), func(tx *store.Transaction) error {
		response, readErr := r.replayWorkflowActionTx(ctx, tx, agentcontract.NewBase("cancelled", r.Tool, r.Operation), prior, nil)
		if !errors.Is(readErr, context.Canceled) {
			t.Errorf("cancelled owner read: response=%+v err=%v", response, readErr)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestIdempotentResponseKeepsCommittedSubjectWithoutHistory(t *testing.T) {
	s, service, grant := tiersRepoFixture(t)
	initializeMutationPinWorkflow(t, s, "work-1")
	_, before := readWorkFromStore(t, s, "work-1")
	input := map[string]any{"work_id": "work-1", "target": "in_progress", "reason": "begin work", "expected_version": before, "idempotency_key": "response-lifecycle"}
	first := tiersInvoke(t, s, service, grant, "concord_work_transition", "lifecycle", input)
	if first.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("first outcome=%s error=%+v", first.Outcome, first.Error)
	}
	_, committed := readWorkFromStore(t, s, "work-1")
	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	r := runtime{Store: s, Authority: service, Envelope: mutationEnvelope(grant, scopeVersion), Tool: "concord_work_transition", Operation: "lifecycle"}
	op, _ := agentcontract.ValidateContractOperation(r.Tool, r.Operation)
	retry := func() agentcontract.Envelope {
		raw, _ := json.Marshal(input)
		response, handled, err := r.replayMutationBeforeScope(context.Background(), agentcontract.NewBase("retry", r.Tool, r.Operation), raw, grant, op)
		if err != nil || !handled {
			t.Fatalf("retry handled=%t error=%v", handled, err)
		}
		return response
	}
	driftWorkPinVersion(t, s, "work-1")
	if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM sqlite_schema WHERE name='domain_events'`); got != 0 {
		t.Fatal("current migrated store unexpectedly retains the event log")
	}
	replay := retry()
	if replay.Outcome != agentcontract.OutcomeOK || !replay.Replayed {
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
	denied, handled, err := r.replayMutationBeforeScope(context.Background(), agentcontract.NewBase("scope", r.Tool, r.Operation), raw, narrow, op)
	if err != nil || !handled || denied.Error == nil || denied.Error.Kind != "unauthorized" {
		t.Fatalf("foreign scope replay=%+v handled=%t err=%v", denied.Error, handled, err)
	}
	foreign := grant
	foreign.PrincipalRef = "another-principal"
	_, handled, err = r.replayMutationBeforeScope(context.Background(), agentcontract.NewBase("actor", r.Tool, r.Operation), raw, foreign, op)
	if err != nil || handled {
		t.Fatalf("foreign principal received retained response: handled=%t err=%v", handled, err)
	}
	key := store.MutationIdempotencyKey{PrincipalRef: grant.PrincipalRef, Tool: r.Tool, OperationKind: r.Operation, IdempotencyKey: "response-lifecycle"}
	prior, found, err := s.LookupMutationIdempotency(context.Background(), key)
	if err != nil || !found {
		t.Fatalf("retained record found=%t err=%v", found, err)
	}
	wrongSubject := prior
	wrongSubject.ResultPayload = strings.ReplaceAll(prior.ResultPayload, "work-1", "foreign-work")
	refused := r.recordedMutationResult(agentcontract.NewBase("subject", r.Tool, r.Operation), wrongSubject)
	if refused.Outcome != agentcontract.OutcomeError || refused.Error == nil || refused.Error.EffectState != agentcontract.EffectPossible {
		t.Fatalf("response changed the committed subject: %+v", refused)
	}
	err = s.Transact(context.Background(), func(tx *store.Transaction) error {
		response, rejected, err := r.replayCachedMutationTx(context.Background(), tx, agentcontract.NewBase("transaction", r.Tool, r.Operation), prior, grant, key)
		if err != nil || rejected || string(response.Result) != string(first.Result) {
			t.Fatalf("transaction replay rejected=%t err=%v response=%+v", rejected, err, response.Error)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	effects := 0
	response, err := r.executeMutation(context.Background(), agentcontract.NewBase("cached-effect", r.Tool, r.Operation), raw, mutationDigest(r.Tool, r.Operation, r.Envelope, raw), map[string]any{"product_id": "product-1", "project_ids": []string{"project-1"}, "work_ids": []string{"work-1"}}, nil, nil,
		func(context.Context, *store.Transaction, Authority) (json.RawMessage, []string, []agentcontract.ChangedRef, error) {
			effects++
			return nil, nil, nil, nil
		})
	if err != nil || response.Outcome != agentcontract.OutcomeOK || !response.Replayed || effects != 0 {
		t.Fatalf("cached replay repeated effect: response=%+v err=%v effects=%d", response.Error, err, effects)
	}
	input["reason"] = "different reason"
	conflict := retry()
	if conflict.Error == nil || conflict.Error.Kind != "idempotency_conflict" {
		t.Fatalf("changed input did not conflict: %+v", conflict.Error)
	}
}

func TestLegacyGenericMutationReplaysRetainedResult(t *testing.T) {
	s, service, grant := tiersRepoFixture(t)
	initializeMutationPinWorkflow(t, s, "work-1")
	_, version := readWorkFromStore(t, s, "work-1")
	input := map[string]any{"work_id": "work-1", "target": "in_progress", "reason": "begin work", "expected_version": version, "idempotency_key": "legacy-generic"}
	first := tiersInvoke(t, s, service, grant, "concord_work_transition", "lifecycle", input)
	if first.Outcome != agentcontract.OutcomeOK || len(first.Result) == 0 || first.ChangedRefs == nil || len(*first.ChangedRefs) == 0 {
		t.Fatalf("first mutation did not retain a result and references: %+v", first)
	}
	_, committed := readWorkFromStore(t, s, "work-1")
	db := s.DatabaseForTesting()
	result, err := db.Exec(`UPDATE idempotency_records SET authorized_scope_snapshot=json_remove(authorized_scope_snapshot,'$.mutation_response') WHERE idempotency_key=?`, "legacy-generic")
	if err != nil {
		t.Fatal(err)
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		t.Fatalf("legacy fixture rows=%d err=%v", n, err)
	}
	row := func() string {
		t.Helper()
		var value string
		err := db.QueryRow(`SELECT json_array(principal_ref,tool,operation_kind,idempotency_key,canonical_digest,op_id,result_event_ids,result_payload,changed_refs,authorized_scope_snapshot,first_observed_at) FROM idempotency_records WHERE idempotency_key=?`, "legacy-generic").Scan(&value)
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	retained := row()
	driftWorkPinVersion(t, s, "work-1")
	replay := tiersInvoke(t, s, service, grant, "concord_work_transition", "lifecycle", input)
	if replay.Outcome != agentcontract.OutcomeOK || !replay.Replayed {
		t.Errorf("legacy replay outcome=%s replayed=%t error=%+v", replay.Outcome, replay.Replayed, replay.Error)
	} else if string(replay.Result) != string(first.Result) || !reflect.DeepEqual(replay.ChangedRefs, first.ChangedRefs) || !reflect.DeepEqual(replay.NextValidIntents, first.NextValidIntents) {
		t.Errorf("legacy replay changed committed result or references: first=%+v replay=%+v", first, replay)
	}
	_, after := readWorkFromStore(t, s, "work-1")
	if after != committed+1 {
		t.Errorf("replay repeated an effect: version=%d want=%d", after, committed+1)
	}
	if got := row(); got != retained {
		t.Errorf("replay changed retained row: before=%s after=%s", retained, got)
	}
}

func TestIdempotentResponseMissingFactsKeepPossibleEffects(t *testing.T) {
	r := runtime{Tool: "concord_work_transition", Operation: "workflow_action"}
	base := agentcontract.NewBase("incomplete", r.Tool, r.Operation)
	complete := agentcontract.NewOKMutation(base, mutationPayload(nil, nil), nil, nil)
	scopeJSON, _ := json.Marshal(mutationResponseScope(nil, complete, nil))
	for _, prior := range []store.MutationIdempotencyRecord{
		{ResultPayload: `{}`, ChangedRefs: `[]`, AuthorizedScopeSnapshot: `{}`},
		{ResultPayload: `{}`, ChangedRefs: `not-json`, AuthorizedScopeSnapshot: string(scopeJSON)},
		{ResultPayload: `{}`, ChangedRefs: `[]`, AuthorizedScopeSnapshot: `{"mutation_response":{"manifest_digest":"` + agentcontract.ManifestDigest + `"}}`},
	} {
		response := r.recordedMutationResult(base, prior)
		if response.Outcome != agentcontract.OutcomeError || response.Error == nil || response.Error.Kind != "malformed_response" || response.Error.EffectState != agentcontract.EffectPossible || response.Error.RecoveryAction.Kind != "contact_operator" || response.Error.RetrySafe {
			t.Fatalf("missing facts became success or a no-effect retry: %+v", response)
		}
	}
}

func TestLegacyGenericReplayRejectsIncompleteFacts(t *testing.T) {
	r := runtime{Tool: "concord_work_transition", Operation: "lifecycle"}
	base := agentcontract.NewBase("legacy-incomplete", r.Tool, r.Operation)
	for _, prior := range []store.MutationIdempotencyRecord{
		{ResultPayload: `{}`, ChangedRefs: `[]`, AuthorizedScopeSnapshot: `{}`},
		{ResultPayload: `{"changed_refs":[],"next_valid_intents":[]}`, ChangedRefs: `null`, AuthorizedScopeSnapshot: `{}`},
		{ResultPayload: `{"changed_refs":[],"next_valid_intents":[]}`, ChangedRefs: `[]`, AuthorizedScopeSnapshot: `{"mutation_response":null}`},
		{ResultPayload: `{"changed_refs":[],"next_valid_intents":[]}`, ChangedRefs: `[]`, AuthorizedScopeSnapshot: `{"mutation_response":{}}`},
	} {
		response := r.recordedMutationResult(base, prior)
		if response.Outcome != agentcontract.OutcomeError || response.Error == nil || response.Error.Kind != "malformed_response" || response.Error.EffectState != agentcontract.EffectPossible || response.Error.RetrySafe {
			t.Fatalf("incomplete legacy result became success or safe retry: record=%+v response=%+v", prior, response)
		}
	}
}

func TestIdempotentResponseWorkflowCapturesTransactionResult(t *testing.T) {
	r, grant, raw, first := committedWorkflowReplay(t)
	ctx := context.Background()
	op, _ := agentcontract.ValidateContractOperation(r.Tool, r.Operation)
	key := store.MutationIdempotencyKey{PrincipalRef: grant.PrincipalRef, Tool: r.Tool, OperationKind: r.Operation, IdempotencyKey: "retained-replay"}
	prior, found, err := r.Store.LookupMutationIdempotency(ctx, key)
	if err != nil || !found || prior.ResultPayload != string(first.Result) {
		t.Fatalf("completed response not captured: record=%+v found=%t err=%v", prior, found, err)
	}
	driftWorkPinVersion(t, r.Store, "work-1")
	if got := countRows(t, r.Store.DatabaseForTesting(), `SELECT count(*) FROM sqlite_schema WHERE name='domain_events'`); got != 0 {
		t.Fatal("current migrated store unexpectedly retains the event log")
	}
	replay, handled, err := r.replayMutationBeforeScope(ctx, agentcontract.NewBase("workflow-retry", r.Tool, r.Operation), raw, grant, op)
	if err != nil || !handled || replay.Outcome != agentcontract.OutcomeOK || !replay.Replayed || string(replay.Result) != string(first.Result) {
		t.Fatalf("workflow replay lost transaction result: handled=%t err=%v response=%+v", handled, err, replay)
	}
	err = r.Store.Transact(ctx, func(tx *store.Transaction) error {
		replay, err := r.replayWorkflowActionTx(ctx, tx, agentcontract.NewBase("workflow-tx-retry", r.Tool, r.Operation), prior, map[string]any{"product_id": "product-1"})
		if err != nil || replay.Outcome != agentcontract.OutcomeOK || string(replay.Result) != string(first.Result) {
			t.Fatalf("workflow transaction replay lost result: err=%v response=%+v", err, replay)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

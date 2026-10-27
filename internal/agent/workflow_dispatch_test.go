package agent

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/pm1fixture"
	"github.com/sharper-flow/concord/internal/store"
)

func seedAgentWorkflow(t *testing.T, s *store.Store, grant Authority) int64 {
	t.Helper()
	// Seed the live authoring head, not a frozen version: the boundary's
	// closed action union admits only the current heads' declared shapes, so
	// every seeded workflow walks the canonical graph (CD-0218 D4 keeps the
	// frozen pins registered for decode, never for new public authoring).
	registered, err := store.LiveWorkflowDefinitionForRef("workflow.implementation")
	if err != nil {
		t.Fatal(err)
	}
	return seedAgentWorkflowAt(t, s, grant, registered)
}

// seedAgentWorkflowAt seeds work-1 under the given registered pin.
func seedAgentWorkflowAt(t *testing.T, s *store.Store, grant Authority, registered store.RegisteredDefinition) int64 {
	t.Helper()
	ctx := context.Background()
	seedCurrentWorkflowDomainFixture(t, s)
	if err := s.Transact(ctx, func(tx *store.Transaction) error {
		return store.InitializeWorkflowTx(ctx, tx, store.WorkflowInitializationRequest{WorkID: "work-1", Definition: registered, Actor: store.WorkflowActor{PrincipalRef: grant.PrincipalRef, ClientRef: grant.ClientRef, AgentRef: grant.AgentRef, SessionRef: grant.SessionRef, ActorClass: store.ActorAgent}, Now: fixedTime()})
	}); err != nil {
		t.Fatal(err)
	}
	version, err := workVersionForWorkflow(t, s)
	if err != nil {
		t.Fatal(err)
	}
	return version
}

func workVersionForWorkflow(t *testing.T, s *store.Store) (int64, error) {
	t.Helper()
	var version int64
	err := s.DatabaseForTesting().QueryRow(`SELECT version FROM work_items WHERE id='work-1'`).Scan(&version)
	return version, err
}

func TestWorkflowReplayTransactionUsesCommittedResponse(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, service, grant, _ := mutationDispatchFixture(t, []agentcontract.Capability{"work_transition"})
	version := seedAgentWorkflow(t, s, grant)
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	input := json.RawMessage(`{"work_id":"work-1","expected_version":` + strconv.FormatInt(version, 10) + `,"action_id":"record_proposal","fields":{"problem":"The bounded problem statement.","affected":["The affected system."],"stakes":"The bounded stakes statement.","user_outcomes":["The expected user outcome."]},"idempotency_key":"tx-replay-owner"}`)
	first, err := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: input}, env)
	if err != nil || first.Outcome != OutcomeOK {
		t.Fatalf("initial workflow response=%+v err=%v", first, err)
	}
	// Exercise the transaction-bound replay window, including its durable
	// metadata write, without reapplying the already committed action.
	before := workflowReplayEffectCounts(t, s)
	commits := s.DurableCommits()
	var in actionMutationInput
	if err := json.Unmarshal(input, &in); err != nil {
		t.Fatal(err)
	}
	r := runtime{Store: s, Authority: service, Envelope: env, Tool: "concord_work_transition", Operation: "workflow_action"}
	var response Envelope
	var rejected, mutated bool
	err = store.AuthorizeWorkflowActionAtBoundaryWithPreflightTx(ctx, s, nil, store.WorkflowActionPreflightRequest{WorkID: in.WorkID, ExpectedVersion: in.ExpectedVersion, ActionID: in.ActionID}, r.workflowActionReplayPreflight(ctx, NewBase(env.RequestID, r.Tool, r.Operation), mutationDigest(r.Tool, r.Operation, env, input), grant, in, &response, &rejected), nil, func(*store.Transaction) error {
		mutated = true
		return nil
	})
	if mutated || workflowReplayEffectCounts(t, s) != before {
		t.Fatalf("STOP: transaction replay repeated an effect: mutated=%t before=%+v after=%+v", mutated, before, workflowReplayEffectCounts(t, s))
	}
	if rejected {
		t.Fatalf("transaction replay refused its committed response: response=%+v err=%v", response.Error, err)
	}
	if err != nil || response.Outcome != OutcomeOK || !response.Replayed || string(response.Result) != string(first.Result) {
		t.Fatalf("transaction replay response=%+v err=%v, want original result=%s", response, err, first.Result)
	}
	if err := response.Validate(); err != nil {
		t.Fatalf("replay envelope: %v", err)
	}
	requireReplayCommit(t, s, "tx-replay-owner", commits, 0, true)
}

type workflowReplayEffects struct {
	Events, Operations, Attempts, Dispatches int
	Version                                  int64
	CurrentRows                              string
}

func workflowReplayEffectCounts(t *testing.T, s *store.Store) workflowReplayEffects {
	t.Helper()
	var counts workflowReplayEffects
	for table, target := range map[string]*int{"effect_receipts": &counts.Events, "durable_operations": &counts.Operations, "worker_attempts": &counts.Attempts} {
		if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM ` + table).Scan(target); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM effect_receipts WHERE kind=?`, store.WorkerDispatched).Scan(&counts.Dispatches); err != nil {
		t.Fatal(err)
	}
	counts.Version = workOneVersion(t, s)
	counts.CurrentRows = agentCurrentEffects(t, s)
	return counts
}

func TestWorkflowReplayStoresCommittedResponse(t *testing.T) {
	t.Parallel()
	s, service, grant, _ := mutationDispatchFixture(t, []agentcontract.Capability{"work_transition"})
	version := seedAgentWorkflow(t, s, grant)
	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	input := json.RawMessage(`{"work_id":"work-1","expected_version":` + strconv.FormatInt(version, 10) + `,"action_id":"record_proposal","fields":{"problem":"The bounded problem statement.","affected":["The affected system."],"stakes":"The bounded stakes statement.","user_outcomes":["The expected user outcome."]},"idempotency_key":"no-response-copy"}`)
	response, err := Dispatch(context.Background(), s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: input}, env)
	if err != nil || response.Outcome != OutcomeOK {
		t.Fatalf("workflow response=%+v err=%v", response, err)
	}
	var payload, refs, events string
	if err := s.DatabaseForTesting().QueryRow(`SELECT result_payload,changed_refs,result_event_ids FROM idempotency_records WHERE idempotency_key='no-response-copy'`).Scan(&payload, &refs, &events); err != nil {
		t.Fatal(err)
	}
	if payload != string(response.Result) {
		t.Fatalf("committed workflow response not retained: payload=%s response=%s", payload, response.Result)
	}
	var changed []ChangedRef
	if err := json.Unmarshal([]byte(refs), &changed); err != nil || response.ChangedRefs == nil || !reflect.DeepEqual(changed, *response.ChangedRefs) {
		t.Fatalf("committed changed references lost: refs=%s response=%+v err=%v", refs, response.ChangedRefs, err)
	}
	if events == "[]" {
		t.Fatal("result identity references were discarded")
	}
	digest := mutationDigest("concord_work_transition", "workflow_action", env, input)
	owner, err := store.Step(context.Background(), s, "workflow-"+digest[7:31])
	if err != nil || owner.ResultKind != store.ResultCompleted || len(owner.ResultPayload) == 0 || len(owner.ChangedRefs) != 1 {
		t.Fatalf("durable owner=%+v err=%v", owner, err)
	}
	prior, found, err := s.LookupMutationIdempotency(context.Background(), store.MutationIdempotencyKey{PrincipalRef: grant.PrincipalRef, Tool: "concord_work_transition", OperationKind: "workflow_action", IdempotencyKey: "no-response-copy"})
	if err != nil || !found {
		t.Fatalf("completed response record found=%t err=%v", found, err)
	}
	snapshot, err := retainedMutationResponse(prior.AuthorizedScopeSnapshot)
	if err != nil || !reflect.DeepEqual(snapshot.ResolvedScope, response.ResolvedScope) || response.NextValidIntents == nil || !reflect.DeepEqual(snapshot.NextValidIntents, *response.NextValidIntents) || !reflect.DeepEqual(snapshot.EvidenceRefs, response.EvidenceRefs) || !reflect.DeepEqual(snapshot.Omissions, response.Omissions) {
		t.Fatalf("committed response metadata differs: snapshot=%+v response=%+v err=%v", snapshot, response, err)
	}
}

func TestWorkflowReplayOwnerClassifications(t *testing.T) {
	t.Parallel()
	for _, vector := range []struct {
		name, kind, ownerPayload, ownerDigest, scope string
		missing                                      bool
		missingPayload                               bool
		want                                         Outcome
	}{
		{name: "completed", kind: "completed", want: OutcomeOK},
		{name: "pending", kind: "pending", want: OutcomePending},
		{name: "partial", kind: "partial", want: OutcomePartial},
		{name: "failed", kind: "failed", want: OutcomePartial},
		{name: "failed_stale", kind: "failed_stale", want: OutcomePartial},
		{name: "malformed result", kind: "completed", ownerPayload: "{}", want: OutcomeError},
		{name: "manifest mismatch", kind: "completed", ownerDigest: "sha256:" + strings.Repeat("0", 64), want: OutcomeError},
		{name: "missing owner", kind: "completed", missing: true, want: OutcomeError},
		{name: "missing response payload", kind: "completed", missingPayload: true, want: OutcomeError},
		{name: "missing response metadata", kind: "completed", scope: `{"product_id":"product-1","project_ids":["project-1"]}`, want: OutcomeError},
		{name: "unreadable scope", kind: "completed", scope: "not JSON", want: OutcomeError},
		{name: "unauthorized original scope", kind: "completed", scope: `{"product_id":"another-product","project_ids":["project-1"]}`, want: OutcomeError},
	} {
		t.Run(vector.name, func(t *testing.T) {
			s, service, grant, _ := mutationDispatchFixture(t, []agentcontract.Capability{"work_transition"})
			version := seedAgentWorkflow(t, s, grant)
			scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
			if err != nil {
				t.Fatal(err)
			}
			env := mutationEnvelope(grant, scopeVersion)
			raw := json.RawMessage(`{"work_id":"work-1","expected_version":4,"action_id":"record_proposal","fields":{"problem":"The bounded problem statement.","affected":["The affected system."],"stakes":"The bounded stakes statement.","user_outcomes":["The expected user outcome."]},"idempotency_key":"owner-classification"}`)
			opID := seedCurrentWorkflowActionReplay(t, s, env, raw, vector.kind)
			changed := ChangedRef{EntityKind: "work_item", ID: "work-1", Version: strconv.FormatInt(version, 10)}
			refs, _ := json.Marshal([]string{`{"entity_kind":"work_item","id":"work-1","version":` + strconv.FormatInt(version, 10) + `}`})
			payload, _ := json.Marshal(map[string]any{"changed_refs": mutationResultChangedRefs([]ChangedRef{changed}), "next_valid_intents": []any{}, "operation_id": opID})
			if vector.ownerPayload != "" {
				payload = []byte(vector.ownerPayload)
			}
			if _, err := s.DatabaseForTesting().Exec(`UPDATE durable_operations SET result_payload=?,changed_refs=? WHERE op_id=?`, string(payload), string(refs), opID); err != nil {
				t.Fatal(err)
			}
			if vector.ownerDigest != "" {
				if _, err := s.DatabaseForTesting().Exec(`UPDATE durable_operations SET contract_digest=? WHERE op_id=?`, vector.ownerDigest, opID); err != nil {
					t.Fatal(err)
				}
			}
			if vector.missing {
				if _, err := s.DatabaseForTesting().Exec(`DELETE FROM durable_operations WHERE op_id=?`, opID); err != nil {
					t.Fatal(err)
				}
			}
			if vector.missingPayload {
				if _, err := s.DatabaseForTesting().Exec(`UPDATE idempotency_records SET result_payload='{}',changed_refs='[]' WHERE op_id=?`, opID); err != nil {
					t.Fatal(err)
				}
			}
			if vector.scope != "" {
				if _, err := s.DatabaseForTesting().Exec(`UPDATE idempotency_records SET authorized_scope_snapshot=? WHERE op_id=?`, vector.scope, opID); err != nil {
					t.Fatal(err)
				}
			}
			before := workflowReplayEffectCounts(t, s)
			beforeCommits := s.DurableCommits()
			public, err := Dispatch(context.Background(), s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: raw}, env)
			if err != nil {
				t.Fatal(err)
			}
			r := runtime{Store: s, Authority: service, Envelope: env, Tool: "concord_work_transition", Operation: "workflow_action"}
			transaction := workflowTransactionReplay(t, r, grant, raw)
			if transaction.Outcome != vector.want || transaction.Outcome != public.Outcome {
				t.Fatalf("transaction=%+v public=%+v want=%s", transaction, public, vector.want)
			}
			if vector.want == OutcomeOK && string(transaction.Result) != string(public.Result) {
				t.Fatalf("transaction result=%s public=%s", transaction.Result, public.Result)
			}
			if !sameWorkflowReplayError(transaction.Error, public.Error) || !reflect.DeepEqual(transaction.OperationRef, public.OperationRef) {
				t.Fatalf("classification mismatch: transaction=%+v public=%+v", transaction, public)
			}
			if err := transaction.Validate(); err != nil {
				t.Fatalf("transaction envelope: %v", err)
			}
			if workflowReplayEffectCounts(t, s) != before {
				t.Fatal("replay changed events, operations, attempts, dispatches or work version")
			}
			if vector.want == OutcomeError && (replayMetadataCount(t, s, "owner-classification") != 0 || s.DurableCommits() != beforeCommits) {
				t.Fatal("refused replay committed metadata or a durable write")
			}
			if vector.missingPayload || vector.name == "missing response metadata" {
				if transaction.Error == nil || transaction.Error.EffectState != EffectPossible || transaction.Error.RetrySafe {
					t.Fatalf("incomplete committed response lost effect truth: %+v", transaction.Error)
				}
			}
		})
	}
}

func sameWorkflowReplayError(left, right *TypedError) bool {
	if left == nil || right == nil {
		return left == right
	}
	return left.Kind == right.Kind && left.RetrySafe == right.RetrySafe && left.EffectState == right.EffectState && reflect.DeepEqual(left.RecoveryAction, right.RecoveryAction)
}

func workflowTransactionReplay(t *testing.T, r runtime, grant Authority, raw json.RawMessage) Envelope {
	t.Helper()
	var in actionMutationInput
	if err := json.Unmarshal(raw, &in); err != nil {
		t.Fatal(err)
	}
	base := NewBase(r.Envelope.RequestID, r.Tool, r.Operation)
	var response Envelope
	var rejected, mutated bool
	err := store.AuthorizeWorkflowActionAtBoundaryWithPreflightTx(context.Background(), r.Store, nil, store.WorkflowActionPreflightRequest{WorkID: in.WorkID, ExpectedVersion: in.ExpectedVersion, ActionID: in.ActionID}, r.workflowActionReplayPreflight(context.Background(), base, mutationDigest(r.Tool, r.Operation, r.Envelope, raw), grant, in, &response, &rejected), nil, func(*store.Transaction) error {
		mutated = true
		return nil
	})
	if mutated {
		t.Fatal("transaction replay reached the business mutation")
	}
	if err != nil && !rejected {
		return failureEnvelope(base, err)
	}
	return response
}

func TestIdempotencyAuthorityLifetimeReplay(t *testing.T) {
	t.Parallel()
	for _, age := range []time.Duration{time.Hour, 30 * 24 * time.Hour, 10 * 365 * 24 * time.Hour} {
		t.Run(age.String(), func(t *testing.T) {
			r, grant, raw, first := committedWorkflowReplay(t)
			stamp := fixedTime().Add(-age).Format(time.RFC3339Nano)
			if _, err := r.Store.DatabaseForTesting().Exec(`UPDATE idempotency_records SET first_observed_at=?,last_observed_at=? WHERE idempotency_key='retained-replay'`, stamp, stamp); err != nil {
				t.Fatal(err)
			}
			assertWorkflowReplayIdentity(t, r, grant, raw, first)
			path := r.Store.Path()
			if err := r.Store.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := store.Open(context.Background(), path)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			service := NewService(reopened)
			service.Now = fixedTime
			service.ProjectHostProber = r.Authority.ProjectHostProber
			service.ProjectHostMatcher = r.Authority.ProjectHostMatcher
			r.Store, r.Authority = reopened, service
			assertWorkflowReplayIdentity(t, r, grant, raw, first)
		})
	}
}

func TestIdempotencyDeduplicationBackupRestore(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	r, grant, raw, first := committedWorkflowReplay(t)
	ownerID := "workflow-" + mutationDigest(r.Tool, r.Operation, r.Envelope, raw)[7:31]
	ownerBefore, err := store.Step(ctx, r.Store, ownerID)
	if err != nil {
		t.Fatal(err)
	}
	indexBefore := workflowReplayIndex(t, r.Store)
	var watermark int64
	if err := r.Store.DatabaseForTesting().QueryRow(`SELECT revision FROM store_revision WHERE id=1`).Scan(&watermark); err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(t.TempDir(), "backup.db")
	if _, err := store.Backup(ctx, r.Store, backup); err != nil {
		t.Fatal(err)
	}
	restoredPath := filepath.Join(t.TempDir(), "restored.db")
	restoreReport, err := store.RestoreBackup(ctx, backup, restoredPath)
	if err != nil {
		t.Fatal(err)
	}
	if restoreReport.SourceSnapshot.EventWatermark != watermark || restoreReport.RestoredDatabase.EventWatermark != watermark || restoreReport.RestoredDatabase.SchemaVersion != store.CurrentSchemaVersion() {
		t.Fatalf("restore inventories do not preserve the store revision: %+v", restoreReport)
	}
	restored, err := store.Open(ctx, restoredPath)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	service := NewService(restored)
	service.Now = fixedTime
	service.ProjectHostProber = r.Authority.ProjectHostProber
	service.ProjectHostMatcher = r.Authority.ProjectHostMatcher
	r.Store, r.Authority = restored, service
	if workflowReplayIndex(t, restored) != indexBefore {
		t.Fatal("backup or restore changed idempotency index")
	}
	ownerAfter, err := store.Step(ctx, restored, ownerID)
	if err != nil || !reflect.DeepEqual(ownerBefore, ownerAfter) {
		t.Fatalf("durable owner changed: before=%+v after=%+v err=%v", ownerBefore, ownerAfter, err)
	}
	assertWorkflowReplayIdentity(t, r, grant, raw, first)
}

func committedWorkflowReplay(t *testing.T) (runtime, Authority, json.RawMessage, Envelope) {
	t.Helper()
	s, service, grant, _ := mutationDispatchFixture(t, []agentcontract.Capability{"work_transition"})
	ctx := context.Background()
	if _, err := pm1fixture.SeedCommittedProductDomain(ctx, s, "product-1", "project-1", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	definition, err := store.BuiltinWorkflowRegistry().Register(store.BuiltinWorkflowDefinitions()[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Transact(ctx, func(tx *store.Transaction) error {
		return store.InitializeWorkflowTx(ctx, tx, store.WorkflowInitializationRequest{WorkID: "work-1", Definition: definition, Actor: store.WorkflowActor{PrincipalRef: grant.PrincipalRef, ClientRef: grant.ClientRef, AgentRef: grant.AgentRef, SessionRef: grant.SessionRef, ActorClass: store.ActorAgent}, Now: fixedTime()})
	}); err != nil {
		t.Fatal(err)
	}
	version := workOneVersion(t, s)
	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	raw := json.RawMessage(`{"work_id":"work-1","expected_version":` + strconv.FormatInt(version, 10) + `,"action_id":"record_proposal","fields":{"problem":"The bounded problem statement.","affected":["The affected system."],"stakes":"The bounded stakes statement.","user_outcomes":["The expected user outcome."]},"idempotency_key":"retained-replay"}`)
	first, err := Dispatch(context.Background(), s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: raw}, env)
	if err != nil || first.Outcome != OutcomeOK {
		t.Fatalf("initial committed action=%+v err=%v", first, err)
	}
	return runtime{Store: s, Authority: service, Envelope: env, Tool: "concord_work_transition", Operation: "workflow_action"}, grant, raw, first
}

func assertWorkflowReplayIdentity(t *testing.T, r runtime, grant Authority, raw json.RawMessage, first Envelope) {
	t.Helper()
	before := workflowReplayEffectCounts(t, r.Store)
	replay, err := Dispatch(context.Background(), r.Store, r.Authority, InvokeRequest{Tool: r.Tool, Operation: r.Operation, Input: raw}, r.Envelope)
	if err != nil || replay.Outcome != OutcomeOK || !replay.Replayed || string(replay.Result) != string(first.Result) {
		t.Fatalf("read replay=%+v err=%v", replay, err)
	}
	transaction := workflowTransactionReplay(t, r, grant, raw)
	if transaction.Outcome != OutcomeOK || !transaction.Replayed || string(transaction.Result) != string(first.Result) {
		t.Fatalf("transaction replay=%+v", transaction)
	}
	conflicting := json.RawMessage(strings.Replace(string(raw), "The bounded stakes statement.", "A different stakes statement.", 1))
	conflict := workflowTransactionReplay(t, r, grant, conflicting)
	publicConflict, err := Dispatch(context.Background(), r.Store, r.Authority, InvokeRequest{Tool: r.Tool, Operation: r.Operation, Input: conflicting}, r.Envelope)
	if err != nil || conflict.Error == nil || conflict.Error.Kind != "idempotency_conflict" || publicConflict.Error == nil || publicConflict.Error.Kind != "idempotency_conflict" {
		t.Fatalf("digest conflict: transaction=%+v public=%+v err=%v", conflict, publicConflict, err)
	}
	if workflowReplayEffectCounts(t, r.Store) != before {
		t.Fatal("replay or conflicting digest produced another effect")
	}
}

func workflowReplayIndex(t *testing.T, s *store.Store) string {
	t.Helper()
	var row string
	if err := s.DatabaseForTesting().QueryRow(`SELECT json_array(principal_ref,tool,operation_kind,idempotency_key,canonical_digest,op_id,result_event_ids,result_payload,changed_refs,authorized_scope_snapshot,first_observed_at,last_observed_at,replayed_count) FROM idempotency_records WHERE idempotency_key='retained-replay'`).Scan(&row); err != nil {
		t.Fatal(err)
	}
	return row
}

func TestWorkflowActionDispatchUsesStrictPreflightAuthApprovalAndReplayPath(t *testing.T) {
	t.Parallel()
	s, service, grant, _ := mutationDispatchFixture(t, []agentcontract.Capability{"work_transition"})
	version := seedAgentWorkflow(t, s, grant)
	if version != 4 {
		t.Fatalf("workflow seed version=%d, want 4", version)
	}
	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)

	unknown := InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: json.RawMessage(`{"work_id":"work-1","expected_version":4,"action_id":"unknown-action","idempotency_key":"wf-unknown"}`)}
	refused, err := Dispatch(context.Background(), s, service, unknown, env)
	// The closed action-variant surface owns the unknown-action refusal
	// (CON-412): the boundary refuses with a field-named invalid_input
	// naming action_id before any authority read.
	if err != nil || refused.Outcome != OutcomeError || refused.Error == nil || refused.Error.Kind != "invalid_input" || !strings.Contains(refused.Error.Message, "action_id") {
		t.Fatalf("unknown workflow action response=%+v err=%v", refused, err)
	}
	var completed int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM effect_receipts WHERE kind=?`, store.WorkflowActionCompleted).Scan(&completed); err != nil {
		t.Fatal(err)
	}
	if completed != 0 {
		t.Fatalf("malformed/unknown request reached authority: completed=%d", completed)
	}
	duplicate := InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: json.RawMessage(`{"work_id":"work-1","expected_version":4,"action_id":"record_proposal","action_id":"unknown-action","idempotency_key":"wf-duplicate"}`)}
	duplicateResponse, err := Dispatch(context.Background(), s, service, duplicate, env)
	if err != nil || duplicateResponse.Outcome != OutcomeError || duplicateResponse.Error == nil || duplicateResponse.Error.Kind != "invalid_input" {
		t.Fatalf("duplicate JSON response=%+v err=%v", duplicateResponse, err)
	}

	request := InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: json.RawMessage(`{"work_id":"work-1","expected_version":4,"action_id":"record_proposal","fields":{"problem":"The bounded problem statement.","affected":["The affected system."],"stakes":"The bounded stakes statement.","user_outcomes":["The expected user outcome."]},"idempotency_key":"wf-record-proposal"}`)}
	first, err := Dispatch(context.Background(), s, service, request, env)
	if err != nil || first.Outcome != OutcomeOK || first.Error != nil || len(*first.ChangedRefs) != 1 {
		t.Fatalf("workflow action response=%+v err=%v", first, err)
	}
	if (*first.ChangedRefs)[0].Version != "6" {
		t.Fatalf("workflow action changed version=%s, want 6", (*first.ChangedRefs)[0].Version)
	}
	var mutationPayload struct {
		WorkPins []store.WorkPin `json:"work_pins"`
	}
	if err := json.Unmarshal(first.Result, &mutationPayload); err != nil {
		t.Fatal(err)
	}
	if len(mutationPayload.WorkPins) != 1 || mutationPayload.WorkPins[0].Version != 6 || mutationPayload.WorkPins[0].WorkID != "work-1" {
		t.Fatalf("mutation work pins=%+v, want post-state pin", mutationPayload.WorkPins)
	}
	if len(*first.NextValidIntents) == 0 || (*first.NextValidIntents)[0].ActionID == "" || (*first.NextValidIntents)[0].ExpectedVersion != 6 {
		t.Fatalf("mutation intents=%+v, want derived action intents", *first.NextValidIntents)
	}
	var operations, records int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM durable_operations WHERE op_id LIKE 'workflow-%'`).Scan(&operations); err != nil {
		t.Fatal(err)
	}
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM idempotency_records WHERE operation_kind='workflow_action'`).Scan(&records); err != nil {
		t.Fatal(err)
	}
	if operations != 1 || records != 1 {
		t.Fatalf("workflow durable state operations=%d idempotency=%d", operations, records)
	}
	var contractVersion string
	if err := s.DatabaseForTesting().QueryRow(`SELECT contract_digest FROM durable_operations WHERE op_id LIKE 'workflow-%'`).Scan(&contractVersion); err != nil {
		t.Fatal(err)
	}
	if contractVersion != agentcontract.ManifestDigest {
		t.Fatalf("workflow durable contract digest=%s, want %s", contractVersion, agentcontract.ManifestDigest)
	}

	replay, err := Dispatch(context.Background(), s, service, request, env)
	if err != nil || replay.Outcome != OutcomeOK || !replay.Replayed || len(*replay.ChangedRefs) != 1 {
		if replay.Error != nil {
			t.Fatalf("workflow replay response=%+v error=%+v err=%v", replay, *replay.Error, err)
		}
		t.Fatalf("workflow replay response=%+v err=%v", replay, err)
	}
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM effect_receipts WHERE kind=?`, store.WorkflowActionCompleted).Scan(&completed); err != nil {
		t.Fatal(err)
	}
	if completed != 1 {
		t.Fatalf("replay emitted duplicate action events=%d", completed)
	}
}

func TestWorkflowActionAvailabilityPrecedesPayloadAndAuthorityValidation(t *testing.T) {
	t.Parallel()
	s, service, grant, _ := mutationDispatchFixture(t, []agentcontract.Capability{"work_transition"})
	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	request := InvokeRequest{
		Tool:      "concord_work_transition",
		Operation: "workflow_action",
		// The payload is intentionally incomplete. Strict boundary validation
		// must be reported before pinned-instance or grant validation.
		Input: json.RawMessage(`{"work_id":"missing-work","fields":{}}`),
	}
	response, dispatchErr := Dispatch(context.Background(), s, service, request, env)
	if dispatchErr != nil || response.Outcome != OutcomeError || response.Error == nil || response.Error.Kind != "invalid_input" {
		t.Fatalf("malformed workflow request response=%+v err=%v", response, dispatchErr)
	}
}

func TestWorkflowActionReplayVectorsUseInvokeAndAuthoritativeDurableResults(t *testing.T) {
	t.Parallel()
	for _, vector := range []struct {
		name           string
		resultKind     string
		want           Outcome
		operationState OperationState
		retrySafe      bool
	}{
		{name: "current success", resultKind: "completed", want: OutcomeOK},
		{name: "current pending", resultKind: "pending", want: OutcomePending, operationState: OperationPending},
		{name: "partial", resultKind: "partial", want: OutcomePartial, operationState: OperationPartial, retrySafe: true},
		{name: "current failure", resultKind: "failed", want: OutcomePartial, operationState: OperationFailed},
		{name: "failed stale", resultKind: "failed_stale", want: OutcomePartial, operationState: OperationFailed},
	} {
		t.Run(vector.name, func(t *testing.T) {
			s, service, grant, _ := mutationDispatchFixture(t, []agentcontract.Capability{"work_transition"})
			if got := seedAgentWorkflow(t, s, grant); got != 4 {
				t.Fatalf("workflow seed version=%d, want 4", got)
			}
			scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
			if err != nil {
				t.Fatal(err)
			}
			env := mutationEnvelope(grant, scopeVersion)
			input := json.RawMessage(`{"work_id":"work-1","expected_version":4,"action_id":"record_proposal","fields":{"problem":"The bounded problem statement.","affected":["The affected system."],"stakes":"The bounded stakes statement.","user_outcomes":["The expected user outcome."]},"idempotency_key":"legacy-replay-` + strings.ReplaceAll(vector.name, " ", "-") + `"}`)
			opID := seedCurrentWorkflowActionReplay(t, s, env, input, vector.resultKind)
			beforeEvents := countWorkflowEvents(t, s)
			beforeVersion := workOneVersion(t, s)
			beforeChallenges := countWorkflowApprovalChallenges(t, s)

			data, marshalErr := json.Marshal(struct {
				CallEnvelope CallEnvelope    `json:"call_envelope"`
				Tool         string          `json:"tool"`
				Operation    string          `json:"operation"`
				Input        json.RawMessage `json:"input"`
			}{CallEnvelope: env, Tool: "concord_work_transition", Operation: "workflow_action", Input: input})
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			response, invokeErr := Invoke(context.Background(), s, service, data)
			if invokeErr != nil {
				t.Fatal(invokeErr)
			}
			if response.Outcome != vector.want || !response.Replayed || (vector.want != OutcomeOK && response.OperationRef == nil) {
				if response.Error != nil {
					t.Fatalf("%s replay outcome=%s replayed=%t error=%s message=%s", vector.name, response.Outcome, response.Replayed, response.Error.Kind, response.Error.Message)
				}
				t.Fatalf("%s replay response=%+v", vector.name, response)
			}
			if vector.want == OutcomeOK && response.Error != nil {
				t.Fatalf("successful replay returned error=%+v", response.Error)
			}
			if vector.want == OutcomePending && (response.OperationRef == nil || response.OperationRef.State != OperationPending) {
				t.Fatalf("pending replay operation_ref=%+v", response.OperationRef)
			}
			if vector.want == OutcomePartial && (response.OperationRef == nil || response.OperationRef.State != vector.operationState || response.Error == nil || response.Error.Kind != "operation_conflict" || response.Error.RetrySafe != vector.retrySafe || response.Error.RecoveryAction.Kind != "reconcile_operation") {
				t.Fatalf("failed replay operation_ref=%+v error=%+v", response.OperationRef, response.Error)
			}
			if err := response.Validate(); err != nil {
				t.Fatalf("replay envelope validation: %v", err)
			}
			var durableResultKind string
			if err := s.DatabaseForTesting().QueryRow(`SELECT result_kind FROM durable_operations WHERE op_id=?`, opID).Scan(&durableResultKind); err != nil {
				t.Fatal(err)
			}
			if durableResultKind != vector.resultKind {
				t.Fatalf("replay changed durable result classification from %q to %q", vector.resultKind, durableResultKind)
			}
			if got := countWorkflowEvents(t, s); got != beforeEvents {
				t.Fatalf("replay changed event count from %d to %d (op=%s)", beforeEvents, got, opID)
			}
			if got := workOneVersion(t, s); got != beforeVersion {
				t.Fatalf("replay changed work version from %d to %d", beforeVersion, got)
			}
			if got := countWorkflowApprovalChallenges(t, s); got != beforeChallenges {
				t.Fatalf("replay changed approval challenges from %d to %d", beforeChallenges, got)
			}
		})
	}

}

func TestWorkflowActionRejectsLegacyEventReplay(t *testing.T) {
	t.Parallel()
	s, service, grant, _ := mutationDispatchFixture(t, []agentcontract.Capability{"work_transition"})
	if got := seedAgentWorkflow(t, s, grant); got != 4 {
		t.Fatalf("workflow seed version=%d, want 4", got)
	}
	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	response, err := Dispatch(context.Background(), s, service, InvokeRequest{
		Tool: "concord_work_transition", Operation: "workflow_action",
		Input: json.RawMessage(`{"work_id":"work-1","expected_version":4,"action_id":"replay","fields":{},"idempotency_key":"legacy-event-replay"}`),
	}, mutationEnvelope(grant, scopeVersion))
	if err != nil {
		t.Fatal(err)
	}
	// The closed action-variant surface owns this refusal (CON-412): a
	// removed action id never passes the boundary schema, so the refusal is
	// the field-named invalid_input naming action_id, not a store transition
	// refusal reached through a permissive union.
	if response.Outcome != OutcomeError || response.Error == nil || response.Error.Kind != "invalid_input" || !strings.Contains(response.Error.Message, "action_id") {
		t.Fatalf("legacy event replay response=%+v", response)
	}
}

func TestWorkflowActionReplayRejectsOldSurfaceResultShapeAndDigest(t *testing.T) {
	t.Parallel()
	s, service, grant, _ := mutationDispatchFixture(t, []agentcontract.Capability{"work_transition"})
	if got := seedAgentWorkflow(t, s, grant); got != 4 {
		t.Fatalf("workflow seed version=%d, want 4", got)
	}
	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	input := json.RawMessage(`{"work_id":"work-1","expected_version":4,"action_id":"record_proposal","fields":{"problem":"The bounded problem statement.","affected":["The affected system."],"stakes":"The bounded stakes statement.","user_outcomes":["The expected user outcome."]},"idempotency_key":"current-replay-shape"}`)
	opID := seedCurrentWorkflowActionReplay(t, s, env, input, "completed")
	if _, err := s.DatabaseForTesting().Exec(`UPDATE durable_operations SET result_payload=? WHERE op_id=?`, `{"changed_refs":["work-1"],"next_valid_intents":[],"operation_id":"`+opID+`"}`, opID); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(struct {
		CallEnvelope CallEnvelope    `json:"call_envelope"`
		Tool         string          `json:"tool"`
		Operation    string          `json:"operation"`
		Input        json.RawMessage `json:"input"`
	}{CallEnvelope: env, Tool: "concord_work_transition", Operation: "workflow_action", Input: input})
	if err != nil {
		t.Fatal(err)
	}
	response, err := Invoke(context.Background(), s, service, data)
	if err != nil {
		t.Fatal(err)
	}
	if response.Outcome != OutcomeError || response.Error == nil || response.Error.Kind != "malformed_response" {
		t.Fatalf("old result shape replay response=%+v", response)
	}
}

func seedCurrentWorkflowActionReplay(t *testing.T, s *store.Store, env CallEnvelope, input json.RawMessage, resultKind string) string {
	t.Helper()
	digest := mutationDigest("concord_work_transition", "workflow_action", env, input)
	opID := "workflow-" + digest[7:31]
	version := workOneVersion(t, s)
	changedRef := `{"entity_kind":"work_item","id":"work-1","version":` + strconv.FormatInt(version, 10) + `}`
	resultPayload := `{"changed_refs":[` + changedRef + `],"next_valid_intents":[],"operation_id":"` + opID + `"}`
	changedRefs := `[` + strconv.Quote(changedRef) + `]`
	scope := `{"product_id":"product-1","project_ids":["project-1"],"work_ids":["work-1"],"scope_version":"` + env.ScopeVersion + `"}`
	definition, defErr := store.LiveWorkflowDefinitionForRef("workflow.implementation")
	if defErr != nil {
		t.Fatal(defErr)
	}
	_, err := s.DatabaseForTesting().Exec(`INSERT INTO durable_operations(op_id,attempt_epoch,work_id,workflow_type_ref,workflow_type_digest,step_id,step_kind,accepted_inputs_digest,accepted_scope_snapshot,result_kind,result_payload,evidence_refs,changed_refs,principal_ref,request_id,observed_at,completed_at,contract_digest) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, opID, 1, "work-1", definition.Definition.Ref, definition.Digest, "proposal", "internal_sqlite", "sha256:legacy-input", scope, "completed", resultPayload, "[]", changedRefs, env.PrincipalRef, env.RequestID, fixedTime().Format(time.RFC3339Nano), fixedTime().Format(time.RFC3339Nano), agentcontract.ManifestDigest)
	if err != nil {
		t.Fatal(err)
	}
	if resultKind != "completed" {
		if _, err := s.DatabaseForTesting().Exec(`UPDATE durable_operations SET result_kind=? WHERE op_id=?`, resultKind, opID); err != nil {
			if resultKind == "future_result" {
				if _, pragmaErr := s.DatabaseForTesting().Exec(`PRAGMA ignore_check_constraints=ON`); pragmaErr != nil {
					t.Fatal(pragmaErr)
				}
				if _, updateErr := s.DatabaseForTesting().Exec(`UPDATE durable_operations SET result_kind=? WHERE op_id=?`, resultKind, opID); updateErr != nil {
					t.Fatal(updateErr)
				}
			} else {
				t.Fatal(err)
			}
		}
	}
	err = s.Transact(context.Background(), func(tx *store.Transaction) error {
		record := store.MutationIdempotencyInsert{Key: store.MutationIdempotencyKey{PrincipalRef: env.PrincipalRef, Tool: "concord_work_transition", OperationKind: "workflow_action", IdempotencyKey: idempotencyKey(input)}, CanonicalDigest: digest, OperationID: opID, ResultEventIDs: "[]", ResultPayload: "{}", ChangedRefs: "[]", AuthorizedScopeSnapshot: scope, ObservedAt: fixedTime()}
		if resultKind == "completed" {
			r := runtime{Store: s, Tool: "concord_work_transition", Operation: "workflow_action"}
			changed := []ChangedRef{{EntityKind: "work_item", ID: "work-1", Version: strconv.FormatInt(version, 10)}}
			payload, intents, pinless, err := r.enrichMutationPayloadTx(context.Background(), tx, json.RawMessage(resultPayload), changed)
			if err != nil {
				return err
			}
			var originalScope map[string]any
			if err := json.Unmarshal([]byte(scope), &originalScope); err != nil {
				return err
			}
			base := NewBase(env.RequestID, r.Tool, r.Operation)
			base.ResolvedScope = scopeFromMap(originalScope)
			base.Omissions = append(base.Omissions, mutationPinOmissions(pinless)...)
			response := r.mutationResult(base, payload, changed, intents, pinless...)
			if response.Outcome != OutcomeOK {
				t.Fatalf("completed response fixture: %+v", response.Error)
			}
			metadata, err := json.Marshal(mutationResponseScope(originalScope, response, pinless, false))
			if err != nil {
				return err
			}
			refs, err := json.Marshal(changed)
			if err != nil {
				return err
			}
			record.ResultPayload, record.ChangedRefs, record.AuthorizedScopeSnapshot = string(payload), string(refs), string(metadata)
		}
		return store.InsertMutationIdempotencyTx(context.Background(), tx, record)
	})
	if err != nil {
		t.Fatal(err)
	}
	return opID
}

func countWorkflowEvents(t *testing.T, s *store.Store) int {
	t.Helper()
	var count int
	if err := s.DatabaseForTesting().QueryRow(`SELECT last FROM store_sequences WHERE name='effect'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func countWorkflowApprovalChallenges(t *testing.T, s *store.Store) int {
	t.Helper()
	var count int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM agent_approval_challenges`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestWorkflowActionDispatchUsesDefinitionApprovalChallenge(t *testing.T) {
	t.Parallel()
	s, service, grant, privateKey := mutationDispatchFixture(t, []agentcontract.Capability{"work_transition"})
	if got := seedAgentWorkflow(t, s, grant); got != 4 {
		t.Fatalf("workflow seed version=%d, want 4", got)
	}
	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	// Proposal and alignment establish the criteria approval context.
	for i, action := range []string{"record_proposal", "record_alignment"} {
		fields := ""
		if action == "record_proposal" {
			fields = `,"fields":{"problem":"The bounded problem statement.","affected":["The affected system."],"stakes":"The bounded stakes statement.","user_outcomes":["The expected user outcome."]}`
		}
		if action == "record_alignment" {
			fields = `,"fields":{"searched":"The bounded backlog search statement.","outcome":"none_found"}`
		}
		// Cumulative expected versions: proposal and alignment each
		// consume two events over the version-4 capture.
		expectedVersion := []int{4, 6}[i]
		input := json.RawMessage(`{"work_id":"work-1","expected_version":` + strconv.Itoa(expectedVersion) + `,"action_id":"` + action + `"` + fields + `,"idempotency_key":"wf-advance-` + action + `"}`)
		response, dispatchErr := Dispatch(context.Background(), s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: input}, env)
		if dispatchErr != nil || response.Outcome != OutcomeOK {
			t.Fatalf("advance action=%s response=%+v err=%v", action, response, dispatchErr)
		}
	}
	input := workflowContractActionInput(t, "wf-approve-contract", "")
	challenge, err := Dispatch(context.Background(), s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: input}, env)
	if err != nil || challenge.Outcome != OutcomeError || challenge.Error == nil || challenge.Error.Kind != "approval_required" {
		t.Fatalf("approval challenge response=%+v err=%v", challenge, err)
	}
	challengeRef, _ := challenge.Error.Details["approval_ref"].(string)
	digest := mutationDigest("concord_work_transition", "workflow_action", env, input)
	scope := map[string]any{"product_id": "product-1", "project_ids": []string{"project-1"}, "work_ids": []string{"work-1"}, "scope_version": scopeVersion}
	versions := map[string]any{"work": 8}
	env.HostApproval = signedHostApproval(privateKey, challengeRef, digest, scope, versions, grant.SessionRef, grant.AgentRef, grant.Worktree, fixedTime(), "workflow-approval-0001")
	var durableBefore int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM durable_operations WHERE workflow_type_ref LIKE 'workflow.%'`).Scan(&durableBefore); err != nil {
		t.Fatal(err)
	}
	if durableBefore != 2 {
		t.Fatalf("approval challenge durable operation count=%d, want 2 prior actions", durableBefore)
	}
	approvedInput := workflowContractActionInput(t, "wf-approve-contract", challengeRef)
	approved, err := Dispatch(context.Background(), s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: approvedInput}, env)
	if err != nil || approved.Outcome != OutcomeOK {
		if approved.Error != nil {
			t.Fatalf("approved workflow action response=%+v error=%+v err=%v", approved, *approved.Error, err)
		}
		t.Fatalf("approved workflow action response=%+v err=%v", approved, err)
	}
	var step string
	if err := s.DatabaseForTesting().QueryRow(`SELECT current_step FROM workflow_instances WHERE work_id='work-1'`).Scan(&step); err != nil {
		t.Fatal(err)
	}
	if step != "design" {
		t.Fatalf("approved action current step=%s, want design", step)
	}
	workflowRead, err := s.QueryQ7(context.Background(), store.Q7Request{Work: "work-1"})
	if err != nil {
		t.Fatal(err)
	}
	projection := workflowRead.Workflow
	if projection == nil {
		t.Fatal("Q7 omitted the recorded workflow")
	}
	if projection.ArchitectureBinding == nil || projection.ArchitectureBinding.DomainRegistryContentHash != pm1fixture.FixtureDomainRegistryContentHash || projection.ArchitectureBinding.HomeDomainID != pm1fixture.FixtureRootDomainID {
		t.Fatalf("agent approval did not persist readable architecture binding: %+v", projection.ArchitectureBinding)
	}
}

// TestWorkflowLateHostApprovalNeedsOneChallenge walks the approval-gated
// workflow path through the pending-challenge boundary: the operator's
// affirmative answer arrives after the ten-minute mint deadline, the core
// re-arms the expired unused invitation for its original interval, and the
// same approved action advances without a second challenge or second ask.
func TestWorkflowLateHostApprovalNeedsOneChallenge(t *testing.T) {
	t.Parallel()
	s, service, grant, _ := mutationDispatchFixture(t, []agentcontract.Capability{"work_transition"})
	if got := seedAgentWorkflow(t, s, grant); got != 4 {
		t.Fatalf("workflow seed version=%d, want 4", got)
	}
	ctx := context.Background()
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	// The pre-contract walk is proposal and alignment: two
	// events each over the version-4 capture, so the approval lands at
	// expected version 8.
	for i, action := range []string{"record_proposal", "record_alignment"} {
		fields := ""
		if action == "record_proposal" {
			fields = `,"fields":{"problem":"The bounded problem statement.","affected":["The affected system."],"stakes":"The bounded stakes statement.","user_outcomes":["The expected user outcome."]}`
		}
		if action == "record_alignment" {
			fields = `,"fields":{"searched":"The bounded backlog search statement.","outcome":"none_found"}`
		}
		expectedVersion := []int{4, 6}[i]
		input := json.RawMessage(`{"work_id":"work-1","expected_version":` + strconv.Itoa(expectedVersion) + `,"action_id":"` + action + `"` + fields + `,"idempotency_key":"wf-late-` + action + `"}`)
		response, dispatchErr := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: input}, env)
		if dispatchErr != nil || response.Outcome != OutcomeOK {
			t.Fatalf("advance action=%s response=%+v err=%v", action, response, dispatchErr)
		}
	}
	input := workflowContractActionInput(t, "wf-late-approval", "")
	challenge, err := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: input}, env)
	if err != nil || challenge.Outcome != OutcomeError || challenge.Error == nil || challenge.Error.Kind != "approval_required" {
		t.Fatalf("approval challenge response=%+v err=%v", challenge, err)
	}
	summary := challenge.Error.ConsequenceSummary
	ref, _ := challenge.Error.Details["approval_ref"].(string)
	if summary == nil || len(ref) != 64 {
		t.Fatalf("workflow challenge lost its exact bindings: %+v", challenge.Error)
	}
	// The prompt stays open past the ten-minute mint deadline while the
	// answer is pending; the operator's answer is fresh.
	answerAt := fixedTime().Add(11 * time.Minute)
	service.Now = func() time.Time { return answerAt }
	env.HostApproval = &HostApprovalAssertion{ChallengeRef: ref, RequestDigest: summary.OperationDigest, Scope: summary.Scope, Versions: summary.Versions, SessionRef: env.SessionRef, AgentRef: env.AgentRef, Worktree: env.Worktree, IssuedAt: answerAt.Format(time.RFC3339Nano)}
	approved, err := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: workflowContractActionInput(t, "wf-late-approval", ref)}, env)
	if err != nil || approved.Outcome != OutcomeOK {
		if approved.Error != nil {
			t.Fatalf("late-approved workflow action response=%+v error=%+v err=%v", approved, *approved.Error, err)
		}
		t.Fatalf("late-approved workflow action response=%+v err=%v", approved, err)
	}
	if countWorkflowApprovalChallenges(t, s) != 1 {
		t.Fatal("late workflow answer minted a second challenge")
	}
	var status string
	var used int
	var issuedAt, expiresAt string
	if err := s.DatabaseForTesting().QueryRow(`SELECT status, used_count, issued_at, expires_at FROM agent_approval_challenges WHERE challenge_ref=?`, ref).Scan(&status, &used, &issuedAt, &expiresAt); err != nil {
		t.Fatal(err)
	}
	if status != "consumed" || used != 1 || issuedAt != fixedTime().Format(time.RFC3339Nano) || expiresAt != answerAt.Add(10*time.Minute).Format(time.RFC3339Nano) {
		t.Fatalf("late workflow exchange challenge state status=%s used=%d issued=%s expires=%s", status, used, issuedAt, expiresAt)
	}
	if countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM agent_approvals WHERE used_count=1`) != 1 {
		t.Fatal("late workflow exchange did not consume exactly one derived approval")
	}
	var step string
	if err := s.DatabaseForTesting().QueryRow(`SELECT current_step FROM workflow_instances WHERE work_id='work-1'`).Scan(&step); err != nil {
		t.Fatal(err)
	}
	if step != "design" {
		t.Fatalf("late-approved action current step=%s, want design", step)
	}
	replayed, err := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: workflowContractActionInput(t, "wf-late-approval", ref)}, env)
	if err != nil || replayed.Outcome != OutcomeOK || !replayed.Replayed {
		t.Fatalf("late workflow replay response=%+v err=%v", replayed, err)
	}
	if countWorkflowApprovalChallenges(t, s) != 1 {
		t.Fatal("late workflow replay minted a challenge")
	}
}

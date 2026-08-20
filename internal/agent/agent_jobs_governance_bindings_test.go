package agent

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

// This file binds the two AJ8 scenarios whose Concord-visible behaviour is a
// refusal rather than an execution. Both refuse before anything starts, and in
// both the interesting half is what the refusal tells the caller: what is being
// consented to, and what budget would actually work. The accepted mutation
// contract keeps native execution outside Concord, so neither scenario builds a
// credential-rotation or audit driver.

// opsRunbookDefinition returns the accepted ops-runbook definition by reference
// rather than by index, so reordering the builtin list cannot silently rebind
// these scenarios to a different workflow.
func opsRunbookDefinition(t *testing.T) store.WorkflowDefinition {
	t.Helper()
	for _, definition := range store.BuiltinWorkflowDefinitions() {
		if definition.Ref == "workflow.ops_runbook" {
			return definition
		}
	}
	t.Fatal("builtin definitions do not contain workflow.ops_runbook")
	return store.WorkflowDefinition{}
}

// seedOpsRunbookAtApprovalGate drives a fresh ops-runbook instance through its
// first human checkpoint so the next action is `approve_operation` on the
// `approval` step — the consent gate the credential-rotation scenario
// describes. It returns the work version the next action must expect and the
// client key that can sign a consent, so a binding may either stop at the gate
// or consume it.
func seedOpsRunbookAtApprovalGate(t *testing.T) (*store.Store, *Service, CallEnvelope, int64, ed25519.PrivateKey) {
	t.Helper()
	ctx := context.Background()
	s, service, grant, privateKey := mutationDispatchFixture(t, []Capability{"work_transition"})
	seedCurrentWorkflowDomainFixture(t, s)

	registered, err := store.BuiltinWorkflowRegistry().Register(opsRunbookDefinition(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Transact(ctx, func(tx *store.Transaction) error {
		return store.InitializeWorkflowTx(ctx, tx, store.WorkflowInitializationRequest{
			WorkID:     "work-1",
			Definition: registered,
			Actor:      store.WorkflowActor{PrincipalRef: grant.PrincipalRef, ClientRef: grant.ClientRef, AgentRef: grant.AgentRef, SessionRef: grant.SessionRef, ActorClass: store.ActorAgent},
			Now:        fixedTime(),
		})
	}); err != nil {
		t.Fatal(err)
	}

	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)

	version, err := workVersionForWorkflow(t, s)
	if err != nil {
		t.Fatal(err)
	}
	contractInput := workflowContractActionInput(t, "work-1", version, "ops-approve-contract", "")
	challenge, err := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: contractInput}, env)
	if err != nil || challenge.Error == nil || challenge.Error.Kind != "approval_required" {
		t.Fatalf("ops runbook contract approval challenge response=%+v err=%v", challenge, err)
	}
	challengeRef, _ := challenge.Error.Details["approval_ref"].(string)
	digest := mutationDigest("concord_work_transition", "workflow_action", env, contractInput)
	scope := map[string]any{"product_id": "product-1", "project_ids": []string{"project-1"}, "work_ids": []string{"work-1"}, "scope_version": scopeVersion}
	versions := map[string]any{"work": version}
	env.HostApproval = signedHostApproval(privateKey, challengeRef, digest, scope, versions, grant.SessionRef, grant.AgentRef, grant.Worktree, fixedTime(), "ops-approval-0001")

	approvedInput := workflowContractActionInput(t, "work-1", version, "ops-approve-contract", challengeRef)
	approved, err := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: approvedInput}, env)
	if err != nil || approved.Outcome != OutcomeOK {
		t.Fatalf("ops runbook contract approval response=%+v err=%v", approved, err)
	}
	env.HostApproval = nil

	if step := workflowCurrentStep(t, s, "work-1"); step != "approval" {
		t.Fatalf("ops runbook current step=%q, want approval", step)
	}
	next, err := workVersionForWorkflow(t, s)
	if err != nil {
		t.Fatal(err)
	}
	return s, service, env, next, privateKey
}

// consumeOpsRunbookConsent completes the `approve_operation` checkpoint with a
// real signed consent and returns the work version and scope the next action
// must expect. The signed challenge cycle is the trusted path the launcher
// exercises; nothing here fabricates authority.
func consumeOpsRunbookConsent(t *testing.T, s *store.Store, service *Service, env CallEnvelope, version int64, privateKey ed25519.PrivateKey, scopeVersion string) int64 {
	t.Helper()
	ctx := context.Background()
	input, _ := json.Marshal(map[string]any{
		"work_id":          "work-1",
		"expected_version": version,
		"action_id":        "approve_operation",
		"idempotency_key":  "ops-approve-operation",
	})
	challenge, err := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: input}, env)
	if err != nil || challenge.Error == nil || challenge.Error.Kind != "approval_required" {
		t.Fatalf("approve_operation challenge response=%+v err=%v", challenge, err)
	}
	challengeRef, _ := challenge.Error.Details["approval_ref"].(string)
	digest := mutationDigest("concord_work_transition", "workflow_action", env, input)
	scope := map[string]any{"product_id": "product-1", "project_ids": []string{"project-1"}, "work_ids": []string{"work-1"}, "scope_version": scopeVersion}
	versions := map[string]any{"work": version}
	env.HostApproval = signedHostApproval(privateKey, challengeRef, digest, scope, versions, env.SessionRef, env.AgentRef, env.Worktree, fixedTime(), "ops-approval-0002")

	approvedInput, _ := json.Marshal(map[string]any{
		"work_id":          "work-1",
		"expected_version": version,
		"action_id":        "approve_operation",
		"approval":         map[string]any{"approval_ref": challengeRef},
		"idempotency_key":  "ops-approve-operation",
	})
	approved, err := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: approvedInput}, env)
	if err != nil || approved.Outcome != OutcomeOK {
		t.Fatalf("approve_operation consent response=%+v err=%v", approved, err)
	}
	env.HostApproval = nil
	if step := workflowCurrentStep(t, s, "work-1"); step != "execute" {
		t.Fatalf("ops runbook step after consent=%q, want execute", step)
	}
	next, err := workVersionForWorkflow(t, s)
	if err != nil {
		t.Fatal(err)
	}
	return next
}

func workflowCurrentStep(t *testing.T, s *store.Store, workID string) string {
	t.Helper()
	var step string
	if err := s.DatabaseForTesting().QueryRow(`SELECT current_step FROM workflow_instances WHERE work_id=?`, workID).Scan(&step); err != nil {
		t.Fatalf("read current step for %s: %v", workID, err)
	}
	return step
}

func countDurableOperations(t *testing.T, s *store.Store) int {
	t.Helper()
	var count int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM durable_operations`).Scan(&count); err != nil {
		t.Fatalf("count durable operations: %v", err)
	}
	return count
}

// AJ8-approval-required: a consequential production operation is requested
// while the human checkpoint is still pending. Nothing may start, and the
// refusal must state exactly what the operator is being asked to consent to.
// The summary is derived by the core from the facts the challenge already
// binds, so the requester cannot describe its own consequential request.
func bindAJ8ApprovalRequired(t *testing.T, sc jobScenario) jobObservation {
	t.Helper()
	ctx := context.Background()

	if approval, present := sc.InitialState["approval"]; !present || approval != nil {
		t.Fatalf("AJ8-approval-required expects no prior approval, got %+v", sc.InitialState["approval"])
	}
	if kind, _ := sc.Driver["kind"].(string); kind != "human_checkpoint" {
		t.Fatalf("AJ8-approval-required expects a human_checkpoint driver, got %+v", sc.Driver)
	}
	if response, _ := sc.Driver["response"].(string); response != "pending" {
		t.Fatalf("AJ8-approval-required expects a pending checkpoint, got %+v", sc.Driver)
	}

	s, service, env, version, _ := seedOpsRunbookAtApprovalGate(t)

	eventsBefore := countWorkflowEvents(t, s)
	operationsBefore := countDurableOperations(t, s)

	input, _ := json.Marshal(map[string]any{
		"work_id":          "work-1",
		"expected_version": version,
		"action_id":        "approve_operation",
		"idempotency_key":  "aj8-approval-required",
	})
	resp, err := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: input}, env)
	if err != nil {
		t.Fatalf("approve_operation dispatch: %v", err)
	}
	if resp.Outcome != OutcomeError || resp.Error == nil || resp.Error.Kind != "approval_required" {
		t.Fatalf("consequential operation was not gated: outcome=%s err=%+v", resp.Outcome, resp.Error)
	}
	challengeRef, _ := resp.Error.Details["approval_ref"].(string)
	if challengeRef == "" {
		t.Fatal("approval refusal named no challenge")
	}

	summary := resp.Error.ConsequenceSummary
	if summary == nil {
		t.Fatal("approval challenge carried no consequence summary")
	}
	if summary.Tool != "concord_work_transition" || summary.Operation != "workflow_action" {
		t.Fatalf("summary names the wrong operation: %+v", summary)
	}
	if summary.Consequence != "workflow_action" {
		t.Fatalf("summary consequence=%q, want the declared operation consequence", summary.Consequence)
	}
	if summary.OperationDigest != mutationDigest("concord_work_transition", "workflow_action", env, input) {
		t.Fatalf("summary digest does not match the request it describes: %q", summary.OperationDigest)
	}
	if len(summary.Scope) == 0 || summary.ExpiresAt == "" {
		t.Fatalf("summary dropped scope or expiry: %+v", summary)
	}
	// A caller that hides authority inside its own prose must not be able to
	// alter what the operator is told. The summary must satisfy the envelope
	// contract as derived, without any caller-authored field.
	if _, encodeErr := resp.Encode(); encodeErr != nil {
		t.Fatalf("approval refusal does not satisfy the envelope contract: %v", encodeErr)
	}

	// Nothing started. The step never advanced past the consent gate, no
	// durable operation was claimed, and a challenge exists for the operator.
	if step := workflowCurrentStep(t, s, "work-1"); step != "approval" {
		t.Fatalf("a refused consent gate advanced the workflow to %q", step)
	}
	if operationsAfter := countDurableOperations(t, s); operationsAfter != operationsBefore {
		t.Fatalf("a refused consent gate claimed a durable operation: %d -> %d", operationsBefore, operationsAfter)
	}
	if challenges := countWorkflowApprovalChallenges(t, s); challenges == 0 {
		t.Fatal("no approval challenge was minted for a core-gated operation")
	}
	if eventsAfter := countWorkflowEvents(t, s); eventsAfter < eventsBefore {
		t.Fatalf("the refusal dropped history: %d events before, %d after", eventsBefore, eventsAfter)
	}

	obs := envelopeToObservation(resp)
	obs.State = map[string]any{
		"operation": map[string]any{"started": false},
	}
	obs.Communication["consequence_summary"] = summary
	obs.Authority["approval_ref"] = challengeRef
	// Authority was required and not granted: the effect was refused before it
	// existed, and the refusal returned consent to its owner.
	obs.Effects["approval_authority_withheld"] = true
	obs.Effects["atomic_core_effect_zero"] = true
	// Concord never rotates a credential: the accepted mutation contract keeps
	// native execution with the native authority. Probing the prohibited effect
	// means proving the refusal left the external-effect step unreached and
	// appended no work-advancing event.
	obs.Effects["credential_rotated"] = probedAbsent{
		Evidence: "approve_operation was refused with a minted challenge; current_step stayed at approval so the external-effect execute step was never entered, no durable operation was claimed, and no start_run action ran",
	}
	return obs
}

// AJ8-budget-refused: an approved audit asks for more time than the operation
// supports. The core refuses before starting and returns the value that would
// work. Quietly running the request against the lower ceiling is the failure
// mode, because the caller could not then distinguish a truncated audit from a
// complete one.
func bindAJ8BudgetRefused(t *testing.T, sc jobScenario) jobObservation {
	t.Helper()
	ctx := context.Background()

	requestedSeconds, ok := numericInitialState(sc, "requested_budget_seconds")
	if !ok {
		t.Fatal("AJ8-budget-refused: missing requested_budget_seconds")
	}
	supportedSeconds, ok := numericInitialState(sc, "supported_budget_seconds")
	if !ok {
		t.Fatal("AJ8-budget-refused: missing supported_budget_seconds")
	}
	if requestedSeconds <= supportedSeconds {
		t.Fatalf("AJ8-budget-refused expects a request above the ceiling, got %d <= %d", requestedSeconds, supportedSeconds)
	}

	// The corpus fixes the supported value, so the manifest must already agree.
	// A drift here means the declared ceiling moved without scenario evidence.
	action, known := ValidateContractOperation("concord_work_transition", "workflow_action")
	if !known || action.SupportedBudgetSeconds != supportedSeconds {
		t.Fatalf("workflow_action declares %d seconds, corpus fixes %d", action.SupportedBudgetSeconds, supportedSeconds)
	}

	// The audit is approved: the consent gate was genuinely consumed before the
	// run asked for time the operation does not support.
	s, service, env, version, privateKey := seedOpsRunbookAtApprovalGate(t)
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	version = consumeOpsRunbookConsent(t, s, service, env, version, privateKey, scopeVersion)

	eventsBefore := countWorkflowEvents(t, s)
	operationsBefore := countDurableOperations(t, s)

	input, _ := json.Marshal(map[string]any{
		"work_id":                  "work-1",
		"expected_version":         version,
		"action_id":                "start_run",
		"idempotency_key":          "aj8-budget-refused",
		"requested_budget_seconds": requestedSeconds,
	})
	resp, err := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: input}, env)
	if err != nil {
		t.Fatalf("over-ceiling budget dispatch: %v", err)
	}
	if resp.Outcome != OutcomeError || resp.Error == nil || resp.Error.Kind != "budget_refused" {
		t.Fatalf("an unsupported budget was admitted: outcome=%s err=%+v", resp.Outcome, resp.Error)
	}
	if resp.Error.SupportedBudgetSeconds != supportedSeconds {
		t.Fatalf("refusal reported ceiling %d, want %d", resp.Error.SupportedBudgetSeconds, supportedSeconds)
	}
	if resp.Error.RecoveryAction.Kind != "adjust_budget" {
		t.Fatalf("budget refusal recovery=%q, want adjust_budget", resp.Error.RecoveryAction.Kind)
	}
	if resp.Error.EffectState != EffectNone {
		t.Fatalf("a pre-admission refusal claimed effect state %q", resp.Error.EffectState)
	}
	if _, encodeErr := resp.Encode(); encodeErr != nil {
		t.Fatalf("budget refusal does not satisfy the envelope contract: %v", encodeErr)
	}

	// The refusal must be explicit rather than a quiet clamp: nothing ran for
	// the supported duration, so no state moved and no operation was claimed.
	if step := workflowCurrentStep(t, s, "work-1"); step != "execute" {
		t.Fatalf("a refused budget moved the workflow off the execute step to %q", step)
	}
	if operationsAfter := countDurableOperations(t, s); operationsAfter != operationsBefore {
		t.Fatalf("a refused budget claimed a durable operation: %d -> %d", operationsBefore, operationsAfter)
	}
	if eventsAfter := countWorkflowEvents(t, s); eventsAfter != eventsBefore {
		t.Fatalf("a refused budget appended events: %d -> %d", eventsBefore, eventsAfter)
	}

	// Lowering the request to the declared ceiling under the same idempotency
	// key must pass admission, proving the refusal was about the budget alone
	// and that a refused request recorded no idempotency effect.
	retryInput, _ := json.Marshal(map[string]any{
		"work_id":                  "work-1",
		"expected_version":         version,
		"action_id":                "start_run",
		"idempotency_key":          "aj8-budget-refused",
		"requested_budget_seconds": supportedSeconds,
	})
	retry, err := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: retryInput}, env)
	if err != nil {
		t.Fatalf("within-ceiling retry dispatch: %v", err)
	}
	if retry.Error != nil && retry.Error.Kind == "budget_refused" {
		t.Fatalf("a budget at the declared ceiling was refused: %+v", retry.Error)
	}

	obs := envelopeToObservation(resp)
	obs.State = map[string]any{
		"operation": map[string]any{"started": false},
	}
	// The audit's consent was consumed before the run was refused.
	obs.Effects["approval_authority_consumed"] = true
	obs.Effects["atomic_core_effect_zero"] = true
	// The prohibited effect is running the 60-second request for 30 seconds and
	// reporting success. Probing it means proving no admission path lowered the
	// request: the call refused, the durable state is identical to the state
	// before it, and a separate request at the declared ceiling passed
	// admission.
	obs.Effects["silent_budget_clamp"] = probedAbsent{
		Evidence: "the 60s request returned budget_refused naming the 30s ceiling; current_step, durable operation count, and event count were all unchanged across the call, and a separate request at 30s passed budget admission",
	}
	return obs
}

// numericInitialState reads a scenario integer without tolerating a missing or
// non-numeric declaration, so a corpus edit cannot silently weaken a binding.
func numericInitialState(sc jobScenario, key string) (int, bool) {
	value, present := sc.InitialState[key]
	if !present {
		return 0, false
	}
	switch typed := value.(type) {
	case float64:
		return int(typed), true
	case int:
		return typed, true
	default:
		return 0, false
	}
}

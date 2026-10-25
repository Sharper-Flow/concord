package agent

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

// A publication interrupted after its committed proof leaves a pending claim.
// Taking over that claim requires an issued approval and fences the old owner.
func TestCompactionTakeoverOfStuckClaimRequiresApproval(t *testing.T) {
	t.Parallel()
	s, service, grant, privateKey, home := agentJobsCompactionFixture(t)
	const key = "compaction-takeover"
	claims := func() (string, []string) {
		t.Helper()
		rows, err := s.DatabaseForTesting().Query(`SELECT op_id, attempt_epoch, COALESCE(result_kind,'pending') FROM durable_operations WHERE work_id='work-cancelled' AND step_id='git_proof' AND step_kind='cross_authority' ORDER BY attempt_epoch`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var opID string
		var epochs []string
		for rows.Next() {
			var epoch int64
			var result string
			if err := rows.Scan(&opID, &epoch, &result); err != nil {
				t.Fatal(err)
			}
			epochs = append(epochs, fmt.Sprintf("%d:%s", epoch, result))
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return opID, epochs
	}
	stuckClaim := []string{"1:pending"}

	service.publicationObserver = func(phase string) error {
		if phase == "verify_commit" {
			return errors.New("crash after the committed proof")
		}
		return nil
	}
	if stuck := publishCancelledNoteWithKey(t, s, service, grant, privateKey, home, key+"-stuck"); stuck.Outcome != OutcomePartial {
		t.Fatalf("interrupted publication outcome=%s error=%+v, want partial", stuck.Outcome, stuck.Error)
	}
	service.publicationObserver = nil
	opID, epochs := claims()
	if !slices.Equal(epochs, stuckClaim) {
		t.Fatalf("claims after the interrupted publication = %v, want %v", epochs, stuckClaim)
	}

	unapproved := newCancelledNotePublication(t, s, grant, home, key)
	response := dispatchMutation(t, s, service, unapproved.request(), unapproved.env)
	_, epochs = claims()
	if response.Outcome != OutcomeError || response.Error == nil || response.Error.Kind != "approval_required" || !slices.Equal(epochs, stuckClaim) {
		t.Fatalf("unapproved takeover response=%+v claims=%v, want approval_required and unchanged claim", response, epochs)
	}

	// The boundary refuses a reference it never issued. Only the refusal and
	// the unchanged claim are asserted: the error kind it returns today is not
	// the contracted approval_invalid.
	forged := newCancelledNotePublication(t, s, grant, home, key+"-unissued")
	request, env := forged.approvedBy(t, privateKey, strings.Repeat("ab", 32))
	if refused := dispatchMutation(t, s, service, request, env); refused.Outcome != OutcomeError {
		t.Fatalf("unissued approval reference outcome=%s, want error", refused.Outcome)
	}
	if _, epochs = claims(); !slices.Equal(epochs, stuckClaim) {
		t.Fatalf("claims after an unissued approval reference = %v, want %v", epochs, stuckClaim)
	}

	if approved := publishCancelledNoteWithKey(t, s, service, grant, privateKey, home, key+"-approved"); approved.Outcome != OutcomeOK {
		t.Fatalf("approved takeover outcome=%s error=%+v", approved.Outcome, approved.Error)
	}
	if _, epochs = claims(); !slices.Equal(epochs, []string{"1:pending", "2:completed"}) {
		t.Fatalf("claims after the approved takeover = %v, want [1:pending 2:completed]", epochs)
	}
	_, staleErr := store.CompleteStep(context.Background(), s, store.CompleteRequest{OpID: opID, AttemptEpoch: 1, ResultKind: store.ResultCompleted, PrincipalRef: grant.PrincipalRef, Tool: "concord_work_compact", IdempotencyKey: key + "-late", RequestID: key + "-late", ObservedAt: fixedTime()})
	var failure *store.Failure
	if !errors.As(staleErr, &failure) || failure.Kind != store.KindStaleAttempt {
		t.Fatalf("late completion of the superseded claim = %v, want stale_attempt", staleErr)
	}
}

func invokeWorkflowBoundary(t *testing.T, s *store.Store, service *Service, env CallEnvelope, input map[string]any, registry store.DefinitionRegistry) Envelope {
	t.Helper()
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	response, err := InvokeWithRegistry(context.Background(), s, service, mustMarshalInvoke(t, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: raw}, env), registry)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func TestWorkflowActionAvailabilityBeforePayloadOrAuth(t *testing.T) {
	t.Parallel()
	s, service, grant, _ := mutationDispatchFixture(t, []Capability{"work_transition"})
	version := seedAgentWorkflow(t, s, grant)
	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	input := map[string]any{"work_id": "work-1", "expected_version": version, "action_id": "start_execution", "fields": map[string]any{}, "idempotency_key": "boundary-unavailable"}
	response := invokeWorkflowBoundary(t, s, service, env, input, store.NewWorkflowDefinitionRegistry())
	if response.Outcome != OutcomeError || response.Error == nil || response.Error.Kind != "invalid_transition" {
		t.Fatalf("unavailable action response outcome=%s error=%+v", response.Outcome, response.Error)
	}
	var started int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM effect_receipts WHERE kind=?`, store.WorkflowActionStarted).Scan(&started); err != nil {
		t.Fatal(err)
	}
	if started != 0 {
		t.Fatalf("availability failure reached mutation authority: action_started=%d", started)
	}
}

func advanceWorkflowBoundaryToExecution(t *testing.T, s *store.Store, service *Service, grant Authority, privateKey ed25519.PrivateKey, env CallEnvelope) (CallEnvelope, int64) {
	t.Helper()
	for _, action := range []string{"record_proposal", "record_alignment", "record_design"} {
		fields := map[string]any{}
		if action == "record_design" {
			fields = map[string]any{"approach": "The recorded approach is the implementation boundary.", "decisions": []map[string]any{{"id": "decision:boundary", "question": "What crosses into execution?", "choice": "The typed design record.", "rationale": "The worker must receive the approved decision.", "rejected": []string{}}}, "touched_refs": []string{"path:boundary"}}
		}
		if action == "record_proposal" {
			fields = map[string]any{"problem": "The bounded problem statement.", "affected": []string{"The affected system."}, "stakes": "The bounded stakes statement.", "user_outcomes": []string{"The expected user outcome."}}
		}
		if action == "record_alignment" {
			fields = map[string]any{"searched": "The bounded backlog search statement.", "outcome": "none_found"}
		}
		version, err := workVersionForWorkflow(t, s)
		if err != nil {
			t.Fatal(err)
		}
		input := map[string]any{"work_id": "work-1", "expected_version": version, "action_id": action, "fields": fields, "idempotency_key": "boundary-" + action}
		response := invokeWorkflowBoundary(t, s, service, env, input, store.BuiltinWorkflowRegistry())
		if response.Outcome != OutcomeOK {
			t.Fatalf("advance action=%s response=%+v", action, response)
		}
	}
	version, err := workVersionForWorkflow(t, s)
	if err != nil {
		t.Fatal(err)
	}
	challengeInput := map[string]any{"work_id": "work-1", "expected_version": version, "action_id": "approve_contract", "fields": map[string]any{}, "idempotency_key": "boundary-approve"}
	addWorkflowContractApprovalFields(challengeInput)
	challenge := invokeWorkflowBoundary(t, s, service, env, challengeInput, store.BuiltinWorkflowRegistry())
	if challenge.Error == nil || challenge.Error.Kind != "approval_required" {
		t.Fatalf("approval challenge outcome=%s error=%+v", challenge.Outcome, challenge.Error)
	}
	challengeRef, _ := challenge.Error.Details["approval_ref"].(string)
	digest := mutationDigest("concord_work_transition", "workflow_action", env, mustJSON(t, challengeInput))
	scope := map[string]any{"product_id": "product-1", "project_ids": []string{"project-1"}, "work_ids": []string{"work-1"}, "scope_version": env.ScopeVersion}
	versions := map[string]any{"work": version}
	approvalEnv := env
	approvalEnv.HostApproval = signedHostApproval(privateKey, challengeRef, digest, scope, versions, grant.SessionRef, grant.AgentRef, grant.Worktree, fixedTime(), "boundary-approval")
	approved := challengeInput
	approved["approval"] = map[string]any{"approval_ref": challengeRef}
	response := invokeWorkflowBoundary(t, s, service, approvalEnv, approved, store.BuiltinWorkflowRegistry())
	if response.Outcome != OutcomeOK {
		t.Fatalf("approved action=%+v", response)
	}
	if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM work_items WHERE id='work-1'`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	return approvalEnv, version
}

func TestWorkflowActionStrictInvokeBoundaryForStepActorAndPayload(t *testing.T) {
	t.Parallel()
	s, service, grant, privateKey := mutationDispatchFixture(t, []Capability{"work_transition"})
	seedAgentWorkflow(t, s, grant)
	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	env, version := advanceWorkflowBoundaryToExecution(t, s, service, grant, privateKey, env)
	beforeEffects := workflowReplayEffectCounts(t, s)
	input := map[string]any{"work_id": "work-1", "expected_version": version, "action_id": "start_execution", "fields": map[string]any{"payload": map[string]any{"current_step": "plan"}}, "idempotency_key": "boundary-malformed-payload"}
	var before int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM effect_receipts WHERE kind=?`, store.WorkflowActionCompleted).Scan(&before); err != nil {
		t.Fatal(err)
	}
	response := invokeWorkflowBoundary(t, s, service, env, input, store.BuiltinWorkflowRegistry())
	if response.Outcome != OutcomeError || response.Error == nil || response.Error.Kind != "invalid_input" {
		t.Fatalf("malformed step/payload response=%+v", response)
	}
	var after int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM effect_receipts WHERE kind=?`, store.WorkflowActionCompleted).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("malformed request appended action event: before=%d after=%d", before, after)
	}

	actorEnv := env
	actorEnv.AgentRef = "agent:malformed"
	actorInput := map[string]any{"work_id": "work-1", "expected_version": version, "action_id": "start_execution", "fields": map[string]any{}, "idempotency_key": "boundary-malformed-actor"}
	actorResponse := invokeWorkflowBoundary(t, s, service, actorEnv, actorInput, store.BuiltinWorkflowRegistry())
	if actorResponse.Outcome != OutcomeError || actorResponse.Error == nil || actorResponse.Error.Kind != "unauthorized" {
		t.Fatalf("malformed actor response outcome=%s error=%+v", actorResponse.Outcome, actorResponse.Error)
	}
	if afterEffects := workflowReplayEffectCounts(t, s); afterEffects != beforeEffects {
		t.Fatalf("malformed payload or actor changed durable state: before=%+v after=%+v", beforeEffects, afterEffects)
	}
}

func TestWorkflowActionRejectsReplayWithoutEffects(t *testing.T) {
	t.Parallel()
	s, service, grant, _ := mutationDispatchFixture(t, []Capability{"work_transition"})
	version := seedAgentWorkflow(t, s, grant)
	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	var eventsBefore int
	if err := s.DatabaseForTesting().QueryRow(`SELECT last FROM store_sequences WHERE name='effect'`).Scan(&eventsBefore); err != nil {
		t.Fatal(err)
	}
	input := map[string]any{"work_id": "work-1", "expected_version": version, "action_id": "replay", "fields": map[string]any{}, "idempotency_key": "boundary-replay-refused"}
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	response, err := Dispatch(context.Background(), s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: raw}, mutationEnvelope(grant, scopeVersion))
	if err != nil {
		t.Fatal(err)
	}
	// The closed action union refuses replay before mutation authority.
	if response.Outcome != OutcomeError || response.Error == nil || response.Error.Kind != "invalid_input" || !strings.Contains(response.Error.Message, "action_id") {
		if response.Error == nil {
			t.Fatalf("forbidden replay response=%+v", response)
		}
		t.Fatalf("forbidden replay response outcome=%s error=%+v", response.Outcome, *response.Error)
	}
	var eventsAfter int
	if err := s.DatabaseForTesting().QueryRow(`SELECT last FROM store_sequences WHERE name='effect'`).Scan(&eventsAfter); err != nil {
		t.Fatal(err)
	}
	if eventsAfter != eventsBefore {
		t.Fatalf("forbidden replay shape appended events: before=%d after=%d", eventsBefore, eventsAfter)
	}
}

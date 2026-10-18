package agent

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

type workflowBoundaryScenario struct {
	ID       string         `json:"id"`
	Action   string         `json:"action"`
	Setup    corpusSetup    `json:"setup"`
	Request  corpusRequest  `json:"request"`
	Expected corpusExpected `json:"expected"`
}

func readWorkflowBoundaryScenario(t *testing.T, id string) workflowBoundaryScenario {
	t.Helper()
	data, err := os.ReadFile("../../.concord/scenarios/workflow-engine.v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var corpus struct {
		Scenarios []workflowBoundaryScenario `json:"scenarios"`
	}
	if err := json.Unmarshal(data, &corpus); err != nil {
		t.Fatal(err)
	}
	for _, scenario := range corpus.Scenarios {
		if scenario.ID == id {
			return scenario
		}
	}
	t.Fatalf("scenario %s is missing", id)
	return workflowBoundaryScenario{}
}

func TestWorkflowCorpusWF56CarriesVacateReclaimBoundary(t *testing.T) {
	t.Parallel()
	scenario := readWorkflowBoundaryScenario(t, "WF56-session-vacate-then-reclaim-passes-gate")
	if scenario.Action != "session_vacate" {
		t.Fatalf("WF56 action=%q, want session_vacate", scenario.Action)
	}
	if len(scenario.Expected.Assertions) < 2 {
		t.Fatalf("WF56 has too few boundary assertions: %d", len(scenario.Expected.Assertions))
	}
}

// TestWorkflowCorpusWF25TakeoverOfStuckClaimRequiresApproval runs CD-0013
// criterion 25 on compaction publication, the only production route that
// claims a durable operation. Compaction publishes only terminal work, so the
// run uses the compaction fixture's cancelled work item. A publication that
// crashed after its committed proof leaves its first claim pending; a new
// claim on that operation must consume an issued approval.
func TestWorkflowCorpusWF25TakeoverOfStuckClaimRequiresApproval(t *testing.T) {
	t.Parallel()
	scenario := readWorkflowBoundaryScenario(t, "WF25-operator-takeover-approval")
	if scenario.Action != "takeover_attempt" || scenario.Request.Approval.ApprovalRef != nil {
		t.Fatalf("WF25 action=%q approval_ref=%v, want an unapproved takeover_attempt", scenario.Action, scenario.Request.Approval.ApprovalRef)
	}
	s, service, grant, privateKey, home := agentJobsCompactionFixture(t)
	key := scenario.Request.Idempotency.Key
	claims := func() (string, []string) {
		t.Helper()
		rows, err := s.DatabaseForTesting().Query(`SELECT op_id, attempt_epoch, COALESCE(result_kind,'pending') FROM durable_operations WHERE work_id='work-cancelled' AND step_id=? AND step_kind=? ORDER BY attempt_epoch`, scenario.Request.Operation.StepID, scenario.Request.Operation.StepKind)
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
		t.Fatalf("WF25 interrupted publication outcome=%s error=%+v, want partial", stuck.Outcome, stuck.Error)
	}
	service.publicationObserver = nil
	opID, epochs := claims()
	if !slices.Equal(epochs, stuckClaim) {
		t.Fatalf("WF25 claims after the interrupted publication = %v, want %v", epochs, stuckClaim)
	}

	unapproved := newCancelledNotePublication(t, s, grant, home, key)
	response := dispatchMutation(t, s, service, unapproved.request(), unapproved.env)
	_, epochs = claims()
	communication := map[string]any{}
	if response.Error != nil {
		communication["error"] = map[string]any{"kind": response.Error.Kind}
	}
	assertAgentCorpus(t, map[string]any{
		"communication": communication,
		"authority":     map[string]any{"attempt": map[string]any{"owner_changed": !slices.Equal(epochs, stuckClaim)}},
	}, scenario.Expected.Assertions)

	// The boundary refuses a reference it never issued. Only the refusal and
	// the unchanged claim are asserted: the error kind it returns today is not
	// the contracted approval_invalid.
	forged := newCancelledNotePublication(t, s, grant, home, key+"-unissued")
	request, env := forged.approvedBy(t, privateKey, strings.Repeat("ab", 32))
	if refused := dispatchMutation(t, s, service, request, env); refused.Outcome != OutcomeError {
		t.Fatalf("WF25 unissued approval reference outcome=%s, want error", refused.Outcome)
	}
	if _, epochs = claims(); !slices.Equal(epochs, stuckClaim) {
		t.Fatalf("WF25 claims after an unissued approval reference = %v, want %v", epochs, stuckClaim)
	}

	if approved := publishCancelledNoteWithKey(t, s, service, grant, privateKey, home, key+"-approved"); approved.Outcome != OutcomeOK {
		t.Fatalf("WF25 approved takeover outcome=%s error=%+v", approved.Outcome, approved.Error)
	}
	if _, epochs = claims(); !slices.Equal(epochs, []string{"1:pending", "2:completed"}) {
		t.Fatalf("WF25 claims after the approved takeover = %v, want [1:pending 2:completed]", epochs)
	}
	_, staleErr := store.CompleteStep(context.Background(), s, store.CompleteRequest{OpID: opID, AttemptEpoch: 1, ResultKind: store.ResultCompleted, PrincipalRef: grant.PrincipalRef, Tool: "concord_work_compact", IdempotencyKey: key + "-late", RequestID: key + "-late", ObservedAt: fixedTime()})
	var failure *store.Failure
	if !errors.As(staleErr, &failure) || failure.Kind != store.KindStaleAttempt {
		t.Fatalf("WF25 late completion of the superseded claim = %v, want stale_attempt", staleErr)
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

func TestWorkflowCorpusWF37UsesAgentAvailabilityBeforePayloadOrAuth(t *testing.T) {
	t.Parallel()
	scenario := readWorkflowBoundaryScenario(t, "WF37-action-availability-before-register")
	s, service, grant, _ := mutationDispatchFixture(t, []Capability{"work_transition"})
	if got := seedAgentWorkflow(t, s, grant); got != 4 {
		t.Fatalf("workflow seed version=%d, want 4", got)
	}
	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	input := map[string]any{"work_id": "work-1", "expected_version": scenario.Request.ExpectedVersion, "action_id": scenario.Request.ActionID, "fields": map[string]any{}, "idempotency_key": scenario.Request.Idempotency.Key}
	response := invokeWorkflowBoundary(t, s, service, env, input, store.NewWorkflowDefinitionRegistry())
	if response.Outcome != OutcomeError || response.Error == nil || response.Error.Kind != "invalid_transition" {
		t.Fatalf("WF37 response outcome=%s error=%+v", response.Outcome, response.Error)
	}
	var started int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE kind=?`, store.WorkflowActionStarted).Scan(&started); err != nil {
		t.Fatal(err)
	}
	if started != 0 {
		t.Fatalf("WF37 availability failure reached mutation authority: action_started=%d", started)
	}
}

func advanceWorkflowBoundaryToExecution(t *testing.T, s *store.Store, service *Service, grant Authority, privateKey ed25519.PrivateKey, env CallEnvelope) (CallEnvelope, int64) {
	t.Helper()
	// The canonical head removed the discovery marker, so the pre-contract
	// walk is proposal, alignment, design; each consumes two events.
	for index, action := range []string{"record_proposal", "record_alignment", "record_design"} {
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
		expectedVersion := []int{4, 6, 8}[index]
		input := map[string]any{"work_id": "work-1", "expected_version": int64(expectedVersion), "action_id": action, "fields": fields, "idempotency_key": "boundary-" + action}
		response := invokeWorkflowBoundary(t, s, service, env, input, store.BuiltinWorkflowRegistry())
		if response.Outcome != OutcomeOK {
			t.Fatalf("advance action=%s response=%+v", action, response)
		}
	}
	challengeInput := map[string]any{"work_id": "work-1", "expected_version": int64(10), "action_id": "approve_contract", "fields": map[string]any{}, "idempotency_key": "boundary-approve"}
	addWorkflowContractApprovalFields(challengeInput)
	challenge := invokeWorkflowBoundary(t, s, service, env, challengeInput, store.BuiltinWorkflowRegistry())
	if challenge.Error == nil || challenge.Error.Kind != "approval_required" {
		t.Fatalf("approval challenge outcome=%s error=%+v", challenge.Outcome, challenge.Error)
	}
	challengeRef, _ := challenge.Error.Details["approval_ref"].(string)
	digest := mutationDigest("concord_work_transition", "workflow_action", env, mustJSON(t, challengeInput))
	scope := map[string]any{"product_id": "product-1", "project_ids": []string{"project-1"}, "work_ids": []string{"work-1"}, "scope_version": env.ScopeVersion}
	versions := map[string]any{"work": int64(10)}
	approvalEnv := env
	approvalEnv.HostApproval = signedHostApproval(privateKey, challengeRef, digest, scope, versions, grant.SessionRef, grant.AgentRef, grant.Worktree, fixedTime(), "boundary-approval")
	approved := challengeInput
	approved["approval"] = map[string]any{"approval_ref": challengeRef}
	response := invokeWorkflowBoundary(t, s, service, approvalEnv, approved, store.BuiltinWorkflowRegistry())
	if response.Outcome != OutcomeOK {
		t.Fatalf("approved action=%+v", response)
	}
	var version int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM work_items WHERE id='work-1'`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	return approvalEnv, version
}

func TestWorkflowCorpusWF38UsesStrictInvokeBoundaryForStepActorAndPayload(t *testing.T) {
	t.Parallel()
	scenario := readWorkflowBoundaryScenario(t, "WF38-action-payload-step-actor")
	s, service, grant, privateKey := mutationDispatchFixture(t, []Capability{"work_transition"})
	seedAgentWorkflow(t, s, grant)
	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	env, version := advanceWorkflowBoundaryToExecution(t, s, service, grant, privateKey, env)
	input := map[string]any{"work_id": "work-1", "expected_version": version, "action_id": scenario.Request.ActionID, "fields": map[string]any{"payload": map[string]any{"current_step": "plan"}}, "idempotency_key": scenario.Request.Idempotency.Key}
	var before int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE kind=?`, store.WorkflowActionCompleted).Scan(&before); err != nil {
		t.Fatal(err)
	}
	response := invokeWorkflowBoundary(t, s, service, env, input, store.BuiltinWorkflowRegistry())
	if response.Outcome != OutcomeError || response.Error == nil || response.Error.Kind != "invalid_input" {
		t.Fatalf("WF38 malformed step/payload response=%+v", response)
	}
	var after int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE kind=?`, store.WorkflowActionCompleted).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("WF38 malformed request appended action event: before=%d after=%d", before, after)
	}

	actorEnv := env
	actorEnv.AgentRef = "agent:malformed"
	actorInput := map[string]any{"work_id": "work-1", "expected_version": version, "action_id": scenario.Request.ActionID, "fields": map[string]any{}, "idempotency_key": "wf38-malformed-actor"}
	actorResponse := invokeWorkflowBoundary(t, s, service, actorEnv, actorInput, store.BuiltinWorkflowRegistry())
	if actorResponse.Outcome != OutcomeError || actorResponse.Error == nil || actorResponse.Error.Kind != "unauthorized" {
		t.Fatalf("WF38 malformed actor response outcome=%s error=%+v", actorResponse.Outcome, actorResponse.Error)
	}
}

func TestWorkflowCorpusWF46RejectsRemovedAgentReplayShape(t *testing.T) {
	t.Parallel()
	scenario := readWorkflowBoundaryScenario(t, "WF46-event-version-fail-closed")
	if scenario.Action != "replay" {
		t.Fatalf("WF46 no longer declares the historical replay action: %q", scenario.Action)
	}
	s, service, grant, _ := mutationDispatchFixture(t, []Capability{"work_transition"})
	workID := scenario.Setup.FixtureRefs.WorkItem
	seedCorpusWorkflowWork(t, s, grant, workID, scenario.Setup.EventHistory)
	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	var eventsBefore int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events`).Scan(&eventsBefore); err != nil {
		t.Fatal(err)
	}
	input := map[string]any{"work_id": workID, "expected_version": scenario.Request.ExpectedVersion, "action_id": scenario.Action, "fields": scenario.Request.Fields, "idempotency_key": scenario.Request.Idempotency.Key}
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	response, err := Dispatch(context.Background(), s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: raw}, mutationEnvelope(grant, scopeVersion))
	if err != nil {
		t.Fatal(err)
	}
	// The closed action-variant surface owns the removed-action refusal
	// (CON-412): the historical replay shape never reaches authority, the
	// boundary refuses it with a field-named invalid_input naming action_id,
	// and no event is appended either way.
	if response.Outcome != OutcomeError || response.Error == nil || response.Error.Kind != "invalid_input" || !strings.Contains(response.Error.Message, "action_id") {
		if response.Error == nil {
			t.Fatalf("WF46 removed replay response=%+v", response)
		}
		t.Fatalf("WF46 removed replay response outcome=%s error=%+v", response.Outcome, *response.Error)
	}
	var eventsAfter int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events`).Scan(&eventsAfter); err != nil {
		t.Fatal(err)
	}
	if eventsAfter != eventsBefore {
		t.Fatalf("WF46 removed replay shape appended events: before=%d after=%d", eventsBefore, eventsAfter)
	}
}

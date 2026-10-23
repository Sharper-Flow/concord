package store

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
)

// TestRegisteredStepsAdmitTheirApprovalRequiredActions holds the invariant
// that once stranded workflow.ops_runbook at its cleanup step: a step may
// declare an approval-required action only when the step's operator question
// serves that approval. workflowOperatorQuestionAction owns which action a
// step's question serves, so a step declaring another approval-required
// action offers a route no caller can take.
//
// request_correction is the one exception. Its approval is never served by a
// step question: the guard admits it from the folded correction route and,
// under the consequence_v1 policy, as a routine repair request with no
// operator identity. A step may therefore declare it where the step owns the
// route that admits it: the CD-0166 delivery gate, or a step whose
// unhealthy_verdict recovery route names it (CD-0143).
//
// The scope is every registered version, frozen ones included, because
// stranded items run on old pins (CD-0115 D2).
func TestRegisteredStepsAdmitTheirApprovalRequiredActions(t *testing.T) {
	for _, definition := range BuiltinWorkflowDefinitions() {
		approvalRequired := map[string]bool{}
		for _, action := range definition.ActionDefinitions {
			if action.Approval == ActionApprovalRequired {
				approvalRequired[action.ID] = true
			}
		}
		for _, step := range definition.StepGraph.Steps {
			if step.Kind == WorkflowStepHumanCheckpoint {
				continue
			}
			served, _ := workflowOperatorQuestionAction(definition, step.ID)
			for _, id := range step.Actions {
				if id == "request_correction" && (workflowStepIsDeliveryGate(&step) || workflowUnhealthyVerdictRouteTarget(definition, step.ID) != "") {
					continue
				}
				if approvalRequired[id] && id != served {
					t.Errorf("%s v%d step %q has kind %q and declares approval-required action %q that its operator question does not serve, so the action can never run", definition.Ref, definition.Version, step.ID, step.Kind, id)
				}
			}
		}
	}
}

// TestRegisteredNonTerminalStepsCanAdvance proves the consequence the
// invariant above protects. A non-terminal step whose every advancing action
// is refused is a dead end, and every work item that reaches it stops.
func TestRegisteredNonTerminalStepsCanAdvance(t *testing.T) {
	for _, definition := range BuiltinWorkflowDefinitions() {
		terminal := map[string]bool{}
		for _, id := range definition.StepGraph.TerminalSteps {
			terminal[id] = true
		}
		effect := map[string]WorkflowActionDefinition{}
		for _, action := range definition.ActionDefinitions {
			effect[action.ID] = action
		}
		for _, step := range definition.StepGraph.Steps {
			if terminal[step.ID] {
				continue
			}
			served, _ := workflowOperatorQuestionAction(definition, step.ID)
			reachable := false
			for _, id := range step.Actions {
				action, ok := effect[id]
				if !ok || !workflowActionAdvancesStep(definition, id) {
					continue
				}
				if action.Approval == ActionApprovalRequired && step.Kind != WorkflowStepHumanCheckpoint && id != served {
					continue
				}
				reachable = true
				break
			}
			if !reachable {
				t.Errorf("%s v%d step %q declares no advancing action it admits; work that reaches this step cannot leave it", definition.Ref, definition.Version, step.ID)
			}
		}
	}
}

// workflowRequiredEvidenceOutsideReach returns, in declaration order, the
// evidence kinds a definition requires — across the definition, its steps,
// and its rigor rules — that no action reachable through forward and optional
// edges can produce. TestShippedDefinitionsCanProduceRequiredEvidence holds
// this set empty for every shipped definition;
// TestSyntheticDefinitionEvidenceGapIsDetected holds it non-empty for a
// synthetic one, so the check cannot pass by accident.
func workflowRequiredEvidenceOutsideReach(definition WorkflowDefinition) []EvidenceKind {
	producible := evidenceStrings(workflowReachableEvidenceKinds(definition))
	var missing []EvidenceKind
	for _, kind := range definition.RequiredEvidenceKinds {
		if !slices.Contains(producible, string(kind)) {
			missing = append(missing, kind)
		}
	}
	for _, step := range definition.StepGraph.Steps {
		for _, kind := range step.RequiredEvidenceKinds {
			if !slices.Contains(producible, string(kind)) {
				missing = append(missing, kind)
			}
		}
	}
	for _, rule := range definition.RigorRules {
		for _, kind := range rule.RequiredEvidenceKinds {
			if !slices.Contains(producible, string(kind)) {
				missing = append(missing, kind)
			}
		}
	}
	return missing
}

// TestShippedDefinitionsCanProduceRequiredEvidence holds the evidence-side
// consequence of the same graph facts the invariants above protect: every
// evidence kind a current shipped definition requires — at the definition, on
// a step, or in a rigor rule — is a kind at least one action on a step
// reachable through forward and optional edges can produce. A requirement
// outside that set strands the gate that waits for it: no route through the
// workflow can ever bind the kind, so no contract naming it can be served.
// Frozen prior versions stay byte-identical by law, so the scope is the
// shipped set alone, like the invariants above.
func TestShippedDefinitionsCanProduceRequiredEvidence(t *testing.T) {
	for _, definition := range BuiltinWorkflowDefinitions() {
		if missing := workflowRequiredEvidenceOutsideReach(definition); len(missing) != 0 {
			producible := evidenceStrings(workflowReachableEvidenceKinds(definition))
			t.Errorf("%s v%d requires evidence kinds %v its reachable actions cannot produce; those actions produce %v", definition.Ref, definition.Version, missing, producible)
		}
	}
}

// TestFrozenOpsRunbookCleanupOpensThePremiseQuestion drives a version-4
// ops-runbook item to its internal-SQLite cleanup step and proves the step
// keeps its question: the healthy verdict records through the recorded event
// route the frozen pin replays, the work pin still carries the premise
// question that serves confirm_premise, and the archived family's public
// route refuses the confirmation — a retired pin offers no new public
// workflow action (CD-0203 D2).
func TestFrozenOpsRunbookCleanupOpensThePremiseQuestion(t *testing.T) {
	t.Parallel()
	const workID = "frozen-ops-cleanup"
	fixture := seedHistoricalWorkflowReturnRouteFixture(t, workID, "workflow.ops_runbook", 4, "health")
	s := fixture.store
	reviewer := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/reviewer", SessionRef: "session/" + workID + "-reviewer", ActorClass: ActorAgent}
	reviewerRef, err := WorkflowActorRef(reviewer)
	if err != nil {
		t.Fatal(err)
	}
	version := readWorkVersion(t, s, workID)
	reviewerEvent := workflowEventWithActor("frozen-ops-reviewer-"+workID, WorkflowActorRecorded, workID, reviewerRef, map[string]any{
		"work_id": workID, "expected_version": version, "resulting_version": version + 1,
		"actor_ref": reviewerRef, "principal_ref": reviewer.PrincipalRef, "client_ref": reviewer.ClientRef,
		"agent_ref": reviewer.AgentRef, "session_ref": reviewer.SessionRef, "actor_class": "agent",
	})
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{reviewerEvent}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}}); err != nil {
		t.Fatalf("record the reviewer actor: %v", err)
	}
	seedVerifiedNativeRunCapture(t, s, workID)
	// The healthy verdict records through the recorded route: the public
	// verdict action on the archived pin refuses, and the frozen pin still
	// folds the shape it was released under.
	verdictVersion := readWorkVersion(t, s, workID)
	verdict := workflowEventWithActor("frozen-ops-verdict-"+workID, WorkflowVerdictRecorded, workID, reviewerRef, map[string]any{
		"work_id": workID, "expected_version": verdictVersion, "resulting_version": verdictVersion + 1,
		"contract_version": 1, "predicate_id": "predicate:return-route", "verdict_kind": "ok",
		"verdict_actor_ref": reviewerRef, "evaluation_evidence": []string{"evidence:return-route-review"},
		"incomparable_with_approved": false,
	})
	// The current payload version keeps the approved predicate as written;
	// the v1 upcast rewrites retired names.
	verdict.PayloadVersion = 2
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{verdict}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): verdictVersion}}); err != nil {
		t.Fatalf("record the healthy verdict on the recorded route: %v", err)
	}
	ownerRef, err := WorkflowActorRef(fixture.owner)
	if err != nil {
		t.Fatal(err)
	}
	if err := advanceWorkflowTestInstanceToStep(context.Background(), s, workID, "cleanup", ownerRef); err != nil {
		t.Fatalf("advance to cleanup: %v", err)
	}
	insertInvestigationGateObservation(t, s, workID, "obs:"+strings.Repeat("5", 16), []string{"root"})
	pin := issue1013Pin(t, s, workID)
	if pin.PendingOperatorDecision == nil || pin.PendingOperatorDecision.ActionID != "confirm_premise" {
		t.Fatalf("cleanup pin question = %+v, withheld = %+v; want the confirm_premise question", pin.PendingOperatorDecision, pin.WithheldOperatorDecision)
	}
	// The question opens as a read, but the archived family offers no public
	// route: the pin's intent list is empty.
	if len(pin.NextValidIntents) != 0 {
		t.Fatalf("archived cleanup pin offers public intents: %#v", pin.NextValidIntents)
	}
	if err := runIssue933OperatorAction(t, s, workID, "confirm_premise", json.RawMessage(`{"contract_version":1}`), fixture.owner, fixture.operator); err == nil {
		t.Fatal("the archived cleanup confirmation ran")
	}
	var step string
	if err := s.DatabaseForTesting().QueryRow(`SELECT current_step FROM workflow_instances WHERE work_id=?`, workID).Scan(&step); err != nil {
		t.Fatal(err)
	}
	if step != "cleanup" {
		t.Fatalf("refused confirmation moved the step to %q, want cleanup", step)
	}
	var completed int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id=? AND kind=?`, workID, WorkflowCompleted).Scan(&completed); err != nil {
		t.Fatal(err)
	}
	if completed != 0 {
		t.Fatalf("a refused archived confirmation completed the workflow: %d events", completed)
	}
}

// TestFrozenOpsRunbookCleanupRecordsAMissingVerdict drives a version-4
// ops-runbook item past health with no verdict to its cleanup step. The
// archived family offers no public route there: the pin advertises no
// intents, the late verdict refuses as a public action, and the confirmation
// refuses with it — nothing confirms a missing verdict and nothing completes
// the instance (CD-0204).
func TestFrozenOpsRunbookCleanupRecordsAMissingVerdict(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	const workID = "frozen-ops-late-verdict"
	fixture := seedHistoricalWorkflowReturnRouteFixture(t, workID, "workflow.ops_runbook", 4, "health")
	s := fixture.store
	reviewer := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/reviewer", SessionRef: "session/" + workID + "-reviewer", ActorClass: ActorAgent}
	reviewerRef, err := WorkflowActorRef(reviewer)
	if err != nil {
		t.Fatal(err)
	}
	version := readWorkVersion(t, s, workID)
	reviewerEvent := workflowEventWithActor("frozen-ops-reviewer-"+workID, WorkflowActorRecorded, workID, reviewerRef, map[string]any{
		"work_id": workID, "expected_version": version, "resulting_version": version + 1,
		"actor_ref": reviewerRef, "principal_ref": reviewer.PrincipalRef, "client_ref": reviewer.ClientRef,
		"agent_ref": reviewer.AgentRef, "session_ref": reviewer.SessionRef, "actor_class": "agent",
	})
	if err := applyWorkflowTestOperation(ctx, s, Operation{Events: []Event{reviewerEvent}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}}); err != nil {
		t.Fatalf("record the reviewer actor: %v", err)
	}
	seedVerifiedNativeRunCapture(t, s, workID)
	ownerRef, err := WorkflowActorRef(fixture.owner)
	if err != nil {
		t.Fatal(err)
	}
	if err := advanceWorkflowTestInstanceToStep(ctx, s, workID, "cleanup", ownerRef); err != nil {
		t.Fatalf("advance to cleanup: %v", err)
	}
	insertInvestigationGateObservation(t, s, workID, "obs:"+strings.Repeat("6", 16), []string{"root"})
	pin := issue1013Pin(t, s, workID)
	if issue1013HasIntent(pin, "confirm_premise") {
		t.Fatalf("cleanup pin offers confirm_premise without a verdict: %#v", pin.NextValidIntents)
	}
	if issue1013HasIntent(pin, "record_verdict") {
		t.Fatalf("cleanup pin offers record_verdict: %#v", pin.NextValidIntents)
	}
	if len(pin.NextValidIntents) != 0 {
		t.Fatalf("archived cleanup pin offers public intents %#v, want none", pin.NextValidIntents)
	}
	verdict := json.RawMessage(`{"contract_version":1,"predicate_id":"predicate:return-route","verdict_kind":"ok"}`)
	lateErr := WorkflowActionPreflightWithRegistry(ctx, s, BuiltinWorkflowRegistry(), WorkflowActionPreflightRequest{WorkID: workID, ExpectedVersion: pin.Version, ActionID: "record_verdict", Actor: reviewer, Payload: verdict})
	var failure *Failure
	if !errors.As(lateErr, &failure) || failure.Kind != KindInvalidOperation || !strings.Contains(failure.Detail, "workflow.ops_runbook is archived: new public workflow actions are refused") {
		t.Fatalf("late verdict preflight = %v, want the archived-family invalid_operation refusal", lateErr)
	}
	if err := runVerdictActionAs(t, s, workID, "record_verdict", verdict, 0, reviewer); err == nil {
		t.Fatal("the archived late verdict ran")
	}
	if err := runIssue933OperatorAction(t, s, workID, "confirm_premise", json.RawMessage(`{"contract_version":1}`), fixture.owner, fixture.operator); err == nil {
		t.Fatal("the archived cleanup confirmed a missing verdict")
	}
	var step string
	if err := s.DatabaseForTesting().QueryRow(`SELECT current_step FROM workflow_instances WHERE work_id=?`, workID).Scan(&step); err != nil {
		t.Fatal(err)
	}
	if step != "cleanup" {
		t.Fatalf("refused archived actions moved the step to %q, want cleanup", step)
	}
	var completed int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id=? AND kind=?`, workID, WorkflowCompleted).Scan(&completed); err != nil {
		t.Fatal(err)
	}
	if completed != 0 {
		t.Fatalf("refused archived actions completed the workflow: %d events", completed)
	}
}

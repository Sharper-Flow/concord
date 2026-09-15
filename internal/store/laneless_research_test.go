package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// Issue #970: a lane-less workflow never runs a fenced action, so no start or
// dispatch ever assigned an executing actor. Every record_verdict fold then
// failed the distinctness JOIN with "executing actor tuple is incomplete",
// stranding research items that reached conclude with all content recorded.
//
// The definition-selected fold now pins the selecting session as the
// executing actor, and instances whose projection predates that pin derive
// the same identity from the immutable definition event.

// seedLanelessResearchItem reproduces a production-shaped research capture:
// the actor-recorded and definition-selected events carry the same
// authenticated selector, no fenced action ever starts, and the item walks
// frame → investigate → findings with a bound report. Nothing writes
// workflow_instances directly.
func seedLanelessResearchItem(t *testing.T, workID string) (*Store, WorkflowActor) {
	t.Helper()
	ctx := context.Background()
	s := openTemp(t)
	seedWork(t, s, workID)
	seedWorkflowLaw(t, s)
	owner := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/researcher", SessionRef: "session/" + workID, ActorClass: ActorAgent}
	ownerRef, err := WorkflowActorRef(owner)
	if err != nil {
		t.Fatal(err)
	}
	definition, ok := BuiltinWorkflowRegistry().Lookup("workflow.research", 5)
	if !ok {
		t.Fatal("workflow.research v5 is not registered")
	}
	setup := []Event{
		workflowEventWithActor("laneless-actor-"+workID, WorkflowActorRecorded, workID, ownerRef, map[string]any{"work_id": workID, "expected_version": 2, "resulting_version": 3, "actor_ref": ownerRef, "principal_ref": owner.PrincipalRef, "client_ref": owner.ClientRef, "agent_ref": owner.AgentRef, "session_ref": owner.SessionRef, "actor_class": "agent"}),
		workflowEventWithActor("laneless-definition-"+workID, WorkflowDefinitionSelected, workID, ownerRef, map[string]any{"work_id": workID, "expected_version": 3, "resulting_version": 4, "ref": definition.Definition.Ref, "version": definition.Definition.Version, "digest": definition.Digest, "work_kind": string(definition.Definition.WorkKind)}),
		workflowEventWithActor("laneless-contract-"+workID, WorkflowContractApproved, workID, ownerRef, map[string]any{"work_id": workID, "expected_version": 4, "resulting_version": 5, "contract_version": 1, "premise": "record the research report", "outcome_kind": "outcome", "outcome_payload": map[string]any{"kind": "outcome", "allowed": []string{"report_recorded"}}, "outcome_predicates": []map[string]any{{"predicate_id": "predicate:research-report-exit", "ordinal": 0, "outcome_kind": "outcome", "outcome_payload": map[string]any{"kind": "outcome", "allowed": []string{"report_recorded"}}}}, "required_evidence": []string{"artifact"}, "route_conventions": []string{}, "spec_mandate": []string{}, "rigor_class": "prototype_internal", "consequence_class": "internal_sqlite"}),
		workflowActionCompletedFixture("laneless-approval-"+workID, workID, ownerRef, 5, "frame", "approve_contract"),
		workflowActionCompletedFixture("laneless-finding-"+workID, workID, ownerRef, 6, "investigate", "record_finding"),
	}
	setup[2].PayloadVersion = 3
	if err := applyWorkflowTestOperation(ctx, s, Operation{Events: setup, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): 2}}); err != nil {
		t.Fatal(err)
	}
	if err := runVerdictActionAs(t, s, workID, "record_report", json.RawMessage(`{"evidence_kind":"artifact","immutable_subject_ref":"evidence:research-report"}`), 0, owner); err != nil {
		t.Fatalf("record research report: %v", err)
	}
	if err := runVerdictActionAs(t, s, workID, "record_conclusion", json.RawMessage(`{}`), 0, owner); err != nil {
		t.Fatalf("record research conclusion: %v", err)
	}
	return s, owner
}

func lanelessInstanceStep(t *testing.T, s *Store, workID string) string {
	t.Helper()
	var step string
	if err := s.DatabaseForTesting().QueryRow(`SELECT current_step FROM workflow_instances WHERE work_id=?`, workID).Scan(&step); err != nil {
		t.Fatal(err)
	}
	return step
}

// The definition-selected fold itself pins the selecting session: no other
// write path is needed for a lane-less instance to name an executor.
func TestDefinitionSelectedPinsSelectingActorAsExecutor(t *testing.T) {
	const workID = "issue970-selector-pinned"
	s, owner := seedLanelessResearchItem(t, workID)
	ownerRef, err := WorkflowActorRef(owner)
	if err != nil {
		t.Fatal(err)
	}
	var executing string
	if err := s.DatabaseForTesting().QueryRow(`SELECT COALESCE(execution_actor_ref,'') FROM workflow_instances WHERE work_id=?`, workID).Scan(&executing); err != nil {
		t.Fatal(err)
	}
	if executing != ownerRef {
		t.Fatalf("execution_actor_ref=%q, want the selecting actor %q", executing, ownerRef)
	}
	if step := lanelessInstanceStep(t, s, workID); step != "conclude" {
		t.Fatalf("current_step=%q, want conclude", step)
	}
}

// The full lane-less exit: operator-signed verdict, operator premise
// confirmation, and completion all succeed with no fenced action and no
// direct projection write.
func TestLanelessResearchCompletesWithoutFencedAction(t *testing.T) {
	const workID = "issue970-laneless-complete"
	s, owner := seedLanelessResearchItem(t, workID)
	operator := operatorVerdictActor(t, workID)

	if err := runOperatorVerdictForPredicate(t, s, workID, owner, operator, "predicate:research-report-exit", "evidence:research-report"); err != nil {
		t.Fatalf("operator verdict on a lane-less research item refused: %v", err)
	}

	operatorRef, _ := WorkflowActorRef(operator)
	premiseVersion := verdictItemVersion(t, s, workID)
	operatorRecorded := workflowEvent("issue970-actor-"+workID, WorkflowActorRecorded, workID, map[string]any{"work_id": workID, "expected_version": premiseVersion, "resulting_version": premiseVersion + 1, "actor_ref": operatorRef, "principal_ref": operator.PrincipalRef, "client_ref": operator.ClientRef, "agent_ref": operator.AgentRef, "session_ref": operator.SessionRef, "actor_class": "operator"})
	premise := workflowEventWithActor("issue970-premise-"+workID, WorkflowPremiseConfirmed, workID, operatorRef, map[string]any{"work_id": workID, "expected_version": premiseVersion + 1, "resulting_version": premiseVersion + 2, "contract_version": 1, "confirming_actor_ref": operatorRef})
	premiseCompleted := workflowActionCompletedFixture("issue970-premise-done-"+workID, workID, operatorRef, premiseVersion+2, "conclude", "confirm_premise")
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{operatorRecorded, premise, premiseCompleted}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): premiseVersion}}); err != nil {
		t.Fatalf("premise confirmation: %v", err)
	}

	completeVersion := verdictItemVersion(t, s, workID)
	tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := enterFold(context.Background(), tx); err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]any{"impact_verdict": "non-breaking"})
	_, err = applyWorkflowActionRawTx(context.Background(), tx, BuiltinWorkflowRegistry(), WorkflowActionExecutionRequest{
		WorkID: workID, ExpectedVersion: completeVersion, ActionID: "complete", Payload: payload, Actor: owner, OperatorActor: &operator,
		AcceptedInputsDigest: "sha256:" + strings.Repeat("9", 64) + workID, IdempotencyIdentity: "issue970-complete-" + workID, OperationID: "issue970-complete-" + workID,
		PrincipalRef: owner.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: "issue970-complete-" + workID, RequestID: "request:issue970-complete-" + workID, ContractDigest: testManifestDigest, Now: time.Unix(9, 0).UTC(),
	})
	if err != nil {
		_ = leaveFold(context.Background(), tx)
		t.Fatalf("lane-less research completion refused: %v", err)
	}
	_ = leaveFold(context.Background(), tx)
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var state string
	if err := s.DatabaseForTesting().QueryRow(`SELECT instance_state FROM workflow_instances WHERE work_id=?`, workID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "completed" {
		t.Fatalf("instance_state=%s, want completed", state)
	}
}

// Pinning the selector must not weaken CD-0013 D5: the session that selected
// the definition and drove every step still cannot verdict its own delivery.
func TestLanelessResearchSelectorCannotVerdictOwnDelivery(t *testing.T) {
	const workID = "issue970-self-verdict"
	s, owner := seedLanelessResearchItem(t, workID)

	payload, _ := json.Marshal(map[string]any{"predicate_id": "predicate:research-report-exit", "verdict_kind": "ok", "evaluation_evidence": []string{"evidence:research-report"}})
	err := runVerdictActionAs(t, s, workID, "record_verdict", payload, 0, owner)
	if err == nil || !strings.Contains(err.Error(), "evaluate") {
		t.Fatalf("selector self-verdict err=%v, want the self-evaluation refusal", err)
	}
}

// Instances pinned before the fix carry an empty executor projection. The
// distinctness checks derive the selector from the immutable definition
// event, so an operator-signed verdict completes without a rebuild.
func TestLegacyInstanceDerivesSelectorForDistinctness(t *testing.T) {
	const workID = "issue970-legacy-derivation"
	s, owner := seedLanelessResearchItem(t, workID)

	// Recreate the pre-fix projection: an instance whose executor was never
	// assigned. This is the state of the live items that reported #970.
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1); UPDATE workflow_instances SET execution_actor_ref=NULL WHERE work_id=?; DELETE FROM fold_guard`, workID); err != nil {
		t.Fatal(err)
	}

	agentRef, sessionRef, found, err := WorkflowExecutingIdentity(context.Background(), s, workID)
	if err != nil || !found {
		t.Fatalf("executing identity found=%v err=%v, want the derived selector", found, err)
	}
	if agentRef != owner.AgentRef || sessionRef != owner.SessionRef {
		t.Fatalf("derived identity=%q/%q, want the selector %q/%q", agentRef, sessionRef, owner.AgentRef, owner.SessionRef)
	}

	operator := operatorVerdictActor(t, workID)
	if err := runOperatorVerdictForPredicate(t, s, workID, owner, operator, "predicate:research-report-exit", "evidence:research-report"); err != nil {
		t.Fatalf("operator verdict with a legacy executor projection refused: %v", err)
	}
}

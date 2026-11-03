package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestOrdinaryExecutionStartDoesNotGrantCompletion(t *testing.T) {
	t.Parallel()
	workID := "authority-work"
	s, executor, _ := seedOrdinaryExecutorAtExecution(t, workID)
	if got := currentStep(t, s, workID); got != "execution" {
		t.Fatalf("starting a step advanced it: %q", got)
	}
	before := readWorkVersion(t, s, workID)
	completion := workflowEventWithActor("authority-completion", WorkflowCompleted, workID, executor, map[string]any{
		"work_id": workID, "expected_version": before, "resulting_version": before + 1,
		"terminal_state": "completed", "final_verdict_kind": "ok", "verdict_actor_ref": executor,
		"premise_confirmed": false, "evidence_count": 0, "changed_refs_digest": "sha256:" + strings.Repeat("c", 64),
	})
	if err := applyWorkflowTestOperation(t.Context(), s, Operation{Events: []Event{completion}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): before}}); err == nil {
		t.Fatal("an execution start authorized workflow completion")
	}
	if got := instanceState(t, s, workID); got != "running" {
		t.Fatalf("refused completion changed instance state: %q", got)
	}
	var completed int
	if err := s.db.QueryRow(`SELECT count(*) FROM effect_receipts WHERE subject_id=? AND kind=?`, workID, WorkflowCompleted).Scan(&completed); err != nil {
		t.Fatal(err)
	}
	if completed != 0 || readWorkVersion(t, s, workID) != before {
		t.Fatalf("refused completion changed durable state: completed=%d", completed)
	}
}

func TestOffStepPublicProposalReplacementHoldsProgress(t *testing.T) {
	const workID = "off-step-public-proposal"
	s, version := seedBootstrapCapturedWork(t, workID)
	seedWorkflowLaw(t, s)
	actor := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/owner", SessionRef: "session/" + workID, ActorClass: ActorAgent}
	proposal := func(problem string) json.RawMessage {
		return mustJSONValue(map[string]any{"problem": problem, "affected": []string{"People"}, "stakes": "The stakes", "user_outcomes": []string{"An outcome"}})
	}
	version = issue31WorkflowActionWithPayload(t, s, workID, version, "record_proposal", "first-proposal", actor, proposal("First problem"))
	step := currentStep(t, s, workID)
	version = issue31WorkflowActionWithPayload(t, s, workID, version, "record_proposal", "replacement-proposal", actor, proposal("Replacement problem"))
	if currentStep(t, s, workID) != step || readWorkVersion(t, s, workID) != version {
		t.Fatal("proposal replacement changed workflow progress")
	}
	var problem string
	if err := s.db.QueryRow(`SELECT problem FROM workflow_proposal_records WHERE work_id=? ORDER BY work_version DESC LIMIT 1`, workID).Scan(&problem); err != nil || problem != "Replacement problem" {
		t.Fatalf("replacement proposal=%q, error=%v", problem, err)
	}
}

func TestOffStepPublicDesignReplacementHoldsProgress(t *testing.T) {
	const workID = "off-step-public-design"
	s, version := seedBootstrapCapturedWork(t, workID)
	seedWorkflowLaw(t, s)
	actor := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/owner", SessionRef: "session/" + workID, ActorClass: ActorAgent}
	version = issue31WorkflowActionWithPayload(t, s, workID, version, "record_proposal", "design-proposal", actor, json.RawMessage(`{"problem":"Problem","affected":["People"],"stakes":"Stakes","user_outcomes":["Outcome"]}`))
	version = issue31WorkflowActionWithPayload(t, s, workID, version, "record_alignment", "design-alignment", actor, json.RawMessage(`{"searched":"Inventory","outcome":"none_found"}`))
	binding := corpusArchitectureBinding()
	binding["home_domain_id"], binding["affected_domain_ids"] = "root", []string{"root"}
	approval := mustJSONValue(map[string]any{"premise": "The design serves the accepted outcome.", "decision_domains": []string{}, "outcome_predicates": []map[string]any{{"predicate_id": "predicate:design", "ordinal": 0, "outcome_kind": "exists", "outcome_payload": map[string]any{"kind": "exists", "surface": "fixture:design", "subjects": []string{"document:design"}}}}, "spec_mandate": []string{}, "law_modifies": []string{}, "architecture_binding": binding})
	version = issue31WorkflowActionWithPayload(t, s, workID, version, "approve_contract", "design-contract", actor, approval)
	design := func(approach string) json.RawMessage {
		return mustJSONValue(map[string]any{"approach": approach, "decisions": []map[string]any{{"id": "decision:design", "question": "Which approach?", "choice": "Direct", "rationale": "It meets the outcome", "rejected": []string{}}}, "touched_refs": []string{"internal/store/workflow.go"}})
	}
	version = issue31WorkflowActionWithPayload(t, s, workID, version, "record_design", "first-design", actor, design("First approach"))
	step := currentStep(t, s, workID)
	version = issue31WorkflowActionWithPayload(t, s, workID, version, "record_design", "replacement-design", actor, design("Replacement approach"))
	if currentStep(t, s, workID) != step || readWorkVersion(t, s, workID) != version {
		t.Fatal("design replacement changed workflow progress")
	}
	var approach string
	if err := s.db.QueryRow(`SELECT approach FROM workflow_design_records WHERE work_id=? ORDER BY work_version DESC LIMIT 1`, workID).Scan(&approach); err != nil || approach != "Replacement approach" {
		t.Fatalf("design=%q error=%v", approach, err)
	}
}

func TestExecutorCannotAdvanceItsStepWithAnUndeclaredCompletionAction(t *testing.T) {
	t.Parallel()
	workID := "authority-step-transition"
	s, executor, _ := seedOrdinaryExecutorAtExecution(t, workID)
	before := readWorkVersion(t, s, workID)
	completed := workflowEventWithActor("authority-step-transition-completed", WorkflowActionCompleted, workID, executor, map[string]any{
		"work_id": workID, "expected_version": before, "resulting_version": before + 1,
		"step_id": "execution", "action_id": "record_proposal", "attempt_epoch": 2,
		"result_evidence_refs": []string{}, "changed_refs": []string{}, "actor_ref": executor,
	})
	err := applyWorkflowTestOperation(t.Context(), s, Operation{Events: []Event{completed}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): before}})
	if !hasFailureKind(err, KindIllegalLifecycleTransition) {
		t.Fatalf("undeclared completion = %v, want illegal_lifecycle_transition", err)
	}
	if currentStep(t, s, workID) != "execution" || readWorkVersion(t, s, workID) != before {
		t.Fatal("refused completion advanced the step or version")
	}
}

func TestOffStepRecordCompletionRequiresOwnEffect(t *testing.T) {
	for _, action := range []string{"record_proposal", "record_design", "record_alignment", "record_diagnosis", "revise_candidates"} {
		t.Run(action, func(t *testing.T) {
			workID := "off-step-own-effect-" + action
			var s *Store
			var actor string
			switch action {
			case "record_proposal", "record_design":
				s, actor, _ = seedOrdinaryExecutorAtExecution(t, workID)
			case "revise_candidates":
				f := seedCurrentWorkflowReturnRouteFixture(t, workID, "workflow.research", "investigate")
				s = f.store
				issue31WorkflowActionWithPayload(t, s, workID, readWorkVersion(t, s, workID), action, workID+"-first", f.owner, json.RawMessage(`{"added":["candidate:one"]}`))
				actor, _ = WorkflowActorRef(f.owner)
			default:
				kind := "task"
				if action == "record_diagnosis" {
					kind = "bug"
				}
				var version int64
				s, version = seedBootstrapCapturedWork(t, workID)
				seedWorkflowLaw(t, s)
				owner := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/owner", SessionRef: "session/" + workID, ActorClass: ActorAgent}
				if kind == "task" {
					version = issue31WorkflowActionWithPayload(t, s, workID, version, "record_proposal", workID+"-proposal", owner, json.RawMessage(`{"problem":"Problem","affected":["People"],"stakes":"Stakes","user_outcomes":["Outcome"]}`))
				}
				payload := json.RawMessage(`{"searched":"Search","outcome":"none_found"}`)
				if kind == "bug" {
					payload = json.RawMessage(`{"reproduction":"Reproduction","searched":"Search","outcome":"none_found","root_cause":"Cause"}`)
				}
				issue31WorkflowActionWithPayload(t, s, workID, version, action, workID+"-first", owner, payload)
				actor, _ = WorkflowActorRef(owner)
			}
			records, err := workflowCurrentRecords(t.Context(), s.db, workID)
			if err != nil || !records[action] {
				t.Fatalf("current record %s absent: %v", action, err)
			}
			before := readWorkVersion(t, s, workID)
			step := currentStep(t, s, workID)
			tx, err := s.db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			epoch, err := workflowActionStartEpochForDispatch(t.Context(), tx, workID, step, false)
			_ = tx.Rollback()
			if err != nil {
				t.Fatal(err)
			}
			event := workflowEventWithActor("bare-"+action, WorkflowActionCompleted, workID, actor, map[string]any{
				"work_id": workID, "expected_version": before, "resulting_version": before + 1,
				"step_id": step, "action_id": action, "attempt_epoch": epoch,
				"result_evidence_refs": []string{}, "changed_refs": []string{}, "actor_ref": actor,
			})
			err = applyWorkflowTestOperation(t.Context(), s, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): before}})
			if err == nil {
				t.Fatal("bare off-step record completion was accepted")
			}
			if currentStep(t, s, workID) != step || readWorkVersion(t, s, workID) != before {
				t.Fatal("refused completion changed progress or version")
			}
		})
	}
}

func TestOffStepNeverRecordedUndeclaredCompletion(t *testing.T) {
	const workID = "off-step-never-recorded"
	s, actor, _ := seedOrdinaryExecutorAtExecution(t, workID)
	records, err := workflowCurrentRecords(t.Context(), s.db, workID)
	if err != nil || records["record_finding"] {
		t.Fatalf("unexpected record: %v %v", records, err)
	}
	before := readWorkVersion(t, s, workID)
	event := workflowTypedEvent("never-recorded", WorkflowActionCompleted, workID, actor, time.Unix(10, 0), before, map[string]any{
		"step_id": "execution", "action_id": "record_finding", "attempt_epoch": 2, "actor_ref": actor,
		"result_evidence_refs": []string{}, "changed_refs": []string{},
	})
	err = applyWorkflowTestOperation(t.Context(), s, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): before}})
	if !hasFailureKind(err, KindIllegalLifecycleTransition) {
		t.Fatalf("undeclared completion=%v", err)
	}
}

func TestOffStepDeliveryRequiresAssertion(t *testing.T) {
	const workID = "off-step-delivery-assertion"
	f := qCurrentAcceptanceWithDelivery(t, workID, "file:internal/store/output.go")
	s := f.store
	actor, err := WorkflowActorRef(f.owner)
	if err != nil {
		t.Fatal(err)
	}
	before := readWorkVersion(t, s, workID)
	step := currentStep(t, s, workID)
	records, err := workflowCurrentRecords(t.Context(), s.db, workID)
	if err != nil || !records["record_delivery"] {
		t.Fatalf("delivery record missing: %v", err)
	}
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	epoch, err := workflowActionStartEpochForDispatch(t.Context(), tx, workID, step, false)
	_ = tx.Rollback()
	if err != nil {
		t.Fatal(err)
	}
	event := workflowTypedEvent("bare-delivery", WorkflowActionCompleted, workID, actor, time.Unix(10, 0), before, map[string]any{
		"step_id": step, "action_id": "record_delivery", "attempt_epoch": epoch, "actor_ref": actor,
		"result_evidence_refs": []string{}, "changed_refs": []string{workID},
	})
	if err := applyWorkflowTestOperation(t.Context(), s, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): before}}); !hasFailureKind(err, KindInvalidPayload) {
		t.Fatalf("bare delivery completion = %v, want invalid_payload", err)
	}
	if currentStep(t, s, workID) != step || readWorkVersion(t, s, workID) != before {
		t.Fatal("refused delivery changed progress")
	}
}

func TestOffStepReviewRequiresOwnEvidence(t *testing.T) {
	j := reviewGateRefineFixture(t, "off-step-review-evidence", "workflow.implementation")
	reviewGateCompleteReview(t, j, "ready_for_final_proof")
	j.bindVerification(t, j.observeRun(t, "review-evidence"))
	if err := reviewGateRecordDelivery(t, j.s, j.workID, j.fixture.owner); err != nil {
		t.Fatal(err)
	}
	step := currentStep(t, j.s, j.workID)
	records, err := workflowCurrentRecords(t.Context(), j.s.db, j.workID)
	if err != nil || !records["record_review"] {
		t.Fatalf("review record missing: %v", err)
	}
	actor, err := WorkflowActorRef(j.fixture.owner)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := j.s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	epoch, err := workflowActionStartEpochForDispatch(t.Context(), tx, j.workID, step, false)
	_ = tx.Rollback()
	if err != nil {
		t.Fatal(err)
	}
	version := readWorkVersion(t, j.s, j.workID)
	report := currentReviewBlockForTest(t, j.s, j.workID, "ready_for_final_proof")
	event := workflowTypedEvent("bare-review", WorkflowActionCompleted, j.workID, actor, time.Unix(15, 0), version, map[string]any{"step_id": step, "action_id": "record_review", "attempt_epoch": epoch, "actor_ref": actor, "review": report, "result_evidence_refs": []string{}, "changed_refs": []string{j.workID}})
	err = applyWorkflowTestOperation(t.Context(), j.s, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, j.workID): version}})
	if !hasFailureKind(err, KindIllegalLifecycleTransition) {
		t.Fatalf("review completion=%v", err)
	}
	if currentStep(t, j.s, j.workID) != step || readWorkVersion(t, j.s, j.workID) != version {
		t.Fatal("refused review changed progress")
	}
}

func TestOffStepProposalEffectMustMatchCompletion(t *testing.T) {
	for _, tc := range []struct {
		name, semanticActor, action, step string
		epoch, versionOffset              int64
		otherWork                         bool
		wantSuccess                       bool
	}{
		{name: "matching", action: "record_proposal", step: "execution", epoch: 2, wantSuccess: true},
		{name: "other actor", semanticActor: "owner", action: "record_proposal", step: "execution", epoch: 2},
		{name: "other work", action: "record_proposal", step: "execution", epoch: 2, otherWork: true, versionOffset: -1},
		{name: "other action", action: "record_design", step: "execution", epoch: 2},
		{name: "wrong step", action: "record_proposal", step: "design", epoch: 2},
		{name: "wrong attempt", action: "record_proposal", step: "execution", epoch: 1},
		{name: "wrong version", action: "record_proposal", step: "execution", epoch: 2, versionOffset: -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workID := "off-step-pair-" + strings.ReplaceAll(tc.name, " ", "-")
			s, actor, owner := seedOrdinaryExecutorAtExecution(t, workID)
			before := readWorkVersion(t, s, workID)
			semanticActor := actor
			if tc.semanticActor == "owner" {
				var err error
				semanticActor, err = WorkflowActorRef(owner)
				if err != nil {
					t.Fatal(err)
				}
			}
			semanticWork, semanticVersion := workID, before
			versions := map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): before}
			if tc.otherWork {
				semanticWork = workID + "-other"
				seedWork(t, s, semanticWork)
				semanticVersion = readWorkVersion(t, s, semanticWork)
				versions[VersionRef(SubjectWorkItem, semanticWork)] = semanticVersion
			}
			semantic := workflowTypedEvent("proposal-effect-"+workID, WorkflowProposalRecorded, semanticWork, semanticActor, time.Unix(10, 0), semanticVersion, map[string]any{
				"problem": "A problem", "affected": []string{"People"}, "stakes": "Stakes", "user_outcomes": []string{"An outcome"},
			})
			completion := workflowTypedEvent("proposal-completion-"+workID, WorkflowActionCompleted, workID, actor, time.Unix(10, 0), before+1+tc.versionOffset, map[string]any{
				"step_id": tc.step, "action_id": tc.action, "attempt_epoch": tc.epoch, "actor_ref": actor,
				"result_evidence_refs": []string{}, "changed_refs": []string{workID},
			})
			err := applyWorkflowTestOperation(t.Context(), s, Operation{Events: []Event{semantic, completion}, ExpectedVersions: versions})
			if (err == nil) != tc.wantSuccess {
				t.Fatalf("completion error=%v, success=%v", err, tc.wantSuccess)
			}
			wantVersion := before
			if tc.wantSuccess {
				wantVersion += 2
			}
			if currentStep(t, s, workID) != "execution" || readWorkVersion(t, s, workID) != wantVersion {
				t.Fatal("unexpected progress or version")
			}
		})
	}
}

func TestOffStepProposalCannotReusePreviousOperationEffect(t *testing.T) {
	const workID = "off-step-previous-operation"
	s, actor, _ := seedOrdinaryExecutorAtExecution(t, workID)
	before := readWorkVersion(t, s, workID)
	semantic := workflowTypedEvent("prior-proposal-effect", WorkflowProposalRecorded, workID, actor, time.Unix(10, 0), before, map[string]any{
		"problem": "A problem", "affected": []string{"People"}, "stakes": "Stakes", "user_outcomes": []string{"An outcome"},
	})
	if err := applyWorkflowTestOperation(t.Context(), s, Operation{Events: []Event{semantic}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): before}}); err != nil {
		t.Fatal(err)
	}
	completion := workflowTypedEvent("later-proposal-completion", WorkflowActionCompleted, workID, actor, time.Unix(11, 0), before+1, map[string]any{
		"step_id": "execution", "action_id": "record_proposal", "attempt_epoch": 2, "actor_ref": actor,
		"result_evidence_refs": []string{}, "changed_refs": []string{workID},
	})
	err := applyWorkflowTestOperation(t.Context(), s, Operation{Events: []Event{completion}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): before + 1}})
	if !hasFailureKind(err, KindIllegalLifecycleTransition) {
		t.Fatalf("prior effect completion=%v", err)
	}
	if currentStep(t, s, workID) != "execution" || readWorkVersion(t, s, workID) != before+1 {
		t.Fatal("refused completion changed progress")
	}
}

func TestWorkflowActionStartedV2AuthorizesActorStepAndEpoch(t *testing.T) {
	t.Parallel()
	workID := "authority-start-guards"
	s, executor, owner := seedOrdinaryExecutorAtExecution(t, workID)
	ownerRef, err := WorkflowActorRef(owner)
	if err != nil {
		t.Fatal(err)
	}
	currentVersion := int64(12)
	beforeStarts := countWorkflowActionStarts(t, s, workID)
	start := func(id, step, action, actor string, epoch, version int64) Event {
		return workflowEventWithActor(id, WorkflowActionStarted, workID, executor, map[string]any{
			"work_id": workID, "expected_version": version, "resulting_version": version + 1,
			"step_id": step, "action_id": action, "attempt_epoch": epoch,
			"accepted_inputs_digest": "sha256:" + strings.Repeat("f", 64), "idempotency_identity": id,
			"actor_ref": actor, "execution_model": "model:fixture",
		})
	}
	for _, test := range []struct {
		name, step, action, actor string
		epoch                     int64
		kind                      FailureKind
	}{
		{"forged actor", "execution", "start_execution", ownerRef, 3, KindUnauthorized},
		{"future step", "acceptance", "record_verdict", executor, 3, KindIllegalLifecycleTransition},
		{"undeclared current-step action", "execution", "record_proposal", executor, 3, KindIllegalLifecycleTransition},
		{"duplicate epoch", "execution", "start_execution", executor, 1, KindIllegalLifecycleTransition},
		{"jumped epoch", "execution", "start_execution", executor, 4, KindIllegalLifecycleTransition},
	} {
		t.Run(test.name, func(t *testing.T) {
			assertRejectedActionStart(t, s, start(strings.ReplaceAll(test.name, " ", "-"), test.step, test.action, test.actor, test.epoch, currentVersion), currentVersion, test.kind, "execution", currentVersion, beforeStarts)
		})
	}
	exact := start("exact-retry-start", "execution", "start_execution", executor, 3, currentVersion)
	if err := applyWorkflowTestOperation(t.Context(), s, Operation{Events: []Event{exact}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): currentVersion}}); err != nil {
		t.Fatalf("exact next epoch rejected: %v", err)
	}
	if currentStep(t, s, workID) != "execution" || readWorkVersion(t, s, workID) != currentVersion+1 || countWorkflowActionStarts(t, s, workID) != beforeStarts+1 {
		t.Fatal("exact next epoch changed the step or failed to persist exactly one start/version")
	}
	stale := start("stale-retry-start", "execution", "start_execution", executor, 3, currentVersion+1)
	assertRejectedActionStart(t, s, stale, currentVersion+1, KindIllegalLifecycleTransition, "execution", currentVersion+1, beforeStarts+1)
}

func assertRejectedActionStart(t *testing.T, s *Store, event Event, expectedVersion int64, wantKind FailureKind, wantStep string, wantVersion int64, wantStarts int) {
	t.Helper()
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = applyWorkflowOperationTx(t.Context(), tx, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, event.SubjectID): expectedVersion}}, newWriteScope(tx))
	_ = tx.Rollback()
	if !hasFailureKind(err, wantKind) {
		t.Fatalf("action start = %v, want %s", err, wantKind)
	}
	if currentStep(t, s, event.SubjectID) != wantStep || readWorkVersion(t, s, event.SubjectID) != wantVersion || countWorkflowActionStarts(t, s, event.SubjectID) != wantStarts {
		t.Fatal("refused action start changed step, version or start count")
	}
}

func countWorkflowActionStarts(t *testing.T, s *Store, workID string) int {
	t.Helper()
	var count int
	if err := s.db.QueryRow(`SELECT count(*) FROM effect_receipts WHERE subject_id=? AND kind=?`, workID, WorkflowActionStarted).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestWorkflowActionCheckpointedV2BindsActorStepKindAndEpoch(t *testing.T) {
	t.Parallel()
	workID := "authority-checkpoint-guards"
	s, executor, owner := seedOrdinaryExecutorAtExecutionWithCheckpoint(t, workID, false)
	ownerRef, err := WorkflowActorRef(owner)
	if err != nil {
		t.Fatal(err)
	}
	baseVersion := int64(11)
	baseCheckpoints := countWorkflowCheckpoints(t, s, workID)
	checkpoint := func(id, eventActor, payloadActor, stepID, stepKind string, epoch int64) Event {
		return workflowEventWithActor(id, WorkflowActionCheckpointed, workID, eventActor, map[string]any{
			"work_id": workID, "expected_version": baseVersion, "resulting_version": baseVersion + 1,
			"step_id": stepID, "step_kind": stepKind, "attempt_epoch": epoch,
			"checkpoint_payload": map[string]any{"action_id": "checkpoint_execution", "cursor": id},
			"resume_cursor":      "cursor:" + id, "actor_ref": payloadActor, "request_id": "request:" + id,
			"checkpoint_id": "checkpoint:" + id, "accepted_inputs_digest": "sha256:" + strings.Repeat("i", 64), "idempotency_identity": id,
		})
	}
	assertRejectedCheckpoint(t, s, workID, checkpoint("checkpoint-forged-actor", executor, ownerRef, "execution", "external_effect", 2), baseVersion, KindUnauthorized, baseCheckpoints)
	assertRejectedCheckpoint(t, s, workID, checkpoint("checkpoint-wrong-step", executor, executor, "acceptance", "external_effect", 2), baseVersion, KindIllegalLifecycleTransition, baseCheckpoints)
	assertRejectedCheckpoint(t, s, workID, checkpoint("checkpoint-wrong-kind", executor, executor, "execution", "cross_authority", 2), baseVersion, KindIllegalLifecycleTransition, baseCheckpoints)
	assertRejectedCheckpoint(t, s, workID, checkpoint("checkpoint-wrong-epoch", executor, executor, "execution", "external_effect", 3), baseVersion, KindIllegalLifecycleTransition, baseCheckpoints)
	assertRejectedCheckpoint(t, s, workID, checkpoint("checkpoint-non-executor", ownerRef, ownerRef, "execution", "external_effect", 2), baseVersion, KindUnauthorized, baseCheckpoints)
	valid := checkpoint("checkpoint-valid", executor, executor, "execution", "external_effect", 2)
	if err := applyWorkflowTestOperation(t.Context(), s, Operation{Events: []Event{valid}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): baseVersion}}); err != nil {
		t.Fatalf("valid executor checkpoint rejected: %v", err)
	}
	if currentStep(t, s, workID) != "execution" || readWorkVersion(t, s, workID) != baseVersion+1 || countWorkflowCheckpoints(t, s, workID) != baseCheckpoints+1 {
		t.Fatal("valid checkpoint changed the step or failed to persist exactly one checkpoint/version")
	}
}

func assertRejectedCheckpoint(t *testing.T, s *Store, workID string, event Event, expectedVersion int64, wantKind FailureKind, wantCheckpoints int) {
	t.Helper()
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = applyWorkflowOperationTx(t.Context(), tx, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): expectedVersion}}, newWriteScope(tx))
	_ = tx.Rollback()
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != wantKind {
		t.Fatalf("checkpoint failure=%v, want %s", err, wantKind)
	}
	if currentStep(t, s, workID) != "execution" || readWorkVersion(t, s, workID) != expectedVersion || countWorkflowCheckpoints(t, s, workID) != wantCheckpoints {
		t.Fatal("refused checkpoint changed step, version or checkpoint count")
	}
}

func countWorkflowCheckpoints(t *testing.T, s *Store, workID string) int {
	t.Helper()
	var count int
	if err := s.db.QueryRow(`SELECT count(*) FROM workflow_checkpoints WHERE work_id=?`, workID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestWorkflowActionCompletedV2BindsPayloadActorToEventActor(t *testing.T) {
	t.Parallel()
	workID := "authority-completed-actor"
	s, executor, owner := seedOrdinaryExecutorAtExecution(t, workID)
	ownerRef, err := WorkflowActorRef(owner)
	if err != nil {
		t.Fatal(err)
	}
	before := countWorkflowActionCompleted(t, s, workID)
	completed := workflowEventWithActor("forged-completed-actor", WorkflowActionCompleted, workID, executor, map[string]any{
		"work_id": workID, "expected_version": 12, "resulting_version": 13,
		"step_id": "execution", "action_id": "checkpoint_execution", "attempt_epoch": 1,
		"result_evidence_refs": []string{}, "changed_refs": []string{}, "actor_ref": ownerRef,
	})
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = applyWorkflowOperationTx(t.Context(), tx, Operation{Events: []Event{completed}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): 12}}, newWriteScope(tx))
	_ = tx.Rollback()
	if !hasFailureKind(err, KindUnauthorized) {
		t.Fatalf("forged completed actor = %v, want unauthorized", err)
	}
	if currentStep(t, s, workID) != "execution" || readWorkVersion(t, s, workID) != 12 || countWorkflowActionCompleted(t, s, workID) != before {
		t.Fatal("forged completion changed step, version or completion count")
	}
}

func TestWorkflowActionFailedV2BindsCurrentExecutorAttempt(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, step                       string
		epoch                            int64
		forged, nonexecutor, recoverable bool
		kind                             FailureKind
		state                            string
	}{
		{name: "forged actor", step: "execution", epoch: 2, forged: true, recoverable: true, kind: KindUnauthorized},
		{name: "wrong step", step: "acceptance", epoch: 2, recoverable: true, kind: KindIllegalLifecycleTransition},
		{name: "stale epoch", step: "execution", epoch: 3, recoverable: true, kind: KindIllegalLifecycleTransition},
		{name: "non-executor actor", step: "execution", epoch: 2, nonexecutor: true, recoverable: true, kind: KindUnauthorized},
		{name: "recoverable failure keeps workflow running", step: "execution", epoch: 2, recoverable: true, state: "running"},
		{name: "nonrecoverable failure blocks workflow", step: "execution", epoch: 2, state: "blocked"},
	} {
		t.Run(test.name, func(t *testing.T) {
			workID := "authority-failed-" + strings.ReplaceAll(test.name, " ", "-")
			s, executor, owner := seedOrdinaryExecutorAtExecution(t, workID)
			ownerRef, err := WorkflowActorRef(owner)
			if err != nil {
				t.Fatal(err)
			}
			eventActor, payloadActor := executor, executor
			if test.forged {
				payloadActor = ownerRef
			}
			if test.nonexecutor {
				eventActor, payloadActor = ownerRef, ownerRef
			}
			event := actionFailedFixture("failed-"+workID, workID, eventActor, payloadActor, test.step, test.epoch, test.recoverable)
			if test.kind != "" {
				assertRejectedActionFailed(t, s, event, test.kind)
				return
			}
			if err := applyWorkflowTestOperation(t.Context(), s, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): 12}}); err != nil {
				t.Fatalf("current executor failure rejected: %v", err)
			}
			if instanceState(t, s, workID) != test.state || readWorkVersion(t, s, workID) != 13 {
				t.Fatalf("failure did not persist expected state %s/version 13", test.state)
			}
		})
	}
}

func actionFailedFixture(eventID, workID, eventActor, payloadActor, stepID string, epoch int64, recoverable bool) Event {
	return actionFailedFixtureWithKind(eventID, workID, eventActor, payloadActor, stepID, epoch, recoverable, KindTimeout)
}

func actionFailedFixtureWithKind(eventID, workID, eventActor, payloadActor, stepID string, epoch int64, recoverable bool, kind FailureKind) Event {
	return workflowEventWithActor(eventID, WorkflowActionFailed, workID, eventActor, map[string]any{
		"work_id": workID, "expected_version": 12, "resulting_version": 13, "step_id": stepID, "attempt_epoch": epoch,
		"failure_kind": string(kind), "recoverable": recoverable, "actor_ref": payloadActor,
	})
}

func assertRejectedActionFailed(t *testing.T, s *Store, event Event, wantKind FailureKind) {
	t.Helper()
	if err := applyWorkflowTestOperation(t.Context(), s, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, event.SubjectID): 12}}); !hasFailureKind(err, wantKind) {
		t.Fatalf("action_failed = %v, want %s", err, wantKind)
	}
	if instanceState(t, s, event.SubjectID) != "running" || readWorkVersion(t, s, event.SubjectID) != 12 {
		t.Fatal("refused failure changed state or version")
	}
	var events int
	if err := s.db.QueryRow(`SELECT count(*) FROM effect_receipts WHERE subject_id=? AND kind=?`, event.SubjectID, WorkflowActionFailed).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 0 {
		t.Fatalf("refused failure persisted %d events", events)
	}
}

func countWorkflowActionCompleted(t *testing.T, s *Store, workID string) int {
	t.Helper()
	var count int
	if err := s.db.QueryRow(`SELECT count(*) FROM effect_receipts WHERE subject_id=? AND kind=?`, workID, WorkflowActionCompleted).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func applyStartForTest(t *testing.T, s *Store, workID string, owner WorkflowActor, expectedVersion int64, operationID string) WorkflowActionExecutionResult {
	t.Helper()
	return applyWorkflowActionForTest(t, s, workID, owner, expectedVersion, "start_execution", "s", operationID, time.Unix(4, 0).UTC())
}

func applyWorkflowActionForTest(t *testing.T, s *Store, workID string, owner WorkflowActor, expectedVersion int64, actionID, digestChar, operationID string, now time.Time) WorkflowActionExecutionResult {
	t.Helper()
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := applyWorkflowActionRawTx(ctx, tx, newWriteScope(tx), BuiltinWorkflowRegistry(), WorkflowActionExecutionRequest{
		WorkID: workID, ExpectedVersion: expectedVersion, ActionID: actionID, Payload: mustJSONValue(map[string]any{}), Actor: owner,
		AcceptedInputsDigest: "sha256:" + strings.Repeat(digestChar, 64), IdempotencyIdentity: operationID, OperationID: operationID,
		PrincipalRef: owner.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: operationID, RequestID: "request:" + operationID, ContractDigest: testManifestDigest, Now: now,
	})
	if err != nil {
		tx.Rollback()
		t.Fatalf("%s: %v", actionID, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return result
}

func workflowActionCompletedFixture(eventID, workID, actor string, expected int64, stepID, actionID string) Event {
	return workflowEventWithActor(eventID, WorkflowActionCompleted, workID, actor, map[string]any{
		"work_id": workID, "expected_version": expected, "resulting_version": expected + 1,
		"step_id": stepID, "action_id": actionID, "attempt_epoch": 1,
		"result_evidence_refs": []string{}, "changed_refs": []string{workID}, "actor_ref": actor,
	})
}

func seedOrdinaryExecutorAtExecution(t *testing.T, workID string) (*Store, string, WorkflowActor) {
	t.Helper()
	return seedOrdinaryExecutorAtExecutionWithCheckpoint(t, workID, true)
}

func seedOrdinaryExecutorAtExecutionWithCheckpoint(t *testing.T, workID string, checkpoint bool) (*Store, string, WorkflowActor) {
	t.Helper()
	s := openTemp(t)
	seedWork(t, s, workID)
	seedWorkflowLaw(t, s)
	executor := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/executor", SessionRef: "session/" + workID, ActorClass: ActorAgent}
	owner := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/owner", SessionRef: "session/" + workID, ActorClass: ActorAgent}
	executorRef, err := WorkflowActorRef(executor)
	if err != nil {
		t.Fatal(err)
	}
	ownerRef, err := WorkflowActorRef(owner)
	if err != nil {
		t.Fatal(err)
	}
	definition := workflowFixtureDefinition(t, 1)
	setup := []Event{
		workflowEvent("executor-actor-"+workID, WorkflowActorRecorded, workID, map[string]any{"work_id": workID, "expected_version": 2, "resulting_version": 3, "actor_ref": executorRef, "principal_ref": executor.PrincipalRef, "client_ref": executor.ClientRef, "agent_ref": executor.AgentRef, "session_ref": executor.SessionRef, "actor_class": "agent"}),
		workflowEvent("owner-actor-"+workID, WorkflowActorRecorded, workID, map[string]any{"work_id": workID, "expected_version": 3, "resulting_version": 4, "actor_ref": ownerRef, "principal_ref": owner.PrincipalRef, "client_ref": owner.ClientRef, "agent_ref": owner.AgentRef, "session_ref": owner.SessionRef, "actor_class": "agent"}),
		workflowEvent("definition-"+workID, WorkflowDefinitionSelected, workID, map[string]any{"work_id": workID, "expected_version": 4, "resulting_version": 5, "ref": definition.Definition.Ref, "version": 1, "digest": definition.Digest, "work_kind": workflowFixtureWorkKind}),
		workflowActionCompletedFixture("proposal-"+workID, workID, executorRef, 5, "proposal", "record_proposal"),
		workflowActionCompletedFixture("discovery-"+workID, workID, executorRef, 6, "discovery", "record_discovery"),
		workflowActionCompletedFixture("design-"+workID, workID, executorRef, 7, "design", "record_design"),
		workflowActionCompletedFixture("planning-"+workID, workID, executorRef, 8, "planning", "approve_contract"),
	}
	for epoch := int64(1); epoch <= 2; epoch++ {
		version := 8 + epoch
		id := "start-" + workID + "-" + itoa(int(epoch))
		setup = append(setup, workflowEventWithActor(id, WorkflowActionStarted, workID, executorRef, map[string]any{
			"work_id": workID, "expected_version": version, "resulting_version": version + 1,
			"step_id": "execution", "action_id": "start_execution", "attempt_epoch": epoch,
			"accepted_inputs_digest": "sha256:" + strings.Repeat("a", 64), "idempotency_identity": id,
			"actor_ref": executorRef, "execution_model": "model:fixture",
		}))
	}
	if checkpoint {
		setup = append(setup, workflowEventWithActor("seed-checkpoint-"+workID, WorkflowActionCheckpointed, workID, executorRef, map[string]any{
			"work_id": workID, "expected_version": 11, "resulting_version": 12, "step_id": "execution", "step_kind": "external_effect", "attempt_epoch": 2,
			"checkpoint_payload": map[string]any{"action_id": "checkpoint_execution", "cursor": "seed"}, "resume_cursor": "cursor:seed", "actor_ref": executorRef,
			"request_id": "request:seed-" + workID, "checkpoint_id": "checkpoint:seed-" + workID,
			"accepted_inputs_digest": "sha256:" + strings.Repeat("a", 64), "idempotency_identity": "seed-" + workID,
		}))
	}
	if err := applyWorkflowTestOperation(t.Context(), s, Operation{Events: setup, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): 2}}); err != nil {
		t.Fatal(err)
	}
	return s, executorRef, owner
}

func workflowActorForRef(t *testing.T, s *Store, actorRef string) WorkflowActor {
	t.Helper()
	var actor WorkflowActor
	if err := s.db.QueryRow(`SELECT actor_ref,principal_ref,client_ref,agent_ref,session_ref,actor_class FROM workflow_actors WHERE actor_ref=?`, actorRef).Scan(&actor.ActorRef, &actor.PrincipalRef, &actor.ClientRef, &actor.AgentRef, &actor.SessionRef, &actor.ActorClass); err != nil {
		t.Fatal(err)
	}
	return actor
}

func readWorkVersion(t *testing.T, s *Store, workID string) int64 {
	t.Helper()
	var version int64
	if err := s.db.QueryRow(`SELECT version FROM work_items WHERE id=?`, workID).Scan(&version); err != nil {
		t.Fatal(err)
	}
	return version
}

func currentStep(t *testing.T, s *Store, workID string) string {
	t.Helper()
	var step string
	if err := s.db.QueryRow(`SELECT current_step FROM workflow_instances WHERE work_id=?`, workID).Scan(&step); err != nil {
		t.Fatal(err)
	}
	return step
}

func instanceState(t *testing.T, s *Store, workID string) string {
	t.Helper()
	var state string
	if err := s.db.QueryRow(`SELECT instance_state FROM workflow_instances WHERE work_id=?`, workID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	return state
}

package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"
)

func recordedRejectionHoldJournal(t *testing.T, workID, finalAction string, sourceVersion int) *recordedAdmissionWork {
	t.Helper()
	w := recordedAdmissionStartWork(t, workID)
	executor := w.recordActor(t, "agent", recordedAdmissionExecutorAgent)
	owner := w.recordActor(t, "agent", recordedAdmissionOwnerAgent)
	lane := w.recordActor(t, "agent", recordedAdmissionLaneAgent)
	w.selectDefinition(t, "break_fix", "workflow.break_fix", 15, recordedPinBreakFixV15)
	w.approveContract(t, true)
	for _, step := range recordedAdmissionWalkWithAlignment(recordedAdmissionBreakFixWalk, "break_fix", 15) {
		if step.actionID == "record_alignment" {
			w.journal.append("workflow.backlog_alignment_recorded", "work_item", workID, executor, 1, map[string]any{
				"expected_version": w.version, "resulting_version": w.version + 1,
				"outcome": "none_found", "searched": "synthetic rejection replay", "work_id": workID,
			})
			w.version++
		}
		w.completeAction(t, step.actionID, step.stepID, executor, 1, nil)
	}
	w.completeAction(t, "record_delivery", "repair", executor, 1, nil)
	const epoch = int64(10)
	attemptID := "attempt/" + workID
	laneVersion, laneDigest := registeredLaneIdentity(t, "research")
	for startedEpoch := int64(1); startedEpoch <= epoch; startedEpoch++ {
		w.startAction(t, "dispatch_worker", "refine", owner, startedEpoch, fmt.Sprintf("synthetic:%s:dispatch:%d", workID, startedEpoch))
	}
	w.dispatchCompletion(t, "refine", owner, epoch, attemptID, laneVersion, laneDigest)
	w.workerDispatched(t, attemptID, lane, laneVersion, laneDigest)
	w.journal.append("worker.completed", "work_item", workID, "worker:test", 1, map[string]any{
		"attempt_id": attemptID, "readback_model": "synthetic/model-a", "report_schema_version": "1.1",
	})
	fields := map[string]any{"attempt_epoch": epoch, "worker_attempt_id": attemptID}
	rejection := map[string]any{
		"attempt_epoch": epoch, "worker_attempt_id": attemptID,
		"correction_diagnosis": "the synthetic result needs correction", "correction_strategy": "dispatch fresh research",
		"correction_predicate_ids": []string{"predicate:primary"}, "correction_evidence_refs": []string{"evidence:synthetic-rejection"},
	}
	w.completeAction(t, "reject_worker_result", "refine", owner, 2, rejection)
	if finalAction == "reject_worker_result" {
		fields = rejection
	}
	w.completeAction(t, finalAction, "refine", owner, sourceVersion, fields)
	return w
}

func TestRecordedRejectionHoldReplaysAcceptedResult(t *testing.T) {
	for _, sourceVersion := range []int{2, 5} {
		t.Run(fmt.Sprintf("payload_v%d", sourceVersion), func(t *testing.T) {
			w := recordedRejectionHoldJournal(t, fmt.Sprintf("recorded-rejection-accept-%d", sourceVersion), "accept_worker_result", sourceVersion)
			s := recordedAdmissionRequireFold(t, w, nil)
			defer s.Close()
			if got := currentStep(t, s, w.workID); got != "delivery" {
				t.Fatalf("recorded acceptance step = %q, want delivery", got)
			}
			if got := readWorkVersion(t, s, w.workID); got != w.version {
				t.Fatalf("recorded acceptance version = %d, want %d", got, w.version)
			}
			recordedAdmissionRebuildStable(t, s, w.journal)
			recordedAdmissionBackupRoundtrip(t, s, w.journal)
		})
	}
}

func TestRecordedRejectionHoldLiveAcceptStillRefuses(t *testing.T) {
	w := recordedRejectionHoldJournal(t, "recorded-rejection-live", "accept_worker_result", 2)
	prefix := &recordedAdmissionJournal{workID: w.workID, events: w.journal.events[:len(w.journal.events)-1]}
	s := restoreRecordedAdmissionJournal(t, prefix, nil)
	defer s.Close()
	if err := RebuildFromLog(context.Background(), s); err != nil {
		t.Fatalf("rebuild the rejected-result prefix: %v", err)
	}
	target := w.journal.events[len(w.journal.events)-1]
	event := Event{EventID: target.eventID, Kind: target.kind, SubjectType: SubjectWorkItem, SubjectID: w.workID,
		Actor: target.actor, OccurredAt: time.Unix(100, 0).UTC(), PayloadVersion: target.payloadVersion, Payload: target.payload}
	err := applyWorkflowTestOperationDirect(context.Background(), s, Operation{Events: []Event{event},
		ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, w.workID): w.version - 1}})
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindIllegalLifecycleTransition || failure.Detail != "an unconsumed rejected worker result holds the step's advance" {
		t.Fatalf("live acceptance = %v, want the unconsumed-rejection hold", err)
	}
	if got := currentStep(t, s, w.workID); got != "refine" {
		t.Fatalf("refused live acceptance step = %q, want refine", got)
	}
	if got := readWorkVersion(t, s, w.workID); got != w.version-1 {
		t.Fatalf("refused live acceptance version = %d, want %d", got, w.version-1)
	}
	recordedAdmissionAssertLogIntact(t, s, prefix)
}

func TestRecordedRejectionEligibilityDoesNotReadmitHistory(t *testing.T) {
	for _, owner := range []string{"off_step", "attempt"} {
		t.Run(owner, func(t *testing.T) {
			w := recordedRejectionHoldJournal(t, "recorded-rejection-"+owner, "reject_worker_result", 2)
			prefix := &recordedAdmissionJournal{workID: w.workID, events: w.journal.events[:len(w.journal.events)-1]}
			s := restoreRecordedAdmissionJournal(t, prefix, nil)
			defer s.Close()
			if err := RebuildFromLog(context.Background(), s); err != nil {
				t.Fatalf("rebuild the recorded rejection prefix: %v", err)
			}
			target := w.journal.events[len(w.journal.events)-1]
			event := Event{Seq: target.seq, EventID: target.eventID, Kind: target.kind, SubjectType: SubjectWorkItem,
				SubjectID: w.workID, Actor: target.actor, OccurredAt: time.Unix(100, 0).UTC(), PayloadVersion: target.payloadVersion, Payload: target.payload}
			var p workflowActionCompletedPayload
			if err := json.Unmarshal(event.Payload, &p); err != nil {
				t.Fatal(err)
			}
			tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			entry, err := VerifyWorkflowInstanceDefinitionTx(context.Background(), tx, BuiltinWorkflowRegistry(), w.workID)
			if err != nil {
				t.Fatal(err)
			}
			check := func(ctx context.Context) error {
				if owner == "off_step" {
					return admitWorkflowActionOffStep(ctx, tx, event, p, entry, "refine", BuiltinWorkflowRegistry())
				}
				return validateWorkerAttemptAction(ctx, tx, event, p, entry.Definition, "refine", "completed")
			}
			var failure *Failure
			if err := check(context.Background()); !errors.As(err, &failure) || failure.Kind != KindIllegalLifecycleTransition || failure.Detail != "worker result rejection does not name an eligible completed result" {
				t.Fatalf("live rejection = %v, want the exact completed-result eligibility refusal", err)
			}
			if err := check(workflowReplayContext(context.Background())); err != nil {
				t.Fatalf("recorded rejection was re-admitted through live eligibility: %v", err)
			}
		})
	}
}

func TestRecordedRejectionKeepsOriginalExecutor(t *testing.T) {
	const workID = "recorded-rejection-original-executor"
	f := seedCrossAttemptFixture(t, workID)
	f.buildTwoAttemptHistory(t)
	epoch := crossAttemptDispatchEpoch(t, f.s, workID, f.attemptR)
	original := crossAttemptDispatchedLaneActor(t, f.s, workID, f.attemptR)
	later := crossAttemptDispatchedLaneActor(t, f.s, workID, f.attemptV)
	var executor string
	if err := f.s.DatabaseForTesting().QueryRow(`SELECT execution_actor_ref FROM workflow_instances WHERE work_id=?`, workID).Scan(&executor); err != nil {
		t.Fatal(err)
	}
	if original == later || executor != later {
		t.Fatalf("executor witness = %q/%q/%q, want distinct dispatch actors and the later executor", original, later, executor)
	}
	event := f.forgedRejectCompletionEvent(t, "recorded-original-executor-rejection", f.attemptR, epoch, later, 100)
	if err := f.applyWitnessEvent(t, event); err != nil {
		t.Fatalf("reject an independent result as the later executor: %v", err)
	}
	version := readWorkVersion(t, f.s, workID)
	payload := crossAttemptEventPayload(t, f.s, workID, WorkflowActionCompleted, f.attemptR)
	if err := RebuildFromLog(context.Background(), f.s); err != nil {
		t.Fatalf("replay must keep the target's original executor: %v", err)
	}
	if got := readWorkVersion(t, f.s, workID); got != version {
		t.Fatalf("replayed rejection version = %d, want %d", got, version)
	}
	if got := crossAttemptEventPayload(t, f.s, workID, WorkflowActionCompleted, f.attemptR); string(got) != string(payload) {
		t.Fatal("replay changed the recorded rejection payload")
	}
}

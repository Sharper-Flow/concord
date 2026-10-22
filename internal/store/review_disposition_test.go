package store

import (
	"context"
	"slices"
	"strings"
	"testing"
)

func TestReviewDispositionRequiresCorrectiveEditCapability(t *testing.T) {
	for _, lane := range BuiltinLaneDefinitions() {
		if lane.ID == "review" {
			if !slices.Contains(lane.Capabilities, "edit_scoped_files") {
				t.Fatal("Review cannot repair its candidate: edit_scoped_files is absent")
			}
			return
		}
	}
	t.Fatal("Review lane is absent")
}

func TestReviewDispositionMainMayAssociateProofAfterExecution(t *testing.T) {
	db, appendEvent, _ := progressHistory(t, false)
	main := progressActorRecord(t, db, "main", "agent")
	appendEvent(WorkflowActionStarted, workflowActionStartedPayload{StepID: "execution", ActionID: "start_execution", ActorRef: main})
	if err := workflowStepExecutorRefusal(context.Background(), db, "work-history", main, "workflow_action"); err != nil {
		t.Fatalf("Main cannot associate producer proof after executing: %v", err)
	}
}

func TestReviewDispositionReplacesShipGate(t *testing.T) {
	for _, disposition := range []string{"continue", "correction_required", "revision_required", "ready_for_final_proof"} {
		t.Run(disposition, func(t *testing.T) {
			report := reviewBlockFixture()
			report.Disposition = disposition
			if err := validateWorkerReviewBlock(report); err != nil {
				t.Fatalf("explicit disposition is rejected by the retired ship gate: %v", err)
			}
		})
	}
}

func TestReviewDispositionRepairMustNotDemandEvaluator(t *testing.T) {
	err := nativeOracleSubjectMoved(strings.Repeat("a", 40), strings.Repeat("b", 40))
	if strings.Contains(err.Error(), "dispatch an evaluator") {
		t.Fatalf("repair verification still demands another evaluator: %v", err)
	}
}

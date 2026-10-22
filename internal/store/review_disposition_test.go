package store

import (
	"context"
	"testing"
)

func TestReviewDispositionMainMayAssociateProofAfterExecution(t *testing.T) {
	db, appendEvent, _ := progressHistory(t, false)
	main := progressActorRecord(t, db, "main", "agent")
	appendEvent(WorkflowActionStarted, workflowActionStartedPayload{StepID: "execution", ActionID: "start_execution", ActorRef: main})
	if err := workflowRequireMain(context.Background(), db, "work-history", main); err != nil {
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

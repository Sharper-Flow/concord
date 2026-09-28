package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestBackfillRecoversPostCutoverPublicationObligations(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	ctx := context.Background()
	setupLinearProduct(t, s, "recovery-product")
	if _, err := s.SetProductPlanningMode(ctx, "recovery-product", PlanningModeLinear, "pilot", "operator", 2); err != nil {
		t.Fatal(err)
	}

	// Missing connection refuses capture publication, but the cutover event
	// keeps each later capture inside the derived obligation.
	captureLinearFixtureWork(t, s, "recovery-terminal", "recovery-product-project", "task", "Terminal", "Terminal value", "")
	captureLinearFixtureWork(t, s, "recovery-retry", "recovery-product-project", "task", "Retry", "Retry value", "")
	transition, err := json.Marshal(map[string]any{"from": "needed", "to": "cancelled", "reason": "closed before recovery", "expected_version": 2, "resulting_version": 3})
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplyOperation(ctx, s, Operation{
		Events:           []Event{{EventID: "recovery-terminal-cancelled", Kind: "work.transitioned", SubjectType: SubjectWorkItem, SubjectID: "recovery-terminal", Actor: "operator", OccurredAt: time.Now().UTC(), PayloadVersion: 1, Payload: transition}},
		ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "recovery-terminal"): 2},
	}); err != nil {
		t.Fatal(err)
	}

	// A capture before cutover stays exempt even when recovery runs later.
	setupLinearProduct(t, s, "recovery-precutover")
	captureLinearFixtureWork(t, s, "recovery-old", "recovery-precutover-project", "task", "Old", "Old value", "")
	if _, err := s.SetProductPlanningMode(ctx, "recovery-precutover", PlanningModeLinear, "pilot", "operator", 2); err != nil {
		t.Fatal(err)
	}

	setupLinearConnectionResourceAtVersion(t, s, "recovery-product", map[string]any{"linear": map[string]any{"workspace_url": "https://linear.app/example", "team_id": "team-uuid-1", "auth_mode": "personal_api_key"}}, 3)
	setupLinearConnectionResourceAtVersion(t, s, "recovery-precutover", map[string]any{"linear": map[string]any{"workspace_url": "https://linear.app/example", "team_id": "team-uuid-1", "auth_mode": "personal_api_key"}}, 3)

	health, err := s.ReadLinearIntegrationHealth(ctx, "recovery-product")
	if err != nil {
		t.Fatal(err)
	}
	if health.UnlinkedObligatedItems != 2 {
		t.Fatalf("unlinked obligations = %d, want 2", health.UnlinkedObligatedItems)
	}

	enqueued, err := s.BackfillLinearIssueCreates(ctx, "recovery-product")
	if err != nil {
		t.Fatal(err)
	}
	if len(enqueued) != 2 {
		t.Fatalf("backfill queued %d operations, want 2", len(enqueued))
	}
	var terminalPayload linearPayload
	var retryOperation ClaimedLinearOperation
	for _, op := range enqueued {
		if op.WorkID == "recovery-terminal" {
			if err := json.Unmarshal(op.Payload, &terminalPayload); err != nil {
				t.Fatal(err)
			}
		} else if op.WorkID == "recovery-retry" {
			retryOperation = op
		}
	}
	if terminalPayload.Lifecycle != "cancelled" {
		t.Fatalf("terminal issue lifecycle = %q, want cancelled", terminalPayload.Lifecycle)
	}

	if retryOperation.OperationID == "" {
		t.Fatal("backfill did not enqueue recovery-retry")
	}
	claimed, err := s.ClaimLinearOperationsForProduct(ctx, "recovery-product", 25)
	if err != nil {
		t.Fatal(err)
	}
	var retryOperationID string
	for _, op := range claimed {
		if op.OperationID == retryOperation.OperationID {
			retryOperationID = op.OperationID
		}
	}
	if retryOperationID == "" {
		t.Fatal("could not claim the recovery-retry operation")
	}
	if err := s.FailLinearOperation(ctx, retryOperationID, "permanent", "connection changed"); err != nil {
		t.Fatal(err)
	}
	// Backfill revives the existing failed create with its operation identity.
	recovered, err := s.BackfillLinearIssueCreates(ctx, "recovery-product")
	if err != nil {
		t.Fatal(err)
	}
	if len(recovered) != 1 || recovered[0].WorkID != "recovery-retry" || recovered[0].OperationID != retryOperation.OperationID {
		t.Fatalf("failed create recovery = %+v, want the original recovery-retry operation", recovered)
	}
	if old, err := s.BackfillLinearIssueCreates(ctx, "recovery-precutover"); err != nil || len(old) != 0 {
		t.Fatalf("pre-cutover backfill = %+v, error %v, want no operation", old, err)
	}
}

package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// Confirmation cannot leave acceptance without applicable criterion proof.
// Both fixtures use the same current candidate, receipt, and accepted Review;
// only the criterion association differs.
func TestConfirmPremiseRequiresVerdictAndRequiredEvidence(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	missing := qBuildAcceptance(t, "premise-missing-proof", "", false)
	err := missing.run(ctx, "confirm_premise", "missing", missing.version(ctx, t), nil, nil)
	var failure *Failure
	if !failureAs(err, &failure) || failure.Kind != KindMissingEvidence || !strings.Contains(failure.Detail, "every approved predicate") {
		t.Fatalf("confirmation without criterion association = %v, want missing_evidence naming the predicates", err)
	}
	full := qCurrentAcceptance(t, "premise-full-proof")
	if err := full.run(ctx, "confirm_premise", "full", full.version(ctx, t), nil, nil); err != nil {
		t.Fatalf("confirmation with current criterion proof refused: %v", err)
	}
}

func TestPreflightRefusesConfirmationMissingMandatedDeliverables(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	preflight := func(f acceptanceRecoveryFixture) error {
		_, err := WorkflowActionDefinitionFor(ctx, f.store, WorkflowActionPreflightRequest{
			WorkID: f.workID, ExpectedVersion: f.version(ctx, t), ActionID: "confirm_premise", Actor: f.owner,
			Payload: json.RawMessage(`{}`),
		})
		return err
	}
	missing := qBuildAcceptance(t, "preflight-missing-proof", "", false)
	err := preflight(missing)
	var failure *Failure
	if !failureAs(err, &failure) || failure.Kind != KindMissingEvidence || !strings.Contains(failure.Detail, "every approved predicate") {
		t.Fatalf("preflight without criterion association = %v, want missing_evidence naming the predicates", err)
	}
	writeErr := missing.run(ctx, "confirm_premise", "preflight-parity", missing.version(ctx, t), nil, nil)
	var writeFailure *Failure
	if !failureAs(writeErr, &writeFailure) || writeFailure.Kind != failure.Kind || writeFailure.Detail != failure.Detail {
		t.Fatalf("preflight=%v write=%v, want matching refusals", err, writeErr)
	}
	full := qCurrentAcceptance(t, "preflight-full-proof")
	if err := preflight(full); err != nil {
		t.Fatalf("preflight with current criterion proof refused: %v", err)
	}
}

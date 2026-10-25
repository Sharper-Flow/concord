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
	if !failureAs(err, &failure) || failure.Kind != KindMissingEvidence || !strings.Contains(failure.Detail, "verdict") {
		t.Fatalf("confirmation without criterion association = %v, want missing_evidence naming the verdict", err)
	}
	full := qCurrentAcceptance(t, "premise-full-proof")
	if err := full.run(ctx, "confirm_premise", "full", full.version(ctx, t), nil, nil); err != nil {
		t.Fatalf("confirmation with current criterion proof refused: %v", err)
	}
}

// Record the Domain investigation the premise-question boundary requires.
func seedPremiseDeliverablesInvestigation(t *testing.T, s *Store, workID string) {
	t.Helper()
	var domainID string
	if err := s.DatabaseForTesting().QueryRow(`SELECT domain_id FROM domains WHERE product_id='product' AND status='current' ORDER BY domain_id LIMIT 1`).Scan(&domainID); err != nil {
		t.Fatalf("read the current Domain: %v", err)
	}
	insertInvestigationGateObservation(t, s, workID, "obs:"+strings.Repeat("d", 16), []string{domainID})
}

func TestPreflightRefusesConfirmationMissingMandatedDeliverables(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	preflight := func(f acceptanceRecoveryFixture) error {
		return WorkflowActionPreflightWithRegistry(ctx, f.store, BuiltinWorkflowRegistry(), WorkflowActionPreflightRequest{
			WorkID: f.workID, ExpectedVersion: f.version(ctx, t), ActionID: "confirm_premise", Actor: f.owner,
			Payload: json.RawMessage(`{}`), SelectedChoice: "confirm",
		})
	}
	missing := qBuildAcceptance(t, "preflight-missing-proof", "", false)
	err := preflight(missing)
	var failure *Failure
	if !failureAs(err, &failure) || failure.Kind != KindMissingEvidence || !strings.Contains(failure.Detail, "verdict") {
		t.Fatalf("preflight without criterion association = %v, want missing_evidence naming the verdict", err)
	}
	full := qCurrentAcceptance(t, "preflight-full-proof")
	if err := preflight(full); err != nil {
		t.Fatalf("preflight with current criterion proof refused: %v", err)
	}
}

package store

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// RetryWallRefusalFixture drives the real escalated dispatch admission and
// returns its genuine refusal. The external serialization harness projects
// this failure across the model boundary; the fixture adds no second
// implementation of the wall.
func RetryWallRefusalFixture(t *testing.T) *Failure {
	const workID = "retry-wall-remedy"
	s, owner, pin := seedIssue1013EscalatedCorrection(t, workID)
	defer s.Close()
	before := readWorkVersion(t, s, workID)
	_, err := invokeWorkflowActionForCD0059(context.Background(), t, s, WorkflowActionExecutionRequest{
		WorkID: workID, ExpectedVersion: before, ActionID: "dispatch_worker",
		Payload:         issue1013CorrectionDispatchPayload(t, s, workID, "repair", "attempt:"+workID+":4", pin.Correction),
		SessionWorktree: dispatchSessionWorktree(t, s, workID), Actor: owner,
		AcceptedInputsDigest: "sha256:" + strings.Repeat("e", 64), IdempotencyIdentity: workID, OperationID: workID,
		PrincipalRef: owner.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: workID,
		RequestID: "request:" + workID, ContractDigest: testManifestDigest, Now: time.Unix(50, 0).UTC(),
	})
	var failure *Failure
	if !errors.As(err, &failure) {
		t.Fatalf("escalated dispatch did not refuse: %v", err)
	}
	return failure
}

// BootstrapOriginRefusalFixture drives the real bootstrap origin gate against
// a path no active Concord worktree occupies and returns its genuine refusal.
func BootstrapOriginRefusalFixture(t *testing.T) *Failure {
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "bootstrap-remedy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_, err = s.ValidateBootstrapOrigin(context.Background(), "concord", filepath.Join(t.TempDir(), "not-an-active-worktree"), nil)
	var failure *Failure
	if !errors.As(err, &failure) {
		t.Fatalf("origin gate did not refuse: %v", err)
	}
	return failure
}

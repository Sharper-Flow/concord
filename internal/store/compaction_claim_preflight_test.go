package store

import (
	"context"
	"strings"
	"testing"
	"time"
)

// compactionClaimRequest is the claim concord_work_compact.publish issues:
// an auxiliary cross-authority workflow layered over a work item that already
// carries its own item workflow.
func compactionClaimRequest(workID string) ClaimRequest {
	return ClaimRequest{OpID: "compact-" + workID, WorkID: workID, WorkflowTypeRef: "concord.pm6.compaction", WorkflowTypeVersion: 1, StepID: "git_proof", StepKind: StepCrossAuthority, AcceptedInputsDigest: "sha256:" + strings.Repeat("c", 64), AcceptedScopeSnapshot: `{"work_ids":["` + workID + `"]}`, PrincipalRef: "principal/operator", Tool: "concord_work_compact", IdempotencyKey: "compact-" + workID, RequestID: "request:compact-" + workID, ObservedAt: time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC), ContractDigest: "sha256:" + strings.Repeat("d", 64)}
}

// A completed work item keeps its workflow instance pinned to its item
// workflow (workflow.break_fix). The compaction publish claim names its own
// auxiliary workflow (concord.pm6.compaction), and the claim preflight used to
// demand the two match, so no work item that carried a workflow could publish
// its compact at all: the claim refused with "workflow claim identity does not
// match the stored definition pin" (#204 replaced the auxiliary-claim skip
// with an item-has-workflow guard and dragged compaction claims into the item
// pin comparison). The item's own pin still verifies; the auxiliary claim is
// not a step of the item workflow and must not be compared against it.
func TestCompactionClaimOnAWorkflowBearingWorkItemIsAdmitted(t *testing.T) {
	t.Parallel()
	const workID = "compact-publish-claim"
	s, _ := seedBootstrapCapturedWork(t, workID, "bug")

	if _, err := ClaimStep(context.Background(), s, compactionClaimRequest(workID)); err != nil {
		t.Fatalf("compaction claim on a workflow-bearing work item refused: %v", err)
	}
}

// The identity check itself stays: a claim that names the item's own workflow
// family must match the stored definition pin's version, and must match the
// current step. The auxiliary route may not become a hole for stale item
// workflow claims.
func TestItemWorkflowClaimStillVerifiesTheStoredPin(t *testing.T) {
	t.Parallel()
	const workID = "compact-publish-own-claim"
	s, _ := seedBootstrapCapturedWork(t, workID, "bug")

	own := compactionClaimRequest(workID)
	own.OpID, own.IdempotencyKey, own.RequestID = "own-"+workID, "own-"+workID, "request:own-"+workID
	own.WorkflowTypeRef = "workflow.break_fix"
	own.WorkflowTypeVersion = 999
	own.StepID = "reproduce"
	err := func() error {
		fence, claimErr := ClaimStep(context.Background(), s, own)
		_ = fence
		return claimErr
	}()
	if err == nil {
		t.Fatal("a claim naming the item workflow at a wrong version was admitted")
	}
	if got := err.Error(); !strings.Contains(got, "workflow claim identity does not match the stored definition pin") {
		t.Fatalf("refusal = %q, want the pin identity refusal", got)
	}

	ownTwo := own
	ownTwo.OpID, ownTwo.IdempotencyKey, ownTwo.RequestID = "own2-"+workID, "own2-"+workID, "request:own2-"+workID
	entry, err := BuiltinWorkflowDefinitionForRef("workflow.break_fix")
	if err != nil {
		t.Fatal(err)
	}
	ownTwo.WorkflowTypeVersion = int(entry.Definition.Version)
	ownTwo.StepID = "verify"
	_, err = ClaimStep(context.Background(), s, ownTwo)
	if err == nil {
		t.Fatal("a claim naming a step that is not the current step was admitted")
	} else if got := err.Error(); !strings.Contains(got, "current definition step") {
		t.Fatalf("refusal = %q, want the current-step refusal", got)
	}
}

package store

import (
	"context"
	"testing"
)

func requireCombinedDeliveryPreflightParity(t *testing.T, c acceptDeliveryCandidate, want string) {
	t.Helper()
	ctx := context.Background()
	s, work := c.f.store, c.f.workID
	payload := acceptDeliveryCombinedPayload(c, "artifact:preflight-parity")
	resolved := ResolveWorkflowFootprint(ctx, s, BuiltinWorkflowRegistry(), work, "accept_worker_result", payload)
	version := readWorkVersion(t, s, work)
	preflight := WorkflowActionPreflightWithRegistry(ctx, s, BuiltinWorkflowRegistry(), WorkflowActionPreflightRequest{
		WorkID: work, ExpectedVersion: version, ActionID: "accept_worker_result", Payload: payload,
		Actor: c.f.owner, SessionWorktree: c.j.repo, ResolvedFootprint: resolved,
	})
	mutation := footprintProofAction(t, s, work, "accept_worker_result", payload, nil, c.f.owner, resolved)
	refineProofRequireMissingEvidence(t, mutation, want)
	refineProofRequireMissingEvidence(t, preflight, want)
	pf, mf := workflowFailureOf(preflight), workflowFailureOf(mutation)
	if pf.Kind != mf.Kind || pf.Detail != mf.Detail || pf.RecoveryAction != mf.RecoveryAction || pf.RetrySafe != mf.RetrySafe {
		t.Fatalf("preflight/mutation refusal differs: %+v / %+v", pf, mf)
	}
	if readWorkVersion(t, s, work) != version {
		t.Fatal("refused combined accept changed work version")
	}
	reviewGateRequireStep(t, s, work, "refine")
	var accepted int
	if err := s.db.QueryRow(`SELECT count(*) FROM worker_attempt_dispositions WHERE work_id=? AND attempt_id=? AND action_id='accept_worker_result'`, work, c.attempt).Scan(&accepted); err != nil {
		t.Fatal(err)
	}
	if accepted != 0 {
		t.Fatal("refused combined accept recorded acceptance")
	}
	if seq, err := workflowLatestDeliveryAssertionSeq(ctx, s, work); err != nil || seq != 0 {
		t.Fatalf("refused combined accept recorded delivery: seq=%d err=%v", seq, err)
	}
}

func TestCombinedAcceptPreflightDeliveryPrecedence(t *testing.T) {
	t.Run("projected Review before footprint", func(t *testing.T) {
		const work = "accept-preflight-review"
		c, _ := footprintProofDeliveryCandidate(t, work, false)
		c.attempt = "attempt:preflight-continue"
		c.epoch = acceptDeliveryCompleteReview(t, c, c.attempt, "continue")
		refineProofCandidateRun(t, c.f.store, work, "declared", refineProofCandidate(t, c.f.store, work), []string{"go", "vet", "./..."})
		workerJobIntegrationGreenRun(t, c.f.store, work, "accept-preflight-review")
		requireCombinedDeliveryPreflightParity(t, c, "leaving execution requires accepted ready_for_final_proof, not continue")
	})
	t.Run("pending ready Review supplies admission", func(t *testing.T) {
		const work = "accept-preflight-pending-ready"
		c, _ := footprintProofDeliveryCandidate(t, work, true)
		s := c.f.store
		refineProofCandidateRun(t, s, work, "declared", refineProofCandidate(t, s, work), []string{"go", "vet", "./..."})
		workerJobIntegrationGreenRun(t, s, work, "accept-preflight-pending-ready")
		footprintProofGuard(t, s, `UPDATE workflow_review_state SET state=json_set(state,'$.accepted',json('null')) WHERE work_id='`+work+`'`)
		requireCombinedDeliveryPreflightParity(t, c, "has no declared tooling coverage binding")
	})
}

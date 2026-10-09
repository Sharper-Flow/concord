package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestConvergenceBareApprovalCannotAdmitEscalatedRetry(t *testing.T) {
	const workID = "convergence-bare-approval"
	s, owner, pin := seedIssue1013EscalatedCorrection(t, workID)
	defer s.Close()
	before := readWorkVersion(t, s, workID)
	_, err := invokeWorkflowActionForCD0059(context.Background(), t, s, WorkflowActionExecutionRequest{
		WorkID: workID, ExpectedVersion: before, ActionID: "dispatch_worker",
		Payload:         issue1013CorrectionDispatchPayload(t, s, workID, "repair", "attempt:"+workID+":4", pin.Correction),
		SessionWorktree: dispatchSessionWorktree(t, s, workID), Actor: owner,
		AcceptedInputsDigest: "sha256:" + strings.Repeat("e", 64), IdempotencyIdentity: workID, OperationID: workID,
		PrincipalRef: owner.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: workID,
		RequestID: "request:" + workID, ContractDigest: testManifestDigest, Now: time.Unix(50, 0).UTC(), FailedRetryApproved: true,
	})
	if !hasFailureKind(err, KindMissingEvidence) {
		t.Fatalf("bare approval dispatch = %v, want missing_evidence", err)
	}
	if after := readWorkVersion(t, s, workID); after != before {
		t.Fatalf("refused dispatch changed work version: %d -> %d", before, after)
	}
	binding, err := WorkflowFailedWorkerRetryBinding(context.Background(), s, nil, workID)
	if err != nil || binding != nil {
		t.Fatalf("retry approval binding = %+v, %v, want nil", binding, err)
	}
	for _, intent := range issue1013Pin(t, s, workID).NextValidIntents {
		if intent.ActionID == "dispatch_worker" {
			if intent.ReasonCode != "escalated_retry_requires_convergence" {
				t.Fatalf("pin reason = %q", intent.ReasonCode)
			}
			return
		}
	}
	t.Fatal("pin hid convergence-gated retry")
}

func TestConvergenceRejectionFindingSets(t *testing.T) {
	for _, tc := range []struct {
		name   string
		latest []string
		admit  bool
	}{
		{"shrinking", []string{"finding:a"}, true},
		{"reordered", []string{"finding:b", "finding:a"}, false},
		{"growing", []string{"finding:a", "finding:b", "finding:c"}, false},
		{"renamed", []string{"finding:c"}, false},
		{"missing", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workID := "convergence-findings-" + tc.name
			s, owner, first, _ := seedOldDefinitionWorker(t, workID)
			defer s.Close()
			worker := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/worker", SessionRef: "session/" + workID, ActorClass: ActorAgent}
			for n, findings := range [][]string{{"finding:a", "finding:b"}, {"finding:a", "finding:b"}, tc.latest} {
				attemptID := first
				if n > 0 {
					attemptID = fmt.Sprintf("attempt:%s:%d", workID, n+1)
					if err := dispatchSameStepAttempt(t, s, workID, "repair", attemptID, worker, false, issue1013Pin(t, s, workID).Correction); err != nil {
						t.Fatal(err)
					}
				}
				lane := BuiltinLaneDefinitions()[0]
				if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{workerCompleteEventForLane(workID, "complete-"+attemptID, attemptID, lane, time.Unix(21, 0).UTC())}}); err != nil {
					t.Fatal(err)
				}
				fields := map[string]any{"attempt_id": attemptID, "attempt_epoch": latestStepStartEpoch(t, s, workID, "repair"), "diagnosis": "a reproduced defect remains", "strategy": "fix the remaining finding", "predicate_ids": []string{"predicate:primary"}, "evidence_refs": []string{"evidence:convergence"}}
				if findings != nil {
					fields["open_finding_ids"] = findings
				}
				if err := runVerdictActionAs(t, s, workID, "reject_worker_result", mustJSONValue(fields), 0, owner); err != nil {
					t.Fatal(err)
				}
			}
			pin := issue1013Pin(t, s, workID)
			before := pin.Version
			err := dispatchSameStepAttempt(t, s, workID, "repair", "attempt:"+workID+":4", worker, false, pin.Correction)
			if !tc.admit {
				if !hasFailureKind(err, KindMissingEvidence) {
					t.Fatalf("dispatch=%v, want missing_evidence", err)
				}
				if readWorkVersion(t, s, workID) != before {
					t.Fatal("refused retry changed version")
				}
				return
			}
			if err != nil {
				t.Fatalf("converging retry without approval: %v", err)
			}
			var raw []byte
			if err := s.db.QueryRow(`SELECT json_extract(payload,'$.retry_convergence') FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.action_id')='dispatch_worker' ORDER BY seq DESC LIMIT 1`, workID, WorkflowActionCompleted).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			var basis WorkflowRetryConvergence
			if json.Unmarshal(raw, &basis) != nil || basis.Basis != "findings_shrinking" || !basis.valid() {
				t.Fatalf("recorded basis=%s", raw)
			}
			failWorkerAttempt(t, s, workID, "attempt:"+workID+":4")
			err = dispatchSameStepAttempt(t, s, workID, "repair", "attempt:"+workID+":5", worker, false, nil)
			if !hasFailureKind(err, KindMissingEvidence) {
				t.Fatalf("reused findings basis=%v", err)
			}
		})
	}
}

func TestConvergenceOpenFindingIDsAreValidated(t *testing.T) {
	for _, raw := range []string{`null`, `[]`, `["finding:a","finding:a"]`, `["finding/a"]`, `[1]`} {
		fields := map[string]json.RawMessage{"open_finding_ids": json.RawMessage(raw)}
		if err := validateWorkflowOpenFindingsPayload(fields); !hasFailureKind(err, KindInvalidPayload) {
			t.Fatalf("%s validation=%v", raw, err)
		}
	}
}

func TestConvergenceStepAndDispositionDoNotRenewBudget(t *testing.T) {
	db, appendEvent := correctionHistoryFixture(t)
	appendEvent(WorkflowActionCompleted, correctionHistoryAction("dispatch_worker", "attempt:nonprogress", "", 0))
	appendEvent(WorkerCompleted, map[string]any{"attempt_id": "attempt:nonprogress", "review": map[string]any{"verdict": "no_ship"}})
	appendEvent(WorkflowActionCompleted, correctionHistoryAction("reject_worker_result", "attempt:nonprogress", "", 0))
	accepted := appendEvent(WorkflowActionCompleted, correctionHistoryAction("accept_worker_result", "attempt:nonprogress", "", 0))
	correctionHistoryBoundary(t, db, accepted+1, 0)
	appendEvent(WorkflowActionCompleted, map[string]any{"action_id": "record_delivery", "step_id": "execution"})
	anchor, err := workflowNonProgressWindowAnchor(context.Background(), db, "work-history", "convergence_test", 0)
	if err != nil || anchor != 0 {
		t.Fatalf("step change renewed budget: anchor=%d, err=%v", anchor, err)
	}
}

func TestConvergenceWallWithoutBasisResolvesThroughOperatorStop(t *testing.T) {
	const workID = "convergence-operator-stop"
	s, owner, pin := seedIssue1013EscalatedCorrection(t, workID)
	defer s.Close()
	if err := dispatchSameStepAttempt(t, s, workID, "repair", "attempt:"+workID+":4", owner, true, pin.Correction); !hasFailureKind(err, KindMissingEvidence) {
		t.Fatalf("wall behind an approval: %v", err)
	}
	var lifecycle string
	if err := s.db.QueryRow(`SELECT lifecycle FROM work_items WHERE id=?`, workID).Scan(&lifecycle); err != nil {
		t.Fatal(err)
	}
	version := readWorkVersion(t, s, workID)
	if err := applyWorkEvent(t, s, workTransitionEvent("stop-"+workID, workID, lifecycle, "cancelled", version, version+1), workVersion(workID, version)); err != nil {
		t.Fatal(err)
	}
	var instanceState string
	if err := s.db.QueryRow(`SELECT instance_state FROM workflow_instances WHERE work_id=?`, workID).Scan(&instanceState); err != nil {
		t.Fatal(err)
	}
	if instanceState != "cancelled" {
		t.Fatalf("operator stop left instance %q", instanceState)
	}
}

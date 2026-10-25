package store

import (
	"errors"
	"strings"
	"testing"
)

func TestDeclaredOutcomeReadOnlyDeliveryUsesItsOracle(t *testing.T) {
	for _, proof := range []bool{false, true} {
		name := "without-proof"
		if proof {
			name = "with-proof"
		}
		t.Run(name, func(t *testing.T) {
			workID := "outcome-readonly-" + name
			f := seedOracleRepairFixture(t, workID)
			t.Cleanup(func() { _ = f.store.Close() })
			job := recordOracleJobRevision(t, f.store, workID, f.owner, "job:readonly-outcome", twoPathOracleFields(t, f.store, workID, nil))
			const attempt = "attempt:readonly-outcome"
			subject := dispatchChangeAcceptanceAttempt(t, f, workID, attempt, job, "verification")
			var receipts []WorkerOracleReceipt
			if proof {
				receipts, _ = noChangeOutcomeReceipts(t, f, workID, true)
			}
			completed := completeChangeAcceptanceAttempt(t, f, workID, attempt, subject, func(p *WorkerCompletedPayload) {
				appendNoChangeReceipts(p, receipts)
			})
			if completed.Review != nil {
				t.Fatal("delivery fixture must not claim a Review disposition")
			}
			if completed.ObservedDelivery == nil || completed.ObservedDelivery.State != "clean" || completed.ObservedDelivery.Commit != subject {
				t.Fatalf("read-only outcome has no producer-observed unchanged subject: %+v", completed.ObservedDelivery)
			}
			before := readWorkVersion(t, f.store, workID)
			err := acceptChangeTestAttempt(t, f, workID, attempt)
			if proof {
				if err != nil {
					t.Fatalf("read-only delivery with complete outcome proof: %v", err)
				}
				return
			}
			if !hasFailureKind(err, KindInvalidOperation) || !strings.Contains(err.Error(), "no changed paths") {
				t.Fatalf("read-only delivery without outcome proof: %v", err)
			}
			if after := readWorkVersion(t, f.store, workID); after != before {
				t.Fatalf("refusal changed version from %d to %d", before, after)
			}
		})
	}
}

func TestDeclaredOutcomeEmptyCorrectionNeedsProofAndCanReachReview(t *testing.T) {
	const workID = "outcome-empty-correction"
	f := seedOracleRepairFixture(t, workID)
	t.Cleanup(func() { _ = f.store.Close() })
	const jobID = "job:outcome-empty-correction"
	var subject string
	for _, suffix := range []string{"first", "correction", "confirmed"} {
		job := recordOracleJobRevision(t, f.store, workID, f.owner, jobID, twoPathOracleFields(t, f.store, workID, nil))
		attempt := "attempt:" + suffix
		current := dispatchChangeAcceptanceAttempt(t, f, workID, attempt, job)
		if subject != "" && current != subject {
			t.Fatalf("no-change correction moved subject from %s to %s", subject, current)
		}
		subject = current
		var receipts []WorkerOracleReceipt
		if suffix == "confirmed" {
			receipts, _ = noChangeOutcomeReceipts(t, f, workID, true)
		}
		completeChangeAcceptanceAttempt(t, f, workID, attempt, subject, func(p *WorkerCompletedPayload) {
			appendNoChangeReceipts(p, receipts)
		})
		before := readWorkVersion(t, f.store, workID)
		err := acceptChangeTestAttempt(t, f, workID, attempt)
		if suffix == "confirmed" {
			if err != nil {
				t.Fatalf("proven unchanged outcome must be accepted without an edit: %v", err)
			}
			break
		}
		var failure *Failure
		if !errors.As(err, &failure) || !strings.Contains(err.Error(), "no changed paths") {
			t.Fatalf("%s accepted without proof or a delivered outcome: %v", suffix, err)
		}
		if !strings.Contains(failure.RecoveryAction, "no edit is required when the outcome already holds") {
			t.Fatalf("%s recovery requires a manufactured edit: %v", suffix, err)
		}
		if after := readWorkVersion(t, f.store, workID); after != before {
			t.Fatalf("%s refusal changed version %d to %d", suffix, before, after)
		}
		if err := runVerdictActionAs(t, f.store, workID, "reject_worker_result", mustJSONValue(map[string]any{
			"attempt_id": attempt, "attempt_epoch": latestStepStartEpoch(t, f.store, workID, "repair"),
			"diagnosis": "the unchanged result has no evidence of the declared outcome", "strategy": "re-check the current candidate and report its outcome evidence without a manufactured edit",
			"predicate_ids": []string{"predicate:return-route"}, "evidence_refs": []string{"evidence:unchanged-subject"},
		}), 0, f.acceptor); err != nil {
			t.Fatalf("%s rejection route: %v", suffix, err)
		}
	}
	job := recordOracleJobRevision(t, f.store, workID, f.owner, "job:outcome-review", twoPathOracleFields(t, f.store, workID, nil))
	lane := reviewGateLane(t, "review")
	const review = "attempt:outcome-review"
	packet := joinPacketFor(t, f.store, workID, "repair", review, lane.ID, lane.Digest)
	packet["inputs"].(map[string]any)["worker_job"] = recordedPacketJobForTest(t, f.store, workID, job, lane.ID)
	if err := dispatchJobPacketForTest(t, f.store, workID, review, f.owner, "dispatch-outcome-review", packet); err != nil {
		t.Fatalf("proven no-change outcome cannot progress to Review: %v", err)
	}
	if window := reviewSubjectWindow(t, f.store, workID, review); window.SubjectCommit != subject {
		t.Fatalf("Review subject=%s, want unchanged outcome subject=%s", window.SubjectCommit, subject)
	}
}

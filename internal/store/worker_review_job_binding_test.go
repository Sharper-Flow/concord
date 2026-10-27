package store

import (
	"context"
	"strings"
	"testing"
)

// seedFinalReviewAnchor accepts one implementation attempt through the real
// dispatch, terminal, and acceptance routes, so its job is satisfied by the
// acceptance that installed the current candidate.
func seedFinalReviewAnchor(t *testing.T, workID string) (oracleRepairFixture, WorkerJobBinding) {
	t.Helper()
	f := seedOracleRepairFixture(t, workID)
	job := recordOracleJobRevision(t, f.store, workID, f.owner, "job:final-review-anchor", twoPathOracleFields(t, f.store, workID, nil))
	attempt := "attempt:" + workID
	dispatchChangeAcceptanceAttempt(t, f, workID, attempt, job)
	completeChangeAcceptanceAttempt(t, f, workID, attempt, strings.Repeat("2", 40))
	if err := acceptChangeTestAttempt(t, f, workID, attempt); err != nil {
		t.Fatalf("accept implementation: %v", err)
	}
	var state string
	if err := f.store.db.QueryRow(`SELECT state FROM worker_job_revisions WHERE work_id=? AND job_id=? AND revision=?`, workID, job.JobID, job.Revision).Scan(&state); err != nil || state != "satisfied" {
		t.Fatalf("anchor job state = %q, %v; want satisfied", state, err)
	}
	return f, job
}

// CD-0205 D2: with no ready revision, Review binds the job whose accepted
// implementation attempt delivered the current candidate. A ready revision
// takes priority, and a revision outside that anchor never binds.
func TestFinalReviewBindsDeliveryAnchorJob(t *testing.T) {
	workID := "final-review-anchor"
	f, anchor := seedFinalReviewAnchor(t, workID)
	s := f.store
	defer s.Close()
	ctx := context.Background()
	q := s.DatabaseForTesting()
	final, err := finalReviewWorkerJobTx(ctx, q, workID)
	if err != nil || final == nil || *final != anchor {
		t.Fatalf("final Review binding = %#v, %v; want the delivery anchor %#v", final, err, anchor)
	}
	if err := requireWorkerReviewJobRevisionStateTx(ctx, q, workID, anchor); err != nil {
		t.Fatalf("Review admission of the delivery anchor job: %v", err)
	}
	if err := requireWorkerJobRevisionStateReadyTx(ctx, q, workID, anchor); err == nil || !strings.Contains(err.Error(), "is not ready") {
		t.Fatalf("ordinary admission of a satisfied revision = %v, want the readiness refusal", err)
	}
	foreign := anchor
	foreign.Digest = "sha256:" + strings.Repeat("e", 64)
	if err := requireWorkerReviewJobRevisionStateTx(ctx, q, workID, foreign); err == nil {
		t.Fatal("Review admitted a binding outside the delivery anchor")
	}
	// A non-Review dispatch of the satisfied revision still refuses in the fold.
	if admitted, err := dispatchBoundFinalReviewTx(ctx, q, workID, "attempt:"+workID, anchor); err != nil || admitted {
		t.Fatalf("implementation authorization admitted as final Review = %v, %v", admitted, err)
	}

	// A moved candidate no longer anchors the job.
	review, err := readWorkflowReviewFacts(ctx, q, workID)
	if err != nil || review.Candidate == nil {
		t.Fatalf("review candidate = %#v, %v", review.Candidate, err)
	}
	moved := review
	moved.Candidate = &WorkerReviewCandidate{ProjectID: review.Candidate.ProjectID, Commit: strings.Repeat("3", 40), Tree: review.Candidate.Tree}
	if _, err := s.db.Exec(`INSERT INTO write_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	if err := writeWorkflowReviewFacts(ctx, s.db, workID, moved); err != nil {
		t.Fatal(err)
	}
	if final, err := finalReviewWorkerJobTx(ctx, q, workID); err != nil || final != nil {
		t.Fatalf("final Review binding on a moved candidate = %#v, %v; want none", final, err)
	}
	if err := writeWorkflowReviewFacts(ctx, s.db, workID, review); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`DELETE FROM write_guard`); err != nil {
		t.Fatal(err)
	}

	// A ready revision is the sole Review binding; the anchor no longer binds.
	recordOracleJobRevision(t, s, workID, f.owner, "job:final-review-other", twoPathOracleFields(t, s, workID, nil))
	if final, err := finalReviewWorkerJobTx(ctx, q, workID); err != nil || final != nil {
		t.Fatalf("final Review binding beside a ready job = %#v, %v; want none", final, err)
	}
	if err := requireWorkerReviewJobRevisionStateTx(ctx, q, workID, anchor); err == nil || !strings.Contains(err.Error(), "is not ready") {
		t.Fatalf("Review admission of the anchor beside a ready job = %v, want the readiness refusal", err)
	}
}

// CD-0205 D2: the anchor binds only the latest revision of its job.
func TestFinalReviewRefusesSupersededAnchorRevision(t *testing.T) {
	workID := "final-review-superseded"
	f, anchor := seedFinalReviewAnchor(t, workID)
	s := f.store
	defer s.Close()
	recordOracleJobRevisionWith(t, s, workID, f.owner, anchor.JobID, twoPathOracleFields(t, s, workID, nil), map[string]any{
		"objective": "A changed objective awaiting readiness", "ready": false, "readiness_evidence": []string{},
	})
	if final, err := finalReviewWorkerJobTx(context.Background(), s.DatabaseForTesting(), workID); err != nil || final != nil {
		t.Fatalf("final Review binding after a later revision = %#v, %v; want none", final, err)
	}
}

// CD-0205 D1: Review execution consumes only its authorization's frozen
// preparation; other lanes, and authorizations recorded without a mapping,
// keep the recorded-reference rule.
func TestNativeDispatchPreparationRefFreezesReviewBinding(t *testing.T) {
	bundle := NativeOracleControlBundle{Control: OracleControl{ControlID: "control:a", ReadinessEvidenceRefs: []string{"recorded:1", "recorded:2"}}}
	review := workflowActionCompletedPayload{WorkerCapabilityClass: "review", WorkerOraclePreparations: []WorkerOraclePreparationBinding{{ControlID: "control:a", PreparationRunRef: "frozen:1"}}}
	for _, tc := range []struct {
		name      string
		dispatch  workflowActionCompletedPayload
		requested string
		want      string
		ok        bool
	}{
		{"review default", review, "", "frozen:1", true},
		{"review exact", review, "frozen:1", "frozen:1", true},
		{"review newer preparation", review, "newer:1", "", false},
		{"review recorded reference", review, "recorded:1", "", false},
		{"implementation default", workflowActionCompletedPayload{WorkerCapabilityClass: "implementation"}, "", "recorded:1", true},
		{"implementation recorded", workflowActionCompletedPayload{WorkerCapabilityClass: "implementation"}, "recorded:2", "recorded:2", true},
		{"implementation foreign", workflowActionCompletedPayload{WorkerCapabilityClass: "implementation"}, "frozen:1", "", false},
		{"review without mapping", workflowActionCompletedPayload{WorkerCapabilityClass: "review"}, "", "recorded:1", true},
		{"review other control", workflowActionCompletedPayload{WorkerCapabilityClass: "review", WorkerOraclePreparations: []WorkerOraclePreparationBinding{{ControlID: "control:b", PreparationRunRef: "frozen:1"}}}, "", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := nativeDispatchPreparationRef(tc.dispatch, bundle, tc.requested)
			if ok != tc.ok || ok && got != tc.want {
				t.Fatalf("ref = %q,%v; want %q,%v", got, ok, tc.want, tc.ok)
			}
		})
	}
}

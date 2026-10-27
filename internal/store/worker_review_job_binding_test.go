package store

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
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

// CD-0205 D2: the delivery anchor is the newest accepted implementation
// delivery. When two jobs' accepted results both delivered the current
// candidate, Review binds the newer job only.
func TestFinalReviewBindsNewestDeliveryAnchor(t *testing.T) {
	workID := "final-review-ambiguous"
	f := seedOracleRepairFixture(t, workID)
	s := f.store
	defer s.Close()
	// Each job's declared outcome already holds on the qualified candidate,
	// so each accepted result delivers that same candidate without a change.
	var receipts []WorkerOracleReceipt
	acceptNoChange := func(jobID string, reviewFirst bool) WorkerJobBinding {
		t.Helper()
		job := recordOracleJobRevision(t, s, workID, f.owner, jobID, twoPathOracleFields(t, s, workID, nil))
		if reviewFirst {
			// Progression past an installed candidate needs a permitting
			// Review bound to this job (CD-0205 D3).
			oAcceptOracleFixtureReview(t, f, workID, "continue")
		}
		attempt := "attempt:" + jobID
		subject := dispatchChangeAcceptanceAttempt(t, f, workID, attempt, job)
		if receipts == nil {
			receipts, _ = noChangeOutcomeReceipts(t, f, workID, true)
		}
		completeChangeAcceptanceAttempt(t, f, workID, attempt, subject, func(p *WorkerCompletedPayload) {
			appendNoChangeReceipts(p, receipts)
		})
		if err := acceptChangeTestAttempt(t, f, workID, attempt); err != nil {
			t.Fatalf("accept %s: %v", jobID, err)
		}
		return job
	}
	first := acceptNoChange("job:final-review-first", false)
	ctx := context.Background()
	q := s.DatabaseForTesting()
	if final, err := finalReviewWorkerJobTx(ctx, q, workID); err != nil || final == nil || *final != first {
		t.Fatalf("final Review binding over one delivery anchor = %#v, %v; want %#v", final, err, first)
	}
	second := acceptNoChange("job:final-review-second", true)
	if final, err := finalReviewWorkerJobTx(ctx, q, workID); err != nil || final == nil || *final != second {
		t.Fatalf("final Review binding after a newer delivery = %#v, %v; want %#v", final, err, second)
	}
	if err := requireWorkerReviewJobRevisionStateTx(ctx, q, workID, second); err != nil {
		t.Fatalf("Review admission of the newest delivery anchor: %v", err)
	}
	if err := requireWorkerReviewJobRevisionStateTx(ctx, q, workID, first); err == nil || !strings.Contains(err.Error(), "is not ready") {
		t.Fatalf("Review admission of the older delivery = %v, want the readiness refusal", err)
	}
	// A member result delivers on its member worktree, not the claimed
	// worktree Review judges, so the newer member delivery is no anchor.
	var actor, project string
	if err := s.db.QueryRow(`SELECT json_extract(payload,'$.actor_ref') FROM worker_authorizations WHERE work_id=? AND attempt_id=?`, workID, "attempt:"+second.JobID).Scan(&actor); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT project_id FROM work_projects WHERE work_id=? AND role='primary'`, workID).Scan(&project); err != nil {
		t.Fatal(err)
	}
	for _, seed := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO write_guard(active) VALUES(1)`, nil},
		{`INSERT INTO workflow_member_groups(work_id,group_id,base_subject,permission) VALUES(?,'group:member-anchor',json_object('commit',?,'verification_ref','verify:member-anchor'),'continue')`, []any{workID, strings.Repeat("1", 40)}},
		{`INSERT INTO workflow_group_members(work_id,group_id,member_id,job_id,job_revision,job_digest,project_id,actor_ref) VALUES(?,'group:member-anchor','member:anchor',?,?,?,?,?)`, []any{workID, second.JobID, second.Revision, second.Digest, project, actor}},
		{`DELETE FROM write_guard`, nil},
	} {
		if _, err := s.db.Exec(seed.query, seed.args...); err != nil {
			t.Fatalf("seed member roster: %v", err)
		}
	}
	if final, err := finalReviewWorkerJobTx(ctx, q, workID); err != nil || final == nil || *final != first {
		t.Fatalf("final Review binding after a newer member delivery = %#v, %v; want the claimed-worktree delivery %#v", final, err, first)
	}
}

// CD-0205 D2: only the newest accepted implementation delivery anchors final
// Review. When that delivery carries no clean core observation, or its job
// has a later revision, no anchor exists and the older delivery never stands
// in for it.
func TestFinalReviewNeverFallsBackFromNewestAnchor(t *testing.T) {
	for _, shape := range []string{"unobserved", "superseded"} {
		t.Run(shape, func(t *testing.T) {
			workID := "final-review-no-fallback-" + shape
			f, first := seedFinalReviewAnchor(t, workID)
			s := f.store
			defer s.Close()
			advanceOracleFixtureSubject(t, f, workID, "2")
			second := recordOracleJobRevision(t, s, workID, f.owner, "job:final-review-newest", twoPathOracleFields(t, s, workID, nil))
			oAcceptOracleFixtureReview(t, f, workID, "continue")
			attempt := "attempt:" + workID + ":newest"
			dispatchChangeAcceptanceAttempt(t, f, workID, attempt, second)
			if shape == "unobserved" {
				if err := completeJobBoundAttemptForTest(s, workID, attempt, reviewGateLane(t, "implementation"), &second); err != nil {
					t.Fatal(err)
				}
			} else {
				completeChangeAcceptanceAttempt(t, f, workID, attempt, strings.Repeat("3", 40))
			}
			if err := acceptChangeTestAttempt(t, f, workID, attempt); err != nil {
				t.Fatalf("accept the newest implementation: %v", err)
			}
			ctx := context.Background()
			q := s.DatabaseForTesting()
			if shape == "superseded" {
				if final, err := finalReviewWorkerJobTx(ctx, q, workID); err != nil || final == nil || *final != second {
					t.Fatalf("final Review binding of the newest delivery = %#v, %v; want %#v", final, err, second)
				}
				recordOracleJobRevisionWith(t, s, workID, f.owner, second.JobID, twoPathOracleFields(t, s, workID, nil), map[string]any{
					"objective": "A changed objective awaiting readiness", "ready": false, "readiness_evidence": []string{},
				})
			}
			if final, err := finalReviewWorkerJobTx(ctx, q, workID); err != nil || final != nil {
				t.Fatalf("final Review binding without a valid newest anchor = %#v, %v; want none", final, err)
			}
			if err := requireWorkerReviewJobRevisionStateTx(ctx, q, workID, first); err == nil || !strings.Contains(err.Error(), "is not ready") {
				t.Fatalf("Review admission of the older delivery = %v, want the readiness refusal", err)
			}
		})
	}
}

// CD-0205 D1: a Review of a ready revision freezes its recorded preparation
// when it qualifies on the current candidate. When the candidate has moved
// and no qualified preparation of the unchanged bundle exists, the dispatch
// refuses before an attempt.
func TestReadyReviewFreezesQualifiedPreparationOrRefusesBeforeAttempt(t *testing.T) {
	workID := "ready-review-preparation"
	f := seedOracleRepairFixture(t, workID)
	s := f.store
	defer s.Close()
	job := recordOracleJobRevision(t, s, workID, f.owner, "job:ready-review", twoPathOracleFields(t, s, workID, nil))
	oracle, err := readWorkerJobOracle(context.Background(), s.db, workID, job)
	if err != nil || oracle == nil {
		t.Fatalf("job oracle = %v, %v", oracle, err)
	}
	review := reviewGateLane(t, "review")
	reviewPacket := func(attempt string) map[string]any {
		packet := joinPacketFor(t, s, workID, "repair", attempt, review.ID, review.Digest)
		packet["inputs"].(map[string]any)["worker_job"] = recordedPacketJobForTest(t, s, workID, job, review.ID)
		return packet
	}
	authorized := func(attempt string) int {
		var n int
		if err := s.db.QueryRow(`SELECT count(*) FROM worker_authorizations WHERE work_id=? AND attempt_id=?`, workID, attempt).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	stale := "attempt:" + workID + ":stale"
	advanceOracleFixtureSubject(t, f, workID, "2")
	err = dispatchJobPacketForTest(t, s, workID, stale, f.owner, "dispatch-stale", reviewPacket(stale))
	var failure *Failure
	if !errors.As(err, &failure) || !strings.Contains(failure.Detail, "qualified native preparation") || !strings.Contains(failure.RecoveryAction, "worktree_verify") {
		t.Fatalf("Review of a ready revision with a stale preparation = %v, want the preparation refusal", err)
	}
	if authorized(stale) != 0 {
		t.Fatal("the refused Review dispatch recorded an authorization")
	}

	fresh := "attempt:" + workID + ":fresh"
	advanceOracleFixtureSubject(t, f, workID, "1")
	if err := dispatchJobPacketForTest(t, s, workID, fresh, f.owner, "dispatch-fresh", reviewPacket(fresh)); err != nil {
		t.Fatalf("Review of a ready revision with a qualifying recorded preparation: %v", err)
	}
	var raw string
	if err := s.db.QueryRow(`SELECT json_extract(payload,'$.worker_oracle_preparations') FROM worker_authorizations WHERE work_id=? AND attempt_id=?`, workID, fresh).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var frozen []WorkerOraclePreparationBinding
	if err := json.Unmarshal([]byte(raw), &frozen); err != nil {
		t.Fatal(err)
	}
	var want []WorkerOraclePreparationBinding
	for _, control := range oracle.Controls {
		want = append(want, WorkerOraclePreparationBinding{ControlID: control.ControlID, PreparationRunRef: control.ReadinessEvidenceRefs[0]})
	}
	if !slices.Equal(frozen, want) {
		t.Fatalf("frozen Review preparations = %#v, want the recorded references %#v", frozen, want)
	}
}

// CD-0205 D1: Review preparation must match the runtime environment observed
// at dispatch, before an attempt exists. On an unchanged candidate a recorded
// reference prepared on another runtime does not qualify: Review refuses
// until Main prepares the unchanged bundle on the current runtime, and then
// binds that preparation instead of the recorded reference.
func TestReadyReviewQualifiesPreparationOnObservedRuntime(t *testing.T) {
	workID := "ready-review-runtime"
	f := seedOracleRepairFixture(t, workID)
	s := f.store
	defer s.Close()
	job := recordOracleJobRevision(t, s, workID, f.owner, "job:ready-review-runtime", twoPathOracleFields(t, s, workID, nil))
	oracle, err := readWorkerJobOracle(context.Background(), s.db, workID, job)
	if err != nil || oracle == nil {
		t.Fatalf("job oracle = %v, %v", oracle, err)
	}
	review := reviewGateLane(t, "review")
	dispatch := func(attempt, runtime string) error {
		packet := joinPacketFor(t, s, workID, "repair", attempt, review.ID, review.Digest)
		packet["inputs"].(map[string]any)["worker_job"] = recordedPacketJobForTest(t, s, workID, job, review.ID)
		return dispatchJobPacketOnRuntimeForTest(t, s, workID, attempt, f.owner, "dispatch-"+attempt, packet, runtime)
	}
	frozen := func(attempt string) []WorkerOraclePreparationBinding {
		var raw string
		if err := s.db.QueryRow(`SELECT json_extract(payload,'$.worker_oracle_preparations') FROM worker_authorizations WHERE work_id=? AND attempt_id=?`, workID, attempt).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var out []WorkerOraclePreparationBinding
		if err := json.Unmarshal([]byte(raw), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	recorded := make([]WorkerOraclePreparationBinding, 0, len(oracle.Controls))
	for _, control := range oracle.Controls {
		recorded = append(recorded, WorkerOraclePreparationBinding{ControlID: control.ControlID, PreparationRunRef: control.ReadinessEvidenceRefs[0]})
	}

	switched := "attempt:" + workID + ":switched"
	err = dispatch(switched, "switched")
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindMissingEvidence || !strings.Contains(failure.RecoveryAction, "worktree_verify") {
		t.Fatalf("Review on a switched runtime with only the recorded preparation = %v, want the preparation refusal", err)
	}
	var n int
	if err := s.db.QueryRow(`SELECT count(*) FROM worker_authorizations WHERE work_id=? AND attempt_id=?`, workID, switched).Scan(&n); err != nil || n != 0 {
		t.Fatalf("the refused Review dispatch recorded %d authorizations, %v", n, err)
	}

	current := *oracle
	current.Controls = slices.Clone(oracle.Controls)
	seedOraclePreparationLedgerFixtureOn(t, s, workID, &current, "switched")
	prepared := "attempt:" + workID + ":prepared"
	if err := dispatch(prepared, "switched"); err != nil {
		t.Fatalf("Review after preparing on the switched runtime: %v", err)
	}
	var want []WorkerOraclePreparationBinding
	for _, control := range current.Controls {
		want = append(want, WorkerOraclePreparationBinding{ControlID: control.ControlID, PreparationRunRef: control.ReadinessEvidenceRefs[0]})
	}
	if got := frozen(prepared); !slices.Equal(got, want) || slices.Equal(got, recorded) {
		t.Fatalf("frozen Review preparations = %#v, want the switched-runtime preparations %#v", got, want)
	}
	after, err := readWorkerJobOracle(context.Background(), s.db, workID, job)
	if err != nil || !slices.EqualFunc(after.Controls, oracle.Controls, func(a, b OracleControl) bool { return slices.Equal(a.ReadinessEvidenceRefs, b.ReadinessEvidenceRefs) }) {
		t.Fatalf("the recorded job's readiness references changed: %v", err)
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

package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func workerDiffRefusalShape(t *testing.T, err error, wantDetail string) {
	t.Helper()
	var failure *Failure
	if !errors.As(err, &failure) {
		t.Fatalf("error %v is not a typed store failure", err)
	}
	if failure.Kind != KindUnauthorizedDispatch || failure.Op != "worker_diff" || failure.Detail != wantDetail || failure.RetrySafe || failure.RecoveryAction != "use a live review with its original packet, claimed worktree, scoped job, and complete first-editing subject history" {
		t.Fatalf("worker diff refusal lost its typed shape, want kind=%s op=worker_diff detail=%q retry_safe=false recovery intact: %+v", KindUnauthorizedDispatch, wantDetail, failure)
	}
}

// A corrective Review produces the change and Main integrates its result;
// an unrecorded HEAD change cannot become the next Review input (CD-0242 D5).
func workerDiffCommit(t *testing.T, j *candidateJourney, id, content string, edits ...func()) string {
	t.Helper()
	attempt := "integrate-" + id
	job := workerDiffJob(t, j, attempt)
	workerDiffDispatchJob(t, j, attempt, job)
	dir := filepath.Join(j.repo, "internal", "store")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "change.txt"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, edit := range edits {
		edit()
	}
	head := j.commit(t, "reviewer@example.invalid", id)
	report := currentReviewBlockForTest(t, j.s, j.workID, attempt, "continue")
	report.OutputCandidate = WorkerReviewCandidate{ProjectID: report.InputCandidate.ProjectID, Commit: head, Tree: reviewObservedTree(t, j.repo, head)}
	j.completeReview(t, attempt, job, report)
	if err := j.acceptEvidence(t, attempt); err != nil {
		t.Fatalf("Main integration of corrective Review: %v", err)
	}
	j.observeRun(t, id)
	return head
}

func workerDiffDispatch(t *testing.T, j *candidateJourney, attempt string) WorkerDiffRequest {
	t.Helper()
	return workerDiffDispatchJob(t, j, attempt, workerDiffJob(t, j, attempt))
}

func workerDiffCompleteRead(t *testing.T, j *candidateJourney, attempt string) {
	t.Helper()
	job, err := workflowDispatchedJobForAttempt(t.Context(), j.s.db, j.workID, attempt)
	if err != nil || job == nil {
		t.Fatalf("read diff assignment binding: %v, %v", job, err)
	}
	j.completeReview(t, attempt, *job, currentReviewBlockForTest(t, j.s, j.workID, attempt, "continue"))
}

func workerDiffJob(t *testing.T, j *candidateJourney, attempt string) WorkerJobBinding {
	t.Helper()
	// The diff-reading job's scope covers the fixture's committed edit
	// paths, so the bounded read returns the review delta.
	fields := map[string]any{
		"job_id": "diff-read-" + attempt, "objective": "read the bounded review delta",
		"stopping_condition": "the bounded review delta is read", "path_scope": []string{"internal/store"},
		"checks": []string{}, "ready": true, "readiness_evidence": []string{"evidence:ready"},
	}
	if err := recordWorkerJobFieldsForTest(t, j.s, j.workID, j.fixture.owner, fields); err != nil {
		t.Fatalf("record diff-read job: %v", err)
	}
	views, err := readWorkerJobRevisions(context.Background(), j.s.db, j.workID)
	if err != nil {
		t.Fatal(err)
	}
	var job WorkerJobBinding
	for _, view := range views {
		if view.Binding.JobID == "diff-read-"+attempt {
			job = view.Binding
		}
	}
	return job
}

func workerDiffDispatchJob(t *testing.T, j *candidateJourney, attempt string, job WorkerJobBinding) WorkerDiffRequest {
	t.Helper()
	digest := registeredLaneIdentity(t, "review")
	packet := joinPacketFor(t, j.s, j.workID, currentStep(t, j.s, j.workID), attempt, "review", digest)
	packet["inputs"].(map[string]any)["worker_job"] = recordedPacketJobForTest(t, j.s, j.workID, job, "review")
	correction, err := workflowCorrectionContextForDispatch(t.Context(), j.s.db, j.workID, currentStep(t, j.s, j.workID), attempt)
	if err != nil {
		t.Fatal(err)
	}
	if correction != nil {
		packet["inputs"].(map[string]any)["correction"] = correction
	}
	ctx, err := reviewSubjectPrepareDispatch(j.s, j.workID, j.repo)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dispatchJoinAttempt(ctx, t, j.s, j.workID, readWorkVersion(t, j.s, j.workID), j.fixture.owner, packet); err != nil {
		t.Fatal(err)
	}
	j.laneDispatch(t, attempt, "review", &job)
	window := reviewSubjectWindow(t, j.s, j.workID, attempt)
	raw, err := json.Marshal(packet)
	if err != nil {
		t.Fatal(err)
	}
	return WorkerDiffRequest{WorkID: j.workID, AttemptID: attempt, WorktreeIdentity: window.WorktreeIdentity, Packet: raw, Length: WorkerReadingPageMaxBytes}
}

func TestReadWorkerDiffMissingFirstHistoryRefusesRatherThanUsingLaterSubject(t *testing.T) {
	for _, missing := range []string{"worker_subject_commit", "worker_worktree_identity"} {
		t.Run(missing, func(t *testing.T) {
			db := workerFoundationCurrentDB(t)
			for seq := 2; seq <= 3; seq++ {
				subject, tree := strings.Repeat("a", 40), "claim"
				if seq == 2 {
					if missing == "worker_subject_commit" {
						subject = ""
					} else {
						tree = ""
					}
				}
				foundationDispatchForTest(t, db, int64(seq), 1, fmt.Sprintf("attempt:%d", seq), "implement", subject, tree)
			}
			base, err := firstWorkerEditingSubject(context.Background(), db, "work", "claim", 4)
			if !hasFailureKind(err, KindUnauthorizedDispatch) || base != "" {
				t.Fatalf("missing first %s selected a later subject: %q %v", missing, base, err)
			}
		})
	}
}

func TestReadWorkerDiffRefusesSelectorsAndRetiredAttempt(t *testing.T) {
	j, _ := descendantReviewFixture(t, "diff-refusal")
	head := workerDiffCommit(t, j, "change", "review change\n")
	original := workerDiffDispatch(t, j, "review-refusal")
	for _, change := range []func(*WorkerDiffRequest){
		func(r *WorkerDiffRequest) { r.WorkID = "" },
		func(r *WorkerDiffRequest) { r.AttemptID = "other" },
		func(r *WorkerDiffRequest) { r.WorktreeIdentity = "other" },
		func(r *WorkerDiffRequest) { r.Packet = json.RawMessage(`{}`) },
		func(r *WorkerDiffRequest) { r.Offset = -1 },
		func(r *WorkerDiffRequest) { r.Length = 0 },
		func(r *WorkerDiffRequest) { r.Length = WorkerReadingPageMaxBytes + 1 },
	} {
		req := original
		change(&req)
		probe := &workerReadingProbeRunner{GitRunner: CommittedReadGitRunner{}}
		req.Runner = probe
		if page, err := j.s.ReadWorkerDiff(context.Background(), req); err == nil || len(page.Data) != 0 || probe.calls != 0 {
			t.Fatalf("invalid selector exposed bytes or ran Git: %+v %v", page, err)
		}
	}
	if page, err := j.s.ReadWorkerDiff(context.Background(), original); err != nil || page.SubjectCommit != head || !bytes.Contains(page.Data, []byte("+review change")) {
		t.Fatalf("bound review read failed before the dirty-HEAD perturbation: %+v %v", page, err)
	}
	if err := os.WriteFile(filepath.Join(j.repo, "seed.txt"), []byte("dirty"), 0o600); err != nil {
		t.Fatal(err)
	}
	if page, err := j.s.ReadWorkerDiff(context.Background(), original); err != nil || page.SubjectCommit != head || !bytes.Contains(page.Data, []byte("+review change")) || bytes.Contains(page.Data, []byte("dirty")) {
		t.Fatalf("mutable repair leaked into the pinned input diff: %+v %v", page, err)
	}
	// A read-only assignment still requires exact clean B on every read.
	readOnly := workerDiffSnapshot{repo: j.repo, window: WorkerDispatchWindow{SubjectCommit: head}}
	if err := checkWorkerDiffSubject(context.Background(), CommittedReadGitRunner{}, readOnly); !hasFailureKind(err, KindUnauthorizedDispatch) {
		t.Fatalf("read-only dirty HEAD was admitted: %v", err)
	}
	gitRunStore(t, j.repo, "restore", "seed.txt")
	if page, err := j.s.ReadWorkerDiff(context.Background(), original); err != nil || page.SubjectCommit != head || !bytes.Contains(page.Data, []byte("+review change")) {
		t.Fatalf("bound review read failed before the mid-read retirement: %+v %v", page, err)
	}
	original.Runner = &workerReadingProbeRunner{GitRunner: CommittedReadGitRunner{}, once: func() {
		if err := j.s.Transact(context.Background(), func(tx *Transaction) error {
			mustTxExec(t, tx, `INSERT INTO write_guard(active) VALUES(1)`)
			mustTxExec(t, tx, `UPDATE worker_attempts SET lifecycle_state='failed',failed_at='2030-01-01T00:00:00Z',failure_kind='abandoned' WHERE work_id=? AND attempt_id=?`, original.WorkID, original.AttemptID)
			mustTxExec(t, tx, `DELETE FROM write_guard`)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}}
	page, err := j.s.ReadWorkerDiff(context.Background(), original)
	workerDiffRefusalShape(t, err, "worker diff read requires the exact live review dispatch and candidate binding")
	if len(page.Data) != 0 {
		t.Fatalf("attempt retired during Git returned bytes: %+v", page)
	}
}

func TestReadWorkerDiffRequiresNonemptyLiteralJobScope(t *testing.T) {
	j, _ := descendantReviewFixture(t, "diff-literal-scope")
	dir := filepath.Join(j.repo, "internal", "store")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	workerDiffCommit(t, j, "literal", "must not match a wildcard\n", func() {
		if err := os.WriteFile(filepath.Join(dir, "*"), []byte("literal star\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	})
	for i, scope := range [][]string{{"internal/store/*"}, {}} {
		jobID := fmt.Sprintf("diff-scope-%d", i)
		fields := map[string]any{"job_id": jobID, "objective": "read only the declared literal scope", "stopping_condition": "the bounded review delta is read", "path_scope": scope, "checks": []string{}, "ready": true, "readiness_evidence": []string{"evidence:ready"}}
		var err error
		if len(scope) == 0 {
			// A read-only job keeps the declared empty scope; the shared
			// helper's size members would author a per-job scope instead.
			if oracleCapablePinForWork(t, j.s, j.workID) {
				bootstrapOracleFixtureSubject(t, j.s, j.workID)
				fields["acceptance_oracle"] = acceptanceOracleFieldsForTest(t, j.s, j.workID)
			}
			err = recordWorkerJobActionForTest(t, j.s, j.workID, j.fixture.owner, fields)
		} else {
			err = recordWorkerJobFieldsForTest(t, j.s, j.workID, j.fixture.owner, fields)
		}
		if err != nil {
			t.Fatal(err)
		}
		views, err := readWorkerJobRevisions(context.Background(), j.s.db, j.workID)
		if err != nil {
			t.Fatal(err)
		}
		var binding WorkerJobBinding
		for _, view := range views {
			if view.Binding.JobID == jobID {
				binding = view.Binding
			}
		}
		req := workerDiffDispatchJob(t, j, fmt.Sprintf("scope-review-%d", i), binding)
		page, err := j.s.ReadWorkerDiff(context.Background(), req)
		if i == 1 {
			if !hasFailureKind(err, KindUnauthorizedDispatch) || len(page.Data) != 0 {
				t.Fatalf("empty scope exposed bytes: %+v %v", page, err)
			}
		} else if err != nil || !bytes.Contains(page.Data, []byte("+literal star")) || bytes.Contains(page.Data, []byte("must not match")) {
			t.Fatalf("literal scope widened to a glob: %s %v", page.Data, err)
		}
		workerDiffCompleteRead(t, j, req.AttemptID)
	}
}

func TestReadWorkerDiffCorrectionLoopKeepsFirstEditingSubject(t *testing.T) {
	j, base := descendantReviewFixture(t, "diff-correction")
	first := workerDiffCommit(t, j, "first", "first change\n")
	reviewSubjectImplementComplete(t, j.fixture, "correction-editor", "correction-completed")
	reviewSubjectAccept(t, j.s, j.workID, "correction-editor")
	head := workerDiffCommit(t, j, "second", "first change\n"+strings.Repeat("second change\n", 400))
	req := workerDiffDispatch(t, j, "review-diff")
	before := j.effects(t)
	var content []byte
	var hash string
	for {
		page, err := j.s.ReadWorkerDiff(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		if page.BaseCommit != base || page.BaseCommit == first || page.SubjectCommit != head || page.Offset != req.Offset || len(page.Data) > req.Length || page.NextOffset != page.Offset+len(page.Data) {
			t.Fatalf("page lost the original review delta: %+v", page)
		}
		if hash != "" && hash != page.ContentHash {
			t.Fatal("diff identity changed between pages")
		}
		hash = page.ContentHash
		content = append(content, page.Data...)
		if page.EOF {
			if len(content) != page.TotalBytes {
				t.Fatal("diff byte count differs from assembled pages")
			}
			break
		}
		req.Offset = page.NextOffset
	}
	if len(content) <= WorkerReadingPageMaxBytes || !bytes.Contains(content, []byte("+first change\n")) || !bytes.Contains(content, []byte("+second change\n")) || bytes.Contains(content, []byte("seed.txt")) || hash != fmt.Sprintf("sha256:%x", sha256.Sum256(content)) {
		t.Fatalf("diff omitted an earlier correction or escaped the job scope: %s", content)
	}
	if after := j.effects(t); after != before {
		t.Fatal("diff read wrote workflow or evidence state")
	}
	req.Offset = len(content)
	if page, err := j.s.ReadWorkerDiff(context.Background(), req); err != nil || !page.EOF || len(page.Data) != 0 || page.ContentHash != hash {
		t.Fatalf("EOF page lost immutable diff identity: %+v %v", page, err)
	}
	req.Offset++
	if page, err := j.s.ReadWorkerDiff(context.Background(), req); !hasFailureKind(err, KindInvalidPayload) || len(page.Data) != 0 {
		t.Fatalf("diff offset beyond EOF was accepted: %+v %v", page, err)
	}
}

func TestReadWorkerDiffSupersessionStartsNewEditingHistory(t *testing.T) {
	j, oldBase := descendantReviewFixture(t, "diff-supersession")
	base := workerDiffCommit(t, j, "old-contract", "old contract change\n")
	oldRequest := workerDiffDispatch(t, j, "old-review")
	workerDiffCompleteRead(t, j, oldRequest.AttemptID)
	reviewSubjectSupersedeContract(t, j.s, j.workID)
	acceptCurrentReviewForTest(t, j.fixture, j.repo, "continue")
	reviewSubjectImplementComplete(t, j.fixture, "new-contract-editor", "new-contract-completed")
	reviewSubjectAccept(t, j.s, j.workID, "new-contract-editor")
	head := workerDiffCommit(t, j, "new-contract", "old contract change\nnew contract change\n")
	req := workerDiffDispatch(t, j, "new-review")
	page, err := j.s.ReadWorkerDiff(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if page.BaseCommit != base || page.BaseCommit == oldBase || page.SubjectCommit != head || bytes.Contains(page.Data, []byte("+old contract change\n")) || !bytes.Contains(page.Data, []byte("+new contract change\n")) {
		t.Fatalf("diff crossed the contract boundary: %+v %s", page, page.Data)
	}
	oldRequest.Runner = &workerReadingProbeRunner{GitRunner: CommittedReadGitRunner{}}
	if page, err := j.s.ReadWorkerDiff(context.Background(), oldRequest); err == nil || len(page.Data) != 0 || oldRequest.Runner.(*workerReadingProbeRunner).calls != 0 {
		t.Fatalf("superseded review exposed bytes or ran Git: %+v %v", page, err)
	}
}

func TestReadWorkerDiffRetiredReviewRefusesBeforeGitWithExactDiffRecovery(t *testing.T) {
	j, _ := descendantReviewFixture(t, "diff-retired-before-read")
	head := workerDiffCommit(t, j, "change", "review change\n")
	original := workerDiffDispatch(t, j, "review-retired")
	if page, err := j.s.ReadWorkerDiff(context.Background(), original); err != nil || page.SubjectCommit != head || !bytes.Contains(page.Data, []byte("+review change")) {
		t.Fatalf("bound review read failed before the retirement: %+v %v", page, err)
	}
	if err := j.s.Transact(context.Background(), func(tx *Transaction) error {
		mustTxExec(t, tx, `INSERT INTO write_guard(active) VALUES(1)`)
		mustTxExec(t, tx, `UPDATE worker_attempts SET lifecycle_state='failed',failed_at='2030-01-01T00:00:00Z',failure_kind='abandoned' WHERE work_id=? AND attempt_id=?`, original.WorkID, original.AttemptID)
		mustTxExec(t, tx, `DELETE FROM write_guard`)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	probe := &workerReadingProbeRunner{GitRunner: CommittedReadGitRunner{}}
	original.Runner = probe
	page, err := j.s.ReadWorkerDiff(context.Background(), original)
	workerDiffRefusalShape(t, err, "worker diff read requires the exact live review dispatch and candidate binding")
	if len(page.Data) != 0 || probe.calls != 0 {
		t.Fatalf("retired review exposed bytes or ran Git: %+v calls=%d", page, probe.calls)
	}
}

func TestReadWorkerDiffMissingActiveContractRefusesBeforeGitWithExactDiffRecovery(t *testing.T) {
	j, _ := descendantReviewFixture(t, "diff-no-active-contract")
	head := workerDiffCommit(t, j, "change", "review change\n")
	original := workerDiffDispatch(t, j, "review-no-contract")
	if page, err := j.s.ReadWorkerDiff(context.Background(), original); err != nil || page.SubjectCommit != head || !bytes.Contains(page.Data, []byte("+review change")) {
		t.Fatalf("bound review read failed before the contract perturbation: %+v %v", page, err)
	}
	if err := j.s.Transact(context.Background(), func(tx *Transaction) error {
		mustTxExec(t, tx, `INSERT INTO write_guard(active) VALUES(1)`)
		// Preserve the version row and its foreign keys while removing active authority.
		mustTxExec(t, tx, `UPDATE workflow_contracts SET superseded_by=contract_version WHERE work_id=? AND superseded_by IS NULL`, original.WorkID)
		mustTxExec(t, tx, `DELETE FROM write_guard`)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	probe := &workerReadingProbeRunner{GitRunner: CommittedReadGitRunner{}}
	original.Runner = probe
	page, err := j.s.ReadWorkerDiff(context.Background(), original)
	workerDiffRefusalShape(t, err, "worker diff read requires an active workflow contract")
	if len(page.Data) != 0 || probe.calls != 0 {
		t.Fatalf("missing active contract exposed bytes or ran Git: %+v calls=%d", page, probe.calls)
	}
	var state string
	if err := j.s.db.QueryRow(`SELECT lifecycle_state FROM worker_attempts WHERE work_id=? AND attempt_id=?`, original.WorkID, original.AttemptID).Scan(&state); err != nil || (state != "in_flight" && state != "dispatched") {
		t.Fatalf("contract perturbation lost the recorded dispatch: state=%q err=%v", state, err)
	}
}

// A correction supersession with no editing dispatch of its own falls back
// to the predecessor boundary's editing history: the review still reads the
// whole delivery the supersession chain produced, instead of losing its
// committed diff to the bookkeeping supersessions the correction machinery
// forces between an accepted implement delivery and its review.
func TestReadWorkerDiffCorrectionSupersessionKeepsPredecessorBase(t *testing.T) {
	j, base := descendantReviewFixture(t, "diff-correction-supersession")
	first := workerDiffCommit(t, j, "first", "delivered change\n")
	reviewSubjectImplementComplete(t, j.fixture, "correction-editor", "correction-completed")
	reviewSubjectAccept(t, j.s, j.workID, "correction-editor")
	head := workerDiffCommit(t, j, "second", "delivered change\nmore delivered change\n")
	reviewSubjectSupersedeContract(t, j.s, j.workID)
	req := workerDiffDispatch(t, j, "review-after-correction-supersede")
	page, err := j.s.ReadWorkerDiff(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if page.BaseCommit != base || page.BaseCommit == first || page.SubjectCommit != head || !bytes.Contains(page.Data, []byte("+delivered change\n")) || !bytes.Contains(page.Data, []byte("+more delivered change\n")) {
		t.Fatalf("correction supersession lost the predecessor editing base: %+v %s", page, page.Data)
	}
}

// A failed review retry between two correction supersessions is not editing
// history: the retry's own dispatch must not qualify the newer supersession
// as a boundary with editing work under it, or the retry re-orphans the
// window the boundary fallback just repaired — the exact sequence a review
// escalation forces (supersede for the dispatch basis, review fails,
// supersede again, review retries).
func TestReadWorkerDiffReviewRetryDoesNotResetEditingBase(t *testing.T) {
	j, base := descendantReviewFixture(t, "diff-review-retry-base")
	workerDiffCommit(t, j, "first", "delivered change\n")
	reviewSubjectImplementComplete(t, j.fixture, "correction-editor", "correction-completed")
	reviewSubjectAccept(t, j.s, j.workID, "correction-editor")
	head := workerDiffCommit(t, j, "second", "delivered change\nmore delivered change\n")
	reviewSubjectSupersedeContract(t, j.s, j.workID)
	first := workerDiffDispatch(t, j, "review-first")
	if page, err := j.s.ReadWorkerDiff(context.Background(), first); err != nil || page.BaseCommit != base || page.SubjectCommit != head {
		t.Fatalf("first review after the supersession lost its base: %+v %v", page, err)
	}
	// The first review attempt ends without new editing work; the
	// correction machinery forces a second supersession before the retry
	// dispatches. The retry's own dispatch must not qualify the newer
	// supersession, and neither must the first review's.
	lane := reviewGateLane(t, "review")
	if err := s2WorkerFailedLive(t, j.s, s2WorkerFailedAttributedEvent(j.workID, first.AttemptID, "failed-review-first", preferredModelForLane(lane), WorkerFailureWorkerError, "the review failed without editing the candidate", time.Unix(30, 0).UTC())); err != nil {
		t.Fatalf("record the first review's terminal failure: %v", err)
	}
	reviewSubjectSupersedeContractVersions(t, j.s, j.workID, 2, 3, "the correction window forces a fresh basis supersession before the review retry")
	retry := workerDiffDispatch(t, j, "review-retry")
	page, err := j.s.ReadWorkerDiff(context.Background(), retry)
	if err != nil {
		t.Fatal(err)
	}
	if page.BaseCommit != base || page.SubjectCommit != head || !bytes.Contains(page.Data, []byte("+delivered change\n")) {
		t.Fatalf("review retry re-orphaned the editing base: %+v %s", page, page.Data)
	}
}

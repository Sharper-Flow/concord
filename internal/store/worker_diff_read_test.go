package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func workerDiffCommit(t *testing.T, j *candidateJourney, id, content string) string {
	t.Helper()
	dir := filepath.Join(j.repo, "internal", "store")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "change.txt"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return descendantCommit(t, j, id)
}

func workerDiffDispatch(t *testing.T, j *candidateJourney, attempt string) WorkerDiffRequest {
	t.Helper()
	// The diff-reading job's scope covers the fixture's committed edit
	// paths, so the bounded read returns the review delta.
	fields := map[string]any{
		"job_id": "diff-read-" + attempt, "objective": "read the bounded review delta",
		"stopping_condition": "the bounded review delta is read", "path_scope": []string{"internal/store"},
		"checks": []string{}, "ready": true, "readiness_evidence": []string{"evidence:ready"},
	}
	if sizeCapablePinForWork(t, j.s, j.workID) {
		bootstrapOracleFixtureSubject(t, j.s, j.workID)
		fields["acceptance_oracle"] = acceptanceOracleFieldsForTest(t, j.s, j.workID)
		for member, value := range workerJobSizeMembersForTest(t, j.s, j.workID, fields) {
			fields[member] = value
		}
	}
	if err := recordWorkerJobActionForTest(t, j.s, j.workID, j.fixture.owner, fields); err != nil {
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
	return workerDiffDispatchJob(t, j, attempt, job)
}

func workerDiffDispatchJob(t *testing.T, j *candidateJourney, attempt string, job WorkerJobBinding) WorkerDiffRequest {
	t.Helper()
	version, digest := registeredLaneIdentity(t, "review")
	packet := joinPacketFor(t, j.s, j.workID, currentStep(t, j.s, j.workID), attempt, "review", version, digest)
	packet["inputs"].(map[string]any)["worker_job"] = recordedPacketJobForTest(t, j.s, j.workID, job, "review")
	ctx, err := reviewSubjectPrepareDispatch(j.s, j.workID, j.repo)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dispatchJoinAttempt(ctx, t, j.s, j.workID, readWorkVersion(t, j.s, j.workID), j.fixture.owner, packet); err != nil {
		t.Fatal(err)
	}
	window := reviewSubjectWindow(t, j.s, j.workID, attempt)
	raw, err := json.Marshal(packet)
	if err != nil {
		t.Fatal(err)
	}
	return WorkerDiffRequest{WorkID: j.workID, AttemptID: attempt, WorktreeIdentity: window.WorktreeIdentity, Packet: raw, Length: WorkerReadingPageMaxBytes}
}

func TestReadWorkerDiffMissingFirstHistoryRefusesRatherThanUsingLaterSubject(t *testing.T) {
	version, digest := registeredLaneIdentity(t, "implement")
	for _, missing := range []string{"worker_subject_commit", "worker_worktree_identity"} {
		t.Run(missing, func(t *testing.T) {
			db, err := sql.Open("sqlite", ":memory:")
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if _, err := db.Exec(`CREATE TABLE domain_events(seq INTEGER PRIMARY KEY,subject_type TEXT,subject_id TEXT,kind TEXT,payload TEXT); CREATE TABLE worker_attempts(work_id TEXT,attempt_id TEXT,lane_id TEXT,lane_version INTEGER,lane_digest TEXT)`); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO domain_events VALUES(1,'work_item','work',?,?)`, WorkflowContractApproved, `{"contract_version":1}`); err != nil {
				t.Fatal(err)
			}
			for seq := 2; seq <= 3; seq++ {
				payload := map[string]any{"action_id": "dispatch_worker", "worker_lane_id": "implement", "worker_lane_version": version, "worker_lane_digest": digest, "worker_worktree_identity": "claim", "worker_subject_commit": strings.Repeat("a", 40)}
				if seq == 2 {
					delete(payload, missing)
				}
				if _, err := db.Exec(`INSERT INTO domain_events VALUES(?,'work_item','work',?,?)`, seq, WorkflowActionCompleted, string(mustJSONValue(payload))); err != nil {
					t.Fatal(err)
				}
			}
			base, err := firstWorkerEditingSubject(context.Background(), db, "work", "claim", 1, 4)
			if !hasFailureKind(err, KindUnauthorizedDispatch) || base != "" {
				t.Fatalf("missing first %s selected a later subject: %q %v", missing, base, err)
			}
		})
	}
}

func TestReadWorkerDiffRefusesSelectorsDirtyHeadAndRetiredAttempt(t *testing.T) {
	j, _ := descendantReviewFixture(t, "diff-refusal")
	workerDiffCommit(t, j, "change", "review change\n")
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
	if err := os.WriteFile(filepath.Join(j.repo, "seed.txt"), []byte("dirty"), 0o600); err != nil {
		t.Fatal(err)
	}
	if page, err := j.s.ReadWorkerDiff(context.Background(), original); !hasFailureKind(err, KindUnauthorizedDispatch) || len(page.Data) != 0 {
		t.Fatalf("dirty review returned bytes: %+v %v", page, err)
	}
	gitRunStore(t, j.repo, "restore", "seed.txt")
	original.Runner = &workerReadingProbeRunner{GitRunner: CommittedReadGitRunner{}, once: func() {
		if err := j.s.Transact(context.Background(), func(tx *Transaction) error {
			mustTxExec(t, tx, `INSERT INTO fold_guard(active) VALUES(1)`)
			mustTxExec(t, tx, `UPDATE worker_attempts SET lifecycle_state='failed',failed_at='2030-01-01T00:00:00Z',failure_kind='abandoned' WHERE work_id=? AND attempt_id=?`, original.WorkID, original.AttemptID)
			mustTxExec(t, tx, `DELETE FROM fold_guard`)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}}
	if page, err := j.s.ReadWorkerDiff(context.Background(), original); err == nil || len(page.Data) != 0 {
		t.Fatalf("attempt retired during Git returned bytes: %+v %v", page, err)
	}
}

func TestReadWorkerDiffRequiresNonemptyLiteralJobScope(t *testing.T) {
	j, _ := descendantReviewFixture(t, "diff-literal-scope")
	dir := filepath.Join(j.repo, "internal", "store")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "*"), []byte("literal star\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	workerDiffCommit(t, j, "literal", "must not match a wildcard\n")
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
	}
}

func TestReadWorkerDiffCorrectionLoopKeepsFirstEditingSubject(t *testing.T) {
	j, base := descendantReviewFixture(t, "diff-correction")
	first := workerDiffCommit(t, j, "first", "first change\n")
	reviewSubjectImplementComplete(t, j.fixture, "correction-editor", "correction-completed", true)
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
	reviewSubjectSupersedeContract(t, j.s, j.workID)
	reviewSubjectImplementComplete(t, j.fixture, "new-contract-editor", "new-contract-completed", true)
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

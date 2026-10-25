package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func workerReadingFixture(t *testing.T, workID string) (workContextFixture, WorkerReadingRequest) {
	t.Helper()
	f := readingResolutionFixture(t, workID, readingSources)
	job := reviewSubjectReadyRevision(t, f)
	attempt := "attempt-" + workID
	packet := workContextDispatchPacket(t, f, attempt, job, currentFixtureView(t, f))
	if err := freshnessDispatch(t, f, attempt, packet); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(packet)
	if err != nil {
		t.Fatal(err)
	}
	window, err := findAuthorizedDispatchWindowBefore(context.Background(), f.store.db, f.workID, attempt, 0)
	if err != nil {
		t.Fatal(err)
	}
	return f, WorkerReadingRequest{WorkID: f.workID, AttemptID: attempt, WorktreeIdentity: window.WorktreeIdentity, Packet: raw, Length: WorkerReadingPageMaxBytes}
}

func TestReadWorkerReadingReturnsOnlyThePinnedDeclaredPage(t *testing.T) {
	f, request := workerReadingFixture(t, "work-reader-pages")
	defer f.store.Close()
	ctx := context.Background()
	witnesses, err := sampleDispatchReadingIdentities(ctx, f.store.db, f.workID, currentFixtureView(t, f).RequiredReading)
	if err != nil {
		t.Fatal(err)
	}
	var eventsBefore int
	if err := f.store.db.QueryRow(`SELECT count(*) FROM effect_receipts`).Scan(&eventsBefore); err != nil {
		t.Fatal(err)
	}
	for ordinal, witness := range witnesses {
		repo, commit, path := witness.repoPath, witness.commitOID, witness.path
		if witness.arm == WorkContextSourceKnowledge {
			repo, commit, path = witness.sourceRepo, witness.recordCommit, witness.recordPath
		}
		expected := []byte(fixtureGitRun(t, repo, "cat-file", "blob", commit+":"+path))
		if err := os.WriteFile(filepath.Join(repo, path), []byte("changed checkout"), 0o600); err != nil {
			t.Fatal(err)
		}
		request.Ordinal, request.Offset, request.Length = ordinal, 0, 7
		page, err := f.store.ReadWorkerReading(ctx, request)
		if err != nil {
			t.Fatal(err)
		}
		end := min(len(expected), request.Length)
		expectedHash := fmt.Sprintf("sha256:%x", sha256.Sum256(expected))
		if page.SourceIdentity != witness.identity || page.ContentHash != expectedHash || page.Offset != 0 || page.NextOffset != end || page.TotalBytes != len(expected) || !bytes.Equal(page.Data, expected[:end]) {
			t.Fatalf("reading %d page = %+v, want only the selected pinned bytes", ordinal, page)
		}
		request.Offset, request.Length = page.NextOffset, WorkerReadingPageMaxBytes
		tail, err := f.store.ReadWorkerReading(ctx, request)
		if err != nil {
			t.Fatal(err)
		}
		tailEnd := min(len(expected), request.Offset+request.Length)
		if !bytes.Equal(tail.Data, expected[request.Offset:tailEnd]) || tail.NextOffset != tailEnd || tail.EOF != (tailEnd == len(expected)) || tail.ContentHash != page.ContentHash {
			t.Fatalf("reading %d tail = %+v, want contiguous pinned bytes", ordinal, tail)
		}
		request.Offset = len(expected)
		eof, err := f.store.ReadWorkerReading(ctx, request)
		if err != nil || !eof.EOF || eof.NextOffset != len(expected) || len(eof.Data) != 0 || eof.ContentHash != expectedHash {
			t.Fatalf("reading %d EOF page = %+v, error=%v", ordinal, eof, err)
		}
		request.Offset++
		past, err := f.store.ReadWorkerReading(ctx, request)
		if !hasFailureKind(err, KindInvalidPayload) || len(past.Data) != 0 {
			t.Fatalf("reading %d accepted an offset beyond EOF: page=%+v error=%v", ordinal, past, err)
		}
	}
	var eventsAfter int
	if err := f.store.db.QueryRow(`SELECT count(*) FROM effect_receipts`).Scan(&eventsAfter); err != nil || eventsAfter != eventsBefore {
		t.Fatalf("read changed the event log: before=%d after=%d error=%v", eventsBefore, eventsAfter, err)
	}
}

type workerReadingProbeRunner struct {
	GitRunner
	calls int
	once  func()
}

func (r *workerReadingProbeRunner) Run(ctx context.Context, repo string, args ...string) ([]byte, error) {
	r.calls++
	if r.once != nil {
		call := r.once
		r.once = nil
		call()
	}
	return r.GitRunner.Run(ctx, repo, args...)
}

func TestReadWorkerReadingRefusesUnboundSelectorsBeforeGit(t *testing.T) {
	f, original := workerReadingFixture(t, "work-reader-refusals")
	defer f.store.Close()
	for _, change := range []func(*WorkerReadingRequest){
		func(r *WorkerReadingRequest) { r.WorkID = "" },
		func(r *WorkerReadingRequest) { r.AttemptID = "" },
		func(r *WorkerReadingRequest) { r.AttemptID = "foreign-attempt" },
		func(r *WorkerReadingRequest) { r.WorktreeIdentity = "sha256:" + strings.Repeat("f", 64) },
		func(r *WorkerReadingRequest) {
			r.Packet = json.RawMessage(`{"work_id":"different","attempt_id":"different"}`)
		},
		func(r *WorkerReadingRequest) { r.Ordinal = 2 },
		func(r *WorkerReadingRequest) { r.Ordinal = -1 },
		func(r *WorkerReadingRequest) { r.Offset = -1 },
		func(r *WorkerReadingRequest) { r.Length = 0 },
		func(r *WorkerReadingRequest) { r.Length = WorkerReadingPageMaxBytes + 1 },
	} {
		request := original
		change(&request)
		runner := &workerReadingProbeRunner{GitRunner: CommittedReadGitRunner{}}
		request.Runner = runner
		page, err := f.store.ReadWorkerReading(context.Background(), request)
		if err == nil || len(page.Data) != 0 || runner.calls != 0 {
			t.Fatalf("unbound request exposed bytes or ran Git: page=%+v calls=%d error=%v", page, runner.calls, err)
		}
	}
	if err := f.store.Transact(context.Background(), func(tx *Transaction) error {
		mustTxExec(t, tx, `INSERT INTO write_guard(active) VALUES(1)`)
		mustTxExec(t, tx, `UPDATE worker_attempts SET lifecycle_state='failed',failed_at='2030-01-01T00:00:00Z',failure_kind='abandoned' WHERE work_id=? AND attempt_id=?`, original.WorkID, original.AttemptID)
		mustTxExec(t, tx, `DELETE FROM write_guard`)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	runner := &workerReadingProbeRunner{GitRunner: CommittedReadGitRunner{}}
	original.Runner = runner
	page, err := f.store.ReadWorkerReading(context.Background(), original)
	if err == nil || len(page.Data) != 0 || runner.calls != 0 {
		t.Fatalf("terminal attempt exposed bytes or ran Git: page=%+v calls=%d error=%v", page, runner.calls, err)
	}
}

func TestReadWorkerReadingRevalidatesRegistrationAfterGit(t *testing.T) {
	f, request := workerReadingFixture(t, "work-reader-drift")
	defer f.store.Close()
	request.Runner = &workerReadingProbeRunner{GitRunner: CommittedReadGitRunner{}, once: func() {
		if err := f.store.Transact(context.Background(), func(tx *Transaction) error {
			mustTxExec(t, tx, `INSERT INTO write_guard(active) VALUES(1)`)
			mustTxExec(t, tx, `UPDATE project_locators SET updated_at='2030-01-01T00:00:00Z' WHERE project_id=?`, f.sources.repoProject)
			mustTxExec(t, tx, `DELETE FROM write_guard`)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}}
	page, err := f.store.ReadWorkerReading(context.Background(), request)
	if !hasFailureKind(err, KindStaleRequiresReview) || len(page.Data) != 0 {
		t.Fatalf("changed registration exposed bytes: page=%+v error=%v", page, err)
	}
}

func TestReadWorkerReadingRevalidatesAttemptLivenessAfterGit(t *testing.T) {
	f, request := workerReadingFixture(t, "work-reader-terminal-drift")
	defer f.store.Close()
	request.Runner = &workerReadingProbeRunner{GitRunner: CommittedReadGitRunner{}, once: func() {
		if err := f.store.Transact(context.Background(), func(tx *Transaction) error {
			mustTxExec(t, tx, `INSERT INTO write_guard(active) VALUES(1)`)
			mustTxExec(t, tx, `UPDATE worker_attempts SET lifecycle_state='failed',failed_at='2030-01-01T00:00:00Z',failure_kind='abandoned' WHERE work_id=? AND attempt_id=?`, request.WorkID, request.AttemptID)
			mustTxExec(t, tx, `DELETE FROM write_guard`)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}}
	page, err := f.store.ReadWorkerReading(context.Background(), request)
	if !hasFailureKind(err, KindUnauthorizedDispatch) || len(page.Data) != 0 {
		t.Fatalf("terminal attempt exposed bytes after Git: page=%+v error=%v", page, err)
	}
}

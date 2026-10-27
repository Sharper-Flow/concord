package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

const nativeOracleStreamLimit = 2 * 1024 * 1024
const nativeOraclePageLimit = 16 * 1024

type NativeOracleStream struct {
	Stream   string `json:"stream"`
	Length   int64  `json:"length"`
	SHA256   string `json:"sha256"`
	Complete bool   `json:"complete"`
	Ref      string `json:"ref"`
}

type NativeOracleOutputPage struct {
	RunRef      string `json:"run_ref"`
	Stream      string `json:"stream"`
	Offset      int64  `json:"offset"`
	DataBase64  string `json:"data_base64"`
	Length      int64  `json:"length"`
	TotalLength int64  `json:"total_length"`
	SHA256      string `json:"sha256"`
	Complete    bool   `json:"complete"`
	NextOffset  int64  `json:"next_offset"`
	EOF         bool   `json:"eof"`
}

func nativeDigest(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// Raw buffers never enter a JSON type. Metadata has a separate capture;
// the remaining native stages share the retained evidence streams.
type nativeStreamCapture struct {
	mu                      sync.Mutex
	stdout, stderr, preview []byte
	complete                bool
	previewTruncated        bool
	isolationEstablished    bool
	isolationReaped         bool
}

type nativeCaptureWriter struct {
	capture *nativeStreamCapture
	stderr  bool
	cancel  context.CancelFunc
}

func (w nativeCaptureWriter) Write(b []byte) (int, error) {
	w.capture.mu.Lock()
	defer w.capture.mu.Unlock()
	target := &w.capture.stdout
	if w.stderr {
		target = &w.capture.stderr
	}
	n := len(b)
	if n > nativeOracleStreamLimit-len(*target) {
		n = nativeOracleStreamLimit - len(*target)
		w.capture.complete = false
		w.cancel()
	}
	*target = append(*target, b[:n]...)
	room := defaultWorktreeVerifyBytes - len(w.capture.preview)
	if room < len(b) {
		w.capture.previewTruncated = true
	} else {
		room = len(b)
	}
	w.capture.preview = append(w.capture.preview, b[:room]...)
	return len(b), nil
}

// Unisolated children own process groups; cancellation and overflow stop them.
// Required isolation delegates lifetime and teardown to its private supervisor.
// The stage reads its own output pipes because os/exec reports a stream that
// WaitDelay cut only when the child exited successfully.
func runNativeOracleStage(ctx context.Context, dir string, argv, env []string, stdin io.Reader, capture *nativeStreamCapture, launched func(), isolation ...*nativeStageIsolation) (int, error) {
	if err := validateWorktreeVerifyCommand(argv); err != nil {
		return -1, err
	}
	if len(isolation) > 1 {
		return -1, nativeIsolationUnavailable("multiple isolation plans")
	}
	if len(isolation) == 1 && isolation[0] != nil {
		return runNativeIsolatedStage(ctx, dir, argv, env, stdin, capture, launched, isolation[0])
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var readers, writers []*os.File
	defer func() {
		for _, f := range append(readers, writers...) {
			_ = f.Close()
		}
	}()
	for range 2 {
		r, w, err := os.Pipe()
		if err != nil {
			return -1, err
		}
		readers, writers = append(readers, r), append(writers, w)
	}
	cmd := exec.CommandContext(runCtx, argv[0], argv[1:]...) //nolint:gosec // producer-generated bounded argv, no shell.
	cmd.Dir, cmd.Env, cmd.Stdin = dir, env, stdin
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = worktreeVerifyFinalizeTimeout
	cmd.Stdout, cmd.Stderr = writers[0], writers[1]
	err := cmd.Start()
	for _, w := range writers {
		_ = w.Close()
	}
	writers = nil
	if err != nil {
		return -1, err
	}
	copied := make(chan error, len(readers))
	for i, r := range readers {
		w := nativeCaptureWriter{capture: capture, stderr: i == 1, cancel: cancel}
		go func() {
			_, err := io.Copy(w, r)
			copied <- err
		}()
	}
	if launched != nil {
		launched()
	}
	err = cmd.Wait()
	cut := drainNativeStreams(copied, readers)
	if cut != nil {
		capture.complete = false
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	if runCtx.Err() != nil {
		return -1, runCtx.Err()
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode(), cut
	}
	if err != nil {
		return -1, err
	}
	return 0, cut
}

var errNativeStreamCut = errors.New("native output stream stayed open past the finalize delay")

// drainNativeStreams waits for every stream to reach end of file. A stream
// still open the finalize delay after the child exited is cut.
func drainNativeStreams(copied <-chan error, readers []*os.File) error {
	timer := time.NewTimer(worktreeVerifyFinalizeTimeout)
	defer timer.Stop()
	var cut error
	for pending := len(readers); pending > 0; {
		select {
		case err := <-copied:
			pending--
			if cut == nil {
				cut = err
			}
		case <-timer.C:
			cut = errNativeStreamCut
			for _, r := range readers {
				_ = r.Close()
			}
		}
	}
	return cut
}

func nativeStreamDescriptor(runRef, stream string, b []byte, complete bool) NativeOracleStream {
	d := NativeOracleStream{Stream: stream, Length: int64(len(b)), SHA256: nativeDigest(b), Complete: complete}
	if complete {
		d.Ref = "oracle_output:" + nativeDigest([]byte(runRef+"\x00"+stream+"\x00"+d.SHA256))
	}
	return d
}

func marshalNativeVerifyRecord(result *WorktreeVerifyResult, capture *nativeStreamCapture) ([]byte, error) {
	result.Output = string(capture.preview)
	result.OutputTruncated = capture.previewTruncated
	result.Oracle.Stdout = nativeStreamDescriptor(result.OperationRef, "stdout", capture.stdout, capture.complete)
	result.Oracle.Stderr = nativeStreamDescriptor(result.OperationRef, "stderr", capture.stderr, capture.complete)
	result.Oracle.StreamsComplete = capture.complete
	raw, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	if len(raw) > 48*1024 {
		// Preview truncation is independent of complete raw-stream retention.
		result.Output = ""
		result.OutputTruncated = true
		raw, err = json.Marshal(result)
		if err != nil {
			return nil, err
		}
	}
	if len(raw) > 48*1024 {
		// The immutable plan still retains the full identity inventory. This
		// bounded diagnostic cannot qualify as readiness or execution evidence.
		result.Oracle.Qualification = "unavailable"
		result.Oracle.Detail = "native metadata exceeds the unchanged result envelope; the immutable plan retains the full inventory"
		result.Oracle.Files = []NativeOracleFile{}
		result.Oracle.SelectedTestNames = []string{}
		result.Oracle.CaseToTestMap = map[string][]string{}
		result.Oracle.SelectedDistinctCount = 0
		result.Oracle.ObservedTestNames = nil
		result.Oracle.Stages = []NativeOracleStage{}
		raw, err = json.Marshal(result)
		if err != nil {
			return nil, err
		}
	}
	if len(raw) > 48*1024 {
		return nil, oracleFailure(KindLimitExceeded, "native diagnostic exceeds the existing result envelope", "reduce the declared native identities")
	}
	return raw, nil
}

func (s *Store) readNativeVerifyOutput(ctx context.Context, req WorktreeInspectRequest) (WorktreeInspectResult, error) {
	out := WorktreeInspectResult{WorkID: req.WorkID, ProjectID: req.ProjectID, Mode: req.Mode}
	if req.WorkID == "" || req.ProjectID == "" || req.RunRef == "" || req.Path != "" || req.Offset < 0 || req.Length < 1 || req.Length > nativeOraclePageLimit || (req.Stream != "stdout" && req.Stream != "stderr") {
		return out, oracleFailure(KindInvalidOperation, "oracle output requires a scoped run, stdout or stderr, nonnegative offset, and 1 to 16384 bytes", "supply a bounded output-page request")
	}
	if err := publishedWorkAbsenceInScopeQ(ctx, s.db, req.WorkID, "", req.ProjectID, "worker_oracle"); err != nil {
		return out, err
	}
	var member *WorkflowMemberCaller
	if owner := sessionWorktreeOwnerFromContext(ctx); owner != (SessionWorktreeOwner{}) {
		actor, err := workflowMemberOwnerActor(ctx, s.db, owner)
		if err != nil {
			return out, err
		}
		member, err = readWorkflowMemberCallerQ(ctx, s.db, actor)
		if err != nil {
			return out, err
		}
		if member != nil && (member.WorkID != req.WorkID || member.ProjectID != req.ProjectID) {
			return out, oracleFailure(KindUnauthorized, "member caller cannot read oracle output outside its retained work/Project scope", "use the recorded member assignment")
		}
	}
	column := "stdout_blob"
	if req.Stream == "stderr" {
		column = "stderr_blob"
	}
	var blob []byte
	var raw string
	query := `SELECT ` + column + `,result_json FROM worktree_verify_leases l WHERE work_id=? AND project_id=? AND state='released' AND native_plan_json IS NOT NULL AND json_extract(result_json,'$.operation_ref')=?`
	args := []any{req.WorkID, req.ProjectID, req.RunRef}
	if member != nil {
		// Claims retain their native path after reclaim. Read authority follows
		// the retained member, not a live binding or the executing lane's owner.
		query += ` AND EXISTS(SELECT 1 FROM worktree_claims c WHERE c.work_id=l.work_id AND c.project_id=l.project_id AND c.member_id=? AND c.pinned_path=l.path)`
		args = append(args, member.MemberID)
	}
	err := s.db.QueryRowContext(ctx, query, args...).Scan(&blob, &raw)
	if err == sql.ErrNoRows {
		return out, oracleFailure(KindProjectionNotFound, "no retained oracle output exists in this work/Project scope", "use this work's native run reference")
	}
	if err != nil {
		return out, err
	}
	if blob == nil {
		return out, oracleFailure(KindUnavailable, "the stream was not retained", "request a new authorized native run")
	}
	var result WorktreeVerifyResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil || result.Oracle == nil {
		return out, oracleFailure(KindInvariantViolation, "retained oracle metadata is malformed", "reconcile the native run")
	}
	d := result.Oracle.Stdout
	if req.Stream == "stderr" {
		d = result.Oracle.Stderr
	}
	if d.Stream != req.Stream || d.Length != int64(len(blob)) || d.SHA256 != nativeDigest(blob) || len(blob) > nativeOracleStreamLimit {
		return out, oracleFailure(KindInvariantViolation, "retained stream length or hash differs from its producer", "reconcile the native run")
	}
	if req.Offset > d.Length {
		return out, oracleFailure(KindInvalidOperation, "output offset exceeds retained length", "select an offset within the retained stream")
	}
	end := min(d.Length, req.Offset+req.Length)
	out.OracleOutput = &NativeOracleOutputPage{RunRef: req.RunRef, Stream: req.Stream, Offset: req.Offset, DataBase64: base64.StdEncoding.EncodeToString(blob[req.Offset:end]), Length: end - req.Offset, TotalLength: d.Length, SHA256: d.SHA256, Complete: d.Complete, NextOffset: end, EOF: end == d.Length}
	return out, nil
}

package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/agent"
	"github.com/sharper-flow/concord/internal/store"
)

// Core-observed terminal delivery at the CLI ingress. A
// completed implementation-lane report records one closed observed_delivery
// value the core observed itself — raw commit plus clean or dirty — from the
// attempt's authorized Project worktree, sampled before the store transaction
// under one fixed deadline and revalidated inside the signed terminal ingest.
// Git failure or deadline refuses with zero terminal, derived, or nonce
// effects; there is no unavailable observation. Failed terminals never probe
// Git. An exact replay acknowledgment returns the original observation
// without Git.

// seedGitFixtureWorktree makes the claimed worktree one real git repository
// with a single committed seed file, the shape a production claimed worktree
// has. The terminal delivery observation reads it read-only.
func seedGitFixtureWorktree(t *testing.T, dir string) {
	t.Helper()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...) //nolint:gosec // test-only git argv, fixed binary
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			t.Fatalf("seed git worktree %v: %v: %s", args, err, stderr.String())
		}
	}
	run("init", "-b", "main")
	run("config", "user.email", "concord@example.invalid")
	run("config", "user.name", "Concord Worker Evidence Test")
	if err := os.WriteFile(filepath.Join(dir, "seed.txt"), []byte("claimed worktree seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "-A")
	run("commit", "--allow-empty", "-m", "claimed worktree seed")
}

// implementCLILane returns the registered implement lane.
func implementCLILane(t *testing.T) store.LaneDefinition {
	t.Helper()
	for _, definition := range store.BuiltinLaneDefinitions() {
		if definition.ID == "implement" {
			return definition
		}
	}
	t.Fatal("no registered implement lane")
	return store.LaneDefinition{}
}

// cliFixtureWorktree is the claimed worktree path seedAuthorizedDispatchWindow
// created for work-1.
func cliFixtureWorktree(t *testing.T, dbPath string) string {
	t.Helper()
	return filepath.Join(filepath.Dir(dbPath), "worktree-work-1")
}

// runGitCLI runs one git command in dir and returns trimmed stdout. It
// returns an empty string when dir is not a git repository, so a fixture
// that predates the repository-backed worktree fails the observation
// assertions for the missing behavior instead of failing setup.
func runGitCLI(t *testing.T, dir string, args ...string) string {
	t.Helper()
	probe := exec.Command("git", "-C", dir, "rev-parse", "--is-inside-work-tree") //nolint:gosec // test-only git argv, fixed binary
	if err := probe.Run(); err != nil {
		return ""
	}
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...) //nolint:gosec // test-only git argv, fixed binary
	raw, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v in %s: %v", args, dir, err)
	}
	return strings.TrimSpace(string(raw))
}

// commitWorktreeFixtureTree commits every change in the fixture worktree and
// returns the new HEAD OID, or "" when the directory is not a repository.
func commitWorktreeFixtureTree(t *testing.T, dir, message string) string {
	t.Helper()
	if runGitCLI(t, dir, "rev-parse", "--is-inside-work-tree") != "true" {
		return ""
	}
	runGitCLISilent(t, dir, "add", "-A")
	runGitCLISilent(t, dir, "-c", "user.email=concord@example.invalid", "-c", "user.name=Concord Item4 Test", "commit", "--allow-empty", "-m", message)
	return runGitCLI(t, dir, "rev-parse", "--verify", "HEAD^{commit}")
}

func runGitCLISilent(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...) //nolint:gosec // test-only git argv, fixed binary
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("git %v in %s: %v: %s", args, dir, err, stderr.String())
	}
}

// hideGitDir temporarily renames a repository's .git away, so the observation
// probe faces an unreachable repository while the recorded history stays
// restorable. The returned func restores the directory.
func hideGitDir(t *testing.T, worktree string) func() {
	t.Helper()
	gitDir := filepath.Join(worktree, ".git")
	if _, err := os.Stat(gitDir); err != nil {
		if os.IsNotExist(err) {
			return func() {}
		}
		t.Fatal(err)
	}
	held := gitDir + "-held"
	if err := os.Rename(gitDir, held); err != nil {
		t.Fatal(err)
	}
	return func() {
		if err := os.Rename(held, gitDir); err != nil {
			t.Fatal(err)
		}
	}
}

// writeFakeGit writes one executable git wrapper into its own PATH directory
// and returns that directory.
func writeFakeGit(t *testing.T, script string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "git")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// observationProbeGate is the shell condition that matches only the terminal
// observation probe's git commands (status --porcelain, rev-parse --verify),
// never the CLI's own database-override repository probe (rev-parse
// --show-toplevel) or any other caller's git.
const observationProbeGate = `[ "$3" = "status" ] || { [ "$3" = "rev-parse" ] && [ "$4" = "--verify" ]; }`

// writeBlockingObservationGit writes a git wrapper that passes every command
// through to the real git except the observation probe, which signals the
// entry file, waits for the release file, and then fails.
func writeBlockingObservationGit(t *testing.T, entry, release string) string {
	t.Helper()
	script := fmt.Sprintf(`#!/bin/sh
real_git="/usr/bin/git"
if %s; then
  touch %q
  for i in $(seq 1 600); do [ -f %q ] && break; sleep 0.1; done
  exit 1
fi
exec "$real_git" "$@"
`, observationProbeGate, entry, release)
	return writeFakeGit(t, script)
}

// writeSlowObservationGit writes a git wrapper that passes every command
// through to the real git except the observation probe, which hangs past the
// probe deadline and then fails.
func writeSlowObservationGit(t *testing.T) string {
	t.Helper()
	script := fmt.Sprintf(`#!/bin/sh
real_git="/usr/bin/git"
if %s; then
  sleep 30
  exit 1
fi
exec "$real_git" "$@"
`, observationProbeGate)
	return writeFakeGit(t, script)
}

// writeDriftGit writes a git wrapper that passes every command through to the
// real git, but applies one SQLite drift statement when the observation
// probe's rev-parse runs — the mutation lands after the preparation sampled
// the claim and window, before the transaction revalidates them.
func writeDriftGit(t *testing.T, dbPath, driftSQL string) string {
	t.Helper()
	script := fmt.Sprintf(`#!/bin/sh
real_git="/usr/bin/git"
if [ "$3" = "rev-parse" ] && [ "$4" = "--verify" ]; then
  sqlite3 %q "%s" || exit 90
fi
exec "$real_git" "$@"
`, dbPath, driftSQL)
	return writeFakeGit(t, script)
}

// typedWorkerEvidenceError parses the typed failure result the worker
// evidence CLI writes on stdout.
func typedWorkerEvidenceError(t *testing.T, out string) (kind, effectState string, retrySafe bool) {
	t.Helper()
	var body struct {
		OK    bool `json:"ok"`
		Error struct {
			Kind        string `json:"kind"`
			EffectState string `json:"effect_state"`
			RetrySafe   bool   `json:"retry_safe"`
			Message     string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(out), &body); err != nil {
		t.Fatalf("worker evidence failure result is not the typed JSON: %v; out=%s", err, out)
	}
	if body.OK {
		t.Fatalf("worker evidence result reports ok; out=%s", out)
	}
	return body.Error.Kind, body.Error.EffectState, body.Error.RetrySafe
}

// assertZeroTerminalEffects proves a refusal landed nothing: no terminal
// event for the attempt, no derived workflow failure record, the attempt
// still dispatched, and the assertion nonce unconsumed.
func assertZeroTerminalEffects(t *testing.T, dbPath, nonce string) {
	t.Helper()
	const attemptID = "attempt-1"
	s, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var terminals, derived, nonces int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_type='work_item' AND subject_id='work-1' AND kind IN (?,?) AND json_extract(payload,'$.attempt_id')=?`, store.WorkerCompleted, store.WorkerFailed, attemptID).Scan(&terminals); err != nil {
		t.Fatal(err)
	}
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_type='work_item' AND subject_id='work-1' AND kind=? AND json_extract(payload,'$.action_id')='record_worker_failure' AND json_extract(payload,'$.worker_attempt_id')=?`, store.WorkflowActionCompleted, attemptID).Scan(&derived); err != nil {
		t.Fatal(err)
	}
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM agent_nonce_replay WHERE nonce=?`, nonce).Scan(&nonces); err != nil {
		t.Fatal(err)
	}
	var state string
	if err := s.DatabaseForTesting().QueryRow(`SELECT lifecycle_state FROM worker_attempts WHERE attempt_id=?`, attemptID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if terminals != 0 || derived != 0 || nonces != 0 || state != "dispatched" {
		t.Fatalf("refusal still had effects: terminals=%d derived=%d nonces=%d attempt_state=%s", terminals, derived, nonces, state)
	}
}

// completeImplementationAttempt seeds an implementation-lane attempt whose
// window the fixture worktree backs, and returns the completion request, its
// unsigned assertion identity, and the signing key.
func completeImplementationAttempt(t *testing.T, dbPath, nonce string) (map[string]any, agent.WorkerEvidenceAssertion, ed25519.PrivateKey) {
	t.Helper()
	key := seedWorkerEvidenceClient(t)
	lane := implementCLILane(t)
	readback := preferredLaneModel(lane)
	seedWorkerEvidenceAttempt(t, key, lane, dbPath, readback)
	request, assertion := workerEvidenceRequest(t, agent.WorkerEvidenceVerbComplete, lane, readback, nonce)
	return request, assertion, key
}

// runCompleteJSON signs and runs one worker-complete request, returning the
// exit code with both output buffers.
func runCompleteJSON(t *testing.T, request map[string]any, assertion agent.WorkerEvidenceAssertion, key ed25519.PrivateKey) (int, string, string) {
	t.Helper()
	request["assertion"] = signWorkerEvidence(t, key, assertion)
	var out, errOut bytes.Buffer
	code := runWithInput([]string{"worker-complete"}, strings.NewReader(mustJSON(t, request)), &out, &errOut)
	return code, out.String(), errOut.String()
}

// rawWorkerPayloadJSON reads one stored worker event's payload bytes.
func rawWorkerPayloadJSON(t *testing.T, dbPath, eventID string) (int, string) {
	t.Helper()
	version, payload, _ := storedWorkerEventRow(t, dbPath, eventID)
	return version, string(payload)
}

// payloadField reads one raw payload member's JSON bytes and reports whether
// the member exists at all.
func payloadField(t *testing.T, payload, field string) (string, bool) {
	t.Helper()
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal([]byte(payload), &decoded); err != nil {
		t.Fatalf("stored payload is not an object: %v", err)
	}
	raw, ok := decoded[field]
	return string(raw), ok
}

// independentWriteCommits proves the database accepts a write from an
// independent connection with a short busy timeout: a SQL transaction that
// spanned the git subprocess would lock the writer out.
func independentWriteCommits(t *testing.T, dbPath string) bool {
	t.Helper()
	db, err := sql.Open("sqlite", store.SQLiteFileURI(dbPath)+"?_pragma=busy_timeout(500)")
	if err != nil {
		t.Fatalf("open independent writer: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS con887_probe (id INTEGER PRIMARY KEY, note TEXT)`); err != nil {
		return false
	}
	_, err = db.Exec(`INSERT INTO con887_probe(note) VALUES('probe')`)
	return err == nil
}

func TestWorkerTerminalObservedDelivery(t *testing.T) {
	t.Run("records the core clean observation at the attempt worktree", func(t *testing.T) {
		dbPath := freshMigratedCLIDatabase(t)
		request, assertion, key := completeImplementationAttempt(t, dbPath, "nonce-observe-clean001")
		worktree := cliFixtureWorktree(t, dbPath)
		if err := os.WriteFile(filepath.Join(worktree, "delivered.txt"), []byte("delivered change\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		head := commitWorktreeFixtureTree(t, worktree, "deliver the change")
		request["event_id"] = "complete-observe-clean"
		code, out, stderr := runCompleteJSON(t, request, assertion, key)
		if code != 0 {
			t.Fatalf("clean implementation completion exit=%d stderr=%s out=%s", code, stderr, out)
		}
		_, payload := rawWorkerPayloadJSON(t, dbPath, "complete-observe-clean")
		deliveryRaw, present := payloadField(t, payload, "observed_delivery")
		if !present {
			t.Fatalf("the completion recorded no observed_delivery; the core must observe the attempt worktree itself: %s", payload)
		}
		var delivery struct {
			Commit string `json:"commit"`
			State  string `json:"state"`
		}
		if err := json.Unmarshal([]byte(deliveryRaw), &delivery); err != nil {
			t.Fatalf("observed_delivery is not the closed shape: %v", err)
		}
		if delivery.State != "clean" {
			t.Fatalf("observed_delivery.state = %q, want clean", delivery.State)
		}
		if head != "" && delivery.Commit != head {
			t.Fatalf("observed_delivery.commit = %q, want the core-observed HEAD %q", delivery.Commit, head)
		}
		if len(delivery.Commit) != 40 {
			t.Fatalf("observed_delivery.commit = %q, want one raw commit OID", delivery.Commit)
		}
	})

	t.Run("records dirty for an uncommitted tracked change", func(t *testing.T) {
		dbPath := freshMigratedCLIDatabase(t)
		request, assertion, key := completeImplementationAttempt(t, dbPath, "nonce-observe-dirty01")
		worktree := cliFixtureWorktree(t, dbPath)
		commitWorktreeFixtureTree(t, worktree, "anchor head")
		if err := os.WriteFile(filepath.Join(worktree, "seed.txt"), []byte("uncommitted\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		request["event_id"] = "complete-observe-dirty"
		code, out, stderr := runCompleteJSON(t, request, assertion, key)
		if code != 0 {
			t.Fatalf("dirty implementation completion exit=%d stderr=%s out=%s", code, stderr, out)
		}
		_, payload := rawWorkerPayloadJSON(t, dbPath, "complete-observe-dirty")
		deliveryRaw, present := payloadField(t, payload, "observed_delivery")
		if !present {
			t.Fatalf("the completion over a dirty index recorded no observed_delivery: %s", payload)
		}
		if !strings.Contains(deliveryRaw, `"state":"dirty"`) {
			t.Fatalf("observed_delivery = %s, want the dirty state", deliveryRaw)
		}
	})

	t.Run("records dirty for untracked content", func(t *testing.T) {
		dbPath := freshMigratedCLIDatabase(t)
		request, assertion, key := completeImplementationAttempt(t, dbPath, "nonce-observe-untrack1")
		worktree := cliFixtureWorktree(t, dbPath)
		commitWorktreeFixtureTree(t, worktree, "anchor head")
		if err := os.WriteFile(filepath.Join(worktree, "untracked.txt"), []byte("loose bytes\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		request["event_id"] = "complete-observe-untracked"
		code, out, stderr := runCompleteJSON(t, request, assertion, key)
		if code != 0 {
			t.Fatalf("untracked-content completion exit=%d stderr=%s out=%s", code, stderr, out)
		}
		_, payload := rawWorkerPayloadJSON(t, dbPath, "complete-observe-untracked")
		deliveryRaw, present := payloadField(t, payload, "observed_delivery")
		if !present {
			t.Fatalf("the completion over untracked content recorded no observed_delivery: %s", payload)
		}
		if !strings.Contains(deliveryRaw, `"state":"dirty"`) {
			t.Fatalf("observed_delivery = %s, want untracked content to record dirty", deliveryRaw)
		}
	})

	t.Run("a non-implementation lane records no observation", func(t *testing.T) {
		dbPath := freshMigratedCLIDatabase(t)
		key := seedWorkerEvidenceClient(t)
		lane := store.BuiltinLaneDefinitions()[0]
		seedWorkerEvidenceAttempt(t, key, lane, dbPath, preferredLaneModel(lane))
		request, assertion := workerEvidenceRequest(t, agent.WorkerEvidenceVerbComplete, lane, preferredLaneModel(lane), "nonce-observe-research")
		request["event_id"] = "complete-observe-research"
		code, out, stderr := runCompleteJSON(t, request, assertion, key)
		if code != 0 {
			t.Fatalf("research completion exit=%d stderr=%s out=%s", code, stderr, out)
		}
		_, payload := rawWorkerPayloadJSON(t, dbPath, "complete-observe-research")
		if deliveryRaw, present := payloadField(t, payload, "observed_delivery"); present {
			t.Fatalf("a non-implementation completion recorded an observation: %s", deliveryRaw)
		}
	})

	t.Run("a failed terminal never probes git and keeps its frontier", func(t *testing.T) {
		dbPath := freshMigratedCLIDatabase(t)
		key := seedWorkerEvidenceClient(t)
		lane := implementCLILane(t)
		readback := preferredLaneModel(lane)
		seedWorkerEvidenceAttempt(t, key, lane, dbPath, readback)
		if err := os.RemoveAll(cliFixtureWorktree(t, dbPath)); err != nil {
			t.Fatal(err)
		}
		request, assertion := workerEvidenceRequest(t, agent.WorkerEvidenceVerbFail, lane, readback, "nonce-observe-fail001")
		request["event_id"] = "failed-observe-no-probe"
		observation := attributionWorkerReportObservation()
		digest, err := store.WorkerFailureObservationDigest(observation)
		if err != nil {
			t.Fatal(err)
		}
		request["failure_attribution"] = observation
		request["assertion"] = attributionSignedV2Assertion(t, key, assertion.AttemptID, lane, readback, assertion.Nonce, digest, nil)
		var out, errOut bytes.Buffer
		if code := runWithInput([]string{"worker-fail"}, strings.NewReader(mustJSON(t, request)), &out, &errOut); code != 0 {
			t.Fatalf("worker-fail with the worktree gone exit=%d stderr=%s out=%s", code, errOut.String(), out.String())
		}
		_, payload := rawWorkerPayloadJSON(t, dbPath, "failed-observe-no-probe")
		if deliveryRaw, present := payloadField(t, payload, "observed_delivery"); present {
			t.Fatalf("a failed terminal recorded an observed_delivery: %s", deliveryRaw)
		}
		frontierRaw, present := payloadField(t, payload, "consumed_context_frontier")
		if !present {
			t.Fatalf("the failed terminal carries no consumed_context_frontier: %s", payload)
		}
		if strings.TrimSpace(frontierRaw) != "0" {
			t.Fatalf("failed terminal consumed_context_frontier = %s, want the recorded zero", frontierRaw)
		}
	})

	t.Run("a git failure refuses with zero terminal effects", func(t *testing.T) {
		dbPath := freshMigratedCLIDatabase(t)
		request, assertion, key := completeImplementationAttempt(t, dbPath, "nonce-observe-gitfail1")
		restore := hideGitDir(t, cliFixtureWorktree(t, dbPath))
		request["event_id"] = "complete-observe-gitfail"
		code, out, stderr := runCompleteJSON(t, request, assertion, key)
		restore()
		if code == 0 {
			t.Fatalf("a git failure admitted the completion: out=%s stderr=%s", out, stderr)
		}
		kind, effectState, retrySafe := typedWorkerEvidenceError(t, out)
		if kind != "git_unreachable" {
			t.Fatalf("typed failure kind = %q, want git_unreachable; out=%s", kind, out)
		}
		if effectState != "none" || !retrySafe {
			t.Fatalf("typed failure effect_state=%q retry_safe=%v, want none and true", effectState, retrySafe)
		}
		assertZeroTerminalEffects(t, dbPath, "nonce-observe-gitfail1")
	})

	t.Run("a probe deadline refuses with zero terminal effects", func(t *testing.T) {
		dbPath := freshMigratedCLIDatabase(t)
		request, assertion, key := completeImplementationAttempt(t, dbPath, "nonce-observe-slow001")
		t.Setenv("PATH", writeSlowObservationGit(t)+string(os.PathListSeparator)+os.Getenv("PATH"))
		request["event_id"] = "complete-observe-slow"
		code, out, stderr := runCompleteJSON(t, request, assertion, key)
		if code == 0 {
			t.Fatalf("an observation deadline admitted the completion: out=%s stderr=%s", out, stderr)
		}
		kind, effectState, _ := typedWorkerEvidenceError(t, out)
		if kind != "timeout" {
			t.Fatalf("typed failure kind = %q, want timeout; out=%s", kind, out)
		}
		if effectState != "none" {
			t.Fatalf("typed failure effect_state = %q, want none", effectState)
		}
		assertZeroTerminalEffects(t, dbPath, "nonce-observe-slow001")
	})

	t.Run("no sql connection is held while git runs", func(t *testing.T) {
		dbPath := freshMigratedCLIDatabase(t)
		request, assertion, key := completeImplementationAttempt(t, dbPath, "nonce-observe-holdg001")
		worktree := cliFixtureWorktree(t, dbPath)
		commitWorktreeFixtureTree(t, worktree, "anchor head")
		entry := filepath.Join(t.TempDir(), "git-entered")
		release := filepath.Join(t.TempDir(), "git-release")
		t.Setenv("PATH", writeBlockingObservationGit(t, entry, release)+string(os.PathListSeparator)+os.Getenv("PATH"))
		request["event_id"] = "complete-observe-hold"
		codeOut := make(chan int, 1)
		var outBuf, errBuf bytes.Buffer
		request["assertion"] = signWorkerEvidence(t, key, assertion)
		requestJSON := mustJSON(t, request)
		go func() {
			var out, errOut bytes.Buffer
			codeOut <- runWithInput([]string{"worker-complete"}, strings.NewReader(requestJSON), &out, &errOut)
			outBuf.WriteString(out.String())
			errBuf.WriteString(errOut.String())
		}()
		started := false
		for range 50 {
			if _, err := os.Stat(entry); err == nil {
				started = true
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		if !started {
			t.Fatal("the core never started a git observation while the ingest ran; the delivery probe is missing")
		}
		if !independentWriteCommits(t, dbPath) {
			if err := os.WriteFile(release, []byte("go\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			<-codeOut
			t.Fatal("an independent write could not commit while the git observation ran; a SQL connection spans the subprocess")
		}
		if err := os.WriteFile(release, []byte("go\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if code := <-codeOut; code == 0 {
			t.Fatalf("the blocked observation admitted the completion: out=%s", outBuf.String())
		}
		assertZeroTerminalEffects(t, dbPath, "nonce-observe-holdg001")
	})

	t.Run("a claim drift between probe and append refuses", func(t *testing.T) {
		dbPath := freshMigratedCLIDatabase(t)
		request, assertion, key := completeImplementationAttempt(t, dbPath, "nonce-observe-driftc1")
		commitWorktreeFixtureTree(t, cliFixtureWorktree(t, dbPath), "anchor head")
		driftSQL := fmt.Sprintf(`UPDATE worktree_entries SET claim_op_id='drifted-claim' WHERE set_id='%s';`, store.WorktreeSetID("work-1"))
		t.Setenv("PATH", writeDriftGit(t, dbPath, driftSQL)+string(os.PathListSeparator)+os.Getenv("PATH"))
		request["event_id"] = "complete-observe-drift-claim"
		code, out, stderr := runCompleteJSON(t, request, assertion, key)
		if code == 0 {
			t.Fatalf("a claim drift after the probe admitted the completion: out=%s stderr=%s", out, stderr)
		}
		assertZeroTerminalEffects(t, dbPath, "nonce-observe-driftc1")
	})

	t.Run("a window drift between probe and append refuses", func(t *testing.T) {
		dbPath := freshMigratedCLIDatabase(t)
		request, assertion, key := completeImplementationAttempt(t, dbPath, "nonce-observe-driftw1")
		commitWorktreeFixtureTree(t, cliFixtureWorktree(t, dbPath), "anchor head")
		driftSQL := `UPDATE domain_events SET payload=json_set(payload,'$.worker_context_frontier',999) WHERE event_id='dispatch-completed-attempt-1';`
		t.Setenv("PATH", writeDriftGit(t, dbPath, driftSQL)+string(os.PathListSeparator)+os.Getenv("PATH"))
		request["event_id"] = "complete-observe-drift-window"
		code, out, stderr := runCompleteJSON(t, request, assertion, key)
		if code == 0 {
			t.Fatalf("a window drift after the probe admitted the completion: out=%s stderr=%s", out, stderr)
		}
		assertZeroTerminalEffects(t, dbPath, "nonce-observe-driftw1")
	})

	t.Run("a forged echo never replaces the core observation", func(t *testing.T) {
		dbPath := freshMigratedCLIDatabase(t)
		request, assertion, key := completeImplementationAttempt(t, dbPath, "nonce-observe-forge01")
		head := commitWorktreeFixtureTree(t, cliFixtureWorktree(t, dbPath), "anchor head")
		request["event_id"] = "complete-observe-forge"
		request["observed_delivery"] = map[string]any{"commit": strings.Repeat("a", 40), "state": "clean"}
		request["consumed_context_frontier"] = 999
		code, _, _ := runCompleteJSON(t, request, assertion, key)
		if code == 0 {
			t.Fatalf("the closed request schema admitted core-owned identity fields: %s", request["event_id"])
		}
		if head != "" {
			s, err := store.Open(context.Background(), dbPath)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			var recorded int
			if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE event_id='complete-observe-forge'`).Scan(&recorded); err != nil {
				t.Fatal(err)
			}
			if recorded != 0 {
				t.Fatal("the forged request still recorded an event")
			}
		}
	})
}

// TestWorkerTerminalObservationReplay proves the acknowledgment boundary: an
// exact repeat of a recorded completion returns the original observation
// without any Git lookup, later HEAD movement never rewrites recorded
// history, and changed caller content still refuses.
func TestWorkerTerminalObservationReplay(t *testing.T) {
	record := func(t *testing.T, dbPath string) (map[string]any, agent.WorkerEvidenceAssertion, ed25519.PrivateKey, store.LaneDefinition, string) {
		t.Helper()
		request, assertion, key := completeImplementationAttempt(t, dbPath, "nonce-replay-record001")
		worktree := cliFixtureWorktree(t, dbPath)
		if err := os.WriteFile(filepath.Join(worktree, "delivered.txt"), []byte("delivered\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		head := commitWorktreeFixtureTree(t, worktree, "deliver the change")
		request["event_id"] = "complete-replay"
		code, out, stderr := runCompleteJSON(t, request, assertion, key)
		if code != 0 {
			t.Fatalf("initial completion exit=%d stderr=%s out=%s", code, stderr, out)
		}
		lane := implementCLILane(t)
		return request, assertion, key, lane, head
	}

	t.Run("an exact acknowledgment retains the original observation without git", func(t *testing.T) {
		dbPath := freshMigratedCLIDatabase(t)
		request, assertion, key, _, head := record(t, dbPath)
		versionBefore, payloadBefore := rawWorkerPayloadJSON(t, dbPath, "complete-replay")
		if !strings.Contains(payloadBefore, `"commit":"`+head) {
			t.Fatalf("recorded observation lost the observed head: %s", payloadBefore)
		}
		worktree := cliFixtureWorktree(t, dbPath)
		// Move HEAD forward, then remove the repository entirely: the
		// acknowledgment must neither re-observe nor fail on Git.
		runGitCLISilent(t, worktree, "-c", "user.email=concord@example.invalid", "-c", "user.name=Concord Item4 Test", "commit", "--allow-empty", "-m", "move head after the record")
		movedHead := runGitCLI(t, worktree, "rev-parse", "--verify", "HEAD^{commit}")
		if movedHead == head {
			t.Fatal("fixture head did not move")
		}
		if err := os.RemoveAll(filepath.Join(worktree, ".git")); err != nil {
			t.Fatal(err)
		}
		assertion.Nonce = "nonce-replay-ack000001"
		code, out, stderr := runCompleteJSON(t, request, assertion, key)
		if code != 0 {
			t.Fatalf("exact acknowledgment with the repository gone exit=%d stderr=%s out=%s", code, stderr, out)
		}
		versionAfter, payloadAfter := rawWorkerPayloadJSON(t, dbPath, "complete-replay")
		if versionAfter != versionBefore || payloadAfter != payloadBefore {
			t.Fatalf("the acknowledgment rewrote the recorded completion:\n%s\nwant\n%s", payloadAfter, payloadBefore)
		}
		if !strings.Contains(payloadAfter, `"commit":"`+head) {
			t.Fatalf("the acknowledgment replaced the original observation: %s", payloadAfter)
		}
		s, err := store.Open(context.Background(), dbPath)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		var completions int
		if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_type='work_item' AND subject_id='work-1' AND kind=? AND json_extract(payload,'$.attempt_id')='attempt-1'`, store.WorkerCompleted).Scan(&completions); err != nil {
			t.Fatal(err)
		}
		if completions != 1 {
			t.Fatalf("the acknowledgment left %d completion events, want exactly the original one", completions)
		}
	})

	t.Run("changed caller content still refuses", func(t *testing.T) {
		dbPath := freshMigratedCLIDatabase(t)
		request, assertion, key, lane, _ := record(t, dbPath)
		request["evidence"] = []map[string]any{{"obligation": lane.EvidenceObligations[0], "detail": "changed result"}}
		assertion.Nonce = "nonce-replay-changed01"
		code, _, stderr := runCompleteJSON(t, request, assertion, key)
		if code == 0 || !strings.Contains(stderr, "identity conflicts") {
			t.Fatalf("changed replay content exit=%d stderr=%s, want the identity conflict", code, stderr)
		}
	})
}

// A transient Git observation refusal must stay recoverable: the existing
// recovery context re-arms the retained report and the same completion route
// records it once Git answers, while worker-abandon closes an attempt whose
// worktree is gone.
func TestWorkerEvidenceRecoveryAfterObservationRefusal(t *testing.T) {
	t.Run("worker_reconcile re-records the retained report through the same owner", func(t *testing.T) {
		dbPath := freshMigratedCLIDatabase(t)
		request, assertion, key := completeImplementationAttempt(t, dbPath, "nonce-recover-refused1")
		worktree := cliFixtureWorktree(t, dbPath)
		if err := os.WriteFile(filepath.Join(worktree, "delivered.txt"), []byte("delivered\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		head := commitWorktreeFixtureTree(t, worktree, "deliver the change")
		restore := hideGitDir(t, worktree)
		request["event_id"] = "complete-recover"
		code, out, _ := runCompleteJSON(t, request, assertion, key)
		restore()
		if code == 0 {
			t.Fatalf("the transient git failure did not refuse the completion: out=%s", out)
		}
		assertZeroTerminalEffects(t, dbPath, "nonce-recover-refused1")

		// The existing recovery route reads the original attempt identity
		// for the retained report.
		s, err := store.Open(context.Background(), dbPath)
		if err != nil {
			t.Fatal(err)
		}
		actor := "client:" + workerEvidenceClientRef + ":operator-1"
		var recovery store.WorkerRecoveryContext
		if err := s.Transact(context.Background(), func(tx *store.Transaction) error {
			var recoverErr error
			recovery, recoverErr = store.WorkerRecoveryContextTx(context.Background(), tx, "work-1", "attempt-1", "project-1", worktree, actor)
			return recoverErr
		}); err != nil {
			t.Fatalf("worker_reconcile recovery context refused after the observation refusal: %v", err)
		}
		s.Close()
		if recovery.LifecycleState != "dispatched" || recovery.DispatchEventID == "" {
			t.Fatalf("recovery context = %+v, want the dispatched original attempt", recovery)
		}

		// Git answers again: the same retained report records through the
		// same completion route, with the core observation attached.
		code, out, stderr := runCompleteJSON(t, request, assertion, key)
		if code != 0 {
			t.Fatalf("the retained report did not record once git answered: exit=%d stderr=%s out=%s", code, stderr, out)
		}
		_, payload := rawWorkerPayloadJSON(t, dbPath, "complete-recover")
		deliveryRaw, present := payloadField(t, payload, "observed_delivery")
		if !present {
			t.Fatalf("the recovered completion recorded no observation: %s", payload)
		}
		if head != "" && !strings.Contains(deliveryRaw, `"commit":"`+head+`"`) {
			t.Fatalf("recovered observation = %s, want the core-observed head %q", deliveryRaw, head)
		}
		if !strings.Contains(deliveryRaw, `"state":"clean"`) {
			t.Fatalf("recovered observation = %s, want clean", deliveryRaw)
		}
	})

	t.Run("worker_abandon still closes the attempt when the worktree is gone", func(t *testing.T) {
		dbPath := freshMigratedCLIDatabase(t)
		key := seedWorkerEvidenceClient(t)
		lane := implementCLILane(t)
		readback := preferredLaneModel(lane)
		seedWorkerEvidenceAttempt(t, key, lane, dbPath, readback)
		if err := os.RemoveAll(cliFixtureWorktree(t, dbPath)); err != nil {
			t.Fatal(err)
		}
		request, assertion := workerEvidenceRequest(t, agent.WorkerEvidenceVerbFail, lane, readback, "nonce-recover-abandon1")
		delete(request, "readback_model")
		delete(request, "failure_kind")
		assertion.ReadbackModel = ""
		assertion.FailureKind = string(store.WorkerFailureAbandoned)
		request["event_id"] = "failed-recover-abandon"
		observation := attributionHostTerminalObservation("the coordinator asked to abandon this attempt")
		observation.Cause, observation.Basis = store.WorkerFailureCauseUnknown, store.WorkerFailureBasisUnknown
		observation.Sources[0].Host = &store.WorkerFailureHostObservation{Tag: store.WorkerFailureHostTagAbandonment, EventType: "worker_abandon"}
		digest, err := store.WorkerFailureObservationDigest(observation)
		if err != nil {
			t.Fatal(err)
		}
		request["failure_attribution"] = observation
		assertion.Version, assertion.ObservationDigest = agent.WorkerEvidenceAssertionVersion2, digest
		request["assertion"] = signWorkerEvidence(t, key, assertion)
		var out, errOut bytes.Buffer
		if code := runWithInput([]string{"worker-abandon"}, strings.NewReader(mustJSON(t, request)), &out, &errOut); code != 0 {
			t.Fatalf("worker-abandon with the worktree gone exit=%d stderr=%s out=%s", code, errOut.String(), out.String())
		}
		_, payload := rawWorkerPayloadJSON(t, dbPath, "failed-recover-abandon")
		if deliveryRaw, present := payloadField(t, payload, "observed_delivery"); present {
			t.Fatalf("the abandonment recorded an observation: %s", deliveryRaw)
		}
		s, err := store.Open(context.Background(), dbPath)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		var state string
		if err := s.DatabaseForTesting().QueryRow(`SELECT lifecycle_state FROM worker_attempts WHERE attempt_id='attempt-1'`).Scan(&state); err != nil {
			t.Fatal(err)
		}
		if state != "failed" {
			t.Fatalf("abandoned attempt state = %q, want failed", state)
		}
	})
}

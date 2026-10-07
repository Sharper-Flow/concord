package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestCiWaitTerminalErrorRemovesState(t *testing.T) {
	for _, script := range []string{`echo 'gh: auth failure' >&2; exit 1`, `echo 'not json'`} {
		t.Run(script, func(t *testing.T) {
			ghStubPath(t, ghStubDir(t, script))
			stateFile := filepath.Join(t.TempDir(), "ci-wait-test.json")
			code, report := runCiWaitStdin(t, ciWaitJSON(t, ciWaitRequest{
				Selector: &ciWaitSelector{Kind: "run", Value: "1"}, Repo: "o/r", StateFile: stateFile,
			}))
			if code != 1 || report.Status != "error" {
				t.Fatalf("want error, got code=%d report=%+v", code, report)
			}
			if _, err := os.Stat(stateFile); !os.IsNotExist(err) {
				t.Fatal("terminal error left its state file")
			}
			if report.StateFile != "" {
				t.Fatal("terminal error must not advertise resumable state")
			}
		})
	}
}

func TestCiWaitReapsAbandonedPendingAndDeadOwnerState(t *testing.T) {
	ghStubPath(t, ghStubDir(t, `echo '{"status":"in_progress"}'`))
	dir := filepath.Join(os.Getenv("XDG_STATE_HOME"), "concord")
	_, pending := runCiWaitStdin(t, ciWaitJSON(t, ciWaitRequest{
		Selector: &ciWaitSelector{Kind: "run", Value: "1"}, Repo: "o/r", TimeSecondsMax: ciWaitBudget(8),
	}))
	if pending.Status != "pending" {
		t.Fatalf("want a resumable pending slice, got %+v", pending)
	}
	state, ok := ciWaitReadState(pending.StateFile)
	if !ok {
		t.Fatal("pending slice lost its state")
	}
	deadline := state.DeadlineAt
	_, resumed, err := ciWaitLoadOrCreate(ciWaitRequest{StateFile: pending.StateFile})
	if err != nil || resumed != pending.StateFile {
		t.Fatalf("pending state cannot resume: %v", err)
	}
	state, _ = ciWaitReadState(resumed)
	if !state.DeadlineAt.Equal(deadline) {
		t.Fatal("resume reset the deadline")
	}

	// A real reaped process supplies an owner PID that is no longer live.
	child := exec.Command("sh", "-c", "kill -KILL $$")
	if exit, ok := child.Run().(*exec.ExitError); !ok || exit.Sys().(syscall.WaitStatus).Signal() != syscall.SIGKILL {
		t.Fatalf("owner did not die through SIGKILL: %v", exit)
	}
	deadPID := child.Process.Pid
	for _, tc := range []struct {
		name    string
		owner   int
		expired bool
		remove  bool
	}{
		{"ci-wait-pending.json", deadPID, true, true},
		{"ci-wait-watch-dead.json", deadPID, true, true},
		{"ci-wait-watch-live.json", os.Getpid(), true, false},
		{"ci-wait-watch-resumable.json", deadPID, false, false},
		{"ci-wait-watch-unknown.json", 0, true, false},
		{fmt.Sprintf("ci-wait-%d.json", deadPID), 0, true, true},
		{"unrelated.json", deadPID, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			at := time.Now().Add(time.Hour)
			if tc.expired {
				at = time.Now().Add(-48 * time.Hour)
			}
			file := filepath.Join(dir, tc.name)
			body := map[string]any{"schema_version": 1, "deadline_at": at, "owner_pid": tc.owner}
			if err := os.WriteFile(file, []byte(ciWaitJSON(t, body)), 0o600); err != nil {
				t.Fatal(err)
			}
			_, _, err := ciWaitLoadOrCreate(ciWaitRequest{
				Selector: &ciWaitSelector{Kind: "run", Value: "1"}, Repo: "o/r",
			})
			if err != nil {
				t.Fatal(err)
			}
			_, err = os.Stat(file)
			if os.IsNotExist(err) != tc.remove {
				t.Fatalf("remove=%v, stat=%v", tc.remove, err)
			}
		})
	}
}

func TestCiWaitReaperPreservesUnknownFilesAndSelectedDeadline(t *testing.T) {
	ghStubPath(t, ghStubDir(t, "exit 97"))
	dir := filepath.Join(os.Getenv("XDG_STATE_HOME"), "concord")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	malformed := filepath.Join(dir, "ci-wait-malformed.json")
	if err := os.WriteFile(malformed, []byte("not JSON"), 0o600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "target.json")
	ciWaitSaveState(&ciWaitState{SchemaVersion: 1, DeadlineAt: time.Now().Add(-time.Hour), OwnerPID: 2147483647}, target)
	symlink := filepath.Join(dir, "ci-wait-symlink.json")
	if err := os.Symlink(target, symlink); err != nil {
		t.Fatal(err)
	}
	selected := filepath.Join(dir, "ci-wait-selected.json")
	deadline := time.Now().Add(-48 * time.Hour)
	ciWaitSaveState(&ciWaitState{SchemaVersion: 1, DeadlineAt: deadline, OwnerPID: 2147483647}, selected)
	state, _, err := ciWaitLoadOrCreate(ciWaitRequest{StateFile: selected})
	if err != nil || !state.DeadlineAt.Equal(deadline) {
		t.Fatalf("selected expired wait must keep its deadline for a timeout report: %v", err)
	}
	for _, file := range []string{malformed, symlink, target, selected} {
		if _, err := os.Lstat(file); err != nil {
			t.Fatalf("sweep removed protected file %s: %v", filepath.Base(file), err)
		}
	}
}

func TestCiWaitKilledInvocationState(t *testing.T) {
	if os.Getenv("CI_WAIT_KILL_HELPER") == "1" {
		var out, errOut bytes.Buffer
		os.Exit(runCiWait([]byte(os.Getenv("CI_WAIT_KILL_REQUEST")), &out, &errOut))
	}
	ghStubPath(t, ghStubDir(t, `kill -KILL "$PPID"`))
	file := filepath.Join(os.Getenv("XDG_STATE_HOME"), "concord", "ci-wait-watch-killed.json")
	request := ciWaitJSON(t, ciWaitRequest{Selector: &ciWaitSelector{Kind: "run", Value: "1"}, Repo: "o/r", StateFile: file})
	child := exec.Command(os.Args[0], "-test.run=^TestCiWaitKilledInvocationState$")
	child.Env = append(os.Environ(), "CI_WAIT_KILL_HELPER=1", "CI_WAIT_KILL_REQUEST="+request)
	err := child.Run()
	if exit, ok := err.(*exec.ExitError); !ok || exit.Sys().(syscall.WaitStatus).Signal() != syscall.SIGKILL {
		t.Fatalf("want SIGKILL, got %v", err)
	}
	state, ok := ciWaitReadState(file)
	if !ok {
		t.Fatal("killed slice did not persist its original deadline before polling")
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	if fields["owner_pid"] != float64(os.Getpid()) {
		t.Fatal("slice owner must be the caller that can resume across child exits")
	}
	state.DeadlineAt = time.Now().Add(-time.Minute)
	ciWaitSaveState(state, file)
	// Even an expired killed slice stays while its caller is live.
	_, _, err = ciWaitLoadOrCreate(ciWaitRequest{Selector: &ciWaitSelector{Kind: "run", Value: "2"}, Repo: "o/r"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(file); err != nil {
		t.Fatal("sweep removed a live caller's state")
	}
	_, report := runCiWaitStdin(t, ciWaitJSON(t, ciWaitRequest{StateFile: file}))
	if report.Status != "timeout" {
		t.Fatalf("expired resume must time out, got %+v", report)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatal("timeout left the killed slice's state")
	}
}

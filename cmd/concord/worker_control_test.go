package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/agent"
	"github.com/sharper-flow/concord/internal/store"
)

func workerControlCLIInput() map[string]any {
	return map[string]any{
		"work_id": "work-control", "project_id": "project-control", "attempt_id": "attempt-control",
		"worktree_identity": "sha256:" + strings.Repeat("a", 64),
		"packet":            map[string]any{"work_id": "work-control", "attempt_id": "attempt-control"},
		"control_id":        "control:fixture", "assertion": map[string]any{},
	}
}

func TestWorkerControlCLICarriesHostProofAndRecordedSelector(t *testing.T) {
	input := workerControlCLIInput()
	input["assertion"] = map[string]any{"agent_ref": "actual-child", "session_ref": "actual-session", "nonce": "host-selected-nonce"}
	raw, _ := json.Marshal(input)
	var out, errOut bytes.Buffer
	code := runWorkerControl(context.Background(), raw, func(_ context.Context, req store.WorkerControlRequest, proof agent.WorkerControlAssertion) (store.WorktreeVerifyResult, error) {
		if req.WorkID != "work-control" || req.ProjectID != "project-control" || req.ControlID != "control:fixture" || proof.AgentRef != "actual-child" || proof.SessionRef != "actual-session" || proof.Nonce != "host-selected-nonce" || !json.Valid(req.Packet) {
			t.Fatalf("private control changed its bound request: %+v %+v", req, proof)
		}
		return store.WorktreeVerifyResult{WorkID: req.WorkID, OperationRef: "native-retained-result"}, nil
	}, &out, &errOut)
	if code != 0 || errOut.Len() != 0 || !strings.Contains(out.String(), "native-retained-result") {
		t.Fatalf("native producer result was not forwarded: code=%d out=%s error=%s", code, out.String(), errOut.String())
	}
}

func TestWorkerControlCLIRejectsCallerAuthoredExecution(t *testing.T) {
	for _, key := range []string{"command", "argv", "cwd", "runner", "oracle", "owner", "principal_ref", "model", "lease_id", "NativeAuthorization"} {
		t.Run(key, func(t *testing.T) {
			input := workerControlCLIInput()
			input[key] = "not a recorded selector"
			raw, _ := json.Marshal(input)
			var out, errOut bytes.Buffer
			called := false
			code := runWorkerControl(context.Background(), raw, func(context.Context, store.WorkerControlRequest, agent.WorkerControlAssertion) (store.WorktreeVerifyResult, error) {
				called = true
				return store.WorktreeVerifyResult{}, nil
			}, &out, &errOut)
			if code != inputRefusalExit || called || out.Len() != 0 || !strings.Contains(errOut.String(), "unknown field") {
				t.Fatalf("caller-authored execution reached the producer: code=%d called=%v out=%s error=%s", code, called, out.String(), errOut.String())
			}
		})
	}
}

func TestWorkerControlCLIPreservesCancellationAndRefusesPartialResults(t *testing.T) {
	raw, _ := json.Marshal(workerControlCLIInput())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var out, errOut bytes.Buffer
	code := runWorkerControl(ctx, raw, func(received context.Context, _ store.WorkerControlRequest, _ agent.WorkerControlAssertion) (store.WorktreeVerifyResult, error) {
		if !errors.Is(received.Err(), context.Canceled) {
			t.Fatal("native execution lost caller cancellation")
		}
		return store.WorktreeVerifyResult{OperationRef: "must-not-return"}, received.Err()
	}, &out, &errOut)
	if code != 1 || out.Len() != 0 || !strings.Contains(errOut.String(), "context canceled") {
		t.Fatalf("refused producer result escaped: code=%d out=%s error=%s", code, out.String(), errOut.String())
	}
}

func TestWorkerControlCLIEntryRoutesOnlyToAuthenticatedExecution(t *testing.T) {
	t.Setenv("CONCORD_DB_PATH", filepath.Join(t.TempDir(), "authority.db"))
	raw, _ := json.Marshal(workerControlCLIInput())
	var out, errOut bytes.Buffer
	code := runWithInput([]string{"worker-control"}, bytes.NewReader(raw), &out, &errOut)
	if code != 1 || out.Len() != 0 || !strings.Contains(errOut.String(), "signed directory/worktree is not a git repository") {
		t.Fatalf("private control did not reach and refuse authenticated native ingress: code=%d out=%s error=%s", code, out.String(), errOut.String())
	}
	if _, _, ok := routeCommand([]string{"worker", "control"}); ok {
		t.Fatal("worker control acquired an undeclared alias")
	}
}

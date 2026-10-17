package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

func workerReadCLIInput() map[string]any {
	return map[string]any{
		"work_id": "work-1", "attempt_id": "attempt-1", "worktree_identity": "sha256:" + strings.Repeat("a", 64),
		"packet": map[string]any{"work_id": "work-1", "attempt_id": "attempt-1"}, "ordinal": 0, "offset": 0, "length": 7,
	}
}

func TestWorkerReadCLICarriesOnlyTheBoundReadingSelectors(t *testing.T) {
	input := workerReadCLIInput()
	raw, _ := json.Marshal(input)
	var out, errOut bytes.Buffer
	code := runWorkerRead(raw, func(_ context.Context, request store.WorkerReadingRequest) (store.WorkerReadingPage, error) {
		if request.WorkID != "work-1" || request.AttemptID != "attempt-1" || request.WorktreeIdentity != input["worktree_identity"] || request.Ordinal != 0 || request.Offset != 0 || request.Length != 7 || request.Runner != nil || string(request.Packet) != `{"attempt_id":"attempt-1","work_id":"work-1"}` {
			t.Fatalf("unexpected worker read request: %+v", request)
		}
		return store.WorkerReadingPage{Ordinal: 0, Data: []byte("pinned"), NextOffset: 6, TotalBytes: 6, EOF: true}, nil
	}, &out, &errOut)
	var page store.WorkerReadingPage
	if code != 0 || errOut.Len() != 0 || json.Unmarshal(out.Bytes(), &page) != nil || string(page.Data) != "pinned" || !page.EOF {
		t.Fatalf("worker read result: code=%d out=%s error=%s", code, out.String(), errOut.String())
	}
}

func TestWorkerReadCLIRejectsCallerAuthoredPathsAndExecution(t *testing.T) {
	for _, key := range []string{"path", "ref", "command", "runner"} {
		input := workerReadCLIInput()
		input[key] = "not a source selector"
		raw, _ := json.Marshal(input)
		var out, errOut bytes.Buffer
		called := false
		code := runWorkerRead(raw, func(context.Context, store.WorkerReadingRequest) (store.WorkerReadingPage, error) {
			called = true
			return store.WorkerReadingPage{}, nil
		}, &out, &errOut)
		if code == 0 || called || out.Len() != 0 || !strings.Contains(errOut.String(), "unknown field") {
			t.Fatalf("field %s reached the reader: code=%d called=%v out=%s error=%s", key, code, called, out.String(), errOut.String())
		}
	}
}

func TestWorkerReadCLIRoutesZeroSelectorsButRefusesAnUnboundAttempt(t *testing.T) {
	t.Setenv("CONCORD_DB_PATH", filepath.Join(t.TempDir(), "authority.db"))
	raw, _ := json.Marshal(workerReadCLIInput())
	var out, errOut bytes.Buffer
	code := runWithInput([]string{"worker-read"}, bytes.NewReader(raw), &out, &errOut)
	if code != 1 || out.Len() != 0 || errOut.Len() == 0 {
		t.Fatalf("unbound worker read: code=%d out=%s error=%s", code, out.String(), errOut.String())
	}
	if _, _, ok := routeCommand([]string{"worker", "read"}); ok {
		t.Fatal("worker read acquired an undeclared two-word alias")
	}
}

func TestWorkerReadCLIDoesNotReturnARefusedPage(t *testing.T) {
	raw, _ := json.Marshal(workerReadCLIInput())
	var out, errOut bytes.Buffer
	code := runWorkerRead(raw, func(context.Context, store.WorkerReadingRequest) (store.WorkerReadingPage, error) {
		return store.WorkerReadingPage{Data: []byte("must not return")}, errors.New("attempt retired during read")
	}, &out, &errOut)
	if code != 1 || out.Len() != 0 || !strings.Contains(errOut.String(), "attempt retired during read") {
		t.Fatalf("refused page escaped the CLI: code=%d out=%s error=%s", code, out.String(), errOut.String())
	}
}

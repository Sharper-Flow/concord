package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/store"
)

func resumeCLI(t *testing.T, s *store.Store, directory, workID string) (int, workResumeOutput, string) {
	t.Helper()
	raw, err := json.Marshal(workResumeInput{ProductID: "product-wl", ProjectID: "project-wl", WorkID: workID})
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(directory)
	var out, errOut bytes.Buffer
	code := runWorkResume(raw, s, &out, &errOut)
	var parsed workResumeOutput
	_ = json.Unmarshal(out.Bytes(), &parsed)
	return code, parsed, errOut.String()
}

func transitionWorkItem(t *testing.T, s *store.Store, workID string, from, to string, version int64) {
	t.Helper()
	payload, err := json.Marshal(map[string]any{"from": from, "to": to, "reason": "fixture transition", "expected_version": version, "resulting_version": version + 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ApplyOperation(context.Background(), s, store.Operation{Events: []store.Event{{EventID: "resume-transition-" + to + "-" + workID, Kind: "work.transitioned", SubjectType: store.SubjectWorkItem, SubjectID: workID, Actor: "operator", OccurredAt: time.Unix(10, 0).UTC(), PayloadVersion: 1, Payload: payload}}, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectWorkItem, workID): version}}); err != nil {
		t.Fatal(err)
	}
}

func TestWorkResumeDerivesActiveEntryFromDefaultCheckout(t *testing.T) {
	repo := initLocatorRepo(t)
	s := mustOpenStore(t, filepath.Join(t.TempDir(), "concord.db"))
	seedLocatorAuthority(t, s, repo)
	origin, err := s.BootstrapWorktree(context.Background(), bootstrapRequest(), nil)
	if err != nil {
		t.Fatal(err)
	}
	code, output, stderr := resumeCLI(t, s, repo, origin.WorkID)
	if code != 0 {
		t.Fatalf("resume code=%d stderr=%q", code, stderr)
	}
	if output.SchemaVersion != "1.0" || output.WorkID != origin.WorkID || output.ProductID != "product-wl" || output.ProjectID != "project-wl" {
		t.Fatalf("resume output=%+v", output)
	}
	if output.Worktree.Path != origin.Entry.Path || output.Worktree.State != "active" || output.Worktree.Branch != origin.Entry.Branch {
		t.Fatalf("resume worktree=%+v want entry=%+v", output.Worktree, origin.Entry)
	}
}

func TestWorkResumeRefusesTerminalUnknownAndUnclaimedWork(t *testing.T) {
	repo := initLocatorRepo(t)
	s := mustOpenStore(t, filepath.Join(t.TempDir(), "concord.db"))
	seedLocatorAuthority(t, s, repo)

	if code, _, stderr := resumeCLI(t, s, repo, "work-missing"); code == 0 || !strings.Contains(stderr, "does not exist") {
		t.Fatalf("unknown work code=%d stderr=%q", code, stderr)
	}
	// work-wl exists from the fixture but holds no worktree claim.
	if code, _, stderr := resumeCLI(t, s, repo, "work-wl"); code == 0 || !strings.Contains(stderr, "no active worktree") {
		t.Fatalf("unclaimed work code=%d stderr=%q", code, stderr)
	}

	origin, err := s.BootstrapWorktree(context.Background(), bootstrapRequest(), nil)
	if err != nil {
		t.Fatal(err)
	}
	transitionWorkItem(t, s, origin.WorkID, "needed", "completed", origin.WorkVersion)
	if code, _, stderr := resumeCLI(t, s, repo, origin.WorkID); code == 0 || !strings.Contains(stderr, "terminal work item") {
		t.Fatalf("terminal work code=%d stderr=%q", code, stderr)
	}
}

func TestWorkResumeAppliesTheBootstrapOriginGate(t *testing.T) {
	repo := initLocatorRepo(t)
	s := mustOpenStore(t, filepath.Join(t.TempDir(), "concord.db"))
	seedLocatorAuthority(t, s, repo)
	first, err := s.BootstrapWorktree(context.Background(), func() store.BootstrapRequest {
		request := bootstrapRequest()
		request.IdempotencyKey = "resume-origin-first"
		return request
	}(), nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.BootstrapWorktree(context.Background(), func() store.BootstrapRequest {
		request := bootstrapRequest()
		request.IdempotencyKey = "resume-origin-second"
		return request
	}(), nil)
	if err != nil {
		t.Fatal(err)
	}

	// A live, clean origin worktree is admitted (CD-0110 D1 as amended for
	// issue #896): a session may leave one item's worktree for another.
	code, output, stderr := resumeCLI(t, s, first.Entry.Path, second.WorkID)
	if code != 0 {
		t.Fatalf("live clean origin resume code=%d stderr=%q", code, stderr)
	}
	if output.Worktree.Path != second.Entry.Path {
		t.Fatalf("live origin resume target=%+v", output)
	}

	if err := os.WriteFile(filepath.Join(first.Entry.Path, "dirty.txt"), []byte("keep here\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Remove(filepath.Join(first.Entry.Path, "dirty.txt")) }()
	if code, _, stderr := resumeCLI(t, s, first.Entry.Path, second.WorkID); code == 0 || !strings.Contains(stderr, "dirty worktree") {
		t.Fatalf("dirty origin resume code=%d stderr=%q", code, stderr)
	}
}

func TestSessionPrepareAcceptsEmptyTask(t *testing.T) {
	repo := initLocatorRepo(t)
	dbPath := filepath.Join(t.TempDir(), "concord.db")
	s := mustOpenStore(t, dbPath)
	seedLocatorAuthority(t, s, repo)
	origin, err := s.BootstrapWorktree(context.Background(), bootstrapRequest(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(dbOverrideEnv, dbPath)
	t.Chdir(origin.Entry.Path)
	var out, errOut bytes.Buffer
	code := runSessionPrepare(commandSessionPrepareInput(t, origin.WorkID, ""), s, &out, &errOut,
		func(string) error { return nil },
		func(context.Context, string, string, string) (string, error) { return "agent", nil },
		func(context.Context, string, string, string) ([]byte, error) {
			return []byte(`{"watermark":"test"}`), nil
		})
	if code != 0 {
		t.Fatalf("empty task session-prepare code=%d stderr=%q", code, errOut.String())
	}
	var prepared sessionPrepareOutput
	if err := json.Unmarshal(out.Bytes(), &prepared); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(prepared.Prompt, "Task:") {
		t.Fatalf("empty task prompt carries a task line: %q", prepared.Prompt)
	}
}

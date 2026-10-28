package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

func TestWorkBootstrapExternalRefInputCorrection(t *testing.T) {
	repo := initLocatorRepo(t)
	s := mustOpenStore(t, filepath.Join(t.TempDir(), "concord.db"))
	seedLocatorAuthority(t, s, repo)
	t.Chdir(repo)
	input := workBootstrapInput{
		ProductID: "product-wl", ProjectID: "project-wl", Title: "Bootstrap work",
		ValueStatement: "A worktree is ready", Kind: "task", Task: "run the task",
		IdempotencyKey: "bootstrap-correction", ExternalRef: "tracker:48", RaisedFromWorkID: "work-parent",
	}
	req := bootstrapRequest()
	req.IdempotencyKey, req.ExternalRef, req.RaisedFromWorkID = input.IdempotencyKey, input.ExternalRef, input.RaisedFromWorkID
	_, refusedWorkID, _, err := store.CanonicalBootstrapIdentity(req)
	if err != nil {
		t.Fatal(err)
	}
	location, err := s.LocateWorktree(context.Background(), req.ProjectID, refusedWorkID, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	branches := gitOutput(t, repo, "branch", "--list")
	snapshot := func() [4]int {
		t.Helper()
		var state [4]int
		for i, table := range []string{"work_items", "relations", "bootstrap_operations", "worktree_claims"} {
			if err := s.DatabaseForTesting().QueryRow("SELECT count(*) FROM " + table).Scan(&state[i]); err != nil {
				t.Fatal(err)
			}
		}
		return state
	}
	before := snapshot()
	var out, errOut bytes.Buffer
	run := func() int {
		t.Helper()
		raw, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		out.Reset()
		errOut.Reset()
		return runWorkBootstrap(raw, s, &out, &errOut)
	}
	var refusal workBootstrapFailureOutput
	if code := run(); code != workBootstrapRefusalExit || json.Unmarshal(out.Bytes(), &refusal) != nil || refusal.EffectState != "none" || refusal.WorkID != "" || !strings.Contains(errOut.String(), "recovery: correct_request") || !strings.Contains(errOut.String(), "omit raised_from_work_id") || !strings.Contains(errOut.String(), "same idempotency_key") {
		t.Fatalf("no-owner refusal code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	if after := snapshot(); after != before {
		t.Fatalf("refusal changed work/relation/journal/claim state: %v -> %v", before, after)
	}
	if _, err := os.Stat(location.Path); !os.IsNotExist(err) {
		t.Fatalf("refused capture created native worktree: %v", err)
	}
	if after := gitOutput(t, repo, "branch", "--list"); after != branches {
		t.Fatalf("refused capture created branch: %q -> %q", branches, after)
	}
	input.RaisedFromWorkID = ""
	if code := run(); code != 0 {
		t.Fatalf("corrected same-key bootstrap code=%d stderr=%q", code, errOut.String())
	}
	var captured workBootstrapOutput
	if err := json.Unmarshal(out.Bytes(), &captured); err != nil || captured.WorkID == "" || captured.Worktree.State != "active" {
		t.Fatalf("corrected capture=%+v err=%v", captured, err)
	}
	input.Title = "Different digest"
	if code := run(); code != workBootstrapRefusalExit || !strings.Contains(errOut.String(), "bound to different input") {
		t.Fatalf("committed bootstrap key conflict code=%d stderr=%q", code, errOut.String())
	}
}

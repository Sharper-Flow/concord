package main

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

// TestProjectCanonicalPathAnswersWithoutADefaultRef covers the read the
// adapter's second-session routing needs (CD-0182): a Project with a valid
// canonical path but no resolvable default ref — its registered repository is
// not a git repository at all — still answers, while worktree-locate refuses
// because it also resolves a default branch ref and a commit.
func TestProjectCanonicalPathAnswersWithoutADefaultRef(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "concord.db")
	seedCLIProduct(t, dbPath, "product-bare", "project-bare")
	s, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	// A plain directory: a valid canonical path with no git fact behind it.
	repo := t.TempDir()
	if err := s.AddProjectLocator(ctx, "project-bare", store.ProjectLocator{ID: "loc-bare", Kind: store.LocatorCanonicalPath, Value: repo}, 1); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	normalized, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatal(err)
	}

	t.Setenv(dbOverrideEnv, dbPath)
	var out, errOut bytes.Buffer
	if code := runWithInput([]string{"project-canonical-path"}, strings.NewReader(`{"project_id":"project-bare"}`), &out, &errOut); code != 0 {
		t.Fatalf("exit=%d stderr=%q, want the canonical path", code, errOut.String())
	}
	var answer struct {
		ProjectID     string `json:"project_id"`
		CanonicalPath string `json:"canonical_path"`
	}
	if err := json.Unmarshal(out.Bytes(), &answer); err != nil {
		t.Fatalf("response %q is not JSON: %v", out.String(), err)
	}
	if answer.ProjectID != "project-bare" || answer.CanonicalPath != normalized {
		t.Fatalf("answer=%+v, want project-bare at %s", answer, normalized)
	}

	// The same Project cannot answer the worktree claim derivation: that read
	// needs a resolvable default ref, which is exactly why the routing
	// decision must not go through it.
	var locateOut, locateErr bytes.Buffer
	if code := runWithInput([]string{"worktree-locate"}, strings.NewReader(`{"project_id":"project-bare","work_id":"work-1"}`), &locateOut, &locateErr); code == 0 {
		t.Fatalf("worktree-locate resolved a Project whose repository has no default ref: %s", locateOut.String())
	}
}

// TestProjectCanonicalPathRefusals covers the typed refusals at the verb
// boundary: a missing project_id (the required-field gate), an empty
// project_id (the verb's own check), and a Project with no canonical_path
// locator.
func TestProjectCanonicalPathRefusals(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "concord.db")
	seedCLIProduct(t, dbPath, "product-bare", "project-bare")
	t.Setenv(dbOverrideEnv, dbPath)
	for _, tc := range []struct {
		name     string
		body     string
		wantExit int
		wantErr  string
	}{
		{"missing project_id", `{}`, 64, "missing required field project_id"},
		{"empty project_id", `{"project_id":""}`, 1, "project_id is required"},
		{"unknown project", `{"project_id":"project-unknown"}`, 1, "no canonical_path locator"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			if code := runWithInput([]string{"project-canonical-path"}, strings.NewReader(tc.body), &out, &errOut); code != tc.wantExit {
				t.Fatalf("exit=%d stdout=%q, want %d", code, out.String(), tc.wantExit)
			}
			if !strings.Contains(errOut.String(), tc.wantErr) {
				t.Fatalf("diagnostic=%q, want %q", errOut.String(), tc.wantErr)
			}
		})
	}
}

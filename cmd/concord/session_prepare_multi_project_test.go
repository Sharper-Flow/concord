package main

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/store"
)

// TestSessionPrepareMultiProject proves directory selection: one work item
// with memberships in two Projects and an active worktree in each admits
// session-prepare from each worktree, and refuses from each main checkout
// before any identity callback runs.
func TestSessionPrepareMultiProject(t *testing.T) {
	repoA := initLocatorRepo(t)
	repoB := initLocatorRepo(t)
	dbPath := filepath.Join(t.TempDir(), "concord.db")
	s := mustOpenStore(t, dbPath)
	seedLocatorAuthority(t, s, repoA)
	// Join project-wl-b to product-wl (the seed leaves the Product at
	// version 2) and register repoB as its canonical path.
	if err := store.ApplyOperation(context.Background(), s, store.Operation{Events: []store.Event{
		{EventID: "wl-project-b", Kind: "project.created", SubjectType: store.SubjectProject, SubjectID: "project-wl-b", Actor: "operator", OccurredAt: time.Unix(1, 0).UTC(), PayloadVersion: 1, Payload: json.RawMessage(`{"display_name":"wl-b"}`)},
		{EventID: "wl-membership-b", Kind: "product_project.added", SubjectType: store.SubjectProduct, SubjectID: "product-wl", Actor: "operator", OccurredAt: time.Unix(2, 0).UTC(), PayloadVersion: 1, Payload: json.RawMessage(`{"product_id":"product-wl","project_id":"project-wl-b","role":"secondary","reason":"fixture","expected_version":2,"resulting_version":3}`)},
	}, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectProduct, "product-wl"): 2, store.VersionRef(store.SubjectProject, "project-wl-b"): 0}}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddProjectLocator(context.Background(), "project-wl-b", store.ProjectLocator{ID: "path-wl-b", Kind: store.LocatorCanonicalPath, Value: repoB}, 1); err != nil {
		t.Fatal(err)
	}
	result, err := s.BootstrapWorktree(context.Background(), bootstrapRequest(), nil)
	if err != nil {
		t.Fatal(err)
	}
	// Hold memberships in both Projects, then claim the second Project's
	// worktree through the ordinary per-Project resume claim.
	memberships, err := json.Marshal(map[string]any{
		"memberships": []map[string]string{
			{"project_id": "project-wl", "role": "primary"},
			{"project_id": "project-wl-b", "role": "secondary"},
		},
		"expected_version":  result.WorkVersion,
		"resulting_version": result.WorkVersion + 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ApplyOperation(context.Background(), s, store.Operation{Events: []store.Event{{
		EventID: "wl-memberships-both", Kind: "work.memberships_replaced", SubjectType: store.SubjectWorkItem, SubjectID: result.WorkID,
		Actor: "operator", OccurredAt: time.Unix(3, 0).UTC(), PayloadVersion: 1, Payload: memberships,
	}}, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectWorkItem, result.WorkID): result.WorkVersion}}); err != nil {
		t.Fatal(err)
	}
	second, err := s.BootstrapExistingWorktree(context.Background(), store.ExistingBootstrapRequest{ProductID: "product-wl", ProjectID: "project-wl-b", WorkID: result.WorkID, SessionRef: "ses-multi"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := s.WorktreeEntries(context.Background(), result.WorkID)
	if err != nil {
		t.Fatal(err)
	}
	active := 0
	for _, candidate := range entries {
		if candidate.State == "active" {
			active++
		}
	}
	if active != 2 {
		t.Fatalf("active worktrees=%d want one per Project", active)
	}

	t.Setenv(dbOverrideEnv, dbPath)
	prepare := func() (int, *bytes.Buffer, *bytes.Buffer, int, int, int) {
		laneCalls, identityCalls, bootCalls := 0, 0, 0
		var out, errOut bytes.Buffer
		code := runSessionPrepare(commandSessionPrepareInput(t, result.WorkID, "run the task"), s, &out, &errOut,
			func(string) error { laneCalls++; return nil },
			func(context.Context, string, string, string, string) (string, error) {
				identityCalls++
				return "agent", nil
			},
			func(context.Context, string, string, string) ([]byte, error) {
				bootCalls++
				return []byte(`{"watermark":"test"}`), nil
			})
		return code, &out, &errOut, laneCalls, identityCalls, bootCalls
	}
	for _, worktree := range []struct {
		name  string
		entry store.WorktreeEntry
	}{
		{"project-wl", result.Entry},
		{"project-wl-b", second.Entry},
	} {
		t.Run("admits "+worktree.name, func(t *testing.T) {
			t.Chdir(worktree.entry.Path)
			code, out, errOut, laneCalls, identityCalls, bootCalls := prepare()
			if code != 0 || laneCalls != 1 || identityCalls != 1 || bootCalls != 1 {
				t.Fatalf("prepare code=%d lane=%d identity=%d boot=%d stderr=%q", code, laneCalls, identityCalls, bootCalls, errOut.String())
			}
			var prepared sessionPrepareOutput
			if err := json.Unmarshal(out.Bytes(), &prepared); err != nil {
				t.Fatalf("decode prepare output: %v", err)
			}
			if !samePath(prepared.Directory, worktree.entry.Path) {
				t.Fatalf("directory=%q want the %s worktree %q", prepared.Directory, worktree.name, worktree.entry.Path)
			}
		})
	}
	for _, repo := range []string{repoA, repoB} {
		t.Run("refuses main checkout of "+filepath.Base(repo), func(t *testing.T) {
			t.Chdir(repo)
			code, _, errOut, laneCalls, identityCalls, bootCalls := prepare()
			if code != sessionPrepareRefusalExit || laneCalls != 0 || identityCalls != 0 || bootCalls != 0 {
				t.Fatalf("main checkout code=%d lane=%d identity=%d boot=%d stderr=%q", code, laneCalls, identityCalls, bootCalls, errOut.String())
			}
			if !strings.Contains(errOut.String(), "not an active claimed worktree") {
				t.Fatalf("refusal diagnostic=%q", errOut.String())
			}
		})
	}
}

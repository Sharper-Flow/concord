package store

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type locatorGitStub struct {
	root         string
	remote       string
	gitDir       string
	commonDir    string
	failTopology bool
	args         [][]string
}

func (g *locatorGitStub) Run(_ context.Context, dir string, args ...string) ([]byte, error) {
	g.args = append(g.args, append([]string{dir}, args...))
	if args[0] == "rev-parse" {
		switch args[1] {
		case "--show-toplevel":
			return []byte(g.root + "\n"), nil
		case "--git-dir":
			if g.failTopology {
				return nil, errors.New("git-dir probe failed")
			}
			if g.gitDir != "" {
				return []byte(g.gitDir + "\n"), nil
			}
			return []byte(g.root + "\n"), nil
		case "--git-common-dir":
			if g.failTopology {
				return nil, errors.New("git-common-dir probe failed")
			}
			if g.commonDir != "" {
				return []byte(g.commonDir + "\n"), nil
			}
			return []byte(g.root + "\n"), nil
		}
	}
	return []byte(g.remote + "\n"), nil
}

func locatorProjectEvent(id string) Event {
	payload, _ := json.Marshal(map[string]any{"display_name": id})
	return Event{EventID: "create-" + id, Kind: "project.created", SubjectType: SubjectProject, SubjectID: id, Actor: "operator", OccurredAt: time.Unix(1, 0).UTC(), PayloadVersion: 1, Payload: payload}
}

func locatorProductEvent(id string) Event {
	payload, _ := json.Marshal(map[string]any{"display_name": id, "stage_maturity": "prototype", "stage_audience_commitment": "operator_only"})
	return Event{EventID: "create-product-" + id, Kind: "product.created", SubjectType: SubjectProduct, SubjectID: id, Actor: "operator", OccurredAt: time.Unix(1, 0).UTC(), PayloadVersion: 1, Payload: payload}
}

func locatorMembershipEvent(product, project string) Event {
	payload, _ := json.Marshal(map[string]any{"product_id": product, "project_id": project, "role": "primary", "reason": "locator fixture", "expected_version": 1, "resulting_version": 2})
	return Event{EventID: "membership-" + product, Kind: "product_project.added", SubjectType: SubjectProduct, SubjectID: product, Actor: "operator", OccurredAt: time.Unix(1, 0).UTC(), PayloadVersion: 1, Payload: payload}
}

func TestProjectLocatorsNormalizeFoldRebuildAndResolveWorktree(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{locatorProductEvent("product-a"), locatorProjectEvent("project-a"), locatorMembershipEvent("product-a", "project-a")}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProduct, "product-a"): 0, VersionRef(SubjectProject, "project-a"): 0}}); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := s.AddProjectLocator(ctx, "project-a", ProjectLocator{ID: "remote-a", Kind: LocatorGitRemote, Value: "git@GitHub.com:Owner/Repo.git"}, 1); err != nil {
		t.Fatal(err)
	}
	if err := s.AddProjectLocator(ctx, "project-a", ProjectLocator{ID: "path-a", Kind: LocatorCanonicalPath, Value: root}, 2); err != nil {
		t.Fatal(err)
	}
	locators, err := s.ProjectLocators(ctx, "project-a")
	if err != nil || len(locators) != 2 {
		t.Fatalf("locators=%+v err=%v", locators, err)
	}
	var remoteLocator ProjectLocator
	for _, locator := range locators {
		if locator.Kind == LocatorGitRemote {
			remoteLocator = locator
		}
	}
	if remoteLocator.NormalizedValue == "git@GitHub.com:Owner/Repo.git" || !strings.Contains(remoteLocator.NormalizedValue, "github.com") {
		t.Fatalf("remote was not normalized: %+v", remoteLocator)
	}
	stub := &locatorGitStub{root: root, remote: "ssh://git@github.com/owner/repo"}
	resolved, err := s.ResolveProjectWithRunner(ctx, filepath.Join(root, "worktree"), root, stub)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.ProjectID != "project-a" || resolved.Repository.CanonicalPath != root {
		t.Fatalf("resolution=%+v", resolved)
	}
	for _, argv := range stub.args {
		if len(argv) < 2 || argv[1] == "" || strings.Contains(strings.Join(argv, " "), "||") {
			t.Fatalf("unsafe git argv=%q", argv)
		}
	}
	if err := RebuildFromLog(ctx, s); err != nil {
		t.Fatal(err)
	}
	locators, err = s.ProjectLocators(ctx, "project-a")
	if err != nil || len(locators) != 2 {
		t.Fatalf("rebuild locators=%+v err=%v", locators, err)
	}
}

func TestProjectLocatorResolutionRejectsUnknownAndAmbiguousRemote(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	root := t.TempDir()
	events := []Event{locatorProductEvent("product-a"), locatorProductEvent("product-b"), locatorProjectEvent("project-a"), locatorProjectEvent("project-b"), locatorMembershipEvent("product-a", "project-a"), locatorMembershipEvent("product-b", "project-b")}
	if err := ApplyOperation(ctx, s, Operation{Events: events, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProduct, "product-a"): 0, VersionRef(SubjectProduct, "product-b"): 0, VersionRef(SubjectProject, "project-a"): 0, VersionRef(SubjectProject, "project-b"): 0}}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddProjectLocator(ctx, "project-a", ProjectLocator{ID: "remote-a", Kind: LocatorGitRemote, Value: "https://example.com/org/repo.git"}, 1); err != nil {
		t.Fatal(err)
	}
	stub := &locatorGitStub{root: root, remote: "https://unknown.example/org/repo"}
	if _, err := s.ResolveProjectWithRunner(ctx, root, root, stub); err == nil {
		t.Fatal("unknown canonical path should not resolve without a matching remote")
	}
	if err := s.AddProjectLocator(ctx, "project-b", ProjectLocator{ID: "remote-b", Kind: LocatorGitRemote, Value: "https://example.com/org/repo"}, 1); err == nil {
		t.Fatal("database uniqueness should reject duplicate remote")
	}
	// Ambiguity is still tested at the resolver boundary when legacy/corrupt
	// state contains two claims; the normal fold cannot create that state.
	if _, err := NormalizeProjectLocator(LocatorCanonicalPath, "../outside"); err == nil {
		t.Fatal("relative path should be rejected when it is not resolvable")
	}
}

func TestResolveProjectDistinguishesMainAndLinkedWorktrees(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{locatorProductEvent("product-wt"), locatorProjectEvent("project-wt"), locatorMembershipEvent("product-wt", "project-wt")}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProduct, "product-wt"): 0, VersionRef(SubjectProject, "project-wt"): 0}}); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	for _, dir := range []string{filepath.Join(root, ".git"), filepath.Join(root, ".git", "worktrees", "feature")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.AddProjectLocator(ctx, "project-wt", ProjectLocator{ID: "path-wt", Kind: LocatorCanonicalPath, Value: root}, 1); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name      string
		gitDir    string
		commonDir string
		main      bool
	}{
		{"absolute main", filepath.Join(root, ".git"), filepath.Join(root, ".git"), true},
		{"absolute linked", filepath.Join(root, ".git", "worktrees", "feature"), filepath.Join(root, ".git"), false},
		{"relative main", ".git", ".git", true},
		{"relative linked", ".git/worktrees/feature", ".git", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub := &locatorGitStub{root: root, remote: "ssh://git@github.com/owner/repo", gitDir: tc.gitDir, commonDir: tc.commonDir}
			resolved, err := s.ResolveProjectWithRunner(ctx, filepath.Join(root, "sub"), root, stub)
			if err != nil {
				t.Fatalf("resolve err=%v", err)
			}
			if resolved.MainWorktree != tc.main {
				t.Fatalf("MainWorktree=%v want %v", resolved.MainWorktree, tc.main)
			}
			if resolved.ProjectID != "project-wt" {
				t.Fatalf("ProjectID=%q", resolved.ProjectID)
			}
		})
	}

	t.Run("topology probe fails closed", func(t *testing.T) {
		stub := &locatorGitStub{root: root, remote: "ssh://git@github.com/owner/repo", failTopology: true}
		if _, err := s.ResolveProjectWithRunner(ctx, root, root, stub); err == nil {
			t.Fatal("expected resolution to fail when git cannot report worktree topology")
		}
	})
}

func TestResolveProjectMatchesLocalLinkedWorktreeToMainCanonicalPath(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{locatorProductEvent("product-local"), locatorProjectEvent("project-local"), locatorMembershipEvent("product-local", "project-local")}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProduct, "product-local"): 0, VersionRef(SubjectProject, "project-local"): 0}}); err != nil {
		t.Fatal(err)
	}
	mainRoot := t.TempDir()
	run := func(dir string, args ...string) {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.invalid", "GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.invalid")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, output)
		}
	}
	run(mainRoot, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(mainRoot, "README.md"), []byte("local\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(mainRoot, "add", ".")
	run(mainRoot, "commit", "-q", "-m", "base")
	if err := s.AddProjectLocator(ctx, "project-local", ProjectLocator{ID: "path-local", Kind: LocatorCanonicalPath, Value: mainRoot}, 1); err != nil {
		t.Fatal(err)
	}
	linked := filepath.Join(t.TempDir(), "linked")
	run(mainRoot, "worktree", "add", "-q", "-b", "feature-local", linked, "main")

	resolved, err := s.ResolveProject(ctx, linked, linked)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.ProjectID != "project-local" || resolved.Repository.CanonicalPath != mainRoot || resolved.Repository.WorktreePath != linked || resolved.MainWorktree || resolved.Repository.GitRemote != "" {
		t.Fatalf("linked local resolution=%+v", resolved)
	}
}

// locatorWorkEvent builds a work.created event for the session-directory
// fixture; memberships arrive separately so a case can leave them out.
func locatorWorkEvent(id string) Event {
	payload, _ := json.Marshal(map[string]any{"work_kind": "task", "title": "Session directory " + id, "priority": 1})
	return Event{EventID: "create-" + id, Kind: "work.created", SubjectType: SubjectWorkItem, SubjectID: id, Actor: "operator", OccurredAt: time.Unix(3, 0).UTC(), PayloadVersion: 2, Payload: payload}
}

func locatorWorkSecondaryEvent(work, project string, expected int64) Event {
	payload, _ := json.Marshal(map[string]any{"memberships": []map[string]string{{"project_id": project, "role": "secondary"}}, "expected_version": expected, "resulting_version": expected + 1})
	return Event{EventID: "membership-secondary-" + work, Kind: "work.memberships_replaced", SubjectType: SubjectWorkItem, SubjectID: work, Actor: "operator", OccurredAt: time.Unix(4, 0).UTC(), PayloadVersion: 1, Payload: payload}
}

func locatorWorkMembershipEvent(work, project string, expected int64) Event {
	payload, _ := json.Marshal(map[string]any{"memberships": []map[string]string{{"project_id": project, "role": "primary"}}, "expected_version": expected, "resulting_version": expected + 1})
	return Event{EventID: "membership-" + work, Kind: "work.memberships_replaced", SubjectType: SubjectWorkItem, SubjectID: work, Actor: "operator", OccurredAt: time.Unix(4, 0).UTC(), PayloadVersion: 1, Payload: payload}
}

// CD-0093 D1/D3: the session-directory read names each missing authority as a
// distinct typed failure, and returns the primary Project's canonical path
// when every authority is present.
func TestProjectDirectoryForWorkNamesEachMissingAuthority(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	root := t.TempDir()
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{locatorProductEvent("product-sd"), locatorProjectEvent("project-sd"), locatorMembershipEvent("product-sd", "project-sd")}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProduct, "product-sd"): 0, VersionRef(SubjectProject, "project-sd"): 0}}); err != nil {
		t.Fatal(err)
	}
	// The fold requires every work to carry a membership, so the no-primary
	// case is a work whose only membership is secondary.
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{locatorWorkEvent("work-sd"), locatorWorkSecondaryEvent("work-sd", "project-sd", 1)}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "work-sd"): 0}}); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name    string
		workID  string
		wantErr string
	}{
		{"empty work ID", "", "work ID is required"},
		{"unknown work", "work-none", "does not exist"},
		{"no primary Project", "work-sd", "no primary Project"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := s.ProjectDirectoryForWork(ctx, tc.workID); err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("ProjectDirectoryForWork(%q) err=%v, want %q", tc.workID, err, tc.wantErr)
			}
		})
	}

	if err := ApplyOperation(ctx, s, Operation{Events: []Event{locatorWorkMembershipEvent("work-sd", "project-sd", 2)}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "work-sd"): 2}}); err != nil {
		t.Fatal(err)
	}
	t.Run("primary Project without a canonical_path locator", func(t *testing.T) {
		if _, err := s.ProjectDirectoryForWork(ctx, "work-sd"); err == nil || !strings.Contains(err.Error(), "no canonical_path locator") {
			t.Fatalf("ProjectDirectoryForWork err=%v, want %q", err, "no canonical_path locator")
		}
	})

	if err := s.AddProjectLocator(ctx, "project-sd", ProjectLocator{ID: "path-sd", Kind: LocatorCanonicalPath, Value: root}, 1); err != nil {
		t.Fatal(err)
	}
	got, err := s.ProjectDirectoryForWork(ctx, "work-sd")
	if err != nil {
		t.Fatalf("ProjectDirectoryForWork err=%v", err)
	}
	if got != root {
		t.Fatalf("ProjectDirectoryForWork=%q want %q", got, root)
	}
}

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

type locateWorktreeGitStub struct {
	defaultRef  string
	sha         string
	args        [][]string
	failResolve bool
}

func (g *locateWorktreeGitStub) Run(_ context.Context, dir string, args ...string) ([]byte, error) {
	g.args = append(g.args, append([]string{dir}, args...))
	if len(args) >= 3 && args[0] == "symbolic-ref" && args[2] == "refs/remotes/origin/HEAD" {
		return []byte(g.defaultRef + "\n"), nil
	}
	if len(args) >= 3 && args[0] == "rev-parse" && args[1] == "--verify" {
		if g.failResolve {
			return nil, errors.New("tracking ref does not resolve")
		}
		return []byte(g.sha + "\n"), nil
	}
	return nil, errors.New("unexpected git verb")
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
	return Event{EventID: "create-" + id, Kind: "project.created", SubjectType: SubjectProject, SubjectID: id, Actor: "operator", OccurredAt: time.Unix(1, 0).UTC(), PayloadVersion: eventKindRegistry["project.created"].CurrentVersion, Payload: payload}
}

func locatorProductEvent(id string) Event {
	payload, _ := json.Marshal(map[string]any{"display_name": id, "stage_maturity": "prototype", "stage_audience_commitment": "operator_only"})
	return Event{EventID: "create-product-" + id, Kind: "product.created", SubjectType: SubjectProduct, SubjectID: id, Actor: "operator", OccurredAt: time.Unix(1, 0).UTC(), PayloadVersion: 1, Payload: payload}
}

func locatorMembershipEvent(product, project string) Event {
	payload, _ := json.Marshal(map[string]any{"product_id": product, "project_id": project, "role": "primary", "reason": "locator fixture", "expected_version": 1, "resulting_version": 2})
	return Event{EventID: "membership-" + product, Kind: "product_project.added", SubjectType: SubjectProduct, SubjectID: product, Actor: "operator", OccurredAt: time.Unix(1, 0).UTC(), PayloadVersion: 1, Payload: payload}
}

func TestProjectLocatorsNormalizeFoldAndResolveWorktree(t *testing.T) {
	t.Parallel()
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
}

func TestProjectLocatorResolutionRejectsUnknownAndAmbiguousRemote(t *testing.T) {
	t.Parallel()
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

func TestProjectLocatorAdmissionRefusesRemoteCredentials(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, remote string }{
		{"https token", "https://TOKEN@github.com/o/r"},
		{"https password", "https://user:pass@github.com/o/r"},
		{"http token", "http://TOKEN@github.com/o/r"},
		{"uppercase scheme", " HTTPS://TOKEN@github.com/o/r "},
		{"ssh password", "ssh://git:pw@host/o/r"},
		{"git password", "git://git:pw@host/o/r"},
		{"other scheme password", "custom://git:pw@host/o/r"},
		{"empty password", "ssh://git:@host/o/r"},
	} {
		for _, update := range []bool{false, true} {
			name := "add/"
			if update {
				name = "update/"
			}
			t.Run(name+tc.name, func(t *testing.T) {
				s := locatorReplayStore(t)
				ctx := context.Background()
				if err := s.AddProjectLocator(ctx, "project-a", ProjectLocator{ID: "remote", Kind: LocatorGitRemote, Value: "git@github.com:o/r.git"}, 1); err != nil {
					t.Fatal(err)
				}
				var before int
				if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM effect_receipts`).Scan(&before); err != nil {
					t.Fatal(err)
				}
				method := s.AddProjectLocator
				id := "new-remote"
				if update {
					method, id = s.UpdateProjectLocator, "remote"
				}
				err := method(ctx, "project-a", ProjectLocator{ID: id, Kind: LocatorGitRemote, Value: tc.remote}, 2)
				assertRemoteAdmissionFailure(t, err, tc.remote, "remove credentials from the remote URL; use a git credential helper")
				assertTableCount(t, s, "effect_receipts", before)
				locators, err := s.ProjectLocators(ctx, "project-a")
				if err != nil || len(locators) != 1 || locators[0].Value != "git@github.com:o/r.git" {
					t.Fatalf("locators changed after refusal: %+v, err=%v", locators, err)
				}
			})
		}
	}
}

func assertRemoteAdmissionFailure(t *testing.T, err error, remote, recovery string) {
	t.Helper()
	assertFailureKind(t, err, KindInvalidFilter)
	var failure *Failure
	errors.As(err, &failure)
	if failure.RecoveryAction != recovery {
		t.Errorf("recovery action=%q", failure.RecoveryAction)
	}
	if strings.Contains(err.Error(), strings.TrimSpace(remote)) {
		t.Error("credential refusal exposes the remote")
	}
	for _, diagnostic := range []string{failure.Detail, failure.RecoveryAction} {
		for _, credential := range []string{"TOKEN", "pw", "pass", "p#ss"} {
			if strings.Contains(diagnostic, credential) {
				t.Errorf("remote refusal diagnostic exposes %q", credential)
			}
		}
	}
}

func TestResolveProjectHostRefusesRemoteCredentials(t *testing.T) {
	t.Parallel()
	for _, remote := range []string{"https://TOKEN@github.com/o/r", "https://user:pass@github.com/o/r", "ssh://git:pw@host/o/r"} {
		t.Run(remote, func(t *testing.T) {
			root := t.TempDir()
			host, err := ResolveProjectHost(context.Background(), root, root, &locatorGitStub{root: root, remote: remote})
			assertRemoteAdmissionFailure(t, err, remote, "remove credentials from the remote URL; use a git credential helper")
			if host != (ResolvedProjectHost{}) {
				t.Fatal("credential refusal returned host data")
			}
		})
	}
}

func TestRemoteAdmissionRefusesURLParseErrors(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, remote string }{
		{"malformed userinfo", "https://user:p#ss@github.com/o/r"},
		{"invalid port", "https://host:badport/o/r"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := locatorReplayStore(t)
			ctx := context.Background()
			var before int
			if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM effect_receipts`).Scan(&before); err != nil {
				t.Fatal(err)
			}
			err := s.AddProjectLocator(ctx, "project-a", ProjectLocator{ID: "remote", Kind: LocatorGitRemote, Value: tc.remote}, 1)
			assertRemoteAdmissionFailure(t, err, tc.remote, "use a remote URL or scp-style SSH remote")
			assertTableCount(t, s, "effect_receipts", before)
			assertTableCount(t, s, "project_locators", 0)
			root := t.TempDir()
			host, err := ResolveProjectHost(ctx, root, root, &locatorGitStub{root: root, remote: tc.remote})
			assertRemoteAdmissionFailure(t, err, tc.remote, "use a remote URL or scp-style SSH remote")
			if host != (ResolvedProjectHost{}) {
				t.Fatal("parse-error refusal returned host data")
			}
		})
	}
}

func TestRemoteURLFormClassification(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		remote string
		url    bool
	}{
		{"https://github.com/o/r", true},
		{"HTTPS://TOKEN@github.com/o/r", true},
		{"git+ssh://host/o/r", true},
		{"g1.t-x://host/o/r", true},
		{"host:o/r@v2.git", false},
		{"git:pw@host:a://b", false},
		{"git:pw@host:o/r.git", false},
		{"1http://host/o/r", false},
	} {
		if got := remoteURLFormPattern.MatchString(tc.remote); got != tc.url {
			t.Errorf("URL form for %q = %v, want %v", tc.remote, got, tc.url)
		}
	}
}

func TestRemoteAdmissionPreservesNormalizedIdentity(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ remote, normalized string }{
		{"git@github.com:o/r.git", "ssh://git@github.com/o/r"},
		{"ssh://git@host/o/r", "ssh://git@host/o/r"},
		{"git://git@host/o/r", "git://git@host/o/r"},
		{"https://github.com/o/r", "https://github.com/o/r"},
		{"http://github.com/o/r", "http://github.com/o/r"},
		{"host:o/r@v2.git", "ssh://host/o/r@v2"},
		{"git:pw@host:a://b", "ssh://git/pw@host:a://b"},
		{"git:pw@host:o/r.git", "ssh://git/pw@host:o/r"},
	} {
		t.Run(tc.remote, func(t *testing.T) {
			s := locatorReplayStore(t)
			ctx := context.Background()
			if err := s.AddProjectLocator(ctx, "project-a", ProjectLocator{ID: "remote", Kind: LocatorGitRemote, Value: tc.remote}, 1); err != nil {
				t.Fatal(err)
			}
			if err := s.UpdateProjectLocator(ctx, "project-a", ProjectLocator{ID: "remote", Kind: LocatorGitRemote, Value: tc.remote}, 2); err != nil {
				t.Fatal(err)
			}
			locators, err := s.ProjectLocators(ctx, "project-a")
			if err != nil || len(locators) != 1 || locators[0].NormalizedValue != tc.normalized {
				t.Fatalf("locators=%+v err=%v; want normalized=%q", locators, err, tc.normalized)
			}
			root := t.TempDir()
			host, err := ResolveProjectHost(ctx, root, root, &locatorGitStub{root: root, remote: tc.remote})
			if err != nil || host.Remote != tc.normalized || host.Host.GitRemote != tc.normalized {
				t.Fatalf("host=%+v err=%v; want normalized=%q", host, err, tc.normalized)
			}
			resolved, err := s.MatchResolvedProjectHost(ctx, host)
			if err != nil || resolved.ProjectID != "project-a" {
				t.Fatalf("resolution=%+v err=%v", resolved, err)
			}
		})
	}
}

func TestResolveProjectDistinguishesMainAndLinkedWorktrees(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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

func TestLocateWorktreeDefaultUsesRemoteTrackingRefWithoutNetwork(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	createProductProject(t, s, "product-locate", "project-locate")
	repo := t.TempDir()
	if err := s.AddProjectLocator(ctx, "project-locate", ProjectLocator{ID: "path-locate", Kind: LocatorCanonicalPath, Value: repo}, 1); err != nil {
		t.Fatal(err)
	}
	sha := strings.Repeat("a", 40)
	runner := &locateWorktreeGitStub{defaultRef: "refs/remotes/origin/main", sha: sha}

	// An omitted ref is default-based: the base resolves through the local
	// remote-tracking ref alone, with no network call, and the location
	// records HEAD as the selected provenance.
	location, err := s.locateWorktreeWithRunner(ctx, "project-locate", "work-locate", "", runner)
	if err != nil {
		t.Fatal(err)
	}
	if location.BaseSHA != sha {
		t.Fatalf("base SHA=%q want %q", location.BaseSHA, sha)
	}
	if location.Ref != "HEAD" {
		t.Fatalf("recorded ref=%q want HEAD", location.Ref)
	}
	if len(runner.args) != 2 || strings.Join(runner.args[1][1:], " ") != "rev-parse --verify refs/remotes/origin/main^{commit}" {
		t.Fatalf("git calls=%q", runner.args)
	}
}

func TestLocateWorktreeRefusalDoesNotFallBackToHEAD(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	createProductProject(t, s, "product-refusal", "project-refusal")
	repo := t.TempDir()
	if err := s.AddProjectLocator(ctx, "project-refusal", ProjectLocator{ID: "path-refusal", Kind: LocatorCanonicalPath, Value: repo}, 1); err != nil {
		t.Fatal(err)
	}
	runner := &locateWorktreeGitStub{defaultRef: "refs/remotes/origin/main", failResolve: true}

	// The default-based read refuses when its tracking ref does not resolve:
	// it never falls back to the repository's local HEAD.
	if _, err := s.locateWorktreeWithRunner(ctx, "project-refusal", "work-refusal", "", runner); err == nil {
		t.Fatal("unresolvable tracking ref unexpectedly fell back to HEAD")
	} else {
		assertFailureKind(t, err, KindGitUnreachable)
	}
	if len(runner.args) != 2 {
		t.Fatalf("git calls=%q", runner.args)
	}
}

func TestLocateWorktreeReturnedBaseCreatesNativeWorktree(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	createProductProject(t, s, "product-native", "project-native")
	repo := initBootstrapStoreRepo(t)
	if err := s.AddProjectLocator(ctx, "project-native", ProjectLocator{ID: "path-native", Kind: LocatorCanonicalPath, Value: repo}, 1); err != nil {
		t.Fatal(err)
	}
	base := strings.TrimSpace(runTestGit(t, repo, "rev-parse", "HEAD"))
	if err := os.WriteFile(filepath.Join(repo, "local-only.txt"), []byte("local\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, repo, "add", "local-only.txt")
	runTestGit(t, repo, "commit", "-q", "-m", "local-only")
	worktreePath := filepath.Join(t.TempDir(), "native-worktree")
	// The default-based selection pins the remote-tracking ref, so a
	// local-only commit on the default branch never leaks into the base a
	// fresh creation builds on.
	location, err := s.LocateWorktree(ctx, "project-native", "work-native", "")
	if err != nil {
		t.Fatal(err)
	}
	if location.BaseSHA != base {
		t.Fatalf("base SHA=%q want tracking SHA %q", location.BaseSHA, base)
	}
	runTestGit(t, repo, "worktree", "add", "--detach", worktreePath, location.BaseSHA)
	checkedOut := strings.TrimSpace(runTestGit(t, worktreePath, "rev-parse", "HEAD"))
	if checkedOut != location.BaseSHA {
		t.Fatalf("native worktree HEAD=%q want %q", checkedOut, location.BaseSHA)
	}
}

// resolveCommitSHARunner guards every base resolution against a ref that git
// would read as an option or that carries whitespace or NUL, so the refusals
// are asserted beside the guard rather than through one caller.
func TestResolveCommitSHARunnerRejectsHostileRefs(t *testing.T) {
	ctx := context.Background()
	repo := initBootstrapStoreRepo(t)
	for _, ref := range []string{"-help", "main branch", "main\x00suffix"} {
		if _, err := resolveCommitSHARunner(ctx, ExecGitRunner{}, repo, ref); err == nil {
			t.Fatalf("hostile ref %q was accepted", ref)
		}
	}
	sha, err := resolveCommitSHARunner(ctx, ExecGitRunner{}, repo, "refs/heads/main")
	if err != nil || len(sha) != 40 {
		t.Fatalf("valid symbolic ref: sha=%q err=%v", sha, err)
	}
}

func locatorReplayStore(t *testing.T) *Store {
	t.Helper()
	s := openTemp(t)
	ctx := context.Background()
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{locatorProductEvent("product-a"), locatorProjectEvent("project-a"), locatorMembershipEvent("product-a", "project-a")}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProduct, "product-a"): 0, VersionRef(SubjectProject, "project-a"): 0}}); err != nil {
		t.Fatal(err)
	}
	return s
}

func recordedLocatorEvent(id, kind, value, normalized string) Event {
	payload, _ := json.Marshal(map[string]any{
		"project_id": "project-a", "locator_id": id, "kind": kind,
		"value": value, "normalized_value": normalized,
		"expected_version": 1, "resulting_version": 2,
	})
	return Event{EventID: "locator-" + id, Kind: "project.locator_added", SubjectType: SubjectProject, SubjectID: "project-a", Actor: "operator", OccurredAt: time.Unix(2, 0).UTC(), PayloadVersion: CurrentEventPayloadVersion("project.locator_added"), Payload: payload}
}

// TestRecordedCanonicalPathNegativeControls proves the fold still refuses a
// recorded canonical_path payload whose normalized value is missing, relative,
// unclean, or carries NUL, without touching the host filesystem.
func TestRecordedCanonicalPathNegativeControls(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, value, normalized string }{
		{name: "missing normalized", value: "/tmp/repo", normalized: ""},
		{name: "relative normalized", value: "/tmp/repo", normalized: "tmp/repo"},
		{name: "unclean normalized", value: "/tmp/repo", normalized: "/tmp//repo"},
		{name: "trailing slash normalized", value: "/tmp/repo", normalized: "/tmp/repo/"},
		{name: "nul in normalized", value: "/tmp/repo", normalized: "/tmp/\x00repo"},
		{name: "nul in value", value: "/tmp/\x00repo", normalized: "/tmp/repo"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := locatorReplayStore(t)
			event := recordedLocatorEvent("path-tampered", string(LocatorCanonicalPath), tc.value, tc.normalized)
			err := ApplyOperation(context.Background(), s, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProject, "project-a"): 1}})
			assertFailureKind(t, err, KindInvalidPayload)
		})
	}
}

// TestRecordedRemoteNormalizationDeterministic proves remote fold validation is
// unchanged: remote normalization is pure, so a tampered recorded normalized
// value still refuses the fold.
func TestRecordedRemoteNormalizationDeterministic(t *testing.T) {
	t.Parallel()
	s := locatorReplayStore(t)
	event := recordedLocatorEvent("remote-tampered", string(LocatorGitRemote), "https://example.com/org/repo", "https://example.com/other/repo")
	err := ApplyOperation(context.Background(), s, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProject, "project-a"): 1}})
	if err == nil || !strings.Contains(err.Error(), "normalization is not deterministic") {
		t.Fatalf("tampered remote normalization err=%v", err)
	}
}

// TestLiveLocatorAdditionStillRefusesAbsentPath proves the live mutation path
// keeps its host check: adding a canonical_path locator to an absent path
// refuses with the unreachable kind.
func TestLiveLocatorAdditionStillRefusesAbsentPath(t *testing.T) {
	t.Parallel()
	s := locatorReplayStore(t)
	absent := filepath.Join(t.TempDir(), "absent")
	err := s.AddProjectLocator(context.Background(), "project-a", ProjectLocator{Kind: LocatorCanonicalPath, Value: absent}, 1)
	assertFailureKind(t, err, KindGitUnreachable)
}

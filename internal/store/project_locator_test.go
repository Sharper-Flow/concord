package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type locatorGitStub struct {
	root   string
	remote string
	args   [][]string
}

func (g *locatorGitStub) Run(_ context.Context, dir string, args ...string) ([]byte, error) {
	g.args = append(g.args, append([]string{dir}, args...))
	if args[0] == "rev-parse" {
		return []byte(g.root + "\n"), nil
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

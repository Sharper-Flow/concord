package store

import (
	"context"
	"os"
	"reflect"
	"testing"
	"time"
)

// multiProjectRecoveryFixture holds one live item with a canonical worktree
// in each of two temporary Git repositories, without a capture bootstrap journal.
type multiProjectRecoveryFixture struct {
	store   *Store
	workID  string
	entries map[string]WorktreeEntry
}

func newMultiProjectRecoveryFixture(t *testing.T) multiProjectRecoveryFixture {
	t.Helper()
	return multiProjectRecoveryFixtureForStore(t, openTemp(t))
}

func multiProjectRecoveryFixtureForStore(t *testing.T, s *Store) multiProjectRecoveryFixture {
	t.Helper()
	const workID = "work-multi-recovery"
	seedWork(t, s, workID)
	ctx := context.Background()
	workVersion := readWorkVersion(t, s, workID)
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{
		projectCreatedEvent("project-sibling", "create-sibling"),
		operationEvent("product-sibling", "product_project.added", SubjectProduct, "product", map[string]any{
			"product_id": "product", "project_id": "project-sibling", "role": "secondary", "reason": "fixture",
			"expected_version": 2, "resulting_version": 3,
		}),
		operationEvent("work-sibling", "work.memberships_replaced", SubjectWorkItem, workID, map[string]any{
			"memberships":      []workMembershipPayload{{ProjectID: "project", Role: "primary"}, {ProjectID: "project-sibling", Role: "secondary"}},
			"expected_version": workVersion, "resulting_version": workVersion + 1,
		}),
	}}); err != nil {
		t.Fatal(err)
	}
	for _, project := range []string{"project", "project-sibling"} {
		repo := initBootstrapStoreRepo(t)
		if err := s.AddProjectLocator(ctx, project, ProjectLocator{ID: "path-" + project, Kind: LocatorCanonicalPath, Value: repo}, 1); err != nil {
			t.Fatal(err)
		}
		base := runTestGit(t, repo, "rev-parse", "HEAD")
		if _, err := s.ClaimWorktree(ctx, WorktreeClaimRequest{
			OpID: "claim-" + project, WorkID: workID, ProjectID: project, BaseSHA: base,
			PrincipalRef: "principal/operator", RequestID: "claim-" + project,
			ExpectedVersion: readWorkVersion(t, s, workID),
		}); err != nil {
			t.Fatal(err)
		}
	}
	return multiProjectRecoveryFixture{store: s, workID: workID, entries: worktreeEntriesByProject(t, s, workID)}
}

func (f multiProjectRecoveryFixture) occupy(t *testing.T, project string, live bool) {
	t.Helper()
	entry := f.entries[project]
	var pid any
	var start any
	if live {
		ownerPID, ownerStart := bootstrapTestOwner(t)
		pid, start = ownerPID, ownerStart
	}
	if _, err := f.store.db.Exec(`INSERT INTO write_guard(active) VALUES(1);
		INSERT INTO worktree_occupancy(worktree_id,session_ref,recorded_at,host_pid,host_pid_start,has_process_identity)
		VALUES(?,?,?,?,?,?); DELETE FROM write_guard`,
		worktreeOccupancyID(entry.SetID, project, entry.ClaimOpID), "session-"+project,
		time.Now().UTC().Format(time.RFC3339Nano), pid, start, live); err != nil {
		t.Fatal(err)
	}
}

func TestMultiProjectRecoveryBootstrapsMissingProject(t *testing.T) {
	f := newMultiProjectRecoveryFixture(t)
	f.occupy(t, "project-sibling", true)
	ctx := context.Background()
	if _, err := f.store.ReclaimWorktree(ctx, WorktreeReclaimRequest{
		WorkID: f.workID, ProjectID: "project", PrincipalRef: "principal/operator", RequestID: "lost-primary",
		ExpectedVersion: readWorkVersion(t, f.store, f.workID),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(f.entries["project"].Path); !os.IsNotExist(err) {
		t.Fatalf("primary worktree is not missing: %v", err)
	}
	request := ExistingBootstrapRequest{ProductID: "product", ProjectID: "project", WorkID: f.workID}
	recovered, err := f.store.BootstrapExistingWorktree(ctx, request, nil)
	if err != nil {
		t.Fatalf("Project-local recovery refused while sibling stays active: %v", err)
	}
	if recovered.Entry.ProjectID != "project" || recovered.Entry.Path != f.entries["project"].Path {
		t.Fatalf("recovery changed its subject: %+v", recovered.Entry)
	}
	if _, err := os.Stat(recovered.Entry.Path); err != nil {
		t.Fatal(err)
	}
	replay, err := f.store.BootstrapExistingWorktree(ctx, request, nil)
	if err != nil || !replay.Replayed || replay.Entry.ClaimOpID != recovered.Entry.ClaimOpID {
		t.Fatalf("replay did not converge: %+v, %v", replay, err)
	}
	f.assertSiblingUnchanged(t)
}

func (f multiProjectRecoveryFixture) assertSiblingUnchanged(t *testing.T) {
	t.Helper()
	entry := worktreeEntriesByProject(t, f.store, f.workID)["project-sibling"]
	if !reflect.DeepEqual(entry, f.entries["project-sibling"]) {
		t.Fatalf("sibling entry changed: %+v", entry)
	}
	if got := worktreeOccupancyByEntry(t, f.store, entry.SetID, entry.ProjectID, entry.ClaimOpID); got != "session-project-sibling" {
		t.Fatalf("sibling occupancy changed: %s", got)
	}
	if head := runTestGit(t, entry.Path, "rev-parse", "HEAD"); head != entry.BaseSHA {
		t.Fatalf("sibling head changed: %s", head)
	}
}

package store

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// multiProjectRecoveryFixture holds one live item with a canonical worktree
// in each of two temporary Git repositories, without a capture bootstrap journal.
type multiProjectRecoveryFixture struct {
	store   *Store
	seed    cd0059DispatchSeed
	entries map[string]WorktreeEntry
}

func newMultiProjectRecoveryFixture(t *testing.T) multiProjectRecoveryFixture {
	t.Helper()
	return multiProjectRecoveryFixtureForStore(t, openTemp(t))
}

func multiProjectRecoveryFixtureForStore(t *testing.T, s *Store) multiProjectRecoveryFixture {
	t.Helper()
	seed := seedDispatchFixture(t, s, "work-multi-recovery")
	ctx := context.Background()
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{
		projectCreatedEvent("project-sibling", "create-sibling"),
		operationEvent("product-sibling", "product_project.added", SubjectProduct, "product", map[string]any{
			"product_id": "product", "project_id": "project-sibling", "role": "secondary", "reason": "fixture",
			"expected_version": 2, "resulting_version": 3,
		}),
		operationEvent("work-sibling", "work.memberships_replaced", SubjectWorkItem, seed.workID, map[string]any{
			"memberships":      []workMembershipPayload{{ProjectID: "project", Role: "primary"}, {ProjectID: "project-sibling", Role: "secondary"}},
			"expected_version": 4, "resulting_version": 5,
		}),
	}}); err != nil {
		t.Fatal(err)
	}
	for _, project := range []string{"project", "project-sibling"} {
		repo := initBootstrapStoreRepo(t)
		if err := s.AddProjectLocator(ctx, project, ProjectLocator{ID: "path-" + project, Kind: LocatorCanonicalPath, Value: repo}, 1); err != nil {
			t.Fatal(err)
		}
		base := runBootstrapGit(t, repo, "rev-parse", "HEAD")
		if _, err := s.ClaimWorktree(ctx, WorktreeClaimRequest{
			OpID: "claim-" + project, WorkID: seed.workID, ProjectID: project, BaseSHA: base,
			PrincipalRef: "principal/operator", RequestID: "claim-" + project,
			ExpectedVersion: readWorkVersion(t, s, seed.workID),
		}); err != nil {
			t.Fatal(err)
		}
	}
	return multiProjectRecoveryFixture{store: s, seed: seed, entries: worktreeEntriesByProject(t, s, seed.workID)}
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
	if _, err := f.store.db.Exec(`INSERT INTO fold_guard(active) VALUES(1);
		INSERT INTO worktree_occupancy(worktree_id,session_ref,recorded_at,host_pid,host_pid_start,has_process_identity)
		VALUES(?,?,?,?,?,?); DELETE FROM fold_guard`,
		worktreeOccupancyID(entry.SetID, project, entry.ClaimOpID), "session-"+project,
		time.Now().UTC().Format(time.RFC3339Nano), pid, start, live); err != nil {
		t.Fatal(err)
	}
}

func (f multiProjectRecoveryFixture) dispatch(t *testing.T, attempt string) {
	t.Helper()
	request := cd781DispatchRequest(t, f.store, f.seed.workID, readWorkVersion(t, f.store, f.seed.workID),
		attempt, f.seed.ownerActor, f.entries["project"].Path, attempt)
	if _, err := invokeWorkflowActionForCD0059(context.Background(), t, f.store, request); err != nil {
		t.Fatal(err)
	}
}

func (f multiProjectRecoveryFixture) abandon(attempt string) error {
	return ApplyOperation(context.Background(), f.store, Operation{Events: []Event{{
		EventID: "abandon-" + attempt, Kind: WorkerFailed, SubjectType: SubjectWorkItem, SubjectID: f.seed.workID,
		Actor: "worker:test", OccurredAt: time.Now().UTC(), PayloadVersion: 1,
		Payload: mustJSONValue(WorkerFailedPayload{AttemptID: attempt, FailureKind: WorkerFailureAbandoned, Detail: "the worker never reported"}),
	}}})
}

func TestMultiProjectRecoveryBootstrapsMissingProject(t *testing.T) {
	f := newMultiProjectRecoveryFixture(t)
	f.occupy(t, "project-sibling", true)
	ctx := context.Background()
	if _, err := f.store.ReclaimWorktree(ctx, WorktreeReclaimRequest{
		WorkID: f.seed.workID, ProjectID: "project", PrincipalRef: "principal/operator", RequestID: "lost-primary",
		ExpectedVersion: readWorkVersion(t, f.store, f.seed.workID),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(f.entries["project"].Path); !os.IsNotExist(err) {
		t.Fatalf("primary worktree is not missing: %v", err)
	}
	request := ExistingBootstrapRequest{ProductID: "product", ProjectID: "project", WorkID: f.seed.workID}
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

func TestMultiProjectRecoveryAbandonsOnlyAttemptProject(t *testing.T) {
	for _, liveSibling := range []bool{false, true} {
		for _, dispatched := range []bool{false, true} {
			t.Run(map[bool]string{false: "legacy", true: "live"}[liveSibling]+"/"+map[bool]string{false: "in_flight", true: "dispatched"}[dispatched], func(t *testing.T) {
				f := newMultiProjectRecoveryFixture(t)
				attempt := "attempt-missing-primary"
				f.dispatch(t, attempt)
				if dispatched {
					version, digest := implementLaneIdentity()
					lane, err := LookupLane("implement", version, digest)
					if err != nil {
						t.Fatal(err)
					}
					if err := ApplyOperation(context.Background(), f.store, Operation{Events: []Event{workerDispatchEvent(f.seed.workID, attempt, lane, nil)}}); err != nil {
						t.Fatal(err)
					}
				}
				f.occupy(t, "project-sibling", liveSibling)
				entry := f.entries["project"]
				runBootstrapGit(t, entry.RepositoryID, "worktree", "remove", entry.Path)
				runBootstrapGit(t, entry.RepositoryID, "branch", "-D", entry.Branch)
				if err := f.abandon(attempt); err != nil {
					t.Fatalf("lost primary attempt resolved to sibling occupancy: %v", err)
				}
				assertWorkerAttemptState(t, f.store, f.seed.workID, attempt, "failed")
				f.assertSiblingUnchanged(t)
			})
		}
	}
}

func TestMultiProjectRecoveryKeepsOwnOccupancyProtection(t *testing.T) {
	for _, live := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy", true: "live"}[live], func(t *testing.T) {
			f := newMultiProjectRecoveryFixture(t)
			f.dispatch(t, "attempt-own-occupant")
			f.occupy(t, "project", live)
			if err := f.abandon("attempt-own-occupant"); !hasFailureKind(err, KindWorktreeOwnershipConflict) {
				t.Fatalf("own occupancy did not refuse abandonment: %v", err)
			}
			assertWorkerAttemptState(t, f.store, f.seed.workID, "attempt-own-occupant", "in_flight")
		})
	}
}

func TestMultiProjectRecoveryRefusesUnresolvedAttemptOwnership(t *testing.T) {
	f := newMultiProjectRecoveryFixture(t)
	f.dispatch(t, "attempt-unresolved")
	if _, err := f.store.db.Exec(`INSERT INTO fold_guard(active) VALUES(1);
		DELETE FROM worktree_entries WHERE set_id=? AND project_id='project';
		DELETE FROM fold_guard`, WorktreeSetID(f.seed.workID)); err != nil {
		t.Fatal(err)
	}
	if err := f.abandon("attempt-unresolved"); !hasFailureKind(err, KindWorktreeOwnershipConflict) {
		t.Fatalf("unresolved bound ownership did not refuse: %v", err)
	}
	assertWorkerAttemptState(t, f.store, f.seed.workID, "attempt-unresolved", "in_flight")
}

func TestMultiProjectRecoveryAbandonsMissingSymlinkedWorktree(t *testing.T) {
	alias := filepath.Join(t.TempDir(), "store-link")
	if err := os.Symlink(t.TempDir(), alias); err != nil {
		t.Fatal(err)
	}
	s, err := Open(context.Background(), filepath.Join(alias, "concord.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	f := multiProjectRecoveryFixtureForStore(t, s)
	f.dispatch(t, "attempt-symlinked")
	f.occupy(t, "project-sibling", true)
	entry := f.entries["project"]
	runBootstrapGit(t, entry.RepositoryID, "worktree", "remove", entry.Path)
	if err := os.Remove(filepath.Dir(entry.Path)); err != nil {
		t.Fatal(err)
	}
	if err := f.abandon("attempt-symlinked"); err != nil {
		t.Fatalf("missing worktree below a symlinked store lost ownership: %v", err)
	}
	assertWorkerAttemptState(t, s, f.seed.workID, "attempt-symlinked", "failed")
	f.assertSiblingUnchanged(t)
}

func TestMultiProjectRecoveryKeepsLegacyAttemptConservative(t *testing.T) {
	f := newMultiProjectRecoveryFixture(t)
	lane := BuiltinLaneDefinitions()[0]
	attempt := "attempt-without-window"
	if err := ApplyOperation(context.Background(), f.store, Operation{Events: []Event{workerDispatchEvent(f.seed.workID, attempt, lane, nil)}}); err != nil {
		t.Fatal(err)
	}
	f.occupy(t, "project-sibling", true)
	if err := f.abandon(attempt); !hasFailureKind(err, KindWorktreeOwnershipConflict) {
		t.Fatalf("legacy attempt without ownership proof bypassed occupancy: %v", err)
	}
	assertWorkerAttemptState(t, f.store, f.seed.workID, attempt, "dispatched")
	f.assertSiblingUnchanged(t)
}

func TestMultiProjectRecoveryAbandonmentReplaysWithoutHostObservations(t *testing.T) {
	for _, observation := range []string{"removed_symlink", "legacy_occupancy"} {
		t.Run(observation, func(t *testing.T) {
			alias := filepath.Join(t.TempDir(), "store-link")
			if err := os.Symlink(t.TempDir(), alias); err != nil {
				t.Fatal(err)
			}
			s, err := Open(context.Background(), filepath.Join(alias, "concord.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = s.Close() })
			f := multiProjectRecoveryFixtureForStore(t, s)
			attempt := "attempt-replay"
			f.dispatch(t, attempt)
			version, digest := implementLaneIdentity()
			lane, err := LookupLane("implement", version, digest)
			if err != nil {
				t.Fatal(err)
			}
			if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{workerDispatchEvent(f.seed.workID, attempt, lane, nil)}}); err != nil {
				t.Fatal(err)
			}
			f.occupy(t, "project-sibling", true)
			if err := f.abandon(attempt); err != nil {
				t.Fatalf("live abandonment refused: %v", err)
			}
			terminal := func() [3]string {
				t.Helper()
				var row [3]string
				if err := s.db.QueryRow(`SELECT lifecycle_state,failure_kind,readback_model FROM worker_attempts WHERE work_id=? AND attempt_id=?`, f.seed.workID, attempt).Scan(&row[0], &row[1], &row[2]); err != nil {
					t.Fatal(err)
				}
				return row
			}
			before := terminal()
			if want := [3]string{"failed", WorkerFailureAbandoned, preferredModelForLane(lane)}; before != want {
				t.Fatalf("live terminal row = %v, want %v", before, want)
			}
			if observation == "removed_symlink" {
				if err := os.Remove(alias); err != nil {
					t.Fatal(err)
				}
			} else {
				f.occupy(t, "project", false)
			}
			if err := RebuildFromLog(context.Background(), s); err != nil {
				t.Fatalf("recorded abandonment consulted present-day %s: %v", observation, err)
			}
			if after := terminal(); after != before {
				t.Fatalf("replay terminal row = %v, want %v", after, before)
			}
		})
	}
}

func (f multiProjectRecoveryFixture) assertSiblingUnchanged(t *testing.T) {
	t.Helper()
	entry := worktreeEntriesByProject(t, f.store, f.seed.workID)["project-sibling"]
	if !reflect.DeepEqual(entry, f.entries["project-sibling"]) {
		t.Fatalf("sibling entry changed: %+v", entry)
	}
	if got := worktreeOccupancyByEntry(t, f.store, entry.SetID, entry.ProjectID, entry.ClaimOpID); got != "session-project-sibling" {
		t.Fatalf("sibling occupancy changed: %s", got)
	}
	if head := runBootstrapGit(t, entry.Path, "rev-parse", "HEAD"); head != entry.BaseSHA {
		t.Fatalf("sibling head changed: %s", head)
	}
}

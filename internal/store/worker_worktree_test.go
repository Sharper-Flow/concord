package store

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateWorkerDispatchWorktreeCanonicalizesSymlinks(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	root := t.TempDir()
	claimed := filepath.Join(root, "claimed")
	if err := os.Mkdir(claimed, 0o755); err != nil {
		t.Fatalf("create claimed worktree: %v", err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(claimed, alias); err != nil {
		t.Fatalf("create worktree alias: %v", err)
	}
	insertWorkerWorktreeEntry(t, s, "work-claim", claimed)
	if err := validateWorkerDispatchWorktree(context.Background(), s.db, "work-claim", alias); err != nil {
		t.Fatalf("canonical worktree match refused: %v", err)
	}
}

func TestValidateWorkerDispatchWorktreeRefusesMismatchWithoutPathLeak(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	root := t.TempDir()
	claimed := filepath.Join(root, "claimed")
	session := filepath.Join(root, "session")
	for _, path := range []string{claimed, session} {
		if err := os.Mkdir(path, 0o755); err != nil {
			t.Fatalf("create worktree %s: %v", path, err)
		}
	}
	insertWorkerWorktreeEntry(t, s, "work-mismatch", claimed)
	err := validateWorkerDispatchWorktree(context.Background(), s.db, "work-mismatch", session)
	if err == nil {
		t.Fatal("mismatched worktree was accepted")
	}
	var failure *Failure
	if !failureAs(err, &failure) || failure.Kind != KindUnauthorizedDispatch {
		t.Fatalf("failure = %v, want unauthorized_dispatch", err)
	}
	if strings.Contains(failure.Detail, claimed) || strings.Contains(failure.Detail, session) {
		t.Fatalf("failure leaked a machine path: %q", failure.Detail)
	}
	if !strings.Contains(failure.Detail, "expected") || !strings.Contains(failure.Detail, "session boundary") {
		t.Fatalf("failure does not identify both worktree boundaries: %q", failure.Detail)
	}
}

func TestValidateWorkerDispatchWorktreeRequiresActiveClaim(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	root := t.TempDir()
	err := validateWorkerDispatchWorktree(context.Background(), s.db, "work-without-claim", root)
	if err == nil {
		t.Fatal("dispatch without an active claim was accepted")
	}
	var failure *Failure
	if !failureAs(err, &failure) || failure.Kind != KindUnauthorizedDispatch {
		t.Fatalf("failure = %v, want unauthorized_dispatch", err)
	}
}

func insertWorkerWorktreeEntry(t *testing.T, s *Store, workID, path string) {
	t.Helper()
	_, err := s.DatabaseForTesting().Exec(`
INSERT INTO fold_guard(active) VALUES(1);
INSERT INTO worktree_entries(set_id,project_id,claim_op_id,branch,base_sha,path,repository_id,state,verified_at,git_facts)
VALUES(?,?,?,?,?,?,?,'active',?,'{}');
DELETE FROM fold_guard;`, WorktreeSetID(workID), "project-1", "claim-1", "change/claim", strings.Repeat("a", 40), path, "repo-1", "2026-09-05T00:00:00Z")
	if err != nil {
		t.Fatalf("insert worktree entry: %v", err)
	}
}

func authorizeWorkerFixtureDispatch(t *testing.T, s *Store, workID, attemptID string) string {
	t.Helper()
	seed := seedDispatchFixture(t, s, workID)
	actorRef, err := WorkflowActorRef(seed.ownerActor)
	if err != nil {
		t.Fatal(err)
	}
	recordWorkerFixtureDispatchAuthorization(t, s, workID, attemptID, "execution", actorRef)
	return actorRef
}

func recordWorkerFixtureDispatchAuthorization(t *testing.T, s *Store, workID, attemptID, stepID, actorRef string) {
	t.Helper()
	appendWorkerFixtureDispatchAuthorization(t, s, workID, attemptID, stepID, actorRef)
	var attempts int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM worker_attempts WHERE work_id=? AND attempt_id=?`, workID, attemptID).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if attempts != 0 {
		t.Fatal("authorization fixture recorded the caller's dispatch evidence")
	}
}

func appendWorkerFixtureDispatchAuthorization(t *testing.T, s *Store, workID, attemptID, stepID, actorRef string) {
	t.Helper()
	identity := workerFixtureWorktreeIdentity(t, s, workID)
	var epoch int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT COALESCE(MAX(json_extract(payload,'$.attempt_epoch')),0)+1 FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.step_id')=?`, workID, WorkflowActionStarted, stepID).Scan(&epoch); err != nil {
		t.Fatal(err)
	}
	version := readWorkVersion(t, s, workID)
	started := workflowEventWithActor("fixture-dispatch-start-"+attemptID, WorkflowActionStarted, workID, actorRef, map[string]any{
		"work_id": workID, "expected_version": version, "resulting_version": version + 1,
		"step_id": stepID, "action_id": "dispatch_worker", "attempt_epoch": epoch,
		"accepted_inputs_digest": "sha256:" + strings.Repeat("a", 64), "idempotency_identity": "dispatch:" + attemptID,
		"actor_ref": actorRef, "execution_model": preferredModelForLane(BuiltinLaneDefinitions()[0]),
	})
	// Pre-binding authorization leaves the caller's generic dispatch evidence unchanged.
	completed := workflowEventWithActor("fixture-dispatch-done-"+attemptID, WorkflowActionCompleted, workID, actorRef, map[string]any{
		"work_id": workID, "expected_version": version + 1, "resulting_version": version + 2,
		"step_id": stepID, "action_id": "dispatch_worker", "attempt_epoch": epoch,
		"worker_attempt_id": attemptID, "worker_worktree_identity": identity,
		"worker_packet_digest": "sha256:" + strings.Repeat("f", 64), "worker_packet_predicate_ids": []string{},
		"result_evidence_refs": []string{}, "changed_refs": []string{workID}, "actor_ref": actorRef,
	})
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{started, completed}}); err != nil {
		t.Fatal(err)
	}
}

func TestValidateWorkerCompletionWorktreeBindsAttemptProject(t *testing.T) {
	for _, tc := range []struct {
		name       string
		directory  string
		fault      string
		wantKind   string
		boundaries bool
	}{
		{name: "own Project", directory: "own"},
		{name: "own Project symlink", directory: "alias"},
		{name: "other Project", directory: "other", wantKind: string(KindUnauthorizedDispatch), boundaries: true},
		{name: "generic mismatch before missing authorization", directory: "outside", fault: "no authorization", wantKind: string(KindUnauthorizedDispatch), boundaries: true},
		{name: "missing authorization", directory: "other", fault: "no authorization", wantKind: string(KindUnauthorizedDispatch)},
		{name: "missing original identity", directory: "other", fault: "no identity", wantKind: string(KindUnauthorizedDispatch)},
		{name: "inactive own claim", directory: "other", fault: "inactive", wantKind: string(KindUnauthorizedDispatch)},
		{name: "missing own claim", directory: "other", fault: "missing", wantKind: string(KindWorktreeOwnershipConflict)},
		{name: "unresolvable active claim", directory: "other", fault: "unreadable", wantKind: string(KindUnavailable)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := openTemp(t)
			own, other := seedWorkerCompletionProjectClaims(t, s, tc.fault != "no authorization", tc.fault != "no identity")
			directory := own
			switch tc.directory {
			case "other":
				directory = other
			case "alias":
				directory = filepath.Join(t.TempDir(), "alias")
				if err := os.Symlink(own, directory); err != nil {
					t.Fatal(err)
				}
			case "outside":
				directory = t.TempDir()
			}
			var faultSQL string
			switch tc.fault {
			case "inactive":
				faultSQL = `UPDATE worktree_entries SET state='reclaimed' WHERE project_id='project-1'`
			case "missing":
				faultSQL = `DELETE FROM worktree_entries WHERE project_id='project-1'`
			case "unreadable":
				if err := os.Remove(own); err != nil {
					t.Fatal(err)
				}
			}
			if faultSQL != "" {
				if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1); ` + faultSQL + `; DELETE FROM fold_guard`); err != nil {
					t.Fatal(err)
				}
			}
			tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			err = validateWorkerCompletionWorktree(context.Background(), tx, "work-completion", "attempt-completion", directory)
			if tc.wantKind == "" {
				if err != nil {
					t.Fatalf("own Project claim refused: %v", err)
				}
				return
			}
			var failure *Failure
			if !failureAs(err, &failure) || string(failure.Kind) != tc.wantKind {
				t.Fatalf("completion directory failure = %v, want %s", err, tc.wantKind)
			}
			for _, path := range []string{own, other, directory} {
				if strings.Contains(failure.Detail, path) {
					t.Fatalf("failure leaked a machine path: %q", failure.Detail)
				}
			}
			if tc.boundaries && (!strings.Contains(failure.Detail, "expected") || !strings.Contains(failure.Detail, "session boundary")) {
				t.Fatalf("failure omitted the worktree boundaries: %q", failure.Detail)
			}
		})
	}
}

func TestValidateWorkerCompletionWorktreePreservesStorageFailure(t *testing.T) {
	s := openTemp(t)
	own, _ := seedWorkerCompletionProjectClaims(t, s, true, true)
	tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	err = validateWorkerCompletionWorktree(context.Background(), tx, "work-completion", "attempt-completion", own)
	var failure *Failure
	if !failureAs(err, &failure) || failure.Kind != KindUnavailable {
		t.Fatalf("closed transaction failure = %v, want unavailable", err)
	}
}

func TestWorkerAttemptProjectRefusalNamesCaller(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, operation, recovery string
		completion                bool
	}{
		{name: "completion", operation: "worker_complete", recovery: "restore the attempt's recorded worktree claim before completion", completion: true},
		{name: "abandonment", operation: "worker_fail", recovery: "restore the attempt's recorded worktree claim before abandonment"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := openTemp(t)
			_, other := seedWorkerCompletionProjectClaims(t, s, true, true)
			if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1); DELETE FROM worktree_entries WHERE project_id='project-1'; DELETE FROM fold_guard`); err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if tc.completion {
				err = validateWorkerCompletionWorktree(ctx, tx, "work-completion", "attempt-completion", other)
			} else {
				err = validateNoLiveWorkerSession(ctx, tx, "work-completion", "attempt-completion")
			}
			var failure *Failure
			if !failureAs(err, &failure) || failure.Kind != KindWorktreeOwnershipConflict {
				t.Fatalf("missing recorded Project failure = %v, want worktree_ownership_conflict", err)
			}
			if failure.Op != tc.operation {
				t.Errorf("failure operation=%q, want %q", failure.Op, tc.operation)
			}
			if failure.RecoveryAction != tc.recovery {
				t.Errorf("failure recovery=%q, want %q", failure.RecoveryAction, tc.recovery)
			}
			if failure.Detail != "worker attempt worktree identity has no recorded Project" || failure.RetrySafe {
				t.Errorf("failure changed detail or retry flag: %+v", failure)
			}
		})
	}
}

func seedWorkerCompletionProjectClaims(t *testing.T, s *Store, withAuthorization, withIdentity bool) (string, string) {
	t.Helper()
	root := t.TempDir()
	own, other := filepath.Join(root, "own"), filepath.Join(root, "other")
	for _, path := range []string{own, other} {
		if err := os.Mkdir(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	insertWorkerWorktreeEntry(t, s, "work-completion", own)
	canonical, err := canonicalWorkerWorktreePath(own)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO worktree_entries(set_id,project_id,claim_op_id,branch,base_sha,path,repository_id,state,verified_at,git_facts)
SELECT set_id,'project-2','claim-2',branch,base_sha,?,'repo-2',state,verified_at,git_facts FROM worktree_entries WHERE set_id=? AND project_id='project-1'`, other, WorktreeSetID("work-completion")); err != nil {
		t.Fatal(err)
	}
	actions := []struct {
		id, kind string
		payload  map[string]any
	}{
		{id: "completion-window-started", kind: WorkflowActionStarted, payload: map[string]any{"step_id": "execution", "action_id": "dispatch_worker", "attempt_epoch": 1}},
		{id: "completion-window-finished", kind: WorkflowActionCompleted, payload: map[string]any{"step_id": "execution", "action_id": "dispatch_worker", "attempt_epoch": 1, "worker_attempt_id": "attempt-completion", "worker_worktree_identity": workerWorktreeIdentity(canonical)}},
	}
	if !withIdentity {
		delete(actions[1].payload, "worker_worktree_identity")
	}
	if !withAuthorization {
		actions = nil
	}
	for _, action := range actions {
		raw, err := json.Marshal(action.payload)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(`INSERT INTO domain_events(event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload) VALUES(?,?,'work_item','work-completion','actor:test','2026-09-05T00:00:00Z',?,?)`, action.id, action.kind, eventKindRegistry[action.kind].CurrentVersion, raw); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tx.Exec(`DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return own, other
}

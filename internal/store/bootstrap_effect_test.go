package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func bootstrapEffectFixture(t *testing.T) (*Store, string, BootstrapRequest) {
	t.Helper()
	repo := initBootstrapStoreRepo(t)
	s := openTemp(t)
	seedBootstrapStoreAuthority(t, s, repo)
	return s, repo, bootstrapStoreRequest()
}

func assertBootstrapRetainedEffect(t *testing.T, s *Store, req BootstrapRequest, got BootstrapResult) {
	t.Helper()
	var operationID, productID, projectID, workID, path string
	var version int64
	err := s.db.QueryRow(`SELECT b.operation_id,b.product_id,b.project_id,b.work_id,w.version,c.pinned_path FROM bootstrap_operations b JOIN work_items w ON w.id=b.work_id JOIN worktree_claims c ON c.op_id=b.operation_id WHERE b.idempotency_key=?`, req.IdempotencyKey).
		Scan(&operationID, &productID, &projectID, &workID, &version, &path)
	if err != nil {
		t.Fatal(err)
	}
	if got.EffectState != "partial" || got.OperationID != operationID || got.ProductID != productID || got.ProjectID != projectID || got.WorkID != workID || got.WorkVersion != version || got.Entry.Path != path {
		t.Fatalf("failure result=%+v; want partial with retained identity %s/%s/%s/%s version=%d path=%s", got, operationID, productID, projectID, workID, version, path)
	}
}

func TestBootstrapFailureRetainsDurableCaptureIdentity(t *testing.T) {
	for _, phase := range []string{"after_prepare", "after_native_create"} {
		t.Run(phase, func(t *testing.T) {
			s, _, req := bootstrapEffectFixture(t)
			cause := newFailure(KindResourceBusy, "phase", "injected interruption", true, "retry")
			got, err := s.BootstrapWorktree(t.Context(), req, func(current string) error {
				if current == phase {
					return cause
				}
				return nil
			})
			if err != cause || !errors.Is(err, cause) || failureKind(err) != KindResourceBusy {
				t.Fatalf("failure classification/cause changed: %v", err)
			}
			assertBootstrapRetainedEffect(t, s, req, got)
			_, statErr := os.Stat(got.Entry.Path)
			if phase == "after_prepare" && !os.IsNotExist(statErr) || phase == "after_native_create" && statErr != nil {
				t.Fatalf("native state after %s: %v", phase, statErr)
			}
			replay, err := s.BootstrapWorktree(t.Context(), req, nil)
			if err != nil || !replay.Replayed || replay.WorkID != got.WorkID || replay.OperationID != got.OperationID || replay.Entry.Path != got.Entry.Path || replay.EffectState != "" {
				t.Fatalf("exact replay=%+v err=%v", replay, err)
			}
		})
	}
}

type bootstrapEffectNativeFailureRunner struct {
	cause error
}

func (r bootstrapEffectNativeFailureRunner) Run(ctx context.Context, dir string, args ...string) ([]byte, error) {
	if len(args) >= 2 && args[0] == "worktree" && args[1] == "add" {
		return nil, r.cause
	}
	return (ExecGitRunner{}).Run(ctx, dir, args...)
}

func TestBootstrapNativeFailureAndEarlyReplayRetainIdentity(t *testing.T) {
	s, repo, req := bootstrapEffectFixture(t)
	cause := errors.New("injected native creation failure")
	got, err := s.bootstrapWorktree(t.Context(), req, nil, bootstrapEffectNativeFailureRunner{cause: cause})
	if !errors.Is(err, cause) || failureKind(err) != KindGitUnreachable {
		t.Fatalf("native failure=%v", err)
	}
	assertBootstrapRetainedEffect(t, s, req, got)
	runTestGit(t, repo, "switch", "-q", "-c", "not-default")
	replayed, err := s.BootstrapWorktree(t.Context(), req, nil)
	if failureKind(err) != KindInvalidOperation {
		t.Fatalf("early replay refusal=%v", err)
	}
	assertBootstrapRetainedEffect(t, s, req, replayed)
}

func TestBootstrapFailureReplayUsesOriginalRowIdentity(t *testing.T) {
	s, repo, original := bootstrapEffectFixture(t)
	original.Ref = "origin/main"
	stop := errors.New("stop after capture")
	_, err := s.BootstrapWorktree(t.Context(), original, func(string) error { return stop })
	if !errors.Is(err, stop) {
		t.Fatal(err)
	}
	intent := original
	intent.Ref = ""
	_, originalWork, _, _ := CanonicalBootstrapIdentity(original)
	_, callerWork, _, _ := CanonicalBootstrapIdentity(intent)
	if originalWork == callerWork {
		t.Fatal("fixture must distinguish stored identity from caller-derived identity")
	}
	runTestGit(t, repo, "switch", "-q", "-c", "not-default")
	got, err := s.BootstrapWorktree(t.Context(), intent, nil)
	if failureKind(err) != KindInvalidOperation {
		t.Fatalf("early original-intent replay refusal=%v", err)
	}
	assertBootstrapRetainedEffect(t, s, original, got)
	if got.WorkID != originalWork {
		t.Fatalf("replay invented caller identity: %+v", got)
	}
	intent.Task = "different capture"
	conflict, err := s.BootstrapWorktree(t.Context(), intent, nil)
	if failureKind(err) != KindInvalidOperation || conflict.EffectState != "none" || conflict.WorkID != "" || conflict.OperationID != "" || conflict.ProductID != "" || conflict.ProjectID != "" || conflict.Entry.Path != "" {
		t.Fatalf("different-digest conflict leaked original identity: %+v err=%v", conflict, err)
	}
}

func TestBootstrapFailureRollbackHasNoEffect(t *testing.T) {
	s, _, req := bootstrapEffectFixture(t)
	if _, err := s.db.Exec(`CREATE TRIGGER refuse_bootstrap_journal BEFORE INSERT ON bootstrap_operations BEGIN SELECT RAISE(ABORT, 'injected journal refusal'); END`); err != nil {
		t.Fatal(err)
	}
	got, err := s.BootstrapWorktree(t.Context(), req, nil)
	if err == nil || got.EffectState != "none" || got.WorkID != "" || got.OperationID != "" || got.Entry.Path != "" {
		t.Fatalf("rolled-back capture=%+v err=%v", got, err)
	}
	for _, table := range []string{"bootstrap_operations", "worktree_claims", "work_items"} {
		var count int
		if err := s.db.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("rollback left %s rows=%d err=%v", table, count, err)
		}
	}
}

func TestBootstrapFailureUnreadableStateIsPossible(t *testing.T) {
	s, _, req := bootstrapEffectFixture(t)
	cause := errors.New("stop with unreadable authority")
	got, err := s.BootstrapWorktree(t.Context(), req, func(string) error {
		if err := s.db.Close(); err != nil {
			t.Fatal(err)
		}
		return cause
	})
	if err != cause || got.EffectState != "possible" || got.WorkID != "" || got.OperationID != "" || got.Entry.Path != "" {
		t.Fatalf("unreadable retained state=%+v err=%v", got, err)
	}
}

func TestBootstrapFailureUncertainEffectWithoutJournalIsPossible(t *testing.T) {
	s, _, req := bootstrapEffectFixture(t)
	cause := newFailure(KindUnavailable, "durable_transaction", "injected unknown commit outcome", true, "reconcile")
	cause.EffectPossible = true
	got := s.bootstrapFailureResult(t.Context(), req, cause)
	if got.EffectState != "possible" || got.WorkID != "" || got.OperationID != "" || got.Entry.Path != "" {
		t.Fatalf("missing journal erased commit uncertainty: %+v", got)
	}
}

func TestBootstrapFailureCommittedStateResolvesPossibleEffect(t *testing.T) {
	s, _, req := bootstrapEffectFixture(t)
	cause := newFailure(KindUnavailable, "phase", "injected uncertain bootstrap effect", true, "reconcile")
	cause.EffectPossible = true
	got, err := s.BootstrapWorktree(t.Context(), req, func(string) error { return cause })
	if !errors.Is(err, cause) || !failureEffectPossible(err) {
		t.Fatalf("uncertain effect=%v", err)
	}
	assertBootstrapRetainedEffect(t, s, req, got)
	if _, err := os.Stat(filepath.Clean(got.Entry.Path)); !os.IsNotExist(err) {
		t.Fatalf("unacknowledged prepare ran native creation: %v", err)
	}
}

func TestBootstrapRolledBackFailureDoesNotOfferReclaimedPath(t *testing.T) {
	s, repo, req := bootstrapEffectFixture(t)
	created, err := s.BootstrapWorktree(t.Context(), req, nil)
	if err != nil {
		t.Fatal(err)
	}
	location := WorktreeLocation{Repo: repo, Path: created.Entry.Path, Branch: created.Entry.Branch, BaseSHA: created.Entry.BaseSHA}
	if err := s.rollbackBootstrap(t.Context(), created.OperationID, created.WorkID, location, ExecGitRunner{}, errors.New("synthetic rollback")); err != nil {
		t.Fatal(err)
	}
	got, err := s.BootstrapWorktree(t.Context(), req, nil)
	var failure *Failure
	if !errors.As(err, &failure) || failure.RetrySafe || got.EffectState != "partial" || got.WorkID != created.WorkID || got.OperationID != created.OperationID || got.Entry.Path != "" {
		t.Fatalf("rolled-back replay=%+v err=%v", got, err)
	}
	var lifecycle, claimState string
	if err := s.db.QueryRow(`SELECT w.lifecycle,c.state FROM work_items w JOIN worktree_claims c ON c.work_id=w.id WHERE c.op_id=?`, created.OperationID).Scan(&lifecycle, &claimState); err != nil || lifecycle != "cancelled" || claimState != "reclaimed" {
		t.Fatalf("retained rollback=%s/%s err=%v", lifecycle, claimState, err)
	}
}

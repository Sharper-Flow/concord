package store

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type bootstrapOriginProbe func(context.Context, string, ...string) ([]byte, error)

func (probe bootstrapOriginProbe) Run(ctx context.Context, dir string, args ...string) ([]byte, error) {
	return probe(ctx, dir, args...)
}

func bootstrapContentionFixture(t *testing.T) (*Store, *Store, BootstrapResult) {
	t.Helper()
	repo := initBootstrapStoreRepo(t)
	path := filepath.Join(t.TempDir(), "concord.db")
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	seedBootstrapStoreAuthority(t, s, repo)
	origin := liveBootstrapOrigin(t, s, "bootstrap-contention")
	other, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = other.Close() })
	// A held lock must fail deterministically instead of consuming the
	// production five-second wait. No assertion depends on elapsed time.
	for _, store := range []*Store{s, other} {
		if _, err := store.db.Exec("PRAGMA busy_timeout=50"); err != nil {
			t.Fatal(err)
		}
	}
	return s, other, origin
}

func TestBootstrapOriginValidationDoesNotTakeWriteLock(t *testing.T) {
	t.Parallel()
	s, other, origin := bootstrapContentionFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	writer, err := other.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = writer.Rollback() }()
	if _, err := s.ValidateBootstrapOrigin(ctx, origin.ProjectID, origin.Entry.Path, nil); err != nil {
		t.Fatalf("read-only origin validation queued behind a foreign writer: %v", err)
	}
}

func TestBootstrapOriginGitProbeHoldsNoDatabaseTransaction(t *testing.T) {
	for _, operation := range []string{"write", "durability", "same-store-read"} {
		t.Run(operation, func(t *testing.T) {
			t.Parallel()
			s, other, origin := bootstrapContentionFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if err := other.appendSeedEvent(ctx); err != nil {
				t.Fatal(err)
			}
			probe := bootstrapOriginProbe(func(_ context.Context, _ string, args ...string) ([]byte, error) {
				if strings.Join(args, " ") != "status --porcelain" {
					t.Errorf("unexpected origin probe: %v", args)
				}
				if operation == "write" {
					return nil, other.appendSeedEvent(ctx)
				}
				if operation == "same-store-read" {
					var count int
					return nil, s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM work_items").Scan(&count)
				}
				return nil, other.SyncDurable(ctx)
			})
			if _, err := s.ValidateBootstrapOrigin(ctx, origin.ProjectID, origin.Entry.Path, probe); err != nil {
				t.Fatalf("git probe blocked a concurrent %s: %v", operation, err)
			}
		})
	}
}

func TestBootstrapOriginValidationReadsLeaseAfterGitProbe(t *testing.T) {
	t.Parallel()
	s, other, origin := bootstrapContentionFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	probe := bootstrapOriginProbe(func(context.Context, string, ...string) ([]byte, error) {
		_, err := other.db.ExecContext(ctx, `INSERT INTO worktree_verify_leases(lease_id,work_id,project_id,path,state,client_ref,agent_ref,session_ref,principal_ref,command_json,acquired_at,outcome) VALUES('lease-during-probe',?,?,?,'held','client-a','agent-a','session-a','principal-a','["true"]','2026-01-01T00:00:00Z','running')`, origin.WorkID, origin.ProjectID, origin.Entry.Path)
		return nil, err
	})
	_, err := s.ValidateBootstrapOrigin(ctx, origin.ProjectID, origin.Entry.Path, probe)
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindResourceBusy || !strings.Contains(failure.Detail, "verify lease") {
		t.Fatalf("lease acquired during git probe was not observed: %v", err)
	}
}

// The origin identity observed before the probe must still name the same
// active claim afterwards. Native probing grants no authority to a new row.
func TestBootstrapOriginValidationRefusesClaimDriftDuringGitProbe(t *testing.T) {
	t.Parallel()
	s, other, origin := bootstrapContentionFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	probe := bootstrapOriginProbe(func(context.Context, string, ...string) ([]byte, error) {
		err := other.Transact(ctx, func(transaction *Transaction) error {
			tx, err := transactionSQL(transaction, "bootstrap_probe_test")
			if err != nil {
				return err
			}
			if err := enterFold(ctx, tx); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE worktree_entries SET branch='work/replaced-claim' WHERE path=?`, origin.Entry.Path); err != nil {
				return err
			}
			return leaveFold(ctx, tx)
		})
		return nil, err
	})
	_, err := s.ValidateBootstrapOrigin(ctx, origin.ProjectID, origin.Entry.Path, probe)
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindProjectionConflict {
		t.Fatalf("changed origin identity was not refused: %v", err)
	}
}

var _ GitRunner = bootstrapOriginProbe(nil)

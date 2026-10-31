package main

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
	"github.com/sharper-flow/concord/internal/store/storetest/neighbor"
)

// TestSQLiteNeighborProcess is the re-exec entry the neighbor fixture's Start
// targets in this package's own test binary. The fixture returns immediately
// outside a neighbor role, so a direct run of this package is unaffected.
func TestSQLiteNeighborProcess(t *testing.T) { neighbor.Child(t, store.SQLiteFileURI) }

// cliNeighborFixture is one row's prepared state: the database path the
// neighbor pins, an optional reader-mode fast-fail pragma, the command run
// under the neighbor, and the committed post-state check.
type cliNeighborFixture struct {
	dbPath  string
	tighten func(t *testing.T)
	run     func() error
	verify  func()
}

type cliNeighborOperation struct {
	name    string
	prepare func(t *testing.T) cliNeighborFixture
}

// TestCLIWritersWithNeighborProcess runs the CLI's durable write surfaces
// against a real neighbor process that holds a WAL read snapshot (reader) or
// the write lock (writer) in a separate OS process. Each row prepares before
// the neighbor starts and verifies committed identities and results after the
// command returns: CD-0050 commits sync the write-ahead log themselves, so
// neither neighbor can turn a committed write into a failure. A writer row
// waits out the bounded hold inside the production busy timeout, and a reader
// row must not wait at all.
func TestCLIWritersWithNeighborProcess(t *testing.T) {
	for _, mode := range []neighbor.Mode{neighbor.Reader, neighbor.Writer} {
		for _, row := range cliNeighborOperations() {
			t.Run(string(mode)+"/"+row.name, func(t *testing.T) {
				fixture := row.prepare(t)
				if mode == neighbor.Reader && fixture.tighten != nil {
					// A TRUNCATE-checkpoint regression must fail fast rather
					// than pin this row for the production busy timeout.
					fixture.tighten(t)
				}
				neighbor.Start(t, fixture.dbPath, mode)
				if err := fixture.run(); err != nil {
					t.Fatalf("%s under %s neighbor: %v", row.name, mode, err)
				}
				fixture.verify()
			})
		}
	}
}

func cliNeighborOperations() []cliNeighborOperation {
	ctx := context.Background()
	rows := []cliNeighborOperation{
		{"session-prepare", prepareSessionPrepareNeighborRow(ctx)},
	}
	return rows
}

// prepareSessionPrepareNeighborRow checks that session-prepare reads its
// claimed worktree under either neighbor without recording identity receipts.
func prepareSessionPrepareNeighborRow(ctx context.Context) func(t *testing.T) cliNeighborFixture {
	return func(t *testing.T) cliNeighborFixture {
		repo := initLocatorRepo(t)
		dbPath := filepath.Join(t.TempDir(), "concord.db")
		s := mustOpenStore(t, dbPath)
		seedLocatorAuthority(t, s, repo)
		result := seedCLISessionWorktree(t, s, repo)
		t.Setenv(dbOverrideEnv, dbPath)
		t.Chdir(result.Entry.Path)
		var before int
		if err := s.DatabaseForTesting().QueryRowContext(ctx, "SELECT count(*) FROM effect_receipts").Scan(&before); err != nil {
			t.Fatal(err)
		}
		run := func() error {
			identityCalls := 0
			var out, errOut bytes.Buffer
			code := runSessionPrepare(commandSessionPrepareInput(t, result.WorkID, "run"), s, &out, &errOut,
				hostCommandAt(defaultHostResolution()),
				func(context.Context, string, hostCommandResolution, string) (string, error) {
					identityCalls++
					return "concord-1", nil
				},
				func(context.Context, string, string, string, string) ([]byte, error) {
					return []byte(`{"watermark":"test"}`), nil
				})
			if code != 0 || identityCalls != 1 {
				return fmt.Errorf("exit=%d identity calls=%d stdout=%q stderr=%q", code, identityCalls, out.String(), errOut.String())
			}
			return nil
		}
		verify := func() {
			var total int
			if err := s.DatabaseForTesting().QueryRowContext(ctx, "SELECT count(*) FROM effect_receipts").Scan(&total); err != nil {
				t.Fatal(err)
			}
			if total != before {
				t.Fatalf("events=%d want %d; session-prepare must not record identity receipts", total, before)
			}
		}
		return cliNeighborFixture{
			dbPath: dbPath,
			tighten: func(t *testing.T) {
				if _, err := s.DatabaseForTesting().ExecContext(ctx, "PRAGMA busy_timeout=1"); err != nil {
					t.Fatal(err)
				}
			},
			run:    run,
			verify: verify,
		}
	}
}

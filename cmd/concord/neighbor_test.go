package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
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
	rows = append(rows, cliNeighborOperation{"predecessor-import", preparePredecessorImportNeighborRow(ctx)})
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
				func(context.Context, string, string, string) ([]byte, error) {
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

// preparePredecessorImportNeighborRow mirrors
// TestPredecessorImportAcknowledgesUnderPinnedReader and extends it with the
// committed-result check: the acknowledged report counts and the imported
// rows must exist once the command returns.
func preparePredecessorImportNeighborRow(ctx context.Context) func(t *testing.T) cliNeighborFixture {
	return func(t *testing.T) cliNeighborFixture {
		dbPath := freshMigratedCLIDatabase(t)
		snapshotPath := writeSyntheticSnapshot(t)
		payload := predecessorImportRequest(t, snapshotPath)
		run := func() error {
			code, raw, diag := runPredecessorImportRequest(t, dbPath, payload)
			if code != 0 {
				return fmt.Errorf("exit=%d stderr=%q", code, diag)
			}
			var report struct {
				DryRun          bool `json:"dry_run"`
				ProductsCreated int  `json:"products_created"`
				ProjectsCreated int  `json:"projects_created"`
				WorkImported    int  `json:"work_imported"`
				AlreadyImported int  `json:"already_imported"`
			}
			if err := json.Unmarshal(raw, &report); err != nil {
				return fmt.Errorf("report is not parseable JSON: %v; raw=%s", err, raw)
			}
			if report.DryRun || report.ProductsCreated != 1 || report.ProjectsCreated != 2 || report.WorkImported != 2 || report.AlreadyImported != 0 {
				return fmt.Errorf("report counts = products:%d projects:%d work:%d already:%d, want 1/2/2/0", report.ProductsCreated, report.ProjectsCreated, report.WorkImported, report.AlreadyImported)
			}
			return nil
		}
		verify := func() {
			s := openFreshImportStore(t, dbPath)
			defer s.Close()
			var productCount, projectCount, workCount int
			if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT count(*) FROM products WHERE id='synth-product'`).Scan(&productCount); err != nil {
				t.Fatal(err)
			}
			if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT count(*) FROM projects WHERE id IN ('synth-project-alpha','synth-project-beta')`).Scan(&projectCount); err != nil {
				t.Fatal(err)
			}
			if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT count(*) FROM work_items WHERE id LIKE 'import-advance-work-%'`).Scan(&workCount); err != nil {
				t.Fatal(err)
			}
			if productCount != 1 || projectCount != 2 || workCount != 2 {
				t.Fatalf("committed rows = products:%d projects:%d work:%d, want 1/2/2", productCount, projectCount, workCount)
			}
			assertCommittedEventOnce(t, s, "import-advance-work-synth-change-alpha-1")
		}
		return cliNeighborFixture{dbPath: dbPath, run: run, verify: verify}
	}
}

// assertCommittedEventOnce proves the identity the command acknowledged is
// exactly one committed receipt with a unique effect position and its owning
// current identity.
func assertCommittedEventOnce(t *testing.T, s *store.Store, eventID string) {
	t.Helper()
	var committed int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM effect_receipts WHERE event_id=?`, eventID).Scan(&committed); err != nil {
		t.Fatal(err)
	}
	if committed != 1 {
		t.Fatalf("event %q committed %d times, want 1", eventID, committed)
	}
	var kind, subjectType, subjectID, actor, digest string
	var seq int64
	var version int
	if err := s.DatabaseForTesting().QueryRow(`SELECT kind,subject_type,subject_id,actor,payload_version,payload_digest,effect_seq FROM effect_receipts WHERE event_id=?`, eventID).Scan(&kind, &subjectType, &subjectID, &actor, &version, &digest, &seq); err != nil {
		t.Fatal(err)
	}
	if kind == "" || subjectID == "" || actor == "" || version <= 0 || seq <= 0 || len(digest) != 71 || !strings.HasPrefix(digest, "sha256:") {
		t.Fatalf("incomplete committed receipt %q: %s/%s/%s actor=%q version=%d digest=%q seq=%d", eventID, kind, subjectType, subjectID, actor, version, digest, seq)
	}
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM effect_receipts WHERE effect_seq=?`, seq).Scan(&committed); err != nil || committed != 1 {
		t.Fatalf("effect position %d is not unique: count=%d err=%v", seq, committed, err)
	}
	switch kind {
	case "work.created":
		if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM work_items WHERE id=?`, subjectID).Scan(&committed); err != nil || committed != 1 || subjectType != "work_item" {
			t.Fatalf("creation receipt has no current work: count=%d err=%v", committed, err)
		}
	default:
		t.Fatalf("unhandled committed identity kind %q", kind)
	}
}

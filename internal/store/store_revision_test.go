package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func revisionValue(t *testing.T, q queryer) int64 {
	t.Helper()
	var value int64
	if err := q.QueryRowContext(context.Background(), `SELECT revision FROM store_revision WHERE id=1`).Scan(&value); err != nil {
		t.Fatal(err)
	}
	return value
}

func TestStoreRevisionWriterClasses(t *testing.T) {
	t.Parallel()
	for _, class := range []string{"ordinary callback", "durable callback", "raw ordinary landing", "multi-event operation", "bypass assertion"} {
		t.Run(class, func(t *testing.T) {
			t.Parallel()
			s := openTemp(t)
			ctx := context.Background()
			before := revisionValue(t, s.db)
			var err error
			switch class {
			case "ordinary callback":
				err = s.Transact(ctx, func(tx *Transaction) error {
					_, err := tx.tx.ExecContext(ctx, `INSERT INTO agent_installation_keys VALUES('revision-fixture',zeroblob(32),'now')`)
					return err
				})
			case "durable callback":
				err = s.TransactDurable(ctx, func(tx *Transaction) error {
					_, err := tx.tx.ExecContext(ctx, `INSERT INTO agent_installation_keys VALUES('revision-fixture',zeroblob(32),'now')`)
					return err
				})
			case "raw ordinary landing":
				_, err = recordLanding(s, ctx, "revision_landing", "work-fixture", "session-fixture", "/fixture", 1, func(ctx context.Context, tx *sql.Tx) (string, error) {
					_, err := tx.ExecContext(ctx, `INSERT INTO agent_installation_keys VALUES('revision-fixture',zeroblob(32),'now')`)
					return "landed", err
				})
			case "multi-event operation":
				err = ApplyOperation(ctx, s, Operation{
					Events: []Event{
						productCreatedEvent("revision-product", "revision-product-created"),
						projectCreatedEvent("revision-project", "revision-project-created"),
						operationEvent("revision-membership", "product_project.added", SubjectProduct, "revision-product", map[string]any{
							"product_id": "revision-product", "project_id": "revision-project", "role": "primary", "reason": "fixture", "expected_version": 1, "resulting_version": 2,
						}),
					},
					ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProduct, "revision-product"): 0, VersionRef(SubjectProject, "revision-project"): 0},
				})
			case "bypass assertion":
				_, err = s.RecordOrchestratorIdentityAssertion(ctx, "revision-assertion", time.Now().UTC(), OrchestratorIdentityAssertion{
					Type: "concord", Version: "1", ProductID: "revision-product", RulesetDigest: "sha256:" + strings.Repeat("a", 64),
					Sources:      []OrchestratorArtifactSource{{Kind: "orchestrator_definition", Path: "/fixture/orchestrator.md", SHA256: strings.Repeat("b", 64)}},
					PrincipalRef: "principal-fixture", ClientRef: "client-fixture", AgentRef: "agent-fixture", SessionRef: "session-fixture",
				})
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := revisionValue(t, s.db); got != before+1 {
				t.Fatalf("committed revision=%d, want %d", got, before+1)
			}
		})
	}
}

func TestStoreRevisionRollbackAndReadSnapshot(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	ctx := context.Background()
	before := revisionValue(t, s.db)
	for _, durable := range []bool{false, true} {
		tx, err := s.beginStoreWriteTx(ctx, durable)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO agent_installation_keys VALUES('rolled-back',zeroblob(32),'now')`); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
		if err := tx.Rollback(); err != nil {
			t.Fatal(err)
		}
		if got := revisionValue(t, s.db); got != before {
			t.Fatalf("rolled-back revision=%d, want %d", got, before)
		}
	}
	if err := s.readSnapshot(ctx, func(q queryer) error {
		if got := revisionValue(t, q); got != before {
			t.Errorf("read snapshot revision=%d, want %d", got, before)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if got := revisionValue(t, s.db); got != before {
		t.Fatalf("revision after reads=%d, want %d", got, before)
	}
}

func TestStoreRevisionInstallationKey(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	ctx := context.Background()
	if _, err := s.db.Exec(`DELETE FROM agent_installation_keys WHERE key_name='cursor'`); err != nil {
		t.Fatal(err)
	}
	before := revisionValue(t, s.db)
	if err := ensureInstallationKey(ctx, s.db); err != nil {
		t.Fatal(err)
	}
	if got := revisionValue(t, s.db); got != before+1 {
		t.Fatalf("key creation revision=%d, want %d", got, before+1)
	}
	if err := ensureInstallationKey(ctx, s.db); err != nil {
		t.Fatal(err)
	}
	if got := revisionValue(t, s.db); got != before+1 {
		t.Fatalf("existing key opened a write: revision=%d, want %d", got, before+1)
	}
}

func TestStoreRevisionReopenUnchanged(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	path := s.Path()
	before := revisionValue(t, s.db)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		reopened, err := Open(t.Context(), path)
		if err != nil {
			t.Fatal(err)
		}
		got := revisionValue(t, reopened.db)
		if err := reopened.Close(); err != nil {
			t.Fatal(err)
		}
		if got != before {
			t.Fatalf("unchanged reopen revision=%d, want %d", got, before)
		}
	}
}

func TestStoreRevisionIgnoredKeyInsert(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	if _, err := s.db.Exec(`DELETE FROM agent_installation_keys WHERE key_name='cursor';
		CREATE TRIGGER ignore_cursor_insert BEFORE INSERT ON agent_installation_keys
		WHEN NEW.key_name='cursor' BEGIN SELECT RAISE(IGNORE); END;`); err != nil {
		t.Fatal(err)
	}
	before := revisionValue(t, s.db)
	if err := ensureInstallationKey(t.Context(), s.db); err != nil {
		t.Fatal(err)
	}
	if got := revisionValue(t, s.db); got != before {
		t.Fatalf("ignored key insert revision=%d, want unchanged %d", got, before)
	}
}

func TestStoreRevisionReadTransactionLifecycle(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	before := revisionValue(t, s.db)
	for _, fail := range []bool{false, true} {
		var retained *Transaction
		stopped := errors.New("stop read snapshot")
		err := s.ReadTransaction(t.Context(), func(tx *Transaction) error {
			retained = tx
			if got := revisionValue(t, tx.tx); got != before {
				t.Fatalf("read revision=%d, want %d", got, before)
			}
			if fail {
				return stopped
			}
			return nil
		})
		if fail && !errors.Is(err, stopped) || !fail && err != nil {
			t.Fatalf("read failure=%v, err=%v", fail, err)
		}
		if retained == nil || retained.tx != nil {
			t.Fatal("read transaction escaped its callback")
		}
		if got := revisionValue(t, s.db); got != before {
			t.Fatalf("read advanced revision to %d, want %d", got, before)
		}
	}
}

func TestStoreRevisionReadMetadataWithoutLog(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	ctx := context.Background()
	if _, err := s.db.Exec(`UPDATE store_revision SET revision=87 WHERE id=1; ALTER TABLE domain_events RENAME TO domain_events_hidden`); err != nil {
		t.Fatal(err)
	}
	tx, err := beginRead(ctx, s, "revision_fixture")
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	meta, err := queryMeta(ctx, tx, "revision_fixture", ResolvedScope{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if meta.SourceVersionWatermark != 87 {
		t.Fatalf("published revision=%d, want 87", meta.SourceVersionWatermark)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if got, err := backupEventInventory(ctx, s.db); err != nil || got != 87 {
		t.Fatalf("backup inventory=%d, err=%v, want 87", got, err)
	}
	if got, err := s.Revision(ctx); err != nil || got != 87 {
		t.Fatalf("cursor source=%d, err=%v, want 87 without the log", got, err)
	}
}

func TestStoreRevisionMigration(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, err := sql.Open(driverName, dataSourceName(filepath.Join(t.TempDir(), "before-revision.db")))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()
	if _, err := db.ExecContext(ctx, schemaManifestDDL); err != nil {
		t.Fatal(err)
	}
	for _, m := range migrations {
		if m.Version >= 138 {
			break
		}
		if err := applyMigration(ctx, db, m); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO schema_migrations(version,name,checksum,applied_at,breaking) VALUES(?,?,?,'now',?)`, m.Version, m.Name, m.checksum(), m.Breaking); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO domain_events(seq,event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload) VALUES(41,'old-event','product.created','product','old-product','operator','2026-10-01T00:00:00Z',1,'{}')`); err != nil {
		t.Fatal(err)
	}
	if got, err := backupEventInventory(ctx, db); err != nil || got != 0 {
		t.Fatalf("pre-revision backup inventory=%d, err=%v, want zero", got, err)
	}
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	if got := revisionValue(t, db); got != 42 {
		t.Fatalf("migration revision=%d, want old watermark 41 plus one", got)
	}
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	if got := revisionValue(t, db); got != 42 {
		t.Fatalf("repeated migration revision=%d, want 42", got)
	}
}

func TestStoreRevisionDigestExcludesCounterNotSchema(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	ctx := context.Background()
	before, err := backupCurrentStateDigest(ctx, s.db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE store_revision SET revision=revision+1 WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	after, err := backupCurrentStateDigest(ctx, s.db)
	if err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatal("publication counter changed Product-state digest")
	}
	if _, err := s.db.Exec(`ALTER TABLE store_revision ADD COLUMN unexpected TEXT`); err != nil {
		t.Fatal(err)
	}
	changed, err := backupCurrentStateDigest(ctx, s.db)
	if err != nil {
		t.Fatal(err)
	}
	if changed == after {
		t.Fatal("revision schema change escaped digest")
	}
}

func TestStoreRevisionAdmissionFailsClosed(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	ctx := context.Background()
	if _, err := s.db.Exec(`DELETE FROM store_revision`); err != nil {
		t.Fatal(err)
	}
	for _, durable := range []bool{false, true} {
		tx, err := s.beginStoreWriteTx(ctx, durable)
		if err == nil {
			_ = tx.Rollback()
			t.Fatal("write admitted without the singleton revision")
		}
		if !errors.As(err, new(*Failure)) {
			t.Fatalf("admission failure=%v, want typed store failure", err)
		}
		if got := readSynchronous(t, ctx, s.db); got != 1 {
			t.Fatalf("failed admission retained synchronous=%d, want NORMAL", got)
		}
	}
}

func TestStoreRevisionConstraints(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	for _, statement := range []string{
		`INSERT INTO store_revision(id,revision) VALUES(2,0)`,
		`UPDATE store_revision SET revision=-1 WHERE id=1`,
		`UPDATE store_revision SET revision=0.5 WHERE id=1`,
	} {
		if _, err := s.db.Exec(statement); err == nil {
			t.Fatalf("invalid singleton revision accepted: %s", statement)
		}
	}
	if _, err := s.db.Exec(`UPDATE store_revision SET revision=9223372036854775807 WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	if err := s.Transact(context.Background(), func(*Transaction) error { return nil }); err == nil {
		t.Fatal("revision overflow admitted a write")
	}
	if got := revisionValue(t, s.db); got != 9223372036854775807 {
		t.Fatalf("overflow changed revision to %d", got)
	}
}

func TestStoreRevisionBackupRestoreInventory(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	ctx := context.Background()
	if err := s.Transact(ctx, func(*Transaction) error { return nil }); err != nil {
		t.Fatal(err)
	}
	want := revisionValue(t, s.db)
	snapshot := filepath.Join(t.TempDir(), "revision-backup.db")
	manifest, err := Backup(ctx, s, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.EventWatermark != want {
		t.Fatalf("backup inventory=%d, want store revision %d", manifest.EventWatermark, want)
	}
	restoredPath := filepath.Join(t.TempDir(), "restored.db")
	report, err := RestoreBackup(ctx, snapshot, restoredPath)
	if err != nil {
		t.Fatal(err)
	}
	if report.SourceSnapshot.EventWatermark != want || report.RestoredDatabase.EventWatermark != want || report.RestoredDatabase.SchemaVersion != CurrentSchemaVersion() {
		t.Fatalf("restore report=%+v, want inventory %d at schema %d", report, want, CurrentSchemaVersion())
	}
	if got, err := storeRevisionAtPath(ctx, restoredPath); err != nil || got != want {
		t.Fatalf("restored revision=%d, err=%v, want %d", got, err, want)
	}
}

package store

import (
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func remoteUpgradeFixture(t *testing.T, locators ...ProjectLocator) *Store {
	t.Helper()
	index := len(migrations)
	for i, m := range migrations {
		if m.Name == "project_locator_remote_identity_v2" {
			index = i
		}
	}
	path := filepath.Join(t.TempDir(), "remote-upgrade.db")
	s := &Store{db: openMigratedTo(t, path, index), path: path}
	events := []Event{locatorProductEvent("product-a"), locatorProjectEvent("project-a"), locatorMembershipEvent("product-a", "project-a")}
	for i, l := range locators {
		normalized, err := normalizeRemoteVersion(l.Value, 1)
		if l.Kind == LocatorCanonicalPath {
			normalized, err = normalizePath(l.Value)
		}
		if err != nil {
			t.Fatal(err)
		}
		p := projectLocatorPayload{ProjectID: "project-a", LocatorID: l.ID, Kind: l.Kind, Value: l.Value, NormalizedValue: normalized, ExpectedVersion: int64(i + 1), ResultingVersion: int64(i + 2)}
		raw, _ := json.Marshal(p)
		events = append(events, Event{EventID: "historical-" + l.ID, Kind: "project.locator_added", SubjectType: SubjectProject, SubjectID: "project-a", Actor: "operator", OccurredAt: time.Unix(int64(i+2), 0).UTC(), PayloadVersion: 1, Payload: raw})
	}
	for _, e := range events {
		insertHistoricalEvent(t, s, e)
	}
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	scope, err := beginFold(ctx, tx)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range events {
		if e.Kind != "project.locator_added" {
			if err := foldRegisteredEvent(ctx, tx, e, scope); err != nil {
				t.Fatal(err)
			}
			continue
		}
		var p projectLocatorPayload
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			t.Fatal(err)
		}
		stamp := e.OccurredAt.Format(time.RFC3339Nano)
		if _, err := tx.ExecContext(ctx, `INSERT INTO project_locators(locator_id,project_id,kind,locator_value,normalized_value,created_at,updated_at) VALUES(?,?,?,?,?,?,?)`, p.LocatorID, p.ProjectID, p.Kind, p.Value, p.NormalizedValue, stamp, stamp); err != nil {
			t.Fatal(err)
		}
		if err := bumpVersion(ctx, tx, "projects", e, p.ExpectedVersion, p.ResultingVersion, "Project"); err != nil {
			t.Fatal(err)
		}
	}
	if err := scope.close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return s
}

func remoteUpgradeSnapshot(t *testing.T, s *Store) string {
	t.Helper()
	var b strings.Builder
	for _, table := range []string{"schema_migrations", "domain_events", "projects", "project_locators"} {
		b.WriteString(readTableSQL(t, context.Background(), s.db, table))
		b.WriteString(cutoverTableSnapshot(t, s, table))
	}
	var active int
	if err := s.db.QueryRow(`SELECT count(*) FROM fold_guard`).Scan(&active); err != nil {
		t.Fatal(err)
	}
	fmt.Fprint(&b, active)
	return b.String()
}

func TestRemoteLocatorUpgradeRecordedCutover(t *testing.T) {
	useStampedBuild(t)
	ctx := context.Background()
	root := t.TempDir()
	s := remoteUpgradeFixture(t,
		ProjectLocator{ID: "https", Kind: LocatorGitRemote, Value: "https://github.com/o/r"},
		ProjectLocator{ID: "scp", Kind: LocatorGitRemote, Value: "git@github.com:o/r"},
		ProjectLocator{ID: "file", Kind: LocatorGitRemote, Value: "file:///srv/o/r"},
		ProjectLocator{ID: "ftp", Kind: LocatorGitRemote, Value: "ftp://user:pw@host/o/r"},
		ProjectLocator{ID: "path", Kind: LocatorCanonicalPath, Value: root})
	old := histEventRows(t, s)
	if err := Migrate(ctx, s.db); err != nil {
		t.Fatal(err)
	}
	rows, err := s.ProjectLocators(ctx, "project-a")
	if err != nil || len(rows) != 3 {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
	want := map[string]string{"https": "https://github.com/o/r", "scp": "ssh://git@github.com/o/r", "path": root}
	for _, l := range rows {
		if expected, ok := want[l.ID]; !ok || l.NormalizedValue != expected {
			t.Fatalf("wrong identity: %+v", l)
		}
	}
	after := histEventRows(t, s)
	if len(after) != len(old)+4 || !reflect.DeepEqual(old, after[:len(old)]) {
		t.Fatal("corrections missing or old journal changed")
	}
	for i, event := range after[len(old):] {
		var p projectLocatorPayload
		if err := json.Unmarshal([]byte(event.Payload), &p); err != nil {
			t.Fatal(err)
		}
		if p.ExpectedVersion != int64(6+i) || p.ResultingVersion != int64(7+i) {
			t.Fatalf("nonconsecutive correction versions: %+v", p)
		}
		if i < 2 {
			if event.Kind != "project.locator_removed" || event.Version != 1 || p.Value != "" || p.NormalizedValue != "" || p.Kind != "" {
				t.Fatalf("removal copied remote data or wrong event: %+v", event)
			}
		} else if event.Kind != "project.locator_updated" || event.Version != 2 {
			t.Fatalf("wrong update event: %+v", event)
		}
	}
	var marked int
	if err := s.db.QueryRow(`SELECT count(*) FROM project_locators WHERE kind='git_remote' AND normalization_version=2`).Scan(&marked); err != nil || marked != 2 {
		t.Fatalf("marked=%d err=%v", marked, err)
	}
	snapshot := remoteUpgradeSnapshot(t, s)
	for range 2 {
		if err := Migrate(ctx, s.db); err != nil {
			t.Fatal(err)
		}
		if err := RebuildFromLog(ctx, s); err != nil {
			t.Fatal(err)
		}
		if got := remoteUpgradeSnapshot(t, s); got != snapshot {
			t.Fatal("retry/rebuild changed upgraded state or journal")
		}
	}
	for _, raw := range []string{"https://github.com/o/r", "file:///srv/o/r"} {
		host, err := ResolveProjectHost(ctx, root, root, &locatorGitStub{root: root, remote: raw})
		if err != nil {
			t.Fatal(err)
		}
		resolved, err := s.MatchResolvedProjectHost(ctx, host)
		if err != nil || resolved.ProjectID != "project-a" {
			t.Fatalf("resolved=%+v err=%v", resolved, err)
		}
	}
}

func TestRemoteLocatorUpgradeLastLocatorWritesNothing(t *testing.T) {
	for _, count := range []int{1, 2} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			locators := []ProjectLocator{{ID: "file", Kind: LocatorGitRemote, Value: "file:///srv/o/r"}, {ID: "ftp", Kind: LocatorGitRemote, Value: "ftp://host/o/r"}}
			s := remoteUpgradeFixture(t, locators[:count]...)
			before := remoteUpgradeSnapshot(t, s)
			err := Migrate(context.Background(), s.db)
			assertFailureKind(t, err, KindProjectionConflict)
			if !strings.Contains(err.Error(), "project-a") || remoteUpgradeSnapshot(t, s) != before {
				t.Fatalf("last-locator refusal lost identity or wrote state: %v", err)
			}
		})
	}
}

func TestRemoteLocatorUpgradeSessionFence(t *testing.T) {
	useStampedBuild(t)
	s := remoteUpgradeFixture(t, ProjectLocator{ID: "https", Kind: LocatorGitRemote, Value: "https://host/o/r"})
	before := remoteUpgradeSnapshot(t, s)
	_, err := Upgrade(context.Background(), s.path, []HeldSchema{{PID: 1, SchemaVersion: CurrentSchemaVersion() - 1}})
	assertFailureKind(t, err, KindUpgradeBlocked)
	if before != remoteUpgradeSnapshot(t, s) {
		t.Fatal("held older session refusal wrote state")
	}
}

func TestRemoteLocatorUpgradePreflightPrecedesWrites(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "schema.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var guard, firstWrite token.Pos
	for _, d := range f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "migrateOnce" {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "preflightRemoteLocatorUpgrade" {
				guard = call.Pos()
			}
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "ExecContext" && firstWrite == 0 {
				firstWrite = call.Pos()
			}
			return true
		})
	}
	if guard == 0 || firstWrite == 0 || guard >= firstWrite {
		t.Fatal("last-locator preflight must precede migration DDL/DML, not merely its commit")
	}
}

func TestRemoteLocatorUpgradeRefusalsRollback(t *testing.T) {
	for _, mode := range []string{"collision", "credential", "reference", "mid-append"} {
		t.Run(mode, func(t *testing.T) {
			locators := []ProjectLocator{{ID: "a", Kind: LocatorGitRemote, Value: "https://host/o/r"}, {ID: "b", Kind: LocatorGitRemote, Value: "file:///srv/o/r"}, {ID: "path", Kind: LocatorCanonicalPath, Value: t.TempDir()}}
			switch mode {
			case "collision":
				locators[0].Value, locators[1].Value = "git@host:o/r", "ssh://git@host/o/r"
			case "credential":
				locators[0].Value = "https://TOKEN@host/o/r"
			}
			s := remoteUpgradeFixture(t, locators...)
			if mode == "reference" {
				if _, err := s.db.Exec(`INSERT INTO fold_guard(active) VALUES(1); INSERT INTO law_subjects(home_project_id,home_locator_id,law_id,kind,status,path,title,content_hash,scanned_commit_oid) VALUES('project-a','b','CD-0001','decision','accepted','.concord/docs/decisions/CD-0001-a.md','A','sha256:` + strings.Repeat("a", 64) + `','commit'); DELETE FROM fold_guard`); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "mid-append" {
				if _, err := s.db.Exec(`CREATE TRIGGER fail_remote_update BEFORE INSERT ON domain_events WHEN NEW.kind='project.locator_updated' BEGIN SELECT RAISE(ABORT,'injected remote update failure'); END`); err != nil {
					t.Fatal(err)
				}
			}
			before := remoteUpgradeSnapshot(t, s)
			err := Migrate(context.Background(), s.db)
			if err == nil {
				t.Fatal("unsafe upgrade succeeded")
			}
			wantKind := map[string]FailureKind{"collision": KindMembershipConflict, "credential": KindInvalidFilter, "reference": KindProjectionConflict, "mid-append": KindUnavailable}[mode]
			assertFailureKind(t, err, wantKind)
			if remoteUpgradeSnapshot(t, s) != before {
				t.Fatal("refused upgrade changed schema/journal/locators/versions")
			}
			if mode == "mid-append" {
				if _, err := s.db.Exec(`DROP TRIGGER fail_remote_update`); err != nil {
					t.Fatal(err)
				}
				if err := Migrate(context.Background(), s.db); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestRemoteLocatorUpgradeKeyOrderingAndLaterDDL(t *testing.T) {
	original := migrations
	t.Cleanup(func() { migrations = original })
	migrations = append(append([]migration(nil), migrations...), migration{Version: CurrentSchemaVersion() + 1, Name: "remote_cutover_order_witness", SQL: `CREATE TABLE remote_cutover_witness(n INTEGER CHECK(n=0)); INSERT INTO remote_cutover_witness SELECT count(*) FROM domain_events WHERE event_id LIKE 'project_locator_remote_identity_v2:%';`})
	s := remoteUpgradeFixture(t,
		ProjectLocator{ID: "a-dependent", Kind: LocatorGitRemote, Value: "ssh://https/host/o/r"},
		ProjectLocator{ID: "z-occupant", Kind: LocatorGitRemote, Value: "https://host/o/r"})
	if err := Migrate(context.Background(), s.db); err != nil {
		t.Fatal(err)
	}
	rows, err := s.ProjectLocators(context.Background(), "project-a")
	if err != nil || len(rows) != 2 {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
	for _, l := range rows {
		if l.ID == "a-dependent" && l.NormalizedValue != "ssh://https/host/o/r" || l.ID == "z-occupant" && l.NormalizedValue != "https://host/o/r" {
			t.Fatalf("wrong ordered update: %+v", l)
		}
	}
}

func TestRemoteLocatorUpgradeHistoricalTransientCollision(t *testing.T) {
	s := remoteUpgradeFixture(t,
		ProjectLocator{ID: "keep", Kind: LocatorGitRemote, Value: "git@host:o/r"},
		ProjectLocator{ID: "gone", Kind: LocatorGitRemote, Value: "ssh://git@host/o/r"})
	e := recordedLocatorEvent("gone", "", "", "")
	e.EventID, e.Kind = "historical-remove", "project.locator_removed"
	e.Payload, _ = json.Marshal(projectLocatorPayload{ProjectID: "project-a", LocatorID: "gone", ExpectedVersion: 3, ResultingVersion: 4})
	insertHistoricalEvent(t, s, e)
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	scope, err := beginFold(ctx, tx)
	if err != nil {
		t.Fatal(err)
	}
	if err := foldRegisteredEvent(ctx, tx, e, scope); err != nil {
		t.Fatal(err)
	}
	if err := scope.close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, s.db); err != nil {
		t.Fatal(err)
	}
	var generation int
	if err := s.db.QueryRow(`SELECT normalization_version FROM project_locators WHERE locator_id='keep'`).Scan(&generation); err != nil || generation != 2 {
		t.Fatalf("surviving locator generation=%d err=%v", generation, err)
	}
	before := remoteUpgradeSnapshot(t, s)
	if err := RebuildFromLog(ctx, s); err != nil {
		t.Fatal(err)
	}
	if before != remoteUpgradeSnapshot(t, s) {
		t.Fatal("historical transient collision changed replay")
	}
}

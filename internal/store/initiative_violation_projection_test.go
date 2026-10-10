package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The reference recompute is the whole-projection scan the trigger-maintained
// violation projections replace: the two statements the validator issued
// before migration 120, kept here as the independent oracle. If a dependency
// change ever leaves the durable projection behind, this comparison fails
// even when the validator's first-row reads happen to agree.

const referenceScopeViolationsSQL = `SELECT w.id FROM work_items w
	LEFT JOIN work_projects wp ON wp.work_id=w.id AND wp.role='primary'
	LEFT JOIN product_projects pp ON pp.project_id=wp.project_id
	WHERE w.kind='initiative' GROUP BY w.id
	HAVING count(DISTINCT pp.product_id)<>1 ORDER BY w.id`

const referenceEntryViolationsSQL = `WITH subjects AS (
		SELECT e.initiative_work_id AS work_id FROM initiative_entries e
		JOIN work_items w ON w.id=e.initiative_work_id AND w.kind='initiative'
		UNION
		SELECT e.child_work_id FROM initiative_entries e
		JOIN work_items w ON w.id=e.initiative_work_id AND w.kind='initiative'
	), scopes AS (
		SELECT wp.work_id, count(DISTINCT pp.product_id) AS products, min(pp.product_id) AS product
		FROM subjects s CROSS JOIN work_projects wp ON wp.work_id=s.work_id
		JOIN product_projects pp ON pp.project_id=wp.project_id
		WHERE wp.role='primary' GROUP BY wp.work_id
	)
	SELECT e.initiative_work_id,e.child_work_id,e.position,
		CASE
			WHEN child.id IS NULL THEN 'missing_child'
			WHEN child.kind='initiative' THEN 'nested'
			WHEN coalesce(cs.products,0)<>1 THEN 'child_scope'
			WHEN coalesce(cs.product,'')<>coalesce(ins.product,'') THEN 'mismatch'
			ELSE 'diverged'
		END
	FROM initiative_entries e
	JOIN work_items parent ON parent.id=e.initiative_work_id AND parent.kind='initiative'
	LEFT JOIN work_items child ON child.id=e.child_work_id
	LEFT JOIN scopes cs ON cs.work_id=e.child_work_id
	LEFT JOIN scopes ins ON ins.work_id=e.initiative_work_id
	LEFT JOIN relations r ON r.work_id_from=e.initiative_work_id AND r.work_id_to=e.child_work_id AND r.kind='includes'
	WHERE child.id IS NULL OR child.kind='initiative' OR coalesce(cs.products,0)<>1
		OR cs.product<>ins.product OR r.work_id_to IS NULL
	ORDER BY e.initiative_work_id,e.position,e.child_work_id`

func queryStrings(t *testing.T, db queryer, query string) []string {
	t.Helper()
	rows, err := db.QueryContext(context.Background(), query)
	if err != nil {
		t.Fatalf("reference query: %v\n%s", err, query)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var parts []string
		cols, err := rows.Columns()
		if err != nil {
			t.Fatal(err)
		}
		values := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range values {
			ptrs[i] = &values[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatalf("reference scan: %v", err)
		}
		for _, v := range values {
			switch typed := v.(type) {
			case nil:
				parts = append(parts, "<null>")
			case int64:
				parts = append(parts, strconv.FormatInt(typed, 10))
			default:
				parts = append(parts, typed.(string))
			}
		}
		out = append(out, strings.Join(parts, " "))
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("reference rows: %v", err)
	}
	return out
}

// TestInitiativeProjection_MaintenanceStatementsStayKeyed proves the plan
// shape every trigger body relies on: SQLite flattens the shared views and
// pushes the key predicates onto the canonical indexes, so one entry,
// membership, or relation change maintains the projection through equality
// searches and never scans the population. EXPLAIN QUERY PLAN on the exact
// INSERT..SELECT texts the triggers run is read-only, so it runs directly
// on the store pool.
func TestInitiativeProjection_MaintenanceStatementsStayKeyed(t *testing.T) {
	t.Parallel()
	s := newBootstrapStore(t)
	entryCols := "initiative_work_id,child_work_id,position,violation"
	entryInsert := "INSERT INTO initiative_entry_violations(" + entryCols + ") SELECT " + entryCols +
		" FROM initiative_entry_violation_rows WHERE violation IS NOT NULL AND "
	for name, statement := range map[string]string{
		"parent-key refresh":     entryInsert + "initiative_work_id='w'",
		"child-key refresh":      entryInsert + "child_work_id='w' AND initiative_work_id<>'w'",
		"entry-pair refresh":     entryInsert + "initiative_work_id='i' AND child_work_id='c'",
		"relation-pair refresh":  entryInsert + "'includes'='includes' AND initiative_work_id='i' AND child_work_id='c'",
		"scope-point refresh":    "INSERT INTO initiative_scope_violations(work_id) SELECT work_id FROM initiative_scope_violation_rows WHERE work_id='w'",
		"project-membership set": entryInsert + "initiative_work_id IN (SELECT DISTINCT wp.work_id FROM work_projects wp WHERE wp.role='primary' AND wp.project_id='p')",
	} {
		rows, err := s.db.QueryContext(context.Background(), "EXPLAIN QUERY PLAN "+statement)
		if err != nil {
			t.Fatalf("%s: %v\n%s", name, err, statement)
		}
		var plan []string
		for rows.Next() {
			var id, parent, notused, detail string
			if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			plan = append(plan, detail)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		for _, detail := range plan {
			if strings.Contains(detail, "SCAN") {
				t.Errorf("%s: maintenance plan scans the population: %s\n%s", name, detail, statement)
			}
		}
		wantIndex := map[string]string{
			"parent-key refresh":    "sqlite_autoindex_initiative_entries_2",
			"child-key refresh":     "initiative_entries_by_child",
			"entry-pair refresh":    "sqlite_autoindex_initiative_entries_1",
			"relation-pair refresh": "sqlite_autoindex_initiative_entries_1",
		}[name]
		if wantIndex != "" && !strings.Contains(strings.Join(plan, "\n"), wantIndex) {
			t.Errorf("%s: driving index %s absent from plan:\n%s", name, wantIndex, strings.Join(plan, "\n"))
		}
	}
}

// assertViolationProjectionMatchesRecompute holds the durable violation
// projection to the whole-projection scan. Scope rows must always agree. A
// scope violation gates the entry reads exactly as the ordered scans did, so
// the entry comparison runs only once no scope violation remains.
func assertViolationProjectionMatchesRecompute(t *testing.T, s *Store) {
	t.Helper()
	want := queryStrings(t, s.db, referenceScopeViolationsSQL)
	got := queryStrings(t, s.db, `SELECT work_id FROM initiative_scope_violations ORDER BY work_id`)
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("scope violations drifted from the whole projection:\n maintained=%v\n recompute =%v", got, want)
	}
	if len(want) > 0 {
		return
	}
	wantEntries := queryStrings(t, s.db, referenceEntryViolationsSQL)
	gotEntries := queryStrings(t, s.db, `SELECT initiative_work_id,child_work_id,position,violation FROM initiative_entry_violations ORDER BY initiative_work_id,position,child_work_id`)
	if strings.Join(gotEntries, "|") != strings.Join(wantEntries, "|") {
		t.Fatalf("entry violations drifted from the whole projection:\n maintained=%v\n recompute =%v", gotEntries, wantEntries)
	}
}

func assertDurableViolations(t *testing.T, s *Store, query string) []string {
	t.Helper()
	return queryStrings(t, s.db, query)
}

// TestInitiativeProjection_MaintainedUnderDependencyChanges walks every
// dependency change class through corruption and repair, and after each step
// requires the durable violation projection to equal the whole-projection
// recompute. Synthetic statements run on an external connection with foreign
// keys disabled and the work_projects partial unique index dropped, so the
// maintenance triggers prove themselves on a writer the Go fold never sees.
func TestInitiativeProjection_MaintainedUnderDependencyChanges(t *testing.T) {
	t.Parallel()
	// Every step is self-contained: it corrupts one dependency class, checks
	// the durable rows, and repairs itself, so step order carries no state.
	total := `SELECT (SELECT count(*) FROM initiative_scope_violations)+(SELECT count(*) FROM initiative_entry_violations)`
	steps := []struct {
		name string
		run  func(t *testing.T, s *Store)
	}{
		{"valid projection stays empty", func(t *testing.T, s *Store) {
			if got := readInt[int64](t, s, total); got != 0 {
				t.Fatalf("valid projection holds %d violation rows", got)
			}
		}},
		{"entry without relation diverges", func(t *testing.T, s *Store) {
			synthExec(t, s, `INSERT INTO initiative_entries(initiative_work_id, child_work_id, position, required) VALUES('init-keep','child-spare',1,1)`)
			if got := readInt[int64](t, s, `SELECT count(*) FROM initiative_entry_violations WHERE child_work_id='child-spare' AND violation='diverged'`); got != 1 {
				t.Fatalf("diverged rows=%d want 1", got)
			}
		}},
		{"relation insert repairs divergence", func(t *testing.T, s *Store) {
			synthExec(t, s, `INSERT INTO initiative_entries(initiative_work_id, child_work_id, position, required) VALUES('init-keep','child-spare',1,1)`)
			if got := readInt[int64](t, s, `SELECT count(*) FROM initiative_entry_violations WHERE violation='diverged'`); got != 1 {
				t.Fatalf("diverged rows=%d want 1 before the repair", got)
			}
			synthExec(t, s, `INSERT INTO relations(id,work_id_from,work_id_to,kind,created_at) VALUES(-10,'init-keep','child-spare','includes','2026-01-01T00:00:00Z')`)
			if got := readInt[int64](t, s, total); got != 0 {
				t.Fatalf("repaired projection still holds %d rows", got)
			}
		}},
		{"relation delete restores divergence", func(t *testing.T, s *Store) {
			synthExec(t, s, `DELETE FROM relations WHERE work_id_from='init-dep' AND work_id_to='child-dep' AND kind='includes'`)
			if got := readInt[int64](t, s, `SELECT count(*) FROM initiative_entry_violations WHERE violation='diverged'`); got != 1 {
				t.Fatalf("diverged rows=%d want 1", got)
			}
			synthExec(t, s, `INSERT INTO relations(id,work_id_from,work_id_to,kind,created_at) VALUES(-11,'init-dep','child-dep','includes','2026-01-01T00:00:00Z')`)
			if got := readInt[int64](t, s, total); got != 0 {
				t.Fatalf("restored relation left %d rows", got)
			}
		}},
		{"relation kind transition diverges and restores", func(t *testing.T, s *Store) {
			synthExec(t, s, `UPDATE relations SET kind='blocks' WHERE work_id_from='init-dep' AND work_id_to='child-dep' AND kind='includes'`)
			if got := readInt[int64](t, s, `SELECT count(*) FROM initiative_entry_violations WHERE violation='diverged'`); got != 1 {
				t.Fatalf("kind transition left %d diverged rows, want 1", got)
			}
			synthExec(t, s, `UPDATE relations SET kind='includes' WHERE work_id_from='init-dep' AND work_id_to='child-dep' AND kind='blocks'`)
			if got := readInt[int64](t, s, total); got != 0 {
				t.Fatalf("restored kind left %d rows", got)
			}
		}},
		{"unrelated relation update cannot erase divergence", func(t *testing.T, s *Store) {
			synthExec(t, s, `DELETE FROM relations WHERE work_id_from='init-dep' AND work_id_to='child-dep' AND kind='includes'`,
				`INSERT INTO relations(id,work_id_from,work_id_to,kind,created_at) VALUES(-12,'init-dep','child-dep','blocks','2026-01-01T00:00:00Z')`,
				`UPDATE relations SET kind='depends_on' WHERE work_id_from='init-dep' AND work_id_to='child-dep' AND kind='blocks'`)
			if got := readInt[int64](t, s, `SELECT count(*) FROM initiative_entry_violations WHERE violation='diverged'`); got != 1 {
				t.Fatalf("unrelated relation update erased divergence: rows=%d want 1", got)
			}
		}},
		{"relation endpoint update moves the includes edge", func(t *testing.T, s *Store) {
			synthExec(t, s, `UPDATE relations SET work_id_to='child-spare' WHERE work_id_from='init-dep' AND work_id_to='child-dep' AND kind='includes'`)
			if got := readInt[int64](t, s, `SELECT count(*) FROM initiative_entry_violations WHERE violation='diverged'`); got != 1 {
				t.Fatalf("endpoint update left %d diverged rows, want 1", got)
			}
			synthExec(t, s, `UPDATE relations SET work_id_to='child-dep' WHERE work_id_from='init-dep' AND work_id_to='child-spare' AND kind='includes'`)
			if got := readInt[int64](t, s, total); got != 0 {
				t.Fatalf("restored endpoint left %d rows", got)
			}
		}},
		{"child membership moves to a foreign project", func(t *testing.T, s *Store) {
			synthExec(t, s, `UPDATE work_projects SET project_id='project-foreign' WHERE work_id='child-dep' AND role='primary'`)
			if got := readInt[int64](t, s, `SELECT count(*) FROM initiative_entry_violations WHERE violation='mismatch'`); got != 1 {
				t.Fatalf("mismatch rows=%d want 1", got)
			}
			synthExec(t, s, `UPDATE work_projects SET project_id='project-bootstrap' WHERE work_id='child-dep' AND role='primary'`)
			if got := readInt[int64](t, s, total); got != 0 {
				t.Fatalf("restored membership left %d rows", got)
			}
		}},
		{"child primary demoted to secondary", func(t *testing.T, s *Store) {
			synthExec(t, s, `UPDATE work_projects SET role='secondary' WHERE work_id='child-dep' AND project_id='project-bootstrap'`)
			if got := readInt[int64](t, s, `SELECT count(*) FROM initiative_entry_violations WHERE violation='child_scope'`); got != 1 {
				t.Fatalf("child_scope rows=%d want 1", got)
			}
			synthExec(t, s, `UPDATE work_projects SET role='primary' WHERE work_id='child-dep' AND project_id='project-bootstrap'`)
			if got := readInt[int64](t, s, total); got != 0 {
				t.Fatalf("restored role left %d rows", got)
			}
		}},
		{"duplicate same-product primary is accepted", func(t *testing.T, s *Store) {
			synthExec(t, s, synthSecondPrimary("child-dep", "project-sibling"))
			if got := readInt[int64](t, s, total); got != 0 {
				t.Fatalf("same-product duplicate primary left %d rows", got)
			}
		}},
		{"second primary in a foreign product is ambiguous", func(t *testing.T, s *Store) {
			synthExec(t, s, synthSecondPrimary("child-dep", "project-foreign"))
			if got := readInt[int64](t, s, `SELECT count(*) FROM initiative_entry_violations WHERE violation='child_scope'`); got != 1 {
				t.Fatalf("child_scope rows=%d want 1", got)
			}
			synthExec(t, s, `DELETE FROM work_projects WHERE work_id='child-dep' AND project_id='project-foreign' AND role='primary'`)
			if got := readInt[int64](t, s, total); got != 0 {
				t.Fatalf("repaired ambiguity left %d rows", got)
			}
		}},
		{"product mapping widens every primary work of the project", func(t *testing.T, s *Store) {
			synthExec(t, s, `INSERT INTO product_projects(product_id,project_id,role) VALUES('product-foreign','project-bootstrap','secondary')`)
			if got := readInt[int64](t, s, `SELECT count(*) FROM initiative_scope_violations`); got != 2 {
				t.Fatalf("scope rows=%d want 2 (init-keep, init-dep)", got)
			}
			if got := readInt[int64](t, s, `SELECT count(*) FROM initiative_entry_violations WHERE violation='child_scope'`); got != 2 {
				t.Fatalf("child_scope rows=%d want 2", got)
			}
			synthExec(t, s, `DELETE FROM product_projects WHERE product_id='product-foreign' AND project_id='project-bootstrap'`)
			if got := readInt[int64](t, s, total); got != 0 {
				t.Fatalf("restored mapping left %d rows", got)
			}
		}},
		{"product mapping update moves between projects", func(t *testing.T, s *Store) {
			synthExec(t, s, `INSERT INTO product_projects(product_id,project_id,role) VALUES('product-extra','project-bootstrap','secondary')`)
			if got := readInt[int64](t, s, `SELECT count(*) FROM initiative_scope_violations`); got != 2 {
				t.Fatalf("scope rows=%d want 2 while both products map to the project", got)
			}
			synthExec(t, s, `UPDATE product_projects SET project_id='project-foreign' WHERE product_id='product-extra' AND project_id='project-bootstrap'`)
			if got := readInt[int64](t, s, total); got != 0 {
				t.Fatalf("remapped product left %d violation rows", got)
			}
		}},
		{"initiative loses its primary membership", func(t *testing.T, s *Store) {
			synthExec(t, s, `UPDATE work_projects SET role='secondary' WHERE work_id='init-dep' AND project_id='project-bootstrap'`)
			if got := readInt[int64](t, s, `SELECT count(*) FROM initiative_scope_violations WHERE work_id='init-dep'`); got != 1 {
				t.Fatalf("scope rows for init-dep=%d want 1", got)
			}
			synthExec(t, s, `UPDATE work_projects SET role='primary' WHERE work_id='init-dep' AND project_id='project-bootstrap'`)
			if got := readInt[int64](t, s, total); got != 0 {
				t.Fatalf("restored primary left %d scope rows", got)
			}
		}},
		{"kind transition to nested initiative and back", func(t *testing.T, s *Store) {
			synthExec(t, s, `UPDATE work_items SET kind='initiative' WHERE id='child-dep'`)
			if got := readInt[int64](t, s, `SELECT count(*) FROM initiative_entry_violations WHERE violation='nested'`); got != 1 {
				t.Fatalf("nested rows=%d want 1", got)
			}
			synthExec(t, s, `UPDATE work_items SET kind='task' WHERE id='child-dep'`)
			if got := readInt[int64](t, s, total); got != 0 {
				t.Fatalf("kind restored left %d rows", got)
			}
		}},
		{"parent stops being an initiative", func(t *testing.T, s *Store) {
			synthExec(t, s, `UPDATE work_items SET kind='task' WHERE id='init-dep'`)
			if got := readInt[int64](t, s, `SELECT count(*) FROM initiative_entry_violations WHERE initiative_work_id='init-dep'`); got != 0 {
				t.Fatalf("non-initiative parent kept %d rows", got)
			}
			synthExec(t, s, `UPDATE work_items SET kind='initiative' WHERE id='init-dep'`)
			if got := readInt[int64](t, s, total); got != 0 {
				t.Fatalf("restored parent holds %d rows", got)
			}
		}},
		{"child deletion leaves a missing-child violation", func(t *testing.T, s *Store) {
			synthExec(t, s, `DELETE FROM work_items WHERE id='child-dep'`)
			if got := readInt[int64](t, s, `SELECT count(*) FROM initiative_entry_violations WHERE child_work_id='child-dep' AND violation='missing_child'`); got != 1 {
				t.Fatalf("missing_child rows=%d want 1", got)
			}
		}},
		{"entry removal clears its violation row", func(t *testing.T, s *Store) {
			synthExec(t, s, `DELETE FROM initiative_entries WHERE initiative_work_id='init-dep' AND child_work_id='child-dep'`)
			if got := readInt[int64](t, s, total); got != 0 {
				t.Fatalf("entry removal left %d rows", got)
			}
		}},
		{"entry key and position updates keep the ordered pick honest", func(t *testing.T, s *Store) {
			synthExec(t, s, `UPDATE initiative_entries SET position=9 WHERE initiative_work_id='init-keep' AND child_work_id='child-keep'`,
				synthEntryOnly("init-keep", "child-spare"))
			var initiative, child, violation string
			if err := s.db.QueryRow(`SELECT initiative_work_id,child_work_id,violation FROM initiative_entry_violations ORDER BY initiative_work_id,position,child_work_id LIMIT 1`).Scan(&initiative, &child, &violation); err != nil {
				t.Fatalf("ordered pick: %v", err)
			}
			if initiative != "init-keep" || child != "child-spare" || violation != "diverged" {
				t.Fatalf("ordered pick=(%s,%s,%s) want (init-keep,child-spare,diverged)", initiative, child, violation)
			}
			synthExec(t, s, `UPDATE initiative_entries SET child_work_id='child-dep', position=1 WHERE initiative_work_id='init-dep' AND child_work_id='child-dep'`)
			if got := readInt[int64](t, s, `SELECT count(*) FROM initiative_entry_violations WHERE child_work_id='child-dep'`); got != 0 {
				t.Fatalf("key update left %d rows for the moved entry", got)
			}
		}},
		{"work id rename orphans entries and follows scope", func(t *testing.T, s *Store) {
			synthExec(t, s, `UPDATE work_items SET id='init-dep-renamed' WHERE id='init-dep'`)
			if got := readInt[int64](t, s, `SELECT count(*) FROM initiative_scope_violations WHERE work_id='init-dep-renamed'`); got != 1 {
				t.Fatalf("renamed initiative scope rows=%d want 1 (no membership carries the new id)", got)
			}
			if got := readInt[int64](t, s, `SELECT count(*) FROM initiative_entry_violations WHERE initiative_work_id='init-dep'`); got != 0 {
				t.Fatalf("orphaned parent kept %d rows", got)
			}
			synthExec(t, s, `UPDATE work_items SET id='init-dep' WHERE id='init-dep-renamed'`)
			if got := readInt[int64](t, s, total); got != 0 {
				t.Fatalf("renamed-back store holds %d violation rows", got)
			}
		}},
		{"rolled-back external transaction leaves no trace", func(t *testing.T, s *Store) {
			db, err := sql.Open(driverName, "file:"+s.Path()+"?_pragma=foreign_keys(0)")
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = db.Close() }()
			db.SetMaxOpenConns(1)
			ctx := context.Background()
			if _, err := db.ExecContext(ctx, `INSERT OR IGNORE INTO fold_guard(active) VALUES(1)`); err != nil {
				t.Fatal(err)
			}
			defer func() { _, _ = db.ExecContext(ctx, `DELETE FROM fold_guard`) }()
			tx, err := db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			for _, q := range []string{
				`UPDATE work_projects SET project_id='project-foreign' WHERE work_id='child-keep' AND role='primary'`,
			} {
				if _, err := tx.ExecContext(ctx, q); err != nil {
					t.Fatalf("corrupt tx: %v\n%s", err, q)
				}
			}
			var rows int64
			if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM initiative_entry_violations`).Scan(&rows); err != nil {
				t.Fatal(err)
			}
			if rows != 1 {
				t.Fatalf("inside the corrupt transaction the projection holds %d rows, want the synchronous mismatch row", rows)
			}
			if err := tx.Rollback(); err != nil {
				t.Fatal(err)
			}
			if got := readInt[int64](t, s, total); got != 0 {
				t.Fatalf("rolled-back transaction left %d violation rows", got)
			}
		}},
	}
	for _, step := range steps {
		t.Run(step.name, func(t *testing.T) {
			s := newBootstrapStore(t)
			seedSiblingProjectInProduct(t, s, "project-sibling")
			seedForeignScope(t, s, "project-foreign", "product-foreign")
			addWork(t, s, "init-keep", "initiative", "project-bootstrap", "primary")
			addWork(t, s, "child-keep", "task", "project-bootstrap", "primary")
			addWork(t, s, "init-dep", "initiative", "project-bootstrap", "primary")
			addWork(t, s, "child-dep", "task", "project-bootstrap", "primary")
			addWork(t, s, "child-spare", "task", "project-bootstrap", "primary")
			keepEntry, keepRel := synthEntryRelation("init-keep", "child-keep", -5)
			depEntry, depRel := synthEntryRelation("init-dep", "child-dep", -6)
			synthExec(t, s, keepEntry, keepRel, depEntry, depRel)
			step.run(t, s)
			assertViolationProjectionMatchesRecompute(t, s)
			assertValidatorAgreesWithProjection(t, s)
		})
	}
}

// assertValidatorAgreesWithProjection pins the typed-refusal contract on top
// of the row-level checks: whichever violation the durable projection names,
// the validator reports it with the kind and detail the scan-era validator
// produced.
func assertValidatorAgreesWithProjection(t *testing.T, s *Store) {
	t.Helper()
	_, _, err := runValidator(context.Background(), t, s, nil)
	var wantKind FailureKind
	wantDetail := ""
	scope := assertDurableViolations(t, s, `SELECT work_id FROM initiative_scope_violations ORDER BY work_id LIMIT 1`)
	if len(scope) > 0 {
		wantKind, wantDetail = KindInitiativeScopeViolation, scope[0]
	} else {
		entry := assertDurableViolations(t, s, `SELECT violation FROM initiative_entry_violations ORDER BY initiative_work_id,position,child_work_id LIMIT 1`)
		if len(entry) == 0 {
			if err != nil {
				t.Fatalf("validator refused a clean projection: %v", err)
			}
			return
		}
		switch entry[0] {
		case "missing_child":
			wantKind = KindProjectionNotFound
		case "child_scope":
			wantKind, wantDetail = KindInitiativeScopeViolation, "exactly one Product"
		case "mismatch":
			wantKind, wantDetail = KindInitiativeScopeViolation, "different Product"
		case "nested":
			wantKind, wantDetail = KindInitiativeScopeViolation, "nested Initiative entry"
		default:
			wantKind, wantDetail = KindInitiativeScopeViolation, "diverged"
		}
	}
	if err == nil {
		t.Fatalf("validator accepted a projection holding violation %q%v", wantDetail, scope)
	}
	var failure *Failure
	if !errors.As(err, &failure) {
		t.Fatalf("validator returned %v, want typed failure kind %q", err, wantKind)
	}
	if failure.Kind != wantKind || (wantDetail != "" && !strings.Contains(failure.Detail, wantDetail)) {
		t.Fatalf("validator failure kind=%q detail=%q, want kind %q with detail %q", failure.Kind, failure.Detail, wantKind, wantDetail)
	}
}

// TestInitiativeProjection_SeededProjectionConvergesToEmpty proves the
// trigger path converges on the benchmark fixture: bulk inserts that never
// pass through the Go fold still leave the projection exactly empty.
func TestInitiativeProjection_SeededProjectionConvergesToEmpty(t *testing.T) {
	t.Parallel()
	s := newBootstrapStore(t)
	seedInitiativeProjection(t, s, "product-bootstrap", "project-bootstrap", 20, 10)
	if got := readInt[int64](t, s, `SELECT (SELECT count(*) FROM initiative_scope_violations)+(SELECT count(*) FROM initiative_entry_violations)`); got != 0 {
		t.Fatalf("seeded projection holds %d violation rows, want 0", got)
	}
	assertViolationProjectionMatchesRecompute(t, s)
	assertValidatorOK(t, s)
}

// TestInitiativeProjection_RebuildLeavesNoStaleViolationState covers both
// rebuild halves: a violated store rebuilds to empty violation tables, and a
// clean store rebuilds to empty violation tables. The rebuild's canonical
// DELETEs and replay folds maintain the projection through the same
// triggers; no extra clear step may exist for it.
func TestInitiativeProjection_RebuildLeavesNoStaleViolationState(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newBootstrapStore(t)
	seedForeignScope(t, s, "project-far", "product-far")
	addWork(t, s, "init-rb", "initiative", "project-bootstrap", "primary")
	addWork(t, s, "child-rb", "task", "project-bootstrap", "primary")
	entry, rel := synthEntryRelation("init-rb", "child-rb", -7)
	synthExec(t, s, entry, rel)
	synthExec(t, s, `UPDATE work_projects SET project_id='project-far' WHERE work_id='child-rb' AND role='primary'`)
	if got := readInt[int64](t, s, `SELECT count(*) FROM initiative_entry_violations`); got != 1 {
		t.Fatalf("pre-rebuild violation rows=%d want 1", got)
	}
	if err := RebuildFromLog(ctx, s); err != nil {
		t.Fatal(err)
	}
	if got := readInt[int64](t, s, `SELECT (SELECT count(*) FROM initiative_scope_violations)+(SELECT count(*) FROM initiative_entry_violations)`); got != 0 {
		t.Fatalf("rebuild left %d violation rows, want convergence to empty", got)
	}
	assertValidatorOK(t, s)
	if err := RebuildFromLog(ctx, s); err != nil {
		t.Fatal(err)
	}
	if got := readInt[int64](t, s, `SELECT (SELECT count(*) FROM initiative_scope_violations)+(SELECT count(*) FROM initiative_entry_violations)`); got != 0 {
		t.Fatalf("second rebuild left %d violation rows", got)
	}
	assertViolationProjectionMatchesRecompute(t, s)
}

// dropMigration120Objects removes every schema object migration 120 creates,
// so a store whose manifest tail was removed can re-apply the step cleanly.
func dropMigration120Objects(ctx context.Context, db *sql.DB) error {
	for _, stmt := range []string{
		`DROP TRIGGER IF EXISTS initiative_projection_work_items_insert`,
		`DROP TRIGGER IF EXISTS initiative_projection_work_items_delete`,
		`DROP TRIGGER IF EXISTS initiative_projection_work_items_update`,
		`DROP TRIGGER IF EXISTS initiative_projection_work_projects_insert`,
		`DROP TRIGGER IF EXISTS initiative_projection_work_projects_delete`,
		`DROP TRIGGER IF EXISTS initiative_projection_work_projects_update`,
		`DROP TRIGGER IF EXISTS initiative_projection_product_projects_insert`,
		`DROP TRIGGER IF EXISTS initiative_projection_product_projects_delete`,
		`DROP TRIGGER IF EXISTS initiative_projection_product_projects_update`,
		`DROP TRIGGER IF EXISTS initiative_projection_relations_insert`,
		`DROP TRIGGER IF EXISTS initiative_projection_relations_delete`,
		`DROP TRIGGER IF EXISTS initiative_projection_relations_update`,
		`DROP TRIGGER IF EXISTS initiative_projection_initiative_entries_insert`,
		`DROP TRIGGER IF EXISTS initiative_projection_initiative_entries_delete`,
		`DROP TRIGGER IF EXISTS initiative_projection_initiative_entries_update`,
		`DROP VIEW IF EXISTS initiative_entry_violation_rows`,
		`DROP VIEW IF EXISTS initiative_scope_violation_rows`,
		`DROP VIEW IF EXISTS initiative_work_scope`,
		`DROP TABLE IF EXISTS initiative_entry_violations`,
		`DROP TABLE IF EXISTS initiative_scope_violations`,
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

// dropMigration122Objects removes every schema object migration 122 creates.
// The guarded worktree_ref_outcomes projection is one plain CREATE TABLE:
// its guard triggers and index are owned by the table and drop with it.
func dropMigration122Objects(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, `DROP TABLE IF EXISTS worktree_ref_outcomes`)
	return err
}

// dropMigration123Objects removes every schema object migration 123 creates:
// the retirement delete guard is one trigger on a pre-existing table.
func dropMigration123Objects(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, `DROP TRIGGER IF EXISTS active_research_packs_retirement_delete_guard`)
	return err
}

// synthExecPath runs statements on an external connection opened straight
// from a path, arming the fold guard the canonical guard triggers require.
func synthExecPath(t *testing.T, path string, queries ...string) {
	t.Helper()
	db, err := sql.Open(driverName, "file:"+path+"?_pragma=foreign_keys(0)")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = db.ExecContext(ctx, `DELETE FROM fold_guard`) }()
	for _, q := range queries {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatalf("path synthetic query: %v\n%s", err, q)
		}
	}
}

// TestInitiativeProjection_MigrationBackfillPreservesDefects builds a store
// that stops at migration 119 with defects in its canonical projection, then
// applies migration 120. The one-time backfill must record exactly those
// defects — the projection preserves them for the validator instead of
// repairing them — and a defect-free store must backfill to empty.
func TestInitiativeProjection_MigrationBackfillPreservesDefects(t *testing.T) {
	stampAsRelease(t)
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "backfill.db")

	s := openTempAtPath(t, path)
	seedBootstrapStoreAuthority(t, s, initBootstrapStoreRepo(t))
	seedForeignScope(t, s, "project-far", "product-far")
	addWork(t, s, "init-bf", "initiative", "project-bootstrap", "primary")
	addWork(t, s, "child-bf", "task", "project-bootstrap", "primary")
	addWork(t, s, "init-scope2", "initiative", "project-bootstrap", "primary")
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	raw, err := sql.Open(driverName, dataSourceName(path))
	if err != nil {
		t.Fatal(err)
	}
	// The backfill under test belongs to migration 120: the manifest tail
	// from 120 on is removed and every step's objects drop, so the upgrade
	// re-applies the ordered tail and the 120 backfill runs against the
	// pre-120 shape it originally met.
	if _, err := raw.ExecContext(ctx, `DELETE FROM schema_migrations WHERE version>=120`); err != nil {
		t.Fatal(err)
	}
	if err := dropMigration120Objects(ctx, raw); err != nil {
		t.Fatal(err)
	}
	if err := dropMigration121Objects(ctx, raw); err != nil {
		t.Fatal(err)
	}
	if err := dropMigration122Objects(ctx, raw); err != nil {
		t.Fatal(err)
	}
	if err := dropMigration123Objects(ctx, raw); err != nil {
		t.Fatal(err)
	}
	// Defects a pre-120 store can carry: an initiative that lost its primary
	// (scope defect) and a foreign-product child entry whose includes relation
	// exists (entry defect). The two exercise the two backfill statements.
	synthExecPath(t, path,
		`UPDATE work_projects SET role='secondary' WHERE work_id='init-scope2' AND project_id='project-bootstrap'`)
	bfEntry, bfRel := synthEntryRelation("init-bf", "child-bf", -20)
	synthExecPath(t, path, bfEntry, bfRel,
		`UPDATE work_projects SET project_id='project-far' WHERE work_id='child-bf' AND role='primary'`)
	_ = raw.Close()

	report, err := Upgrade(ctx, path, nil)
	if err != nil {
		t.Fatalf("upgrade with migration 120: %v (%+v)", err, report)
	}
	// The applied set is the ordered tail this branch's schema holds from
	// 120 on — derived from the schema, never hand-listed — so the 120
	// backfill stays asserted while a migration appended after the tail
	// fails here loudly until the fixture's drops join it.
	wantApplied := []int{}
	for _, m := range migrations {
		if m.Version >= 120 {
			wantApplied = append(wantApplied, m.Version)
		}
	}
	if len(report.Applied) != len(wantApplied) {
		t.Fatalf("upgrade applied=%v, want the ordered tail %v", report.Applied, wantApplied)
	}
	for i := range wantApplied {
		if report.Applied[i] != wantApplied[i] {
			t.Fatalf("upgrade applied=%v, want the ordered tail %v", report.Applied, wantApplied)
		}
	}

	upgraded, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = upgraded.Close() }()
	if got := assertDurableViolations(t, upgraded, `SELECT work_id FROM initiative_scope_violations ORDER BY work_id`); len(got) != 1 || got[0] != "init-scope2" {
		t.Fatalf("backfilled scope violations=%v want [init-scope2]", got)
	}
	if got := assertDurableViolations(t, upgraded, `SELECT violation FROM initiative_entry_violations ORDER BY initiative_work_id,position,child_work_id`); len(got) != 1 || got[0] != "mismatch" {
		t.Fatalf("backfilled entry violations=%v want [mismatch]", got)
	}
	assertViolationProjectionMatchesRecompute(t, upgraded)
	// The scope refusal is reported first: the ordered reads the migration
	// replaced always answered scope before any entry defect.
	assertValidatorKind(t, upgraded, KindInitiativeScopeViolation, "init-scope2")

	// The repair path runs through the same dependency changes: restoring the
	// primary unmasks the entry defect, and no projection rewrite happens.
	synthExec(t, upgraded, `UPDATE work_projects SET role='primary' WHERE work_id='init-scope2' AND project_id='project-bootstrap'`)
	if got := readInt[int64](t, upgraded, `SELECT count(*) FROM initiative_scope_violations`); got != 0 {
		t.Fatalf("repaired scope defect left %d rows", got)
	}
	assertValidatorKind(t, upgraded, KindInitiativeScopeViolation, "different Product")
	synthExec(t, upgraded, `UPDATE work_projects SET project_id='project-bootstrap' WHERE work_id='child-bf' AND role='primary'`)
	assertValidatorOK(t, upgraded)
	assertViolationProjectionMatchesRecompute(t, upgraded)
}

// TestInitiativeProjection_MigrationBackfillCleanStoreIsEmpty proves the
// backfill invents no defects on a defect-free store.
func TestInitiativeProjection_MigrationBackfillCleanStoreIsEmpty(t *testing.T) {
	stampAsRelease(t)
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "clean.db")
	s := openTempAtPath(t, path)
	seedBootstrapStoreAuthority(t, s, initBootstrapStoreRepo(t))
	seedInitiativeProjection(t, s, "product-bootstrap", "project-bootstrap", 3, 3)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open(driverName, dataSourceName(path))
	if err != nil {
		t.Fatal(err)
	}
	// The backfill under test belongs to migration 120: the manifest tail
	// from 120 on is removed and every step's objects drop, so the upgrade
	// re-applies the ordered tail and the 120 backfill runs against the
	// pre-120 shape it originally met.
	if _, err := raw.ExecContext(ctx, `DELETE FROM schema_migrations WHERE version>=120`); err != nil {
		t.Fatal(err)
	}
	if err := dropMigration120Objects(ctx, raw); err != nil {
		t.Fatal(err)
	}
	if err := dropMigration121Objects(ctx, raw); err != nil {
		t.Fatal(err)
	}
	if err := dropMigration122Objects(ctx, raw); err != nil {
		t.Fatal(err)
	}
	if err := dropMigration123Objects(ctx, raw); err != nil {
		t.Fatal(err)
	}
	_ = raw.Close()
	if _, err := Upgrade(ctx, path, nil); err != nil {
		t.Fatal(err)
	}
	upgraded, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = upgraded.Close() }()
	if got := readInt[int64](t, upgraded, `SELECT (SELECT count(*) FROM initiative_scope_violations)+(SELECT count(*) FROM initiative_entry_violations)`); got != 0 {
		t.Fatalf("clean backfill produced %d violation rows", got)
	}
	assertValidatorOK(t, upgraded)
}

// TestInitiativeProjection_ExternalConnectionMaintainsSynchronously pins
// the other-connection obligation at the row level: a committed external
// transaction's dependency changes are already reflected when the main
// store connection reads next, with no intervening fold or validation.
func TestInitiativeProjection_ExternalConnectionMaintainsSynchronously(t *testing.T) {
	t.Parallel()
	s := newBootstrapStore(t)
	addWork(t, s, "init-ext", "initiative", "project-bootstrap", "primary")
	addWork(t, s, "child-ext", "task", "project-bootstrap", "primary")
	if got := readInt[int64](t, s, `SELECT count(*) FROM initiative_entry_violations`); got != 0 {
		t.Fatalf("pre-change violation rows=%d want 0", got)
	}
	entry, rel := synthEntryRelation("init-ext", "child-ext", -30)
	rel2 := strings.Replace(rel, "'includes'", "'blocks'", 1)
	synthExec(t, s, entry, rel2)
	if got := readInt[int64](t, s, `SELECT count(*) FROM initiative_entry_violations WHERE violation='diverged'`); got != 1 {
		t.Fatalf("external write left %d diverged rows, want the synchronous 1", got)
	}
	assertViolationProjectionMatchesRecompute(t, s)
}

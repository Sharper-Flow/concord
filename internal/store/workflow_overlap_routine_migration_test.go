package store

// The routine overlap resolution kinds (compatible_with, depends_on, blocks)
// settle coordination without a consumed approval, and the destructive kinds
// (merged_into, supersedes) keep one. Migration 91's approval_ref CHECK still
// demands a non-empty reference for every kind except depends_on, so a
// routine compatible_with or blocks fold dies at the projection insert with a
// projection conflict. The forward migration
// overlap_routine_resolution_carries_own_authority rebuilds the table so the
// routine kinds carry their own authority in an empty approval_ref while the
// destructive kinds still refuse one.
//
// This file is that migration's preservation witness: it builds the real
// immutable prefix that excludes the located target, seeds the historical
// depends_on shapes the old check alone admits, captures the table's rows and
// metadata, advances with the real Migrate runner, and holds the result to
// preserving all of it. The fixture rows are schema-migration data only: they
// satisfy the migration's foreign keys and claim no application authority and
// no rebuild proof. The destructive host-approval coverage stays in
// internal/agent, unchanged.

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// overlapRoutineAuthorityMigrationIndex locates the routine-overlap authority
// migration by name. The witness never pins the migration's version and
// never assumes it is the manifest tail.
func overlapRoutineAuthorityMigrationIndex(t *testing.T) int {
	t.Helper()
	for i, step := range migrations {
		if step.Name == "overlap_routine_resolution_carries_own_authority" {
			return i
		}
	}
	t.Fatal("the routine overlap authority migration is missing; add the forward migration beside this witness")
	return -1
}

// resolutionRowDump renders every resolution row's full column values as one
// comparable line per row, headed by the column inventory, so the
// preservation check never restates the table shape in prose.
func resolutionRowDump(t *testing.T, db *sql.DB) []string {
	t.Helper()
	rows, err := db.QueryContext(context.Background(), `SELECT * FROM workflow_overlap_resolutions ORDER BY resolution_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	dump := []string{strings.Join(columns, "\x1f")}
	for rows.Next() {
		values := make([]any, len(columns))
		pointers := make([]any, len(columns))
		for i := range values {
			pointers[i] = &values[i]
		}
		if err := rows.Scan(pointers...); err != nil {
			t.Fatal(err)
		}
		rendered := make([]string, len(columns))
		for i, value := range values {
			switch typed := value.(type) {
			case nil:
				rendered[i] = "\x00"
			case []byte:
				rendered[i] = string(typed)
			default:
				rendered[i] = fmt.Sprint(typed)
			}
		}
		dump = append(dump, strings.Join(rendered, "\x1f"))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return dump
}

// overlapSchemaObjects captures the pair and reverse indexes and the three
// fold-guard triggers the resolution table depends on, straight from
// sqlite_master, so their definitions are compared as the prefix wrote them.
func overlapSchemaObjects(t *testing.T, db *sql.DB) []schemaObject {
	t.Helper()
	rows, err := db.QueryContext(context.Background(), `SELECT type, name, sql FROM sqlite_master WHERE sql IS NOT NULL AND type IN ('index','trigger') AND name LIKE 'workflow_overlap_resolutions%' ORDER BY type, name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	objects := []schemaObject{}
	for rows.Next() {
		var object schemaObject
		if err := rows.Scan(&object.kind, &object.name, &object.sql); err != nil {
			t.Fatal(err)
		}
		objects = append(objects, object)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return objects
}

// renderSchemaObjects renders captured objects as comparable lines, so the
// preservation check compares kind, name, and SQL text as one list.
func renderSchemaObjects(objects []schemaObject) []string {
	dump := make([]string, len(objects))
	for i, object := range objects {
		dump[i] = object.kind + "\x1f" + object.name + "\x1f" + object.sql
	}
	return dump
}

// tableColumnInventory captures one table's columns in declaration order.
func tableColumnInventory(t *testing.T, db *sql.DB, table string) []string {
	t.Helper()
	rows, err := db.QueryContext(context.Background(), `SELECT name FROM pragma_table_info(?) ORDER BY cid`, table)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	names := []string{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return names
}

// foreignKeyDeclarations captures one table's foreign keys as declared.
func foreignKeyDeclarations(t *testing.T, db *sql.DB, table string) []string {
	t.Helper()
	rows, err := db.QueryContext(context.Background(), `SELECT "table", "from", "to", on_update, on_delete FROM pragma_foreign_key_list(?) ORDER BY id, seq`, table)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	declarations := []string{}
	for rows.Next() {
		var target, from, to, onUpdate, onDelete string
		if err := rows.Scan(&target, &from, &to, &onUpdate, &onDelete); err != nil {
			t.Fatal(err)
		}
		declarations = append(declarations, strings.Join([]string{from, "->", target + "." + to, onUpdate, onDelete}, "\x1f"))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return declarations
}

// domainEventDump captures the whole log, payload bytes included, so the
// migration is held to retaining every recorded event byte for byte.
func domainEventDump(t *testing.T, db *sql.DB) []string {
	t.Helper()
	rows, err := db.QueryContext(context.Background(), `SELECT seq, event_id, kind, subject_type, subject_id, actor, occurred_at, payload_version, payload FROM domain_events ORDER BY seq`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	dump := []string{}
	for rows.Next() {
		var dumpLine struct {
			seq            int64
			eventID        string
			kind           string
			subjectType    string
			subjectID      string
			actor          string
			occurredAt     string
			payloadVersion int
			payload        string
		}
		if err := rows.Scan(&dumpLine.seq, &dumpLine.eventID, &dumpLine.kind, &dumpLine.subjectType, &dumpLine.subjectID, &dumpLine.actor, &dumpLine.occurredAt, &dumpLine.payloadVersion, &dumpLine.payload); err != nil {
			t.Fatal(err)
		}
		dump = append(dump, fmt.Sprintf("%d\x1f%s\x1f%s\x1f%s\x1f%s\x1f%s\x1f%s\x1f%d\x1f%s", dumpLine.seq, dumpLine.eventID, dumpLine.kind, dumpLine.subjectType, dumpLine.subjectID, dumpLine.actor, dumpLine.occurredAt, dumpLine.payloadVersion, dumpLine.payload))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return dump
}

// manifestDump captures the applied-migration manifest rows in order.
func manifestDump(t *testing.T, db *sql.DB) []string {
	t.Helper()
	rows, err := db.QueryContext(context.Background(), `SELECT version, name, checksum, breaking, applied_at FROM schema_migrations ORDER BY version`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	dump := []string{}
	for rows.Next() {
		var version, breaking int
		var name, checksum, appliedAt string
		if err := rows.Scan(&version, &name, &checksum, &breaking, &appliedAt); err != nil {
			t.Fatal(err)
		}
		dump = append(dump, fmt.Sprintf("%d\x1f%s\x1f%s\x1f%d\x1f%s", version, name, checksum, breaking, appliedAt))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return dump
}

func assertUnchangedDump(t *testing.T, label string, before, after []string) {
	t.Helper()
	if !slices.Equal(before, after) {
		t.Fatalf("%s changed across the migration:\nbefore: %q\nafter:  %q", label, before, after)
	}
}

// requireGuardRefusal holds one write to the write-seam-only resolution table that
// runs without a write guard: the guards themselves, not the caller, must
// refuse.
func requireGuardRefusal(t *testing.T, err error, action string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s without a write guard was admitted; the workflow_overlap_resolutions write guards no longer operate", action)
	}
	if !strings.Contains(err.Error(), "workflow_overlap_resolutions is fold-only") {
		t.Fatalf("%s without a historical guard failed with %v, want the fold-only refusal", action, err)
	}
}

// requireSchemaRefusal holds one claim the rebuilt approval_ref CHECK must
// refuse while the write guard is armed, so the refusal names the schema and
// not the guard.
func requireSchemaRefusal(t *testing.T, err error, action string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s was admitted; the rebuilt approval_ref check dropped the destructive authority requirement", action)
	}
	if strings.Contains(err.Error(), "workflow_overlap_resolutions is fold-only") {
		t.Fatalf("%s was refused by the write guard, want the approval_ref check refusal", action)
	}
}

// TestRoutineOverlapAuthorityMigration advances a real pre-target prefix
// through the routine-overlap authority migration and holds the rebuilt
// table to preserving every existing row, column, foreign key, index, and
// guard, every recorded event byte, and the recorded checksum of the
// migration itself. It then probes the widened check as schema behavior
// only: the routine kinds accept an empty approval_ref and the destructive
// kinds refuse one. These probes are schema constraints, never
// authorization witnesses.
func TestRoutineOverlapAuthorityMigration(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	target := overlapRoutineAuthorityMigrationIndex(t)
	path := filepath.Join(t.TempDir(), "pre-routine-overlap-authority.db")
	db := openMigratedTo(t, path, target)

	// The prerequisite projection the resolution rows' foreign keys need:
	// one Product and the two endpoint work items. The old events are retained
	// as bytes, not submitted to current folds. Append them before the fixture
	// transaction because the store pools one connection.
	created := func(id, eventID string) Event {
		event := operationEvent(eventID, "work.created", SubjectWorkItem, id, map[string]any{"work_kind": "task", "title": id, "priority": 0})
		event.PayloadVersion = 2
		return event
	}
	historical := []Event{
		locatorProductEvent("overlap-authority-product"),
		created("overlap-from", "create-overlap-from"),
		created("overlap-to", "create-overlap-to"),
	}
	insertHistoricalEvents(t, db, historical)
	eventSeq := func(eventID string) int64 {
		t.Helper()
		var seq int64
		if err := db.QueryRowContext(ctx, `SELECT seq FROM domain_events WHERE event_id=?`, eventID).Scan(&seq); err != nil {
			t.Fatal(err)
		}
		return seq
	}
	productSeq := eventSeq("create-product-overlap-authority-product")
	fromSeq := eventSeq("create-overlap-from")
	toSeq := eventSeq("create-overlap-to")

	// The fixture transaction: seed the prerequisite rows, then write the
	// historical depends_on shapes the pre-target check alone admits — an
	// active row and an invalidated one, both with an empty approval_ref and
	// no invented approval. The compatible_with probe documents the old
	// check's refusal, so the witness proves the prefix really is
	// pre-widening.
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO products(id,display_name,stage_maturity,stage_audience_commitment,version,created_at,updated_at)
		VALUES('overlap-authority-product','overlap-authority-product','prototype','operator_only',1,'1970-01-01T00:00:01Z','1970-01-01T00:00:01Z')`); err != nil {
		t.Fatal(err)
	}
	for _, event := range historical[1:] {
		intent := mustJSONValue(workIntentProjection{Title: event.SubjectID, Kind: "task", Urgency: "standard", Tags: []string{}})
		at := event.OccurredAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00")
		if _, err := tx.ExecContext(ctx, `INSERT INTO work_items(id,kind,title,lifecycle,priority,version,created_at,updated_at,intent_json)
			VALUES(?,'task',?,'needed',0,1,?,?,?)`, event.SubjectID, event.SubjectID, at, at, string(intent)); err != nil {
			t.Fatal(err)
		}
	}
	actorRef := DeriveWorkflowActorRef("principal:operator", "client:overlap-authority", "agent:operator", "session:overlap-authority")
	if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_actors(actor_ref,principal_ref,client_ref,agent_ref,session_ref,actor_class,first_seen_at) VALUES(?,?,?,?,?,?,?)`,
		actorRef, "principal:operator", "client:overlap-authority", "agent:operator", "session:overlap-authority", "operator", "2026-10-15T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	for _, workID := range []string{"overlap-from", "overlap-to"} {
		if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
			workID, 1, "overlap authority migration fixture", "internal_sqlite", `[]`, `[]`, "2026-10-15T00:00:00Z", actorRef, `[]`, `[]`, 1, "prototype_internal"); err != nil {
			t.Fatal(err)
		}
	}
	insertResolution := func(resolutionID string, eventSeqValue int64, kind, reason string, invalidatedSeq any) error {
		t.Helper()
		_, err := tx.ExecContext(ctx, `INSERT INTO workflow_overlap_resolutions(resolution_id,event_seq,product_id,from_work_id,to_work_id,from_contract_version,to_contract_version,resolution_kind,reason,approval_ref,created_at,invalidated_seq) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
			resolutionID, eventSeqValue, "overlap-authority-product", "overlap-from", "overlap-to", 1, 1, kind, reason, "", "2026-10-15T00:00:00Z", invalidatedSeq)
		return err
	}
	if err := insertResolution("fixture-depends-on-active", productSeq, ResolutionDependsOn, "fixture active sequencing settlement", nil); err != nil {
		t.Fatal(err)
	}
	if err := insertResolution("fixture-depends-on-invalidated", fromSeq, ResolutionDependsOn, "fixture historical sequencing settlement", toSeq); err != nil {
		t.Fatal(err)
	}
	if err := insertResolution("probe-compatible-with", toSeq, ResolutionCompatibleWith, "fixture probe of the old check", nil); err == nil {
		t.Fatal("the prefix schema admitted an empty compatible_with approval_ref; the prefix is not the pre-widening shape")
	} else if strings.Contains(err.Error(), "workflow_overlap_resolutions is fold-only") {
		t.Fatalf("the fold guard, not the old approval_ref check, refused the prefix probe: %v", err)
	}

	if _, err := tx.Exec(`DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	// The seeded prefix premise: exactly the two depends_on rows, both with
	// an empty approval_ref, one still active and one historically
	// invalidated.
	var seededRows int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM workflow_overlap_resolutions`).Scan(&seededRows); err != nil {
		t.Fatal(err)
	}
	if seededRows != 2 {
		t.Fatalf("prefix holds %d resolution rows, want the two seeded depends_on shapes", seededRows)
	}
	var activeKind, activeRef string
	var activeInvalidated sql.NullInt64
	if err := db.QueryRowContext(ctx, `SELECT resolution_kind, approval_ref, invalidated_seq FROM workflow_overlap_resolutions WHERE resolution_id='fixture-depends-on-active'`).Scan(&activeKind, &activeRef, &activeInvalidated); err != nil {
		t.Fatal(err)
	}
	if activeKind != ResolutionDependsOn || activeRef != "" || activeInvalidated.Valid {
		t.Fatalf("active fixture = %s/%q/invalidated=%v, want depends_on with an empty reference and no invalidation", activeKind, activeRef, activeInvalidated.Valid)
	}
	var historicalKind, historicalRef string
	var historicalInvalidated sql.NullInt64
	if err := db.QueryRowContext(ctx, `SELECT resolution_kind, approval_ref, invalidated_seq FROM workflow_overlap_resolutions WHERE resolution_id='fixture-depends-on-invalidated'`).Scan(&historicalKind, &historicalRef, &historicalInvalidated); err != nil {
		t.Fatal(err)
	}
	if historicalKind != ResolutionDependsOn || historicalRef != "" || !historicalInvalidated.Valid {
		t.Fatalf("invalidated fixture = %s/%q/invalidated=%v, want depends_on with an empty reference and a recorded invalidation", historicalKind, historicalRef, historicalInvalidated.Valid)
	}

	// Capture the genuine pre-target state: every row value, the column
	// inventory, the foreign keys, the dependent indexes and triggers, the
	// whole event log, and the manifest.
	beforeRows := resolutionRowDump(t, db)
	beforeColumns := tableColumnInventory(t, db, "workflow_overlap_resolutions")
	beforeForeignKeys := foreignKeyDeclarations(t, db, "workflow_overlap_resolutions")
	beforeObjectList := overlapSchemaObjects(t, db)
	beforeObjects := renderSchemaObjects(beforeObjectList)
	beforeEvents := domainEventDump(t, db)
	beforeReceipts := histEventRows(t, &Store{db: db})
	beforeManifest := manifestDump(t, db)
	present := map[string]bool{}
	for _, object := range beforeObjectList {
		present[object.name] = true
	}
	for _, name := range []string{
		"workflow_overlap_resolutions_pair",
		"workflow_overlap_resolutions_reverse_pair",
		"workflow_overlap_resolutions_guard_insert",
		"workflow_overlap_resolutions_guard_update",
		"workflow_overlap_resolutions_guard_delete",
	} {
		if !present[name] {
			t.Fatalf("the prefix is missing the migration-91 object %s", name)
		}
	}

	// Advance through the real runner: the pending tail records itself, the
	// target included.
	advanceHistoricalPrefix(t, db, migrations[target].Version)

	// The rebuild's TEMP backup is scratch state on the single pooled
	// connection: a successful migration leaves none of it behind.
	var leftoverBackups int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_temp_master WHERE type='table' AND name='workflow_overlap_resolutions_v129_backup'`).Scan(&leftoverBackups); err != nil {
		t.Fatal(err)
	}
	if leftoverBackups != 0 {
		t.Fatalf("the migration left the workflow_overlap_resolutions_v129_backup TEMP table in sqlite_temp_master; the rebuild must drop its scratch backup")
	}

	// Foreign-key integrity across the rebuilt table.
	var violations int
	checkRows, err := db.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	for checkRows.Next() {
		violations++
	}
	if err := checkRows.Err(); err != nil {
		t.Fatal(err)
	}
	checkRows.Close()
	if violations != 0 {
		t.Fatalf("foreign_key_check reported %d violations after the migration", violations)
	}

	// The migration preserved everything it touched: the seeded rows byte
	// for byte, the column inventory, the foreign keys, and the dependent
	// indexes and trigger definitions.
	assertUnchangedDump(t, "workflow_overlap_resolutions rows", beforeRows, resolutionRowDump(t, db))
	assertUnchangedDump(t, "workflow_overlap_resolutions columns", beforeColumns, tableColumnInventory(t, db, "workflow_overlap_resolutions"))
	assertUnchangedDump(t, "workflow_overlap_resolutions foreign keys", beforeForeignKeys, foreignKeyDeclarations(t, db, "workflow_overlap_resolutions"))
	assertUnchangedDump(t, "workflow_overlap_resolutions indexes and guards", beforeObjects, renderSchemaObjects(overlapSchemaObjects(t, db)))
	assertUnchangedDump(t, "domain event bytes", beforeEvents, domainEventDump(t, db))

	// The manifest grew append-only and records the target's actual
	// checksum, name, and compatibility bit as the manifest itself defines
	// them — never a pinned literal.
	afterManifest := manifestDump(t, db)
	if len(afterManifest) <= len(beforeManifest) || !slices.Equal(afterManifest[:len(beforeManifest)], beforeManifest) {
		t.Fatalf("the manifest was rewritten across the migration:\nbefore: %q\nafter:  %q", beforeManifest, afterManifest)
	}
	var recordedName, recordedChecksum string
	var recordedBreaking int
	if err := db.QueryRowContext(ctx, `SELECT name, checksum, breaking FROM schema_migrations WHERE version=?`, migrations[target].Version).Scan(&recordedName, &recordedChecksum, &recordedBreaking); err != nil {
		t.Fatalf("the target migration recorded no manifest row: %v", err)
	}
	want := migrations[target]
	if recordedName != want.Name || recordedChecksum != want.checksum() || recordedBreaking != boolInt(want.Breaking) {
		t.Fatalf("recorded target row = %s/%s/breaking=%d, want %s/%s/breaking=%d", recordedName, recordedChecksum, recordedBreaking, want.Name, want.checksum(), boolInt(want.Breaking))
	}

	// Without a write guard the rebuilt table still refuses every direct
	// write, proving the three guards survived the rebuild.
	insertGuardProbe := func() error {
		_, err := db.ExecContext(ctx, `INSERT INTO workflow_overlap_resolutions(resolution_id,event_seq,product_id,from_work_id,to_work_id,from_contract_version,to_contract_version,resolution_kind,reason,approval_ref,created_at,invalidated_seq) VALUES('probe-guard',?,?,?,?,?,?,'depends_on','probe without a fold guard','','2026-10-15T00:00:00Z',NULL)`,
			toSeq, "overlap-authority-product", "overlap-from", "overlap-to", 1, 1)
		return err
	}
	requireGuardRefusal(t, insertGuardProbe(), "insert")
	requireGuardRefusal(t, func() error {
		_, err := db.ExecContext(ctx, `UPDATE workflow_overlap_resolutions SET reason='probe' WHERE resolution_id='fixture-depends-on-active'`)
		return err
	}(), "update")
	requireGuardRefusal(t, func() error {
		_, err := db.ExecContext(ctx, `DELETE FROM workflow_overlap_resolutions WHERE resolution_id='fixture-depends-on-active'`)
		return err
	}(), "delete")

	// Under normal test-local fold ownership the widened check admits an
	// empty approval_ref for each routine kind and refuses one for both
	// destructive kinds. Schema probes only: the destructive kinds' real
	// consumed-approval coverage stays in internal/agent.
	foldTx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = foldTx.Rollback() }()
	if _, err := foldTx.Exec(`INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{ResolutionCompatibleWith, ResolutionDependsOn, ResolutionBlocks} {
		probeID := "probe-" + kind
		if _, err := foldTx.ExecContext(ctx, `INSERT INTO workflow_overlap_resolutions(resolution_id,event_seq,product_id,from_work_id,to_work_id,from_contract_version,to_contract_version,resolution_kind,reason,approval_ref,created_at,invalidated_seq) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
			probeID, toSeq, "overlap-authority-product", "overlap-from", "overlap-to", 1, 1, kind, "probe of the widened routine check", "", "2026-10-15T00:00:00Z", nil); err != nil {
			t.Fatalf("the widened table refused an empty %s approval_ref: %v", kind, err)
		}
		var recordedKind, recordedRef string
		if err := foldTx.QueryRowContext(ctx, `SELECT resolution_kind, approval_ref FROM workflow_overlap_resolutions WHERE resolution_id=?`, probeID).Scan(&recordedKind, &recordedRef); err != nil {
			t.Fatal(err)
		}
		if recordedKind != kind || recordedRef != "" {
			t.Fatalf("widened-table probe row = %s/%q, want %s with an empty reference", recordedKind, recordedRef, kind)
		}
		if _, err := foldTx.ExecContext(ctx, `DELETE FROM workflow_overlap_resolutions WHERE resolution_id=?`, probeID); err != nil {
			t.Fatalf("remove the %s probe row: %v", kind, err)
		}
	}
	for _, kind := range []string{ResolutionMergedInto, ResolutionSupersedes} {
		_, err := foldTx.ExecContext(ctx, `INSERT INTO workflow_overlap_resolutions(resolution_id,event_seq,product_id,from_work_id,to_work_id,from_contract_version,to_contract_version,resolution_kind,reason,approval_ref,created_at,invalidated_seq) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
			"probe-"+kind, toSeq, "overlap-authority-product", "overlap-from", "overlap-to", 1, 1, kind, "probe of the destructive empty-claim refusal", "", "2026-10-15T00:00:00Z", nil)
		requireSchemaRefusal(t, err, "an empty "+kind+" approval_ref claim")
	}
	if _, err := foldTx.Exec(`DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	if err := foldTx.Commit(); err != nil {
		t.Fatal(err)
	}

	// The full tail intentionally replaces the log FK and guard in 151.
	// Preserve the resolution rows and every surviving acknowledgment.
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	assertUnchangedDump(t, "resolution rows through current head", beforeRows, resolutionRowDump(t, db))
	assertHistoricalReceipts(t, db, beforeReceipts)
	if _, err := db.Exec(`UPDATE workflow_overlap_resolutions SET reason=reason`); err == nil || !strings.Contains(err.Error(), "write-seam-only") {
		t.Fatalf("current guard lost: %v", err)
	}
	// A second full pass is a no-op.
	beforeSecondManifest := manifestDump(t, db)
	beforeSecondRows := resolutionRowDump(t, db)
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("second migration pass: %v", err)
	}
	assertUnchangedDump(t, "manifest across the idempotent second pass", beforeSecondManifest, manifestDump(t, db))
	assertUnchangedDump(t, "workflow_overlap_resolutions rows across the idempotent second pass", beforeSecondRows, resolutionRowDump(t, db))
}

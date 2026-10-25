package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

// Historical migration fixtures seed the pre-150 row shape, not current writers.
func seedPreQ7WorkCreated(t *testing.T, tx *sql.Tx, event Event) {
	t.Helper()
	var payload workCreatedPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	stamp := event.OccurredAt.UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(t.Context(), `INSERT INTO work_items(id,kind,title,lifecycle,priority,version,created_at,updated_at) VALUES(?,?,?,'needed',?,1,?,?)`, event.SubjectID, payload.WorkKind, payload.Title, *payload.Priority, stamp, stamp); err != nil {
		t.Fatal(err)
	}
}

const q7CurrentMetadataFixtureReversal = `
ALTER TABLE workflow_phase_exits DROP COLUMN record_version;
ALTER TABLE workflow_phase_exits DROP COLUMN contract_version;
ALTER TABLE workflow_phase_exits DROP COLUMN step_id;
ALTER TABLE workflow_phase_exits DROP COLUMN actor;
ALTER TABLE relations DROP COLUMN record_version;
ALTER TABLE relations DROP COLUMN reason;
DROP INDEX effect_receipts_relation_seq;
ALTER TABLE work_items DROP COLUMN transition_version;
ALTER TABLE work_items DROP COLUMN transition_seq;
ALTER TABLE work_items DROP COLUMN transition_evidence_refs;
ALTER TABLE work_items DROP COLUMN transition_actor;
ALTER TABLE work_items DROP COLUMN transition_reason;
ALTER TABLE work_items DROP COLUMN transition_from;
`

func seedQ7CurrentRows(t *testing.T) (*Store, string) {
	t.Helper()
	const work = "q7-all-current-rows"
	s, _ := seedCompletionGateCase(t, work, completionGateCase{requiredEvidence: []string{"verification", "review"}})
	seedWork(t, s, "q7-related")
	version := readWorkVersion(t, s, work)
	if err := applyWorkEvent(t, s, relationAddedEvent("q7-related-edge", "depends_on", work, "q7-related", version, version+1), workVersion(work, version)); err != nil {
		t.Fatal(err)
	}
	seedWorkflowAuthority(t, s, "q7-proof-producer", work, "principal/q7-proof", "request/q7-proof", []string{"evidence:q7-proof"})
	version = readWorkVersion(t, s, work)
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{workflowEvent("q7-proof-binding", WorkflowEvidenceBound, work, map[string]any{
		"work_id": work, "expected_version": version, "resulting_version": version + 1,
		"evidence_kind": "verification", "immutable_subject_ref": "evidence:q7-proof", "producer_id": "principal/q7-proof",
		"producer_run_ref": "q7-proof-producer", "producer_watermark": "request/q7-proof", "observed_at": "2026-10-01T00:00:00Z",
	})}}); err != nil {
		t.Fatal(err)
	}
	return s, work
}

func TestQ7CurrentRowsWithoutLog(t *testing.T) {
	t.Parallel()
	s, work := seedQ7CurrentRows(t)
	before, err := s.QueryQ7(t.Context(), Q7Request{Work: work, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]bool{}
	for _, entry := range before.Entries {
		kinds[entry.EntryKind] = true
	}
	for _, kind := range q7RecordKinds {
		if !kinds[kind] {
			t.Fatalf("fixture lacks %s", kind)
		}
	}
	if _, err := s.db.Exec(`ALTER TABLE domain_events RENAME TO hidden_domain_events`); err != nil {
		t.Fatal(err)
	}
	after, err := s.QueryQ7(t.Context(), Q7Request{Work: work, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before.Result, after.Result) {
		t.Fatalf("log removal changed current history")
	}
}

func TestQ7CurrentMetadataMigration150Parity(t *testing.T) {
	t.Parallel()
	s, work := seedQ7CurrentRows(t)
	seedWork(t, s, "q7-migrate-next")
	v := readWorkVersion(t, s, "q7-related")
	if err := applyWorkEvent(t, s, workSupersededEvent("q7-migrate-superseded", "q7-migrate-next", "q7-related", v, v+1), workVersion("q7-related", v)); err != nil {
		t.Fatal(err)
	}
	queries := []string{
		`SELECT json_array(id,transition_from,transition_reason,transition_actor,transition_evidence_refs,transition_seq,transition_version) FROM work_items ORDER BY id`,
		`SELECT json_array(id,reason,record_version) FROM relations ORDER BY id`,
		`SELECT json_array(work_id,action_id,actor,step_id,contract_version,record_version) FROM workflow_phase_exits ORDER BY work_id,action_id`,
	}
	wantRows := workerFactsQueryRows(t, s, queries)
	want, err := s.QueryQ7(t.Context(), Q7Request{Work: work, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	repopulateQ7CurrentMetadata(t, s)
	got, err := s.QueryQ7(t.Context(), Q7Request{Work: work, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Result, want.Result) {
		t.Fatalf("migration150 diverged from current writers:\ngot %+v\nwant %+v", got.Entries, want.Entries)
	}
	if gotRows := workerFactsQueryRows(t, s, queries); !reflect.DeepEqual(gotRows, wantRows) {
		t.Fatalf("migration150 metadata:\ngot %v\nwant %v", gotRows, wantRows)
	}
}

func repopulateQ7CurrentMetadata(t *testing.T, s *Store) {
	t.Helper()
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(q7CurrentMetadataFixtureReversal); err != nil {
		t.Fatal(err)
	}
	for _, m := range migrations {
		if m.Version == 150 {
			if err := applyMigration(t.Context(), tx, m); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestQ7WorkflowLifecycleStartAndSupersessionMetadata(t *testing.T) {
	t.Parallel()
	s, work := seedQ7CurrentRows(t)
	read := func() HistoryEntry {
		t.Helper()
		page, err := s.QueryQ7(t.Context(), Q7Request{Work: work, RecordKinds: []string{"lifecycle"}})
		if err != nil || len(page.Entries) != 1 {
			t.Fatalf("lifecycle page: %+v, %v", page.Entries, err)
		}
		return page.Entries[0]
	}
	started := read()
	if started.Lifecycle != "in_progress" || started.From == nil || *started.From != "needed" || started.Version <= 1 {
		t.Fatalf("first workflow action lost lifecycle metadata: %+v", started)
	}
	repopulateQ7CurrentMetadata(t, s)
	if got := read(); !reflect.DeepEqual(got, started) {
		t.Fatalf("start backfill: got %+v, want %+v", got, started)
	}
	seedWork(t, s, "q7-workflow-successor")
	v := readWorkVersion(t, s, work)
	if err := applyWorkEvent(t, s, workSupersededEvent("q7-active-supersession", "q7-workflow-successor", work, v, v+1), workVersion(work, v)); err != nil {
		t.Fatal(err)
	}
	want := read()
	if want.From == nil || *want.From != "in_progress" {
		t.Fatalf("supersession lost prior lifecycle: %+v", want)
	}
	repopulateQ7CurrentMetadata(t, s)
	if got := read(); !reflect.DeepEqual(got, want) {
		t.Fatalf("supersession backfill: got %+v, want %+v", got, want)
	}
}

func TestQ7RemovedRelationDoesNotBecomeHistory(t *testing.T) {
	t.Parallel()
	s, work := seedQ7CurrentRows(t)
	v := readWorkVersion(t, s, work)
	if err := applyWorkEvent(t, s, relationRemovedEvent("q7-remove-edge", "depends_on", work, "q7-related", v, v+1), workVersion(work, v)); err != nil {
		t.Fatal(err)
	}
	out, err := s.QueryQ7(t.Context(), Q7Request{Work: work, RecordKinds: []string{"relation"}})
	if err != nil {
		t.Fatal(err)
	}
	if out.Entries == nil || len(out.Entries) != 0 || out.NextCursor != nil {
		t.Fatalf("removed relation survived through its receipt: %+v", out)
	}
}

func TestQ7CreationMetadataDoesNotUseUnrelatedFrom(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	const work = "q7-created-from"
	seedWork(t, s, "q7-creation-peer")
	event := workCreatedEvent(work, "q7-created-from-event")
	var fields map[string]any
	if err := json.Unmarshal(event.Payload, &fields); err != nil {
		t.Fatal(err)
	}
	fields["from"] = "unrelated-source"
	event.Payload, _ = json.Marshal(fields)
	if err := ApplyOperation(t.Context(), s, Operation{
		Events: []Event{event, operationEvent("q7-created-membership", "work_project.added", SubjectWorkItem, work, map[string]any{
			"work_id": work, "project_id": "project", "role": "primary", "reason": "test", "expected_version": 1, "resulting_version": 2,
		})},
		ExpectedVersions: workVersion(work, 0),
	}); err != nil {
		t.Fatal(err)
	}
	want, err := s.QueryQ7(t.Context(), Q7Request{Work: work})
	if err != nil || len(want.Entries) != 1 || want.Entries[0].From != nil {
		t.Fatalf("creation metadata: %+v, %v", want.Entries, err)
	}
	repopulateQ7CurrentMetadata(t, s)
	got, err := s.QueryQ7(t.Context(), Q7Request{Work: work})
	if err != nil || !reflect.DeepEqual(got.Result, want.Result) {
		t.Fatalf("creation metadata backfill: %+v, %v", got.Entries, err)
	}
}

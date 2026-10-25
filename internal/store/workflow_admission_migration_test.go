package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

type admissionMigrationExitKey struct{ work, family, step, action string }
type admissionMigrationActorKey struct{ work, actor string }
type admissionMigrationDisplay struct {
	seq        int64
	action, at string
}
type admissionMigrationFacts struct {
	exits  map[admissionMigrationExitKey]int64
	actors map[admissionMigrationActorKey]admissionMigrationDisplay
}

// Derive the old selections from source rows, independently of the maintained
// facts and migration's grouped/window queries. Sequence selects the prior pin
// and the latest member of a completion class; timestamp text selects display.
func admissionMigrationLegacyFacts(t *testing.T, s *Store) admissionMigrationFacts {
	t.Helper()
	got := admissionMigrationFacts{map[admissionMigrationExitKey]int64{}, map[admissionMigrationActorKey]admissionMigrationDisplay{}}
	rows, err := s.db.Query(`SELECT e.subject_id,e.kind,e.actor,e.seq,e.occurred_at,
	 json_extract(e.payload,'$.ref'),json_extract(e.payload,'$.step_id'),json_extract(e.payload,'$.action_id'),
	 COALESCE(json_extract(e.payload,'$.action_id'),e.kind)
	 FROM domain_events e JOIN work_items w ON w.id=e.subject_id
	 WHERE e.subject_type='work_item' AND e.kind IN
	 ('workflow.definition_selected','workflow.action_started','workflow.action_completed','workflow.action_checkpointed','workflow.action_failed')
	 ORDER BY e.seq`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	families := map[string]string{}
	for rows.Next() {
		var work, kind, actor, at, display string
		var seq int64
		var family, step, action sql.NullString
		if err := rows.Scan(&work, &kind, &actor, &seq, &at, &family, &step, &action, &display); err != nil {
			t.Fatal(err)
		}
		if kind == WorkflowDefinitionSelected {
			families[work] = family.String
			continue
		}
		if kind == WorkflowActionCompleted {
			if !step.Valid || !action.Valid || step.String == "" || action.String == "" {
				t.Fatalf("completed source row %d has no valid step/action class", seq)
			}
			got.exits[admissionMigrationExitKey{work, families[work], step.String, action.String}] = seq
		}
		key := admissionMigrationActorKey{work, actor}
		prior, found := got.actors[key]
		if !found || at > prior.at || at == prior.at && seq > prior.seq {
			got.actors[key] = admissionMigrationDisplay{seq, display, at}
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return got
}

func admissionMigrationCurrentFacts(t *testing.T, s *Store) admissionMigrationFacts {
	t.Helper()
	got := admissionMigrationFacts{map[admissionMigrationExitKey]int64{}, map[admissionMigrationActorKey]admissionMigrationDisplay{}}
	rows, err := s.db.Query(`SELECT work_id,definition_ref,step_id,action_id,exit_seq FROM workflow_completed_exits`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var key admissionMigrationExitKey
		var seq int64
		if err := rows.Scan(&key.work, &key.family, &key.step, &key.action, &seq); err != nil {
			t.Fatal(err)
		}
		got.exits[key] = seq
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	rows, err = s.db.Query(`SELECT work_id,actor_ref,last_effect_seq,last_action_id,last_acted_at FROM workflow_coordinator_actions`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var key admissionMigrationActorKey
		var display admissionMigrationDisplay
		if err := rows.Scan(&key.work, &key.actor, &display.seq, &display.action, &display.at); err != nil {
			t.Fatal(err)
		}
		got.actors[key] = display
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return got
}

func admissionMigrationRequireFacts(t *testing.T, s *Store, want admissionMigrationFacts) {
	t.Helper()
	if got := admissionMigrationCurrentFacts(t, s); !reflect.DeepEqual(got, want) {
		t.Fatalf("admission facts = %#v\nlegacy selections = %#v", got, want)
	}
}

// Supplemental histories isolate ordering and class identity. They append the
// legacy source and invoke the same current writers as the folds, not ad hoc
// INSERTs into the new tables. The pinned journey below covers fold integration.
func admissionMigrationAppend(t *testing.T, s *Store, work, actor, kind, payload, stamp string) int64 {
	t.Helper()
	ctx := t.Context()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := enterWrite(ctx, tx); err != nil {
		t.Fatal(err)
	}
	var next int64
	if err := tx.QueryRow(`SELECT last+1 FROM store_sequences WHERE name='effect'`).Scan(&next); err != nil {
		t.Fatal(err)
	}
	at, err := time.Parse(time.RFC3339Nano, stamp)
	if err != nil {
		t.Fatal(err)
	}
	event := Event{EventID: fmt.Sprintf("admission-migration-%d", next), Kind: kind, SubjectType: SubjectWorkItem, SubjectID: work, Actor: actor, OccurredAt: at, PayloadVersion: 1, Payload: json.RawMessage(payload)}
	seq, err := appendPreparedEvent(ctx, tx, event)
	if err != nil {
		t.Fatal(err)
	}
	event.Seq = seq
	if kind == WorkflowActionCompleted {
		var p workflowActionCompletedPayload
		if err := json.Unmarshal(event.Payload, &p); err != nil {
			t.Fatal(err)
		}
		var family string
		if err := tx.QueryRow(`SELECT COALESCE((SELECT json_extract(payload,'$.ref') FROM domain_events WHERE subject_type='work_item' AND subject_id=? AND kind='workflow.definition_selected' AND seq<=? ORDER BY seq DESC LIMIT 1),'')`, work, event.Seq).Scan(&family); err != nil {
			t.Fatal(err)
		}
		if err := writeWorkflowCompletedExitTx(ctx, tx, event, p, family); err != nil {
			t.Fatal(err)
		}
	}
	if err := writeWorkflowCoordinatorActionTx(ctx, tx, event); err != nil {
		t.Fatal(err)
	}
	if err := leaveWrite(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return event.Seq
}

func admissionMigration149(t *testing.T) migration {
	t.Helper()
	for _, m := range migrations {
		if m.Version == 149 {
			if m.Name != "workflow_admission_facts" {
				t.Fatalf("migration149 = %q", m.Name)
			}
			return m
		}
	}
	t.Fatal("migration149 is missing")
	return migration{}
}

func admissionMigrationDrop149(t *testing.T, s *Store) {
	t.Helper()
	if _, err := s.db.Exec(`DROP TABLE workflow_completed_exits; DROP TABLE workflow_coordinator_actions; DELETE FROM schema_migrations WHERE version=149`); err != nil {
		t.Fatal(err)
	}
}

func admissionMigrationApply149(t *testing.T, s *Store) error {
	t.Helper()
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := applyMigration(t.Context(), tx, admissionMigration149(t)); err != nil {
		return err
	}
	return tx.Commit()
}

func TestWorkflowAdmissionMigration149Parity(t *testing.T) {
	const work = "admission-migration-journey"
	fixture := seedWorkflowReturnRouteFixture(t, work, "workflow.break_fix", "repair")
	s := fixture.store
	want := admissionMigrationLegacyFacts(t, s)
	if len(want.exits) == 0 || len(want.actors) == 0 {
		t.Fatal("pinned journey must produce real completed actions and coordinator facts")
	}
	admissionMigrationRequireFacts(t, s, want)

	// Exercise each remaining action fold and check immediately, so a later
	// completion cannot hide a missing started/checkpointed/failed writer.
	actor, err := WorkflowActorRef(fixture.owner)
	if err != nil {
		t.Fatal(err)
	}
	step := workflowStep(pinnedWorkflowDefinitionForTest(t, s, work), "repair")
	for i, tc := range []struct {
		kind   string
		fields map[string]any
	}{
		{WorkflowActionStarted, map[string]any{"action_id": "dispatch_worker", "accepted_inputs_digest": "sha256:" + strings.Repeat("a", 64), "idempotency_identity": "admission-migration-start"}},
		{WorkflowActionCheckpointed, map[string]any{"step_kind": string(step.Kind), "checkpoint_payload": map[string]any{}, "request_id": "admission-migration-checkpoint"}},
		{WorkflowActionFailed, map[string]any{"failure_kind": "timeout", "recoverable": true}},
	} {
		version := readWorkVersion(t, s, work)
		p := tc.fields
		p["work_id"], p["expected_version"], p["resulting_version"] = work, version, version+1
		p["step_id"], p["attempt_epoch"], p["actor_ref"] = "repair", 1, actor
		event := workflowEventWithActor(fmt.Sprintf("admission-migration-real-%d", i), tc.kind, work, actor, p)
		event.OccurredAt = time.Date(2027, 1, 1, 0, 0, i, 0, time.UTC)
		if err := applyWorkflowTestOperationDirect(t.Context(), s, Operation{Events: []Event{event}, ExpectedVersions: workVersion(work, version)}); err != nil {
			t.Fatalf("real %s fold: %v", tc.kind, err)
		}
		want = admissionMigrationLegacyFacts(t, s)
		admissionMigrationRequireFacts(t, s, want)
	}

	const supplement = "admission-migration-supplement"
	seedWork(t, s, supplement)
	appendRow := func(actor, kind, payload, at string) int64 {
		return admissionMigrationAppend(t, s, supplement, actor, kind, payload, at)
	}
	const earlier = "2026-01-01T00:00:00Z"
	const later = "2028-01-01T00:00:00Z"
	const report = `{"step_id":"findings","action_id":"record_report"}`
	missingPin := appendRow("actor/classes", WorkflowActionCompleted, report, later)
	appendRow("actor/pin", WorkflowDefinitionSelected, `{"ref":"workflow.research","version":1}`, earlier)
	appendRow("actor/classes", WorkflowActionCompleted, report, earlier)
	appendRow("actor/classes", WorkflowActionCompleted, `{"step_id":"findings","action_id":"accept_worker_result","review_advance_held":true}`, earlier)
	admissionMigrationRequireFacts(t, s, admissionMigrationLegacyFacts(t, s))
	local := appendRow("actor/classes", WorkflowActionCompleted, `{"step_id":"findings","action_id":"accept_worker_result","worker_job":{"job_id":"job:local","revision":1,"digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}`, earlier)
	appendRow("actor/pin", WorkflowDefinitionSelected, `{"ref":"workflow.implementation","version":1}`, earlier)
	otherFamily := appendRow("actor/classes", WorkflowActionCompleted, report, earlier)
	appendRow("actor/pin", WorkflowDefinitionSelected, `{"ref":"workflow.research","version":2}`, earlier)
	appendRow("actor/classes", WorkflowActionCompleted, `{"step_id":"investigate","action_id":"record_report"}`, earlier)
	beforeRepeats := admissionMigrationCurrentFacts(t, s)
	var latest int64
	for range 32 {
		latest = appendRow("actor/classes", WorkflowActionCompleted, report, earlier)
	}
	if got := admissionMigrationCurrentFacts(t, s); len(got.exits) != len(beforeRepeats.exits) || len(got.actors) != len(beforeRepeats.actors) {
		t.Fatalf("repeated class grew facts: before=%#v after=%#v", beforeRepeats, got)
	}

	appendRow("actor/order", WorkflowActionStarted, `{"action_id":"first"}`, later)
	tie := appendRow("actor/order", WorkflowActionCheckpointed, `{"action_id":"tie-winner"}`, later)
	appendRow("actor/order", WorkflowActionFailed, `{"action_id":"older-but-higher-seq"}`, earlier)
	appendRow("actor/null", WorkflowActionFailed, `{"action_id":null}`, earlier)
	appendRow("actor/missing", WorkflowActionCheckpointed, `{}`, earlier)
	appendRow("actor/empty", WorkflowActionStarted, `{"action_id":""}`, earlier)
	// The legacy display compares RFC3339Nano text, not parsed instants.
	lexical := appendRow("actor/lexical", WorkflowActionStarted, `{"action_id":"whole-second"}`, later)
	appendRow("actor/lexical", WorkflowActionFailed, `{"action_id":"fractional-second"}`, "2028-01-01T00:00:00.1Z")
	appendRow("actor/noise", WorkflowActorRecorded, `{"action_id":"not-an-action"}`, later)

	want = admissionMigrationLegacyFacts(t, s)
	for key, seq := range map[admissionMigrationExitKey]int64{
		{supplement, "", "findings", "record_report"}:                         missingPin,
		{supplement, "workflow.research", "findings", "record_report"}:        latest,
		{supplement, "workflow.implementation", "findings", "record_report"}:  otherFamily,
		{supplement, "workflow.research", "findings", "accept_worker_result"}: local,
	} {
		if want.exits[key] != seq {
			t.Fatalf("legacy class %v = %d; want %d", key, want.exits[key], seq)
		}
	}
	for actor, display := range map[string]admissionMigrationDisplay{
		"actor/classes": {missingPin, "record_report", later},
		"actor/order":   {tie, "tie-winner", later},
		"actor/lexical": {lexical, "whole-second", later},
	} {
		if got := want.actors[admissionMigrationActorKey{supplement, actor}]; got != display {
			t.Fatalf("legacy display %s = %+v; want %+v", actor, got, display)
		}
	}
	for actor, action := range map[string]string{"actor/null": WorkflowActionFailed, "actor/missing": WorkflowActionCheckpointed, "actor/empty": ""} {
		if got := want.actors[admissionMigrationActorKey{supplement, actor}].action; got != action {
			t.Fatalf("COALESCE display for %s = %q; want %q", actor, got, action)
		}
	}
	if _, found := want.actors[admissionMigrationActorKey{supplement, "actor/noise"}]; found {
		t.Fatal("non-action event became a coordinator action")
	}
	admissionMigrationRequireFacts(t, s, want)
	before := admissionMigrationCurrentFacts(t, s)
	admissionMigrationGuards(t, s, supplement, "write-seam-only")
	admissionMigrationDrop149(t, s)
	if err := admissionMigrationApply149(t, s); err != nil {
		t.Fatalf("apply only migration149: %v", err)
	}
	admissionMigrationRequireFacts(t, s, want)
	admissionMigrationRequireFacts(t, s, before)
	assertFoldGuardEmpty(t, s)
	admissionMigrationGuards(t, s, supplement, "fold-only")

	// Deleting a work must remove its current facts without deleting another
	// work's facts. Retained source rows must not resurrect an orphan on backfill.
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := enterWrite(t.Context(), tx); err != nil {
		t.Fatal(err)
	}
	// The fixture's Project membership independently restricts work deletion.
	if _, err := tx.Exec(`DELETE FROM work_projects WHERE work_id=?`, supplement); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`DELETE FROM work_items WHERE id=?`, supplement); err != nil {
		t.Fatalf("cascade admission facts: %v", err)
	}
	if err := leaveWrite(t.Context(), tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	admissionMigrationRequireFacts(t, s, admissionMigrationLegacyFacts(t, s))
	for key := range admissionMigrationCurrentFacts(t, s).exits {
		if key.work == supplement {
			t.Fatal("deleted work retained a completed-exit fact")
		}
	}
	for key := range admissionMigrationCurrentFacts(t, s).actors {
		if key.work == supplement {
			t.Fatal("deleted work retained a coordinator fact")
		}
	}
	admissionMigrationDrop149(t, s)
	if err := admissionMigrationApply149(t, s); err != nil {
		t.Fatal(err)
	}
	admissionMigrationRequireFacts(t, s, admissionMigrationLegacyFacts(t, s))
}

func admissionMigrationGuards(t *testing.T, s *Store, work, refusal string) {
	t.Helper()
	before := admissionMigrationCurrentFacts(t, s)
	for _, query := range []string{
		`INSERT INTO workflow_completed_exits VALUES(?, 'family', 'step', 'action', 1)`,
		`UPDATE workflow_completed_exits SET exit_seq=exit_seq+1 WHERE work_id=?`,
		`DELETE FROM workflow_completed_exits WHERE work_id=?`,
		`INSERT INTO workflow_coordinator_actions VALUES(?, 'actor/guard', 1, 'action', '2026-01-01T00:00:00Z')`,
		`UPDATE workflow_coordinator_actions SET last_effect_seq=last_effect_seq+1 WHERE work_id=?`,
		`DELETE FROM workflow_coordinator_actions WHERE work_id=?`,
	} {
		if _, err := s.db.Exec(query, work); err == nil || !strings.Contains(err.Error(), "is "+refusal) {
			t.Fatalf("unguarded %s: %v; want %s refusal", query, err, refusal)
		}
	}
	admissionMigrationRequireFacts(t, s, before)
	assertFoldGuardEmpty(t, s)
}

type admissionMigrationWriteProbe struct {
	migrationRunner
	writes int
}

func (q *admissionMigrationWriteProbe) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	q.writes++
	return q.migrationRunner.ExecContext(ctx, query, args...)
}

func TestWorkflowAdmissionMigration149RejectsMissingCompletedClass(t *testing.T) {
	for _, payload := range []string{
		`{"step_id":"findings"}`,
		`{"step_id":"findings","action_id":null}`,
		`{"action_id":"record_report"}`,
		`{"step_id":null,"action_id":"record_report"}`,
	} {
		t.Run(payload, func(t *testing.T) {
			s := openTemp(t)
			seedWork(t, s, "admission-invalid-source")
			insertHistoricalEvent(t, s, Event{EventID: "admission-invalid-completion", Kind: WorkflowActionCompleted, SubjectType: SubjectWorkItem, SubjectID: "admission-invalid-source", Actor: "actor/legacy", OccurredAt: time.Unix(20, 0).UTC(), PayloadVersion: 1, Payload: json.RawMessage(payload)})
			admissionMigrationDrop149(t, s)
			tx, err := s.db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			probe := &admissionMigrationWriteProbe{migrationRunner: tx}
			err = applyMigration(t.Context(), probe, admissionMigration149(t))
			assertFailureKind(t, err, KindInvariantViolation)
			for _, identity := range []string{"admission-invalid-source", "admission-invalid-completion", "sequence"} {
				if !strings.Contains(err.Error(), identity) {
					t.Fatalf("corrupt completion refusal = %v; missing %s", err, identity)
				}
			}
			if probe.writes != 0 {
				t.Fatalf("migration wrote %d times before refusing corrupt source", probe.writes)
			}
			if err := tx.Rollback(); err != nil {
				t.Fatal(err)
			}
			var tables, manifest, source int
			if err := s.db.QueryRow(`SELECT
			 (SELECT count(*) FROM sqlite_master WHERE type='table' AND name IN ('workflow_completed_exits','workflow_coordinator_actions')),
			 (SELECT count(*) FROM schema_migrations WHERE version=149),
			 (SELECT count(*) FROM domain_events WHERE event_id='admission-invalid-completion')`).Scan(&tables, &manifest, &source); err != nil {
				t.Fatal(err)
			}
			if tables != 0 || manifest != 0 || source != 1 {
				t.Fatalf("failed migration left tables=%d manifest=%d source=%d; want 0,0,1", tables, manifest, source)
			}
			assertFoldGuardEmpty(t, s)
		})
	}
}

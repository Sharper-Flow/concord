package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"
)

func admissionExplicitCorpus() historicalFactCorpus {
	const work = "admission-corpus"
	c := historicalFactCorpus{parents: func(t *testing.T, s *Store, g string) { corpusWorkParents(t, s, g, work) }}
	add := func(actor, kind, raw, stamp string) {
		e := corpusEvent(fmt.Sprintf("admission:%d", len(c.events)+1), kind, work, json.RawMessage(raw))
		e.Actor = actor
		e.OccurredAt, _ = time.Parse(time.RFC3339Nano, stamp)
		c.events = append(c.events, e)
	}
	const early = "2026-01-01T00:00:00Z"
	const late = "2028-01-01T00:00:00Z"
	const report = `{"step_id":"findings","action_id":"record_report"}`
	add("actor/classes", WorkflowActionCompleted, report, late)
	add("actor/pin", WorkflowDefinitionSelected, `{"ref":"workflow.research","version":1}`, early)
	add("actor/classes", WorkflowActionCompleted, report, early)
	add("actor/classes", WorkflowActionCompleted, `{"step_id":"findings","action_id":"accept_worker_result","review_advance_held":true}`, early)
	add("actor/classes", WorkflowActionCompleted, `{"step_id":"findings","action_id":"accept_worker_result","worker_job":{"job_id":"job:local","revision":1,"digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}`, early)
	add("actor/pin", WorkflowDefinitionSelected, `{"ref":"workflow.implementation","version":1}`, early)
	add("actor/classes", WorkflowActionCompleted, report, early)
	add("actor/pin", WorkflowDefinitionSelected, `{"ref":"workflow.research","version":2}`, early)
	add("actor/classes", WorkflowActionCompleted, `{"step_id":"investigate","action_id":"record_report"}`, early)
	for range 32 {
		add("actor/classes", WorkflowActionCompleted, report, early)
	}
	add("actor/order", WorkflowActionStarted, `{"action_id":"first"}`, late)
	add("actor/order", WorkflowActionCheckpointed, `{"action_id":"tie-winner"}`, late)
	add("actor/order", WorkflowActionFailed, `{"action_id":"older-but-higher-seq"}`, early)
	add("actor/null", WorkflowActionFailed, `{"action_id":null}`, early)
	add("actor/missing", WorkflowActionCheckpointed, `{}`, early)
	add("actor/empty", WorkflowActionStarted, `{"action_id":""}`, early)
	add("actor/lexical", WorkflowActionStarted, `{"action_id":"whole-second"}`, late)
	add("actor/lexical", WorkflowActionFailed, `{"action_id":"fractional-second"}`, "2028-01-01T00:00:00.1Z")
	add("actor/noise", WorkflowActorRecorded, `{"action_id":"not-an-action"}`, late)
	family := ""
	c.write = func(ctx context.Context, tx *sql.Tx, e Event) error {
		if e.Kind == WorkflowDefinitionSelected {
			var p struct {
				Ref string `json:"ref"`
			}
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				return err
			}
			family = p.Ref
			return nil
		}
		if e.Kind == WorkflowActionCompleted {
			var p workflowActionCompletedPayload
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				return err
			}
			if err := writeWorkflowCompletedExitTx(ctx, tx, e, p, family); err != nil {
				return err
			}
		}
		return writeWorkflowCoordinatorActionTx(ctx, tx, e)
	}
	return c
}

// Independent Go selection over the authored corpus, never over maintained rows.
func admissionCorpusOracle(events []Event) admissionMigrationFacts {
	out := admissionMigrationFacts{map[admissionMigrationExitKey]int64{}, map[admissionMigrationActorKey]admissionMigrationDisplay{}}
	families := map[string]string{}
	for i, e := range events {
		var p map[string]any
		_ = json.Unmarshal(e.Payload, &p)
		if e.Kind == WorkflowDefinitionSelected {
			families[e.SubjectID], _ = p["ref"].(string)
			continue
		}
		if e.Kind != WorkflowActionStarted && e.Kind != WorkflowActionCompleted && e.Kind != WorkflowActionCheckpointed && e.Kind != WorkflowActionFailed {
			continue
		}
		seq := int64(i + 1)
		action, ok := p["action_id"].(string)
		if !ok {
			action = e.Kind
		}
		if e.Kind == WorkflowActionCompleted {
			step, _ := p["step_id"].(string)
			out.exits[admissionMigrationExitKey{e.SubjectID, families[e.SubjectID], step, action}] = seq
		}
		key := admissionMigrationActorKey{e.SubjectID, e.Actor}
		stamp := e.OccurredAt.UTC().Format(time.RFC3339Nano)
		old, found := out.actors[key]
		if !found || stamp > old.at || stamp == old.at && seq > old.seq {
			out.actors[key] = admissionMigrationDisplay{seq, action, stamp}
		}
	}
	return out
}

func assertAdmissionExplicitCorpus(t *testing.T) {
	t.Helper()
	c := admissionExplicitCorpus()
	live := c.current(t)
	want := admissionCorpusOracle(c.events)
	admissionMigrationRequireFacts(t, live, want)
	historical := c.prefix(t, 148)
	if err := Migrate(t.Context(), historical.db); err != nil {
		t.Fatal(err)
	}
	admissionMigrationRequireFacts(t, historical, want)
	admissionMigrationGuards(t, historical, "admission-corpus", "write-seam-only")
	queries := []string{`SELECT json_array(work_id,actor_ref,last_effect_seq,last_action_id,last_acted_at) FROM workflow_coordinator_actions ORDER BY work_id,actor_ref`}
	rows := workerFactsQueryRows(t, live, queries)
	assertCorpusSelectionRed(t, c, 149, "ORDER BY e.occurred_at DESC,e.seq DESC", "ORDER BY e.seq DESC,e.occurred_at DESC", queries, rows)
	for _, s := range []*Store{live, historical} {
		if _, err := s.db.Exec(`INSERT INTO write_guard VALUES(1); DELETE FROM work_items WHERE id='admission-corpus'; DELETE FROM write_guard`); err != nil {
			t.Fatal(err)
		}
		got := admissionMigrationCurrentFacts(t, s)
		if len(got.exits) != 0 || len(got.actors) != 0 {
			t.Fatalf("deleted work retained admission facts: %+v", got)
		}
	}
	// Backfill must not resurrect facts from the retained removed-work source.
	orphan := c.prefix(t, 148)
	if _, err := orphan.db.Exec(`INSERT INTO fold_guard VALUES(1); DELETE FROM work_items WHERE id='admission-corpus'; DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(t.Context(), orphan.db); err != nil {
		t.Fatal(err)
	}
	if got := admissionMigrationCurrentFacts(t, orphan); !reflect.DeepEqual(got, admissionMigrationCurrentFacts(t, historical)) {
		t.Fatal("orphan source resurrected admission facts")
	}
}

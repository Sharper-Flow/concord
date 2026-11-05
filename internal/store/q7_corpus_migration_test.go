package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
)

func q7ExplicitCorpus(t *testing.T) historicalFactCorpus {
	t.Helper()
	const work = "q7-corpus"
	base := seedWorkflowReturnRouteFixture(t, work, WorkflowStepPlanResearch, "investigate")
	actor, err := WorkflowActorRef(base.owner)
	if err != nil {
		t.Fatal(err)
	}
	version := readWorkVersion(t, base.store, work)
	c := historicalFactCorpus{}
	add := func(kind string, p map[string]any) {
		p["work_id"], p["expected_version"], p["resulting_version"] = work, version, version+1
		p["reason"], p["evidence_refs"] = "Explicit corpus transition", []string{"evidence:q7-corpus"}
		if kind == WorkflowActionCompleted {
			delete(p, "reason")
			delete(p, "evidence_refs")
		}
		e := corpusEvent(fmt.Sprintf("q7-corpus:%d", len(c.events)+1), kind, work, p)
		e.Actor = actor
		c.events = append(c.events, e)
		version++
	}
	add(WorkflowContractApproved, map[string]any{"contract_version": 1})
	// Creation, two distinct needed intervals, and a later non-starting action.
	add("work.created", map[string]any{"from": "unrelated", "raised_from_work_id": "q7-corpus-related"})
	add(WorkflowActionCheckpointed, map[string]any{"step_id": "findings", "attempt_epoch": 1})
	add(WorkflowActionCheckpointed, map[string]any{"step_id": "findings", "attempt_epoch": 1})
	add("work.transitioned", map[string]any{"from": "in_progress", "to": "cancelled"})
	add("work.reopened", map[string]any{"from": "cancelled"})
	add(WorkflowActionCheckpointed, map[string]any{"step_id": "findings", "attempt_epoch": 2})
	add(WorkflowActionCompleted, map[string]any{"step_id": "findings", "attempt_epoch": 2, "action_id": "checkpoint_context", "actor_ref": actor})
	add(WorkflowActionCheckpointed, map[string]any{"step_id": "findings", "attempt_epoch": 3})
	add("workflow.outside_repair_reconciled", map[string]any{"from": "in_progress", "mode": "completed"})
	// Both routine overlap destinations use the correct subject and resulting
	// version; the terminal supersession kinds are retired (CON-1571 end
	// state 2), so the corpus carries one depends_on resolution.
	add(WorkflowOverlapResolved, map[string]any{"resolution_kind": "depends_on", "to_work_id": "q7-corpus-related", "to_resulting_version": 2})
	add("relation.added", map[string]any{"relation_kind": "depends_on", "from_work_id": work, "to_work_id": "q7-corpus-related"})
	add("workflow.successor_linked", map[string]any{"successor_work_id": "q7-corpus-next"})
	for i, kind := range []string{"work.created", WorkflowActionCheckpointed, WorkflowActionCheckpointed} {
		e := corpusEvent(fmt.Sprintf("q7-start-only:%d", i), kind, "q7-start-only", map[string]any{"expected_version": max(i, 1), "resulting_version": i + 1, "step_id": "findings", "attempt_epoch": 1})
		e.Actor = actor
		c.events = append(c.events, e)
	}
	c.parents = func(t *testing.T, s *Store, guard string) {
		copyFixtureDataToPrefix(t, base.store, s.db, []string{"products", "projects", "product_projects", "work_items", "work_projects", "workflow_actors", "workflow_instances", "workflow_contracts"}, map[string]map[string]string{
			"work_items":         {"priority": "0", "urgency": "'standard'", "lifecycle": "'needed'", "transition_from": "NULL", "transition_reason": "''", "transition_actor": "''", "transition_evidence_refs": "'[]'", "transition_seq": "0", "transition_version": "1"},
			"workflow_instances": {"outside_repair_state": "NULL", "definition_ref": "'workflow.research'", "definition_digest": "'" + testManifestDigest + "'", "definition_version": "26", "current_step": "'findings'", "result_disposition": "''", "latest_non_dispatch_start_seq": "0", "unhealthy_verdict_seq": "0"}, "workflow_contracts": {"required_evidence": "'[]'", "definition_ref": "'workflow.research'", "definition_digest": "'" + testManifestDigest + "'", "definition_version": "26", "law_change": "NULL"},
		}, guard)
		corpusWorkParents(t, s, guard, "q7-corpus-related", "q7-corpus-next", "q7-start-only")
		if guard != "fold_guard" {
			return
		}
		if _, err := s.db.Exec(`INSERT INTO fold_guard VALUES(1)`); err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.Exec(`UPDATE work_items SET lifecycle=CASE id WHEN 'q7-start-only' THEN 'in_progress' ELSE 'superseded' END WHERE id IN ('q7-corpus','q7-corpus-related','q7-start-only')`); err != nil {
			t.Fatal(err)
		}
		// Relation identity is the source effect's position, including gaps
		// reserved by lifecycle/overlap effects that insert no standing edge.
		var ordinal int
		for i, e := range c.events {
			var source map[string]any
			if err := json.Unmarshal(e.Payload, &source); err != nil {
				t.Fatal(err)
			}
			if e.Kind == "work.created" && source["raised_from_work_id"] != nil || e.Kind == WorkflowOverlapResolved || e.Kind == "relation.added" || e.Kind == "workflow.successor_linked" {
				ordinal++
			}
			if e.Kind == WorkflowActionCompleted {
				if _, err := s.db.Exec(`INSERT INTO workflow_phase_exits(work_id,action_id,exit_seq) VALUES(?,'checkpoint_context',?)`, work, i+1); err != nil {
					t.Fatal(err)
				}
			}
			if e.Kind != "relation.added" && e.Kind != "workflow.successor_linked" {
				continue
			}
			to, kind := "q7-corpus-related", "depends_on"
			if e.Kind == "workflow.successor_linked" {
				to, kind = "q7-corpus-next", "forward_link"
			}
			if _, err := s.db.Exec(`INSERT INTO relations(id,work_id_from,work_id_to,kind,created_at) VALUES(?,?,?,?,?)`, ordinal, work, to, kind, e.OccurredAt); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := s.db.Exec(`DELETE FROM fold_guard`); err != nil {
			t.Fatal(err)
		}
	}
	c.write = func(ctx context.Context, tx *sql.Tx, e Event) error {
		var p struct {
			Expected      int64  `json:"expected_version"`
			Resulting     int64  `json:"resulting_version"`
			From          string `json:"from"`
			To            string `json:"to"`
			Resolution    string `json:"resolution_kind"`
			Target        string `json:"to_work_id"`
			TargetVersion int64  `json:"to_resulting_version"`
		}
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return err
		}
		switch e.Kind {
		case WorkflowContractApproved, "work.created":
			// These source effects precede the first interval. Their pre-target
			// work/contract rows are declared parent input on both routes.
			_, err := tx.ExecContext(ctx, `UPDATE work_items SET version=? WHERE id=?`, p.Resulting, e.SubjectID)
			return err
		case WorkflowActionCheckpointed:
			if err := beginWorkflowLifecycleTx(ctx, tx, e); err != nil {
				return err
			}
			return advanceWorkflowVersion(ctx, tx, e, WorkflowVersionFields{WorkID: e.SubjectID, ExpectedVersion: &p.Expected, ResultingVersion: &p.Resulting})
		case WorkflowActionCompleted:
			// This source effect was already admitted historically. Project its
			// retained facts without treating it as a new terminal action.
			var completed workflowActionCompletedPayload
			if err := json.Unmarshal(e.Payload, &completed); err != nil {
				return err
			}
			if err := advanceWorkflowVersion(ctx, tx, e, completed.WorkflowVersionFields); err != nil {
				return err
			}
			return persistWorkflowCompletionExitTx(ctx, tx, e, completed)
		case "work.transitioned":
			return updateWorkLifecycle(ctx, tx, e, p.To, p.Expected, p.Resulting)
		case "work.reopened":
			return updateWorkLifecycle(ctx, tx, e, "needed", p.Expected, p.Resulting)
		case "workflow.outside_repair_reconciled":
			return updateWorkLifecycle(ctx, tx, e, "completed", p.Expected, p.Resulting)
		case WorkflowOverlapResolved:
			return updateWorkVersionByID(ctx, tx, e.SubjectID, p.Expected, p.Resulting, e.OccurredAt)
		case "relation.added":
			return insertRelation(ctx, tx, e, relationPayload{From: work, To: "q7-corpus-related", Kind: "depends_on"})
		case "workflow.successor_linked":
			return insertRelation(ctx, tx, e, relationPayload{From: work, To: "q7-corpus-next", Kind: "forward_link"})
		}
		return nil
	}
	return c
}

func assertQ7Corpus150(t *testing.T) {
	t.Helper()
	c := q7ExplicitCorpus(t)
	live := c.current(t)
	plan, err := readWorkflowPlanTx(t.Context(), live.db, "q7-corpus")
	if err != nil || plan == nil || plan.Type != WorkflowStepPlanResearch || len(plan.Steps) != 3 || len(plan.Steps[0].Attempts) != 1 || len(plan.Steps[1].Attempts) != 0 {
		t.Fatalf("current plan attempts: plan=%+v err=%v", plan, err)
	}
	old := c.prefix(t, 149)
	queries := []string{
		`SELECT json_array(id,transition_from,transition_reason,transition_actor,transition_evidence_refs,transition_seq,transition_version) FROM work_items ORDER BY id`,
		`SELECT json_array(id,reason,record_version) FROM relations ORDER BY id`,
	}
	want := workerFactsQueryRows(t, live, queries)
	if err := Migrate(outcomeSnapshotContext(t, t.Context(), old.db), old.db); err != nil {
		t.Fatal(err)
	}
	plan, err = readWorkflowPlanTx(t.Context(), old.db, "q7-corpus")
	if err != nil || plan != nil {
		t.Fatalf("migrated historical plan: plan=%+v err=%v", plan, err)
	}
	if got := workerFactsQueryRows(t, old, queries); !reflect.DeepEqual(got, want) {
		for i := range queries {
			if !reflect.DeepEqual(got[i], want[i]) {
				t.Errorf("150 %s: got %v want %v", queries[i], got[i], want[i])
			}
		}
		t.FailNow()
	}
	assertCorpusSelectionRed(t, c, 150, "WHERE first_start=1", "WHERE first_start=2", queries, want)
}

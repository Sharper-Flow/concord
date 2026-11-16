package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
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
	add(WorkflowOverlapResolved, map[string]any{"resolution_kind": "depends_on", "to_work_id": "q7-corpus-related", "to_resulting_version": 2})
	add("relation.added", map[string]any{"relation_kind": "depends_on", "from_work_id": work, "to_work_id": "q7-corpus-related"})
	add("workflow.successor_linked", map[string]any{"successor_work_id": "q7-corpus-next"})
	for i, kind := range []string{"work.created", WorkflowActionCheckpointed, WorkflowActionCheckpointed} {
		e := corpusEvent(fmt.Sprintf("q7-start-only:%d", i), kind, "q7-start-only", map[string]any{"expected_version": max(i, 1), "resulting_version": i + 1, "step_id": "findings", "attempt_epoch": 1})
		e.Actor = actor
		c.events = append(c.events, e)
	}
	c.parents = func(t *testing.T, s *Store, guard string) {
		copyCurrentCorpusParents(t, base.store, s.db, []string{"products", "projects", "product_projects", "work_items", "work_projects", "workflow_actors", "workflow_instances", "workflow_contracts"}, map[string]map[string]string{
			"work_items":         {"priority": "0", "urgency": "'standard'", "lifecycle": "'needed'", "transition_from": "NULL", "transition_reason": "''", "transition_actor": "''", "transition_evidence_refs": "'[]'", "transition_seq": "0", "transition_version": "1"},
			"workflow_instances": {"outside_repair_state": "NULL", "definition_ref": "'workflow.research'", "definition_digest": "'" + testManifestDigest + "'", "definition_version": "26", "current_step": "'findings'", "result_disposition": "''", "latest_non_dispatch_start_seq": "0", "unhealthy_verdict_seq": "0"}, "workflow_contracts": {"premise": "'retired historical fixture'", "route_conventions": "'[]'", "rigor_class": "'prototype_internal'", "decision_domains": "NULL", "required_evidence": "'[]'", "definition_ref": "'workflow.research'", "definition_digest": "'" + testManifestDigest + "'", "definition_version": "26", "law_change": "NULL"},
		}, guard)
		corpusWorkParents(t, s, guard, "q7-corpus-related", "q7-corpus-next", "q7-start-only")
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
	if err != nil || plan == nil || plan.Type != WorkflowStepPlanResearch || len(plan.Steps) != 4 || len(plan.Steps[0].Attempts) != 1 || len(plan.Steps[1].Attempts) != 1 || len(plan.Steps[2].Attempts) != 0 {
		t.Fatalf("current plan attempts: plan=%+v err=%v", plan, err)
	}
}

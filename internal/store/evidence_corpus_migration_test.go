package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func evidenceExplicitCorpus(t *testing.T) historicalFactCorpus {
	t.Helper()
	c := evidenceCorpusForWork(t, "evidence-corpus")
	c.events = append(c.events,
		corpusEvent("removed-evidence", WorkflowEvidenceBound, "removed-work", json.RawMessage(`{}`)),
		corpusEvent("removed-completion", WorkflowActionCompleted, "removed-work", json.RawMessage(`{"action_id":"confirm_premise","step_id":"verify"}`)),
	)
	held := corpusEvent("unbound-held-review", WorkflowActionCompleted, "evidence-corpus", map[string]any{"action_id": "accept_worker_result", "step_id": "verify", "review_advance_held": true})
	held.PayloadVersion = 7
	c.events = append(c.events, held)
	return c
}

func evidenceCorpusForWork(t *testing.T, work string) historicalFactCorpus {
	t.Helper()
	base := seedWorkflowReturnRouteFixtureWithPlan(t, work, WorkflowStepPlanFix, "implement", []string{})
	owner, _ := WorkflowActorRef(base.owner)
	seedWorkflowAuthority(t, base.store, "evidence-corpus-producer", work, "principal/corpus", "request/corpus", []string{"evidence:corpus"})
	version := readWorkVersion(t, base.store, work)
	c := historicalFactCorpus{}
	add := func(kind string, p map[string]any, bump bool) Event {
		p["work_id"] = work
		if kind == "compaction_link.published" {
			delete(p, "work_id")
		}
		if bump {
			p["expected_version"], p["resulting_version"] = version, version+1
			version++
		}
		e := corpusEvent(fmt.Sprintf("evidence-corpus:%d", len(c.events)+1), kind, work, p)
		e.Actor = owner
		e.OccurredAt = time.Unix(int64(1000-len(c.events)), 0).UTC()
		c.events = append(c.events, e)
		return e
	}
	add(WorkflowContractApproved, map[string]any{"contract_version": 1}, false)
	action := func(kind, step, id string, epoch int64, extra map[string]any) Event {
		p := map[string]any{"step_id": step, "attempt_epoch": epoch, "actor_ref": owner}
		if id != "" {
			p["action_id"] = id
		}
		if kind == WorkflowActionStarted {
			p["accepted_inputs_digest"], p["idempotency_identity"] = testManifestDigest, fmt.Sprintf("corpus-start:%d", len(c.events))
		}
		if kind == WorkflowActionFailed {
			p["failure_kind"], p["recoverable"] = "timeout", false
		}
		for k, v := range extra {
			p[k] = v
		}
		return add(kind, p, true)
	}
	action(WorkflowActionStarted, "verify", "checkpoint_context", 1, nil)
	action(WorkflowActionStarted, "verify", "checkpoint_context", 2, nil)
	action(WorkflowActionFailed, "verify", "", 2, nil)
	action(WorkflowActionFailed, "verify", "", 2, nil)
	action(WorkflowActionCompleted, "verify", "checkpoint_context", 2, nil)
	action(WorkflowActionFailed, "verify", "", 2, nil)
	action(WorkflowActionStarted, "verify", "checkpoint_context", 3, nil)
	action(WorkflowActionFailed, "verify", "", 3, nil)
	action(WorkflowActionCompleted, "verify", "checkpoint_context", 3, nil)
	action(WorkflowActionCompleted, "verify", "checkpoint_context", 77, nil)
	for _, id := range []string{"record_finding", "record_decision", "record_delivery"} {
		extra := map[string]any{}
		if id == "record_delivery" {
			extra["delivery_artifact"], extra["delivery_state"] = "file:internal/store/value.go", "asserted"
		}
		action(WorkflowActionCompleted, "verify", id, 77, extra)
	}
	verdict := func(kind string, incomparable bool) {
		add("workflow.verdict_recorded", map[string]any{"contract_version": 1, "predicate_id": "predicate:return-route", "verdict_kind": kind, "verdict_actor_ref": owner, "evaluation_evidence": []string{"evidence:corpus"}, "incomparable_with_approved": incomparable}, true)
	}
	verdict("insufficient_evidence", false)
	verdict("ok", true)
	action(WorkflowActionCompleted, "verify", "record_finding", 78, nil)
	verdict("ok", false)
	action(WorkflowActionCompleted, "verify", "request_correction", 79, map[string]any{"correction_diagnosis": "The recorded criterion needs another observation", "correction_strategy": "Recheck the exact criterion", "correction_predicate_ids": []string{"predicate:return-route"}, "correction_evidence_refs": []string{"evidence:corpus"}})
	verdict("ok", false)
	add(WorkflowEvidenceBound, map[string]any{"evidence_kind": "verification", "immutable_subject_ref": "evidence:corpus", "producer_id": "principal/corpus", "producer_run_ref": "evidence-corpus-producer", "producer_watermark": "request/corpus", "observed_at": "2026-01-01T00:00:00Z"}, true)
	add("work.observation_recorded", map[string]any{"observation_id": "obs:aaaaaaaaaaaaaaaa", "statement": "The corpus records an explicit observation", "refs": []string{}, "tags": []string{}}, false)
	add("compaction_link.published", map[string]any{
		"id": work, "type": "work_note", "title": "Evidence corpus",
		"completed_at": "2026-01-01T00:00:00Z", "outcome_tag": "resolved", "terminal_state": "completed",
		"summary":     "The corpus preserves the published note link",
		"lesson_tags": []string{}, "product_ids": []string{}, "project_ids": []string{}, "domain_ids": []string{}, "tag_ids": []string{},
		"home_project_id": "project/corpus", "home_locator_id": "locator/corpus",
		"note_path": ".concord/docs/work/evidence-corpus.md", "commit_oid": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"content_hash": testManifestDigest, "reason": "Publish the verified work note", "expected_version": version,
	}, false)
	c.events[len(c.events)-1].PayloadVersion = 3
	completed := func() {
		add(WorkflowCompleted, map[string]any{"terminal_state": "completed", "final_verdict_kind": "ok", "verdict_actor_ref": owner, "premise_confirmed": false, "evidence_count": 1, "changed_refs_digest": testManifestDigest, "impact_verdict": "non-breaking"}, true)
	}
	completed()
	assertion := c.events[13]
	correct := func(target Event) {
		var seq int
		for i, e := range c.events {
			if e.EventID == target.EventID {
				seq = i + 1
			}
		}
		add(WorkflowDeliveryCorrected, map[string]any{"target_event_id": target.EventID, "target_seq": seq, "target_payload_version": target.PayloadVersion, "reason": "Use the actual merged delivery", "delivery_artifact": "https://github.com/Sharper-Flow/concord/pull/1339", "delivery_state": "asserted", "evidence_source": "coordinator_asserted"}, true)
	}
	correct(assertion)
	last := action(WorkflowActionCompleted, "repair", "record_delivery", 1, map[string]any{"delivery_artifact": "file:internal/store/next.go", "delivery_state": "asserted"})
	completed()
	correct(last)
	c.parents = func(t *testing.T, s *Store, guard string) {
		copyCurrentCorpusParents(t, base.store, s.db, []string{"products", "projects", "product_projects", "work_items", "work_projects", "workflow_actors", "workflow_instances", "workflow_contracts", "durable_operations"}, map[string]map[string]string{
			"work_items":         {"priority": "0", "urgency": "'standard'"},
			"workflow_instances": {"outside_repair_state": "NULL", "definition_ref": "'workflow.break_fix'", "definition_digest": "'" + testManifestDigest + "'", "definition_version": "26", "current_step": "'verify'", "completion_payload": "NULL", "completion_occurred_at": "NULL", "unhealthy_verdict_seq": "0", "result_disposition": "''", "latest_non_dispatch_start_seq": "0"},
			"workflow_contracts": {"premise": "'retired historical fixture'", "route_conventions": "'[]'", "rigor_class": "'prototype_internal'", "decision_domains": "NULL", "required_evidence": "'[]'", "definition_ref": "'workflow.break_fix'", "definition_digest": "'" + testManifestDigest + "'", "definition_version": "26", "supersession_seq": "0", "last_healthy_seq": "0", "law_change": "NULL"},
			"durable_operations": {"workflow_type_version": "26", "resume_cursor": "NULL"},
		}, guard)
		if _, err := s.db.Exec(`INSERT INTO ` + guard + ` VALUES(1);
INSERT INTO projects(id,display_name,version,created_at,updated_at) VALUES('project/corpus','Corpus',1,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z');
INSERT INTO project_locators(locator_id,project_id,kind,locator_value,normalized_value,created_at,updated_at) VALUES('locator/corpus','project/corpus','git_remote','https://example.invalid/corpus.git','https://example.invalid/corpus','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z');
DELETE FROM ` + guard); err != nil {
			t.Fatal(err)
		}
	}
	c.write = func(ctx context.Context, tx *sql.Tx, e Event) error {
		switch e.Kind {
		case WorkflowActionStarted:
			return foldWorkflowActionStarted(ctx, tx, e)
		case WorkflowActionFailed:
			return foldWorkflowActionFailed(ctx, tx, e)
		case WorkflowActionCompleted:
			return foldWorkflowActionCompleted(ctx, tx, e)
		case WorkflowEvidenceBound:
			return foldWorkflowEvidenceBound(ctx, tx, e)
		case "work.observation_recorded":
			_, err := tx.ExecContext(ctx, `INSERT INTO work_observations(observation_id,work_id,statement,refs,tags,recorded_at) VALUES('obs:aaaaaaaaaaaaaaaa',?,'The corpus records an explicit observation','[]','[]',?)`, e.SubjectID, e.OccurredAt.UTC().Format(time.RFC3339Nano))
			return err
		case WorkflowCompleted:
			return foldWorkflowCompleted(ctx, tx, e)
		}
		return nil
	}
	return c
}

func assertEvidenceCorpus142(t *testing.T) {
	t.Helper()
	c := evidenceExplicitCorpus(t)
	// The retained binding is produced through the current authority check.
	supported := c
	supported.events = nil
	var version int64
	for i, e := range c.events {
		if e.SubjectID != "evidence-corpus" {
			continue
		}
		var p map[string]any
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			t.Fatal(err)
		}
		if version == 0 {
			if initial, ok := p["expected_version"].(float64); ok {
				version = int64(initial)
			}
		}
		if e.Kind != WorkflowEvidenceBound && e.Kind != "compaction_link.published" {
			continue
		}
		if _, bump := p["expected_version"]; bump && e.Kind != "compaction_link.published" {
			p["expected_version"], p["resulting_version"] = version, version+1
			version++
		}
		e.Payload = mustJSONValue(p)
		e.Seq = int64(i + 1)
		supported.events = append(supported.events, e)
	}
	live := supported.current(t)
	plan, err := readWorkflowPlanTx(t.Context(), live.db, "evidence-corpus")
	if err != nil || plan == nil || plan.Type != WorkflowStepPlanFix || len(plan.Steps) != 5 || len(plan.Steps[0].Attempts) != 1 || len(plan.Steps[1].Attempts) != 1 || len(plan.Steps[2].Attempts) != 1 || len(plan.Steps[3].Attempts) != 0 {
		t.Fatalf("current plan attempts: plan=%+v err=%v", plan, err)
	}
	var count int
	if err := live.db.QueryRow(`SELECT count(*) FROM workflow_evidence_bindings WHERE work_id='evidence-corpus'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("current evidence bindings: count=%d err=%v", count, err)
	}
}

func copyCurrentCorpusParents(t *testing.T, source *Store, target *sql.DB, tables []string, overrides map[string]map[string]string, guard string) {
	t.Helper()
	if _, err := target.Exec(`ATTACH DATABASE ? AS corpus_source`, source.path); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := target.Exec(`DETACH DATABASE corpus_source`); err != nil {
			t.Fatal(err)
		}
	}()
	if _, err := target.Exec(`INSERT INTO ` + guard + ` VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	for _, table := range tables {
		rows, err := target.Query(`PRAGMA table_info(` + table + `)`)
		if err != nil {
			t.Fatal(err)
		}
		var columns, values []string
		for rows.Next() {
			var cid, notNull, primaryKey int
			var name, typ string
			var defaultValue any
			if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &primaryKey); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			quoted := `"` + name + `"`
			columns = append(columns, quoted)
			value := quoted
			if override, ok := overrides[table][name]; ok {
				value = override
			}
			values = append(values, value)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			t.Fatal(err)
		}
		query := `INSERT INTO "` + table + `" (` + strings.Join(columns, ",") + `) SELECT ` + strings.Join(values, ",") + ` FROM corpus_source."` + table + `"`
		if _, err := target.Exec(query); err != nil {
			t.Fatalf("copy current corpus table %s: %v", table, err)
		}
	}
	if _, err := target.Exec(`DELETE FROM ` + guard); err != nil {
		t.Fatal(err)
	}
}

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"testing"
	"time"
)

func evidenceExplicitCorpus(t *testing.T) historicalFactCorpus {
	t.Helper()
	c := evidenceCorpusForWork(t, "evidence-corpus")
	c.events = append(c.events,
		corpusEvent("removed-evidence", WorkflowEvidenceBound, "removed-work", json.RawMessage(`{}`)),
		corpusEvent("removed-observation", WorkflowStalenessObserved, "removed-work", json.RawMessage(`{"rule_id":"staleness:removed"}`)),
		corpusEvent("removed-completion", WorkflowActionCompleted, "removed-work", json.RawMessage(`{"action_id":"confirm_premise","step_id":"verify"}`)),
	)
	held := corpusEvent("unbound-held-review", WorkflowActionCompleted, "evidence-corpus", map[string]any{"action_id": "accept_worker_result", "step_id": "verify", "review_advance_held": true})
	held.PayloadVersion = 7
	c.events = append(c.events, held)
	return c
}

func evidenceCorpusForWork(t *testing.T, work string) historicalFactCorpus {
	t.Helper()
	definition := cloneWorkflowDefinition(legacyApprovalPolicyDefinitionForRef(t, "workflow.break_fix").Definition)
	definition.Ref = "workflow.evidence_corpus_fixture"
	var hold WorkflowActionDefinition
	for _, a := range definition.ActionDefinitions {
		if a.ID == "checkpoint_context" {
			hold = a
		}
	}
	for _, id := range []string{"record_finding", "record_decision", "record_delivery"} {
		if !slices.Contains(definition.AvailableActions, id) {
			definition.AvailableActions = append(definition.AvailableActions, id)
			a := hold
			a.ID = id
			a.ExecutionMode = ActionHold
			definition.ActionDefinitions = append(definition.ActionDefinitions, a)
		}
		for i := range definition.ActionDefinitions {
			if definition.ActionDefinitions[i].ID == id {
				definition.ActionDefinitions[i].ExecutionMode = ActionHold
			}
		}
		for i := range definition.StepGraph.Steps {
			if definition.StepGraph.Steps[i].ID == "verify" || definition.StepGraph.Steps[i].ID == "repair" {
				if !slices.Contains(definition.StepGraph.Steps[i].Actions, id) {
					definition.StepGraph.Steps[i].Actions = append(definition.StepGraph.Steps[i].Actions, id)
				}
			}
		}
	}
	for i := range definition.StepGraph.Steps {
		if definition.StepGraph.Steps[i].ID == "verify" && !slices.Contains(definition.StepGraph.Steps[i].Actions, "dispatch_worker") {
			definition.StepGraph.Steps[i].Actions = append(definition.StepGraph.Steps[i].Actions, "dispatch_worker")
		}
	}
	registered, err := BuiltinWorkflowRegistry().Register(definition)
	if err != nil {
		t.Fatal(err)
	}
	base := seedWorkflowReturnRouteFixtureWithDefinition(t, work, registered, "repair", []string{}, []string{})
	owner, _ := WorkflowActorRef(base.owner)
	worker, _ := WorkflowActorRef(base.operator)
	seedWorkflowAuthority(t, base.store, "evidence-corpus-producer", work, "principal/corpus", "request/corpus", []string{"evidence:corpus"})
	version := readWorkVersion(t, base.store, work)
	c := historicalFactCorpus{}
	add := func(kind string, p map[string]any, bump bool) Event {
		p["work_id"] = work
		if kind == WorkflowStalenessObserved || kind == "compaction_link.published" {
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
	action(WorkflowActionStarted, "verify", "dispatch_worker", 1, nil)
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
		add(WorkflowVerdictRecorded, map[string]any{"contract_version": 1, "predicate_id": "predicate:return-route", "verdict_kind": kind, "verdict_actor_ref": owner, "evaluation_evidence": []string{"evidence:corpus"}, "incomparable_with_approved": incomparable}, true)
	}
	verdict("insufficient_evidence", false)
	verdict("ok", true)
	action(WorkflowActionCompleted, "verify", "record_finding", 78, nil)
	verdict("ok", false)
	action(WorkflowActionCompleted, "verify", "request_correction", 79, map[string]any{"correction_diagnosis": "The recorded criterion needs another observation", "correction_strategy": "Recheck the exact criterion", "correction_predicate_ids": []string{"predicate:return-route"}, "correction_evidence_refs": []string{"evidence:corpus"}})
	verdict("ok", false)
	add(WorkflowEvidenceBound, map[string]any{"evidence_kind": "verification", "immutable_subject_ref": "evidence:corpus", "producer_id": "principal/corpus", "producer_run_ref": "evidence-corpus-producer", "producer_watermark": "request/corpus", "observed_at": "2026-01-01T00:00:00Z"}, true)
	for _, drifted := range []bool{true, false} {
		add(WorkflowStalenessObserved, map[string]any{"rule_id": "staleness:corpus", "severity": "warning", "drifted": drifted, "accepted_inputs_digest": testManifestDigest, "observed_at": "2026-01-01T00:00:00Z"}, false)
	}
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
	for range 2 {
		add(WorkerDispatched, map[string]any{"lane_actor_ref": worker}, false)
	}
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
		add(WorkflowDeliveryCorrected, map[string]any{"target_event_id": target.EventID, "target_seq": seq, "target_payload_version": target.PayloadVersion, "reason": "Use the actual merged delivery", "delivery_artifact": "https://github.com/Sharper-Flow/concord/pull/1339", "delivery_state": "asserted", "evidence_source": DeliveryEvidenceSourceCoordinatorAsserted}, true)
	}
	correct(assertion)
	last := action(WorkflowActionCompleted, "repair", "record_delivery", 1, map[string]any{"delivery_artifact": "file:internal/store/next.go", "delivery_state": "asserted"})
	completed()
	correct(last)
	c.parents = func(t *testing.T, s *Store, guard string) {
		copyFixtureDataToPrefix(t, base.store, s.db, []string{"products", "projects", "product_projects", "work_items", "work_projects", "workflow_actors", "workflow_instances", "workflow_contracts", "workflow_contract_predicates", "durable_operations"}, map[string]map[string]string{
			"workflow_instances": {"definition_version": "26", "current_step": "'verify'", "completion_payload": "NULL", "completion_occurred_at": "NULL", "unhealthy_verdict_seq": "0"},
			"workflow_contracts": {"definition_version": "26", "supersession_seq": "0", "last_healthy_seq": "0"},
			"durable_operations": {"workflow_type_version": "26"},
		}, guard)
		if _, err := s.db.Exec(`INSERT INTO ` + guard + ` VALUES(1);
INSERT INTO projects(id,display_name,version,created_at,updated_at) VALUES('project/corpus','Corpus',1,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z');
INSERT INTO project_locators(locator_id,project_id,kind,locator_value,normalized_value,created_at,updated_at) VALUES('locator/corpus','project/corpus','git_remote','https://example.invalid/corpus.git','https://example.invalid/corpus','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z');
DELETE FROM ` + guard); err != nil {
			t.Fatal(err)
		}
		if guard != "fold_guard" {
			return
		}
		if _, err := s.db.Exec(`INSERT INTO fold_guard VALUES(1); INSERT INTO workflow_step_epochs(work_id,step_id,attempt_epoch) VALUES(?,'verify',3); INSERT INTO work_observations(observation_id,work_id,statement,refs,tags,recorded_at) VALUES('obs:aaaaaaaaaaaaaaaa',?,'The corpus records an explicit observation','[]','[]','2026-01-01T00:00:00Z'); DELETE FROM fold_guard`, work, work); err != nil {
			t.Fatal(err)
		}
	}
	c.write = func(ctx context.Context, tx *sql.Tx, e Event) error {
		switch e.Kind {
		case WorkflowActionStarted:
			return foldWorkflowActionStarted(ctx, tx, e, nil)
		case WorkflowActionFailed:
			return foldWorkflowActionFailed(ctx, tx, e, nil)
		case WorkflowActionCompleted:
			return foldWorkflowActionCompleted(ctx, tx, e, nil)
		case WorkflowVerdictRecorded:
			return foldWorkflowVerdictRecorded(ctx, tx, e)
		case WorkflowEvidenceBound:
			return foldWorkflowEvidenceBound(ctx, tx, e)
		case WorkflowStalenessObserved:
			return foldWorkflowStalenessObserved(ctx, tx, e)
		case "work.observation_recorded":
			return foldWorkObservationRecorded(ctx, tx, e)
		case WorkerDispatched:
			var p WorkerDispatchedPayload
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				return err
			}
			return pinWorkerDispatchedLaneActor(ctx, tx, e, p)
		case WorkflowCompleted:
			return foldWorkflowCompleted(ctx, tx, e, nil)
		case WorkflowDeliveryCorrected:
			return foldWorkflowDeliveryCorrected(ctx, tx, e)
		case "compaction_link.published":
			return foldCompactionLinkPublished(ctx, tx, e)
		}
		return nil
	}
	return c
}

func assertEvidenceCorpus142(t *testing.T) {
	t.Helper()
	c := evidenceExplicitCorpus(t)
	// Historical verdicts lack the current integrated-candidate proof. Check
	// their selections without bypassing the current criterion producer.
	supported := c
	supported.events = nil
	var version int64
	for i, e := range c.events {
		if e.SubjectID != "evidence-corpus" {
			continue
		}
		switch e.Kind {
		case WorkflowContractApproved, WorkflowVerdictRecorded, WorkflowCompleted, WorkflowDeliveryCorrected:
			continue
		}
		var p map[string]any
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			t.Fatal(err)
		}
		if e.Kind == WorkflowActionCompleted && (p["action_id"] == "request_correction" || p["step_id"] == "repair") {
			continue
		}
		if p["review_advance_held"] == true {
			continue
		}
		if _, bump := p["expected_version"]; bump && e.Kind != "compaction_link.published" {
			if version == 0 {
				version = int64(p["expected_version"].(float64))
			}
			p["expected_version"], p["resulting_version"] = version, version+1
			version++
		}
		e.Payload = mustJSONValue(p)
		e.Seq = int64(i + 1)
		supported.events = append(supported.events, e)
	}
	live := supported.current(t)
	old := c.prefix(t, 141)
	queries := []string{
		`SELECT json_array(work_id,step_id,attempt_epoch,latest_start_seq,latest_start_event_id,json_remove(latest_start_payload,'$.expected_version','$.resulting_version'),latest_dispatch_start_seq,latest_dispatch_start_event_id,json_remove(latest_dispatch_start_payload,'$.expected_version','$.resulting_version')) FROM workflow_step_epochs ORDER BY work_id,step_id`,
		`SELECT json_array(work_id,action_id,exit_seq) FROM workflow_phase_exits WHERE action_id NOT IN ('request_correction','record_delivery') ORDER BY work_id,action_id`,
		`SELECT json_array(work_id,step_id,action_id,production_seq) FROM workflow_artifact_actions WHERE step_id='verify' ORDER BY work_id,step_id,action_id`,
		`SELECT json_array(work_id,contract_version,supersession_seq) FROM workflow_contracts ORDER BY work_id,contract_version`,
		`SELECT json_array(work_id,binding_seq,binding_id,actor,json_remove(payload,'$.expected_version','$.resulting_version')) FROM workflow_evidence_bindings ORDER BY work_id,binding_seq`,
		`SELECT json_array(work_id,step_id,attempt_epoch,state,actor_ref,effect_seq,json_remove(payload,'$.expected_version','$.resulting_version')) FROM workflow_action_obligations ORDER BY work_id,effect_seq`,
		`SELECT json_array(actor_ref,first_dispatch_seq) FROM workflow_worker_actors ORDER BY actor_ref`,
		`SELECT json_array(observation_id,recorded_seq) FROM work_observations ORDER BY observation_id`,
	}
	want := workerFactsQueryRows(t, live, queries)
	advanceHistoricalPrefix(t, old.db, 152)
	assertEvidenceLegacySelections(t, old, c.events)
	if err := Migrate(t.Context(), old.db); err != nil {
		t.Fatal(err)
	}
	if got := workerFactsQueryRows(t, old, queries); !reflect.DeepEqual(got, want) {
		for i := range queries {
			if !reflect.DeepEqual(got[i], want[i]) {
				t.Errorf("142 %s: got %v want %v", queries[i], got[i], want[i])
			}
		}
		t.FailNow()
	}
	assertCorpusSelectionRed(t, c, 142, "AND c.seq>e.seq", "AND c.seq<e.seq", queries[5:6], want[5:6])
}

func TestEvidenceExplicitCorpusMigration142(t *testing.T) {
	assertEvidenceCorpus142(t)
	assertEvidenceSupersession142(t)
}

func assertEvidenceSupersession142(t *testing.T) {
	t.Helper()
	const work = "supersession-corpus"
	definition := cloneWorkflowDefinition(legacyApprovalPolicyDefinitionForRef(t, "workflow.research").Definition)
	definition.Ref = "workflow.supersession_corpus_fixture"
	registered, err := BuiltinWorkflowRegistry().Register(definition)
	if err != nil {
		t.Fatal(err)
	}
	base := seedWorkflowReturnRouteFixtureWithDefinition(t, work, registered, "findings", []string{}, []string{})
	version := readWorkVersion(t, base.store, work)
	actor, err := WorkflowActorRef(base.owner)
	if err != nil {
		t.Fatal(err)
	}
	c := historicalFactCorpus{parents: func(t *testing.T, s *Store, guard string) {
		copyFixtureDataToPrefix(t, base.store, s.db, []string{"products", "projects", "product_projects", "work_items", "work_projects", "workflow_actors", "workflow_instances", "workflow_contracts", "workflow_contract_predicates"}, map[string]map[string]string{"workflow_instances": {"definition_version": "26"}, "workflow_contracts": {"definition_version": "26"}}, guard)
		if guard == "write_guard" {
			// This current corpus starts without a law home. Capture that
			// explicit state before its first effect; historical prefixes
			// retain their absent baseline through migration.
			if err := s.Transact(t.Context(), func(tx *Transaction) error {
				scope, err := beginWrite(t.Context(), tx.tx)
				if err != nil {
					return err
				}
				if err := initializeWorkflowLawBaselineTx(t.Context(), tx.tx, work); err != nil {
					return err
				}
				return scope.close(t.Context())
			}); err != nil {
				t.Fatal(err)
			}
		}
	}}
	e := corpusEvent("supersession-corpus:successor", WorkflowContractSuperseded, work, map[string]any{
		"work_id": work, "expected_version": version, "resulting_version": version + 1,
		"previous_contract_version": 1, "new_contract_version": 2, "predecessor_contract_versions": []int64{1}, "supersede_reason": "Use the next approved contract", "audit_evidence": []string{"evidence:corpus"},
		"successor_contract": map[string]any{
			"contract_version": 2, "premise": "deliver the checked change", "consequence_class": "internal_sqlite",
			"required_evidence": []string{}, "route_conventions": []string{}, "spec_mandate": []string{}, "law_modifies": []string{},
			"law_revisions": []WorkflowLawRevision{}, "law_boundary_version": 1, "rigor_class": "prototype_internal", "decision_domains": []string{},
			"outcome_predicates": []map[string]any{{"predicate_id": "predicate:return-route", "ordinal": 0, "outcome_kind": "outcome", "outcome_payload": map[string]any{"kind": "outcome", "allowed": []string{"report_recorded"}}}},
		},
	})
	e.Actor = actor
	e.PayloadVersion = eventKindRegistry[WorkflowContractSuperseded].CurrentVersion
	c.events = []Event{e}
	parents := c.parents
	c.parents = func(t *testing.T, s *Store, guard string) {
		parents(t, s, guard)
		if _, err := s.db.Exec(`INSERT INTO `+guard+` VALUES(1); DELETE FROM workflow_step_epochs WHERE work_id=?; DELETE FROM `+guard, work); err != nil {
			t.Fatal(err)
		}
		if guard != "fold_guard" {
			return
		}
		// These are predecessor-owned data columns, not migration-142 outputs.
		if _, err := s.db.Exec(`INSERT INTO fold_guard VALUES(1);
INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,rigor_class,law_modifies,law_boundary_version,definition_ref,definition_version)
SELECT work_id,2,premise,consequence_class,required_evidence,route_conventions,?,?,spec_mandate,rigor_class,law_modifies,law_boundary_version,definition_ref,definition_version FROM workflow_contracts WHERE work_id=? AND contract_version=1;
INSERT INTO workflow_contract_predicates(work_id,contract_version,predicate_id,ordinal,outcome_kind,outcome_payload) SELECT work_id,2,predicate_id,ordinal,outcome_kind,outcome_payload FROM workflow_contract_predicates WHERE work_id=? AND contract_version=1;
UPDATE workflow_contracts SET superseded_by=2 WHERE work_id=? AND contract_version=1;
DELETE FROM fold_guard`, e.OccurredAt.UTC().Format(time.RFC3339Nano), e.Actor, work, work, work); err != nil {
			t.Fatal(err)
		}
	}
	c.write = func(ctx context.Context, tx *sql.Tx, e Event) error {
		return foldWorkflowContractSuperseded(ctx, tx, e, nil)
	}
	live := c.current(t)
	old := c.prefix(t, 141)
	if err := Migrate(t.Context(), old.db); err != nil {
		t.Fatal(err)
	}
	queries := []string{`SELECT json_array(contract_version,supersession_seq,last_healthy_seq) FROM workflow_contracts WHERE work_id='supersession-corpus' ORDER BY contract_version`}
	want := [][]string{{`[1,0,0]`, `[2,1,0]`}}
	for _, s := range []*Store{live, old} {
		if got := workerFactsQueryRows(t, s, queries); !reflect.DeepEqual(got, want) {
			t.Fatalf("supersession boundary: got %v want %v", got, want)
		}
	}
}

func assertEvidenceLegacySelections(t *testing.T, s *Store, events []Event) {
	t.Helper()
	row := func(query string, want []any, args ...any) {
		t.Helper()
		var got string
		if err := s.db.QueryRow(query, args...).Scan(&got); err != nil {
			t.Fatal(err)
		}
		var actual, expected any
		if err := json.Unmarshal([]byte(got), &actual); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(mustJSONValue(want), &expected); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(actual, expected) {
			t.Fatalf("legacy142 %s: got %s want %s", query, got, mustJSONValue(want))
		}
	}
	var lastVerdict, lastCompletion, lastAssertion, lastCorrection Event
	var verdictSeq, unhealthySeq, correctionSeq, priorCorrection, completionSeq, assertionSeq int64
	var unboundAcceptance int64
	type production struct {
		step, action   string
		seq, atVerdict int64
	}
	artifacts := map[string]production{}
	exits := map[string]int64{}
	steps := map[string]int64{}
	var correctionEvent Event
	for i, e := range events {
		if e.SubjectID != "evidence-corpus" {
			continue
		}
		seq := int64(i + 1)
		var p map[string]any
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			t.Fatal(err)
		}
		switch e.Kind {
		case WorkflowActionCompleted:
			action, _ := p["action_id"].(string)
			step, _ := p["step_id"].(string)
			steps[step] = seq
			if action == "accept_worker_result" && p["worker_job"] == nil {
				unboundAcceptance = seq
			}
			if action == "accept_worker_result" && p["review_advance_held"] == true {
				continue
			}
			if action == "request_correction" {
				correctionSeq = seq
				correctionEvent = e
			}
			exits[action] = seq
			if action == "record_finding" || action == "record_decision" || action == "record_delivery" {
				key := step + ":" + action
				previous := artifacts[key]
				previous.step, previous.action, previous.seq = step, action, seq
				artifacts[key] = previous
			}
			if p["delivery_state"] == "asserted" && p["delivery_artifact"] != "" {
				lastAssertion, assertionSeq = e, seq
				lastCorrection = Event{}
			}
		case WorkflowVerdictRecorded:
			lastVerdict, verdictSeq, priorCorrection = e, seq, correctionSeq
			if p["verdict_kind"] != "ok" || p["incomparable_with_approved"] == true {
				unhealthySeq = seq
			}
			for key, v := range artifacts {
				v.atVerdict = v.seq
				artifacts[key] = v
			}
		case WorkflowCompleted:
			lastCompletion, completionSeq = e, seq
		case WorkflowDeliveryCorrected:
			if p["target_event_id"] == lastAssertion.EventID {
				lastCorrection = e
			}
		}
	}
	if verdictSeq == 0 || completionSeq == 0 || assertionSeq == 0 || lastCorrection.EventID == "" || unhealthySeq == 0 || priorCorrection == 0 {
		t.Fatal("legacy corpus omitted a verdict, completion, correction, or delivery selection population")
	}
	row(`SELECT json_array(verdict_seq,actor,payload,prior_correction_seq) FROM workflow_predicate_verdicts WHERE work_id='evidence-corpus' AND contract_version=1 AND predicate_id='predicate:return-route'`, []any{verdictSeq, lastVerdict.Actor, string(lastVerdict.Payload), priorCorrection})
	row(`SELECT json_array(completion_payload,completion_occurred_at,unhealthy_verdict_seq) FROM workflow_instances WHERE work_id='evidence-corpus'`, []any{string(lastCompletion.Payload), lastCompletion.OccurredAt.Format(time.RFC3339Nano), unhealthySeq})
	row(`SELECT json_array(assertion_seq,assertion_id,actor,occurred_at,payload_version,payload,correction_id,correction_occurred_at,correction_payload) FROM workflow_delivery_state WHERE work_id='evidence-corpus'`, []any{assertionSeq, lastAssertion.EventID, lastAssertion.Actor, lastAssertion.OccurredAt.Format(time.RFC3339Nano), lastAssertion.PayloadVersion, string(lastAssertion.Payload), lastCorrection.EventID, lastCorrection.OccurredAt.Format(time.RFC3339Nano), string(lastCorrection.Payload)})
	for _, p := range artifacts {
		selectedVerdict := verdictSeq
		if p.atVerdict == 0 {
			selectedVerdict = 0
		}
		row(`SELECT json_array(production_seq,verdict_seq,verdict_production_seq) FROM workflow_artifact_actions WHERE work_id='evidence-corpus' AND step_id=? AND action_id=?`, []any{p.seq, selectedVerdict, p.atVerdict}, p.step, p.action)
	}
	row(`SELECT json_array(last_healthy_seq) FROM workflow_contracts WHERE work_id='evidence-corpus' AND contract_version=1`, []any{verdictSeq})
	row(`SELECT json_array(count(*)) FROM compaction_publications WHERE work_id='evidence-corpus'`, []any{1})
	row(`SELECT json_array(count(*)) FROM workflow_phase_exits WHERE work_id='evidence-corpus' AND action_id='accept_worker_result'`, []any{0})
	for action, seq := range exits {
		row(`SELECT json_array(exit_seq) FROM workflow_phase_exits WHERE work_id='evidence-corpus' AND action_id=?`, []any{seq}, action)
	}
	for step, seq := range steps {
		row(`SELECT json_array(completion_seq) FROM workflow_step_completions WHERE work_id='evidence-corpus' AND step_id=?`, []any{seq}, step)
	}
	var fields workflowCorrectionCompletionFields
	if err := json.Unmarshal(correctionEvent.Payload, &fields); err != nil {
		t.Fatal(err)
	}
	wantCorrection := correctionStateFact{Boundary: unboundAcceptance, Unresolved: map[string]bool{}, Records: []correctionRecordFact{{Seq: correctionSeq, SourceID: correctionEvent.EventID, Fields: fields}}}
	row(`SELECT json_array(state) FROM workflow_correction_state WHERE work_id='evidence-corpus'`, []any{string(mustJSONValue(wantCorrection))})
}

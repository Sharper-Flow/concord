package store

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

func pinWorkflowAtStep(t *testing.T, s *Store, workID, ref, step string) {
	t.Helper()
	entry, ok := BuiltinWorkflowRegistry().Current(ref)
	if !ok {
		t.Fatalf("%s is not registered", ref)
	}
	seedStepWork(t, s, workID)
	initializeStepWorkflow(t, s, workID, entry.Definition)
	if step != "" {
		actorRef, err := WorkflowActorRef(stepFixtureActor())
		if err != nil {
			t.Fatal(err)
		}
		if err := advanceWorkflowTestInstanceToStep(t.Context(), s, workID, step, actorRef); err != nil {
			t.Fatal(err)
		}
	}
}

func stalenessSeedContract(t *testing.T, s *Store, workID string) {
	t.Helper()
	actorRef, err := WorkflowActorRef(stepFixtureActor())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO write_guard(active) VALUES(1); INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class) VALUES(?,1,?,'internal_sqlite','[]','[]','2026-01-01T00:00:00Z',?,'[]','[]',0,'prototype_internal'); INSERT INTO workflow_contract_predicates(work_id,contract_version,predicate_id,ordinal,outcome_kind,outcome_payload) VALUES(?,1,'predicate:primary',0,'check','{"kind":"check"}'); DELETE FROM write_guard`, workID, "The premise the test contract states.", actorRef, workID); err != nil {
		t.Fatal(err)
	}
}

func stalenessEvent(t *testing.T, s *Store, workID, kind, payload string) {
	t.Helper()
	actorRef, err := WorkflowActorRef(stepFixtureActor())
	if err != nil {
		t.Fatal(err)
	}
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO write_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	var lastSeq int64
	if err := tx.QueryRow(`SELECT COALESCE(MAX(effect_seq),0) FROM effect_receipts`).Scan(&lastSeq); err != nil {
		t.Fatal(err)
	}
	event := Event{EventID: workID + ":" + kind + ":after:" + fmt.Sprint(lastSeq), Kind: kind, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: actorRef, OccurredAt: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC), PayloadVersion: 1, Payload: json.RawMessage(payload)}
	seq, err := appendPreparedEvent(t.Context(), tx, event)
	if err != nil {
		t.Fatal(err)
	}
	switch kind {
	case WorkflowActionStarted:
		var start workflowActionStartedPayload
		if err := json.Unmarshal(event.Payload, &start); err != nil {
			t.Fatal(err)
		}
		if start.AttemptEpoch > 0 {
			if _, err := tx.Exec(`INSERT INTO workflow_step_epochs(work_id,step_id,attempt_epoch,latest_start_seq,latest_start_event_id,latest_start_payload) VALUES(?,?,?,?,?,?) ON CONFLICT(work_id,step_id) DO UPDATE SET attempt_epoch=excluded.attempt_epoch,latest_start_seq=excluded.latest_start_seq,latest_start_event_id=excluded.latest_start_event_id,latest_start_payload=excluded.latest_start_payload`, workID, start.StepID, start.AttemptEpoch, seq, event.EventID, payload); err != nil {
				t.Fatal(err)
			}
		}
	case WorkflowActionCheckpointed:
		var checkpoint workflowActionCheckpointedPayload
		if err := json.Unmarshal(event.Payload, &checkpoint); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(`INSERT INTO workflow_checkpoints(work_id,checkpoint_id,step_id,step_kind,attempt_epoch,accepted_inputs_digest,result_evidence_refs,resume_cursor,idempotency_identity,actor_ref,request_id,recorded_at) VALUES(?,?,?,'internal_sqlite',?,'checkpoint-inputs','[]','',?,?,?,'2026-01-02T00:00:00Z')`, workID, event.EventID, checkpoint.StepID, checkpoint.AttemptEpoch, event.EventID, actorRef, event.EventID); err != nil {
			t.Fatal(err)
		}
	case WorkflowVerdictRecorded:
		var verdict workflowVerdictRecordedPayload
		if err := json.Unmarshal(event.Payload, &verdict); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(`INSERT INTO workflow_predicate_verdicts(work_id,contract_version,predicate_id,verdict_seq,actor,payload) VALUES(?,?,?,?,?,?) ON CONFLICT(work_id,contract_version,predicate_id) DO UPDATE SET verdict_seq=excluded.verdict_seq,actor=excluded.actor,payload=excluded.payload`, workID, verdict.ContractVersion, verdict.PredicateID, seq, actorRef, payload); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tx.Exec(`DELETE FROM write_guard`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func stalenessActionPayload(step, action string) string {
	raw, _ := json.Marshal(map[string]any{"step_id": step, "action_id": action})
	return string(raw)
}

func stalenessVerdictPayload(kind string) string {
	actor := stepFixtureActor()
	raw, _ := json.Marshal(map[string]any{"predicate_id": "predicate:primary", "verdict_kind": kind, "contract_version": 1, "verdict_actor_ref": DeriveWorkflowActorRef(actor.PrincipalRef, actor.ClientRef, actor.AgentRef, actor.SessionRef), "evaluation_evidence": []string{}})
	return string(raw)
}

func TestUnfencedCheckpointEpochUsesCausalPrefix(t *testing.T) {
	s := openTemp(t)
	defer s.Close()
	const workID = "unfenced-checkpoint-prefix"
	definition := mustBuiltinDefinition(t, "workflow.implementation").Definition
	pinWorkflowAtStep(t, s, workID, definition.Ref, "")
	if err := ApplyOperation(t.Context(), s, Operation{Events: []Event{
		{EventID: "another-work-create", Kind: "work.created", SubjectType: SubjectWorkItem, SubjectID: "another-work", Actor: "operator", OccurredAt: time.Unix(1, 0).UTC(), PayloadVersion: CurrentEventPayloadVersion("work.created"), Payload: jsonRaw(`{"work_kind":"task","title":"Other checkpoint work","priority":1}`)},
		{EventID: "another-work-membership", Kind: "work.memberships_replaced", SubjectType: SubjectWorkItem, SubjectID: "another-work", Actor: "operator", OccurredAt: time.Unix(2, 0).UTC(), PayloadVersion: 1, Payload: jsonRaw(`{"memberships":[{"project_id":"project-s","role":"primary"}],"expected_version":1,"resulting_version":2}`)},
	}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "another-work"): 0}}); err != nil {
		t.Fatal(err)
	}
	step := workflowStep(definition, "acceptance")
	assertEpoch := func(want int64) {
		t.Helper()
		got, err := workflowCheckpointAttemptEpoch(context.Background(), s.db, definition, step, workID, step.ID)
		if err != nil || got != want {
			t.Fatalf("current checkpoint epoch = %d, %v; want %d", got, err, want)
		}
	}
	assertEpoch(1)
	stalenessEvent(t, s, workID, WorkflowActionCheckpointed, `{"step_id":"acceptance","attempt_epoch":1}`)
	assertEpoch(2)
	stalenessEvent(t, s, workID, WorkflowActionStarted, `{"step_id":"acceptance","action_id":"dispatch_worker","attempt_epoch":1}`)
	assertEpoch(2)
	stalenessEvent(t, s, workID, WorkflowActionCheckpointed, `{"step_id":"review","attempt_epoch":41}`)
	stalenessEvent(t, s, "another-work", WorkflowActionCheckpointed, `{"step_id":"acceptance","attempt_epoch":51}`)
	assertEpoch(2)
	stalenessEvent(t, s, workID, WorkflowActionCheckpointed, `{"step_id":"acceptance","attempt_epoch":2}`)
	assertEpoch(3)
	stalenessEvent(t, s, workID, WorkflowActionStarted, `{"step_id":"acceptance","action_id":"dispatch_worker","attempt_epoch":61}`)
	assertEpoch(61)
	stalenessEvent(t, s, workID, WorkflowActionCheckpointed, `{"step_id":"acceptance","attempt_epoch":61}`)
	assertEpoch(62)
	var logs int
	if err := s.db.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE name='domain_events'`).Scan(&logs); err != nil || logs != 0 {
		t.Fatalf("event log tables=%d, want zero: %v", logs, err)
	}
	assertEpoch(62)
	if _, err := workflowCheckpointAttemptEpoch(context.Background(), s.db, definition, workflowStep(definition, "execution"), workID, "execution"); !hasFailureKind(err, KindIllegalLifecycleTransition) {
		t.Fatalf("fenced checkpoint without a start = %v, want invalid_transition", err)
	}
}

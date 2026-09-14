package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// This corpus uses interfaces present in v8.13.3 so an immutable source
// snapshot and the repaired tree can execute the same assertions.
func TestHistoricalReproDeclaredSchema(t *testing.T) {
	d := WorkflowDefinition{ActionDefinitions: []WorkflowActionDefinition{{ID: "probe", Payload: WorkflowPayloadDefinition{Closed: true, Fields: []WorkflowPayloadField{{Name: "value", ValueType: PayloadObject, Required: true, SchemaRef: "workflow_design_decision"}}}}}}
	if err := validateWorkflowActionPayload(d, "probe", json.RawMessage(`{"value":{}}`)); err == nil {
		t.Error("REPRO: preflight accepted an object that violates its declared schema")
	}
}

func historicalDecisionFields() map[string]any {
	return map[string]any{"question": "question", "options_considered": []string{"option"}, "decision": "accepted_decision", "rationale": "rationale", "consequences": []string{"consequence"}, "inputs": []string{"input"}, "poc_findings": "findings"}
}

func TestHistoricalReproDecisionDeclaration(t *testing.T) {
	d, err := BuiltinWorkflowDefinitionForRef("workflow.architecture_spike")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(historicalDecisionFields())
	if err != nil {
		t.Fatal(err)
	}
	if err := validateWorkflowActionPayload(d.Definition, "record_decision", raw); err != nil {
		t.Errorf("REPRO: decision record rejected by its declaration: %v", err)
	}
}

func TestHistoricalReproDecisionEnvelope(t *testing.T) {
	s := openTemp(t)
	const workID = "historical-envelope"
	seedStepWork(t, s, workID)
	d, err := BuiltinWorkflowDefinitionForRef("workflow.architecture_spike")
	if err != nil {
		t.Fatal(err)
	}
	initializeStepWorkflow(t, s, workID, d.Definition)
	raw, err := json.Marshal(map[string]any{"action_id": "record_decision", "fields": historicalDecisionFields()})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	err = s.Transact(ctx, func(tx *Transaction) error {
		if err := enterFold(ctx, tx.tx); err != nil {
			return err
		}
		if err := foldWorkflowDecisionRecord(ctx, tx.tx, Event{SubjectID: workID, OccurredAt: time.Unix(100, 0)}, raw); err != nil {
			return err
		}
		return leaveFold(ctx, tx.tx)
	})
	if err != nil {
		t.Errorf("REPRO: fold rejected the assembler's fields envelope: %v", err)
	}
}

func TestHistoricalReproCheckpointWithoutFencedStart(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	const workID = "historical-checkpoint"
	seedStepWork(t, s, workID)
	d, err := BuiltinWorkflowDefinitionForRef("workflow.architecture_spike")
	if err != nil {
		t.Fatal(err)
	}
	initializeStepWorkflow(t, s, workID, d.Definition)
	actorRef, err := WorkflowActorRef(stepFixtureActor())
	if err != nil {
		t.Fatal(err)
	}
	if err := advanceWorkflowTestInstanceToStep(ctx, s, workID, "decision_record", actorRef); err != nil {
		t.Fatal(err)
	}
	var version int64
	if err := s.db.QueryRow(`SELECT version FROM work_items WHERE id=?`, workID).Scan(&version); err != nil {
		t.Fatal(err)
	}
	decision := historicalDecisionFields()
	decision["action_id"] = "record_decision"
	payload, err := json.Marshal(map[string]any{"work_id": workID, "expected_version": version, "resulting_version": version + 1, "step_id": "decision_record", "step_kind": string(WorkflowStepHumanCheckpoint), "attempt_epoch": 1, "checkpoint_payload": decision, "resume_cursor": "", "actor_ref": actorRef, "request_id": "request:historical", "checkpoint_id": "checkpoint:historical", "accepted_inputs_digest": "sha256:" + strings.Repeat("a", 64), "idempotency_identity": "historical-checkpoint"})
	if err != nil {
		t.Fatal(err)
	}
	err = applyWorkflowTestOperation(ctx, s, Operation{Events: []Event{{EventID: "historical-checkpoint-event", Kind: WorkflowActionCheckpointed, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: actorRef, OccurredAt: time.Unix(100, 0), PayloadVersion: 1, Payload: payload}}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}})
	if err != nil {
		t.Errorf("REPRO: a non-fenced decision checkpoint cannot be recorded: %v", err)
	}
}

func TestHistoricalReproCancellationDeclaration(t *testing.T) {
	d, err := BuiltinWorkflowDefinitionForRef("workflow.ops_runbook")
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []json.RawMessage{json.RawMessage(`{}`), json.RawMessage(`{"condition_id":"condition:test","cancellation_authority":"agent:test","cancellation_evidence":["evidence:test"],"cancelled_by_event":"event:test"}`)} {
		if err := validateWorkflowActionPayload(d.Definition, "cancel_condition", raw); err == nil {
			t.Error("REPRO: cancellation declaration admits missing fields or non-operator authority")
		}
	}
}

func TestHistoricalReproExplicitTimestamp(t *testing.T) {
	d, err := BuiltinWorkflowDefinitionForRef("workflow.ops_runbook")
	if err != nil {
		t.Fatal(err)
	}
	raw := json.RawMessage(`{"run_id":"run-test","native_subject_ref":"native:test","status":"started","evidence_ref":"artifact:test","evidence_digest":"sha256:` + strings.Repeat("a", 64) + `","asserted_at":"not-a-time"}`)
	if err := validateWorkflowActionPayload(d.Definition, "start_run", raw); err == nil {
		t.Error("REPRO: invalid explicit timestamp accepted by preflight")
	}
}

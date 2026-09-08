package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func currentWorkflowDefinition(t *testing.T, ref string) WorkflowDefinition {
	t.Helper()
	for _, definition := range BuiltinWorkflowDefinitions() {
		if definition.Ref == ref {
			return definition
		}
	}
	t.Fatalf("current workflow definition %q is absent", ref)
	return WorkflowDefinition{}
}

func requirePayloadFailure(t *testing.T, err error, field, rule string) *Failure {
	t.Helper()
	if err == nil {
		t.Fatalf("payload containing invalid field %q was accepted", field)
	}
	var failure *Failure
	if !failureAs(err, &failure) {
		t.Fatalf("payload refusal is not typed: %v", err)
	}
	if failure.Kind != KindInvalidPayload {
		t.Fatalf("payload refusal kind = %q, want %q: %v", failure.Kind, KindInvalidPayload, err)
	}
	if !strings.Contains(failure.Detail, field) || !strings.Contains(failure.Detail, rule) {
		t.Fatalf("payload refusal detail = %q, want field %q and rule %q", failure.Detail, field, rule)
	}
	return failure
}

func TestCurrentActionPayloadContractsRefuseCrossActionAndInvalidFields(t *testing.T) {
	definition := currentWorkflowDefinition(t, "workflow.implementation")
	tests := []struct {
		name    string
		action  string
		payload string
		field   string
		rule    string
	}{
		{
			name:    "transferred record discovery diagnostic",
			action:  "record_discovery",
			payload: `{"touched_refs":["secret:must-not-echo"]}`,
			field:   "touched_refs",
			rule:    "declared",
		},
		{
			name:    "field belongs to another action",
			action:  "bind_evidence",
			payload: `{"edge_id":"edge:wrong-action"}`,
			field:   "edge_id",
			rule:    "declared",
		},
		{
			name:    "wrong registered type",
			action:  "declare_impact",
			payload: `{"target_work_id":"work-target","edge_id":42}`,
			field:   "edge_id",
			rule:    "ref",
		},
		{
			name:    "wrong registered bound",
			action:  "record_verdict",
			payload: `{"predicate_id":"predicate:one","evaluation_evidence":[]}`,
			field:   "evaluation_evidence",
			rule:    "min_items=1",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			failure := requirePayloadFailure(t, validateWorkflowActionPayload(definition, test.action, json.RawMessage(test.payload)), test.field, test.rule)
			if strings.Contains(failure.Detail, "secret:must-not-echo") {
				t.Fatalf("payload refusal echoed a field value: %q", failure.Detail)
			}
		})
	}
}

func TestMissingRequiredActionFieldHasNoDurableEffect(t *testing.T) {
	s := openTemp(t)
	defer s.Close()
	const workID = "work-required-payload"
	seedStepWork(t, s, workID)
	initializeStepWorkflow(t, s, workID, currentWorkflowDefinition(t, "workflow.implementation"))

	var beforeVersion, beforeEvents int64
	if err := s.db.QueryRow(`SELECT version FROM work_items WHERE id=?`, workID).Scan(&beforeVersion); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT count(*) FROM domain_events WHERE subject_type=? AND subject_id=?`, SubjectWorkItem, workID).Scan(&beforeEvents); err != nil {
		t.Fatal(err)
	}

	actor := stepFixtureActor()
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := enterFold(context.Background(), tx); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	_, actionErr := applyWorkflowActionRawTx(context.Background(), tx, BuiltinWorkflowRegistry(), WorkflowActionExecutionRequest{
		WorkID: workID, ExpectedVersion: beforeVersion, ActionID: "approve_contract", Payload: json.RawMessage(`{}`), Actor: actor,
		AcceptedInputsDigest: "sha256:required-payload", IdempotencyIdentity: "required-payload", OperationID: "required-payload",
		PrincipalRef: actor.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: "required-payload",
		RequestID: "request:required-payload", ContractDigest: testManifestDigest, Now: time.Date(2026, 9, 8, 0, 0, 0, 0, time.UTC),
	})
	_ = leaveFold(context.Background(), tx)
	_ = tx.Rollback()
	requirePayloadFailure(t, actionErr, "outcome_predicates", "required")

	var afterVersion, afterEvents int64
	if err := s.db.QueryRow(`SELECT version FROM work_items WHERE id=?`, workID).Scan(&afterVersion); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT count(*) FROM domain_events WHERE subject_type=? AND subject_id=?`, SubjectWorkItem, workID).Scan(&afterEvents); err != nil {
		t.Fatal(err)
	}
	if afterVersion != beforeVersion || afterEvents != beforeEvents {
		t.Fatalf("missing required field changed durable state: version %d->%d, events %d->%d", beforeVersion, afterVersion, beforeEvents, afterEvents)
	}
}

func TestDispatchWorkerIntentNamesThePublicAdapterField(t *testing.T) {
	definition := currentWorkflowDefinition(t, "workflow.implementation")
	intents := workPinIntents(definition, "execution", 11)
	for _, intent := range intents {
		if intent.ActionID != "dispatch_worker" {
			continue
		}
		if len(intent.RequiredFields) != 1 || intent.RequiredFields[0] != "lane_id" {
			t.Fatalf("dispatch_worker required fields = %v, want [lane_id]", intent.RequiredFields)
		}
		return
	}
	t.Fatal("execution step has no dispatch_worker intent")
}

func TestActionPayloadListItemSchemasMatchPreflight(t *testing.T) {
	definition := currentWorkflowDefinition(t, "workflow.implementation")
	if err := validateWorkflowActionPayload(definition, "record_verdict", json.RawMessage(`{"predicate_id":"predicate:one","evaluation_evidence":["artifact:path/with/slash"]}`)); err != nil {
		t.Fatalf("workflow reference accepted by the generated reference schema was refused: %v", err)
	}
	longLawID := "spec:" + strings.Repeat("a", 124)
	payload := json.RawMessage(`{"outcome_predicates":[{"predicate_id":"predicate:one","ordinal":0,"outcome_kind":"check","outcome_payload":{"kind":"check","check_ref":"check:test","immutable_subject_ref":"commit:test","expected_result":"pass"}}],"spec_mandate":["` + longLawID + `"]}`)
	if err := validateWorkflowActionPayload(definition, "approve_contract", payload); err != nil {
		t.Fatalf("bounded law ID accepted by the generated law schema was refused: %v", err)
	}
	invalid := json.RawMessage(`{"outcome_predicates":[{"predicate_id":"predicate:one","ordinal":0,"outcome_kind":"check","outcome_payload":{"kind":"check","check_ref":"check:test","immutable_subject_ref":"commit:test","expected_result":"pass"}}],"spec_mandate":[" spec:one"]}`)
	requirePayloadFailure(t, validateWorkflowActionPayload(definition, "approve_contract", invalid), "spec_mandate", "item_ref=law_id")
}

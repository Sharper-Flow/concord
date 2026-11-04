package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestCurrentActionStringFieldsRequireNonBlank(t *testing.T) {
	t.Parallel()
	for id := range builtinWorkflowActionCatalog() {
		action, err := catalogAction(id)
		if err != nil {
			t.Fatal(err)
		}
		payloads := []*WorkflowPayloadDefinition{&action.Payload}
		if action.PublicPayload != nil {
			payloads = append(payloads, action.PublicPayload)
		}
		for _, payload := range payloads {
			for _, field := range payload.Fields {
				if field.ValueType == PayloadString && len(field.Enum) == 0 && !field.NonBlank {
					t.Errorf("action %s field %s does not declare NonBlank", id, field.Name)
				}
			}
		}
	}
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
	t.Parallel()
	for _, test := range []struct {
		name, action, payload, field, rule string
	}{
		{"another action's field", "bind_evidence", `{"edge_id":"secret:must-not-echo"}`, "edge_id", "declared"},
		{"wrong type", "link_successor", `{"successor_work_id":42}`, "successor_work_id", "type or bounds"},
		{"empty verdict batch", "record_verdict", `{"verdicts":[]}`, "verdicts", "type or bounds"},
		{"empty receipts", "record_verdict", `{"verdicts":[{"predicate_id":"predicate:one","receipt_refs":[]}]}`, "receipt_refs", "minimum of 1"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := validateWorkflowPlanActionPayload(test.action, json.RawMessage(test.payload))
			failure := requirePayloadFailure(t, err, test.field, test.rule)
			if strings.Contains(failure.Detail, "secret:must-not-echo") {
				t.Fatalf("payload refusal echoed a field value: %q", failure.Detail)
			}
		})
	}
}

func TestContractPremiseRejectsBlankAndFallbackText(t *testing.T) {
	t.Parallel()
	valid := `{"decision_domains":[],"premise":"The operator stated this premise.","outcome_predicates":[{"predicate_id":"predicate:one","ordinal":0,"outcome_kind":"check","outcome_payload":{"kind":"check","check_ref":"check:test","immutable_subject_ref":"commit:test","expected_result":"pass"}}]}`
	if _, err := validateWorkflowPlanActionPayload("approve_contract", json.RawMessage(valid)); err != nil {
		t.Fatalf("valid contract refused: %v", err)
	}
	for _, premise := range []string{"", "   ", "workflow premise"} {
		payload := strings.Replace(valid, "The operator stated this premise.", premise, 1)
		_, err := validateWorkflowPlanActionPayload("approve_contract", json.RawMessage(payload))
		_ = requirePayloadFailure(t, err, "premise", "type or bounds")
	}
}

func TestRecordPlanPayloadContractIsTyped(t *testing.T) {
	t.Parallel()
	typed := json.RawMessage(`{"outcomes":"Deliver the change","approach":"Use the current plan","decisions":[{"id":"decision:one","question":"Which route?","choice":"native","rationale":"The adapter owns the route","rejected":[]}],"touches":["work:item"],"merge_split":"One change","risks":"Check the boundary"}`)
	if _, err := validateWorkflowPlanActionPayload("record_plan", typed); err != nil {
		t.Fatalf("record_plan refused its typed payload: %v", err)
	}
	_, err := validateWorkflowPlanActionPayload("record_plan", json.RawMessage(`{}`))
	_ = requirePayloadFailure(t, err, "decisions", "required")
	invalid := strings.Replace(string(typed), `"decisions":[{`, `"decisions":[{"unexpected":true,`, 1)
	_, err = validateWorkflowPlanActionPayload("record_plan", json.RawMessage(invalid))
	_ = requirePayloadFailure(t, err, "decisions", "schema")
}

func TestMissingRequiredActionFieldHasNoDurableEffect(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	defer s.Close()
	const workID = "work-required-payload"
	seedStepWork(t, s, workID)
	var beforeVersion, beforeEvents int64
	if err := s.db.QueryRow(`SELECT version FROM work_items WHERE id=?`, workID).Scan(&beforeVersion); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT count(*) FROM effect_receipts WHERE subject_type=? AND subject_id=?`, SubjectWorkItem, workID).Scan(&beforeEvents); err != nil {
		t.Fatal(err)
	}
	actor := stepFixtureActor()
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	_, actionErr := applyWorkflowActionRawTx(context.Background(), tx, newWriteScope(tx), WorkflowActionExecutionRequest{
		WorkID: workID, ExpectedVersion: beforeVersion, ActionID: "approve_contract", Payload: json.RawMessage(`{"decision_domains":[],"outcome_predicates":[{"predicate_id":"predicate:required-payload","ordinal":0,"outcome_kind":"check","outcome_payload":{"kind":"check","check_ref":"check:required-payload","immutable_subject_ref":"commit:required-payload","expected_result":"pass"}}]}`), Actor: actor,
		AcceptedInputsDigest: "sha256:required-payload", IdempotencyIdentity: "required-payload", OperationID: "required-payload",
		PrincipalRef: actor.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: "required-payload",
		RequestID: "request:required-payload", ContractDigest: testManifestDigest, Now: time.Date(2026, 9, 8, 0, 0, 0, 0, time.UTC),
	})
	_ = requirePayloadFailure(t, actionErr, "premise", "required")
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var afterVersion, afterEvents int64
	if err := s.db.QueryRow(`SELECT version FROM work_items WHERE id=?`, workID).Scan(&afterVersion); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT count(*) FROM effect_receipts WHERE subject_type=? AND subject_id=?`, SubjectWorkItem, workID).Scan(&afterEvents); err != nil {
		t.Fatal(err)
	}
	if afterVersion != beforeVersion || afterEvents != beforeEvents {
		t.Fatalf("missing required field changed durable state: version %d->%d, events %d->%d", beforeVersion, afterVersion, beforeEvents, afterEvents)
	}
}

func TestActionPayloadListItemSchemasMatchPreflight(t *testing.T) {
	t.Parallel()
	if _, err := validateWorkflowPlanActionPayload("record_verdict", json.RawMessage(`{"verdicts":[{"predicate_id":"predicate:one","receipt_refs":["artifact:path/with/slash"]}]}`)); err != nil {
		t.Fatalf("workflow reference accepted by the generated reference schema was refused: %v", err)
	}
	longLawID := "spec:" + strings.Repeat("a", 124)
	payload := json.RawMessage(`{"premise":"The operator stated this premise.","outcome_predicates":[{"predicate_id":"predicate:one","ordinal":0,"outcome_kind":"check","outcome_payload":{"kind":"check","check_ref":"check:test","immutable_subject_ref":"commit:test","expected_result":"pass"}}],"spec_mandate":["` + longLawID + `"],"decision_domains":[]}`)
	if _, err := validateWorkflowPlanActionPayload("approve_contract", payload); err != nil {
		t.Fatalf("bounded law ID accepted by the generated law schema was refused: %v", err)
	}
	invalid := json.RawMessage(`{"premise":"The operator stated this premise.","outcome_predicates":[{"predicate_id":"predicate:one","ordinal":0,"outcome_kind":"check","outcome_payload":{"kind":"check","check_ref":"check:test","immutable_subject_ref":"commit:test","expected_result":"pass"}}],"spec_mandate":[" spec:one"],"decision_domains":[]}`)
	_, err := validateWorkflowPlanActionPayload("approve_contract", invalid)
	_ = requirePayloadFailure(t, err, "spec_mandate", "type or bounds")
}

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

func TestRecordPlanPayloadContractIsTyped(t *testing.T) {
	t.Parallel()
	typed := json.RawMessage(`{"outcomes":[{"id":"outcome:one","statement":"Deliver the change","done_condition":"Done","evidence":"Tests pass","depends_on":[]}],"approach":"Use the current plan","decisions":[{"id":"decision:one","question":"Which route?","choice":"native","rationale":"The adapter owns the route","rejected":[],"status":"settled"}],"touches":[{"path":"work:item","role":"change"}],"merge_split":"One change","risks":"Check the boundary"}`)
	if _, err := validateWorkflowPlanActionPayload("record_plan", typed); err != nil {
		t.Fatalf("record_plan refused its typed payload: %v", err)
	}
	_, err := validateWorkflowPlanActionPayload("record_plan", json.RawMessage(`{}`))
	_ = requirePayloadFailure(t, err, "decisions", "required")
	invalid := strings.Replace(string(typed), `"decisions":[{`, `"decisions":[{"unexpected":true,`, 1)
	_, err = validateWorkflowPlanActionPayload("record_plan", json.RawMessage(invalid))
	_ = requirePayloadFailure(t, err, "decisions", "schema")
}

func TestRecordPlanPayloadContractDeclaresArrayItems(t *testing.T) {
	action, err := WorkflowPlanActionContract("record_plan")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]struct {
		ref      string
		min, max int64
	}{
		"outcomes":  {"workflow_plan_outcome", 1, 16},
		"decisions": {"workflow_design_decision", 0, 16},
		"touches":   {"workflow_plan_touch", 1, 64},
	}
	for _, field := range action.Payload.Fields {
		expected, ok := want[field.Name]
		if !ok {
			continue
		}
		if field.ValueType != PayloadArray || field.ItemRef != expected.ref || field.SchemaRef != "" || field.MinItems == nil || *field.MinItems != expected.min || field.MaxItems == nil || *field.MaxItems != expected.max {
			t.Errorf("%s declaration = %+v, want element %s and bounds %d..%d", field.Name, field, expected.ref, expected.min, expected.max)
		}
		delete(want, field.Name)
	}
	if len(want) != 0 {
		t.Fatalf("missing array declarations: %v", want)
	}
}

func TestRecordPlanClosure(t *testing.T) {
	const valid = `{"outcomes":[{"id":"outcome:one","statement":"one","done_condition":"done","evidence":"test","depends_on":[]},{"id":"outcome:two","statement":"two","done_condition":"done","evidence":"test","depends_on":["outcome:one"]}],"approach":"bounded","decisions":[{"id":"decision:one","question":"which","choice":"one","rationale":"fits","rejected":[],"status":"settled"}],"touches":[{"path":"file:one","role":"change"}],"merge_split":"one","risks":"bounded"}`
	for _, tc := range []struct{ name, old, replacement, field string }{
		{"duplicate outcome", `"id":"outcome:two"`, `"id":"outcome:one"`, "outcomes"},
		{"missing dependency", `"depends_on":["outcome:one"]`, `"depends_on":["outcome:missing"]`, "depends_on"},
		{"self dependency", `"depends_on":["outcome:one"]`, `"depends_on":["outcome:two"]`, "depends_on"},
		{"duplicate touch", `"touches":[{"path":"file:one","role":"change"}]`, `"touches":[{"path":"file:one","role":"change"},{"path":"file:one","role":"reader"}]`, "touches"},
		{"question without approval", `"status":"settled"`, `"status":"operator_question"`, "approval"},
		{"question with evidence", `"status":"settled"`, `"status":"operator_question","approval":{"class":"security","consequence":"risk","evidence_ref":"evidence:one"}`, "evidence_ref"},
		{"settled approval without evidence", `"status":"settled"`, `"status":"settled","approval":{"class":"security","consequence":"risk"}`, "evidence_ref"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := json.RawMessage(strings.Replace(valid, tc.old, tc.replacement, 1))
			_, err := validateWorkflowPlanActionPayload("record_plan", raw)
			_ = requirePayloadFailure(t, err, tc.field, "")
		})
	}
	for _, decision := range []struct{ name, statusAndApproval string }{
		{"no approval needed", `"status":"settled"`},
		{"operator question", `"status":"operator_question","approval":{"class":"security","consequence":"risk"}`},
		{"settled approval", `"status":"settled","approval":{"class":"security","consequence":"risk","evidence_ref":"evidence:one"}`},
	} {
		t.Run(decision.name, func(t *testing.T) {
			raw := json.RawMessage(strings.Replace(valid, `"status":"settled"`, decision.statusAndApproval, 1))
			if _, err := validateWorkflowPlanActionPayload("record_plan", raw); err != nil {
				t.Fatalf("valid plan refused: %v", err)
			}
		})
	}
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
		WorkID: workID, ExpectedVersion: beforeVersion, ActionID: "record_intake", Payload: json.RawMessage(`{}`), Actor: actor,
		AcceptedInputsDigest: "sha256:required-payload", IdempotencyIdentity: "required-payload", OperationID: "required-payload",
		PrincipalRef: actor.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: "required-payload",
		RequestID: "request:required-payload", ContractDigest: testManifestDigest, Now: time.Date(2026, 9, 8, 0, 0, 0, 0, time.UTC),
	})
	_ = requirePayloadFailure(t, actionErr, "outcome", "required")
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
	longLawID := "spec:" + strings.Repeat("a", 124)
	payload := json.RawMessage(`{"legislative_subjects":[],"spec_mandate":["` + longLawID + `"]}`)
	_, err := validateWorkflowPlanActionPayload("approve_contract", payload)
	_ = requirePayloadFailure(t, err, "spec_mandate", "type or bounds")
	invalid := json.RawMessage(`{"legislative_subjects":[],"spec_mandate":[" spec:one"]}`)
	_, err = validateWorkflowPlanActionPayload("approve_contract", invalid)
	_ = requirePayloadFailure(t, err, "spec_mandate", "type or bounds")
}

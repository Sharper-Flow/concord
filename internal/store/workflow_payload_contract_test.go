package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func currentWorkflowDefinition(t *testing.T) WorkflowDefinition {
	t.Helper()
	for _, definition := range BuiltinWorkflowDefinitions() {
		if definition.Ref == "workflow.implementation" {
			return definition
		}
	}
	t.Fatalf("current workflow definition %q is absent", "workflow.implementation")
	return WorkflowDefinition{}
}

func TestBuiltinWorkflowRegistryStringFieldsRequireNonBlank(t *testing.T) {
	t.Parallel()
	registry := BuiltinWorkflowRegistry()
	for _, current := range BuiltinWorkflowDefinitions() {
		registered, ok := registry.Current(current.Ref)
		if !ok {
			t.Errorf("current workflow definition %q is not registered", current.Ref)
			continue
		}
		for _, action := range registered.Definition.ActionDefinitions {
			payloads := []*WorkflowPayloadDefinition{&action.Payload}
			if action.PublicPayload != nil {
				payloads = append(payloads, action.PublicPayload)
			}
			for _, payload := range payloads {
				for _, field := range payload.Fields {
					if field.ValueType == PayloadString && len(field.Enum) == 0 && !field.NonBlank {
						t.Errorf("%s action %s field %s does not declare NonBlank", current.Ref, action.ID, field.Name)
					}
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
	definition := currentWorkflowDefinition(t)
	tests := []struct {
		name    string
		action  string
		payload string
		field   string
		rule    string
	}{
		{
			// record_discovery left the canonical implementation head, so the
			// transfer discipline moves to the diagnostic action the head
			// still declares: record_proposal must refuse record_design's
			// field.
			name:    "transferred record design diagnostic",
			action:  "record_proposal",
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
			// The canonical record_verdict declaration carries the verdicts
			// batch alone, so the declared bound the refusal names is the
			// batch's own min_items=1.
			name:    "wrong registered bound",
			action:  "record_verdict",
			payload: `{"verdicts":[]}`,
			field:   "verdicts",
			rule:    "min_items=1",
		},
		{
			// The batch entry keeps its own receipt bound: the generated
			// entry schema refuses an empty receipt_refs inside the verdicts
			// item.
			name:    "wrong registered item bound",
			action:  "record_verdict",
			payload: `{"verdicts":[{"predicate_id":"predicate:one","receipt_refs":[]}]}`,
			field:   "receipt_refs",
			rule:    "minimum of 1",
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

func TestContractPremiseRejectsBlankAndFallbackText(t *testing.T) {
	t.Parallel()
	definition := currentWorkflowDefinition(t)
	valid := `{"premise":"The operator stated this premise.","outcome_predicates":[{"predicate_id":"predicate:one","ordinal":0,"outcome_kind":"check","outcome_payload":{"kind":"check","check_ref":"check:test","immutable_subject_ref":"commit:test","expected_result":"pass"}}]}`
	for _, premise := range []string{"", "   ", "workflow premise"} {
		payload := strings.Replace(valid, "The operator stated this premise.", premise, 1)
		if err := validateWorkflowActionPayload(definition, "approve_contract", json.RawMessage(payload)); err == nil {
			t.Fatalf("approve_contract accepted premise %q", premise)
		}
	}
	for _, premise := range []string{"   ", "workflow premise"} {
		payload := `{"contract_version":2,"premise":"` + premise + `","outcome_predicates":[{"predicate_id":"predicate:one","ordinal":0,"outcome_kind":"check","outcome_payload":{"kind":"check","check_ref":"check:test","immutable_subject_ref":"commit:test","expected_result":"pass"}}],"required_evidence":[],"route_conventions":[],"spec_mandate":[],"law_modifies":[],"rigor_class":"prototype_internal","supersede_reason":"replace the contract","audit_evidence":["evidence:test"]}`
		if err := validateWorkflowContractRecoveryPayload(WorkflowDefinition{}, json.RawMessage(payload)); err == nil {
			t.Fatalf("supersede_contract accepted premise %q", premise)
		}
	}
}

func TestRecordDesignPayloadContractIsTyped(t *testing.T) {
	definition := currentWorkflowDefinition(t)
	var action WorkflowActionDefinition
	for _, candidate := range definition.ActionDefinitions {
		if candidate.ID == "record_design" {
			action = candidate
		}
	}
	if action.ID == "" {
		t.Fatal("record_design is not declared")
	}
	if len(action.Payload.Fields) != 3 {
		t.Fatalf("record_design payload has %d fields, want 3", len(action.Payload.Fields))
	}
	typed := json.RawMessage(`{"approach":"keep the pinned contract","decisions":[{"id":"decision:one","question":"Which route?","choice":"native","rationale":"The adapter owns the route","rejected":[]}],"touched_refs":["work:item"]}`)
	if err := validateWorkflowActionPayload(definition, "record_design", typed); err != nil {
		t.Fatalf("record_design refused its typed payload: %v", err)
	}
	if err := validateWorkflowActionPayload(definition, "record_design", json.RawMessage(`{}`)); err == nil {
		t.Fatal("record_design accepted an empty payload")
	}
}

func TestMissingRequiredActionFieldHasNoDurableEffect(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	defer s.Close()
	const workID = "work-required-payload"
	seedStepWork(t, s, workID)
	initializeStepWorkflow(t, s, workID, currentWorkflowDefinition(t))

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
	_, actionErr := applyWorkflowActionRawTx(context.Background(), tx, newFoldScope(tx), BuiltinWorkflowRegistry(), WorkflowActionExecutionRequest{
		WorkID: workID, ExpectedVersion: beforeVersion, ActionID: "approve_contract", Payload: json.RawMessage(`{"outcome_predicates":[{"predicate_id":"predicate:required-payload","ordinal":0,"outcome_kind":"check","outcome_payload":{"kind":"check","check_ref":"check:required-payload","immutable_subject_ref":"commit:required-payload","expected_result":"pass"}}]}`), Actor: actor,
		AcceptedInputsDigest: "sha256:required-payload", IdempotencyIdentity: "required-payload", OperationID: "required-payload",
		PrincipalRef: actor.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: "required-payload",
		RequestID: "request:required-payload", ContractDigest: testManifestDigest, Now: time.Date(2026, 9, 8, 0, 0, 0, 0, time.UTC),
	})
	_ = tx.Rollback()
	_ = requirePayloadFailure(t, actionErr, "premise", "required")

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
	t.Parallel()
	definition := currentWorkflowDefinition(t)
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
	t.Parallel()
	definition := currentWorkflowDefinition(t)
	// The canonical record_verdict batch carries the entry's receipt
	// references through the generated reference schema: the slash the
	// reference rule admits must survive the item boundary.
	if err := validateWorkflowActionPayload(definition, "record_verdict", json.RawMessage(`{"verdicts":[{"predicate_id":"predicate:one","receipt_refs":["artifact:path/with/slash"]}]}`)); err != nil {
		t.Fatalf("workflow reference accepted by the generated reference schema was refused: %v", err)
	}
	longLawID := "spec:" + strings.Repeat("a", 124)
	payload := json.RawMessage(`{"premise":"The operator stated this premise.","outcome_predicates":[{"predicate_id":"predicate:one","ordinal":0,"outcome_kind":"check","outcome_payload":{"kind":"check","check_ref":"check:test","immutable_subject_ref":"commit:test","expected_result":"pass"}}],"spec_mandate":["` + longLawID + `"],"decision_domains":[]}`)
	if err := validateWorkflowActionPayload(definition, "approve_contract", payload); err != nil {
		t.Fatalf("bounded law ID accepted by the generated law schema was refused: %v", err)
	}
	invalid := json.RawMessage(`{"premise":"The operator stated this premise.","outcome_predicates":[{"predicate_id":"predicate:one","ordinal":0,"outcome_kind":"check","outcome_payload":{"kind":"check","check_ref":"check:test","immutable_subject_ref":"commit:test","expected_result":"pass"}}],"spec_mandate":[" spec:one"],"decision_domains":[]}`)
	_ = requirePayloadFailure(t, validateWorkflowActionPayload(definition, "approve_contract", invalid), "spec_mandate", "item_ref=law_id")
}

func TestRecordProposalPersistsAndReads(t *testing.T) {
	s := openTemp(t)
	defer s.Close()
	const workID = "work-proposal-read"
	seedStepWork(t, s, workID)
	initializeStepWorkflow(t, s, workID, currentWorkflowDefinition(t))
	var version int64
	if err := s.db.QueryRow(`SELECT version FROM work_items WHERE id=?`, workID).Scan(&version); err != nil {
		t.Fatal(err)
	}
	actor := stepFixtureActor()
	payload := json.RawMessage(`{"problem":"The problem statement preserves its text.","affected":["The first affected system.","The second affected system."],"stakes":"The stakes statement preserves its text.","user_outcomes":["The first user outcome.","The second user outcome."],"constraints":["The implementation constraint."],"open_questions":["The unresolved question."]}`)
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	_, actionErr := applyWorkflowActionRawTx(context.Background(), tx, newFoldScope(tx), BuiltinWorkflowRegistry(), WorkflowActionExecutionRequest{
		WorkID: workID, ExpectedVersion: version, ActionID: "record_proposal", Payload: payload, Actor: actor,
		AcceptedInputsDigest: "sha256:proposal-read", IdempotencyIdentity: "proposal-read", OperationID: "proposal-read",
		PrincipalRef: actor.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: "proposal-read",
		RequestID: "request:proposal-read", ContractDigest: testManifestDigest, Now: time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC),
	})
	if actionErr != nil {
		_ = tx.Rollback()
		t.Fatal(actionErr)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	read := func() WorkflowProposalRecord {
		t.Helper()
		projection, readErr := ReadWorkflowProjection(context.Background(), s, WorkflowReadRequest{WorkID: workID})
		if readErr != nil {
			t.Fatal(readErr)
		}
		if projection.ProposalRecord == nil {
			t.Fatal("workflow read omitted the proposal record")
		}
		return *projection.ProposalRecord
	}
	first := read()
	if first.Problem != "The problem statement preserves its text." || len(first.Affected) != 2 || first.Affected[1] != "The second affected system." || first.Constraints[0] != "The implementation constraint." || first.OpenQuestions[0] != "The unresolved question." {
		t.Fatalf("workflow read proposal=%+v", first)
	}
}

func TestRecordProposalRefusesAnInvalidDocument(t *testing.T) {
	definition := currentWorkflowDefinition(t)
	valid := `{"problem":"The recorded problem.","affected":["The affected party."],"stakes":"The recorded stakes.","user_outcomes":["The expected outcome."]}`
	if err := validateWorkflowActionPayload(definition, "record_proposal", json.RawMessage(valid)); err != nil {
		t.Fatalf("the bounded proposal document was refused: %v", err)
	}
	for name, payload := range map[string]string{
		"empty_document":     `{}`,
		"missing_problem":    `{"affected":["The affected party."],"stakes":"The recorded stakes.","user_outcomes":["The expected outcome."]}`,
		"blank_problem":      `{"problem":"   ","affected":["The affected party."],"stakes":"The recorded stakes.","user_outcomes":["The expected outcome."]}`,
		"blank_list_item":    `{"problem":"The recorded problem.","affected":["  "],"stakes":"The recorded stakes.","user_outcomes":["The expected outcome."]}`,
		"empty_affected":     `{"problem":"The recorded problem.","affected":[],"stakes":"The recorded stakes.","user_outcomes":["The expected outcome."]}`,
		"duplicate_affected": `{"problem":"The recorded problem.","affected":["The affected party.","The affected party."],"stakes":"The recorded stakes.","user_outcomes":["The expected outcome."]}`,
		"unknown_field":      `{"problem":"The recorded problem.","affected":["The affected party."],"stakes":"The recorded stakes.","user_outcomes":["The expected outcome."],"solution":"Write the code."}`,
		"null_optional_list": `{"problem":"The recorded problem.","affected":["The affected party."],"stakes":"The recorded stakes.","user_outcomes":["The expected outcome."],"constraints":null}`,
	} {
		if err := validateWorkflowActionPayload(definition, "record_proposal", json.RawMessage(payload)); err == nil {
			t.Errorf("%s: the transition accepted an invalid proposal document", name)
		}
	}
}

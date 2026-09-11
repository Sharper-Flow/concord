package store

import (
	"context"
	"encoding/json"
	"reflect"
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
	_ = requirePayloadFailure(t, actionErr, "outcome_predicates", "required")

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
	intents := workPinIntents(definition, "execution", 11, false)
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
	_ = requirePayloadFailure(t, validateWorkflowActionPayload(definition, "approve_contract", invalid), "spec_mandate", "item_ref=law_id")
}

func TestRecordProposalPersistsAndReadsAfterReplay(t *testing.T) {
	s := openTemp(t)
	defer s.Close()
	const workID = "work-proposal-read"
	seedStepWork(t, s, workID)
	initializeStepWorkflow(t, s, workID, currentWorkflowDefinition(t, "workflow.implementation"))
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
	if err := enterFold(context.Background(), tx); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	_, actionErr := applyWorkflowActionRawTx(context.Background(), tx, BuiltinWorkflowRegistry(), WorkflowActionExecutionRequest{
		WorkID: workID, ExpectedVersion: version, ActionID: "record_proposal", Payload: payload, Actor: actor,
		AcceptedInputsDigest: "sha256:proposal-read", IdempotencyIdentity: "proposal-read", OperationID: "proposal-read",
		PrincipalRef: actor.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: "proposal-read",
		RequestID: "request:proposal-read", ContractDigest: testManifestDigest, Now: time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC),
	})
	_ = leaveFold(context.Background(), tx)
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
	if err := RebuildFromLog(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	second := read()
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("proposal changed after replay: before=%+v after=%+v", first, second)
	}
}

// historicalWorkflowDefinition returns one released definition version so a
// test can hold a pin taken before a payload gained typed fields.
func historicalWorkflowDefinition(t *testing.T, ref string, version int64) WorkflowDefinition {
	t.Helper()
	for _, definition := range builtinWorkflowDefinitionsWithHistory() {
		if definition.Ref == ref && definition.Version == version {
			return definition
		}
	}
	t.Fatalf("workflow definition %q version %d is absent", ref, version)
	return WorkflowDefinition{}
}

func TestRecordProposalRefusesAnInvalidDocument(t *testing.T) {
	definition := currentWorkflowDefinition(t, "workflow.implementation")
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

func TestRecordProposalKeepsTheEmptyCallOfAnEarlierPin(t *testing.T) {
	for _, version := range []int64{4, 5, 6} {
		definition := historicalWorkflowDefinition(t, "workflow.implementation", version)
		if err := validateWorkflowActionPayload(definition, "record_proposal", json.RawMessage(`{}`)); err != nil {
			t.Errorf("version %d refused its released empty call: %v", version, err)
		}
		if err := validateWorkflowActionPayload(definition, "record_proposal", json.RawMessage(`{"problem":"The recorded problem."}`)); err == nil {
			t.Errorf("version %d accepted a field it never declared", version)
		}
	}
}

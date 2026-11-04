package store

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestWorkflowPlanApprovalRecordsLegislativeSubjects(t *testing.T) {
	ctx := t.Context()
	const workID = "plan-approval-predicates"
	s := openTemp(t)
	seedWork(t, s, workID)
	seedWorkflowLaw(t, s)
	owner := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/owner", SessionRef: "session/" + workID, ActorClass: ActorAgent}
	ownerRef, err := WorkflowActorRef(owner)
	if err != nil {
		t.Fatal(err)
	}
	ctx, err = s.EstablishKnowledgeSourceSetProof(ctx, workID)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := initializeWorkflowRawTx(ctx, tx, WorkflowInitializationRequest{WorkID: workID, Actor: owner, Now: time.Unix(9, 0).UTC()}); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	seedWorkflowPlanFrontier(t, s, workID, WorkflowStepPlanChange, "plan")
	payload := testApprovalPayload(t, s, workID, "approve_contract", mustJSONValue(map[string]any{
		"contract_version": 1, "premise": "deliver the change", "legislative_subjects": []string{"spec:one"}, "route_conventions": []string{}, "spec_mandate": []string{}, "law_modifies": []string{}, "rigor_class": "prototype_internal",
	}))
	version := readWorkVersion(t, s, workID)
	operationID := "approval-" + workID
	tx, err = s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	fields, err := workflowActionObject(payload)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(10, 0).UTC()
	events, err := workflowApproveContractEvents(ctx, tx, WorkflowStepPlanChange, WorkflowActionExecutionRequest{WorkID: workID, Now: now}, ownerRef, fields, operationID+":semantic", version)
	if err != nil || len(events) != 1 {
		t.Fatalf("approval constructor=%+v err=%v", events, err)
	}
	var emitted map[string]json.RawMessage
	if err := json.Unmarshal(events[0].Payload, &emitted); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(emitted["legislative_subjects"]), "spec:one") || string(emitted["decision_domains"]) != "[]" {
		t.Fatalf("approval payload=%s", events[0].Payload)
	}
	_, err = applyWorkflowActionRawTx(ctx, tx, newWriteScope(tx), WorkflowActionExecutionRequest{WorkID: workID, ExpectedVersion: version, ActionID: "approve_contract", Payload: payload, Actor: owner, AcceptedInputsDigest: "sha256:" + strings.Repeat("c", 64), IdempotencyIdentity: operationID, OperationID: operationID, PrincipalRef: owner.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: operationID, RequestID: "request:" + operationID, ContractDigest: testManifestDigest, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var digest string
	if err := s.DatabaseForTesting().QueryRow(`SELECT payload_digest FROM effect_receipts WHERE subject_id=? AND kind=? ORDER BY effect_seq DESC LIMIT 1`, workID, WorkflowContractApproved).Scan(&digest); err != nil {
		t.Fatal(err)
	}
	want, err := effectPayloadDigest(events[0].Payload)
	if err != nil || digest != want {
		t.Fatalf("approval digest=%q want=%q err=%v", digest, want, err)
	}
	contract, err := readWorkflowContractVersion(ctx, s.db, workID, 1)
	if err != nil || len(contract.LegislativeSubjects) != 1 || contract.LegislativeSubjects[0] != "spec:one" {
		t.Fatalf("contract=%+v err=%v", contract, err)
	}
}

func reviseExplicitPredicateContract(t *testing.T, s *Store, workID string, owner WorkflowActor, contract map[string]any, expected int64) error {
	t.Helper()
	actor, err := WorkflowActorRef(owner)
	if err != nil {
		return err
	}
	var kind string
	if err := s.db.QueryRowContext(t.Context(), `SELECT kind FROM work_items WHERE id=?`, workID).Scan(&kind); err != nil {
		return err
	}
	payload, err := json.Marshal(map[string]any{"title": "Updated plan", "value_statement": "Retain valid proof", "kind": kind, "tags": []string{}, "reason": "revise the plan", "expected_version": expected, "resulting_version": expected + 1, "contract": contract})
	if err != nil {
		return err
	}
	ctx, err := s.EstablishKnowledgeSourceSetProof(t.Context(), workID)
	if err != nil {
		return err
	}
	return ApplyOperation(ctx, s, Operation{Events: []Event{{EventID: fmt.Sprintf("record-update-%s-%d", workID, expected), Kind: "work.intent_revised", SubjectType: SubjectWorkItem, SubjectID: workID, Actor: actor, OccurredAt: time.Unix(40, expected).UTC(), PayloadVersion: 1, Payload: payload}}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): expected}})
}

func TestWorkflowPlanContractUpdateValidatesLegislativeSubjects(t *testing.T) {
	const workID = "contract-update-predicates"
	f := seedWorkflowReturnRouteFixtureWithPlan(t, workID, WorkflowStepPlanFix, "implement", nil)
	contract := pContractSuccessor(t, f.store, workID, "Continue with explicit corrected criteria.")
	if err := reviseExplicitPredicateContract(t, f.store, workID, f.owner, contract, readWorkVersion(t, f.store, workID)); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"missing", "duplicate", "invalid type"} {
		t.Run(name, func(t *testing.T) {
			contract := pContractSuccessor(t, f.store, workID, "Require explicit outcome predicates.")
			switch name {
			case "missing":
				delete(contract, "legislative_subjects")
			case "duplicate":
				contract["legislative_subjects"] = []string{"spec:one", "spec:one"}
			case "invalid type":
				contract["legislative_subjects"] = "spec:one"
			}
			version := readWorkVersion(t, f.store, workID)
			if err := reviseExplicitPredicateContract(t, f.store, workID, f.owner, contract, version); !hasFailureKind(err, KindInvalidPayload) {
				t.Fatalf("refusal=%v", err)
			}
			if got := readWorkVersion(t, f.store, workID); got != version {
				t.Fatalf("refusal changed version %d to %d", version, got)
			}
		})
	}
}

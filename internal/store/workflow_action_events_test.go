package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func issue31WorkflowAction(t *testing.T, s *Store, workID string, version int64, actionID, operationID string, actor WorkflowActor) int64 {
	return issue31WorkflowActionWithPayload(t, s, workID, version, actionID, operationID, actor, json.RawMessage(`{}`))
}

func issue31WorkflowActionWithPayload(t *testing.T, s *Store, workID string, version int64, actionID, operationID string, actor WorkflowActor, payload json.RawMessage) int64 {
	t.Helper()
	payload = testApprovalPayload(t, s, workID, actionID, payload)
	ctx, err := s.PrepareWorkflowActionProof(context.Background(), actionID, workID)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := applyWorkflowActionRawTx(ctx, tx, newWriteScope(tx), WorkflowActionExecutionRequest{
		WorkID: workID, ExpectedVersion: version, ActionID: actionID, Payload: payload, Actor: actor,
		AcceptedInputsDigest: "sha256:issue31", IdempotencyIdentity: operationID, OperationID: operationID,
		PrincipalRef: actor.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: operationID,
		RequestID: "request:" + operationID, ContractDigest: testManifestDigest, Now: time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		tx.Rollback()
		t.Fatalf("action %s: %v", actionID, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return result.ResultingVersion
}

func TestGenericApplyOperationRejectsEveryReservedWorkflowEvent(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	var reserved []string
	for kind, registration := range eventKindRegistry {
		if registration.Authority == EventAppendAuthorityWorkflow {
			reserved = append(reserved, kind)
		}
	}
	for _, kind := range reserved {
		err := ApplyOperation(context.Background(), s, Operation{Events: []Event{{
			EventID: "reserved-" + kind, Kind: kind, SubjectType: SubjectWorkItem, SubjectID: "work-issue31",
			Actor: "actor:issue31", OccurredAt: time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC), PayloadVersion: CurrentEventPayloadVersion(kind), Payload: json.RawMessage(`{}`),
		}}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "work-issue31"): 0}})
		if err == nil {
			t.Fatalf("ApplyOperation accepted reserved event %q", kind)
		}
	}
	var count int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM effect_receipts WHERE kind LIKE 'workflow.%'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("reserved event append left %d rows", count)
	}
}

func TestWorkflowActionSemanticEventsAreFollowedByUniversalCompletion(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	seedWork(t, s, "workflow-issue31-actions")
	seedWorkflowLaw(t, s)
	actor := WorkflowActor{PrincipalRef: "principal:issue31", ClientRef: "client:issue31", AgentRef: "agent:issue31", SessionRef: "session:issue31", ActorClass: ActorAgent}
	ctx, err := s.EstablishKnowledgeSourceSetProof(context.Background(), "workflow-issue31-actions")
	if err != nil {
		t.Fatal(err)
	}
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := initializeWorkflowRawTx(ctx, tx, WorkflowInitializationRequest{WorkID: "workflow-issue31-actions", Actor: actor, Now: time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC)}); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	seedWorkflowPlanFrontier(t, s, "workflow-issue31-actions", WorkflowStepPlanChange, "plan")
	version := readWorkVersion(t, s, "workflow-issue31-actions")
	initialVersion := version
	plan := json.RawMessage(`{"outcomes":"working outcome","approach":"bounded change","decisions":[{"id":"decision:one","question":"which?","choice":"one","rationale":"fits","rejected":[]}],"touches":["file:one"],"merge_split":"one item","risks":"bounded risk"}`)
	version = issue31WorkflowActionWithPayload(t, s, "workflow-issue31-actions", version, "record_plan", "issue31-plan", actor, plan)
	approval := json.RawMessage(`{"spec_mandate":[],"law_modifies":[],"architecture_binding":{"home_domain_id":"root","affected_domain_ids":["root"],"domain_modifies":[],"domain_relation_modifies":[],"law_additions":[],"verification_obligations":[]}}`)
	version = issue31WorkflowActionWithPayload(t, s, "workflow-issue31-actions", version, "approve_contract", "issue31-approve", actor, approval)
	if version != initialVersion+4 {
		t.Fatalf("approve_contract resulting version=%d, want %d", version, initialVersion+4)
	}
	moveWorkflowPlanFixtureFrontier(t, s, "workflow-issue31-actions", "implement")
	version = issue31WorkflowActionWithPayload(t, s, "workflow-issue31-actions", version, "bind_evidence", "issue31-evidence", actor, json.RawMessage(`{"evidence_kind":"artifact","evidence_ref":"artifact:issue31"}`))
	if version != initialVersion+6 {
		t.Fatalf("bind_evidence resulting version=%d, want %d", version, initialVersion+6)
	}
	rows, err := s.DatabaseForTesting().Query(`SELECT kind FROM effect_receipts WHERE subject_id=? AND (kind=? OR kind=? OR kind=? OR kind=?) ORDER BY effect_seq`, "workflow-issue31-actions", WorkflowPlanRecorded, WorkflowContractApproved, WorkflowEvidenceBound, WorkflowActionCompleted)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var kinds []string
	for rows.Next() {
		var kind string
		if err := rows.Scan(&kind); err != nil {
			t.Fatal(err)
		}
		kinds = append(kinds, kind)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	want := []string{WorkflowPlanRecorded, WorkflowActionCompleted, WorkflowContractApproved, WorkflowActionCompleted, WorkflowEvidenceBound, WorkflowActionCompleted}
	if len(kinds) != len(want) {
		t.Fatalf("semantic event order=%v, want %v", kinds, want)
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Fatalf("event[%d]=%q, want %q", i, kinds[i], want[i])
		}
	}
}

func TestApproveContractPersistsEvidenceAndRigor(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name             string
		extraFields      string
		requiredEvidence string
		rigorClass       string
	}{
		{name: "explicit", extraFields: `"required_evidence":["artifact"],"rigor_class":"critical_public",`, requiredEvidence: `["artifact"]`, rigorClass: "critical_public"},
		{name: "defaults", requiredEvidence: `["verification","review","artifact"]`, rigorClass: "prototype_internal"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			s := openTemp(t)
			workID := "workflow-contract-fields-" + testCase.name
			seedWork(t, s, workID)
			seedWorkflowLaw(t, s)
			actor := WorkflowActor{PrincipalRef: "principal:contract-fields", ClientRef: "client:contract-fields", AgentRef: "agent:contract-fields", SessionRef: "session:contract-fields", ActorClass: ActorAgent}
			ctx, err := s.EstablishKnowledgeSourceSetProof(context.Background(), workID)
			if err != nil {
				t.Fatal(err)
			}
			tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := initializeWorkflowRawTx(ctx, tx, WorkflowInitializationRequest{WorkID: workID, Actor: actor, Now: time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC)}); err != nil {
				tx.Rollback()
				t.Fatal(err)
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			seedWorkflowPlanFrontier(t, s, workID, WorkflowStepPlanChange, "plan")
			version := readWorkVersion(t, s, workID)
			approval := json.RawMessage(`{` + testCase.extraFields + `"spec_mandate":[],"law_modifies":[],"architecture_binding":{"home_domain_id":"root","affected_domain_ids":["root"],"domain_modifies":[],"domain_relation_modifies":[],"law_additions":[],"verification_obligations":[]}}`)
			issue31WorkflowActionWithPayload(t, s, workID, version, "approve_contract", "contract-fields-"+testCase.name+"-approve", actor, approval)
			var requiredEvidence, rigorClass string
			if err := s.DatabaseForTesting().QueryRow(`SELECT required_evidence,rigor_class FROM workflow_contracts WHERE work_id=? AND contract_version=1`, workID).Scan(&requiredEvidence, &rigorClass); err != nil {
				t.Fatal(err)
			}
			if requiredEvidence != testCase.requiredEvidence || rigorClass != testCase.rigorClass {
				t.Fatalf("stored contract fields=(%s,%s), want (%s,%s)", requiredEvidence, rigorClass, testCase.requiredEvidence, testCase.rigorClass)
			}
		})
	}
}

func TestWorkflowContractApprovalPinsLawBoundaryForLegacySurface(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	workID := "workflow-law-pin-legacy-surface"
	seedWork(t, s, workID)
	seedWorkflowLaw(t, s)
	actor := WorkflowActor{PrincipalRef: "principal:law-pin", ClientRef: "client:law-pin", AgentRef: "agent:law-pin", SessionRef: "session:law-pin", ActorClass: ActorAgent}
	ctx, err := s.EstablishKnowledgeSourceSetProof(context.Background(), workID)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := initializeWorkflowRawTx(ctx, tx, WorkflowInitializationRequest{WorkID: workID, Actor: actor, Now: time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC)}); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	seedWorkflowPlanFrontier(t, s, workID, WorkflowStepPlanChange, "plan")
	version := readWorkVersion(t, s, workID)
	version = issue31WorkflowActionWithPayload(t, s, workID, version, "approve_contract", "law-pin-approve", actor, json.RawMessage(`{"spec_mandate":["spec:one"]}`))

	var boundaryVersion int
	if err := s.DatabaseForTesting().QueryRow(`SELECT law_boundary_version FROM workflow_contracts WHERE work_id=? AND contract_version=1`, workID).Scan(&boundaryVersion); err != nil {
		t.Fatal(err)
	}
	if boundaryVersion != 1 {
		t.Fatalf("law boundary version=%d, want 1 for a new contract submitted through the legacy surface", boundaryVersion)
	}
	var mandate string
	if err := s.DatabaseForTesting().QueryRow(`SELECT spec_mandate FROM workflow_contracts WHERE work_id=? AND contract_version=1`, workID).Scan(&mandate); err != nil || mandate != `["spec:one"]` {
		t.Fatalf("approval law mandate=%q err=%v", mandate, err)
	}
	if version == 0 {
		t.Fatal("approval route did not produce a resulting version")
	}
}

func issue31ActorSetup(t *testing.T, workID string) (*Store, WorkflowActor, int64) {
	t.Helper()
	s := openTemp(t)
	seedWork(t, s, workID)
	executor := WorkflowActor{PrincipalRef: "principal:issue31", ClientRef: "client:issue31", AgentRef: "agent:executor", SessionRef: "session:executor", ActorClass: ActorAgent}
	evaluator := WorkflowActor{PrincipalRef: "principal:issue31", ClientRef: "client:issue31", AgentRef: "agent:evaluator", SessionRef: "session:evaluator", ActorClass: ActorOperator}
	tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := initializeWorkflowRawTx(context.Background(), tx, WorkflowInitializationRequest{WorkID: workID, Actor: executor, Now: time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC)}); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	version := readWorkVersion(t, s, workID)
	evaluatorRef, err := WorkflowActorRef(evaluator)
	if err != nil {
		t.Fatal(err)
	}
	actorPayload := map[string]any{"work_id": workID, "expected_version": version, "resulting_version": version + 1, "actor_ref": evaluatorRef, "principal_ref": evaluator.PrincipalRef, "client_ref": evaluator.ClientRef, "agent_ref": evaluator.AgentRef, "session_ref": evaluator.SessionRef, "actor_class": string(evaluator.ActorClass)}
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{workflowEventWithActor(workID+":evaluator", WorkflowActorRecorded, workID, evaluatorRef, actorPayload)}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}}); err != nil {
		t.Fatal(err)
	}
	return s, executor, version + 1
}

func TestWorkflowAuthenticatedActorBindingRejectsSpoofedVerdictAndPremise(t *testing.T) {
	t.Parallel()
	spoofed := []struct {
		name  string
		actor WorkflowActor
	}{
		{name: "operator row", actor: WorkflowActor{PrincipalRef: "principal:issue31", ClientRef: "client:issue31", AgentRef: "agent:evaluator", SessionRef: "session:evaluator", ActorClass: ActorOperator}},
		{name: "same principal different process", actor: WorkflowActor{PrincipalRef: "principal:issue31", ClientRef: "client:other", AgentRef: "agent:other", SessionRef: "session:other", ActorClass: ActorAgent}},
		{name: "unknown actor", actor: WorkflowActor{PrincipalRef: "principal:unknown", ClientRef: "client:unknown", AgentRef: "agent:unknown", SessionRef: "session:unknown", ActorClass: ActorAgent}},
	}
	for _, testCase := range spoofed {
		t.Run("verdict "+testCase.name, func(t *testing.T) {
			s, executor, version := issue31ActorSetup(t, "spoof-verdict-"+strings.ReplaceAll(testCase.name, " ", "-"))
			spoofRef, err := WorkflowActorRef(testCase.actor)
			if err != nil {
				t.Fatal(err)
			}
			event := workflowEventWithActor("spoof-verdict", WorkflowVerdictRecorded, "spoof-verdict-"+strings.ReplaceAll(testCase.name, " ", "-"), mustActorRef(t, executor), map[string]any{"work_id": "spoof-verdict-" + strings.ReplaceAll(testCase.name, " ", "-"), "expected_version": version, "resulting_version": version + 1, "contract_version": 1, "predicate_id": "predicate:spoof", "verdict_kind": "ok", "verdict_actor_ref": spoofRef, "evaluation_evidence": []string{"evidence:spoof"}, "incomparable_with_approved": false})
			if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, event.SubjectID): version}}); err == nil {
				t.Fatal("spoofed verdict was accepted")
			}
			assertIssue31VersionAndNoVerdict(t, s, event.SubjectID, version)
		})
	}

	// The fold checks the actor binding before criterion proof, so the
	// accepted control needs a current reviewed candidate while the spoofed
	// verdicts above refuse on their actor alone.
	valid := qBuildAcceptance(t, "valid-evaluator", "", false)
	validVersion := readWorkVersion(t, valid.store, "valid-evaluator")
	if err := runVerdictActionAs(t, valid.store, "valid-evaluator", "record_verdict", qAssociationPayload(t, valid), 0, valid.owner); err != nil {
		t.Fatalf("valid recorded evaluator rejected: %v", err)
	}
	assertIssue31VersionAndVerdict(t, valid.store, "valid-evaluator", readWorkVersion(t, valid.store, "valid-evaluator"))
	if after := readWorkVersion(t, valid.store, "valid-evaluator"); after <= validVersion {
		t.Fatalf("valid verdict left version %d, want it advanced past %d", after, validVersion)
	}

	version := readWorkVersion(t, valid.store, valid.workID)
	ownerRef := mustActorRef(t, valid.owner)
	for _, testCase := range spoofed {
		spoofRef := mustActorRef(t, testCase.actor)
		premise := workflowEventWithActor("spoof-premise-"+strings.ReplaceAll(testCase.name, " ", "-"), WorkflowPremiseConfirmed, valid.workID, ownerRef, map[string]any{"work_id": valid.workID, "expected_version": version, "resulting_version": version + 1, "contract_version": 1, "confirming_actor_ref": spoofRef, "confirmation_authority": "coordinator"})
		if err := applyWorkflowTestOperation(context.Background(), valid.store, Operation{Events: []Event{premise}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, valid.workID): version}}); err == nil {
			t.Fatalf("spoofed premise accepted for %s", testCase.name)
		}
		var confirmations int
		if err := valid.store.db.QueryRow(`SELECT count(*) FROM workflow_premise_confirmations WHERE work_id=?`, valid.workID).Scan(&confirmations); err != nil || confirmations != 0 || readWorkVersion(t, valid.store, valid.workID) != version {
			t.Fatalf("spoofed premise changed state: confirmations=%d err=%v", confirmations, err)
		}
	}
	premise := workflowEventWithActor("valid-premise", WorkflowPremiseConfirmed, valid.workID, ownerRef, map[string]any{"work_id": valid.workID, "expected_version": version, "resulting_version": version + 1, "contract_version": 1, "confirming_actor_ref": ownerRef, "confirmation_authority": "coordinator"})
	if err := applyWorkflowTestOperation(context.Background(), valid.store, Operation{Events: []Event{premise}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, valid.workID): version}}); err != nil {
		t.Fatalf("valid authenticated coordinator rejected: %v", err)
	}
}

func assertIssue31VersionAndNoVerdict(t *testing.T, s *Store, workID string, want int64) {
	t.Helper()
	var version int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM work_items WHERE id=?`, workID).Scan(&version); err != nil {
		t.Fatal(err)
	}
	var verdicts int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM effect_receipts WHERE subject_id=? AND kind=?`, workID, WorkflowVerdictRecorded).Scan(&verdicts); err != nil {
		t.Fatal(err)
	}
	if version != want || verdicts != 0 {
		t.Fatalf("spoof changed state: version=%d verdict_projection=%d, want version=%d and no verdict projection", version, verdicts, want)
	}
}

func assertIssue31VersionAndVerdict(t *testing.T, s *Store, workID string, want int64) {
	t.Helper()
	var version int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM work_items WHERE id=?`, workID).Scan(&version); err != nil {
		t.Fatal(err)
	}
	var events int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM effect_receipts WHERE subject_id=? AND kind=?`, workID, WorkflowVerdictRecorded).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if version != want || events != 1 {
		t.Fatalf("valid evaluator state: version=%d verdict_events=%d", version, events)
	}
}

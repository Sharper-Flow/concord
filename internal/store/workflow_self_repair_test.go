package store

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestMigrateV81AddsWorkflowSelfRepairClassification(t *testing.T) {
	ctx := context.Background()
	db := openMigratedTo(t, filepath.Join(t.TempDir(), "concord-v80.db"), 80)
	var columns int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM pragma_table_info('workflow_contracts') WHERE name='self_repair_json'`).Scan(&columns); err != nil {
		t.Fatal(err)
	}
	if columns != 0 {
		t.Fatal("v80 database already contains self_repair_json")
	}
	var v81 migration
	found := false
	for i := range migrations {
		if migrations[i].Version == 81 {
			v81 = migrations[i]
			found = true
			break
		}
	}
	if !found {
		t.Fatal("migration 81 is not registered")
	}
	if err := applyMigration(ctx, db, v81); err != nil {
		t.Fatalf("apply migration 81: %v", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM pragma_table_info('workflow_contracts') WHERE name='self_repair_json'`).Scan(&columns); err != nil {
		t.Fatal(err)
	}
	if columns != 1 {
		t.Fatalf("self_repair_json columns = %d, want 1", columns)
	}
}

func TestWorkflowSelfRepairStillRequiresCurrentDomainRegistry(t *testing.T) {
	ctx := context.Background()
	s, _, binding, _, _, _ := architectureValidationFixtureWithProductKey(t, "self-repair-stale", "concord")
	seedWork(t, s, "ordinary-peer-stale")
	actor, selfVersion := seedProductChangingContract(t, s, "self-repair-stale", binding)
	_, _ = seedProductChangingContract(t, s, "ordinary-peer-stale", binding)
	supersede := workflowEventWithActor("self-repair-stale-contract-v2", WorkflowContractSuperseded, "self-repair-stale", mustActorRef(t, actor), map[string]any{
		"work_id": "self-repair-stale", "expected_version": selfVersion, "resulting_version": selfVersion + 1,
		"previous_contract_version": int64(1), "new_contract_version": int64(2), "supersede_reason": "classify workflow self-repair",
		"audit_evidence": []string{"obs:0000000000000001"}, "successor_contract": selfRepairSuccessorContract(binding),
	})
	supersede = pOverlapContractFacts(t, s, supersede)
	if err := applyWorkflowTestOperation(ctx, s, Operation{Events: []Event{supersede}, ExpectedVersions: workVersion("self-repair-stale", selfVersion)}); err != nil {
		t.Fatalf("classify self-repair: %v", err)
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1); UPDATE domains SET status='deprecated' WHERE product_id='product' AND domain_id='child'; DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	err := InspectWorkflowDomainOverlap(ctx, s, "self-repair-stale")
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindStaleRequiresReview {
		t.Fatalf("self-repair bypassed stale Domain registry validation: %v", err)
	}
}

func TestWorkflowSelfRepairRequiresConcordAndRefusesWorkerAuthority(t *testing.T) {
	for _, testCase := range []struct {
		name       string
		productKey string
		operator   bool
		detail     string
	}{
		{name: "foreign Product", productKey: "other", operator: true, detail: "self_repair is available only for Concord's own workflow defects"},
		{name: "worker classification", productKey: "concord", detail: "worker actors cannot classify contract self-repair"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			ctx := context.Background()
			workID := "self-repair-" + testCase.productKey
			s, _, binding, _, _, _ := architectureValidationFixtureWithProductKey(t, workID, testCase.productKey)
			actor, version := seedProductChangingContract(t, s, workID, binding)
			actorRef, err := WorkflowActorRef(actor)
			if err != nil {
				t.Fatal(err)
			}
			if testCase.operator {
				actorRef = seedSelfRepairOperator(t, s)
			} else {
				dispatch := workerDispatchEvent(workID, "self-repair-worker-dispatch", BuiltinLaneDefinitions()[0], map[string]any{"lane_actor_ref": actorRef})
				dispatch.Actor = actorRef
				if err := ApplyOperation(ctx, s, Operation{Events: []Event{dispatch}}); err != nil {
					t.Fatal(err)
				}
			}
			supersede := workflowEventWithActor("invalid-self-repair", WorkflowContractSuperseded, workID, actorRef, map[string]any{
				"work_id": workID, "expected_version": version, "resulting_version": version + 1,
				"previous_contract_version": int64(1), "new_contract_version": int64(2), "supersede_reason": "invalid classification",
				"audit_evidence": []string{"obs:typed-refusal"}, "successor_contract": selfRepairSuccessorContract(binding),
			})
			supersede = pOverlapContractFacts(t, s, supersede)
			assertSelfRepairRefusalWithoutEffects(t, s, supersede, version, KindUnauthorized, "workflow_self_repair", testCase.detail)
		})
	}
}

func TestInitialContractSelfRepairAdmissionRunsBeforeAppend(t *testing.T) {
	t.Parallel()
	const workID = "initial-self-repair-admission"
	s, _, binding, _, _, _ := architectureValidationFixtureWithProductKey(t, workID, "concord")
	actor, version := seedProductChangingContract(t, s, workID, binding)
	actorRef, err := WorkflowActorRef(actor)
	if err != nil {
		t.Fatal(err)
	}
	payload := selfRepairSuccessorContract(binding)
	payload["work_id"], payload["expected_version"], payload["resulting_version"] = workID, version, version+1
	event := workflowEventWithActor("initial-self-repair-invalid", WorkflowContractApproved, workID, actorRef, payload)
	event = pOverlapContractFacts(t, s, event)
	event = withMalformedSelfRepairReference(t, event)
	assertSelfRepairRefusalWithoutEffects(t, s, event, version, KindInvalidPayload, "validate_event", "self_repair")
}

func TestSupersedingContractSelfRepairAdmissionRunsBeforeAppend(t *testing.T) {
	t.Parallel()
	const workID = "superseding-self-repair-admission"
	s, _, binding, _, _, _ := architectureValidationFixtureWithProductKey(t, workID, "concord")
	_, version := seedProductChangingContract(t, s, workID, binding)
	operator := seedSelfRepairOperator(t, s)
	successor := selfRepairSuccessorContract(binding)
	event := workflowEventWithActor("superseding-self-repair-invalid", WorkflowContractSuperseded, workID, operator, map[string]any{
		"work_id": workID, "expected_version": version, "resulting_version": version + 1,
		"previous_contract_version": int64(1), "new_contract_version": int64(2), "supersede_reason": "classify workflow self-repair",
		"audit_evidence": []string{"obs:self-repair"}, "successor_contract": successor,
	})
	event = pOverlapContractFacts(t, s, event)
	event = withMalformedSelfRepairReference(t, event)
	assertSelfRepairRefusalWithoutEffects(t, s, event, version, KindInvalidPayload, "validate_event", "self_repair")
}

func withMalformedSelfRepairReference(t *testing.T, event Event) Event {
	t.Helper()
	if err := validateRegisteredEvent(event); err != nil {
		t.Fatalf("valid self-repair fixture does not match the current schema: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	contract := payload
	if event.Kind == WorkflowContractSuperseded {
		contract = payload["successor_contract"].(map[string]any)
	}
	contract["self_repair"].(map[string]any)["blocked_operation"] = "workflow_action.\u00a0dispatch_worker"
	event.Payload = mustJSONValue(payload)
	return event
}

func assertSelfRepairRefusalWithoutEffects(t *testing.T, s *Store, event Event, version int64, kind FailureKind, op, detail string) {
	t.Helper()
	before := fullWorkflowProjectionSnapshot(t, s)
	eventsBefore, versionBefore := completionFoldBookkeeping(t, s, event.SubjectID)
	err := applyWorkflowTestOperation(t.Context(), s, Operation{Events: []Event{event}, ExpectedVersions: workVersion(event.SubjectID, version)})
	var failure *Failure
	if !failureAs(err, &failure) || failure.Kind != kind || failure.Op != op || !strings.Contains(err.Error(), detail) {
		t.Fatalf("self-repair refusal = %v, want %s from %s naming %q", err, kind, op, detail)
	}
	assertCompletionFoldRolledBack(t, s, event.SubjectID, eventsBefore, versionBefore)
	if after := fullWorkflowProjectionSnapshot(t, s); after != before {
		t.Fatalf("refused self-repair changed projection rows:\nbefore=%s\nafter=%s", before, after)
	}
}

func selfRepairSuccessorContract(binding WorkflowArchitectureBinding) map[string]any {
	return map[string]any{
		"contract_version": int64(2), "premise": "repair a Concord workflow refusal", "outcome_kind": "check",
		"outcome_payload":   map[string]any{"kind": "check", "check_ref": "check:self-repair", "immutable_subject_ref": "commit:self-repair", "expected_result": "pass"},
		"required_evidence": []string{"verification", "review"}, "route_conventions": []string{}, "spec_mandate": []string{"spec:one"}, "law_modifies": []string{},
		"law_revisions": []WorkflowLawRevision{{LawID: "spec:one", ContentHash: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}, "law_boundary_version": 1,
		"rigor_class": "production_safety_critical", "consequence_class": "internal_sqlite", "architecture_binding": binding,
		"self_repair": WorkflowSelfRepair{RefusalKind: string(KindDomainOverlap), BlockedOperation: "workflow_action.dispatch_worker", EvidenceRefs: []string{"obs:0000000000000001"}},
	}
}

func seedSelfRepairOperator(t *testing.T, s *Store) string {
	t.Helper()
	actor := WorkflowActor{PrincipalRef: "principal:operator", ClientRef: "client:self-repair", AgentRef: "agent:operator", SessionRef: "session:self-repair", ActorClass: ActorOperator}
	actorRef, err := WorkflowActorRef(actor)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := enterFold(context.Background(), tx); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO workflow_actors(actor_ref,principal_ref,client_ref,agent_ref,session_ref,actor_class,first_seen_at) VALUES(?,?,?,?,?,?,?)`, actorRef, actor.PrincipalRef, actor.ClientRef, actor.AgentRef, actor.SessionRef, actor.ActorClass, "2026-09-14T00:00:00Z"); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := leaveFold(context.Background(), tx); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return actorRef
}

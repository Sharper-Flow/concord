package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestMigrateV81AddsWorkflowSelfRepairClassification(t *testing.T) {
	ctx := context.Background()
	db := openMigratedTo(t, filepath.Join(t.TempDir(), "concord-v80.db"), len(migrations)-1)
	var columns int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM pragma_table_info('workflow_contracts') WHERE name='self_repair_json'`).Scan(&columns); err != nil {
		t.Fatal(err)
	}
	if columns != 0 {
		t.Fatal("v80 database already contains self_repair_json")
	}
	last := migrations[len(migrations)-1]
	if last.Version != 81 {
		t.Fatalf("last migration version = %d, want 81", last.Version)
	}
	if err := applyMigration(ctx, db, last); err != nil {
		t.Fatalf("apply migration 81: %v", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM pragma_table_info('workflow_contracts') WHERE name='self_repair_json'`).Scan(&columns); err != nil {
		t.Fatal(err)
	}
	if columns != 1 {
		t.Fatalf("self_repair_json columns = %d, want 1", columns)
	}
}

func TestWorkflowSelfRepairBypassesOverlapForClassifiedWorkOnly(t *testing.T) {
	ctx := context.Background()
	s, _, binding, _, _, _ := architectureValidationFixtureWithProductKey(t, "self-repair", "concord")
	seedWork(t, s, "ordinary-peer")
	_, selfVersion := seedProductChangingContract(t, s, "self-repair", binding)
	_, _ = seedProductChangingContract(t, s, "ordinary-peer", binding)

	if err := CheckWorkflowDomainOverlap(ctx, s, "self-repair"); err == nil {
		t.Fatal("precondition: self-repair work has no unresolved overlap")
	}
	operator := seedSelfRepairOperator(t, s)
	successor := selfRepairSuccessorContract(binding)
	supersede := workflowEventWithActor("self-repair-contract-v2", WorkflowContractSuperseded, "self-repair", operator, map[string]any{
		"work_id": "self-repair", "expected_version": selfVersion, "resulting_version": selfVersion + 1,
		"previous_contract_version": int64(1), "new_contract_version": int64(2), "supersede_reason": "classify workflow self-repair",
		"audit_evidence": []string{"obs:0000000000000001"}, "successor_contract": successor,
	})
	supersede.PayloadVersion = 2
	if err := applyWorkflowTestOperation(ctx, s, Operation{Events: []Event{supersede}, ExpectedVersions: workVersion("self-repair", selfVersion)}); err != nil {
		t.Fatalf("classify self-repair: %v", err)
	}

	if err := CheckWorkflowDomainOverlap(ctx, s, "self-repair"); err != nil {
		t.Fatalf("classified self-repair stayed overlap-blocked: %v", err)
	}
	err := CheckWorkflowDomainOverlap(ctx, s, "ordinary-peer")
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindDomainOverlap {
		t.Fatalf("ordinary peer escaped overlap authority: %v", err)
	}

	projection, err := ReadWorkflowProjection(ctx, s, WorkflowReadRequest{WorkID: "self-repair", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if projection.Contract == nil || projection.Contract.SelfRepair == nil || projection.Contract.SelfRepair.RefusalKind != string(KindDomainOverlap) {
		t.Fatalf("self-repair classification missing from read projection: %#v", projection.Contract)
	}
	continuity, err := ReadWorkflowContinuity(ctx, s, ContinuityRequest{Work: "self-repair"})
	if err != nil {
		t.Fatal(err)
	}
	if len(continuity.UnresolvedOverlaps) == 0 {
		t.Fatal("self-repair exemption hid the unresolved overlap")
	}
}

func TestWorkflowSelfRepairRequiresConcordAndOperatorAuthority(t *testing.T) {
	for _, testCase := range []struct {
		name       string
		productKey string
		operator   bool
	}{
		{name: "foreign Product", productKey: "other", operator: true},
		{name: "agent classification", productKey: "concord", operator: false},
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
			}
			supersede := workflowEventWithActor("invalid-self-repair", WorkflowContractSuperseded, workID, actorRef, map[string]any{
				"work_id": workID, "expected_version": version, "resulting_version": version + 1,
				"previous_contract_version": int64(1), "new_contract_version": int64(2), "supersede_reason": "invalid classification",
				"audit_evidence": []string{"obs:typed-refusal"}, "successor_contract": selfRepairSuccessorContract(binding),
			})
			supersede.PayloadVersion = 2
			if err := applyWorkflowTestOperation(ctx, s, Operation{Events: []Event{supersede}, ExpectedVersions: workVersion(workID, version)}); err == nil {
				t.Fatal("invalid self-repair classification accepted")
			}
		})
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

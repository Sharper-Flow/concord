package store

import (
	"context"
	"strings"
	"testing"
	"time"
)

// seedSecondaryProductMembership anchors a second Product, its Project, its
// canonical knowledge home, and one secondary work membership, reproducing the
// cross-Product shape a set_memberships call creates.
func seedSecondaryProductMembership(t *testing.T, s *Store, workID string) {
	t.Helper()
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1);
		INSERT INTO products(id,display_name,stage_maturity,stage_audience_commitment,version,created_at,updated_at) VALUES('product-secondary','Secondary','prototype','operator_only',1,'now','now');
		INSERT INTO projects(id,display_name,version,created_at,updated_at) VALUES('project-secondary','Secondary',1,'now','now');
		INSERT INTO product_projects(product_id,project_id,role) VALUES('product-secondary','project-secondary','primary');
		INSERT INTO project_locators(locator_id,project_id,kind,locator_value,normalized_value,created_at,updated_at) VALUES('secondary-locator','project-secondary','canonical_path','/test/secondary','/test/secondary','now','now');
		INSERT INTO product_knowledge_homes(product_id,project_id,locator_id) VALUES('product-secondary','project-secondary','secondary-locator');
		INSERT INTO work_projects(work_id,project_id,role) VALUES(?,'project-secondary','secondary');
		DELETE FROM fold_guard`, workID); err != nil {
		t.Fatalf("seed secondary Product membership: %v", err)
	}
}

// seedWorkflowLawFixture initializes one non-Product-changing workflow on a
// work item whose primary Project carries the seeded law home, ready for a
// contract approval that binds spec:one.
func seedWorkflowLawFixture(t *testing.T, s *Store, workID string) WorkflowActor {
	t.Helper()
	ctx := context.Background()
	seedWork(t, s, workID)
	seedWorkflowLaw(t, s)
	actor := WorkflowActor{PrincipalRef: "principal:law-home", ClientRef: "client:law-home", AgentRef: "agent:law-home", SessionRef: "session:law-home", ActorClass: ActorAgent}
	definition := workflowFixtureDefinition(t, 1)
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := initializeWorkflowRawTx(ctx, tx, WorkflowInitializationRequest{WorkID: workID, Definition: definition, Actor: actor, Now: time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC)}); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return actor
}

// A work item whose primary Project sits in one Product and whose secondary
// Project sits in another resolves its Git law home over the primary
// membership alone: the primary Project's Product owns the contract, so a
// secondary membership in another Product must not make the law boundary
// ambiguous and the workflow unrecordable (CD-0178/CD-0182 cross-repository
// route).
func TestWorkflowLawHomeFollowsThePrimaryMembership(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openTemp(t)
	workID := "law-home-cross-product"
	seedWork(t, s, workID)
	seedWorkflowLaw(t, s)
	seedSecondaryProductMembership(t, s, workID)

	project, locator, err := workflowLawHome(ctx, s.DatabaseForTesting(), workID)
	if err != nil {
		t.Fatalf("cross-Product work refused its law home: %v", err)
	}
	if project != "project" || locator != "workflow-law-locator" {
		t.Fatalf("law home = %s/%s, want project/workflow-law-locator", project, locator)
	}
}

// The remaining ambiguity is the real one: a primary Project whose Product
// memberships themselves span two Products with knowledge homes. That refusal
// stays typed and names the enumerated homes, so the envelope can deliver it.
func TestWorkflowLawHomeRefusesAnAmbiguousPrimaryHomeWithCandidates(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openTemp(t)
	workID := "law-home-ambiguous-primary"
	seedWork(t, s, workID)
	seedWorkflowLaw(t, s)
	seedSecondaryProductMembership(t, s, workID)
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1);
		INSERT INTO product_projects(product_id,project_id,role) VALUES('product-secondary','project','secondary');
		DELETE FROM fold_guard`); err != nil {
		t.Fatalf("seed the ambiguous primary home: %v", err)
	}

	_, _, err := workflowLawHome(ctx, s.DatabaseForTesting(), workID)
	var failure *Failure
	if !failureAs(err, &failure) || failure.Kind != KindAmbiguousScope || failure.Op != "check_mandated_laws" {
		t.Fatalf("ambiguous primary home diagnosis = %v, want typed ambiguous_scope from check_mandated_laws", err)
	}
	want := []string{"project/workflow-law-locator", "project-secondary/secondary-locator"}
	if len(failure.CandidateIDs) != len(want) || failure.CandidateIDs[0] != want[0] || failure.CandidateIDs[1] != want[1] {
		t.Fatalf("candidates = %v, want %v", failure.CandidateIDs, want)
	}
}

// The recorded defect: a contract approval on cross-Product work met the
// ambiguous-scope refusal and the workflow action recorded nothing. The
// approval must now fold, resolving the mandated law over the primary
// membership's home.
func TestCrossProductWorkRecordsContractApproval(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openTemp(t)
	workID := "cross-product-approval"
	actor := seedWorkflowLawFixture(t, s, workID)
	seedSecondaryProductMembership(t, s, workID)

	event := workflowEventWithActor("cross-product-approval-event", WorkflowContractApproved, workID, DeriveWorkflowActorRef(actor.PrincipalRef, actor.ClientRef, actor.AgentRef, actor.SessionRef), map[string]any{
		"work_id": workID, "expected_version": int64(4), "resulting_version": int64(5), "contract_version": int64(1), "premise": "record across Products", "outcome_kind": "check", "outcome_payload": map[string]any{"kind": "check", "check_ref": "check:cross-product", "immutable_subject_ref": "commit:cross-product", "expected_result": "pass"}, "required_evidence": []string{"verification", "review"}, "route_conventions": []string{}, "spec_mandate": []string{"spec:one"}, "law_modifies": []string{}, "law_revisions": nil, "law_boundary_version": 1, "rigor_class": "prototype_internal", "consequence_class": "internal_sqlite",
	})
	event.PayloadVersion = 3
	if err := applyWorkflowTestOperation(ctx, s, Operation{Events: []Event{event}, ExpectedVersions: workVersion(workID, 4)}); err != nil {
		t.Fatalf("cross-Product contract approval refused: %v", err)
	}
	var boundaryVersion int64
	var mandate string
	if err := s.DatabaseForTesting().QueryRow(`SELECT law_boundary_version,spec_mandate FROM workflow_contracts WHERE work_id=? AND contract_version=1`, workID).Scan(&boundaryVersion, &mandate); err != nil {
		t.Fatalf("the approved contract did not record: %v", err)
	}
	if boundaryVersion != 1 || !strings.Contains(mandate, "spec:one") {
		t.Fatalf("recorded contract boundary=%d mandate=%s, want boundary 1 mandating spec:one", boundaryVersion, mandate)
	}
}

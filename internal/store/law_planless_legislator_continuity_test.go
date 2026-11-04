package store

// The legislated-authority admission classifies the approving contract by
// its recorded approval fact: the changes_product_truth column, or for NULL
// historical rows the retained architecture binding the approval fold
// persisted exactly for Product-changing contracts. Migration 183 leaves
// every pre-cutover item planless with a NULL column, so the classification
// must not consult the item's current step plan — a pre-plan legislator has
// none and a plan is mutable after approval.
//
// These cases drive the public continuity read and the owning
// workflow-action write boundary against the recorded post-cutover row
// shape of a pre-plan legislating work: completed item, completed planless
// instance, approved active contract with NULL changes_product_truth and a
// retained binding, admitted through law_modifies.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// assertLegislatorRefusal asserts one legislated-authority refusal of the
// exact kind carrying the exact detail, so a control cannot pass on an
// unrelated refusal reason.
func assertLegislatorRefusal(t *testing.T, err error, wantDetail string) {
	t.Helper()
	assertFailureKind(t, err, KindInvalidNoteProof)
	var failure *Failure
	if !errors.As(err, &failure) {
		t.Fatalf("refusal is not a typed Failure: %v", err)
	}
	if !strings.Contains(failure.Error(), wantDetail) {
		t.Fatalf("refusal detail = %q, want it to contain %q", failure.Error(), wantDetail)
	}
}

// seedPlanlessLegislatorFixture seeds one historical legislator in the
// recorded post-cutover shape: a completed planless instance, an approved
// active contract at the declared version whose changes_product_truth is NULL
// (historical absence), the retained architecture binding a Product-changing
// approval persisted at that version, and the primary Project membership that
// resolves the work's designated law home. Synthetic fixture evidence about
// the rule, never a claim of real approval.
func seedPlanlessLegislatorFixture(t *testing.T, s *Store, workID string, version int64, lawModifies string) {
	t.Helper()
	db := s.DatabaseForTesting()
	seedLegislatorPrimaryMembership(t, s, workID)
	if _, err := db.Exec(`INSERT INTO write_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := db.Exec(`DELETE FROM write_guard`); err != nil {
			t.Errorf("remove write guard: %v", err)
		}
	}()
	if _, err := db.Exec(`INSERT OR IGNORE INTO workflow_actors(actor_ref,principal_ref,client_ref,agent_ref,session_ref,actor_class,first_seen_at) VALUES(?,?,?,?,?,'agent','now')`, authorityActorRef, "principal:authority-fixture", "client:authority-fixture", "agent:authority-fixture", "session:authority-fixture"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO workflow_instances(work_id,workflow_type,step_plan,instance_state) VALUES(?, NULL, NULL, 'completed')`, workID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class,changes_product_truth) VALUES(?,?,'Synthetic fixture premise for the authority resolver.','internal_sqlite','[]','[]','2026-09-30T00:21:16Z',?,'[]',?,0,'prototype_internal',NULL)`,
		workID, version, authorityActorRef, lawModifies); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO workflow_architecture_bindings(work_id,contract_version,product_id,home_domain_id,projection_hash) VALUES(?,?,'authority-product','product-root:authority-fixture',?)`,
		workID, version, "sha256:"+strings.Repeat("c", 64)); err != nil {
		t.Fatal(err)
	}
}

// seedLegislatorPrimaryMembership gives a seeded fixture work the primary
// Project membership the public reads resolve its law home and Product
// through. The dedicated Project belongs to exactly the one fixture Product,
// so the work's Product resolution stays unambiguous beside the anchor
// Product the home authorization creates.
func seedLegislatorPrimaryMembership(t *testing.T, s *Store, workID string) {
	t.Helper()
	db := s.DatabaseForTesting()
	if _, err := db.Exec(`INSERT INTO write_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := db.Exec(`DELETE FROM write_guard`); err != nil {
			t.Errorf("remove write guard: %v", err)
		}
	}()
	if _, err := db.Exec(`INSERT OR IGNORE INTO projects(id,display_name,version,created_at,updated_at) VALUES('authority-work-project','authority-work-project',1,'now','now')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT OR IGNORE INTO product_projects(product_id,project_id,role) VALUES('authority-product','authority-work-project','primary')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT OR IGNORE INTO work_projects(work_id,project_id,role) VALUES(?, 'authority-work-project', 'primary')`, workID); err != nil {
		t.Fatal(err)
	}
}

// Continuity must resolve law whose legislating work's approved contract
// predates stored step plans.
func TestContinuityResolvesPlanlessHistoricalLegislator(t *testing.T) {
	t.Parallel()
	s, _ := authorityLawScenario(t, authorityLawFixture{ID: "CD-8751", Kind: "decision", LegislatedBy: authoritySyntheticWork, Version: 6})
	defer s.Close()
	seedAuthorityWork(t, s, authoritySyntheticWork)
	seedPlanlessLegislatorFixture(t, s, authoritySyntheticWork, 6, `["CD-8751"]`)
	if _, err := ReadWorkflowContinuity(context.Background(), s, ContinuityRequest{Work: authoritySyntheticWork}); err != nil {
		t.Fatalf("continuity must resolve law whose legislator predates stored plans: %v", err)
	}
}

// Control: a research plan at the same public seam still refuses. A research
// approving contract is genuinely not Product-changing, plan or no migration.
func TestContinuityRefusesResearchPlanLegislator(t *testing.T) {
	t.Parallel()
	s, _ := authorityLawScenario(t, authorityLawFixture{ID: "CD-8751", Kind: "decision", LegislatedBy: authoritySyntheticWork, Version: 6})
	defer s.Close()
	seedAuthorityWork(t, s, authoritySyntheticWork)
	seedAuthorityContract(t, s, authoritySyntheticWork, 6, WorkflowStepPlanResearch, []string{"CD-8751"}, nil)
	seedLegislatorPrimaryMembership(t, s, authoritySyntheticWork)
	_, err := ReadWorkflowContinuity(context.Background(), s, ContinuityRequest{Work: authoritySyntheticWork})
	assertLegislatorRefusal(t, err, "the approving contract is not Product-changing")
}

// Control: a historical contract with NULL product-truth and no retained
// architecture binding still refuses. The pre-column fold persisted a binding
// exactly for Product-changing approvals and refused one on every other
// contract, so binding absence is the recorded non-Product-changing fact:
// a new declaration naming an old research contract cannot ride the NULL
// column to standing.
func TestContinuityRefusesPlanlessLegislatorWithoutRetainedBinding(t *testing.T) {
	t.Parallel()
	s, _ := authorityLawScenario(t, authorityLawFixture{ID: "CD-8751", Kind: "decision", LegislatedBy: authoritySyntheticWork, Version: 6})
	defer s.Close()
	seedAuthorityWork(t, s, authoritySyntheticWork)
	seedPlanlessLegislatorFixture(t, s, authoritySyntheticWork, 6, `["CD-8751"]`)
	db := s.DatabaseForTesting()
	if _, err := db.Exec(`INSERT INTO write_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM workflow_architecture_bindings WHERE work_id=?`, authoritySyntheticWork); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM write_guard`); err != nil {
		t.Fatal(err)
	}
	_, err := ReadWorkflowContinuity(context.Background(), s, ContinuityRequest{Work: authoritySyntheticWork})
	assertLegislatorRefusal(t, err, "the approving contract is not Product-changing")
}

// Control: a planless historical contract that names the law in no admitted
// legislative field still refuses, whatever the product-truth classification
// turns out to be. The contract mandates a second, derived fixture law so the
// demand-driven establishment walk runs, while admitting nothing for CD-8751.
func TestContinuityRefusesPlanlessLegislatorWithoutAdmittedReference(t *testing.T) {
	t.Parallel()
	s, _ := authorityLawScenario(t,
		authorityLawFixture{ID: "CD-8751", Kind: "decision", LegislatedBy: authoritySyntheticWork, Version: 6},
		authorityLawFixture{ID: "CD-8752", Kind: "decision", Derived: true},
	)
	defer s.Close()
	seedAuthorityWork(t, s, authoritySyntheticWork)
	seedPlanlessLegislatorFixture(t, s, authoritySyntheticWork, 6, `[]`)
	db := s.DatabaseForTesting()
	if _, err := db.Exec(`INSERT INTO write_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	_, err := db.Exec(`UPDATE workflow_contracts SET spec_mandate='["CD-8752"]' WHERE work_id=? AND contract_version=6`, authoritySyntheticWork)
	if _, guardErr := db.Exec(`DELETE FROM write_guard`); guardErr != nil {
		t.Fatal(guardErr)
	}
	if err != nil {
		t.Fatal(err)
	}
	_, err = ReadWorkflowContinuity(context.Background(), s, ContinuityRequest{Work: authoritySyntheticWork})
	assertLegislatorRefusal(t, err, "names the law in no admitted legislative field")
}

// A native workflow write whose proof preparation walks the same
// establishment must not refuse because of an unrelated planless historical
// legislator in the same Product's law folder. The running work's approved
// contract modifies the legislated fixture law, so record_intake prepares
// the law proof, admits the historical record, and the write proceeds.
func TestWorkflowWriteAdmitsPlanlessHistoricalLegislator(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _ := authorityLawScenario(t, authorityLawFixture{ID: "CD-8751", Kind: "decision", LegislatedBy: authoritySyntheticWork, Version: 6})
	defer s.Close()
	seedAuthorityWork(t, s, authoritySyntheticWork)
	seedPlanlessLegislatorFixture(t, s, authoritySyntheticWork, 6, `["CD-8751"]`)
	writeWork := "work-write-path-neighbor"
	db := s.DatabaseForTesting()
	if _, err := db.Exec(`INSERT INTO write_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO work_items(id,kind,title,lifecycle,priority,version,created_at,updated_at) VALUES(?, 'task', 'Authority fixture write work', 'in_progress', 1, 1, 'now', 'now')`, writeWork); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM write_guard`); err != nil {
		t.Fatal(err)
	}
	seedAuthorityContract(t, s, writeWork, 1, WorkflowStepPlanChange, []string{"CD-8751"}, nil)
	seedLegislatorPrimaryMembership(t, s, writeWork)
	actor := WorkflowActor{PrincipalRef: "principal:authority-fixture", ClientRef: "client:authority-fixture", AgentRef: "agent:authority-fixture", SessionRef: "session:authority-fixture", ActorClass: ActorAgent}
	request := WorkflowActionPreflightRequest{
		WorkID:          writeWork,
		ActionID:        "record_intake",
		ExpectedVersion: 1,
		Payload:         json.RawMessage(`{"outcome":"proceed","open_questions_found":false}`),
		Actor:           actor,
	}
	err := AuthorizeWorkflowActionAtBoundaryTx(ctx, s, request, nil, func(*Transaction) error { return nil })
	if err != nil {
		t.Fatalf("a workflow write must not refuse over a planless historical legislator: %v", err)
	}
}

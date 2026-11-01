package store

import (
	"context"
	"strings"
	"testing"
)

// The reservation rows a contract's own law addition folds. The FK chain
// workflow_contracts → workflow_architecture_bindings →
// workflow_law_addition_reservations → workflow_contract_law_additions is the
// store-side shape of "this contract reserved the id first", so the fixture
// seeds the binding the fold writes before the reservation it owns.
func seedReservedLawAddition(t *testing.T, s *Store, workID string) {
	t.Helper()
	digest := "sha256:" + strings.Repeat("a", 64)
	db := s.DatabaseForTesting()
	if _, err := db.Exec(`INSERT INTO write_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO workflow_architecture_bindings(work_id,contract_version,product_id,home_domain_id,projection_hash) VALUES(?,1,'product','root',?)`, workID, digest); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO workflow_law_addition_reservations(product_id,law_id,owner_work_id,owner_contract_version,home_domain_id) VALUES('product',?,?,1,'root')`, "CD-0189", workID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO workflow_contract_law_additions(work_id,contract_version,product_id,law_id,home_domain_id,reservation_owner_work_id,reservation_owner_contract_version) VALUES(?,1,'product',?,'root',?,1)`, workID, "CD-0189", workID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM write_guard`); err != nil {
		t.Fatal(err)
	}
}

// seedPublishedLawSubject commits the law file the reserved id names into the
// fixture's committed home. The recovery path never reads committed law; the
// published-addition tests commit the file to prove the refusal does not move
// with published-law state.
func seedPublishedLawSubject(t *testing.T, s *Store, lawID string) {
	t.Helper()
	home := KnowledgeHome{HomeProjectID: "project", HomeLocatorID: "workflow-law-locator", RepoPath: workflowLawFixtureRepo(t, s), HeadRef: "HEAD"}
	commitWorkflowFixtureLaw(t, home, lawID, fixtureLawFolderLaw(lawID))
}

func seedMandateContract(t *testing.T, s *Store, workID string) {
	t.Helper()
	actorRef := DeriveWorkflowActorRef("principal/mandate", "client/mandate", "agent/mandate", "session/"+workID)
	db := s.DatabaseForTesting()
	if _, err := db.Exec(`INSERT INTO write_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO workflow_actors(actor_ref,principal_ref,client_ref,agent_ref,session_ref,actor_class,first_seen_at) VALUES(?,?,?,?,?,'agent','now')`, actorRef, "principal/mandate", "client/mandate", "agent/mandate", "session/"+workID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class) VALUES(?,1,'mandate recovery','internal_sqlite','[]','[]','now',?,'["`+"CD-0189"+`"]','[]',1,'prototype_internal')`, workID, actorRef); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM write_guard`); err != nil {
		t.Fatal(err)
	}
}

func mandateRecoveryDefinition() WorkflowDefinition {
	return WorkflowDefinition{StepGraph: WorkflowStepGraph{Steps: []WorkflowStep{
		{ID: "repair", Kind: WorkflowStepInternalSQLite, Actions: []string{"bind_evidence", "confirm_premise"}},
	}}}
}

func wantMandateRefusal(t *testing.T, err error) *Failure {
	t.Helper()
	var failure *Failure
	if err == nil || !failureAs(err, &failure) {
		t.Fatalf("err=%v, want the mandate refusal", err)
	}
	return failure
}

// A mandated law the contract itself adds cannot decide branch authorship in
// the store: another merge may hold the same id, and the store cannot read
// branch contents. At a confirmation step the refusal stays conditional —
// bind_evidence only when the branch adds the id, otherwise supersede_contract.
func TestMandateRecoveryKeepsConditionalGuidanceForAnUnseenCollision(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	workID := "mandate-recovery-unseen"
	seedWork(t, s, workID)
	seedWorkflowLaw(t, s)
	seedMandateContract(t, s, workID)
	seedReservedLawAddition(t, s, workID)
	failure := wantMandateRefusal(t, mandateFixtureAdmission(context.Background(), s.db, workID, mandateRecoveryDefinition(), "repair", "record_verdict"))
	if !strings.Contains(failure.Detail, "one of the contract's own law additions") || strings.Contains(failure.Detail, "publishes") {
		t.Fatalf("detail=%q, want the conditional added-law detail", failure.Detail)
	}
	if !strings.Contains(failure.RecoveryAction, `bind_evidence on step "repair" before record_verdict only when the branch adds "CD-0189"`) {
		t.Fatalf("recovery=%q, want conditional bind_evidence guidance", failure.RecoveryAction)
	}
	if !strings.Contains(failure.RecoveryAction, `otherwise run supersede_contract on step "repair"`) {
		t.Fatalf("recovery=%q, want the checkpoint correction route", failure.RecoveryAction)
	}
}

// A committed law file for the reserved id names no branch authorship either:
// the store cannot read branch contents, so a published id still refuses
// through the same conditional route. The refusal is identical to the
// unseen collision's.
func TestMandateRecoveryTreatsAPublishedAdditionLikeAnUnseenOne(t *testing.T) {
	t.Parallel()
	const lawID = "CD-0189"
	s := openTemp(t)
	workID := "mandate-recovery-published"
	seedWork(t, s, workID)
	seedWorkflowLaw(t, s)
	seedMandateContract(t, s, workID)
	seedReservedLawAddition(t, s, workID)
	seedPublishedLawSubject(t, s, lawID)
	failure := wantMandateRefusal(t, mandateFixtureAdmission(context.Background(), s.db, workID, mandateRecoveryDefinition(), "repair", "record_verdict"))
	if !strings.Contains(failure.Detail, "one of the contract's own law additions") {
		t.Fatalf("detail=%q, want the conditional added-law detail", failure.Detail)
	}
	if strings.Contains(failure.RecoveryAction, "projection") || strings.Contains(failure.Detail, "projection") {
		t.Fatalf("detail=%q recovery=%q, want no projection-based claim", failure.Detail, failure.RecoveryAction)
	}
	if !strings.Contains(failure.RecoveryAction, `otherwise run supersede_contract on step "repair"`) {
		t.Fatalf("recovery=%q, want the checkpoint correction route", failure.RecoveryAction)
	}
}

// A mandated law the contract does not add stays in the plain bind_evidence
// shape even when the Git projection publishes the id: the contract never
// claimed the id, so there is no reservation collision to correct.
func TestMandateRecoveryStaysWithPlainBindingOutsideTheContractAdditions(t *testing.T) {
	t.Parallel()
	const lawID = "CD-0189"
	s := openTemp(t)
	workID := "mandate-recovery-no-addition"
	seedWork(t, s, workID)
	seedWorkflowLaw(t, s)
	seedMandateContract(t, s, workID)
	seedPublishedLawSubject(t, s, lawID)
	failure := wantMandateRefusal(t, mandateFixtureAdmission(context.Background(), s.db, workID, mandateRecoveryDefinition(), "repair", "record_verdict"))
	if failure.Detail != `spec mandate law "CD-0189" is not bound` {
		t.Fatalf("detail=%q, want the plain unbound detail", failure.Detail)
	}
	if failure.RecoveryAction != `run bind_evidence on step "repair" before record_verdict` {
		t.Fatalf("recovery=%q, want the plain bind_evidence route", failure.RecoveryAction)
	}
}

// The complete gate keeps its invariant kind while carrying the conditional
// guidance and the correction route the checkpoint step admits.
func TestMandateRecoveryCompleteGateKeepsTheInvariantKind(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	workID := "mandate-recovery-complete"
	seedWork(t, s, workID)
	seedWorkflowLaw(t, s)
	seedMandateContract(t, s, workID)
	seedReservedLawAddition(t, s, workID)
	failure := wantMandateRefusal(t, mandateFixtureAdmission(context.Background(), s.db, workID, mandateRecoveryDefinition(), "repair", "complete"))
	if failure.Kind != KindInvariantViolation {
		t.Fatalf("kind=%q, want %q for a complete gate", failure.Kind, KindInvariantViolation)
	}
	if !strings.Contains(failure.RecoveryAction, "only when the branch adds") || !strings.Contains(failure.RecoveryAction, `supersede_contract on step "repair"`) {
		t.Fatalf("recovery=%q, want conditional guidance with the checkpoint correction route", failure.RecoveryAction)
	}
}

// A helper query failure propagates instead of degrading into the
// bind_evidence advice: an unavailable projection is not a binding
// instruction.
func TestMandateRecoveryPropagatesQueryFailures(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	workID := "mandate-recovery-query"
	seedWork(t, s, workID)
	seedWorkflowLaw(t, s)
	seedMandateContract(t, s, workID)
	seedReservedLawAddition(t, s, workID)
	if _, err := s.DatabaseForTesting().Exec(`DROP TABLE workflow_contract_law_additions`); err != nil {
		t.Fatal(err)
	}
	failure := wantMandateRefusal(t, mandateFixtureAdmission(context.Background(), s.db, workID, mandateRecoveryDefinition(), "repair", "record_verdict"))
	if failure.Kind != KindUnavailable {
		t.Fatalf("kind=%q detail=%q, want the propagated unavailable failure", failure.Kind, failure.Detail)
	}
}

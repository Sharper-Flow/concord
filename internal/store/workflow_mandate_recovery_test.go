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
	if _, err := db.Exec(`INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO workflow_architecture_bindings(work_id,contract_version,product_id,domain_registry_content_hash,home_domain_id,projection_hash) VALUES(?,1,'product',?,'root',?)`, workID, digest, digest); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO workflow_law_addition_reservations(product_id,law_id,owner_work_id,owner_contract_version,home_domain_id) VALUES('product',?,?,1,'root')`, "CD-0189", workID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO workflow_contract_law_additions(work_id,contract_version,product_id,law_id,home_domain_id,reservation_owner_work_id,reservation_owner_contract_version) VALUES(?,1,'product',?,'root',?,1)`, workID, "CD-0189", workID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
}

// A law-subject row in the Git-derived projection. The recovery path never
// reads it; the published-addition tests seed it to prove the refusal does
// not move with projection state.
func seedPublishedLawSubject(t *testing.T, s *Store, lawID string) {
	t.Helper()
	db := s.DatabaseForTesting()
	if _, err := db.Exec(`INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO law_subjects(home_project_id,home_locator_id,law_id,kind,status,path,title,content_hash,scanned_commit_oid) VALUES('project','workflow-law-locator',?,'decision','accepted','docs/decisions/`+lawID+`-other.md','Another work''s decision','sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb','test')`, lawID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
}

func seedMandateContract(t *testing.T, s *Store, workID string) {
	t.Helper()
	actorRef := DeriveWorkflowActorRef("principal/mandate", "client/mandate", "agent/mandate", "session/"+workID)
	db := s.DatabaseForTesting()
	if _, err := db.Exec(`INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO workflow_actors(actor_ref,principal_ref,client_ref,agent_ref,session_ref,actor_class,first_seen_at) VALUES(?,?,?,?,?,'agent','now')`, actorRef, "principal/mandate", "client/mandate", "agent/mandate", "session/"+workID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class) VALUES(?,1,'mandate recovery','internal_sqlite','[]','[]','now',?,'["`+"CD-0189"+`"]','[]',1,'prototype_internal')`, workID, actorRef); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
}

func mandateRecoveryDefinition() WorkflowDefinition {
	return WorkflowDefinition{StepGraph: WorkflowStepGraph{Steps: []WorkflowStep{
		{ID: "repair", Kind: WorkflowStepHumanCheckpoint, Actions: []string{"bind_evidence", "confirm_premise"}},
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
// branch contents. At a human checkpoint the refusal stays conditional —
// bind_evidence only when the branch adds the id, otherwise supersede_contract.
func TestMandateRecoveryKeepsConditionalGuidanceForAnUnseenCollision(t *testing.T) {
	t.Parallel()
	const lawID = "CD-0189"
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

// A Git law projection row for the reserved id names no branch authorship
// either: the projection can be stale or can carry another work's law, and
// the store cannot read branch contents. The refusal is identical to the
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

// A completed worker result at the dispatch step leaves rejection as the
// first admitted correction route: the refusal names the conditional
// bind_evidence guidance and reject_worker_result, then the supersession it
// reopens.
func TestMandateRecoveryNamesRejectionFirstAtAWorkerDispatchStep(t *testing.T) {
	t.Parallel()
	const lawID = "CD-0189"
	workID := "mandate-recovery-reject"
	s, _, _, _ := seedCompletedWorkerAtExecution(t, workID)
	seedMandateContract(t, s, workID)
	seedReservedLawAddition(t, s, workID)
	entry := workflowFixtureDefinition(t, 2)
	failure := wantMandateRefusal(t, mandateFixtureAdmission(context.Background(), s.db, workID, entry.Definition, "execution", "accept_worker_result"))
	if !strings.Contains(failure.RecoveryAction, "only when the branch adds") {
		t.Fatalf("recovery=%q, want the conditional bind_evidence guidance", failure.RecoveryAction)
	}
	if !strings.Contains(failure.RecoveryAction, "reject_worker_result, then supersede_contract, before accept_worker_result") {
		t.Fatalf("recovery=%q, want reject_worker_result then supersede_contract", failure.RecoveryAction)
	}
	if strings.Contains(failure.RecoveryAction, "record_worker_failure") {
		t.Fatalf("recovery=%q, want no record_worker_failure route", failure.RecoveryAction)
	}
}

// A failed dispatched attempt leaves the failure record as the first admitted
// route at a step whose definition predates record_worker_failure's pinned
// actions: the refusal names record_worker_failure, then the supersession.
func TestMandateRecoveryNamesFailureRecordFirstWhenRejectionIsUnavailable(t *testing.T) {
	t.Parallel()
	const lawID = "CD-0189"
	workID := "mandate-recovery-failure"
	s, _, attemptID, entry := seedOldDefinitionWorker(t, workID)
	failAbandonedWorkerAttempt(t, s, workID, attemptID)
	seedMandateContract(t, s, workID)
	seedReservedLawAddition(t, s, workID)
	definition := entry.Definition
	bindingStep := workflowEvidenceBindingStep(definition, "repair")
	if bindingStep == "" {
		t.Fatal("the old fixture carries no bind_evidence step")
	}
	failure := wantMandateRefusal(t, mandateFixtureAdmission(context.Background(), s.db, workID, definition, bindingStep, "record_verdict"))
	if !strings.Contains(failure.RecoveryAction, "only when the branch adds") || !strings.Contains(failure.RecoveryAction, "record_worker_failure, then supersede_contract") {
		t.Fatalf("recovery=%q, want conditional guidance then record_worker_failure and supersede_contract", failure.RecoveryAction)
	}
}

func TestMandateRecoveryNamesDeclaredFailureRecordForAFailedAttempt(t *testing.T) {
	t.Parallel()
	const lawID = "CD-0189"
	workID := "mandate-recovery-declared-failure"
	s, _, attemptID, entry := seedOldDefinitionWorker(t, workID)
	failAbandonedWorkerAttempt(t, s, workID, attemptID)
	seedMandateContract(t, s, workID)
	seedReservedLawAddition(t, s, workID)
	definition := entry.Definition
	for index := range definition.StepGraph.Steps {
		if definition.StepGraph.Steps[index].ID == "repair" {
			definition.StepGraph.Steps[index].Actions = append(definition.StepGraph.Steps[index].Actions, "record_worker_failure")
		}
	}
	definition.AvailableActions = append(definition.AvailableActions, "record_worker_failure")
	bindingStep := workflowEvidenceBindingStep(definition, "repair")
	failure := wantMandateRefusal(t, mandateFixtureAdmission(context.Background(), s.db, workID, definition, bindingStep, "record_verdict"))
	if !strings.Contains(failure.RecoveryAction, "record_worker_failure, then supersede_contract") {
		t.Fatalf("recovery=%q, want declared record_worker_failure then supersede_contract", failure.RecoveryAction)
	}
}

// A live dispatched worker admits no correction route: no completed result to
// reject and no failure to record. The refusal then names the conditional
// bind_evidence guidance alone and no correction route the step does not
// admit.
func TestMandateRecoveryNamesNoCorrectionRouteWhileAWorkerIsLive(t *testing.T) {
	t.Parallel()
	const lawID = "CD-0189"
	workID := "mandate-recovery-live"
	s, _ := seedDispatchedWorkerAtExecution(t, workID)
	seedMandateContract(t, s, workID)
	seedReservedLawAddition(t, s, workID)
	entry := workflowFixtureDefinition(t, 2)
	failure := wantMandateRefusal(t, mandateFixtureAdmission(context.Background(), s.db, workID, entry.Definition, "execution", "accept_worker_result"))
	if !strings.Contains(failure.RecoveryAction, `only when the branch adds "CD-0189"`) {
		t.Fatalf("recovery=%q, want conditional bind_evidence guidance", failure.RecoveryAction)
	}
	for _, route := range []string{"supersede_contract", "reject_worker_result", "record_worker_failure"} {
		if strings.Contains(failure.RecoveryAction, route) {
			t.Fatalf("recovery=%q, must not name %s at a step that admits no correction route", failure.RecoveryAction, route)
		}
	}
}

// The complete gate keeps its invariant kind while carrying the conditional
// guidance and the correction route the checkpoint step admits.
func TestMandateRecoveryCompleteGateKeepsTheInvariantKind(t *testing.T) {
	t.Parallel()
	const lawID = "CD-0189"
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
	const lawID = "CD-0189"
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

// The checkout-owner read the cd-reservations verb serves: a directory
// inside a claimed worktree names the owning work, including a nested
// directory; a directory outside every claimed worktree names none. The
// reservation listing itself is exercised end-to-end by the verb's test.
func TestWorktreeOwnerWorkIDRead(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	db := s.DatabaseForTesting()
	digest := strings.Repeat("a", 40)
	if _, err := db.Exec(`INSERT INTO worktree_claims(op_id,work_id,project_id,set_id,pinned_branch,pinned_base_sha,pinned_path,state,principal_ref,request_id,observed_at,updated_at) VALUES
			('op-a','work-a','project','set-a','work/work-a',?,?,'verified','principal','request','now','now')`, digest, "/data/worktrees/project/work-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO worktree_entries(set_id,project_id,claim_op_id,branch,base_sha,path,repository_id,state,verified_at) VALUES
			('set-a','project','op-a','work/work-a',?,?,'repo','active','now')`, digest, "/data/worktrees/project/work-a"); err != nil {
		t.Fatal(err)
	}
	owner, err := s.WorktreeOwnerWorkID(context.Background(), "project", "/data/worktrees/project/work-a/nested/dir")
	if err != nil || owner != "work-a" {
		t.Fatalf("owner=%q err=%v, want work-a under the claimed path", owner, err)
	}
	owner, err = s.WorktreeOwnerWorkID(context.Background(), "project", "/data/elsewhere")
	if err != nil || owner != "" {
		t.Fatalf("owner=%q err=%v, want no owner outside the claimed worktrees", owner, err)
	}
}

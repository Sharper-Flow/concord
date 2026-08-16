package store

import (
	"context"
	"strings"
	"testing"
)

// CD-0031: out-of-band operator direction binds through contract supersession.
// The structural tooth: premise confirmation binds to the current contract
// version, so superseding after a redirect forces the operator to confirm the
// NEW premise — and failing to supersede leaves the original premise named at
// confirmation, where the operator confronts it.

func TestSupersessionInvalidatesPriorPremiseConfirmation(t *testing.T) {
	s := openTemp(t)
	seedWork(t, s, "cd31-work")
	seedWorkflowLaw(t, s)
	executor := DeriveWorkflowActorRef("principal/operator", "client/concord-1", "agent/executor", "session/cd31")
	operator := DeriveWorkflowActorRef("principal/operator", "client/concord-1", "agent/reviewer", "session/cd31-r")
	digest := legacyImplementationDigest(t)
	events := []Event{
		workflowEvent("cd31-executor", WorkflowActorRecorded, "cd31-work", map[string]any{"work_id": "cd31-work", "expected_version": 2, "resulting_version": 3, "actor_ref": executor, "principal_ref": "principal/operator", "client_ref": "client/concord-1", "agent_ref": "agent/executor", "session_ref": "session/cd31", "actor_class": "agent"}),
		workflowEvent("cd31-operator", WorkflowActorRecorded, "cd31-work", map[string]any{"work_id": "cd31-work", "expected_version": 3, "resulting_version": 4, "actor_ref": operator, "principal_ref": "principal/operator", "client_ref": "client/concord-1", "agent_ref": "agent/reviewer", "session_ref": "session/cd31-r", "actor_class": "operator"}),
		workflowEvent("cd31-definition", WorkflowDefinitionSelected, "cd31-work", map[string]any{"work_id": "cd31-work", "expected_version": 4, "resulting_version": 5, "ref": "workflow.implementation", "version": 1, "digest": digest, "work_kind": "implementation"}),
		workflowEventWithActor("cd31-contract-v1", WorkflowContractApproved, "cd31-work", executor, map[string]any{"work_id": "cd31-work", "expected_version": 5, "resulting_version": 6, "contract_version": 1, "premise": "align one provider service", "outcome_kind": "check", "outcome_payload": map[string]any{"kind": "check", "check_ref": "check:cd31", "immutable_subject_ref": "commit:cd31", "expected_result": "pass"}, "required_evidence": []string{"verification"}, "route_conventions": []string{}, "spec_mandate": []string{}, "rigor_class": "prototype/internal", "consequence_class": "internal_sqlite"}),
		workflowEventWithActor("cd31-start", WorkflowActionStarted, "cd31-work", executor, map[string]any{"work_id": "cd31-work", "expected_version": 6, "resulting_version": 7, "step_id": "execution", "action_id": "start_execution", "attempt_epoch": 1, "accepted_inputs_digest": "sha256:" + strings.Repeat("b", 64), "idempotency_identity": "cd31-operation", "actor_ref": executor}),
		workflowEvent("cd31-impact", WorkflowImpactDeclared, "cd31-work", map[string]any{"work_id": "cd31-work", "expected_version": 7, "resulting_version": 8, "edge_id": "edge:cd31", "edge_kind": "modifies", "edge_class": "none", "target_work_id": "cd31-work", "target_kind": "work_item", "severity": "breaking"}),
	}
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: events, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "cd31-work"): 2}}); err != nil {
		t.Fatal(err)
	}
	seedWorkflowAuthority(t, s, "cd31-verification", "cd31-work", "principal/verify", "request/verify", []string{"evidence:verification", "evidence:verification-v2"})
	seedWorkflowAuthority(t, s, "cd31-review", "cd31-work", "principal/review", "request/review", []string{"evidence:review"})
	evidence := []Event{
		workflowEvent("cd31-evidence-verification", WorkflowEvidenceBound, "cd31-work", map[string]any{"work_id": "cd31-work", "expected_version": 8, "resulting_version": 9, "evidence_kind": "verification", "immutable_subject_ref": "evidence:verification", "producer_id": "principal/verify", "producer_run_ref": "cd31-verification", "producer_watermark": "request/verify", "observed_at": "2026-08-09T00:00:00Z"}),
		workflowEventWithActor("cd31-verdict", WorkflowVerdictRecorded, "cd31-work", operator, map[string]any{"work_id": "cd31-work", "expected_version": 9, "resulting_version": 10, "contract_version": 1, "predicate_id": "predicate:cd31", "verdict_kind": "ok", "verdict_actor_ref": operator, "evaluation_evidence": []string{"evidence:verification"}, "incomparable_with_approved": false}),
		workflowEventWithActor("cd31-confirm-v1", WorkflowPremiseConfirmed, "cd31-work", operator, map[string]any{"work_id": "cd31-work", "expected_version": 10, "resulting_version": 11, "contract_version": 1, "confirming_actor_ref": operator}),
	}
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: evidence, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "cd31-work"): 8}}); err != nil {
		t.Fatal(err)
	}

	// Baseline: with v1 confirmed, completion passes clause 5 (it may fail
	// elsewhere, but not on the premise). Instead of completing, assert the
	// direct precondition: a confirmation row exists for the current version.
	var confirmed int
	if err := s.DB().QueryRow(`SELECT count(*) FROM workflow_premise_confirmations WHERE work_id='cd31-work' AND contract_version=1`).Scan(&confirmed); err != nil || confirmed != 1 {
		t.Fatalf("v1 confirmation missing: %d err=%v", confirmed, err)
	}

	// Redirect: the operator widens scope mid-run. The contract is superseded.
	supersession := []Event{
		workflowEventWithActor("cd31-contract-v2", WorkflowContractApproved, "cd31-work", executor, map[string]any{"work_id": "cd31-work", "expected_version": 11, "resulting_version": 12, "contract_version": 2, "premise": "align the five sibling provider services", "outcome_kind": "check", "outcome_payload": map[string]any{"kind": "check", "check_ref": "check:cd31", "immutable_subject_ref": "commit:cd31", "expected_result": "pass"}, "required_evidence": []string{"verification"}, "route_conventions": []string{}, "spec_mandate": []string{}, "rigor_class": "prototype/internal", "consequence_class": "internal_sqlite"}),
		workflowEvent("cd31-supersede-v1", WorkflowContractSuperseded, "cd31-work", map[string]any{"work_id": "cd31-work", "expected_version": 12, "resulting_version": 13, "previous_contract_version": 1, "new_contract_version": 2, "supersede_reason": "operator redirect widens scope to five siblings", "audit_evidence": []string{"operator-direction"}}),
	}
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: supersession, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "cd31-work"): 11}}); err != nil {
		t.Fatal(err)
	}

	// Re-bind evidence and verdict under v2 so the completion attempt fails
	// on nothing except the premise.
	rebind := []Event{
		workflowEvent("cd31-evidence-v2", WorkflowEvidenceBound, "cd31-work", map[string]any{"work_id": "cd31-work", "expected_version": 13, "resulting_version": 14, "evidence_kind": "verification", "immutable_subject_ref": "evidence:verification-v2", "producer_id": "principal/verify", "producer_run_ref": "cd31-verification", "producer_watermark": "request/verify", "observed_at": "2026-08-09T00:00:00Z"}),
		workflowEvent("cd31-evidence-review", WorkflowEvidenceBound, "cd31-work", map[string]any{"work_id": "cd31-work", "expected_version": 14, "resulting_version": 15, "evidence_kind": "review", "immutable_subject_ref": "evidence:review", "producer_id": "principal/review", "producer_run_ref": "cd31-review", "producer_watermark": "request/review", "observed_at": "2026-08-09T00:00:00Z"}),
		workflowEventWithActor("cd31-verdict-v2", WorkflowVerdictRecorded, "cd31-work", operator, map[string]any{"work_id": "cd31-work", "expected_version": 15, "resulting_version": 16, "contract_version": 2, "predicate_id": "predicate:cd31", "verdict_kind": "ok", "verdict_actor_ref": operator, "evaluation_evidence": []string{"evidence:verification-v2", "evidence:review"}, "incomparable_with_approved": false}),
	}
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: rebind, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "cd31-work"): 13}}); err != nil {
		t.Fatal(err)
	}

	// The structural tooth (CD-0031): clause 5 binds to the CURRENT contract
	// version. The v1 confirmation no longer counts — the operator must
	// confirm the redirected premise, or completion refuses.
	var current int
	if err := s.DB().QueryRow(`SELECT count(*) FROM workflow_premise_confirmations c WHERE c.work_id='cd31-work' AND c.contract_version=(SELECT COALESCE(MAX(contract_version),0) FROM workflow_contracts WHERE work_id='cd31-work' AND superseded_by IS NULL)`).Scan(&current); err != nil || current != 0 {
		t.Fatalf("superseded contract still premise-satisfied: %d err=%v", current, err)
	}

	// The refusal at the gate: a completion attempt for the redirected work
	// cannot pass clause 5 on the strength of the v1 confirmation.
	var refusals int
	_ = refusals
	err := func() error {
		completion := workflowEventWithActor("cd31-terminal", WorkflowCompleted, "cd31-work", executor, map[string]any{"work_id": "cd31-work", "expected_version": 16, "resulting_version": 17, "terminal_state": "completed", "final_verdict_kind": "ok", "verdict_actor_ref": operator, "premise_confirmed": true, "evidence_count": 1, "changed_refs_digest": "sha256:" + strings.Repeat("a", 64), "impact_verdict": "non-breaking"})
		completion.PayloadVersion = 2
		return CompleteWorkflow(context.Background(), s, completion)
	}()
	if err == nil {
		t.Fatal("completion must refuse after supersession without a fresh premise confirmation")
	}
	if !strings.Contains(err.Error(), "premise") {
		t.Fatalf("refusal should be the premise clause, got: %v", err)
	}
}

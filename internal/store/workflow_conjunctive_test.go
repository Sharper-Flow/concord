package store

import (
	"context"
	"strings"
	"testing"
)

// TestPinnedConjunctiveContractCompletes is the pinned reproduction for the
// single-predicate storage defect. The approval payload names both an exists
// and an absent predicate, then drives the workflow through completion.
func TestPinnedConjunctiveContractCompletes(t *testing.T) {
	s := openTemp(t)
	seedWork(t, s, "conjunctive-work")
	seedWorkflowLaw(t, s)

	executor := DeriveWorkflowActorRef("principal/operator", "client/concord-1", "agent/executor", "session/conjunctive")
	operator := DeriveWorkflowActorRef("principal/operator", "client/concord-1", "agent/reviewer", "session/conjunctive")
	version := int64(2)
	events := []Event{
		workflowEvent("conjunctive-executor", WorkflowActorRecorded, "conjunctive-work", map[string]any{
			"work_id": "conjunctive-work", "expected_version": version, "resulting_version": version + 1,
			"actor_ref": executor, "principal_ref": "principal/operator", "client_ref": "client/concord-1", "agent_ref": "agent/executor", "session_ref": "session/conjunctive", "actor_class": "agent",
		}),
		workflowEvent("conjunctive-operator", WorkflowActorRecorded, "conjunctive-work", map[string]any{
			"work_id": "conjunctive-work", "expected_version": version + 1, "resulting_version": version + 2,
			"actor_ref": operator, "principal_ref": "principal/operator", "client_ref": "client/concord-1", "agent_ref": "agent/reviewer", "session_ref": "session/conjunctive", "actor_class": "operator",
		}),
		workflowEvent("conjunctive-definition", WorkflowDefinitionSelected, "conjunctive-work", map[string]any{
			"work_id": "conjunctive-work", "expected_version": version + 2, "resulting_version": version + 3,
			"ref": workflowFixtureRef, "version": 1, "digest": workflowFixtureDigest(t), "work_kind": workflowFixtureWorkKind,
		}),
		workflowEventWithActor("conjunctive-contract", WorkflowContractApproved, "conjunctive-work", executor, map[string]any{
			"work_id": "conjunctive-work", "expected_version": version + 3, "resulting_version": version + 4, "contract_version": 1,
			"premise": "deliver both required end states", "outcome_kind": "check", "outcome_payload": map[string]any{"kind": "check", "check_ref": "check:conjunctive", "immutable_subject_ref": "commit:conjunctive", "expected_result": "pass"},
			"outcome_predicates": []map[string]any{
				{"predicate_id": "predicate:present", "ordinal": 0, "outcome_kind": "exists", "outcome_payload": map[string]any{"kind": "exists", "surface": "surface:one", "subjects": []string{"subject:one"}}},
				{"predicate_id": "predicate:absent", "ordinal": 1, "outcome_kind": "absent", "outcome_payload": map[string]any{"kind": "absent", "surface": "surface:two", "subjects": []string{"subject:two"}, "distinguish_from": []string{"archived"}}},
			},
			"required_evidence": []string{"verification", "review"}, "route_conventions": []string{}, "spec_mandate": []string{}, "rigor_class": "prototype_internal", "consequence_class": "internal_sqlite",
		}),
		workflowEventWithActor("conjunctive-start", WorkflowActionStarted, "conjunctive-work", executor, map[string]any{
			"work_id": "conjunctive-work", "expected_version": version + 4, "resulting_version": version + 5, "step_id": "execution", "action_id": "start_execution", "attempt_epoch": 1,
			"accepted_inputs_digest": "sha256:" + strings.Repeat("b", 64), "idempotency_identity": "conjunctive-operation", "actor_ref": executor,
		}),
	}
	seedWorkflowAuthority(t, s, "conjunctive-verification", "conjunctive-work", "principal/verify", "request/verification", []string{"evidence:verification"})
	seedWorkflowAuthority(t, s, "conjunctive-review", "conjunctive-work", "principal/review", "request/review", []string{"evidence:review"})
	events = append(events,
		workflowEvent("conjunctive-verification", WorkflowEvidenceBound, "conjunctive-work", map[string]any{
			"work_id": "conjunctive-work", "expected_version": version + 5, "resulting_version": version + 6, "evidence_kind": "verification", "immutable_subject_ref": "evidence:verification", "producer_id": "principal/verify", "producer_run_ref": "conjunctive-verification", "producer_watermark": "request/verification", "observed_at": "2026-08-09T00:00:00Z",
		}),
		workflowEvent("conjunctive-review", WorkflowEvidenceBound, "conjunctive-work", map[string]any{
			"work_id": "conjunctive-work", "expected_version": version + 6, "resulting_version": version + 7, "evidence_kind": "review", "immutable_subject_ref": "evidence:review", "producer_id": "principal/review", "producer_run_ref": "conjunctive-review", "producer_watermark": "request/review", "observed_at": "2026-08-09T00:00:00Z",
		}),
		workflowEventWithActor("conjunctive-present-verdict", WorkflowVerdictRecorded, "conjunctive-work", operator, map[string]any{
			"work_id": "conjunctive-work", "expected_version": version + 7, "resulting_version": version + 8, "contract_version": 1, "predicate_id": "predicate:present", "verdict_kind": "ok", "verdict_actor_ref": operator, "evaluation_evidence": []string{"evidence:verification"}, "incomparable_with_approved": false,
		}),
		workflowEventWithActor("conjunctive-absent-verdict", WorkflowVerdictRecorded, "conjunctive-work", operator, map[string]any{
			"work_id": "conjunctive-work", "expected_version": version + 8, "resulting_version": version + 9, "contract_version": 1, "predicate_id": "predicate:absent", "verdict_kind": "ok", "verdict_actor_ref": operator, "evaluation_evidence": []string{"evidence:review"}, "incomparable_with_approved": false,
		}),
		workflowEventWithActor("conjunctive-premise", WorkflowPremiseConfirmed, "conjunctive-work", operator, map[string]any{
			"work_id": "conjunctive-work", "expected_version": version + 9, "resulting_version": version + 10, "contract_version": 1, "confirming_actor_ref": operator,
		}),
	)
	events[7].PayloadVersion = 2
	events[8].PayloadVersion = 2
	events[3].PayloadVersion = 3
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: events, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "conjunctive-work"): 2}}); err != nil {
		t.Fatal(err)
	}
	finalVersion := readWorkVersion(t, s, "conjunctive-work")
	completion := workflowEventWithActor("conjunctive-completion", WorkflowCompleted, "conjunctive-work", operator, map[string]any{
		"work_id": "conjunctive-work", "expected_version": finalVersion, "resulting_version": finalVersion + 1, "terminal_state": "completed", "final_verdict_kind": "ok", "verdict_actor_ref": operator,
		"premise_confirmed": true, "evidence_count": 2, "changed_refs_digest": "sha256:" + strings.Repeat("a", 64), "impact_verdict": "non-breaking",
	})
	completion.PayloadVersion = 2
	if err := CompleteWorkflow(context.Background(), s, completion); err != nil {
		t.Fatalf("conjunctive completion: %v", err)
	}

	var predicateCount int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM workflow_contract_predicates WHERE work_id='conjunctive-work' AND contract_version=1`).Scan(&predicateCount); err != nil {
		t.Fatalf("pinned reproduction: read conjunctive predicates: %v", err)
	}
	if predicateCount != 2 {
		t.Fatalf("conjunctive predicate count = %d, want 2", predicateCount)
	}
}

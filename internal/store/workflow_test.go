package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestWorkflowActorReferenceUsesDocumentedCanonicalNULEncoding(t *testing.T) {
	parts := []string{"principal/operator", "client/concord-1", "agent/reviewer", "session/42"}
	canonical := "actor-v1\x00" + fmt.Sprintf("principal_ref=%d:%s|", len([]byte(parts[0])), parts[0]) + fmt.Sprintf("client_ref=%d:%s|", len([]byte(parts[1])), parts[1]) + fmt.Sprintf("agent_ref=%d:%s|", len([]byte(parts[2])), parts[2]) + fmt.Sprintf("session_ref=%d:%s|", len([]byte(parts[3])), parts[3])
	digest := sha256.Sum256([]byte(canonical))
	want := "actor:" + hex.EncodeToString(digest[:])
	if got := DeriveWorkflowActorRef(parts[0], parts[1], parts[2], parts[3]); got != want {
		t.Fatalf("actor_ref = %q, want %q", got, want)
	}
	if !strings.Contains(canonical, "actor-v1\x00") || strings.Contains(canonical, `actor-v1\\0`) {
		t.Fatal("canonical actor encoding did not retain exactly one NUL separator")
	}
}

func TestWorkflowEventsFoldAndRebuildByteIdentically(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	seedWork(t, s, "workflow-work")
	seedWorkflowAuthority(t, s, "condition-authority", "workflow-work", "principal/resolver", "request:condition", []string{"approval:one"})
	actor := DeriveWorkflowActorRef("principal/operator", "client/concord-1", "agent/runner", "session/1")
	implementationDigest, err := WorkflowDefinitionDigest(BuiltinWorkflowDefinitions()[0])
	if err != nil {
		t.Fatal(err)
	}
	base := int64(2)
	events := []Event{
		workflowEvent("actor", WorkflowActorRecorded, "workflow-work", map[string]any{
			"work_id": "workflow-work", "expected_version": base, "resulting_version": base + 1,
			"actor_ref": actor, "principal_ref": "principal/operator", "client_ref": "client/concord-1", "agent_ref": "agent/runner", "session_ref": "session/1", "actor_class": "agent",
		}),
		workflowEvent("definition", WorkflowDefinitionSelected, "workflow-work", map[string]any{
			"work_id": "workflow-work", "expected_version": 3, "resulting_version": 4,
			"ref": "workflow.implementation", "version": 1, "digest": implementationDigest, "work_kind": "implementation",
		}),
		workflowEventWithActor("contract", WorkflowContractApproved, "workflow-work", actor, map[string]any{
			"work_id": "workflow-work", "expected_version": 4, "resulting_version": 5,
			"contract_version": 1, "premise": "delivery is required", "outcome_kind": "check", "outcome_payload": map[string]any{"kind": "check", "check_ref": "check:workflow", "immutable_subject_ref": "commit:abc", "expected_result": "pass"}, "required_evidence": []string{"verification"}, "route_conventions": []string{"test"}, "spec_mandate": []string{}, "rigor_class": "prototype/internal", "consequence_class": "internal_sqlite",
		}),
		workflowEventWithActor("start", WorkflowActionStarted, "workflow-work", actor, map[string]any{
			"work_id": "workflow-work", "expected_version": 5, "resulting_version": 6, "step_id": "execution", "action_id": "start_execution", "attempt_epoch": 1, "accepted_inputs_digest": "sha256:" + strings.Repeat("b", 64), "idempotency_identity": "operation-1",
			"actor_ref": actor,
		}),
		workflowEventWithActor("checkpoint", WorkflowActionCheckpointed, "workflow-work", actor, map[string]any{
			"work_id": "workflow-work", "expected_version": 6, "resulting_version": 7, "step_id": "execution", "step_kind": "human_checkpoint", "attempt_epoch": 1, "checkpoint_payload": map[string]any{"action_id": "checkpoint_execution", "cursor": "one"}, "resume_cursor": "cursor:one", "actor_ref": actor, "request_id": "request:one", "checkpoint_id": "checkpoint:one", "accepted_inputs_digest": "sha256:" + strings.Repeat("c", 64), "idempotency_identity": "operation-checkpoint", "result_evidence_refs": []string{"evidence:one"},
		}),
		workflowEventWithActor("condition", WorkflowConditionAdded, "workflow-work", "authority:one", map[string]any{
			"work_id": "workflow-work", "expected_version": 7, "resulting_version": 8, "condition_id": "condition:one", "await_type": "ci_result", "await_ref": "run:one", "resolution_authority": "durable_operation:condition-authority",
		}),
		workflowEvent("operator", WorkflowActorRecorded, "workflow-work", map[string]any{
			"work_id": "workflow-work", "expected_version": 8, "resulting_version": 9, "actor_ref": DeriveWorkflowActorRef("principal/operator", "client/concord-1", "agent/operator", "session/operator"), "principal_ref": "principal/operator", "client_ref": "client/concord-1", "agent_ref": "agent/operator", "session_ref": "session/operator", "actor_class": "operator",
		}),
		workflowEventWithActor("cancel-condition", WorkflowConditionCancelled, "workflow-work", DeriveWorkflowActorRef("principal/operator", "client/concord-1", "agent/operator", "session/operator"), map[string]any{
			"work_id": "workflow-work", "expected_version": 9, "resulting_version": 10, "condition_id": "condition:one", "cancellation_authority": "operator", "cancellation_evidence": []string{"approval:one"}, "cancelled_by_event": "event:cancelled",
		}),
		workflowEvent("impact", WorkflowImpactDeclared, "workflow-work", map[string]any{
			"work_id": "workflow-work", "expected_version": 10, "resulting_version": 11, "edge_id": "edge:one", "edge_kind": "modifies", "edge_class": "none", "target_work_id": "workflow-work", "target_kind": "work_item", "severity": "informational",
		}),
		workflowEvent("notice", WorkflowImpactNoticeRecorded, "workflow-work", map[string]any{
			"work_id": "workflow-work", "expected_version": 11, "resulting_version": 12, "notice_id": WorkflowNoticeID("workflow-work", 1, "spec", "spec:one", "workflow-work", "informational"), "source_contract_version": 1, "entity_kind": "spec", "entity_ref": "spec:one", "target_work_id": "workflow-work", "edge_id": "edge:one", "old_hash": nil, "new_hash": nil, "severity": "informational",
		}),
		workflowEventWithActor("decision", WorkflowActionCheckpointed, "workflow-work", actor, map[string]any{
			"work_id": "workflow-work", "expected_version": 12, "resulting_version": 13, "step_id": "decision", "step_kind": "human_checkpoint", "attempt_epoch": 2, "checkpoint_payload": map[string]any{"action_id": "record_decision", "question": "Which path?", "options_considered": []string{"one"}, "decision": "accepted_decision", "rationale": "one is supported", "consequences": []string{"ship one"}, "inputs": []string{"input:one"}, "poc_findings": "no POC"}, "resume_cursor": "", "actor_ref": actor, "request_id": "request:decision", "checkpoint_id": "checkpoint:decision", "accepted_inputs_digest": "sha256:" + strings.Repeat("d", 64), "idempotency_identity": "operation-decision",
		}),
		workflowEventWithActor("premise", WorkflowPremiseConfirmed, "workflow-work", DeriveWorkflowActorRef("principal/operator", "client/concord-1", "agent/operator", "session/operator"), map[string]any{
			"work_id": "workflow-work", "expected_version": 13, "resulting_version": 14, "contract_version": 1, "confirming_actor_ref": DeriveWorkflowActorRef("principal/operator", "client/concord-1", "agent/operator", "session/operator"),
		}),
		workflowEventWithActor("candidate", WorkflowCandidateSetRevised, "workflow-work", actor, map[string]any{
			"work_id": "workflow-work", "expected_version": 14, "resulting_version": 15, "contract_version": 1, "candidate_kind": "work_item", "candidate_ref": "candidate:one", "added": []string{"candidate:one"}, "removed": []string{},
		}),
	}
	if err := applyWorkflowTestOperation(ctx, s, Operation{Events: events, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "workflow-work"): base}}); err != nil {
		t.Fatalf("workflow operation: %v", err)
	}
	before := fullWorkflowProjectionSnapshot(t, s)
	if err := RebuildFromLog(ctx, s); err != nil {
		t.Fatalf("workflow rebuild: %v", err)
	}
	after := fullWorkflowProjectionSnapshot(t, s)
	if after != before {
		t.Fatalf("workflow rebuild changed projection bytes:\nbefore=%s\nafter=%s", before, after)
	}
}

func TestWorkflowActorRowsRejectMutationAndDifferentTuple(t *testing.T) {
	s := openTemp(t)
	seedWork(t, s, "actor-work")
	actor := DeriveWorkflowActorRef("principal/operator", "client/concord-1", "agent/runner", "session/1")
	event := workflowEvent("actor", WorkflowActorRecorded, "actor-work", map[string]any{
		"work_id": "actor-work", "expected_version": 2, "resulting_version": 3, "actor_ref": actor,
		"principal_ref": "principal/operator", "client_ref": "client/concord-1", "agent_ref": "agent/runner", "session_ref": "session/1", "actor_class": "agent",
	})
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "actor-work"): 2}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`UPDATE workflow_actors SET client_ref='client/other' WHERE actor_ref=?`, actor); err == nil {
		t.Fatal("direct actor update succeeded")
	}
	conflict := workflowEvent("actor-again", WorkflowActorRecorded, "actor-work", map[string]any{
		"work_id": "actor-work", "expected_version": 3, "resulting_version": 4, "actor_ref": actor,
		"principal_ref": "principal/operator", "client_ref": "client/other", "agent_ref": "agent/runner", "session_ref": "session/1", "actor_class": "agent",
	})
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{conflict}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "actor-work"): 3}}); err == nil {
		t.Fatal("different actor tuple was accepted for an existing actor_ref")
	}
}

func TestWorkflowCompletedCannotBeAppendedThroughGenericEventAPI(t *testing.T) {
	s := openTemp(t)
	event := workflowEvent("direct-completed", WorkflowCompleted, "work-not-created", map[string]any{
		"work_id": "work-not-created", "expected_version": 1, "resulting_version": 2,
		"terminal_state": "completed", "final_verdict_kind": "ok", "verdict_actor_ref": "actor:" + strings.Repeat("a", 64),
		"premise_confirmed": true, "evidence_count": 0, "changed_refs_digest": "sha256:" + strings.Repeat("b", 64),
	})
	err := ApplyOperation(context.Background(), s, Operation{Events: []Event{event}})
	assertFailureKind(t, err, KindWorkflowCompletionRequired)
	assertTableCount(t, s, "domain_events", 0)
}

func TestWorkflowCompletionGateUsesFirstRefusalAndRollsBackAllTerminalWrites(t *testing.T) {
	s := openTemp(t)
	seedWork(t, s, "ordered-gate-work")
	actor := DeriveWorkflowActorRef("principal/operator", "client/concord-1", "agent/executor", "session/executor")
	digest, err := WorkflowDefinitionDigest(BuiltinWorkflowDefinitions()[0])
	if err != nil {
		t.Fatal(err)
	}
	events := []Event{
		workflowEvent("ordered-actor", WorkflowActorRecorded, "ordered-gate-work", map[string]any{
			"work_id": "ordered-gate-work", "expected_version": 2, "resulting_version": 3, "actor_ref": actor,
			"principal_ref": "principal/operator", "client_ref": "client/concord-1", "agent_ref": "agent/executor", "session_ref": "session/executor", "actor_class": "agent",
		}),
		workflowEvent("ordered-definition", WorkflowDefinitionSelected, "ordered-gate-work", map[string]any{
			"work_id": "ordered-gate-work", "expected_version": 3, "resulting_version": 4, "ref": "workflow.implementation", "version": 1, "digest": digest, "work_kind": "implementation",
		}),
		workflowEventWithActor("ordered-contract", WorkflowContractApproved, "ordered-gate-work", actor, map[string]any{
			"work_id": "ordered-gate-work", "expected_version": 4, "resulting_version": 5, "contract_version": 1, "premise": "deliver the checked change",
			"outcome_kind": "check", "outcome_payload": map[string]any{"kind": "check", "check_ref": "check:workflow", "immutable_subject_ref": "commit:ordered", "expected_result": "pass"},
			"required_evidence": []string{"verification"}, "route_conventions": []string{}, "spec_mandate": []string{}, "rigor_class": "prototype/internal", "consequence_class": "internal_sqlite",
		}),
	}
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: events, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "ordered-gate-work"): 2}}); err != nil {
		t.Fatal(err)
	}
	var eventsBefore int
	if err := s.DB().QueryRow(`SELECT count(*) FROM domain_events`).Scan(&eventsBefore); err != nil {
		t.Fatal(err)
	}
	completion := workflowEvent("ordered-completion", WorkflowCompleted, "ordered-gate-work", map[string]any{
		"work_id": "ordered-gate-work", "expected_version": 5, "resulting_version": 6, "terminal_state": "completed", "final_verdict_kind": "ok", "verdict_actor_ref": actor,
		"premise_confirmed": true, "evidence_count": 1, "changed_refs_digest": "sha256:" + strings.Repeat("a", 64),
	})
	err = CompleteWorkflow(context.Background(), s, completion)
	assertFailureKind(t, err, KindMissingEvidence)
	assertTableCount(t, s, "domain_events", eventsBefore)
	assertTableCount(t, s, "workflow_impact_notices", 0)
	var state string
	if err := s.DB().QueryRow(`SELECT instance_state FROM workflow_instances WHERE work_id='ordered-gate-work'`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state == "completed" {
		t.Fatal("first completion refusal changed the terminal projection")
	}
}

func TestWorkflowCompletionGateCommitsTerminalEventOnlyAfterAllClauses(t *testing.T) {
	s := openTemp(t)
	seedWork(t, s, "complete-work")
	seedWorkflowLaw(t, s)
	executor := DeriveWorkflowActorRef("principal/operator", "client/concord-1", "agent/executor", "session/complete")
	operator := DeriveWorkflowActorRef("principal/operator", "client/concord-1", "agent/reviewer", "session/review")
	digest, err := WorkflowDefinitionDigest(BuiltinWorkflowDefinitions()[0])
	if err != nil {
		t.Fatal(err)
	}
	events := []Event{
		workflowEvent("complete-executor", WorkflowActorRecorded, "complete-work", map[string]any{"work_id": "complete-work", "expected_version": 2, "resulting_version": 3, "actor_ref": executor, "principal_ref": "principal/operator", "client_ref": "client/concord-1", "agent_ref": "agent/executor", "session_ref": "session/complete", "actor_class": "agent"}),
		workflowEvent("complete-operator", WorkflowActorRecorded, "complete-work", map[string]any{"work_id": "complete-work", "expected_version": 3, "resulting_version": 4, "actor_ref": operator, "principal_ref": "principal/operator", "client_ref": "client/concord-1", "agent_ref": "agent/reviewer", "session_ref": "session/review", "actor_class": "operator"}),
		workflowEvent("complete-definition", WorkflowDefinitionSelected, "complete-work", map[string]any{"work_id": "complete-work", "expected_version": 4, "resulting_version": 5, "ref": "workflow.implementation", "version": 1, "digest": digest, "work_kind": "implementation"}),
		workflowEventWithActor("complete-contract", WorkflowContractApproved, "complete-work", executor, map[string]any{"work_id": "complete-work", "expected_version": 5, "resulting_version": 6, "contract_version": 1, "premise": "deliver the checked change", "outcome_kind": "check", "outcome_payload": map[string]any{"kind": "check", "check_ref": "check:workflow", "immutable_subject_ref": "commit:complete", "expected_result": "pass"}, "required_evidence": []string{"verification", "review"}, "route_conventions": []string{}, "spec_mandate": []string{"spec:one"}, "law_boundary_version": 1, "rigor_class": "prototype/internal", "consequence_class": "internal_sqlite"}),
		workflowEventWithActor("complete-start", WorkflowActionStarted, "complete-work", executor, map[string]any{"work_id": "complete-work", "expected_version": 6, "resulting_version": 7, "step_id": "execution", "action_id": "start_execution", "attempt_epoch": 1, "accepted_inputs_digest": "sha256:" + strings.Repeat("b", 64), "idempotency_identity": "complete-operation", "actor_ref": executor}),
		workflowEvent("complete-impact", WorkflowImpactDeclared, "complete-work", map[string]any{"work_id": "complete-work", "expected_version": 7, "resulting_version": 8, "edge_id": "edge:complete", "edge_kind": "modifies", "edge_class": "none", "target_work_id": "complete-work", "target_kind": "work_item", "severity": "breaking"}),
	}
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: events, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "complete-work"): 2}}); err != nil {
		t.Fatal(err)
	}
	seedWorkflowAuthority(t, s, "complete-verification", "complete-work", "principal/verify", "request/verify", []string{"evidence:verification"})
	seedWorkflowAuthority(t, s, "complete-review", "complete-work", "principal/review", "request/review", []string{"evidence:review"})
	seedWorkflowAuthority(t, s, "complete-spec", "complete-work", "principal/spec", "request/spec", []string{"spec:one"})
	evidence := []Event{
		workflowEvent("complete-evidence-verification", WorkflowEvidenceBound, "complete-work", map[string]any{"work_id": "complete-work", "expected_version": 8, "resulting_version": 9, "evidence_kind": "verification", "immutable_subject_ref": "evidence:verification", "producer_id": "principal/verify", "producer_run_ref": "complete-verification", "producer_watermark": "request/verify", "observed_at": "2026-08-09T00:00:00Z"}),
		workflowEvent("complete-evidence-review", WorkflowEvidenceBound, "complete-work", map[string]any{"work_id": "complete-work", "expected_version": 9, "resulting_version": 10, "evidence_kind": "review", "immutable_subject_ref": "evidence:review", "producer_id": "principal/review", "producer_run_ref": "complete-review", "producer_watermark": "request/review", "observed_at": "2026-08-09T00:00:00Z"}),
		workflowEvent("complete-evidence-spec", WorkflowEvidenceBound, "complete-work", map[string]any{"work_id": "complete-work", "expected_version": 10, "resulting_version": 11, "evidence_kind": "artifact", "immutable_subject_ref": "spec:one", "producer_id": "principal/spec", "producer_run_ref": "complete-spec", "producer_watermark": "request/spec", "observed_at": "2026-08-09T00:00:00Z"}),
		workflowEventWithActor("complete-verdict", WorkflowVerdictRecorded, "complete-work", operator, map[string]any{"work_id": "complete-work", "expected_version": 11, "resulting_version": 12, "contract_version": 1, "predicate_id": "predicate:complete", "verdict_kind": "ok", "verdict_actor_ref": operator, "evaluation_evidence": []string{"evidence:verification"}, "incomparable_with_approved": false}),
		workflowEventWithActor("complete-premise", WorkflowPremiseConfirmed, "complete-work", operator, map[string]any{"work_id": "complete-work", "expected_version": 12, "resulting_version": 13, "contract_version": 1, "confirming_actor_ref": operator}),
	}
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: evidence, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "complete-work"): 8}}); err != nil {
		t.Fatal(err)
	}
	completion := workflowEventWithActor("complete-terminal", WorkflowCompleted, "complete-work", executor, map[string]any{"work_id": "complete-work", "expected_version": 13, "resulting_version": 14, "terminal_state": "completed", "final_verdict_kind": "ok", "verdict_actor_ref": operator, "premise_confirmed": true, "evidence_count": 3, "changed_refs_digest": "sha256:" + strings.Repeat("a", 64)})
	if err := CompleteWorkflow(context.Background(), s, completion); err != nil {
		t.Fatal(err)
	}
	var state string
	if err := s.DB().QueryRow(`SELECT instance_state FROM workflow_instances WHERE work_id='complete-work'`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "completed" {
		t.Fatalf("workflow state = %q, want completed", state)
	}
	if err := CompleteWorkflow(context.Background(), s, completion); err != nil {
		t.Fatalf("completion replay: %v", err)
	}
	assertTableCount(t, s, "workflow_impact_notices", 1)
	var noticeID string
	if err := s.DB().QueryRow(`SELECT notice_id FROM workflow_impact_notices WHERE source_work_id='complete-work'`).Scan(&noticeID); err != nil {
		t.Fatal(err)
	}
	wantNotice := WorkflowNoticeID("complete-work", 1, "spec", "spec:one", "complete-work", "breaking")
	if noticeID != wantNotice {
		t.Fatalf("notice_id = %q, want %q", noticeID, wantNotice)
	}
}

func TestWorkflowConditionResolutionMustUseStoredAuthority(t *testing.T) {
	s := openTemp(t)
	seedWork(t, s, "condition-work")
	seedWorkflowAuthority(t, s, "condition-authority", "condition-work", "principal/resolver", "request:condition", []string{"evidence:valid"})
	added := workflowEventWithActor("condition-added", WorkflowConditionAdded, "condition-work", "principal/resolver", map[string]any{
		"work_id": "condition-work", "expected_version": 2, "resulting_version": 3, "condition_id": "condition-1", "await_type": "ci_result", "await_ref": "run:one", "resolution_authority": "durable_operation:condition-authority",
	})
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{added}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "condition-work"): 2}}); err != nil {
		t.Fatal(err)
	}
	resolved := workflowEventWithActor("condition-resolved", WorkflowConditionResolved, "condition-work", "principal/resolver", map[string]any{
		"work_id": "condition-work", "expected_version": 3, "resulting_version": 4, "condition_id": "condition-1", "resolution_evidence": []string{"evidence:forged"}, "resolved_by_event": "provider-event",
	})
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{resolved}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "condition-work"): 3}}); err == nil {
		t.Fatal("condition resolved through an authority different from the stored resolver")
	}
	var state string
	if err := s.DB().QueryRow(`SELECT condition_state FROM workflow_external_conditions WHERE work_id='condition-work' AND condition_id='condition-1'`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "open" {
		t.Fatalf("condition state = %q, want open", state)
	}
}

type explicitConditionResolver struct {
	calls int
}

func (r *explicitConditionResolver) Resolve(_ context.Context, _ ExternalCondition, _ time.Time) (Resolution, error) {
	r.calls++
	return Resolution{ResolutionEvidence: []string{"evidence:valid"}, ResolvedByEvent: "condition-resolution-event", ActorRef: "principal/resolver"}, nil
}

func TestWorkflowConditionResolutionIsExplicitAndUsesAuthoritativeEvidence(t *testing.T) {
	s := openTemp(t)
	seedWork(t, s, "explicit-condition-work")
	seedWorkflowAuthority(t, s, "explicit-condition-authority", "explicit-condition-work", "principal/resolver", "request:condition", []string{"evidence:valid"})
	added := workflowEventWithActor("explicit-condition-added", WorkflowConditionAdded, "explicit-condition-work", "principal/resolver", map[string]any{
		"work_id": "explicit-condition-work", "expected_version": 2, "resulting_version": 3, "condition_id": "condition:explicit", "await_type": "timer", "await_ref": "deadline:one", "resolution_authority": "durable_operation:explicit-condition-authority",
	})
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{added}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "explicit-condition-work"): 2}}); err != nil {
		t.Fatal(err)
	}
	resolver := &explicitConditionResolver{}
	if err := ResolveWorkflowCondition(context.Background(), s, "explicit-condition-work", "condition:explicit", resolver, time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	if resolver.calls != 1 {
		t.Fatalf("resolver calls = %d, want one explicit resolution", resolver.calls)
	}
	var state string
	if err := s.DB().QueryRow(`SELECT condition_state FROM workflow_external_conditions WHERE work_id='explicit-condition-work' AND condition_id='condition:explicit'`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "resolved" {
		t.Fatalf("condition state = %q, want resolved", state)
	}
}

type boundaryConditionResolver struct {
	resolutions map[string]Resolution
	errors      map[string]error
}

func (r *boundaryConditionResolver) Resolve(_ context.Context, condition ExternalCondition, _ time.Time) (Resolution, error) {
	if err := r.errors[condition.ConditionID]; err != nil {
		return Resolution{}, err
	}
	return r.resolutions[condition.ConditionID], nil
}

func TestWorkflowConsequentialBoundaryResolvesEligibleConditionsOnly(t *testing.T) {
	s := openTemp(t)
	seedWork(t, s, "boundary-work")
	seedWorkflowAuthority(t, s, "boundary-authority-one", "boundary-work", "principal/one", "request/one", []string{"evidence:one"})
	seedWorkflowAuthority(t, s, "boundary-authority-two", "boundary-work", "principal/two", "request/two", []string{"evidence:two"})
	events := []Event{
		workflowEvent("boundary-add-one", WorkflowConditionAdded, "boundary-work", map[string]any{"work_id": "boundary-work", "expected_version": 2, "resulting_version": 3, "condition_id": "condition:one", "await_type": "ci_result", "await_ref": "run:one", "resolution_authority": "durable_operation:boundary-authority-one"}),
		workflowEvent("boundary-add-two", WorkflowConditionAdded, "boundary-work", map[string]any{"work_id": "boundary-work", "expected_version": 3, "resulting_version": 4, "condition_id": "condition:two", "await_type": "pr_merge", "await_ref": "pr:two", "resolution_authority": "durable_operation:boundary-authority-two"}),
	}
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: events, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "boundary-work"): 2}}); err != nil {
		t.Fatal(err)
	}
	resolver := &boundaryConditionResolver{
		resolutions: map[string]Resolution{"condition:one": {ResolutionEvidence: []string{"evidence:one"}, ResolvedByEvent: "boundary-resolve-one", ActorRef: "principal/one"}},
		errors:      map[string]error{"condition:two": errors.New("provider state is not terminal")},
	}
	resolved, err := ResolveWorkflowConditionsAtBoundary(context.Background(), s, "boundary-work", resolver, time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC))
	if err != nil || resolved != 1 {
		t.Fatalf("resolved=%d err=%v, want one eligible condition", resolved, err)
	}
	var one, two, version string
	if err := s.DB().QueryRow(`SELECT condition_state FROM workflow_external_conditions WHERE work_id='boundary-work' AND condition_id='condition:one'`).Scan(&one); err != nil {
		t.Fatal(err)
	}
	if err := s.DB().QueryRow(`SELECT condition_state FROM workflow_external_conditions WHERE work_id='boundary-work' AND condition_id='condition:two'`).Scan(&two); err != nil {
		t.Fatal(err)
	}
	if err := s.DB().QueryRow(`SELECT version FROM work_items WHERE id='boundary-work'`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if one != "resolved" || two != "open" || version != "5" {
		t.Fatalf("condition states/version = %s/%s/%s, want resolved/open/5", one, two, version)
	}
}

func TestWorkflowConsequentialBoundaryRollsBackEarlierResolutionOnInvalidEvidence(t *testing.T) {
	s := openTemp(t)
	seedWork(t, s, "boundary-rollback-work")
	seedWorkflowAuthority(t, s, "boundary-rollback-one", "boundary-rollback-work", "principal/one", "request/one", []string{"evidence:one"})
	seedWorkflowAuthority(t, s, "boundary-rollback-two", "boundary-rollback-work", "principal/two", "request/two", []string{"evidence:two"})
	events := []Event{
		workflowEvent("boundary-rollback-add-one", WorkflowConditionAdded, "boundary-rollback-work", map[string]any{"work_id": "boundary-rollback-work", "expected_version": 2, "resulting_version": 3, "condition_id": "condition:one", "await_type": "ci_result", "await_ref": "run:one", "resolution_authority": "durable_operation:boundary-rollback-one"}),
		workflowEvent("boundary-rollback-add-two", WorkflowConditionAdded, "boundary-rollback-work", map[string]any{"work_id": "boundary-rollback-work", "expected_version": 3, "resulting_version": 4, "condition_id": "condition:two", "await_type": "pr_merge", "await_ref": "pr:two", "resolution_authority": "durable_operation:boundary-rollback-two"}),
	}
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: events, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "boundary-rollback-work"): 2}}); err != nil {
		t.Fatal(err)
	}
	resolver := &boundaryConditionResolver{resolutions: map[string]Resolution{
		"condition:one": {ResolutionEvidence: []string{"evidence:one"}, ResolvedByEvent: "boundary-rollback-resolve-one", ActorRef: "principal/one"},
		"condition:two": {ResolutionEvidence: []string{"evidence:forged"}, ResolvedByEvent: "boundary-rollback-resolve-two", ActorRef: "principal/two"},
	}}
	if _, err := ResolveWorkflowConditionsAtBoundary(context.Background(), s, "boundary-rollback-work", resolver, time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC)); err == nil {
		t.Fatal("invalid boundary evidence was accepted")
	}
	var states, version string
	if err := s.DB().QueryRow(`SELECT group_concat(condition_state, ',') FROM workflow_external_conditions WHERE work_id='boundary-rollback-work' ORDER BY condition_id`).Scan(&states); err != nil {
		t.Fatal(err)
	}
	if err := s.DB().QueryRow(`SELECT version FROM work_items WHERE id='boundary-rollback-work'`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if states != "open,open" || version != "4" {
		t.Fatalf("boundary rollback states/version = %s/%s, want open,open/4", states, version)
	}
}

type completionGateCase struct {
	name             string
	requiredEvidence []string
	includeSpec      bool
	includeVerdict   bool
	includePremise   bool
	verdictKind      string
	openCondition    bool
	conflictNotice   bool
	wantKind         FailureKind
	wantClause       int
}

func TestWorkflowCompletionGateAdjacentClausePrecedenceAndRollback(t *testing.T) {
	cases := []completionGateCase{
		{name: "clause-1-before-2", requiredEvidence: []string{"verification", "review", "native_run"}, openCondition: true, wantKind: KindMissingEvidence, wantClause: 1},
		{name: "clause-2-before-3", requiredEvidence: []string{"verification", "review"}, openCondition: true, wantKind: KindNotTerminal, wantClause: 2},
		{name: "clause-3-before-4", requiredEvidence: []string{"verification", "review"}, wantKind: KindInvariantViolation, wantClause: 3},
		{name: "clause-4-before-5", requiredEvidence: []string{"verification", "review"}, includeSpec: true, wantKind: KindMissingEvidence, wantClause: 4},
		{name: "clause-5-before-6", requiredEvidence: []string{"verification", "review"}, includeSpec: true, includeVerdict: true, verdictKind: "outcome_mismatch", wantKind: KindApprovalRequired, wantClause: 5},
		{name: "clause-6-before-7", requiredEvidence: []string{"verification", "review"}, includeSpec: true, includeVerdict: true, includePremise: true, verdictKind: "outcome_mismatch", conflictNotice: true, wantKind: KindOutcomeMismatch, wantClause: 6},
		{name: "clause-7-rollback", requiredEvidence: []string{"verification", "review"}, includeSpec: true, includeVerdict: true, includePremise: true, verdictKind: "ok", conflictNotice: true, wantKind: KindOperationConflict, wantClause: 7},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			workID := "gate-" + strings.ReplaceAll(testCase.name, "-", "_")
			s, completion := seedCompletionGateCase(t, workID, testCase)
			var eventsBefore int
			if err := s.DB().QueryRow(`SELECT count(*) FROM domain_events`).Scan(&eventsBefore); err != nil {
				t.Fatal(err)
			}
			err := CompleteWorkflow(context.Background(), s, completion)
			var failure *Failure
			if !failureAs(err, &failure) || failure.Kind != testCase.wantKind || failure.Clause != testCase.wantClause {
				t.Fatalf("failure=%+v err=%v, want kind=%s clause=%d", failure, err, testCase.wantKind, testCase.wantClause)
			}
			assertTableCount(t, s, "domain_events", eventsBefore)
			assertTableCount(t, s, "workflow_impact_notices", 0)
		})
	}
}

func TestWorkflowConsequentialActionBoundaryIsOneOwningTransaction(t *testing.T) {
	t.Run("success commits resolution and action", func(t *testing.T) {
		s, request, resolver := seedBoundaryActionRequest(t, "action-boundary-success", false)
		if err := AuthorizeWorkflowActionAtBoundaryTx(context.Background(), s, BuiltinWorkflowRegistry(), request, resolver, time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC), nil, appendActionCompletion); err != nil {
			t.Fatal(err)
		}
		assertConditionState(t, s, "action-boundary-success", "condition:gate", "resolved")
		assertTableCount(t, s, "workflow_external_conditions", 1)
		var completed int
		if err := s.DB().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id='action-boundary-success' AND kind=?`, WorkflowActionCompleted).Scan(&completed); err != nil {
			t.Fatal(err)
		}
		if completed != 1 {
			t.Fatalf("action completion events=%d, want 1", completed)
		}
	})
	for _, testCase := range []struct {
		name      string
		authorize func(*sql.Tx) error
		mutate    func(*sql.Tx) error
		addSecond bool
		resolver  *boundaryConditionResolver
	}{
		{name: "later preflight failure", addSecond: true, resolver: &boundaryConditionResolver{resolutions: map[string]Resolution{"condition:gate": {ResolutionEvidence: []string{"evidence:condition"}, ResolvedByEvent: "boundary-later-preflight", ActorRef: "principal/condition"}}, errors: map[string]error{"condition:later": errors.New("later condition is ineligible")}}},
		{name: "authorization denial", resolver: &boundaryConditionResolver{resolutions: map[string]Resolution{"condition:gate": {ResolutionEvidence: []string{"evidence:condition"}, ResolvedByEvent: "boundary-auth-denial", ActorRef: "principal/condition"}}}, authorize: func(*sql.Tx) error { return errors.New("authorization denied") }, mutate: func(*sql.Tx) error { return nil }},
		{name: "mutation failure", resolver: &boundaryConditionResolver{resolutions: map[string]Resolution{"condition:gate": {ResolutionEvidence: []string{"evidence:condition"}, ResolvedByEvent: "boundary-mutation-failure", ActorRef: "principal/condition"}}}, mutate: func(*sql.Tx) error { return errors.New("action mutation failed") }},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			workID := "action-boundary-" + strings.ReplaceAll(testCase.name, " ", "-")
			s, request, _ := seedBoundaryActionRequest(t, workID, testCase.addSecond)
			if testCase.addSecond {
				// The helper's resolver is replaced below after the second condition
				// has been added; this preserves one deterministic owning transaction.
			}
			if testCase.addSecond {
				if err := AuthorizeWorkflowActionAtBoundaryTx(context.Background(), s, BuiltinWorkflowRegistry(), request, testCase.resolver, time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC), testCase.authorize, testCase.mutate); err == nil {
					t.Fatal("later preflight failure was accepted")
				}
			} else if err := AuthorizeWorkflowActionAtBoundaryTx(context.Background(), s, BuiltinWorkflowRegistry(), request, testCase.resolver, time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC), testCase.authorize, testCase.mutate); err == nil {
				t.Fatal("downstream failure was accepted")
			}
			assertConditionState(t, s, workID, "condition:gate", "open")
			var resolvedEvents, completedEvents int
			if err := s.DB().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id=? AND kind=?`, workID, WorkflowConditionResolved).Scan(&resolvedEvents); err != nil {
				t.Fatal(err)
			}
			if err := s.DB().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id=? AND kind=?`, workID, WorkflowActionCompleted).Scan(&completedEvents); err != nil {
				t.Fatal(err)
			}
			if resolvedEvents != 0 || completedEvents != 0 {
				t.Fatalf("rolled-back events resolved=%d completed=%d", resolvedEvents, completedEvents)
			}
		})
	}
}

func TestWorkflowReadyReportsUnreadableConditionWithoutRewrite(t *testing.T) {
	s, _ := seedCompletionGateCase(t, "ready-unreadable", completionGateCase{requiredEvidence: []string{"verification", "review"}, openCondition: true})
	if _, err := s.DB().Exec(`DELETE FROM durable_operations WHERE op_id=?`, "gate-condition-ready-unreadable"); err != nil {
		t.Fatal(err)
	}
	var before int
	if err := s.DB().QueryRow(`SELECT count(*) FROM domain_events`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	result, err := DeriveWorkflowReady(context.Background(), s, "ready-unreadable")
	if err != nil || result.Ready || len(result.UnknownConditions) != 1 || result.UnknownConditions[0] != "condition:gate" {
		t.Fatalf("ready result=%+v err=%v", result, err)
	}
	var after int
	if err := s.DB().QueryRow(`SELECT count(*) FROM domain_events`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("readiness derivation rewrote event history: before=%d after=%d", before, after)
	}
}

func TestWorkflowBlockingStalenessRefusesCompletionSemantics(t *testing.T) {
	s, _ := seedCompletionGateCase(t, "staleness-block", completionGateCase{requiredEvidence: []string{"verification", "review"}})
	tx, err := s.DB().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	definition := cloneWorkflowDefinition(BuiltinWorkflowDefinitions()[0])
	definition.StalenessRules = []WorkflowStalenessRule{{ID: "staleness:block", InputRef: "input:drifted", Severity: "block"}}
	err = verifyBlockingStaleness(context.Background(), tx, "staleness-block", definition, nil)
	var failure *Failure
	if !failureAs(err, &failure) || failure.Kind != KindStaleRequiresReview {
		t.Fatalf("staleness block failure=%+v err=%v", failure, err)
	}
}

func TestWorkflowWarningStalenessIsRecordedForNextRead(t *testing.T) {
	s, _ := seedCompletionGateCase(t, "staleness-warning", completionGateCase{requiredEvidence: []string{"verification", "review"}})
	tx, err := s.DB().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	definition := cloneWorkflowDefinition(BuiltinWorkflowDefinitions()[0])
	definition.StalenessRules = []WorkflowStalenessRule{{ID: "staleness:warning", InputRef: "input:drifted", Severity: "warning"}}
	warnings, err := workflowStalenessWarnings(context.Background(), tx, "staleness-warning", definition, nil)
	if err != nil || !reflect.DeepEqual(warnings, []string{"staleness:warning"}) {
		t.Fatalf("staleness warnings=%v err=%v", warnings, err)
	}
}

func TestWorkflowContractRevisionEmitsBreakingNoticeForConsumedActiveDependent(t *testing.T) {
	source, _ := seedCompletionGateCase(t, "revision-source", completionGateCase{requiredEvidence: []string{"verification", "review"}})
	seedWork(t, source, "revision-dependent")
	dependentActor := DeriveWorkflowActorRef("principal/operator", "client/concord-1", "agent/dependent", "session/revision-dependent")
	digest, err := WorkflowDefinitionDigest(BuiltinWorkflowDefinitions()[0])
	if err != nil {
		t.Fatal(err)
	}
	dependentEvents := []Event{
		workflowEvent("revision-dependent-actor", WorkflowActorRecorded, "revision-dependent", map[string]any{"work_id": "revision-dependent", "expected_version": 2, "resulting_version": 3, "actor_ref": dependentActor, "principal_ref": "principal/operator", "client_ref": "client/concord-1", "agent_ref": "agent/dependent", "session_ref": "session/revision-dependent", "actor_class": "agent"}),
		workflowEvent("revision-dependent-definition", WorkflowDefinitionSelected, "revision-dependent", map[string]any{"work_id": "revision-dependent", "expected_version": 3, "resulting_version": 4, "ref": "workflow.implementation", "version": 1, "digest": digest, "work_kind": "implementation"}),
		workflowEventWithActor("revision-dependent-contract", WorkflowContractApproved, "revision-dependent", dependentActor, map[string]any{"work_id": "revision-dependent", "expected_version": 4, "resulting_version": 5, "contract_version": 1, "premise": "consume the source contract", "outcome_kind": "check", "outcome_payload": map[string]any{"kind": "check", "check_ref": "check:workflow", "immutable_subject_ref": "commit:dependent", "expected_result": "pass"}, "required_evidence": []string{"verification"}, "route_conventions": []string{}, "spec_mandate": []string{}, "rigor_class": "prototype/internal", "consequence_class": "internal_sqlite"}),
		workflowEventWithActor("revision-dependent-start", WorkflowActionStarted, "revision-dependent", dependentActor, map[string]any{"work_id": "revision-dependent", "expected_version": 5, "resulting_version": 6, "step_id": "execution", "action_id": "start_execution", "attempt_epoch": 1, "accepted_inputs_digest": "sha256:" + strings.Repeat("b", 64), "idempotency_identity": "dependent-operation", "actor_ref": dependentActor}),
	}
	if err := applyWorkflowTestOperation(context.Background(), source, Operation{Events: dependentEvents, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "revision-dependent"): 2}}); err != nil {
		t.Fatal(err)
	}
	var sourceVersion int64
	if err := source.DB().QueryRow(`SELECT version FROM work_items WHERE id='revision-source'`).Scan(&sourceVersion); err != nil {
		t.Fatal(err)
	}
	actor := DeriveWorkflowActorRef("principal/operator", "client/concord-1", "agent/executor", "session/revision-source")
	edge := workflowEventWithActor("revision-edge", WorkflowImpactDeclared, "revision-source", actor, map[string]any{"work_id": "revision-source", "expected_version": sourceVersion, "resulting_version": sourceVersion + 1, "edge_id": "edge:revision", "edge_kind": "depends_on", "edge_class": "hard", "target_work_id": "revision-dependent", "target_kind": "work_item", "severity": "breaking"})
	if err := applyWorkflowTestOperation(context.Background(), source, Operation{Events: []Event{edge}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "revision-source"): sourceVersion}}); err != nil {
		t.Fatal(err)
	}
	sourceVersion++
	newContract := workflowEventWithActor("revision-new-contract", WorkflowContractApproved, "revision-source", actor, map[string]any{"work_id": "revision-source", "expected_version": sourceVersion, "resulting_version": sourceVersion + 1, "contract_version": 2, "premise": "refresh the checked change", "outcome_kind": "check", "outcome_payload": map[string]any{"kind": "check", "check_ref": "check:workflow", "immutable_subject_ref": "commit:revision-new", "expected_result": "pass"}, "required_evidence": []string{"verification", "review"}, "route_conventions": []string{}, "spec_mandate": []string{}, "rigor_class": "prototype/internal", "consequence_class": "internal_sqlite"})
	if err := applyWorkflowTestOperation(context.Background(), source, Operation{Events: []Event{newContract}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "revision-source"): sourceVersion}}); err != nil {
		t.Fatal(err)
	}
	sourceVersion++
	supersede := workflowEventWithActor("revision-supersede", WorkflowContractSuperseded, "revision-source", actor, map[string]any{"work_id": "revision-source", "expected_version": sourceVersion, "resulting_version": sourceVersion + 1, "previous_contract_version": 1, "new_contract_version": 2, "supersede_reason": "refresh approved contract", "audit_evidence": []string{"evidence:audit"}})
	if err := SupersedeWorkflowContract(context.Background(), source, supersede); err != nil {
		t.Fatal(err)
	}
	var severity, entityKind string
	if err := source.DB().QueryRow(`SELECT severity,entity_kind FROM workflow_impact_notices WHERE source_work_id='revision-source' AND target_work_id='revision-dependent'`).Scan(&severity, &entityKind); err != nil {
		t.Fatal(err)
	}
	if severity != "breaking" || entityKind != "workflow_contract" {
		t.Fatalf("revision notice=%s/%s, want breaking/workflow_contract", severity, entityKind)
	}
}

func seedBoundaryActionRequest(t *testing.T, workID string, addSecond bool) (*Store, WorkflowActionPreflightRequest, *boundaryConditionResolver) {
	t.Helper()
	s, _ := seedCompletionGateCase(t, workID, completionGateCase{requiredEvidence: []string{"verification", "review"}, openCondition: true})
	actor := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/executor", SessionRef: "session/" + workID, ActorClass: ActorAgent}
	var version int64
	if err := s.DB().QueryRow(`SELECT version FROM work_items WHERE id=?`, workID).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if addSecond {
		seedWorkflowAuthority(t, s, "boundary-second-"+workID, workID, "principal/second", "request/second", []string{"evidence:second"})
		event := workflowEvent("boundary-second-event-"+workID, WorkflowConditionAdded, workID, map[string]any{"work_id": workID, "expected_version": version, "resulting_version": version + 1, "condition_id": "condition:later", "await_type": "ci_result", "await_ref": "run:later", "resolution_authority": "durable_operation:boundary-second-" + workID})
		if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}}); err != nil {
			t.Fatal(err)
		}
		version++
	}
	request := WorkflowActionPreflightRequest{WorkID: workID, ExpectedVersion: version, StepID: "execution", ActionID: "start_execution", Actor: actor}
	return s, request, &boundaryConditionResolver{resolutions: map[string]Resolution{"condition:gate": {ResolutionEvidence: []string{"evidence:condition"}, ResolvedByEvent: "boundary-default-resolution-" + workID, ActorRef: "principal/condition"}}}
}

func appendActionCompletion(tx *sql.Tx) error {
	var workID string
	var version int64
	if err := tx.QueryRow(`SELECT work_id,version FROM workflow_instances JOIN work_items ON work_items.id=workflow_instances.work_id WHERE current_step='execution' ORDER BY work_id DESC LIMIT 1`).Scan(&workID, &version); err != nil {
		return err
	}
	var actor string
	if err := tx.QueryRow(`SELECT execution_actor_ref FROM workflow_instances WHERE work_id=?`, workID).Scan(&actor); err != nil {
		return err
	}
	payload := map[string]any{"work_id": workID, "expected_version": version, "resulting_version": version + 1, "step_id": "execution", "attempt_epoch": 1, "result_evidence_refs": []string{}, "changed_refs": []string{}, "actor_ref": actor}
	raw, _ := json.Marshal(payload)
	event := Event{EventID: "boundary-action-completed-" + workID, Kind: WorkflowActionCompleted, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: actor, OccurredAt: time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC), PayloadVersion: 1, Payload: raw}
	if _, err := appendEvent(context.Background(), tx, event, true); err != nil {
		return err
	}
	return foldRegisteredEvent(context.Background(), tx, event)
}

func assertConditionState(t *testing.T, s *Store, workID, conditionID, want string) {
	t.Helper()
	var got string
	if err := s.DB().QueryRow(`SELECT condition_state FROM workflow_external_conditions WHERE work_id=? AND condition_id=?`, workID, conditionID).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("condition %s state=%q, want %q", conditionID, got, want)
	}
}

func seedWorkflowLaw(t *testing.T, s *Store) {
	t.Helper()
	if _, err := s.DB().Exec(`INSERT INTO fold_guard(active) VALUES(1); INSERT INTO project_locators(locator_id,project_id,kind,locator_value,normalized_value,created_at,updated_at) VALUES('workflow-law-locator','project','canonical_path','workflow-law-repo','workflow-law-repo','now','now'); INSERT INTO product_knowledge_homes(product_id,project_id,locator_id) VALUES('product','project','workflow-law-locator'); INSERT INTO law_subjects(home_project_id,home_locator_id,law_id,kind,status,path,title,content_hash,scanned_commit_oid) VALUES('project','workflow-law-locator','spec:one','spec','accepted','docs/spec.md','Synthetic test law','sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','test'); DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
}

func seedCompletionGateCase(t *testing.T, workID string, testCase completionGateCase) (*Store, Event) {
	t.Helper()
	s := openTemp(t)
	seedWork(t, s, workID)
	seedWorkflowLaw(t, s)
	executor := DeriveWorkflowActorRef("principal/operator", "client/concord-1", "agent/executor", "session/"+workID)
	operator := DeriveWorkflowActorRef("principal/operator", "client/concord-1", "agent/reviewer", "session/"+workID)
	digest, err := WorkflowDefinitionDigest(BuiltinWorkflowDefinitions()[0])
	if err != nil {
		t.Fatal(err)
	}
	version := int64(2)
	events := make([]Event, 0, 12)
	appendEvent := func(event Event) {
		events = append(events, event)
		version++
	}
	appendEvent(workflowEvent("gate-actor-"+workID, WorkflowActorRecorded, workID, map[string]any{"work_id": workID, "expected_version": version, "resulting_version": version + 1, "actor_ref": executor, "principal_ref": "principal/operator", "client_ref": "client/concord-1", "agent_ref": "agent/executor", "session_ref": "session/" + workID, "actor_class": "agent"}))
	appendEvent(workflowEvent("gate-operator-"+workID, WorkflowActorRecorded, workID, map[string]any{"work_id": workID, "expected_version": version, "resulting_version": version + 1, "actor_ref": operator, "principal_ref": "principal/operator", "client_ref": "client/concord-1", "agent_ref": "agent/reviewer", "session_ref": "session/" + workID, "actor_class": "operator"}))
	appendEvent(workflowEvent("gate-definition-"+workID, WorkflowDefinitionSelected, workID, map[string]any{"work_id": workID, "expected_version": version, "resulting_version": version + 1, "ref": "workflow.implementation", "version": 1, "digest": digest, "work_kind": "implementation"}))
	appendEvent(workflowEventWithActor("gate-contract-"+workID, WorkflowContractApproved, workID, executor, map[string]any{"work_id": workID, "expected_version": version, "resulting_version": version + 1, "contract_version": 1, "premise": "deliver the checked change", "outcome_kind": "check", "outcome_payload": map[string]any{"kind": "check", "check_ref": "check:workflow", "immutable_subject_ref": "commit:" + workID, "expected_result": "pass"}, "required_evidence": testCase.requiredEvidence, "route_conventions": []string{}, "spec_mandate": []string{"spec:one"}, "rigor_class": "prototype/internal", "consequence_class": "internal_sqlite"}))
	appendEvent(workflowEventWithActor("gate-start-"+workID, WorkflowActionStarted, workID, executor, map[string]any{"work_id": workID, "expected_version": version, "resulting_version": version + 1, "step_id": "execution", "action_id": "start_execution", "attempt_epoch": 1, "accepted_inputs_digest": "sha256:" + strings.Repeat("b", 64), "idempotency_identity": "gate-operation-" + workID, "actor_ref": executor}))
	appendEvent(workflowEvent("gate-impact-"+workID, WorkflowImpactDeclared, workID, map[string]any{"work_id": workID, "expected_version": version, "resulting_version": version + 1, "edge_id": "edge:" + workID, "edge_kind": "modifies", "edge_class": "none", "target_work_id": workID, "target_kind": "work_item", "severity": "breaking"}))
	if testCase.openCondition {
		seedWorkflowAuthority(t, s, "gate-condition-"+workID, workID, "principal/condition", "request/condition", []string{"evidence:condition"})
		appendEvent(workflowEvent("gate-condition-"+workID, WorkflowConditionAdded, workID, map[string]any{"work_id": workID, "expected_version": version, "resulting_version": version + 1, "condition_id": "condition:gate", "await_type": "ci_result", "await_ref": "run:gate", "resolution_authority": "durable_operation:gate-condition-" + workID}))
	}
	seedWorkflowAuthority(t, s, "gate-verification-"+workID, workID, "principal/verify", "request/verify", []string{"evidence:verification"})
	seedWorkflowAuthority(t, s, "gate-review-"+workID, workID, "principal/review", "request/review", []string{"evidence:review"})
	appendEvent(workflowEvent("gate-verification-"+workID, WorkflowEvidenceBound, workID, map[string]any{"work_id": workID, "expected_version": version, "resulting_version": version + 1, "evidence_kind": "verification", "immutable_subject_ref": "evidence:verification", "producer_id": "principal/verify", "producer_run_ref": "gate-verification-" + workID, "producer_watermark": "request/verify", "observed_at": "2026-08-09T00:00:00Z"}))
	appendEvent(workflowEvent("gate-review-"+workID, WorkflowEvidenceBound, workID, map[string]any{"work_id": workID, "expected_version": version, "resulting_version": version + 1, "evidence_kind": "review", "immutable_subject_ref": "evidence:review", "producer_id": "principal/review", "producer_run_ref": "gate-review-" + workID, "producer_watermark": "request/review", "observed_at": "2026-08-09T00:00:00Z"}))
	if testCase.includeSpec {
		seedWorkflowAuthority(t, s, "gate-spec-"+workID, workID, "principal/spec", "request/spec", []string{"spec:one"})
		appendEvent(workflowEvent("gate-spec-"+workID, WorkflowEvidenceBound, workID, map[string]any{"work_id": workID, "expected_version": version, "resulting_version": version + 1, "evidence_kind": "artifact", "immutable_subject_ref": "spec:one", "producer_id": "principal/spec", "producer_run_ref": "gate-spec-" + workID, "producer_watermark": "request/spec", "observed_at": "2026-08-09T00:00:00Z"}))
	}
	if testCase.includeVerdict {
		appendEvent(workflowEventWithActor("gate-verdict-"+workID, WorkflowVerdictRecorded, workID, operator, map[string]any{"work_id": workID, "expected_version": version, "resulting_version": version + 1, "contract_version": 1, "predicate_id": "predicate:gate", "verdict_kind": testCase.verdictKind, "verdict_actor_ref": operator, "evaluation_evidence": []string{"evidence:verification"}, "incomparable_with_approved": testCase.verdictKind != "ok"}))
	}
	if testCase.includePremise {
		appendEvent(workflowEventWithActor("gate-premise-"+workID, WorkflowPremiseConfirmed, workID, operator, map[string]any{"work_id": workID, "expected_version": version, "resulting_version": version + 1, "contract_version": 1, "confirming_actor_ref": operator}))
	}
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: events, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): 2}}); err != nil {
		t.Fatal(err)
	}
	if testCase.conflictNotice {
		noticeID := WorkflowNoticeID(workID, 1, "spec", "spec:one", workID, "breaking")
		if _, err := s.DB().Exec(`INSERT INTO domain_events(event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload) VALUES(?,?,?,?,?,?,?,?)`, "notice-event:"+noticeID, "work.intent_revised", string(SubjectWorkItem), workID, executor, "2026-08-09T00:00:00Z", 1, `{}`); err != nil {
			t.Fatal(err)
		}
	}
	completion := workflowEventWithActor("gate-completion-"+workID, WorkflowCompleted, workID, executor, map[string]any{"work_id": workID, "expected_version": version, "resulting_version": version + 1, "terminal_state": "completed", "final_verdict_kind": "ok", "verdict_actor_ref": operator, "premise_confirmed": testCase.includePremise, "evidence_count": 3, "changed_refs_digest": "sha256:" + strings.Repeat("a", 64)})
	return s, completion
}

func TestWorkflowEvidenceBindingRejectsCrossWorkAndProducerIdentityLaundering(t *testing.T) {
	s := openTemp(t)
	seedWork(t, s, "evidence-work-a")
	seedWork(t, s, "evidence-work-b")
	seedWorkflowAuthority(t, s, "evidence-cross-work", "evidence-work-b", "principal/actual", "request/actual", []string{"evidence:shared"})
	crossWork := workflowEvent("evidence-cross-work-event", WorkflowEvidenceBound, "evidence-work-a", map[string]any{
		"work_id": "evidence-work-a", "expected_version": 2, "resulting_version": 3, "evidence_kind": "verification", "immutable_subject_ref": "evidence:shared", "producer_id": "principal/actual", "producer_run_ref": "evidence-cross-work", "producer_watermark": "request/actual", "observed_at": "2026-08-09T00:00:00Z",
	})
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{crossWork}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "evidence-work-a"): 2}}); err == nil {
		t.Fatal("evidence from another work item was accepted")
	}
	seedWorkflowAuthority(t, s, "evidence-identity", "evidence-work-a", "principal/actual", "request/actual", []string{"evidence:identity"})
	mismatchedIdentity := workflowEvent("evidence-identity-event", WorkflowEvidenceBound, "evidence-work-a", map[string]any{
		"work_id": "evidence-work-a", "expected_version": 2, "resulting_version": 3, "evidence_kind": "verification", "immutable_subject_ref": "evidence:identity", "producer_id": "principal/forged", "producer_run_ref": "evidence-identity", "producer_watermark": "request/actual", "observed_at": "2026-08-09T00:00:00Z",
	})
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{mismatchedIdentity}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "evidence-work-a"): 2}}); err == nil {
		t.Fatal("evidence with mismatched producer identity was accepted")
	}
}

func TestWorkflowCancellationRejectsForgedEvidenceFromOperator(t *testing.T) {
	s := openTemp(t)
	seedWork(t, s, "cancel-evidence-work")
	seedWorkflowAuthority(t, s, "cancel-authority", "cancel-evidence-work", "principal/provider", "request/provider", []string{"evidence:real"})
	added := workflowEvent("cancel-condition-added", WorkflowConditionAdded, "cancel-evidence-work", map[string]any{
		"work_id": "cancel-evidence-work", "expected_version": 2, "resulting_version": 3, "condition_id": "condition:cancel", "await_type": "human_approval", "await_ref": "approval:pending", "resolution_authority": "durable_operation:cancel-authority",
	})
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{added}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "cancel-evidence-work"): 2}}); err != nil {
		t.Fatal(err)
	}
	operator := DeriveWorkflowActorRef("principal/operator", "client/concord-1", "agent/operator", "session/cancel")
	actor := workflowEvent("cancel-operator", WorkflowActorRecorded, "cancel-evidence-work", map[string]any{
		"work_id": "cancel-evidence-work", "expected_version": 3, "resulting_version": 4, "actor_ref": operator, "principal_ref": "principal/operator", "client_ref": "client/concord-1", "agent_ref": "agent/operator", "session_ref": "session/cancel", "actor_class": "operator",
	})
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{actor}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "cancel-evidence-work"): 3}}); err != nil {
		t.Fatal(err)
	}
	cancel := workflowEventWithActor("cancel-forged", WorkflowConditionCancelled, "cancel-evidence-work", operator, map[string]any{
		"work_id": "cancel-evidence-work", "expected_version": 4, "resulting_version": 5, "condition_id": "condition:cancel", "cancellation_authority": "operator", "cancellation_evidence": []string{"evidence:forged"}, "cancelled_by_event": "event:forged",
	})
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{cancel}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "cancel-evidence-work"): 4}}); err == nil {
		t.Fatal("operator forged cancellation evidence was accepted")
	}
	var state string
	if err := s.DB().QueryRow(`SELECT condition_state FROM workflow_external_conditions WHERE work_id='cancel-evidence-work' AND condition_id='condition:cancel'`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "open" {
		t.Fatalf("condition state = %q, want open", state)
	}
}

func seedWorkflowAuthority(t *testing.T, s *Store, opID, workID, principal, requestID string, evidence []string) {
	t.Helper()
	_, err := s.DB().Exec(`INSERT INTO durable_operations(op_id,attempt_epoch,work_id,workflow_type_ref,workflow_type_version,step_id,step_kind,accepted_inputs_digest,accepted_scope_snapshot,result_kind,result_payload,evidence_refs,changed_refs,principal_ref,request_id,observed_at,completed_at) VALUES(?,1,?,'workflow.test',1,'evidence','internal_sqlite','digest','{}','completed','{}',?,'[]',?,?,?,?)`, opID, workID, workflowJSON(evidence), principal, requestID, "2026-08-09T00:00:00Z", "2026-08-09T00:00:01Z")
	if err != nil {
		t.Fatalf("seed durable workflow authority: %v", err)
	}
}

func TestMigrateV14ToV15PreservesExistingData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v14.db")
	ctx := context.Background()
	db, err := sql.Open(driverName, dataSourceName(path))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()
	if _, err := db.ExecContext(ctx, schemaManifestDDL); err != nil {
		t.Fatal(err)
	}
	for _, migration := range migrations[:14] {
		if _, err := db.ExecContext(ctx, migration.SQL); err != nil {
			t.Fatalf("migration %d: %v", migration.Version, err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO schema_migrations(version,name,checksum,applied_at) VALUES(?,?,?,?)`, migration.Version, migration.Name, migration.checksum(), "2026-08-08T00:00:00Z"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO fold_guard(active) VALUES(1);
INSERT INTO products(id,display_name,stage_maturity,stage_audience_commitment,version,created_at,updated_at) VALUES('v14-product','V14','prototype','operator_only',1,'created','updated');
INSERT INTO projects(id,display_name,version,created_at,updated_at) VALUES('v14-project','V14 project',1,'created','updated');
INSERT INTO work_items(id,kind,title,lifecycle,priority,version,created_at,updated_at) VALUES('v14-work','task','V14 work','needed',1,2,'created','updated'),('v14-work-2','task','V14 work 2','needed',1,1,'created','updated');
INSERT INTO relations(work_id_from,work_id_to,kind,created_at) VALUES('v14-work','v14-work-2','blocks','created');
INSERT INTO domain_events(event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload) VALUES('v14-event','work.created','work_item','v14-work','operator','2026-08-08T00:00:00Z',2,'{"work_kind":"task","title":"V14 work","priority":1}');
DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	var name string
	if err := db.QueryRowContext(ctx, `SELECT display_name FROM products WHERE id='v14-product'`).Scan(&name); err != nil || name != "V14" {
		t.Fatalf("preserved v14 product = %q, error=%v", name, err)
	}
	var workTitle, relationKind, eventPayload string
	if err := db.QueryRowContext(ctx, `SELECT title FROM work_items WHERE id='v14-work'`).Scan(&workTitle); err != nil || workTitle != "V14 work" {
		t.Fatalf("preserved v14 work = %q, error=%v", workTitle, err)
	}
	if err := db.QueryRowContext(ctx, `SELECT kind FROM relations WHERE work_id_from='v14-work' AND work_id_to='v14-work-2'`).Scan(&relationKind); err != nil || relationKind != "blocks" {
		t.Fatalf("preserved v14 relation = %q, error=%v", relationKind, err)
	}
	if err := db.QueryRowContext(ctx, `SELECT payload FROM domain_events WHERE event_id='v14-event'`).Scan(&eventPayload); err != nil || eventPayload != `{"work_kind":"task","title":"V14 work","priority":1}` {
		t.Fatalf("preserved v14 event = %q, error=%v", eventPayload, err)
	}
	if got, err := SchemaVersion(ctx, db); err != nil || got != CurrentSchemaVersion() {
		t.Fatalf("schema version = %d, error=%v, want %d", got, err, CurrentSchemaVersion())
	}
}

func TestWorkflowProjectionSchemaHasClosedChecksForeignKeysAndFoldGuards(t *testing.T) {
	s := openTemp(t)
	expectedColumns := map[string][]string{
		"workflow_instances":             {"work_id", "definition_ref", "definition_version", "definition_digest", "current_step", "instance_state", "execution_actor_ref", "started_at", "completed_at", "last_checkpoint_at", "execution_model"},
		"workflow_contracts":             {"work_id", "contract_version", "premise", "outcome_kind", "outcome_payload", "consequence_class", "required_evidence", "route_conventions", "approved_at", "approved_by", "superseded_by", "spec_mandate", "rigor_class", "law_modifies", "law_boundary_version"},
		"workflow_candidate_sets":        {"work_id", "contract_version", "candidate_kind", "candidate_ref", "candidate_role", "candidate_scope", "recorded_at", "recorded_by"},
		"workflow_actors":                {"actor_ref", "principal_ref", "client_ref", "agent_ref", "session_ref", "actor_class", "first_seen_at"},
		"workflow_checkpoints":           {"work_id", "checkpoint_id", "step_id", "step_kind", "attempt_epoch", "accepted_inputs_digest", "result_evidence_refs", "resume_cursor", "idempotency_identity", "actor_ref", "request_id", "recorded_at"},
		"workflow_external_conditions":   {"work_id", "condition_id", "await_type", "await_ref", "resolution_authority", "condition_state", "resolution_evidence", "resolved_by_event", "cancellation_authority", "cancellation_evidence", "cancelled_by_event", "recorded_at", "resolved_at", "cancelled_at"},
		"workflow_impact_edges":          {"work_id", "edge_id", "edge_kind", "edge_class", "target_work_id", "target_kind", "severity", "recorded_at"},
		"workflow_impact_notices":        {"notice_id", "source_work_id", "source_contract_version", "entity_kind", "entity_ref", "target_work_id", "edge_id", "old_hash", "new_hash", "severity", "recorded_at"},
		"workflow_decision_records":      {"work_id", "question", "options_considered", "decision", "rationale", "consequences", "inputs", "poc_findings", "supersedes", "superseded_by", "recorded_at"},
		"workflow_premise_confirmations": {"work_id", "contract_version", "confirmed_by", "confirmed_at"},
	}
	expectedForeignKeys := map[string][]string{
		"workflow_instances": {"work_items", "workflow_actors"}, "workflow_contracts": {"work_items", "workflow_actors", "workflow_contracts", "workflow_contracts"},
		"workflow_candidate_sets": {"workflow_contracts", "workflow_contracts", "workflow_actors"}, "workflow_actors": {}, "workflow_checkpoints": {"work_items", "workflow_actors"},
		"workflow_external_conditions": {"work_items"}, "workflow_impact_edges": {"work_items", "work_items"}, "workflow_impact_notices": {"work_items", "work_items", "workflow_impact_edges", "workflow_impact_edges"},
		"workflow_decision_records": {"work_items"}, "workflow_premise_confirmations": {"workflow_contracts", "workflow_contracts", "workflow_actors"},
	}
	expectedUniqueKeys := map[string][][]string{
		"workflow_instances": {{"work_id"}}, "workflow_contracts": {{"work_id", "contract_version"}}, "workflow_candidate_sets": {{"work_id", "contract_version", "candidate_kind", "candidate_ref"}},
		"workflow_actors": {{"actor_ref"}, {"principal_ref", "client_ref", "agent_ref", "session_ref"}}, "workflow_checkpoints": {{"work_id", "checkpoint_id"}, {"work_id", "step_id", "attempt_epoch"}, {"work_id", "idempotency_identity"}},
		"workflow_external_conditions": {{"work_id", "condition_id"}}, "workflow_impact_edges": {{"work_id", "edge_id"}}, "workflow_impact_notices": {{"notice_id"}, {"source_work_id", "source_contract_version", "entity_kind", "entity_ref", "target_work_id", "severity"}},
		"workflow_decision_records": {{"work_id", "question"}}, "workflow_premise_confirmations": {{"work_id", "contract_version"}},
	}
	enumFragments := map[string][]string{
		"workflow_instances": {"'planned'", "'superseded'"}, "workflow_contracts": {"'exists'", "'external_effect'"},
		"workflow_candidate_sets": {"'work_item'", "candidate_role IN ('include')"}, "workflow_actors": {"'agent'", "'operator'"},
		"workflow_checkpoints": {"'human_checkpoint'"}, "workflow_external_conditions": {"'pr_merge'", "'cancelled'"},
		"workflow_impact_edges": {"'depends_on'", "'work_item'"}, "workflow_impact_notices": {"'breaking'"},
		"workflow_decision_records": {"'accepted_decision'", "'insufficient_evidence'"}, "workflow_premise_confirmations": {},
	}
	for table, columns := range expectedColumns {
		t.Run(table, func(t *testing.T) {
			assertWorkflowColumns(t, s, table, columns)
			assertWorkflowForeignKeys(t, s, table, expectedForeignKeys[table])
			for _, key := range expectedUniqueKeys[table] {
				assertWorkflowUniqueKey(t, s, table, key)
			}
			var ddl string
			if err := s.DB().QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&ddl); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(ddl, "CHECK") && table != "workflow_actors" {
				t.Fatalf("%s lacks CHECK constraints", table)
			}
			for _, fragment := range enumFragments[table] {
				if !strings.Contains(ddl, fragment) {
					t.Errorf("%s missing exact enum fragment %q", table, fragment)
				}
			}
			for _, action := range []string{"insert", "update", "delete"} {
				var count int
				if err := s.DB().QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='trigger' AND name=?`, table+"_guard_"+action).Scan(&count); err != nil {
					t.Fatal(err)
				}
				if count != 1 {
					t.Errorf("missing %s guard trigger", action)
				}
			}
		})
	}
}

func TestWorkflowImpactTargetKindRejectsProductAndProject(t *testing.T) {
	s := openTemp(t)
	seedWork(t, s, "target-kind-work")
	for _, targetKind := range []string{"product", "project"} {
		t.Run(targetKind, func(t *testing.T) {
			tx, err := s.DB().BeginTx(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := enterFold(context.Background(), tx); err != nil {
				tx.Rollback()
				t.Fatal(err)
			}
			_, err = tx.Exec(`INSERT INTO workflow_impact_edges(work_id,edge_id,edge_kind,edge_class,target_work_id,target_kind,severity,recorded_at) VALUES('target-kind-work',?,?,?,'target-kind-work',?,'informational','now')`, "edge:"+targetKind, "modifies", "none", targetKind)
			if err == nil {
				tx.Rollback()
				t.Fatalf("target_kind=%s was accepted", targetKind)
			}
			if rollbackErr := tx.Rollback(); rollbackErr != nil {
				t.Fatal(rollbackErr)
			}
		})
	}
}

func assertWorkflowUniqueKey(t *testing.T, s *Store, table string, want []string) {
	t.Helper()
	rows, err := s.DB().Query(`PRAGMA index_list(` + table + `)`)
	if err != nil {
		t.Fatal(err)
	}
	var indexes []string
	for rows.Next() {
		var seq, unique, partial int
		var name, origin string
		if err := rows.Scan(&seq, &name, &unique, &origin, &partial); err != nil {
			t.Fatal(err)
		}
		if unique == 1 {
			indexes = append(indexes, name)
		}
	}
	rows.Close()
	for _, name := range indexes {
		info, err := s.DB().Query(`PRAGMA index_info(` + name + `)`)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for info.Next() {
			var seqno, cid int
			var column string
			if err := info.Scan(&seqno, &cid, &column); err != nil {
				info.Close()
				t.Fatal(err)
			}
			got = append(got, column)
		}
		info.Close()
		if strings.Join(got, "|") == strings.Join(want, "|") {
			return
		}
	}
	t.Fatalf("%s lacks unique key %v", table, want)
}

func assertWorkflowColumns(t *testing.T, s *Store, table string, want []string) {
	t.Helper()
	rows, err := s.DB().Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull, pk int
		var defaultValue any
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			t.Fatal(err)
		}
		got = append(got, name)
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("columns = %v, want %v", got, want)
	}
}

func assertWorkflowForeignKeys(t *testing.T, s *Store, table string, want []string) {
	t.Helper()
	rows, err := s.DB().Query(`PRAGMA foreign_key_list(` + table + `)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var id, seq int
		var ref, from, to, onUpdate, onDelete, match string
		if err := rows.Scan(&id, &seq, &ref, &from, &to, &onUpdate, &onDelete, &match); err != nil {
			t.Fatal(err)
		}
		got = append(got, ref)
	}
	sort.Strings(got)
	sort.Strings(want)
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("foreign keys = %v, want %v", got, want)
	}
}

func TestConcurrentWorkflowActorAppendsUseTheSingleWriter(t *testing.T) {
	s := openTemp(t)
	for i := 0; i < 8; i++ {
		seedWork(t, s, fmt.Sprintf("concurrent-work-%d", i))
	}
	var wg sync.WaitGroup
	errors := make(chan error, 8)
	for i := 0; i < 8; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			workID := fmt.Sprintf("concurrent-work-%d", i)
			actor := DeriveWorkflowActorRef("principal/operator", "client/concord-1", fmt.Sprintf("agent/%d", i), fmt.Sprintf("session/%d", i))
			event := workflowEvent("concurrent-actor-"+fmt.Sprint(i), WorkflowActorRecorded, workID, map[string]any{
				"work_id": workID, "expected_version": 2, "resulting_version": 3, "actor_ref": actor,
				"principal_ref": "principal/operator", "client_ref": "client/concord-1", "agent_ref": fmt.Sprintf("agent/%d", i), "session_ref": fmt.Sprintf("session/%d", i), "actor_class": "agent",
			})
			errors <- applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): 2}})
		}()
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatalf("concurrent workflow append: %v", err)
		}
	}
	var count int
	if err := s.DB().QueryRow(`SELECT count(*) FROM workflow_actors`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 8 {
		t.Fatalf("workflow actor rows = %d, want 8", count)
	}
}

func TestWorkflowGoverningCycleCombinesDependsOnAndForwardLink(t *testing.T) {
	s := openTemp(t)
	seedWork(t, s, "mixed-a")
	seedWork(t, s, "mixed-b")
	first := workflowEvent("mixed-depends", WorkflowImpactDeclared, "mixed-a", map[string]any{
		"work_id": "mixed-a", "expected_version": 2, "resulting_version": 3, "edge_id": "edge-depends", "edge_kind": "depends_on", "edge_class": "hard", "target_work_id": "mixed-b", "target_kind": "work_item", "severity": "non-breaking",
	})
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{first}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "mixed-a"): 2}}); err != nil {
		t.Fatal(err)
	}
	second := workflowEvent("mixed-forward", WorkflowImpactDeclared, "mixed-b", map[string]any{
		"work_id": "mixed-b", "expected_version": 2, "resulting_version": 3, "edge_id": "edge-forward", "edge_kind": "forward_link", "edge_class": "hard", "target_work_id": "mixed-a", "target_kind": "work_item", "severity": "informational",
	})
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{second}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "mixed-b"): 2}}); err == nil {
		t.Fatal("mixed depends_on/forward_link cycle was accepted")
	} else {
		assertFailureKind(t, err, KindCycleDetected)
	}
	assertTableCount(t, s, "workflow_impact_edges", 1)
}

func TestWorkflowPoisonEventLeavesAllProjectionsAtomic(t *testing.T) {
	s := openTemp(t)
	seedWork(t, s, "poison-work")
	actor := DeriveWorkflowActorRef("principal/operator", "client/concord-1", "agent/runner", "session/poison")
	event := workflowEvent("poison-actor", WorkflowActorRecorded, "poison-work", map[string]any{
		"work_id": "poison-work", "expected_version": 2, "resulting_version": 3, "actor_ref": actor,
		"principal_ref": "principal/operator", "client_ref": "client/concord-1", "agent_ref": "agent/runner", "session_ref": "session/poison", "actor_class": "agent",
	})
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "poison-work"): 2}}); err != nil {
		t.Fatal(err)
	}
	before := fullWorkflowProjectionSnapshot(t, s)
	poison := workflowEvent("poison-v2", WorkflowActorRecorded, "poison-work", map[string]any{"work_id": "poison-work", "expected_version": 3, "resulting_version": 4})
	poison.PayloadVersion = 2
	if _, err := s.DB().Exec(`INSERT INTO domain_events(event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload) VALUES(?,?,?,?,?,?,?,?)`, poison.EventID, poison.Kind, poison.SubjectType, poison.SubjectID, poison.Actor, poison.OccurredAt.UTC().Format(time.RFC3339Nano), poison.PayloadVersion, string(poison.Payload)); err != nil {
		t.Fatal(err)
	}
	if err := RebuildFromLog(context.Background(), s); err == nil {
		t.Fatal("poison workflow event rebuilt successfully")
	}
	after := fullWorkflowProjectionSnapshot(t, s)
	if after != before {
		t.Fatalf("poison rebuild changed projections:\nbefore=%s\nafter=%s", before, after)
	}
}

func fullWorkflowProjectionSnapshot(t *testing.T, s *Store) string {
	t.Helper()
	tables := []string{
		"work_items", "relations", "workflow_instances", "workflow_contracts", "workflow_candidate_sets", "workflow_actors",
		"workflow_checkpoints", "workflow_external_conditions", "workflow_impact_edges", "workflow_impact_notices",
		"workflow_decision_records", "workflow_premise_confirmations",
	}
	var snapshot strings.Builder
	for _, table := range tables {
		var columnCount int
		if err := s.DB().QueryRow(`SELECT count(*) FROM pragma_table_info(?)`, table).Scan(&columnCount); err != nil {
			t.Fatalf("columns for %s: %v", table, err)
		}
		order := make([]string, columnCount)
		for i := range order {
			order[i] = fmt.Sprintf("%d", i+1)
		}
		rows, err := s.DB().Query(`SELECT * FROM ` + table + ` ORDER BY ` + strings.Join(order, ","))
		if err != nil {
			t.Fatalf("snapshot %s: %v", table, err)
		}
		columns, err := rows.Columns()
		if err != nil {
			rows.Close()
			t.Fatalf("columns for %s: %v", table, err)
		}
		for rows.Next() {
			values := make([]any, len(columns))
			dest := make([]any, len(columns))
			for i := range values {
				dest[i] = &values[i]
			}
			if err := rows.Scan(dest...); err != nil {
				rows.Close()
				t.Fatalf("scan %s: %v", table, err)
			}
			snapshot.WriteString(table)
			for _, value := range values {
				snapshot.WriteByte('|')
				switch typed := value.(type) {
				case nil:
					snapshot.WriteString("NULL")
				case []byte:
					snapshot.WriteString(fmt.Sprintf("bytes:%x", typed))
				default:
					snapshot.WriteString(fmt.Sprintf("%T:%v", typed, typed))
				}
			}
			snapshot.WriteByte('\n')
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			t.Fatalf("iterate %s: %v", table, err)
		}
		rows.Close()
	}
	return snapshot.String()
}

func workflowEvent(id, kind, workID string, payload map[string]any) Event {
	event := operationEvent(id, kind, SubjectWorkItem, workID, payload)
	event.Actor = "actor:test"
	return event
}

func workflowEventWithActor(id, kind, workID, actor string, payload map[string]any) Event {
	event := workflowEvent(id, kind, workID, payload)
	event.Actor = actor
	return event
}

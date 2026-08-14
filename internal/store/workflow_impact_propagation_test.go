package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestWorkflowCompletedV1UpcastsToNonBreakingImpactVerdict(t *testing.T) {
	event := workflowEvent("legacy-completion", WorkflowCompleted, "legacy-work", map[string]any{
		"work_id": "legacy-work", "expected_version": 2, "resulting_version": 3,
		"terminal_state": "completed", "final_verdict_kind": "ok",
		"verdict_actor_ref": "actor:" + strings.Repeat("a", 64), "premise_confirmed": true,
		"evidence_count": 1, "changed_refs_digest": "sha256:" + strings.Repeat("b", 64),
	})
	upcasted, err := upcastEvent(event)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(upcasted.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if upcasted.PayloadVersion != 2 || payload["impact_verdict"] != "non-breaking" {
		t.Fatalf("upcasted completion = version %d payload %#v", upcasted.PayloadVersion, payload)
	}
}

func TestWorkflowImpactNoticeV1UpcastsEdgeOwnerToLegacySource(t *testing.T) {
	event := workflowEvent("legacy-notice", WorkflowImpactNoticeRecorded, "legacy-source", map[string]any{
		"work_id": "legacy-source", "expected_version": 2, "resulting_version": 3,
		"notice_id":               WorkflowNoticeID("legacy-source", 1, "spec", "spec:one", "legacy-target", "breaking"),
		"source_contract_version": 1, "entity_kind": "spec", "entity_ref": "spec:one",
		"target_work_id": "legacy-target", "edge_id": "edge:legacy", "old_hash": nil,
		"new_hash": nil, "severity": "breaking",
	})
	upcasted, err := upcastEvent(event)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(upcasted.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if upcasted.PayloadVersion != 2 || payload["edge_owner_work_id"] != "legacy-source" {
		t.Fatalf("upcasted notice = version %d payload %#v", upcasted.PayloadVersion, payload)
	}
}

func TestWorkflowCompletionPropagatesReverseDependentsAndBoundaryUsesEdgeClass(t *testing.T) {
	for _, testCase := range []struct {
		name          string
		edgeClass     string
		impactVerdict string
		wantBlocked   bool
	}{
		{name: "hard breaking blocks", edgeClass: "hard", impactVerdict: "breaking", wantBlocked: true},
		{name: "soft breaking warns", edgeClass: "soft", impactVerdict: "breaking", wantBlocked: false},
		{name: "hard non-breaking warns", edgeClass: "hard", impactVerdict: "non-breaking", wantBlocked: false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			sourceID := "impact-source-" + strings.ReplaceAll(testCase.name, " ", "-")
			dependentID := "impact-dependent-" + strings.ReplaceAll(testCase.name, " ", "-")
			s, completion := seedCompletionGateCase(t, sourceID, completionGateCase{
				requiredEvidence: []string{"verification", "review"}, emptyMandate: true, omitImpact: true,
				includeVerdict: true, includePremise: true, verdictKind: "ok",
			})
			actor := seedImpactDependent(t, s, dependentID, sourceID, testCase.edgeClass)
			completion = withImpactVerdict(t, completion, testCase.impactVerdict)
			if err := CompleteWorkflow(context.Background(), s, completion); err != nil {
				t.Fatal(err)
			}

			var source, target, edgeOwner, edgeID, entityKind, entityRef, severity string
			if err := s.DB().QueryRow(`SELECT source_work_id,target_work_id,edge_owner_work_id,edge_id,entity_kind,entity_ref,severity FROM workflow_impact_notices`).Scan(&source, &target, &edgeOwner, &edgeID, &entityKind, &entityRef, &severity); err != nil {
				t.Fatal(err)
			}
			if source != sourceID || target != dependentID || edgeOwner != dependentID || edgeID != "edge:"+dependentID || entityKind != "work_item" || entityRef != sourceID || severity != testCase.impactVerdict {
				t.Fatalf("notice = %s/%s/%s/%s/%s/%s/%s", source, target, edgeOwner, edgeID, entityKind, entityRef, severity)
			}

			var version int64
			if err := s.DB().QueryRow(`SELECT version FROM work_items WHERE id=?`, dependentID).Scan(&version); err != nil {
				t.Fatal(err)
			}
			if err := WorkflowActionPreflight(context.Background(), s, WorkflowActionPreflightRequest{WorkID: dependentID, ExpectedVersion: version, StepID: "execution", ActionID: "bind_evidence", Payload: json.RawMessage(`{}`), Actor: actor}); err != nil {
				t.Fatalf("non-consequential action was blocked: %v", err)
			}
			err := WorkflowActionPreflight(context.Background(), s, WorkflowActionPreflightRequest{WorkID: dependentID, ExpectedVersion: version, StepID: "execution", ActionID: "start_execution", Payload: json.RawMessage(`{}`), Actor: actor})
			if testCase.wantBlocked {
				assertFailureKind(t, err, KindInvariantViolation)
			} else if err != nil {
				t.Fatalf("advisory impact blocked dependent: %v", err)
			}
		})
	}
}

func TestWorkflowCompletionChoosesHardEdgeWhenDependentDeclaresMultipleEdges(t *testing.T) {
	sourceID := "impact-multi-source"
	dependentID := "impact-multi-dependent"
	s, completion := seedCompletionGateCase(t, sourceID, completionGateCase{
		requiredEvidence: []string{"verification", "review"}, emptyMandate: true, omitImpact: true,
		includeVerdict: true, includePremise: true, verdictKind: "ok",
	})
	actor := seedImpactDependent(t, s, dependentID, sourceID, "soft")
	var version int64
	if err := s.DB().QueryRow(`SELECT version FROM work_items WHERE id=?`, dependentID).Scan(&version); err != nil {
		t.Fatal(err)
	}
	actorRef, _ := WorkflowActorRef(actor)
	hard := workflowEventWithActor("impact-multi-hard", WorkflowImpactDeclared, dependentID, actorRef, map[string]any{"work_id": dependentID, "expected_version": version, "resulting_version": version + 1, "edge_id": "edge:zz-hard", "edge_kind": "depends_on", "edge_class": "hard", "target_work_id": sourceID, "target_kind": "work_item", "severity": "informational"})
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{hard}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, dependentID): version}}); err != nil {
		t.Fatal(err)
	}
	completion = withImpactVerdict(t, completion, "breaking")
	if err := CompleteWorkflow(context.Background(), s, completion); err != nil {
		t.Fatal(err)
	}
	var edgeID string
	if err := s.DB().QueryRow(`SELECT edge_id FROM workflow_impact_notices WHERE source_work_id=? AND target_work_id=?`, sourceID, dependentID).Scan(&edgeID); err != nil {
		t.Fatal(err)
	}
	if edgeID != "edge:zz-hard" {
		t.Fatalf("selected edge = %q, want hard edge", edgeID)
	}
	if err := s.DB().QueryRow(`SELECT version FROM work_items WHERE id=?`, dependentID).Scan(&version); err != nil {
		t.Fatal(err)
	}
	assertFailureKind(t, WorkflowActionPreflight(context.Background(), s, WorkflowActionPreflightRequest{WorkID: dependentID, ExpectedVersion: version, StepID: "execution", ActionID: "start_execution", Payload: json.RawMessage(`{}`), Actor: actor}), KindInvariantViolation)
}

func seedImpactDependent(t *testing.T, s *Store, dependentID, sourceID, edgeClass string) WorkflowActor {
	t.Helper()
	seedWork(t, s, dependentID)
	actor := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/dependent", SessionRef: "session/" + dependentID, ActorClass: ActorAgent}
	actorRef, err := WorkflowActorRef(actor)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := WorkflowDefinitionDigest(BuiltinWorkflowDefinitions()[0])
	if err != nil {
		t.Fatal(err)
	}
	events := []Event{
		workflowEvent("actor-"+dependentID, WorkflowActorRecorded, dependentID, map[string]any{"work_id": dependentID, "expected_version": 2, "resulting_version": 3, "actor_ref": actorRef, "principal_ref": actor.PrincipalRef, "client_ref": actor.ClientRef, "agent_ref": actor.AgentRef, "session_ref": actor.SessionRef, "actor_class": "agent"}),
		workflowEvent("definition-"+dependentID, WorkflowDefinitionSelected, dependentID, map[string]any{"work_id": dependentID, "expected_version": 3, "resulting_version": 4, "ref": "workflow.implementation", "version": 1, "digest": digest, "work_kind": "implementation"}),
		workflowEventWithActor("contract-"+dependentID, WorkflowContractApproved, dependentID, actorRef, map[string]any{"work_id": dependentID, "expected_version": 4, "resulting_version": 5, "contract_version": 1, "premise": "depend on source", "outcome_kind": "check", "outcome_payload": map[string]any{"kind": "check", "check_ref": "check:dependent", "immutable_subject_ref": "commit:" + dependentID, "expected_result": "pass"}, "required_evidence": []string{"verification", "review"}, "route_conventions": []string{}, "spec_mandate": []string{}, "rigor_class": "prototype/internal", "consequence_class": "internal_sqlite"}),
		workflowEventWithActor("start-"+dependentID, WorkflowActionStarted, dependentID, actorRef, map[string]any{"work_id": dependentID, "expected_version": 5, "resulting_version": 6, "step_id": "execution", "action_id": "start_execution", "attempt_epoch": 1, "accepted_inputs_digest": "sha256:" + strings.Repeat("b", 64), "idempotency_identity": "operation-" + dependentID, "actor_ref": actorRef}),
		workflowEventWithActor("impact-"+dependentID, WorkflowImpactDeclared, dependentID, actorRef, map[string]any{"work_id": dependentID, "expected_version": 6, "resulting_version": 7, "edge_id": "edge:" + dependentID, "edge_kind": "depends_on", "edge_class": edgeClass, "target_work_id": sourceID, "target_kind": "work_item", "severity": "informational"}),
	}
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: events, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, dependentID): 2}}); err != nil {
		t.Fatal(err)
	}
	return actor
}

func withImpactVerdict(t *testing.T, event Event, verdict string) Event {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	payload["impact_verdict"] = verdict
	event.Payload, _ = json.Marshal(payload)
	event.PayloadVersion = 2
	return event
}

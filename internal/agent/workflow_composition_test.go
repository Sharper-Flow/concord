package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/store"
)

func TestAgentWorkflowCompositionUsesSelectedSuccessorFamily(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, service, grant, _ := mutationDispatchFixture(t, []agentcontract.Capability{"work_define", "work_transition"})
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	sourceID := captureCompositionWork(t, ctx, s, service, env, "Composition source", "task", "", "composition-source")
	successorID := captureCompositionWork(t, ctx, s, service, env, "Composition successor", "research", "", "composition-successor")
	// A capture always pins a workflow instance now (#650), so the
	// instance-less successor this refusal guards is created the way real
	// instance-less work arises: raw events, as previously imported work carries.
	missingInstanceID := "work-composition-missing-instance"
	if err := store.ApplyOperation(ctx, s, store.Operation{Events: []store.Event{
		{EventID: "composition-missing:create", Kind: "work.created", SubjectType: store.SubjectWorkItem, SubjectID: missingInstanceID, Actor: "operator", OccurredAt: s.Clock().UTC(), PayloadVersion: 4, Payload: []byte(`{"work_kind":"research","title":"Composition missing instance"}`)},
		{EventID: "composition-missing:memberships", Kind: "work.memberships_replaced", SubjectType: store.SubjectWorkItem, SubjectID: missingInstanceID, Actor: "operator", OccurredAt: s.Clock().UTC(), PayloadVersion: 1, Payload: []byte(`{"memberships":[{"project_id":"project-1","role":"primary"}],"expected_version":1,"resulting_version":2}`)},
	}, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectWorkItem, missingInstanceID): 0}}); err != nil {
		t.Fatal(err)
	}
	// link_successor is declared from the implement step onward: drive the
	// source through intake, type pick, plan record and the plan step, and
	// give the successor its picked type.
	compositionPrelude := func(workID, planType string) {
		t.Helper()
		for _, step := range []struct {
			action string
			fields map[string]any
		}{
			{"record_intake", map[string]any{"outcome": "proceed"}},
			{"pick_type", map[string]any{"type": planType, "reason": "Composition fixture"}},
		} {
			raw, err := json.Marshal(map[string]any{"work_id": workID, "expected_version": agentCompositionWorkVersion(t, s, workID), "action_id": step.action, "fields": step.fields, "idempotency_key": "composition-" + workID + "-" + step.action})
			if err != nil {
				t.Fatal(err)
			}
			response, err := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: raw}, env)
			if err != nil || response.Outcome != agentcontract.OutcomeOK {
				t.Fatalf("%s %s: response=%+v err=%v", workID, step.action, response, err)
			}
		}
	}
	compositionPrelude(sourceID, "change")
	compositionAction(t, ctx, s, service, env, sourceID, "record_clarify", map[string]any{"resolved_questions": []string{}, "note": "No operator questions remain."})
	compositionAction(t, ctx, s, service, env, sourceID, "complete_step", map[string]any{"step_id": "clarify", "note": "Clarification complete."})
	compositionRecordPlan(t, ctx, s, service, env, sourceID)
	compositionCompletePlanStep(t, ctx, s, service, env, sourceID)
	compositionPrelude(successorID, "research")

	version := agentCompositionWorkVersion(t, s, sourceID)
	linked := dispatchCompositionLink(t, ctx, s, service, env, sourceID, successorID, version, "composition-link")
	if linked.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("agent forward link failed: %+v", linked.Error)
	}
	var storedKind, workflowType string
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT w.kind,COALESCE(i.workflow_type,'') FROM relations r JOIN effect_receipts e ON e.relation_seq=r.id JOIN work_items w ON w.id=r.work_id_to JOIN workflow_instances i ON i.work_id=w.id WHERE r.work_id_from=? AND e.kind=? AND r.kind='forward_link' ORDER BY e.effect_seq DESC LIMIT 1`, sourceID, store.WorkflowSuccessorLinked).Scan(&storedKind, &workflowType); err != nil {
		t.Fatal(err)
	}
	if storedKind != "research" || workflowType != "research" {
		t.Fatalf("successor evidence kind=%q type=%q, want research and research", storedKind, workflowType)
	}

	version = agentCompositionWorkVersion(t, s, sourceID)
	refused := dispatchCompositionLink(t, ctx, s, service, env, sourceID, missingInstanceID, version, "composition-missing-link")
	if refused.Outcome != agentcontract.OutcomeError || refused.Error == nil || refused.Error.Kind != "unknown_scope" {
		if refused.Error != nil {
			t.Fatalf("missing workflow instance error kind=%q message=%q recovery=%+v", refused.Error.Kind, refused.Error.Message, refused.Error.RecoveryAction)
		}
		t.Fatalf("missing workflow instance outcome=%q without error", refused.Outcome)
	}
	if refused.Error.Message != "workflow instance does not exist" {
		t.Fatalf("missing workflow instance message=%q", refused.Error.Message)
	}
}

func captureCompositionWork(t *testing.T, ctx context.Context, s *store.Store, service *Service, env CallEnvelope, title, kind, _ string, idempotencyKey string) string {
	t.Helper()
	input := map[string]any{
		"title": title, "kind": kind,
		"project_ids":       []string{"project-1"},
		"human_key":         fmt.Sprintf("ZZ-%d", len(idempotencyKey)+7000),
		"remote_issue_uuid": "fixture-" + idempotencyKey,
		"url":               "https://linear.app/example/issue/ZZ",
		"idempotency_key":   idempotencyKey,
	}
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	response, err := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_define", Operation: "capture", Input: raw}, env)
	if err != nil || response.Outcome != agentcontract.OutcomeOK || len(*response.ChangedRefs) != 2 {
		t.Fatalf("capture %q response=%+v err=%v", title, response, err)
	}
	return (*response.ChangedRefs)[0].ID
}

func compositionAction(t *testing.T, ctx context.Context, s *store.Store, service *Service, env CallEnvelope, workID, action string, fields map[string]any) {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"work_id": workID, "expected_version": agentCompositionWorkVersion(t, s, workID), "action_id": action, "fields": fields, "idempotency_key": "composition-" + workID + "-" + action + "-" + strconv.FormatInt(agentCompositionWorkVersion(t, s, workID), 10)})
	if err != nil {
		t.Fatal(err)
	}
	response, err := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: raw}, env)
	if err != nil || response.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("%s %s: response=%+v err=%v", workID, action, response, err)
	}
}

func compositionRecordPlan(t *testing.T, ctx context.Context, s *store.Store, service *Service, env CallEnvelope, workID string) {
	t.Helper()
	compositionAction(t, ctx, s, service, env, workID, "record_plan", agentPlanFields())
}

func compositionCompletePlanStep(t *testing.T, ctx context.Context, s *store.Store, service *Service, env CallEnvelope, workID string) {
	t.Helper()
	compositionAction(t, ctx, s, service, env, workID, "complete_step", map[string]any{"step_id": "plan", "note": "composition fixture"})
}

func dispatchCompositionLink(t *testing.T, ctx context.Context, s *store.Store, service *Service, env CallEnvelope, sourceID, successorID string, version int64, idempotencyKey string) agentcontract.Envelope {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"work_id": sourceID, "expected_version": version, "action_id": "link_successor",
		"fields": map[string]any{"successor_work_id": successorID}, "idempotency_key": idempotencyKey,
	})
	if err != nil {
		t.Fatal(err)
	}
	env.RequestID = "request:" + idempotencyKey + ":" + strconv.FormatInt(version, 10)
	response, err := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: raw}, env)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func agentCompositionWorkVersion(t *testing.T, s *store.Store, workID string) int64 {
	t.Helper()
	var version int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM work_items WHERE id=?`, workID).Scan(&version); err != nil {
		t.Fatal(err)
	}
	return version
}

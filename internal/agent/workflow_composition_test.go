package agent

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

func TestAgentWorkflowCompositionUsesSelectedSuccessorFamily(t *testing.T) {
	ctx := context.Background()
	s, service, grant, _ := mutationDispatchFixture(t, []Capability{"work_define", "work_transition"})
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	sourceID := captureCompositionWork(t, ctx, s, service, env, "Composition source", "task", "workflow.implementation", "composition-source")
	successorID := captureCompositionWork(t, ctx, s, service, env, "Composition successor", "task", "workflow.research", "composition-successor")
	// A capture always pins a workflow instance now (#650), so the
	// instance-less successor this refusal guards is created the way real
	// instance-less work arises: raw events, as predecessor import leaves.
	missingInstanceID := "work-composition-missing-instance"
	if err := store.ApplyOperation(ctx, s, store.Operation{Events: []store.Event{
		{EventID: "composition-missing:create", Kind: "work.created", SubjectType: store.SubjectWorkItem, SubjectID: missingInstanceID, Actor: "operator", OccurredAt: s.Now().UTC(), PayloadVersion: 2, Payload: []byte(`{"work_kind":"research","title":"Composition missing instance","priority":0}`)},
		{EventID: "composition-missing:memberships", Kind: "work.memberships_replaced", SubjectType: store.SubjectWorkItem, SubjectID: missingInstanceID, Actor: "operator", OccurredAt: s.Now().UTC(), PayloadVersion: 1, Payload: []byte(`{"memberships":[{"project_id":"project-1","role":"primary"}],"expected_version":1,"resulting_version":2}`)},
	}, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectWorkItem, missingInstanceID): 0}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().ExecContext(ctx, `INSERT INTO fold_guard(active) VALUES(1); UPDATE workflow_instances SET current_step='execution' WHERE work_id=?; DELETE FROM fold_guard`, sourceID); err != nil {
		t.Fatal(err)
	}

	version := agentCompositionWorkVersion(t, s, sourceID)
	linked := dispatchCompositionLink(t, ctx, s, service, env, sourceID, successorID, version, "composition-link")
	if linked.Outcome != OutcomeOK {
		t.Fatalf("agent forward link failed: %+v", linked.Error)
	}
	var storedKind, definitionRef string
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT json_extract(payload,'$.successor_kind'),json_extract(payload,'$.definition_ref') FROM domain_events WHERE subject_id=? AND kind=? ORDER BY seq DESC LIMIT 1`, sourceID, store.WorkflowSuccessorLinked).Scan(&storedKind, &definitionRef); err != nil {
		t.Fatal(err)
	}
	if storedKind != "task" || definitionRef != "workflow.research" {
		t.Fatalf("successor evidence kind=%q definition=%q, want task and workflow.research", storedKind, definitionRef)
	}

	version = agentCompositionWorkVersion(t, s, sourceID)
	refused := dispatchCompositionLink(t, ctx, s, service, env, sourceID, missingInstanceID, version, "composition-missing-link")
	if refused.Outcome != OutcomeError || refused.Error == nil || refused.Error.Kind != "invalid_relation" {
		if refused.Error != nil {
			t.Fatalf("missing workflow instance error kind=%q message=%q recovery=%+v", refused.Error.Kind, refused.Error.Message, refused.Error.RecoveryAction)
		}
		t.Fatalf("missing workflow instance outcome=%q without error", refused.Outcome)
	}
	if refused.Error.Message != "successor has no workflow instance, so its family is undetermined" {
		t.Fatalf("missing workflow instance message=%q", refused.Error.Message)
	}
}

func captureCompositionWork(t *testing.T, ctx context.Context, s *store.Store, service *Service, env CallEnvelope, title, kind, workflowRef, idempotencyKey string) string {
	t.Helper()
	input := map[string]any{
		"title": title, "value_statement": "Prove workflow composition", "kind": kind,
		"project_ids": []string{"project-1"}, "idempotency_key": idempotencyKey,
	}
	if workflowRef != "" {
		input["workflow_type_ref"] = workflowRef
	}
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	response, err := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_define", Operation: "capture", Input: raw}, env)
	if err != nil || response.Outcome != OutcomeOK || len(*response.ChangedRefs) != 1 {
		t.Fatalf("capture %q response=%+v err=%v", title, response, err)
	}
	return (*response.ChangedRefs)[0].ID
}

func dispatchCompositionLink(t *testing.T, ctx context.Context, s *store.Store, service *Service, env CallEnvelope, sourceID, successorID string, version int64, idempotencyKey string) Envelope {
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

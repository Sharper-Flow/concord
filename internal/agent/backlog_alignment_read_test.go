package agent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/store"
)

func TestDiagnosisEvidenceSurvivesUnrelatedWorkRemoval(t *testing.T) {
	s, service, grant, _ := mutationDispatchFixture(t, []agentcontract.Capability{"work_transition", "product_read"})
	version := seedAgentWorkflowAt(t, s, grant, store.WorkflowStepPlanFix, "diagnose")
	if err := store.ApplyOperation(context.Background(), s, store.Operation{Events: []store.Event{{
		EventID: "create-related", Kind: "work.created", SubjectType: store.SubjectWorkItem, SubjectID: "related",
		Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 4, Payload: json.RawMessage(`{"work_kind":"task","title":"Related work"}`),
	}, {
		EventID: "membership-related", Kind: "work.memberships_replaced", SubjectType: store.SubjectWorkItem, SubjectID: "related",
		Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"memberships":[{"project_id":"project-1","role":"primary"}],"expected_version":1,"resulting_version":2}`),
	}}, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectWorkItem, "related"): 0}}); err != nil {
		t.Fatal(err)
	}
	scope, _, err := s.ScopeVersion(t.Context(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(map[string]any{"work_id": "work-1", "expected_version": version, "action_id": "record_diagnosis", "fields": map[string]any{"reproduction": "The defect reproduces.", "root_cause": "The cause is identified."}, "idempotency_key": "diagnosis-record"})
	if err != nil {
		t.Fatal(err)
	}
	response := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: raw}, mutationEnvelope(grant, scope))
	if response.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("diagnosis: %+v", response.Error)
	}
	evidence := func() [5]string {
		t.Helper()
		var facts [5]string
		if err := s.DatabaseForTesting().QueryRow(`SELECT event_id,payload_digest FROM effect_receipts WHERE subject_id='work-1' AND kind=?`, store.WorkflowDiagnosisRecorded).Scan(&facts[0], &facts[1]); err != nil {
			t.Fatal(err)
		}
		if err := s.DatabaseForTesting().QueryRow(`SELECT canonical_digest,op_id,result_event_ids FROM idempotency_records WHERE idempotency_key='diagnosis-record'`).Scan(&facts[2], &facts[3], &facts[4]); err != nil {
			t.Fatal(err)
		}
		return facts
	}
	before := evidence()
	if err := s.Transact(t.Context(), func(tx *store.Transaction) error {
		_, err := store.RecordLinearIssueLinkTx(t.Context(), tx, store.LinearIssueLink{WorkID: "related", RemoteIssueUUID: "remote-related", HumanKey: "CON-1", URL: "https://linear.app/example/issue/CON-1"})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	handoff := store.WorkRemovalHandoff{Findings: []string{"finding"}, RemainingScope: []string{"scope"}, Blockers: []string{"blocker"}, Artifacts: []string{"artifact"}, RenewalConditions: []string{"renewal"}}
	handoffJSON, err := json.Marshal(handoff)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(handoffJSON)
	scope, _, err = s.ScopeVersion(t.Context(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	removal, err := json.Marshal(map[string]any{"operation_id": "remove-related", "idempotency_key": "remove-related-key", "work_id": "related", "expected_version": workVersion(t, s, "related"), "reason": "shelved", "actor": "human-1", "product_id": "product-1", "execution_relinquished": true, "writes_reconciled": true, "effects_reconciled": true, "dependencies_resolved": true, "artifacts_verified": true, "handoff": handoff, "linear": store.LinearHandoffConfirmation{ProductID: "product-1", RemoteIssueUUID: "remote-related", Destination: "linear", HandoffDigest: fmt.Sprintf("sha256:%x", hash)}})
	if err != nil {
		t.Fatal(err)
	}
	if removed := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "remove", Input: removal}, mutationEnvelope(grant, scope)); removed.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("removal: %+v", removed.Error)
	}
	if after := evidence(); after != before {
		t.Fatalf("diagnosis evidence changed: %v -> %v", before, after)
	}
}

func TestDiagnosisRefusesRetiredSearchInput(t *testing.T) {
	s, service, grant, _ := mutationDispatchFixture(t, []agentcontract.Capability{"work_transition", "product_read"})
	version := seedAgentWorkflowAt(t, s, grant, store.WorkflowStepPlanFix, "diagnose")
	scope, _, err := s.ScopeVersion(t.Context(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(map[string]any{"work_id": "work-1", "expected_version": version, "action_id": "record_diagnosis", "fields": map[string]any{"reproduction": "The defect reproduces.", "searched": "The backlog", "outcome": "none_found", "root_cause": "The cause."}, "idempotency_key": "old-diagnosis"})
	if err != nil {
		t.Fatal(err)
	}
	response := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: raw}, mutationEnvelope(grant, scope))
	if response.Outcome != agentcontract.OutcomeError || response.Error == nil || response.Error.Kind != "invalid_input" {
		t.Fatalf("retired input accepted: %+v", response)
	}
	if got := workVersion(t, s, "work-1"); got != version {
		t.Fatalf("refusal changed version: %d -> %d", version, got)
	}
}

package agent

import (
	"context"
	"encoding/json"
	"strconv"
	"sync"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

func TestPublicDuplicateContractRecoveryConsumesApprovalAndKeepsWorkID(t *testing.T) {
	ctx := context.Background()
	s, service, grant, privateKey := mutationDispatchFixture(t, []Capability{"work_transition"})
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	if got := seedAgentWorkflow(t, s, grant); got != 4 {
		t.Fatalf("workflow seed version=%d, want 4", got)
	}
	for i, action := range []string{"record_proposal", "record_discovery", "record_design"} {
		fields := ""
		if action == "record_proposal" {
			fields = `,"fields":{"problem":"The bounded problem statement.","affected":["The affected system."],"stakes":"The bounded stakes statement.","user_outcomes":["The expected user outcome."]}`
		} else if action == "record_design" {
			fields = `,"fields":{"approach":"The recorded approach is the implementation boundary.","decisions":[{"id":"decision:dispatch","question":"What crosses into execution?","choice":"The typed design record.","rationale":"The worker must receive the approved decision.","rejected":[]}],"touched_refs":["path:dispatch"]}`
		} else {
			fields = `,"fields":{}`
		}
		expectedVersion := 4 + i
		if i > 0 {
			expectedVersion++
		}
		input := json.RawMessage(`{"work_id":"work-1","expected_version":` + strconv.Itoa(expectedVersion) + `,"action_id":"` + action + `"` + fields + `,"idempotency_key":"public-recovery-` + action + `"}`)
		response, dispatchErr := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: input}, env)
		if dispatchErr != nil || response.Outcome != OutcomeOK {
			t.Fatalf("advance action=%s response=%+v err=%v", action, response, dispatchErr)
		}
	}
	initialInput := workflowContractActionInput(t, "work-1", 9, "public-recovery-approve", "")
	challenge, err := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: initialInput}, env)
	if err != nil || challenge.Outcome != OutcomeError || challenge.Error == nil || challenge.Error.Kind != "approval_required" {
		t.Fatalf("initial approval challenge response=%+v err=%v", challenge, err)
	}
	challengeRef, _ := challenge.Error.Details["approval_ref"].(string)
	env.HostApproval = signedHostApproval(privateKey, challengeRef, mutationDigest("concord_work_transition", "workflow_action", env, initialInput), map[string]any{
		"product_id": "product-1", "project_ids": []string{"project-1"}, "work_ids": []string{"work-1"}, "scope_version": scopeVersion,
	}, map[string]any{"work": 9}, grant.SessionRef, grant.AgentRef, grant.Worktree, fixedTime(), "public-recovery-initial")
	if approved, err := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: workflowContractActionInput(t, "work-1", 9, "public-recovery-approve", challengeRef)}, env); err != nil || approved.Outcome != OutcomeOK {
		t.Fatalf("initial approval response=%+v err=%v", approved, err)
	}

	db := s.DatabaseForTesting()
	if _, err := db.Exec(`INSERT INTO fold_guard(active) VALUES(1);
INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class)
SELECT work_id,2,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class FROM workflow_contracts WHERE work_id='work-1' AND contract_version=1;
INSERT INTO workflow_contract_predicates(work_id,contract_version,predicate_id,ordinal,outcome_kind,outcome_payload)
SELECT work_id,2,predicate_id,ordinal,outcome_kind,outcome_payload FROM workflow_contract_predicates WHERE work_id='work-1' AND contract_version=1;
DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	version := workVersion(t, s, "work-1")
	recoveryFields := map[string]any{
		"contract_version": 3, "predecessor_contract_versions": []int64{1, 2}, "premise": "recovered premise",
		"outcome_predicates": []map[string]any{{
			"predicate_id": "predicate:primary", "ordinal": 0, "outcome_kind": "check",
			"outcome_payload": map[string]any{"kind": "check", "check_ref": "check:public-recovery", "immutable_subject_ref": "commit:work-1", "expected_result": "pass"},
		}}, "required_evidence": []string{"verification"}, "route_conventions": []string{},
		"spec_mandate": []string{}, "law_modifies": []string{}, "architecture_binding": workflowArchitectureBindingFixture(), "rigor_class": "prototype_internal",
		"supersede_reason": "repair the duplicate projection", "audit_evidence": []string{"evidence:public-recovery"},
	}
	recoveryInput, err := json.Marshal(map[string]any{"work_id": "work-1", "expected_version": version, "action_id": "supersede_contract", "fields": recoveryFields, "idempotency_key": "public-recovery-supersede"})
	if err != nil {
		t.Fatal(err)
	}
	challenge, err = Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: recoveryInput}, env)
	if err != nil || challenge.Outcome != OutcomeError || challenge.Error == nil || challenge.Error.Kind != "approval_required" {
		t.Fatalf("recovery approval challenge response=%+v error=%+v err=%v", challenge, challenge.Error, err)
	}
	recoveryRef, _ := challenge.Error.Details["approval_ref"].(string)
	env.HostApproval = signedHostApproval(privateKey, recoveryRef, mutationDigest("concord_work_transition", "workflow_action", env, recoveryInput), map[string]any{
		"product_id": "product-1", "project_ids": []string{"project-1"}, "work_ids": []string{"work-1"}, "scope_version": scopeVersion,
	}, map[string]any{"work": version, "contract": int64(2)}, grant.SessionRef, grant.AgentRef, grant.Worktree, fixedTime(), "public-recovery-supersede")
	approvedInput := map[string]any{"work_id": "work-1", "expected_version": version, "action_id": "supersede_contract", "fields": recoveryFields, "approval": map[string]any{"approval_ref": recoveryRef}, "idempotency_key": "public-recovery-supersede"}
	approvedPayload, err := json.Marshal(approvedInput)
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan struct {
		response Envelope
		err      error
	}, 2)
	var group sync.WaitGroup
	for range 2 {
		group.Add(1)
		go func() {
			defer group.Done()
			response, dispatchErr := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: approvedPayload}, env)
			results <- struct {
				response Envelope
				err      error
			}{response: response, err: dispatchErr}
		}()
	}
	group.Wait()
	close(results)
	var successful, replayedCount int
	for result := range results {
		if result.err != nil || result.response.Outcome != OutcomeOK {
			t.Fatalf("concurrent recovery response=%+v error=%+v err=%v", result.response, result.response.Error, result.err)
		}
		successful++
		if result.response.Replayed {
			replayedCount++
		}
	}
	if successful != 2 || replayedCount != 1 {
		t.Fatalf("concurrent recovery successful=%d replayed=%d, want 2 and 1", successful, replayedCount)
	}
	if got := workVersion(t, s, "work-1"); got != version+2 {
		t.Fatalf("recovery work version=%d, want %d after operator attribution and recovery", got, version+2)
	}
	if active := countRows(t, db, `SELECT count(*) FROM workflow_contracts WHERE work_id='work-1' AND superseded_by IS NULL`); active != 1 {
		t.Fatalf("active contract count=%d, want 1", active)
	}
	pin, err := store.ReadWorkPin(ctx, s, "work-1")
	if err != nil || pin.WorkID != "work-1" {
		t.Fatalf("recovery changed execution identity: pin=%+v err=%v", pin, err)
	}
	if err := store.WorkflowActionPreflight(ctx, s, store.WorkflowActionPreflightRequest{WorkID: "work-1", ExpectedVersion: version + 2, ActionID: "start_execution", Payload: json.RawMessage(`{}`), Actor: store.WorkflowActor{PrincipalRef: grant.PrincipalRef, ClientRef: grant.ClientRef, AgentRef: grant.AgentRef, SessionRef: grant.SessionRef, ActorClass: store.ActorAgent}}); err != nil {
		t.Fatalf("recovered work did not reach execution admission: %v", err)
	}
	var payload string
	if err := db.QueryRow(`SELECT payload FROM domain_events WHERE kind=? AND subject_id=? ORDER BY seq DESC LIMIT 1`, store.WorkflowContractSuperseded, "work-1").Scan(&payload); err != nil {
		t.Fatal(err)
	}
	var eventFields map[string]any
	if err := json.Unmarshal([]byte(payload), &eventFields); err != nil {
		t.Fatal(err)
	}
	consumedRef, ok := eventFields["approval_ref"].(string)
	if !ok || consumedRef == "" || consumedRef == recoveryRef {
		t.Fatalf("recovery event approval_ref=%q, challenge ref=%q", consumedRef, recoveryRef)
	}
	if used := usedCount(t, db, `SELECT used_count FROM agent_approvals WHERE approval_ref=?`, consumedRef); used != 1 {
		t.Fatalf("recovery approval used_count=%d, want 1", used)
	}
	replayed, err := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: approvedPayload}, env)
	if err != nil || replayed.Outcome != OutcomeOK || !replayed.Replayed {
		t.Fatalf("exact recovery retry response=%+v err=%v", replayed, err)
	}
	if active := countRows(t, db, `SELECT count(*) FROM workflow_contracts WHERE work_id='work-1' AND superseded_by IS NULL`); active != 1 {
		t.Fatalf("exact recovery retry changed active contract count to %d", active)
	}
}

package agent

import (
	"context"
	"encoding/json"
	"strconv"
	"sync"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

func TestPublicDuplicateContractRecoveryUsesRoutineAuthorityAndKeepsWorkID(t *testing.T) {
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
	// The pre-contract walk is proposal and alignment;
	// each consumes two events over the version-4 capture.
	for i, action := range []string{"record_proposal", "record_alignment"} {
		var fields string
		switch action {
		case "record_proposal":
			fields = `,"fields":{"problem":"The bounded problem statement.","affected":["The affected system."],"stakes":"The bounded stakes statement.","user_outcomes":["The expected user outcome."]}`
		case "record_alignment":
			fields = `,"fields":{"searched":"The bounded backlog search statement.","outcome":"none_found"}`
		}
		expectedVersion := []int{4, 6}[i]
		input := json.RawMessage(`{"work_id":"work-1","expected_version":` + strconv.Itoa(expectedVersion) + `,"action_id":"` + action + `"` + fields + `,"idempotency_key":"public-recovery-` + action + `"}`)
		response, dispatchErr := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: input}, env)
		if dispatchErr != nil || response.Outcome != OutcomeOK {
			t.Fatalf("advance action=%s response=%+v err=%v", action, response, dispatchErr)
		}
	}
	initialInput := workflowContractActionInput(t, "public-recovery-approve", "")
	challenge, err := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: initialInput}, env)
	if err != nil || challenge.Outcome != OutcomeError || challenge.Error == nil || challenge.Error.Kind != "approval_required" {
		t.Fatalf("initial approval challenge response=%+v err=%v", challenge, err)
	}
	challengeRef, _ := challenge.Error.Details["approval_ref"].(string)
	env.HostApproval = signedHostApproval(privateKey, challengeRef, mutationDigest("concord_work_transition", "workflow_action", env, initialInput), map[string]any{
		"product_id": "product-1", "project_ids": []string{"project-1"}, "work_ids": []string{"work-1"}, "scope_version": scopeVersion,
	}, map[string]any{"work": 8}, grant.SessionRef, grant.AgentRef, grant.Worktree, fixedTime(), "public-recovery-initial")
	if approved, err := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: workflowContractActionInput(t, "public-recovery-approve", challengeRef)}, env); err != nil || approved.Outcome != OutcomeOK {
		t.Fatalf("initial approval response=%+v err=%v", approved, err)
	}

	recordApprovedWorkDesign(t, s, service, mutationEnvelope(grant, scopeVersion), "work-1", "public-recovery-design")
	db := s.DatabaseForTesting()
	if _, err := db.Exec(`INSERT INTO write_guard(active) VALUES(1);
INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class)
SELECT work_id,2,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class FROM workflow_contracts WHERE work_id='work-1' AND contract_version=1;
INSERT INTO workflow_contract_predicates(work_id,contract_version,predicate_id,ordinal,outcome_kind,outcome_payload)
SELECT work_id,2,predicate_id,ordinal,outcome_kind,outcome_payload FROM workflow_contract_predicates WHERE work_id='work-1' AND contract_version=1;
DELETE FROM write_guard`); err != nil {
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
		"decision_domains": []string{},
		"supersede_reason": "repair the duplicate projection", "audit_evidence": []string{"evidence:public-recovery"},
	}
	recoveryInput, err := json.Marshal(map[string]any{"work_id": "work-1", "expected_version": version, "action_id": "supersede_contract", "fields": recoveryFields, "idempotency_key": "public-recovery-supersede"})
	if err != nil {
		t.Fatal(err)
	}
	// A non-serious supersession is routine under consequence_v1, even when
	// recovery repairs a duplicate projection. No second approval is consumed.
	env.HostApproval = nil
	challengesBefore := verifyWallChallenges(t, s)
	approvalsBefore := agentRowsSnapshot(t, s, `SELECT * FROM agent_approvals`)
	results := make(chan struct {
		response Envelope
		err      error
	}, 2)
	var group sync.WaitGroup
	for range 2 {
		group.Add(1)
		go func() {
			defer group.Done()
			response, dispatchErr := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: recoveryInput}, env)
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
	if got := workVersion(t, s, "work-1"); got != version+1 {
		t.Fatalf("recovery work version=%d, want %d after routine recovery", got, version+1)
	}
	if active := countRows(t, db, `SELECT count(*) FROM workflow_contracts WHERE work_id='work-1' AND superseded_by IS NULL`); active != 1 {
		t.Fatalf("active contract count=%d, want 1", active)
	}
	pin, err := store.ReadWorkPin(ctx, s, "work-1")
	if err != nil || pin.WorkID != "work-1" {
		t.Fatalf("recovery changed execution identity: pin=%+v err=%v", pin, err)
	}
	if err := store.WorkflowActionPreflightWithRegistry(ctx, s, store.BuiltinWorkflowRegistry(), store.WorkflowActionPreflightRequest{WorkID: "work-1", ExpectedVersion: version + 1, ActionID: "start_execution", Payload: json.RawMessage(`{}`), Actor: store.WorkflowActor{PrincipalRef: grant.PrincipalRef, ClientRef: grant.ClientRef, AgentRef: grant.AgentRef, SessionRef: grant.SessionRef, ActorClass: store.ActorAgent}}); err != nil {
		t.Fatalf("recovered work did not reach execution admission: %v", err)
	}
	var approvingActor string
	if err := db.QueryRow(`SELECT approved_by FROM workflow_contracts WHERE work_id='work-1' AND superseded_by IS NULL`).Scan(&approvingActor); err != nil {
		t.Fatal(err)
	}
	if approvingActor != store.DeriveWorkflowActorRef(grant.PrincipalRef, grant.ClientRef, grant.AgentRef, grant.SessionRef) || agentRowsSnapshot(t, s, `SELECT * FROM agent_approvals`) != approvalsBefore {
		t.Fatalf("routine recovery did not remain agent-attributed and approval-free: approving_actor=%s", approvingActor)
	}
	if got := verifyWallChallenges(t, s); got != challengesBefore {
		t.Fatalf("routine recovery minted approval challenges: %d -> %d", challengesBefore, got)
	}
	replayed, err := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: recoveryInput}, env)
	if err != nil || replayed.Outcome != OutcomeOK || !replayed.Replayed {
		t.Fatalf("exact recovery retry response=%+v err=%v", replayed, err)
	}
	if active := countRows(t, db, `SELECT count(*) FROM workflow_contracts WHERE work_id='work-1' AND superseded_by IS NULL`); active != 1 {
		t.Fatalf("exact recovery retry changed active contract count to %d", active)
	}
	if agentRowsSnapshot(t, s, `SELECT * FROM agent_approvals`) != approvalsBefore {
		t.Fatal("exact recovery retry changed retained approval state")
	}
}

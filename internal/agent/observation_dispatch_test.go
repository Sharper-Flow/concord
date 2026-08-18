package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

// CD-0030: observations through the tool surface; visible at resume; carry
// no authority.

func TestObservationRecordDispatchContinuityAndNonAuthority(t *testing.T) {
	ctx := context.Background()
	s, service, grant := claimsFixture(t)
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	invoke := func(tool, op string, input any) Envelope {
		t.Helper()
		raw, _ := json.Marshal(input)
		response, err := Dispatch(ctx, s, service, InvokeRequest{Tool: tool, Operation: op, Input: raw}, mutationEnvelope(grant, scopeVersion))
		if err != nil {
			t.Fatal(err)
		}
		return response
	}

	// Record through the surface.
	recorded := invoke("concord_work_define", "observation_record", map[string]any{
		"work_id": "work-holder", "statement": "The sibling services share the misalignment this change fixes locally.",
		"refs": []string{"service:cards-b"}, "tags": []string{"systemic"}, "idempotency_key": "obs-1",
	})
	if recorded.Outcome != OutcomeOK {
		t.Fatalf("observation_record failed: %+v", recorded.Error)
	}

	// The work needs a workflow instance for continuity.
	definition, defErr := store.BuiltinWorkflowDefinitionForRef("workflow.break_fix")
	if defErr != nil {
		t.Fatal(defErr)
	}
	if err := s.Transact(ctx, func(tx *store.Transaction) error {
		return store.InitializeWorkflowTx(ctx, tx, store.WorkflowInitializationRequest{WorkID: "work-holder", Definition: definition, Actor: store.WorkflowActor{PrincipalRef: "human-1", ClientRef: "client-1", AgentRef: "agent-1", SessionRef: "session-1", ActorClass: store.ActorAgent}, Now: fixedTime()})
	}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.ReadWorkflowContinuity(ctx, s, store.ContinuityRequest{Work: "work-holder", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Observations) != 1 || !strings.Contains(snapshot.Observations[0].Statement, "sibling services") {
		t.Fatalf("observations at resume=%+v", snapshot.Observations)
	}

	// Non-authority: an observation does not soften the terminal transition's
	// approval requirement, and no workflow gate reads it. Post-D3 the
	// refusal surfaces as missing_evidence (the agent must supply the
	// verification evidence before the approval challenge can even be
	// minted); this is strictly stronger than the pre-fix approval-only
	// gate because the missing-evidence recovery_action=provide_evidence
	// names the exact next step the agent must take.
	terminalInput, _ := json.Marshal(map[string]any{"work_id": "work-holder", "expected_version": 3, "target": "completed", "reason": "observed and delivered", "idempotency_key": "obs-terminal"})
	response, err := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "lifecycle", Input: terminalInput}, mutationEnvelope(grant, scopeVersion))
	if err != nil {
		t.Fatal(err)
	}
	if response.Outcome != OutcomeError || response.Error == nil || response.Error.Kind != "missing_evidence" {
		t.Fatalf("observation must not substitute for terminal evidence: %+v", response.Error)
	}
	// Supplying evidence proves the observation buys no approval either, so
	// the approval gate remains asserted here and not merely the evidence one.
	obsWithEvidence, _ := json.Marshal(map[string]any{"work_id": "work-holder", "expected_version": 3, "target": "completed", "reason": "observed and delivered", "idempotency_key": "obs-terminal-evidence",
		"evidence": []map[string]any{{"kind": "verification", "authority": "native_run", "locator_kind": "run_ref", "locator": "observation-verification"}}})
	obsApprovalGated, err := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "lifecycle", Input: obsWithEvidence}, mutationEnvelope(grant, scopeVersion))
	if err != nil {
		t.Fatal(err)
	}
	if obsApprovalGated.Outcome != OutcomeError || obsApprovalGated.Error == nil || obsApprovalGated.Error.Kind != "approval_required" {
		t.Fatalf("observation must not substitute for approval: %+v", obsApprovalGated.Error)
	}

	// Replay is idempotent.
	replay := invoke("concord_work_define", "observation_record", map[string]any{
		"work_id": "work-holder", "statement": "The sibling services share the misalignment this change fixes locally.",
		"refs": []string{"service:cards-b"}, "tags": []string{"systemic"}, "idempotency_key": "obs-1",
	})
	if replay.Outcome != OutcomeOK || !replay.Replayed {
		t.Fatalf("replay=%+v", replay.Error)
	}
	var count int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM work_observations WHERE work_id='work-holder'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("observation count=%d err=%v", count, err)
	}
}

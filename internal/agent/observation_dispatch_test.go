package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/store"
)

// CD-0030: observations through the tool surface; visible at resume; carry
// no authority.

func TestObservationRecordDispatchContinuityAndNonAuthority(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, service, grant := observationFixture(t)
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	invoke := func(tool, op string, input any) agentcontract.Envelope {
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
	if recorded.Outcome != agentcontract.OutcomeOK {
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

	// CD-0243 D3/D7 retire terminal consent. An observation still cannot
	// substitute for verification evidence. Initialization records two events.
	_, version := readWorkFromStore(t, s, "work-holder")
	if version != 4 {
		t.Fatalf("initialized work version = %d, want 4", version)
	}
	terminalInput, _ := json.Marshal(map[string]any{"work_id": "work-holder", "expected_version": version, "target": "completed", "reason": "observed and delivered", "idempotency_key": "obs-terminal"})
	response, err := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "lifecycle", Input: terminalInput}, mutationEnvelope(grant, scopeVersion))
	if err != nil {
		t.Fatal(err)
	}
	if response.Outcome != agentcontract.OutcomeError || response.Error == nil || response.Error.Kind != "missing_evidence" {
		t.Fatalf("observation must not substitute for terminal evidence: %+v", response.Error)
	}
	obsWithEvidence, _ := json.Marshal(map[string]any{"work_id": "work-holder", "expected_version": version, "target": "completed", "reason": "observed and delivered", "idempotency_key": "obs-terminal-evidence",
		"evidence": []map[string]any{{"kind": "verification", "authority": "native_run", "locator_kind": "run_ref", "locator": "observation-verification"}}})
	workflowGated, err := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "lifecycle", Input: obsWithEvidence}, mutationEnvelope(grant, scopeVersion))
	if err != nil {
		t.Fatal(err)
	}
	// D7 retains workflow admission: evidence alone cannot finish a planned
	// workflow, and neither refusal may change its version or disposition.
	if workflowGated.Outcome != agentcontract.OutcomeError || workflowGated.Error == nil || workflowGated.Error.Kind != "not_terminal" {
		t.Fatalf("observation must not substitute for workflow completion: %+v", workflowGated.Error)
	}
	if lifecycle, got := readWorkFromStore(t, s, "work-holder"); lifecycle != "needed" || got != version {
		t.Fatalf("refused completion changed work = %s v%d, want needed v%d", lifecycle, got, version)
	}

	// Replay is idempotent.
	replay := invoke("concord_work_define", "observation_record", map[string]any{
		"work_id": "work-holder", "statement": "The sibling services share the misalignment this change fixes locally.",
		"refs": []string{"service:cards-b"}, "tags": []string{"systemic"}, "idempotency_key": "obs-1",
	})
	if replay.Outcome != agentcontract.OutcomeOK || !replay.Replayed {
		t.Fatalf("replay=%+v", replay.Error)
	}
	var count int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM work_observations WHERE work_id='work-holder'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("observation count=%d err=%v", count, err)
	}
}

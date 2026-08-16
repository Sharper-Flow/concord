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
	tx, txErr := s.DB().BeginTx(ctx, nil)
	if txErr != nil {
		t.Fatal(txErr)
	}
	if err := store.InitializeWorkflowTx(ctx, tx, store.WorkflowInitializationRequest{WorkID: "work-holder", Definition: definition, Actor: store.WorkflowActor{PrincipalRef: "human-1", ClientRef: "client-1", AgentRef: "agent-1", SessionRef: "session-1", ActorClass: store.ActorAgent}, Now: fixedTime()}); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
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
	// approval requirement, and no workflow gate reads it.
	terminalInput, _ := json.Marshal(map[string]any{"work_id": "work-holder", "expected_version": 3, "target": "completed", "reason": "observed and delivered", "idempotency_key": "obs-terminal"})
	response, err := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "lifecycle", Input: terminalInput}, mutationEnvelope(grant, scopeVersion))
	if err != nil {
		t.Fatal(err)
	}
	if response.Outcome != OutcomeError || response.Error == nil || response.Error.Kind != "approval_required" {
		t.Fatalf("observation must not substitute for approval: %+v", response.Error)
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
	if err := s.DB().QueryRow(`SELECT count(*) FROM work_observations WHERE work_id='work-holder'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("observation count=%d err=%v", count, err)
	}
}

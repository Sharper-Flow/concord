package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestCriterion40ReadCorrectionPreservesAuthorityAndProgress(t *testing.T) {
	f := newSpineExecutionFixture(t)
	ctx := context.Background()
	challenge := f.call(t, "concord_work_transition", "outside_repair", map[string]any{
		"work_id": f.work, "expected_version": f.read(t).Pinned.Pin.Version,
		"reason": "Keep an unrelated approval pending during bounded reads.", "idempotency_key": "pending-read-approval",
	})
	if challenge.Error == nil || challenge.Error.Kind != "approval_required" {
		t.Fatalf("no pending approval for the preservation witness: %+v", challenge.Error)
	}
	snapshot := func() string {
		t.Helper()
		var state string
		if err := f.s.DatabaseForTesting().QueryRow(`SELECT json_array(
			(SELECT version FROM work_items WHERE id=?),
			(SELECT last FROM store_sequences WHERE name='effect'),
			(SELECT json_group_array(json_array(project_id,role)) FROM work_projects WHERE work_id=?),
			(SELECT json_group_array(json_array(challenge_ref,status,used_count,consumed_at)) FROM agent_approval_challenges))`, f.work, f.work).Scan(&state); err != nil {
			t.Fatal(err)
		}
		return state
	}
	invoke := func(tool, operation string, input map[string]any, env CallEnvelope) Envelope {
		t.Helper()
		response, err := Invoke(ctx, f.s, f.service, mustMarshalInvoke(t, InvokeRequest{Tool: tool, Operation: operation, Input: retryJSON(input)}, env))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := response.Encode(); err != nil {
			t.Fatal(err)
		}
		return response
	}
	env := f.env
	env.RequestID = "criterion40-read"
	before, progress := snapshot(), f.read(t).Pinned.Pin
	input := map[string]any{"work_id": f.work, "page": map[string]any{"limit": 21}}
	oversized := invoke("concord_work_trace", "continuity", input, env)
	assertInputCorrection(t, oversized, nil, "page.limit")
	input["page"] = map[string]any{"limit": 20}
	corrected := invoke("concord_work_trace", "continuity", input, env)
	if corrected.Outcome != OutcomeOK || corrected.RequestID != oversized.RequestID || corrected.ResolvedScope == nil || corrected.ResolvedScope.ScopeVersion != env.ScopeVersion {
		t.Fatalf("immediate same-session correction changed identity or authority: %+v", corrected)
	}
	if after := snapshot(); before != after || !reflect.DeepEqual(progress, f.read(t).Pinned.Pin) {
		t.Fatalf("read correction changed scope, approval or workflow progress: %s -> %s", before, after)
	}
	deniedEnv := env
	deniedEnv.ClientRef = "untrusted-client"
	denied := invoke("concord_work_trace", "continuity", input, deniedEnv)
	if denied.Error == nil || denied.Error.Kind != "unauthorized" || denied.Error.EffectState != EffectNone || denied.Error.RecoveryAction.Kind == "correct_request" {
		t.Fatalf("denied authority became input correction: %+v", denied.Error)
	}
	// The write reached its owner, but its retained acknowledgement is missing.
	// A replay must reconcile that uncertain effect, not invite corrected input.
	observation := map[string]any{"work_id": f.work, "statement": "A bounded observation.", "idempotency_key": "criterion40-uncertain"}
	spineOK(t, invoke("concord_work_define", "observation_record", observation, env))
	if _, err := f.s.DatabaseForTesting().Exec(`UPDATE idempotency_records SET changed_refs='null' WHERE principal_ref=? AND tool='concord_work_define' AND operation_kind='observation_record' AND idempotency_key='criterion40-uncertain'`, env.PrincipalRef); err != nil {
		t.Fatal(err)
	}
	before = snapshot()
	unknown := invoke("concord_work_define", "observation_record", observation, env)
	if unknown.Error == nil || unknown.Error.Kind != "operation_conflict" || unknown.Error.EffectState != EffectPossible || unknown.Error.RetrySafe || unknown.Error.RecoveryAction.Kind != "reconcile_operation" {
		t.Fatalf("uncertain write became a pre-effect input correction: %+v", unknown.Error)
	}
	if after := snapshot(); after != before {
		t.Fatalf("unknown-effect replay repeated the write: %s -> %s", before, after)
	}
}

func TestInvokeContinuityLimitCorrection(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, service, grant, _ := mutationDispatchFixture(t, []Capability{"product_read"})
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	invoke := func(input map[string]any) Envelope {
		t.Helper()
		raw, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		response, err := Invoke(ctx, s, service, mustMarshalInvoke(t, InvokeRequest{Tool: "concord_work_trace", Operation: "continuity", Input: raw}, env))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := response.Encode(); err != nil {
			t.Fatalf("continuity response cannot serialize: %v", err)
		}
		return response
	}
	snapshot := func() [2]int64 {
		t.Helper()
		state := [2]int64{workVersion(t, s, "work-1")}
		if err := s.DatabaseForTesting().QueryRow(`SELECT last FROM store_sequences WHERE name='effect'`).Scan(&state[1]); err != nil {
			t.Fatal(err)
		}
		return state
	}
	before := snapshot()
	for _, field := range []string{"limit", "page.limit"} {
		for _, limit := range []int{21, 100} {
			t.Run(fmt.Sprintf("%s/%d", field, limit), func(t *testing.T) {
				input := map[string]any{"work_id": "work-1"}
				bounds := input
				if field == "page.limit" {
					bounds = map[string]any{"cursor": nil}
					input["page"] = bounds
				}
				bounds["limit"] = limit
				refused := invoke(input)
				assertInputCorrection(t, refused, nil, field)
				if !strings.Contains(refused.Error.Message, "20") {
					t.Fatalf("refusal does not name the page bound: %+v", refused.Error)
				}
				if refused.RequestID != env.RequestID || refused.QueryID != "C19.Continuity" || refused.Tool != "concord_work_trace" || refused.Operation != "continuity" {
					t.Fatalf("refusal lost request identity: %+v", refused)
				}
				if after := snapshot(); after != before {
					t.Fatalf("refusal mutated work version/events: %v -> %v", before, after)
				}
				bounds["limit"] = 20
				if corrected := invoke(input); corrected.Outcome != OutcomeOK {
					t.Fatalf("correcting only %s under unchanged authority failed: %+v", field, corrected.Error)
				}
			})
		}
	}
	if defaulted := invoke(map[string]any{"work_id": "work-1"}); defaulted.Outcome != OutcomeOK {
		t.Fatalf("default continuity page failed: %+v", defaulted.Error)
	}
	if after := snapshot(); after != before {
		t.Fatalf("continuity reads mutated work version/events: %v -> %v", before, after)
	}
}

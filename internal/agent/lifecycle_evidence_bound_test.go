package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
)

// TestLifecycleCompletionEvidenceFieldBoundsNeverFaultTheResult checks input
// bounds at dispatch, including field-specific refusals and unchanged state.
func TestLifecycleCompletionEvidenceFieldBoundsNeverFaultTheResult(t *testing.T) {
	cases := []struct {
		name        string
		field       string
		size        int
		wantInvalid bool
	}{
		{name: "authority_129", field: "authority", size: 129},
		{name: "authority_256", field: "authority", size: 256},
		{name: "authority_257", field: "authority", size: 257, wantInvalid: true},
		{name: "locator_kind_65", field: "locator_kind", size: 65},
		{name: "locator_kind_256", field: "locator_kind", size: 256},
		{name: "locator_kind_257", field: "locator_kind", size: 257, wantInvalid: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, service, grant := agentJobsMutationPM1Fixture(t)
			env := agentJobsMutationEnvelope(t, s, grant)
			_, preVersion := readWorkFromStore(t, s, "work-cross")

			evidence := map[string]any{"kind": "verification", "authority": "agent-verifier", "locator_kind": "test", "locator": "verification-pass"}
			evidence[tc.field] = strings.Repeat("a", tc.size)
			payload := map[string]any{
				"work_id":          "work-cross",
				"expected_version": preVersion,
				"target":           "completed",
				"reason":           "complete with verification",
				"idempotency_key":  fmt.Sprintf("evidence-bound-%s-%d", tc.name, preVersion),
				"evidence":         []any{evidence},
			}
			input, err := json.Marshal(payload)
			if err != nil {
				t.Fatal(err)
			}

			request := InvokeRequest{Tool: "concord_work_transition", Operation: "lifecycle", Input: input}
			if tc.wantInvalid {
				// Invoke shapes a dispatch refusal into the envelope the caller receives.
				response, dispatchErr := Dispatch(context.Background(), s, service, request, env)
				first := shapeInvokeFailure(response, dispatchErr, request, env)
				if first.Error == nil || first.Error.Kind != "invalid_input" {
					t.Fatalf("%d-byte %s: outcome=%s err=%+v, want invalid_input", tc.size, tc.field, first.Outcome, first.Error)
				}
				assertEvidenceFieldPath(t, first, tc.field)
				if _, after := readWorkFromStore(t, s, "work-cross"); after != preVersion {
					t.Fatalf("refused request moved work version %d -> %d", preVersion, after)
				}
				return
			}
			// CD-0243 D3/D7 retire ordinary terminal consent, not field bounds.
			approved := dispatchMutation(t, s, service, request, env)
			if approved.Outcome != agentcontract.OutcomeOK {
				t.Fatalf("%d-byte %s: outcome=%s err=%+v, want ok", tc.size, tc.field, approved.Outcome, approved.Error)
			}
			if lifecycle, _ := readWorkFromStore(t, s, "work-cross"); lifecycle != "completed" {
				t.Fatalf("post-lifecycle=%q, want completed", lifecycle)
			}
		})
	}
}

func assertEvidenceFieldPath(t *testing.T, response agentcontract.Envelope, field string) {
	t.Helper()
	if want := "$.evidence[0]." + field; !strings.Contains(response.Error.Message, want) {
		t.Fatalf("invalid_input message %q does not name %s", response.Error.Message, want)
	}
	if response.Error.EffectState != agentcontract.EffectNone {
		t.Fatalf("invalid_input effect_state=%s, want none", response.Error.EffectState)
	}
}

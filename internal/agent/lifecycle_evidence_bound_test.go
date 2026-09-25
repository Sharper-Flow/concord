package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// TestLifecycleCompletionEvidenceFieldBoundsNeverFaultTheResult proves that an
// evidence field the request schema admits cannot make the completion result
// fail its own envelope validation. The request either succeeds or refuses as
// invalid_input naming the field path; malformed_response is a core fault and
// is never the answer to a caller-supplied value.
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
			s, service, grant, privateKey, _ := agentJobsMutationPM1Fixture(t)
			env := agentJobsMutationEnvelope(t, s, grant, "proj-web", "prod-alpha")
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
			first := dispatchMutation(t, s, service, request, env)
			if first.Error == nil || first.Error.Kind != "approval_required" {
				t.Fatalf("expected approval_required, got outcome=%s err=%+v", first.Outcome, first.Error)
			}
			challengeRef, _ := first.Error.Details["approval_ref"].(string)
			withApproval, err := injectApproval(input, challengeRef)
			if err != nil {
				t.Fatalf("inject approval: %v", err)
			}
			scope := map[string]any{
				"product_id":    "prod-alpha",
				"product_ids":   []string{"prod-alpha"},
				"project_ids":   []string{"proj-web"},
				"work_ids":      []string{"work-cross"},
				"scope_version": env.ScopeVersion,
			}
			versions := map[string]any{"work": preVersion}
			digest := mutationDigest("concord_work_transition", "lifecycle", env, withApproval)
			env.HostApproval = signedHostApproval(privateKey, challengeRef, digest, scope, versions, env.SessionRef, env.AgentRef, env.Worktree, fixedTime(), nonceForChallenge(challengeRef))

			approved := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "lifecycle", Input: withApproval}, env)
			if approved.Outcome != OutcomeOK {
				t.Fatalf("%d-byte %s: outcome=%s err=%+v, want ok", tc.size, tc.field, approved.Outcome, approved.Error)
			}
			if lifecycle, _ := readWorkFromStore(t, s, "work-cross"); lifecycle != "completed" {
				t.Fatalf("post-lifecycle=%q, want completed", lifecycle)
			}
			if len(approved.EvidenceRefs) != 1 {
				t.Fatalf("completion envelope evidence_refs=%d, want 1", len(approved.EvidenceRefs))
			}
			echoed := map[string]string{"authority": approved.EvidenceRefs[0].Authority, "locator_kind": approved.EvidenceRefs[0].LocatorKind}[tc.field]
			if len(echoed) != tc.size {
				t.Fatalf("completion envelope echoed a %d-byte %s, want %d", len(echoed), tc.field, tc.size)
			}
		})
	}
}

func assertEvidenceFieldPath(t *testing.T, response Envelope, field string) {
	t.Helper()
	if want := "$.evidence[0]." + field; !strings.Contains(response.Error.Message, want) {
		t.Fatalf("invalid_input message %q does not name %s", response.Error.Message, want)
	}
	if response.Error.EffectState != EffectNone {
		t.Fatalf("invalid_input effect_state=%s, want none", response.Error.EffectState)
	}
}

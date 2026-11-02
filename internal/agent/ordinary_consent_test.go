package agent

import (
	"fmt"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
)

// CD-0243 D3/D7 retire terminal status as an independent consent requirement.
// Verification evidence, exact versions and replay admission still apply.
func TestOrdinaryTerminalLifecycleNeedsNoConsent(t *testing.T) {
	for _, target := range []string{"completed", "cancelled"} {
		t.Run(target, func(t *testing.T) {
			s, service, grant := agentJobsMutationPM1Fixture(t)
			env := agentJobsMutationEnvelope(t, s, grant)
			_, version := readWorkFromStore(t, s, "work-cross")
			input := []byte(fmt.Sprintf(`{"work_id":"work-cross","expected_version":%d,"target":%q,"reason":"obsolete or verified work","idempotency_key":"ordinary-terminal","evidence":[{"kind":"verification","authority":"verifier","locator_kind":"test","locator":"verification-pass"}]}`, version, target))
			request := InvokeRequest{Tool: "concord_work_transition", Operation: "lifecycle", Input: input}
			result := dispatchMutation(t, s, service, request, env)
			if result.Outcome != agentcontract.OutcomeOK {
				t.Fatalf("ordinary %s = %s: %+v", target, result.Outcome, result.Error)
			}
			if lifecycle, got := readWorkFromStore(t, s, "work-cross"); lifecycle != target || got != version+1 {
				t.Fatalf("state = %s v%d, want %s v%d", lifecycle, got, target, version+1)
			}
			replay := dispatchMutation(t, s, service, request, env)
			if replay.Outcome != agentcontract.OutcomeOK || !replay.Replayed {
				t.Fatalf("replay = %+v", replay)
			}
		})
	}
}

// CD-0243 D3/D7 retire kind-only merger/supersession consent. Both endpoint
// versions and the atomic terminal transition remain the store's decision.
func TestOrdinaryOverlapNeedsNoConsent(t *testing.T) {
	for _, kind := range []string{"merged_into", "supersedes"} {
		t.Run(kind, func(t *testing.T) {
			s, service, env := routineCoordinationFixture(t)
			binding := routineOverlapExactBinding(t, s, kind)
			request := InvokeRequest{Tool: "concord_work_relate", Operation: "resolve_overlap", Input: routineOverlapResolveInput(t, binding, "ordinary-overlap")}
			result := dispatchMutation(t, s, service, request, env)
			if result.Outcome != agentcontract.OutcomeOK {
				t.Fatalf("ordinary %s: %+v", kind, result.Error)
			}
			replay := dispatchMutation(t, s, service, request, env)
			if replay.Outcome != agentcontract.OutcomeOK || !replay.Replayed {
				t.Fatalf("overlap replay: %+v", replay.Error)
			}
		})
	}
}

package agent

import (
	"encoding/json"
	"fmt"
	"testing"
)

// CD-0243 D3/D7 retire terminal status as an independent consent requirement.
// Verification evidence, exact versions and replay admission still apply.
func TestOrdinaryTerminalLifecycleNeedsNoConsent(t *testing.T) {
	for _, target := range []string{"completed", "cancelled"} {
		t.Run(target, func(t *testing.T) {
			s, service, grant, _, _ := agentJobsMutationPM1Fixture(t)
			env := agentJobsMutationEnvelope(t, s, grant, "proj-web")
			_, version := readWorkFromStore(t, s, "work-cross")
			input := []byte(fmt.Sprintf(`{"work_id":"work-cross","expected_version":%d,"target":%q,"reason":"obsolete or verified work","idempotency_key":"ordinary-terminal","evidence":[{"kind":"verification","authority":"verifier","locator_kind":"test","locator":"verification-pass"}]}`, version, target))
			request := InvokeRequest{Tool: "concord_work_transition", Operation: "lifecycle", Input: input}
			result := dispatchMutation(t, s, service, request, env)
			if result.Outcome != OutcomeOK {
				t.Fatalf("ordinary %s = %s: %+v", target, result.Outcome, result.Error)
			}
			if lifecycle, got := readWorkFromStore(t, s, "work-cross"); lifecycle != target || got != version+1 {
				t.Fatalf("state = %s v%d, want %s v%d", lifecycle, got, target, version+1)
			}
			replay := dispatchMutation(t, s, service, request, env)
			if replay.Outcome != OutcomeOK || !replay.Replayed {
				t.Fatalf("replay = %+v", replay)
			}
			if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM agent_approval_challenges`); got != 0 {
				t.Fatalf("ordinary lifecycle minted %d challenges", got)
			}
		})
	}
}

// CD-0243 D3/D7 retire ordinary outside-repair consent, not hold or proof gates.
func TestOrdinaryOutsideRepairNeedsNoConsent(t *testing.T) {
	for _, mode := range []string{"resume", "completed"} {
		t.Run(mode, func(t *testing.T) {
			s, service, env, fake := outsideFixture(t)
			dispatch := func(operation string, input map[string]any) Envelope {
				raw, err := json.Marshal(input)
				if err != nil {
					t.Fatal(err)
				}
				return dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: operation, Input: raw}, env)
			}
			assertOutsideOK(t, dispatch("outside_repair", outsideHoldInput(2, nil)), "ordinary hold")
			if fake.fetches != 0 {
				t.Fatal("hold fetched external evidence")
			}
			input := outsideResumeInput(4, nil)
			if mode == "completed" {
				input = outsideReconcileInput(4, nil)
			}
			assertOutsideOK(t, dispatch("outside_repair_reconcile", input), mode)
			if mode == "completed" && fake.fetches == 0 {
				t.Fatal("completed reconcile did not fetch proof")
			}
			if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM agent_approval_challenges`); got != 0 {
				t.Fatalf("ordinary outside repair minted %d challenges", got)
			}
		})
	}
}

// CD-0243 D3/D7 retire kind-only merger/supersession consent. Both endpoint
// versions and the atomic terminal transition remain the store's decision.
func TestOrdinaryOverlapNeedsNoConsent(t *testing.T) {
	for _, kind := range []string{"merged_into", "supersedes"} {
		t.Run(kind, func(t *testing.T) {
			s, service, _, env := routineCoordinationFixture(t)
			binding := routineOverlapExactBinding(t, s, kind)
			request := InvokeRequest{Tool: "concord_work_relate", Operation: "resolve_overlap", Input: routineOverlapResolveInput(t, binding, "ordinary-overlap")}
			before := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM agent_approval_challenges`)
			result := dispatchMutation(t, s, service, request, env)
			if result.Outcome != OutcomeOK {
				t.Fatalf("ordinary %s: %+v", kind, result.Error)
			}
			replay := dispatchMutation(t, s, service, request, env)
			if replay.Outcome != OutcomeOK || !replay.Replayed {
				t.Fatalf("overlap replay: %+v", replay.Error)
			}
			if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM agent_approval_challenges`); got != before {
				t.Fatalf("ordinary overlap minted %d challenges", got-before)
			}
		})
	}
}

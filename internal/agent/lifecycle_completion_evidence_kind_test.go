package agent

import (
	"encoding/json"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
)

func TestLifecycleCompletionRequiresVerificationEvidence(t *testing.T) {
	cases := []struct {
		name     string
		kinds    []string
		accepted bool
	}{
		{name: "review_only", kinds: []string{"review"}},
		{name: "approval_only", kinds: []string{"approval"}},
		{name: "commit_only", kinds: []string{"commit"}},
		{name: "durable_note_only", kinds: []string{"durable_note"}},
		{name: "native_run_only", kinds: []string{"native_run"}},
		{name: "artifact_only", kinds: []string{"artifact"}},
		{name: "verification", kinds: []string{"verification"}, accepted: true},
		{name: "review_and_verification", kinds: []string{"review", "verification"}, accepted: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, service, grant := agentJobsMutationPM1Fixture(t)
			env := agentJobsMutationEnvelope(t, s, grant)
			beforeLifecycle, beforeVersion := readWorkFromStore(t, s, "work-cross")
			if beforeLifecycle != "in_progress" {
				t.Fatalf("initial lifecycle = %q, want in_progress", beforeLifecycle)
			}
			beforeEffects := agentCurrentEffects(t, s)
			evidence := make([]agentcontract.EvidenceRef, 0, len(tc.kinds))
			for _, kind := range tc.kinds {
				evidence = append(evidence, agentcontract.EvidenceRef{Kind: kind, Authority: "agent-verifier", LocatorKind: "test", Locator: kind + "-pass"})
			}
			input, err := json.Marshal(map[string]any{
				"work_id": "work-cross", "expected_version": beforeVersion,
				"target": "completed", "reason": "complete work", "idempotency_key": "complete-evidence-kind",
				"evidence": evidence,
			})
			if err != nil {
				t.Fatal(err)
			}
			resp := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "lifecycle", Input: input}, env)
			lifecycle, version := readWorkFromStore(t, s, "work-cross")
			if tc.accepted {
				if resp.Outcome != agentcontract.OutcomeOK || resp.Error != nil {
					t.Errorf("completion outcome = %s, error = %+v; want success", resp.Outcome, resp.Error)
				}
				if lifecycle != "completed" || version != beforeVersion+1 {
					t.Errorf("work = %s version %d; want completed version %d", lifecycle, version, beforeVersion+1)
				}
				return
			}
			if resp.Outcome != agentcontract.OutcomeError || resp.Error == nil {
				t.Errorf("completion outcome = %s, error = %+v; want missing_evidence", resp.Outcome, resp.Error)
			} else {
				if resp.Error.Kind != "missing_evidence" || resp.Error.Details["required_kind"] != "verification" || resp.Error.RecoveryAction.Kind != "provide_evidence" || resp.Error.EffectState != agentcontract.EffectNone {
					t.Errorf("refusal = %+v; want missing_evidence, verification, provide_evidence, effect none", resp.Error)
				}
			}
			if lifecycle != beforeLifecycle || version != beforeVersion {
				t.Errorf("work = %s version %d; want unchanged %s version %d", lifecycle, version, beforeLifecycle, beforeVersion)
			}
			if afterEffects := agentCurrentEffects(t, s); afterEffects != beforeEffects {
				t.Error("refusal changed current rows, receipts or effect sequence")
			}
		})
	}
}

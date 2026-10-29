package agent

import (
	"sort"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/store"
)

// A coupled kind that carries a typed block must also be deliverable end to
// end. The tests in internal/agentcontract/recovery_test.go judge the coupling
// alone; these drive a store failure through failureEnvelope and encode it,
// which is the path a caller actually receives.

// storeStaleLawFailure is the store-side refusal the law revision guard emits.
func storeStaleLawFailure(proposed string) *store.Failure {
	hash := "sha256:" + strings.Repeat("a", 64)
	return &store.Failure{
		Kind:           store.KindStaleLawRevision,
		Op:             "check_workflow_law_revision",
		Detail:         "workflow contract pins a deleted law",
		RecoveryAction: proposed,
		StaleLawRevision: &store.StaleLawRevision{
			OldLawID:        "law-old",
			OldContentHash:  hash,
			RecoveryActions: []string{"supersede_contract"},
		},
	}
}

// Supported store actions survive projection with their typed companion fields.
// Action payload constraints still apply, and prose remains remedy detail.
func TestCarrierRefusalsAreDeliverableWhateverTheStoreProposes(t *testing.T) {
	cases := map[string]func(string) *store.Failure{
		"stale_law_revision": storeStaleLawFailure,
	}
	for _, kind := range sortedKeys(boolKeys(cases)) {
		build := cases[kind]
		for _, proposed := range append(contractRecoveryKindList(t), "free operator prose") {
			name := kind + "/" + strings.ReplaceAll(proposed, " ", "_")
			t.Run(name, func(t *testing.T) {
				failure := build(proposed)
				if proposed == "use_declared_route" {
					failure.RecoveryRefs = []string{"supersede_contract"}
				}
				out := failureEnvelope(agentcontract.NewBase("coupling-1", "concord_work_transition", "workflow_action"), failure)
				if out.Error == nil || out.Error.Kind != kind {
					t.Fatalf("failure produced error %+v, want kind %q", out.Error, kind)
				}
				if _, err := out.Encode(); err != nil {
					t.Fatalf("store proposed recovery %q, so the %s refusal cannot be delivered: %v", proposed, kind, err)
				}
				want := proposed
				if proposed == "correct_request" || proposed == "free operator prose" {
					want = "reread_entities"
				}
				if out.Error.RecoveryAction.Kind != want {
					t.Fatalf("store proposed %q and the envelope kept %q, want %q", proposed, out.Error.RecoveryAction.Kind, want)
				}
				if proposed == "use_declared_route" && strings.Join(out.Error.RecoveryAction.RequiredRefs, ",") != "supersede_contract" {
					t.Fatalf("declared route refs lost: %+v", out.Error.RecoveryAction)
				}
				if proposed == "free operator prose" && out.Error.Details["recovery_detail"] != proposed {
					t.Fatalf("store remedy prose lost: %+v", out.Error.Details)
				}
			})
		}
	}
}

// contractRecoveryKindList returns the contract's recovery actions in a stable
// order so a subtest name is deterministic.
func contractRecoveryKindList(t *testing.T) []string {
	t.Helper()
	out := []string{}
	for kind := range contractRecoveryKinds(t) {
		out = append(out, kind)
	}
	sort.Strings(out)
	return out
}

// boolKeys adapts a lookup table to the key helper shared with the enum guard.
func boolKeys[V any](m map[string]V) map[string]bool {
	out := map[string]bool{}
	for k := range m {
		out[k] = true
	}
	return out
}

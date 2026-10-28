package agent

import (
	"sort"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/store"
)

// A coupled kind that carries a typed block must also be deliverable end to
// end. The tests in recovery_coupling_test.go judge the coupling alone; these
// drive a store failure through failureEnvelope and encode it, which is the
// path a caller actually receives.

// storeOverlapFailure is the store-side refusal the Domain overlap guard emits,
// with the recovery action the store proposed.
func storeOverlapFailure(proposed string) *store.Failure {
	return &store.Failure{
		Kind:           store.KindDomainOverlap,
		Op:             "workflow_domain_overlap",
		Detail:         "active Product-changing workflows have unresolved Domain overlap",
		RecoveryAction: proposed,
		DomainOverlap: &store.DomainOverlapFailure{
			Overlaps: []store.WorkflowDomainOverlap{{
				ProductID: "prod-alpha", FromWorkID: "work-from", ToWorkID: "work-to",
				FromContractVersion: 1, ToContractVersion: 1,
				SharedAffectedDomainIDs:   []string{"root"},
				OverlapClasses:            []string{"architecture"},
				ResolutionState:           "unresolved",
				RecoveryActions:           []string{"resolve_overlap"},
				SharedAffectedDomainCount: 1,
			}},
			TotalOverlaps: 1, ReturnedOverlaps: 1,
		},
	}
}

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

// TestCarrierRefusalsAreDeliverableWhateverTheStoreProposes is the
// reproduction. Both kinds were refused by validateError unless the recovery
// action was request_approval, and neither was named in
// enforcedRecoveryCouplings, so publicRecovery passed any other contractual
// proposal straight through. The pair then failed to marshal and the caller
// received a transport fault in place of the refusal. The reconcile path
// proposes reconcile_operation, which is contractual, which is how this
// reached a caller.
func TestCarrierRefusalsAreDeliverableWhateverTheStoreProposes(t *testing.T) {
	cases := map[string]func(string) *store.Failure{
		"domain_overlap":     storeOverlapFailure,
		"stale_law_revision": storeStaleLawFailure,
	}
	for _, kind := range sortedKeys(boolKeys(cases)) {
		build := cases[kind]
		for _, proposed := range append(contractRecoveryKindList(t), "free operator prose") {
			name := kind + "/" + strings.ReplaceAll(proposed, " ", "_")
			t.Run(name, func(t *testing.T) {
				out := failureEnvelope(agentcontract.NewBase("coupling-1", "concord_work_transition", "workflow_action"), build(proposed))
				if out.Error == nil || out.Error.Kind != kind {
					t.Fatalf("failure produced error %+v, want kind %q", out.Error, kind)
				}
				if _, err := out.Encode(); err != nil {
					t.Fatalf("store proposed recovery %q, so the %s refusal cannot be delivered: %v", proposed, kind, err)
				}
				if out.Error.RecoveryAction.Kind != "request_approval" {
					t.Fatalf("store proposed %q and the envelope kept %q; the contract couples %s to request_approval", proposed, out.Error.RecoveryAction.Kind, kind)
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

package agent

import (
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/store"
)

// TestRetrySafeStoreKindsNeverCoupleToReconcile is the reproduction for issue
// #736. A store refusal that proposes retry_same_request recorded nothing, so
// the public kind it maps to must not be coupled to reconcile_operation: that
// advice names an operation that does not exist. The lease-held refusal was
// mapped to operation_conflict, and the coupling overrode the store's own
// retry proposal with reconcile advice the caller could not follow.
func TestRetrySafeStoreKindsNeverCoupleToReconcile(t *testing.T) {
	t.Parallel()
	retrySafe := []store.FailureKind{store.KindWorktreeLeaseHeld}
	for _, kind := range retrySafe {
		public := mapFailureKind(kind)
		if coupled, ok := coupledRecovery(public); ok && coupled == "reconcile_operation" {
			t.Errorf("store kind %q maps to %q, which is coupled to reconcile_operation; a retry-safe refusal has nothing to reconcile", kind, public)
		}
		if got := agentcontract.PublicRecovery(public, "retry_same_request"); got != "retry_same_request" {
			t.Errorf("PublicRecovery(%q, retry_same_request) = %q, want the store's retry proposal to survive", public, got)
		}
	}
}

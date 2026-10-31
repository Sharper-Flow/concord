package store

import "testing"

func TestWorkRemovalRequestRetainsSafetyGates(t *testing.T) {
	req := removalTestRequest()
	if err := validateRemovalRequest(req); err != nil {
		t.Fatalf("complete removal request: %v", err)
	}
	req.WritesReconciled = false
	assertFailureKind(t, validateRemovalRequest(req), KindInvalidOperation)
}

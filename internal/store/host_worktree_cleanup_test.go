package store

import "testing"

func TestHostWorktreeSessionDoesNotBlockRemovalRequest(t *testing.T) {
	req := removalTestRequest()
	req.Sessions = []WorkRemovalSession{{SessionRef: "host-session", State: "active"}}
	if err := validateRemovalRequest(req); err != nil {
		t.Fatalf("host session blocked removal request: %v", err)
	}
	req.WritesReconciled = false
	assertFailureKind(t, validateRemovalRequest(req), KindInvalidOperation)
}

package agent

import (
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/store"
)

// validGrantFixture returns a record that passes every check in
// validateGrantRecord other than the one a test is exercising, so a failure
// names the boundary under test rather than an unrelated binding mismatch.
func validGrantFixture(expiresAt string) (store.GrantRecord, Invocation) {
	record := store.GrantRecord{
		RecordID:              "grant-1",
		PrincipalRef:          "principal-1",
		ClientRef:             "client-1",
		SessionRef:            "session-1",
		AgentRef:              "agent-1",
		Directory:             "/w",
		Worktree:              "/w",
		ClientKeyID:           "key-1",
		ActiveKeyID:           "key-1",
		ClientStatus:          "active",
		ManifestDigest:        ManifestDigest,
		CapabilitiesJSON:      `["work_transition"]`,
		ProductScopeJSON:      `[]`,
		ProjectScopeJSON:      `[]`,
		ScopeSnapshotJSON:     `{}`,
		CandidateProductsJSON: `[]`,
		IssuedAt:              "2026-01-01T00:00:00Z",
		ExpiresAt:             expiresAt,
	}
	invocation := Invocation{
		ClientRef:          "client-1",
		PrincipalRef:       "principal-1",
		SessionRef:         "session-1",
		AgentRef:           "agent-1",
		Directory:          "/w",
		Worktree:           "/w",
		ManifestDigest:     ManifestDigest,
		RequiredCapability: Capability("work_transition"),
	}
	return record, invocation
}

func mustTime(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		t.Fatalf("fixture timestamp %q does not parse: %v", value, err)
	}
	return parsed
}

// An expiry without a fractional part sorts after a "now" that carries one,
// because '.' (0x2E) precedes 'Z' (0x5A). A string comparison therefore judges
// an already-expired grant to be live for up to one second — fail-open at the
// authorization boundary.
func TestExpiredGrantWithoutFractionalSecondsIsRejected(t *testing.T) {
	record, invocation := validGrantFixture("2026-01-01T00:00:00Z")
	now := mustTime(t, "2026-01-01T00:00:00.5Z")

	service := &Service{}
	if _, err := service.validateGrantRecord(record, invocation, now); err == nil {
		t.Fatal("expected a grant that expired 500ms ago to be rejected, but it was accepted")
	}
}

// The chronological inverse: an expiry carrying a fractional part sorts before
// a whole-second "now", so a still-live grant is judged expired early.
func TestLiveGrantWithFractionalSecondsIsAccepted(t *testing.T) {
	record, invocation := validGrantFixture("2026-01-01T00:00:00.5Z")
	now := mustTime(t, "2026-01-01T00:00:00Z")

	service := &Service{}
	if _, err := service.validateGrantRecord(record, invocation, now); err != nil {
		t.Fatalf("expected a grant with 500ms remaining to be accepted, got %v", err)
	}
}

// A stored expiry that does not parse cannot be compared, so it must fail
// closed rather than fall through to a string comparison against arbitrary
// bytes.
func TestUnparseableStoredExpiryFailsClosed(t *testing.T) {
	record, invocation := validGrantFixture("not-a-timestamp")
	now := mustTime(t, "2026-01-01T00:00:00Z")

	service := &Service{}
	if _, err := service.validateGrantRecord(record, invocation, now); err == nil {
		t.Fatal("expected a corrupt stored expiry to be rejected, but it was accepted")
	}
}

// A scope snapshot that fails to parse leaves the decoded scope nil, and a nil
// scope satisfies every containment lookup by missing it. The snapshot must be
// rejected instead of silently widening the grant.
func TestCorruptScopeSnapshotFailsClosed(t *testing.T) {
	record, invocation := validGrantFixture("2026-01-01T01:00:00Z")
	record.ScopeSnapshotJSON = `{"products": [` // truncated, unparseable
	now := mustTime(t, "2026-01-01T00:00:00Z")

	service := &Service{}
	if _, err := service.validateGrantRecord(record, invocation, now); err == nil {
		t.Fatal("expected a corrupt scope snapshot to be rejected, but it was accepted")
	}
}

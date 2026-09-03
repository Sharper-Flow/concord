package agent

import (
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/store"
)

// An unavailable store maps to the public kind "unreachable", and the envelope
// contract pairs that kind with authority "unreachable" and no freshness or
// watermark. A core error that keeps the base's authoritative claim cannot
// marshal, so the caller sees a marshal fault instead of the refusal the core
// decided (issue #768).
func TestUnavailableStoreRefusalMarshalsAsUnreachable(t *testing.T) {
	failure := &store.Failure{
		Kind:           store.KindUnavailable,
		Op:             "rebuild_knowledge_index",
		Detail:         "cannot clear git-derived law_subjects",
		RetrySafe:      true,
		RecoveryAction: "retry once the database is writable",
	}
	base := NewBase("unreachable-1", "concord_domain", "list")
	base.Freshness = &Freshness{ObservedAt: time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC)}
	base.SourceVersionWatermark = []Watermark{{SourceKind: "product_memory", SourceID: "sqlite", Version: "1"}}
	out := failureEnvelope(base, failure)
	if out.Error == nil || out.Error.Kind != "unreachable" {
		t.Fatalf("error=%+v, want kind unreachable", out.Error)
	}
	if out.Authority != AuthorityUnreachable {
		t.Fatalf("authority=%q, want %q", out.Authority, AuthorityUnreachable)
	}
	if out.Freshness != nil || len(out.SourceVersionWatermark) != 0 {
		t.Fatalf("an unreachable envelope carries freshness=%v watermark=%v", out.Freshness, out.SourceVersionWatermark)
	}
	if _, err := out.Encode(); err != nil {
		t.Fatalf("an unavailable-store refusal must be deliverable, got %v", err)
	}
	if out.Error.Message != failure.Detail || !out.Error.RetrySafe {
		t.Fatalf("refusal detail lost: %+v", out.Error)
	}
}

// Every other core error keeps the authoritative claim: the core answered, and
// the answer is a typed refusal.
func TestCoreErrorKeepsAuthoritativeForOtherKinds(t *testing.T) {
	out := coreError(NewBase("other-1", "concord_domain", "list"), "unknown_scope", "no Product", "contact_operator", false)
	if out.Authority != AuthorityAuthoritative {
		t.Fatalf("authority=%q, want authoritative", out.Authority)
	}
	if _, err := out.Encode(); err != nil {
		t.Fatal(err)
	}
}

package agent

import (
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

// A store kind whose value is already a public kind must reach the caller as
// that kind. mapFailureKind once defaulted every unmatched kind to
// internal_error, so a typed refusal such as not_terminal arrived as an
// internal fault with contact_operator, and the store's own remedy was
// discarded. The public vocabulary is generated from the envelope schema, so
// this enumerates it rather than a hand list: any store kind naming a public
// kind that maps elsewhere fails here, today and on the next kind added.
func TestStoreKindNamingAPublicKindMapsToItself(t *testing.T) {
	for _, public := range store.TypedErrorKinds() {
		kind := store.FailureKind(public)
		if got := mapFailureKind(kind); got != public {
			t.Errorf("store kind %q names public kind %q but maps to %q", kind, public, got)
		}
	}
}

// publicRecovery's default is reserved for a kind the vocabulary added without
// a recovery decision. Every current public kind must resolve by a named
// case or a coupling, never by that default. This gives each kind an invalid
// proposal and asserts the answer is deliberate: the kinds whose honest
// answer is contact_operator are listed here by name, so a new kind that
// silently falls to the default fails this test.
func TestEveryPublicKindHasADeliberateRecovery(t *testing.T) {
	contactByDesign := map[string]bool{"unauthorized": true, "unreachable": true, "internal_error": true, "unknown_scope": true, "malformed_response": true, "transport_failure": true, "outcome_mismatch": true}
	for _, kind := range store.TypedErrorKinds() {
		got := publicRecovery(kind, "free prose the store proposed")
		if got == "contact_operator" && !contactByDesign[kind] {
			t.Errorf("public kind %q resolves to contact_operator by default; name its recovery or add it to contactByDesign with a reason", kind)
		}
	}
}

// The remedy the store proposes survives when the kind is honest. With the
// kind collapsed to internal_error, publicRecovery forced contact_operator;
// with not_terminal, a valid proposal passes through and an invalid one
// resolves by the kind, never by a fault it did not raise.
func TestNotTerminalRefusalKeepsAnActionableRecovery(t *testing.T) {
	public := mapFailureKind(store.KindNotTerminal)
	if public != "not_terminal" {
		t.Fatalf("mapFailureKind(KindNotTerminal) = %q", public)
	}
	if got := publicRecovery(public, "reread_entities"); got != "reread_entities" {
		t.Errorf("a valid proposed action must survive, got %q", got)
	}
	if got := publicRecovery(public, "promote the observation through capture instead"); got == "contact_operator" {
		t.Errorf("free-prose proposal on a typed refusal resolved to contact_operator; the kind should decide")
	}
}

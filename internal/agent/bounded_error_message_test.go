package agent

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// The envelope's message bound is the boundary a refusal detail must survive.
// A detail already inside the budget crosses unchanged — which is how the
// recurrence refusal's budgeted detail keeps its count and research route
// whole — and a detail that exceeds the budget is cut with a marker, never
// shortened silently. The refusal that reaches the agent names the count and
// the route, or says that it was cut (CD-0211 D2/D3).
func TestBoundedErrorMessageKeepsBudgetAndMarksCut(t *testing.T) {
	t.Parallel()
	within := strings.Repeat("a", 1000)
	if got := boundedErrorMessage(within); got != within {
		t.Fatalf("message inside the budget was altered to %d bytes", len(got))
	}
	over := strings.Repeat("b", 1500)
	got := boundedErrorMessage(over)
	if len(got) > 1000 {
		t.Fatalf("cut message measures %d bytes, want at most 1000", len(got))
	}
	if !strings.HasSuffix(got, "… [truncated]") {
		t.Fatalf("cut message %q carries no truncation marker", got)
	}
	if !utf8.ValidString(got) {
		t.Fatal("cut message is not valid UTF-8")
	}
	// A multi-byte rune straddling the cut byte is dropped whole, not emitted
	// as a partial encoding.
	straddled := "z" + strings.Repeat("é", 700)
	cut := boundedErrorMessage(straddled)
	if !utf8.ValidString(cut) {
		t.Fatal("a mid-rune cut left an invalid byte sequence")
	}
}

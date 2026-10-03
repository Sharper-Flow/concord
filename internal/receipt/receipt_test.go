package receipt

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

func criterion(predicateID string, ordinal int, outcomeKind string, payload string, verdictKind string) store.WorkPinVerifiedCriterion {
	return store.WorkPinVerifiedCriterion{
		PredicateID:    predicateID,
		Ordinal:        ordinal,
		OutcomeKind:    outcomeKind,
		OutcomePayload: json.RawMessage(payload),
		VerdictKind:    verdictKind,
	}
}

// closure wraps a pin in the closure value Render takes.
func closure(pin store.WorkPin) store.WorkClosure {
	return store.WorkClosure{Pin: pin}
}

// Golden test: the closure receipt's exact bytes are fixed here, not in
// prose, and this is the one test that owns the emoji rules. The glyphs
// U+1F6EB and U+2705 are double-width and U+2713 is narrow, so no test may
// assert column alignment over any of them: alignment belongs to the host
// markdown renderer. The table is single-column: the plane line is its
// header row, the summary row carries the work title with ✅, the CD-0202
// problem and shipped rows carry ❓ and 📦 before the criteria, and the
// left-out and follow-up rows carry ➖ and 🔗 after them.
func TestRenderPinsTheClosureReceiptBytes(t *testing.T) {
	t.Parallel()
	value := closure(store.WorkPin{
		WorkID:             "work-6f97c48d3e6983bf",
		LinearIssueKey:     "CON-392",
		Lifecycle:          "completed",
		ProjectDisplayName: "Concord",
		Title:              "Product-owned closure receipt formatter with CLI verb",
		VerifiedCriteria: []store.WorkPinVerifiedCriterion{
			criterion("predicate:check", 0, "check", `{"kind":"check","check_ref":"check:repo:full-verify","immutable_subject_ref":"commit:abc","expected_result":"pass"}`, "ok"),
			criterion("predicate:exists", 1, "exists", `{"kind":"exists","subjects":["internal/receipt"],"surface":"cli_verb"}`, "ok"),
			criterion("predicate:absent", 2, "absent", `{"kind":"absent","subjects":["adapter:closureCell"],"surface":"adapter_cleanup","distinguish_from":["archived"]}`, "ok"),
			criterion("predicate:outcome", 3, "outcome", `{"kind":"outcome","allowed":["completed"]}`, "ok"),
		},
	})
	value.Problem = "The closure receipt shows only the status table, so the operator cannot see what shipped."
	value.DeliveryArtifact = "commit:abc"
	value.OutOfScope = []string{"The launcher stays untouched.", "No release automation changes."}
	value.FollowUps = []store.WorkClosureFollowUp{
		{WorkID: "work-followup1", Title: "Follow the error rate for one week", LinearIssueKey: "CON-401"},
		{WorkID: "work-followup2", Title: "Follow-up without a Linear link"},
	}
	want := "| 🛫 CON-392 Complete (work-6f97c48d3e6983bf) |\n" +
		"| :-- |\n" +
		"| ✅ Product-owned closure receipt formatter with CLI verb |\n" +
		"| ❓ The closure receipt shows only the status table, so the operator cannot see what shipped. |\n" +
		"| 📦 commit:abc |\n" +
		"| ✓ check check:repo:full-verify · pass |\n" +
		"| ✓ exists internal/receipt · cli_verb |\n" +
		"| ✓ absent adapter:closureCell · adapter_cleanup |\n" +
		"| ✓ outcome completed |\n" +
		"| ➖ The launcher stays untouched. |\n" +
		"| ➖ No release automation changes. |\n" +
		"| 🔗 CON-401 · Follow the error rate for one week |\n" +
		"| 🔗 work-followup2 · Follow-up without a Linear link |"
	if got := Render(value); got != want {
		t.Fatalf("receipt bytes drifted:\ngot:\n%s\nwant:\n%s", got, want)
	}
}

// A closure that carries none of the CD-0202 facts renders exactly the
// bytes the pin alone produced before the sections existed.
func TestRenderKeepsThePinBytesWithoutClosureFacts(t *testing.T) {
	t.Parallel()
	value := closure(store.WorkPin{
		WorkID:         "work-6f97c48d3e6983bf",
		LinearIssueKey: "CON-392",
		Lifecycle:      "completed",
		Title:          "Product-owned closure receipt formatter with CLI verb",
		VerifiedCriteria: []store.WorkPinVerifiedCriterion{
			criterion("predicate:check", 0, "check", `{"kind":"check","check_ref":"check:repo:full-verify","immutable_subject_ref":"commit:abc","expected_result":"pass"}`, "ok"),
		},
	})
	want := "| 🛫 CON-392 Complete (work-6f97c48d3e6983bf) |\n" +
		"| :-- |\n" +
		"| ✅ Product-owned closure receipt formatter with CLI verb |\n" +
		"| ✓ check check:repo:full-verify · pass |"
	if got := Render(value); got != want {
		t.Fatalf("legacy bytes drifted:\ngot:\n%s\nwant:\n%s", got, want)
	}
}

func TestRenderFallsBackToTheWorkIDWithoutALinearKey(t *testing.T) {
	t.Parallel()
	value := closure(store.WorkPin{WorkID: "work-6f97c48d3e6983bf", Lifecycle: "completed"})
	want := "🛫 work-6f97c48d3e6983bf Complete"
	if got := Render(value); got != want {
		t.Fatalf("header = %q, want %q", got, want)
	}
}

// An ambiguous contract projection, a contract with no renderable predicate,
// or a payload outside the typed shapes all degrade by omission: the receipt
// keeps its header and prints no table.
func TestRenderDegradesToTheHeaderAloneWithoutCriteria(t *testing.T) {
	t.Parallel()
	value := closure(store.WorkPin{WorkID: "work-1", LinearIssueKey: "CON-42", Lifecycle: "completed"})
	if got, want := Render(value), "🛫 CON-42 Complete (work-1)"; got != want {
		t.Fatalf("header-only receipt = %q, want %q", got, want)
	}
	value.Pin.VerifiedCriteria = []store.WorkPinVerifiedCriterion{}
	if got := Render(value); got != "🛫 CON-42 Complete (work-1)" {
		t.Fatalf("empty-criteria receipt = %q, want the header alone", got)
	}
	value.Pin.VerifiedCriteria = []store.WorkPinVerifiedCriterion{
		criterion("predicate:broken", 0, "exists", `{"kind":"exists","subjects":[],"surface":"cli_verb"}`, "ok"),
		criterion("predicate:unknown", 1, "artifact", `{"kind":"artifact"}`, "ok"),
	}
	if got := Render(value); got != "🛫 CON-42 Complete (work-1)" {
		t.Fatalf("unlabelable criteria rendered %q, want the header alone", got)
	}
}

// An empty section renders no row: a blank problem, a closure without a
// delivery assertion, empty out_of_scope entries, and a follow-up without a
// usable identifier each drop instead of printing a bare mark.
func TestRenderDropsEmptyClosureSections(t *testing.T) {
	t.Parallel()
	value := closure(store.WorkPin{WorkID: "work-1", Lifecycle: "completed"})
	value.Problem = "   "
	value.DeliveryArtifact = ""
	value.OutOfScope = []string{"  ", ""}
	value.FollowUps = []store.WorkClosureFollowUp{{WorkID: " ", Title: "A title"}}
	if got := Render(value); got != "🛫 work-1 Complete" {
		t.Fatalf("empty sections rendered %q, want the header alone", got)
	}
}

func TestRenderDropsUnverifiedVerdicts(t *testing.T) {
	t.Parallel()
	value := closure(store.WorkPin{
		WorkID:    "work-1",
		Lifecycle: "completed",
		VerifiedCriteria: []store.WorkPinVerifiedCriterion{
			criterion("predicate:mismatch", 0, "check", `{"kind":"check","check_ref":"check:one","immutable_subject_ref":"commit:abc","expected_result":"pass"}`, "outcome_mismatch"),
			criterion("predicate:thin", 1, "exists", `{"kind":"exists","subjects":["internal/receipt"],"surface":"cli_verb"}`, "insufficient_evidence"),
		},
	})
	if got := Render(value); got != "🛫 work-1 Complete" {
		t.Fatalf("unverified criteria rendered %q, want the header alone", got)
	}
}

// The receipt is a completion receipt. A cancelled or superseded pin renders
// nothing at all, whatever the projection carries.
func TestRenderEmitsNothingOutsideTheCompletedLifecycle(t *testing.T) {
	t.Parallel()
	value := closure(store.WorkPin{
		WorkID:             "work-1",
		Lifecycle:          "cancelled",
		ProjectDisplayName: "Concord",
		Title:              "Repair the adapter",
	})
	value.Problem = "A recorded problem."
	value.DeliveryArtifact = "commit:abc"
	if got := Render(value); got != "" {
		t.Fatalf("cancelled pin rendered %q, want nothing", got)
	}
	value.Pin.Lifecycle = "superseded"
	if got := Render(value); got != "" {
		t.Fatalf("superseded pin rendered %q, want nothing", got)
	}
	value.Pin.Lifecycle = "in_progress"
	if got := Render(value); got != "" {
		t.Fatalf("in-progress pin rendered %q, want nothing", got)
	}
}

// The single-column cell holds at most sixty-four code points for a
// criterion label. A longer label abbreviates to the width minus one plus
// an ellipsis, visibly.
func TestRenderAbbreviatesLabelsToTheCellWidth(t *testing.T) {
	t.Parallel()
	value := closure(store.WorkPin{
		WorkID:    "work-1",
		Lifecycle: "completed",
		VerifiedCriteria: []store.WorkPinVerifiedCriterion{
			criterion("predicate:long", 0, "exists", `{"kind":"exists","subjects":["`+strings.Repeat("a", 60)+`"],"surface":"work_pin"}`, "ok"),
		},
	})
	want := "| 🛫 work-1 Complete |\n" +
		"| :-- |\n" +
		"| ✓ exists " + strings.Repeat("a", 56) + "… |"
	if got := Render(value); got != want {
		t.Fatalf("abbreviated receipt =\n%s\nwant:\n%s", got, want)
	}
	label := "exists " + strings.Repeat("a", 56) + "…"
	if len([]rune(label)) != labelMaxColumns {
		t.Fatalf("abbreviated label holds %d code points, want %d", len([]rune(label)), labelMaxColumns)
	}
}

// The prose cells CD-0202 binds hold at most one hundred twenty code points.
// A longer problem, excluded-scope entry, or follow-up title abbreviates to
// the width minus one plus an ellipsis, so one long statement cannot
// dominate the banner.
func TestRenderAbbreviatesProseRowsToTheProseWidth(t *testing.T) {
	t.Parallel()
	value := closure(store.WorkPin{WorkID: "work-1", Lifecycle: "completed"})
	value.Problem = strings.Repeat("b", 130)
	value.OutOfScope = []string{strings.Repeat("c", 130)}
	value.FollowUps = []store.WorkClosureFollowUp{{
		WorkID: "work-2",
		Title:  strings.Repeat("d", 130),
	}}
	want := "| 🛫 work-1 Complete |\n" +
		"| :-- |\n" +
		"| ❓ " + strings.Repeat("b", 119) + "… |\n" +
		"| ➖ " + strings.Repeat("c", 119) + "… |\n" +
		"| 🔗 work-2 · " + strings.Repeat("d", 119) + "… |"
	if got := Render(value); got != want {
		t.Fatalf("prose receipt =\n%s\nwant:\n%s", got, want)
	}
	if len([]rune(strings.Repeat("b", 119)+"…")) != proseMaxColumns {
		t.Fatalf("abbreviated prose holds the wrong width, want %d", proseMaxColumns)
	}
}

// A pipe or a control character inside a dynamic field can never corrupt the
// header line or a cell.
func TestRenderSanitizesHeaderFields(t *testing.T) {
	t.Parallel()
	value := closure(store.WorkPin{WorkID: "work-1", LinearIssueKey: "CON-1\u0007|x", Lifecycle: "completed"})
	if got, want := Render(value), "🛫 CON-1x Complete (work-1)"; got != want {
		t.Fatalf("sanitized header = %q, want %q", got, want)
	}
	value.Pin.LinearIssueKey = ""
	value.Pin.VerifiedCriteria = []store.WorkPinVerifiedCriterion{
		criterion("predicate:piped", 0, "outcome", `{"kind":"outcome","allowed":["com|pleted\u0000"]}`, "ok"),
	}
	value.OutOfScope = []string{"excluded | scope\u0000"}
	value.DeliveryArtifact = "commit:ab|c"
	want := "| 🛫 work-1 Complete |\n" +
		"| :-- |\n" +
		"| 📦 commit:abc |\n" +
		"| ✓ outcome completed |\n" +
		"| ➖ excluded  scope |"
	if got := Render(value); got != want {
		t.Fatalf("sanitized cell receipt =\n%s\nwant:\n%s", got, want)
	}
}

// The receipt never carries a code fence: the block is markdown the host
// renderer owns.
func TestRenderNeverEmitsAFence(t *testing.T) {
	t.Parallel()
	value := closure(store.WorkPin{
		WorkID:    "work-1",
		Lifecycle: "completed",
		VerifiedCriteria: []store.WorkPinVerifiedCriterion{
			criterion("predicate:primary", 0, "check", `{"kind":"check","check_ref":"check:one","immutable_subject_ref":"commit:abc","expected_result":"pass"}`, "ok"),
		},
	})
	if got := Render(value); strings.Contains(got, "```") {
		t.Fatalf("receipt carries a fence: %q", got)
	}
}

package store

import (
	"encoding/json"
	"strings"
	"testing"
)

// completeWorkflow requires a verdict reference to equal a bound
// immutable_subject_ref exactly (workflow_completion.go, "verdict refs must
// equal a bound immutable_subject_ref"). Verdict references are drawn from the
// reference domain: ValidReference admits 2 to 128 bytes with no whitespace.
//
// A bind that admits a wider domain therefore mints evidence no verdict can
// ever name. The binding succeeds, the evidence is durable, and the reference
// is unreachable from record_verdict and complete for the life of the item.
// Both surfaces must draw the locator from one domain.
func TestBindEvidenceRefusesLocatorTheVerdictCannotName(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		locator string
	}{
		{name: "whitespace", locator: "go test ./internal/store/ -run TestThing -count=1"},
		{name: "over_reference_length", locator: "https://example.test/" + strings.Repeat("a", 120)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if ValidReference(tc.locator) {
				t.Fatalf("fixture locator is already a valid reference, so it proves nothing: %q", tc.locator)
			}
			workID := "evidence-bind-domain-" + tc.name
			s := seedMandateRecoveryItem(t, workID)

			payload, err := json.Marshal(map[string]any{
				"evidence_kind":         "verification",
				"immutable_subject_ref": tc.locator,
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := runVerdictAction(t, s, workID, "bind_evidence", json.RawMessage(payload), 0); err == nil {
				t.Fatalf("bind_evidence admitted a locator no verdict can name: %q", tc.locator)
			} else if !strings.Contains(err.Error(), "reference rule") {
				t.Fatalf("bind_evidence refusal does not name the reference rule: %v", err)
			}
		})
	}
}

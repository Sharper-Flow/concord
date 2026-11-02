package agent

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

// CD-0016: empty peer signals stay out of the pinned
// projection, and an unchanged snapshot renders identical bytes.
func TestContinuityPayloadOmitsEmptyPeerSignals(t *testing.T) {
	t.Parallel()
	payload := ContinuityPayload(store.ContinuitySnapshot{})
	pinned, ok := payload["pinned"].(map[string]any)
	if !ok {
		t.Fatalf("pinned payload type = %T", payload["pinned"])
	}
	if _, present := pinned["unresolved_overlaps"]; present {
		t.Fatalf("empty unresolved_overlaps was emitted: %#v", pinned["unresolved_overlaps"])
	}
}

func TestContinuityBytesAreStableAcrossRenders(t *testing.T) {
	t.Parallel()
	snapshot := store.ContinuitySnapshot{WorkID: "work-1", ProductIdentity: []string{"product-1"}, WorkflowStep: "execution", StepActions: []string{}, Boundaries: []store.ContextBoundary{}, SpecMandate: []string{}}
	first, err := json.Marshal(ContinuityPayload(snapshot))
	if err != nil {
		t.Fatal(err)
	}
	second, err := json.Marshal(ContinuityPayload(snapshot))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatalf("unchanged snapshot rendered different bytes")
	}
	if bytes.Contains(first, []byte("unresolved_overlaps")) {
		t.Fatalf("empty peer signals leaked into the payload: %s", first)
	}
}

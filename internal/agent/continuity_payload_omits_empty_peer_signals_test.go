package agent

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

// Empty peer signals stay out of the pinned projection.
func TestContinuityPayloadOmitsEmptyPeerSignals(t *testing.T) {
	payload := ContinuityPayload(store.ContinuitySnapshot{})
	pinned, ok := payload["pinned"].(map[string]any)
	if !ok {
		t.Fatalf("pinned payload type = %T", payload["pinned"])
	}
	for _, field := range []string{"unresolved_overlaps", "compatible_law_amendments"} {
		if _, present := pinned[field]; present {
			t.Fatalf("empty peer signal %q was emitted: %#v", field, pinned[field])
		}
	}
}

func TestContinuityRepinBytesAreStableAcrossRenders(t *testing.T) {
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
	if bytes.Contains(first, []byte("unresolved_overlaps")) || bytes.Contains(first, []byte("compatible_law_amendments")) {
		t.Fatalf("empty peer signals leaked into the payload: %s", first)
	}
}

func TestContinuityPayloadValidatesEmptyOverlapIntersections(t *testing.T) {
	snapshot := fullyPopulatedContinuitySnapshot()
	overlap := &snapshot.UnresolvedOverlaps[0]
	overlap.SharedLawIDs = []string{}
	overlap.SharedDomainModifications = []string{}
	overlap.SharedRelationTuples = []store.WorkflowDomainRelationTuple{}
	overlap.SharedLawCount = 0
	overlap.SharedDomainModificationCount = 0
	overlap.SharedRelationTupleCount = 0
	raw, err := json.Marshal(ContinuityPayload(snapshot))
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateOperationPayload("concord_work_trace", "continuity", raw, true); err != nil {
		t.Fatalf("empty overlap intersections rejected: %v", err)
	}
}

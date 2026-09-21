package store

import (
	"context"
	"strings"
	"testing"
)

// The store details bound and the adapter detail bound must name the same
// rule. The store counts UTF-8 bytes (len) but the refusal said "characters",
// so a lane author diagnosing a refusal read the wrong rule into the guard and
// reproduced the adapter-vs-store split it stems from (CON-354). The bound at
// internal/store/worker_lanes.go counts bytes; the refusal must say bytes.

func TestWorkerDetailRefusalNamesUTF8Bytes(t *testing.T) {
	s := openTemp(t)
	lane := BuiltinLaneDefinitions()[0]
	attemptID := "detail-utf8-attempt"
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{workerDispatchEvent("evidence-utf8", attemptID, lane, nil)}}); err != nil {
		t.Fatal(err)
	}
	// 257 code points: one UTF-16 unit each, so the adapter's code-unit count
	// is under maxLength 512, while the store's byte count is 514, over 512.
	heavyDetail := strings.Repeat("\u00e9", 257)
	evidence := []WorkerReportEvidence{{Obligation: lane.EvidenceObligations[0], Detail: heavyDetail}}
	complete := workerCompleteEventV2("evidence-utf8", "evidence-utf8-complete", attemptID, preferredModelForLane(lane), WorkerEvidenceReported, evidence)
	err := ApplyOperation(context.Background(), s, Operation{Events: []Event{complete}})
	if err == nil {
		t.Fatal("a detail carrying 514 UTF-8 bytes was accepted by the store, so the bound is not the byte rule this test asserts")
	}
	refusal := failureDetail(t, err)
	if !strings.Contains(refusal, "UTF-8 bytes") {
		t.Fatalf("refusal = %q, want it to name the rule it enforces (UTF-8 bytes)", refusal)
	}
}

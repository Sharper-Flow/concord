package store

import (
	"encoding/json"
	"strings"
	"testing"
)

// The checks repair metadata is writer-bounded like the acceptance oracle:
// its canonical JSON bytes measure at most WorkerJobChecksRepairMaxBytes, and
// an over-bound repair refuses whole instead of truncating a named revision's
// recorded past. An absent metadata member keeps the historical shape and
// every historical digest unchanged.
func TestChecksRepairMetadataRefusesOverBoundWhole(t *testing.T) {
	const workID = "checks-repair-metadata-bound"
	fixture := seedChecksRepairFixture(t, workID, "repair")
	s := fixture.store
	defer s.Close()
	named := WorkerJobRecordedPayload{
		JobID: "job:metadata-bound", Revision: 1, ContractVersion: 1,
		Objective: "bounded objective", StoppingCondition: "the recorded checks pass",
		PathScope: []string{"internal/store"}, PredicateIDs: []string{}, Checks: []string{"concord check --bound --exit-code=never"},
		Prerequisites: []WorkerJobPrerequisite{}, UnresolvedRefs: []string{},
		Readiness: &WorkerJobReadiness{Ready: true, Evidence: []string{"evidence:metadata-bound"}},
	}
	named.Digest = DeriveWorkerJobDigest(named)
	if err := applyWorkerJobEventForTest(t, s, workID, named); err != nil {
		t.Fatalf("record the named revision: %v", err)
	}
	if err := applyWorkerJobEventForTest(t, s, workID, func() WorkerJobRecordedPayload {
		failed := named
		failed.Revision = 2
		failed.Digest = DeriveWorkerJobDigest(failed)
		return failed
	}()); err != nil {
		t.Fatalf("record the failed revision: %v", err)
	}

	// A filler reason whose canonical bytes land one past the writer bound:
	// the reason sits inside the metadata object beside the named entry, so
	// the filler length backs the entry's share out exactly.
	entry := WorkerJobChecksRepairEntry{
		WorkerJob: WorkerJobBinding{JobID: "job:metadata-bound", Revision: 1, Digest: named.Digest},
		Checks:    named.Checks,
	}
	entryBytes, err := json.Marshal(entry)
	if err != nil {
		t.Fatal(err)
	}
	over := WorkerJobRecordedPayload{
		JobID: "job:metadata-bound", Revision: 3, ContractVersion: 1,
		Objective: named.Objective, StoppingCondition: named.StoppingCondition,
		PathScope: named.PathScope, PredicateIDs: named.PredicateIDs, Checks: []string{"go test ./internal/store/ -run TestChecksRepairBound"},
		Prerequisites: []WorkerJobPrerequisite{}, UnresolvedRefs: []string{},
		Readiness: named.Readiness,
		ChecksRepair: &WorkerJobChecksRepair{
			Reason:    strings.Repeat("a", WorkerJobChecksRepairMaxBytes),
			Revisions: []WorkerJobChecksRepairEntry{entry},
		},
	}
	metadata, err := json.Marshal(over.ChecksRepair)
	if err != nil {
		t.Fatal(err)
	}
	if len(metadata) <= WorkerJobChecksRepairMaxBytes {
		t.Fatalf("the fixture metadata measures %d bytes, want over the %d-byte writer bound; shrink the filler by the entry share %d", len(metadata), WorkerJobChecksRepairMaxBytes, len(entryBytes))
	}
	over.Digest = DeriveWorkerJobDigest(over)
	if err := applyWorkerJobEventForTest(t, s, workID, over); err == nil {
		t.Fatal("an over-bound checks repair metadata was recorded")
	} else if !strings.Contains(err.Error(), "exceeds") && !strings.Contains(err.Error(), "bound") {
		t.Fatalf("over-bound metadata error = %v, want the writer-bound refusal", err)
	}

	// An absent metadata member keeps the historical shape: the bound gates
	// nothing a pre-repair revision carries.
	plain := WorkerJobRecordedPayload{
		JobID: "job:metadata-bound-plain", Revision: 1, ContractVersion: 1,
		Objective: "bounded objective", StoppingCondition: "the recorded checks pass",
		PathScope: named.PathScope, PredicateIDs: named.PredicateIDs, Checks: named.Checks,
		Prerequisites: []WorkerJobPrerequisite{}, UnresolvedRefs: []string{},
		Readiness: named.Readiness,
	}
	plain.Digest = DeriveWorkerJobDigest(plain)
	if err := applyWorkerJobEventForTest(t, s, workID, plain); err != nil {
		t.Fatalf("a revision without checks repair metadata recorded: %v", err)
	}
}

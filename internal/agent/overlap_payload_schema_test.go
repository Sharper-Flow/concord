package agent

import (
	"encoding/json"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

func TestLawOnlyDomainOverlapReadPayloadValidates(t *testing.T) {
	payload := fullyPopulatedDomainPayloads(t).overlaps
	payload.Pairs = []store.DomainOverlapPair{{
		FromWorkID: "work-1", ToWorkID: "work-2", SharedDomainIDs: []string{},
		SharedLawIDs: []string{"spec:one"}, ResolutionState: "absent",
	}}
	for _, null := range []bool{false, true} {
		if null {
			payload.Pairs[0].SharedDomainIDs = nil
		}
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		err = ValidateOperationPayload("concord_domain", "overlaps", raw, true)
		if !null && err != nil {
			t.Fatalf("law-only read payload must validate: %v", err)
		}
		if null && err == nil {
			t.Fatal("null shared Domains must not validate")
		}
	}
}

func TestLawOnlyDomainOverlapContinuityPayloadValidates(t *testing.T) {
	snapshot := fullyPopulatedContinuitySnapshot()
	row := &snapshot.UnresolvedOverlaps[0]
	row.SharedAffectedDomainIDs = []string{}
	row.SharedAffectedDomainCount = 0
	row.SharedDomainModifications = []string{}
	row.SharedDomainModificationCount = 0
	row.SharedRelationTuples = []store.WorkflowDomainRelationTuple{}
	row.SharedRelationTupleCount = 0
	row.OverlapClasses = []string{"law_write"}
	for _, null := range []bool{false, true} {
		if null {
			row.SharedAffectedDomainIDs = nil
		}
		raw, err := json.Marshal(ContinuityPayload(snapshot))
		if err != nil {
			t.Fatal(err)
		}
		err = ValidateOperationPayload("concord_work_trace", "continuity", raw, true)
		if !null && err != nil {
			t.Fatalf("law-only continuity payload must validate: %v", err)
		}
		if null && err == nil {
			t.Fatal("null shared affected Domains must not validate")
		}
	}
}

func TestArchitectureOverlapClassIsRejected(t *testing.T) {
	snapshot := fullyPopulatedContinuitySnapshot()
	snapshot.UnresolvedOverlaps[0].OverlapClasses = []string{"architecture"}
	raw, err := json.Marshal(ContinuityPayload(snapshot))
	if err != nil {
		t.Fatal(err)
	}
	t.Run("continuity", func(t *testing.T) {
		if err := ValidateOperationPayload("concord_work_trace", "continuity", raw, true); err == nil {
			t.Fatal("architecture class must not validate in continuity")
		}
	})
	t.Run("refusal", func(t *testing.T) {
		failure := storeOverlapFailure("resolve_overlap")
		failure.DomainOverlap.Overlaps[0].OverlapClasses = []string{"architecture"}
		out := failureEnvelope(NewBase("overlap-class", "concord_work_transition", "workflow_action"), failure)
		if _, err := out.Encode(); err == nil {
			t.Fatal("architecture class must not validate in a refusal")
		}
	})
}

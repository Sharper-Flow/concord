package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// A law-only pair shares a law write while its affected Domain sets are
// disjoint. The guard refuses it, so the overlap read reports it, and the
// shared Domain list is an empty array rather than null.
func TestOverlapLawOnlyPairReportsEmptySharedDomains(t *testing.T) {
	s, _, binding, _, _, _ := architectureValidationFixture(t, "content-left")
	seedWork(t, s, "content-right")
	binding.DomainModifies = []string{}
	binding.DomainRelationModifies = []WorkflowDomainRelationModification{}
	binding.AffectedDomainIDs = []string{"child"}
	seedProductChangingContract(t, s, "content-left", binding)
	binding.AffectedDomainIDs = []string{"root"}
	binding.HomeDomainID = "root"
	seedProductChangingContract(t, s, "content-right", binding)
	// Synthetic historical footprints: current authoring requires the
	// modified law's home among the affected Domains, but replayed contracts
	// carry no such join.
	overlapFixtureExec(t, s, `INSERT INTO workflow_contract_law_modifications(work_id,contract_version,law_id) VALUES('content-left',1,'spec:one'),('content-right',1,'spec:one')`)

	var failure *Failure
	if err := InspectWorkflowDomainOverlap(context.Background(), s, "content-left"); !errors.As(err, &failure) || failure.Kind != KindDomainOverlap {
		t.Fatalf("law-only admission = %v, want domain_overlap refusal", err)
	}
	read, err := s.QueryDomainOverlaps(context.Background(), DomainOverlapsRequest{Product: "product"})
	if err != nil {
		t.Fatal(err)
	}
	if len(read.Pairs) != 1 {
		t.Fatalf("pairs = %+v, want the law-only pair", read.Pairs)
	}
	pair := read.Pairs[0]
	if pair.FromWorkID != "content-left" || pair.ToWorkID != "content-right" || len(pair.SharedLawIDs) != 1 || pair.SharedLawIDs[0] != "spec:one" {
		t.Fatalf("pair = %+v, want content-left/content-right sharing spec:one", pair)
	}
	encoded, err := json.Marshal(pair)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"shared_domain_ids":[]`) {
		t.Fatalf("pair payload = %s, want \"shared_domain_ids\":[]", encoded)
	}
	snapshot, err := ReadWorkflowContinuity(context.Background(), s, ContinuityRequest{Work: "content-left", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.UnresolvedOverlaps) != 1 || snapshot.UnresolvedOverlaps[0].SharedAffectedDomainIDs == nil || len(snapshot.UnresolvedOverlaps[0].SharedAffectedDomainIDs) != 0 {
		t.Fatalf("law-only continuity must preserve an empty, non-null Domain list: %+v", snapshot.UnresolvedOverlaps)
	}
	encoded, err = json.Marshal(snapshot.UnresolvedOverlaps[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"shared_affected_domain_ids":[]`) {
		t.Fatalf("continuity payload = %s, want \"shared_affected_domain_ids\":[]", encoded)
	}
}

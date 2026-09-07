package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// Issue #765: the pinned projection re-pins the Domain overlaps that will
// refuse this work's next consequential mutation.
func TestContinuityRepinsUnresolvedDomainOverlaps(t *testing.T) {

	ctx := context.Background()
	s, _ := seedOverlapProjection(t, "continuity-overlap-left", "continuity-overlap-right", false)
	definition, err := BuiltinWorkflowDefinitionForRef("workflow.implementation")
	if err != nil {
		t.Fatal(err)
	}
	actor := WorkflowActor{PrincipalRef: "principal:continuity", ClientRef: "client:continuity", AgentRef: "agent:continuity", SessionRef: "session:continuity", ActorClass: ActorAgent}
	for _, workID := range []string{"continuity-overlap-left", "continuity-overlap-right"} {
		if err := s.Transact(ctx, func(transaction *Transaction) error {
			return InitializeWorkflowTx(ctx, transaction, WorkflowInitializationRequest{WorkID: workID, Definition: definition, Actor: actor})
		}); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err := ReadWorkflowContinuity(ctx, s, ContinuityRequest{Work: "continuity-overlap-left", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.UnresolvedOverlaps) != 1 {
		t.Fatalf("unresolved overlaps=%+v", snapshot.UnresolvedOverlaps)
	}
	overlap := snapshot.UnresolvedOverlaps[0]
	if overlap.FromWorkID != "continuity-overlap-left" || overlap.ToWorkID != "continuity-overlap-right" || overlap.ResolutionState != "unresolved" || len(overlap.RecoveryActions) == 0 {
		t.Fatalf("overlap=%+v", overlap)
	}
	// An architecture-only overlap shares no laws or relations. Those lists
	// must marshal as empty arrays, not null: the generated envelope schema
	// types each one as an array, and a nil slice fails that validation at
	// the continuity read.
	encoded, err := json.Marshal(overlap)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"shared_law_ids", "shared_domain_modifications", "shared_relation_tuples", "shared_affected_domain_ids"} {
		if strings.Contains(string(encoded), `"`+field+`":null`) {
			t.Fatalf("overlap %s marshals a null list: %s", field, encoded)
		}
	}
	if overlap.SharedLawIDs == nil || overlap.SharedRelationTuples == nil {
		t.Fatalf("overlap carries nil lists: %s", encoded)
	}
}

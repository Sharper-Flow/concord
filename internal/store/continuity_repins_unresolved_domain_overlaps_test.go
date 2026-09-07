package store

import (
	"context"
	"encoding/json"
	"testing"
)

// The pinned projection includes the Domain overlaps that refuse the next
// consequential mutation, with empty intersections encoded as arrays.
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
	raw, err := json.Marshal(overlap)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"shared_law_ids", "shared_relation_tuples"} {
		if string(fields[field]) != "[]" {
			t.Errorf("%s = %s, want []", field, fields[field])
		}
	}
}

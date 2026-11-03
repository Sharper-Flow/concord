package store

import (
	"testing"
	"time"
)

func TestWorkflowForwardLinkRequiresTypedSuccessorAndRejectsCycle(t *testing.T) {
	const source = "forward-link-source"
	const target = "forward-link-target"
	f := seedWorkflowReturnRouteFixtureWithPlan(t, source, WorkflowStepPlanChange, "implement", nil, nil)
	s := f.store
	seedWork(t, s, target)
	if err := s.Transact(t.Context(), func(tx *Transaction) error {
		return InitializeWorkflowTx(t.Context(), tx, WorkflowInitializationRequest{WorkID: target, Actor: f.owner, Now: time.Unix(1, 0).UTC()})
	}); err != nil {
		t.Fatal(err)
	}
	link := func(successor string) error {
		return runVerdictActionAs(t, s, source, "link_successor", mustJSONValue(map[string]any{"successor_work_id": successor}), 0, f.owner)
	}
	before := readWorkVersion(t, s, source)
	err := link(target)
	var failure *Failure
	if !failureAs(err, &failure) || failure.Kind != KindInvalidRelation {
		t.Fatalf("untyped successor = %v, want invalid relation", err)
	}
	if got := readWorkVersion(t, s, source); got != before {
		t.Fatalf("refused link changed version %d to %d", before, got)
	}
	err = link(source)
	if !failureAs(err, &failure) || failure.Kind != KindCycleDetected {
		t.Fatalf("cyclic forward link = %v, want invalid relation", err)
	}
	if got := readWorkVersion(t, s, source); got != before {
		t.Fatalf("refused cycle changed version %d to %d", before, got)
	}
	seedWorkflowPlanFrontier(t, s, target, WorkflowStepPlanFix, "implement")
	if err := link(target); err != nil {
		t.Fatalf("typed forward link refused: %v", err)
	}
	var count int
	if err := s.db.QueryRow(`SELECT count(*) FROM workflow_impact_edges WHERE work_id=? AND target_work_id=? AND edge_kind='forward_link'`, source, target).Scan(&count); err != nil || count != 1 {
		t.Fatalf("forward links=%d err=%v", count, err)
	}
}

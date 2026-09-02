package store

import (
	"context"
	"testing"
)

// seedImportedWork appends a work.created pair whose events carry the
// predecessor import actor, mirroring the import verb's write shape.
func seedImportedWork(t *testing.T, s *Store, workID, actor string) {
	t.Helper()
	if err := ApplyOperation(context.Background(), s, Operation{
		Events: []Event{
			productCreatedEvent("product", "create-product"),
			projectCreatedEvent("project", "create-project"),
			operationEvent("product-project", "product_project.added", SubjectProduct, "product", map[string]any{
				"product_id": "product", "project_id": "project", "role": "primary", "reason": "test",
				"expected_version": 1, "resulting_version": 2,
			}),
		},
		ExpectedVersions: map[SubjectRef]int64{
			VersionRef(SubjectProduct, "product"): 0,
			VersionRef(SubjectProject, "project"): 0,
		},
	}); err != nil {
		t.Fatalf("seed test Project: %v", err)
	}
	created := operationEvent("import-advance-work-"+workID, "work.created", SubjectWorkItem, workID, map[string]any{
		"work_kind": "task", "title": "imported", "value_statement": "imported", "priority": 3,
		"tags": []string{"predecessor-migrated"}, "external_ref": "advance:" + workID,
	})
	created.PayloadVersion = 2
	created.Actor = actor
	membership := operationEvent("import-advance-work-membership-"+workID, "work.memberships_replaced", SubjectWorkItem, workID, map[string]any{
		"memberships":       []map[string]any{{"project_id": "project", "role": "primary"}},
		"expected_version":  1,
		"resulting_version": 2,
	})
	membership.Actor = actor
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{created, membership}}); err != nil {
		t.Fatalf("seed imported work: %v", err)
	}
}

// TestFirstWorkEventByOtherActorProbesConflictEvidence pins the CD-0097 D4
// conflict probe read: an imported work item whose every event carries the
// import actor reports no foreign event, and one Concord-side event by
// another actor is returned with its kind and actor.
func TestFirstWorkEventByOtherActorProbesConflictEvidence(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	const workID = "import-advance-work-change-1"
	seedImportedWork(t, s, workID, "operator:predecessor-import")

	kind, actor, found, err := s.FirstWorkEventByOtherActor(ctx, workID, "operator:predecessor-import")
	if err != nil {
		t.Fatalf("probe clean item: %v", err)
	}
	if found {
		t.Fatalf("clean item reported foreign event %q by %q", kind, actor)
	}

	revised := operationEvent("work-revision-change-1", "work.intent_revised", SubjectWorkItem, workID, map[string]any{
		"title": "re-contracted", "value_statement": "re-contracted", "kind": "task", "priority": 3,
		"tags": []string{"predecessor-migrated"}, "reason": "operator re-contract",
		"expected_version": 2, "resulting_version": 3,
	})
	revised.Actor = "operator"
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{revised}}); err != nil {
		t.Fatalf("append revision: %v", err)
	}

	kind, actor, found, err = s.FirstWorkEventByOtherActor(ctx, workID, "operator:predecessor-import")
	if err != nil {
		t.Fatalf("probe conflicted item: %v", err)
	}
	if !found {
		t.Fatal("conflicted item reported no foreign event")
	}
	if kind != "work.intent_revised" || actor != "operator" {
		t.Fatalf("foreign event = %q by %q, want work.intent_revised by operator", kind, actor)
	}
}

// TestFirstWorkEventByOtherActorRefusesEmptyInputs pins the typed refusals
// for the probe's own contract.
func TestFirstWorkEventByOtherActorRefusesEmptyInputs(t *testing.T) {
	s := openTemp(t)
	if _, _, _, err := s.FirstWorkEventByOtherActor(context.Background(), "", "actor"); err == nil {
		t.Fatal("empty work id was accepted")
	} else {
		assertFailureKind(t, err, KindInvalidOperation)
	}
	if _, _, _, err := s.FirstWorkEventByOtherActor(context.Background(), "work", ""); err == nil {
		t.Fatal("empty excluded actor was accepted")
	} else {
		assertFailureKind(t, err, KindInvalidOperation)
	}
}

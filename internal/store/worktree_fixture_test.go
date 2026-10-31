package store

import (
	"context"
	"testing"
	"time"
)

func WorktreeSetID(workID string) string { return "wts:" + workID }

func worktreeFixture(t *testing.T) *Store {
	t.Helper()
	s := openTemp(t)
	ctx := context.Background()
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{locatorProductEvent("product-w"), locatorProjectEvent("project-w"), locatorMembershipEvent("product-w", "project-w")}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProduct, "product-w"): 0, VersionRef(SubjectProject, "project-w"): 0}}); err != nil {
		t.Fatal(err)
	}
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{
		{EventID: "work-w-create", Kind: "work.created", SubjectType: SubjectWorkItem, SubjectID: "work-w", Actor: "operator", OccurredAt: time.Unix(1, 0).UTC(), PayloadVersion: CurrentEventPayloadVersion("work.created"), Payload: jsonRaw(`{"work_kind":"task","title":"Worktree Work","priority":1}`)},
		{EventID: "work-w-membership", Kind: "work.memberships_replaced", SubjectType: SubjectWorkItem, SubjectID: "work-w", Actor: "operator", OccurredAt: time.Unix(2, 0).UTC(), PayloadVersion: 1, Payload: jsonRaw(`{"memberships":[{"project_id":"project-w","role":"primary"}],"expected_version":1,"resulting_version":2}`)},
	}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "work-w"): 0}}); err != nil {
		t.Fatal(err)
	}
	repo := t.TempDir()
	if err := s.AddProjectLocator(ctx, "project-w", ProjectLocator{ID: "path-w", Kind: LocatorCanonicalPath, Value: repo}, 1); err != nil {
		t.Fatal(err)
	}
	return s
}

func jsonRaw(s string) []byte { return []byte(s) }

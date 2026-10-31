package store

import (
	"context"
	"reflect"
	"testing"
)

func TestRecordedReviewRequiresWorkProjectMembership(t *testing.T) {
	for _, tc := range []struct {
		name    string
		project string
		allowed bool
	}{
		{"known unattached", "project:foreign", false},
		{"unknown", "project:unknown", false},
		{"attached", "project", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			j, _ := retainedReviewFixture(t, "review-membership")
			if err := ApplyOperation(context.Background(), j.s, Operation{
				Events: []Event{
					productCreatedEvent("product:foreign", "create-foreign-product"),
					projectCreatedEvent("project:foreign", "create-foreign-project"),
					operationEvent("foreign-product-project", "product_project.added", SubjectProduct, "product:foreign", map[string]any{
						"product_id": "product:foreign", "project_id": "project:foreign", "role": "primary", "reason": "test", "expected_version": 1, "resulting_version": 2,
					}),
				},
				ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProduct, "product:foreign"): 0, VersionRef(SubjectProject, "project:foreign"): 0},
			}); err != nil {
				t.Fatal(err)
			}
			report := currentReviewBlockForTest(t, j.s, j.workID, "continue")
			report.InputCandidate.ProjectID = tc.project
			report.OutputCandidate.ProjectID = tc.project
			before, err := readWorkflowReviewFacts(t.Context(), j.s.db, j.workID)
			if err != nil {
				t.Fatal(err)
			}
			effects := j.effects(t)
			err = recordReviewForTest(t, j.s, j.workID, j.fixture.owner, report)
			if tc.allowed {
				if err != nil {
					t.Fatalf("attached Project refused: %v", err)
				}
				after, err := readWorkflowReviewFacts(t.Context(), j.s.db, j.workID)
				if err != nil || after.Accepted == nil || after.Accepted.ReportDigest != reviewDigest(report) {
					t.Fatalf("attached Review not recorded: %+v, %v", after, err)
				}
				return
			}
			if !hasFailureKind(err, KindUnknownScope) {
				t.Errorf("unattached Project error = %v, want unknown scope", err)
			}
			after, readErr := readWorkflowReviewFacts(t.Context(), j.s.db, j.workID)
			if readErr != nil || !reflect.DeepEqual(before, after) || effects != j.effects(t) {
				t.Errorf("refused Review changed recorded state or effects: %v", readErr)
			}
		})
	}
}

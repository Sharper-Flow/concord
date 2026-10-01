package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

// agentFixtureProductEvents builds the canonical Product, Project, and
// primary-membership creation triple every agent-surface fixture starts
// from. Display names and the membership reason stay per-caller so failure
// output keeps naming the fixture that produced it.
func agentFixtureProductEvents(prefix, productID, projectID, productName, projectName, reason string) []store.Event {
	return []store.Event{
		{EventID: prefix + "-product", Kind: "product.created", SubjectType: store.SubjectProduct, SubjectID: productID, Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"display_name":"` + productName + `","stage_maturity":"prototype","stage_audience_commitment":"operator_only"}`)},
		{EventID: prefix + "-project", Kind: "project.created", SubjectType: store.SubjectProject, SubjectID: projectID, Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"display_name":"` + projectName + `"}`)},
		{EventID: prefix + "-membership", Kind: "product_project.added", SubjectType: store.SubjectProduct, SubjectID: productID, Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"product_id":"` + productID + `","project_id":"` + projectID + `","role":"primary","reason":"` + reason + `","expected_version":1,"resulting_version":2}`)},
	}
}

// applyAgentFixtureProduct applies the fixture triple on its own.
func applyAgentFixtureProduct(ctx context.Context, t *testing.T, s *store.Store, prefix, productID, projectID, productName, projectName, reason string) {
	t.Helper()
	if err := store.ApplyOperation(ctx, s, store.Operation{Events: agentFixtureProductEvents(prefix, productID, projectID, productName, projectName, reason), ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectProduct, productID): 0, store.VersionRef(store.SubjectProject, projectID): 0}}); err != nil {
		t.Fatal(err)
	}
}

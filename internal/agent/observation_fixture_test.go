package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/store"
	"github.com/sharper-flow/concord/internal/store/storetest"
)

func observationFixture(t *testing.T) (*store.Store, *Service, Authority) {
	t.Helper()
	ctx := context.Background()
	s, err := storetest.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	applyAgentFixtureProduct(ctx, t, s, "observations", "product-1", "project-1", "Observations", "Observations Project", "observation fixture")
	for _, w := range []struct{ id, title string }{{"work-holder", "Holder"}, {"work-contender", "Contender"}} {
		if err := store.ApplyOperation(ctx, s, store.Operation{Events: []store.Event{
			{EventID: w.id + "-create", Kind: "work.created", SubjectType: store.SubjectWorkItem, SubjectID: w.id, Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: store.CurrentEventPayloadVersion("work.created"), Payload: json.RawMessage(`{"work_kind":"task","title":"` + w.title + `","priority":1}`)},
			{EventID: w.id + "-membership", Kind: "work.memberships_replaced", SubjectType: store.SubjectWorkItem, SubjectID: w.id, Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"memberships":[{"project_id":"project-1","role":"primary"}],"expected_version":1,"resulting_version":2}`)},
		}, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectWorkItem, w.id): 0}}); err != nil {
			t.Fatal(err)
		}
	}
	service, _, grant := newAuthorizedService(t, s, "client-1", "human-1", []agentcontract.Capability{"product_read", "work_relate", "work_define", "work_transition"}, []string{"product-1"}, []string{"project-1"}, store.ProjectResolution{ProjectID: "project-1"})
	return s, service, grant
}

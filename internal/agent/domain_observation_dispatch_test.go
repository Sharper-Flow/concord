package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/pm1fixture"
	"github.com/sharper-flow/concord/internal/store"
)

// Domain detail reads the bounded open window from existing journal events.
// Dismissed rows remain durable but do not enter the open observation payload.
func TestDomainDetailReadsExistingOpenObservations(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, service, grant, _ := mutationDispatchFixture(t, []agentcontract.Capability{"product_read"})
	if err := pm1fixture.SeedCurrentProductDomain(ctx, s, "product-1", "project-1", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	domain := pm1fixture.FixtureRootDomainID
	for i := 1; i <= 2; i++ {
		payload, err := json.Marshal(map[string]any{
			"observation_id": fmt.Sprintf("dob:%016x", i), "product_id": "product-1", "domain_id": domain,
			"statement": fmt.Sprintf("existing observation %d", i), "refs": []string{}, "tags": []string{},
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := store.ApplyOperation(ctx, s, store.Operation{Events: []store.Event{{
			EventID: fmt.Sprintf("domain-observation-%d", i), Kind: "domain.observation_recorded",
			SubjectType: store.SubjectProduct, SubjectID: "product-1", Actor: "agent", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: payload,
		}}}); err != nil {
			t.Fatal(err)
		}
	}
	dismissal, err := json.Marshal(map[string]any{"observation_id": "dob:0000000000000002", "product_id": "product-1", "domain_id": domain})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ApplyOperation(ctx, s, store.Operation{Events: []store.Event{{
		EventID: "domain-observation-dismissed", Kind: "domain.observation_dismissed",
		SubjectType: store.SubjectProduct, SubjectID: "product-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: dismissal,
	}}}); err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersionForProject(t, s, "project-1"))
	detail := domainRead(t, s, service, env, "detail", fmt.Sprintf(`{"product_id":"product-1","domain_id":%q}`, domain))
	var payload struct {
		Observations []struct {
			ObservationID string `json:"observation_id"`
			Statement     string `json:"statement"`
			State         string `json:"state"`
		} `json:"observations"`
	}
	if err := json.Unmarshal(detail.Result, &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Observations) != 1 || payload.Observations[0].ObservationID != "dob:0000000000000001" || payload.Observations[0].Statement != "existing observation 1" || payload.Observations[0].State != "open" {
		t.Fatalf("detail open observations=%+v", payload.Observations)
	}
	if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM domain_observations`); got != 2 {
		t.Fatalf("detail removed durable observations: rows=%d", got)
	}
}

package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/store"
)

// AJ9 — read the managed-resource inventory (C15 §6, CD-0106 D4).
//
// bindAJ9ProductResources proves the read-only resources operation answers
// the identity half of external-system knowledge: which systems the selected
// Product owns or consumes, the owner, the consumers, and the vendor
// documentation locator. It also proves the two things the operation must
// not do: inline vendor content, and mutate the inventory.
func bindAJ9ProductResources(t *testing.T, sc jobScenario) jobObservation {
	t.Helper()
	s, service, grant, _ := agentJobsPM1Fixture(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	ownerVersion := productVersionForTest(t, s, "prod-alpha")
	if _, err := store.CreateManagedResource(ctx, s, store.ManagedResourceCreateRequest{
		EventID: "aj9-vendor-api-created", ResourceID: "vendor-api", ProductID: "prod-alpha",
		DisplayName: "Vendor API", Class: "saas", Kind: "service", Purpose: "hosted pricing feed",
		StageMaturity: "production", StageAudienceCommitment: "limited", Environments: []string{"production"},
		MetadataSchemaVersion: "1", Metadata: []byte(`{"documentation_locator":"/vendor/api-docs"}`),
		OwnerPurpose: "operates the feed", OwnerEnvironments: []string{"production"},
		ExpectedProductVersion: ownerVersion, Actor: "operator", OccurredAt: now,
	}); err != nil {
		t.Fatalf("seed resource: %v", err)
	}
	if err := store.AddManagedResourceConsumer(ctx, s, store.AddManagedResourceConsumerRequest{
		EventID: "aj9-vendor-api-consumer", ResourceID: "vendor-api", ProductID: "prod-beta",
		Purpose: "reads prices", Environments: []string{"production"}, ExpectedResourceVersion: 1, Actor: "operator", OccurredAt: now.Add(time.Second),
	}); err != nil {
		t.Fatalf("seed consumer: %v", err)
	}
	eventsBefore := domainEventCountForTest(t, s)

	env := agentJobsEnvelope(grant, "proj-web", "prod-alpha")
	resp := dispatchRead(t, s, service, InvokeRequest{
		Tool:      "concord_product_view",
		Operation: "resources",
		Input:     json.RawMessage(`{"page":{"cursor":null,"limit":20}}`),
	}, env)
	if resp.Outcome != OutcomeOK {
		t.Fatalf("resources outcome=%s error=%+v", resp.Outcome, resp.Error)
	}
	var page struct {
		Resources []struct {
			ResourceID   string `json:"resource_id"`
			MetadataJSON string `json:"metadata_json"`
			Owner        struct {
				ProductID string `json:"product_id"`
			} `json:"owner"`
			Consumers []struct {
				ProductID string `json:"product_id"`
			} `json:"consumers"`
		} `json:"resources"`
	}
	if err := json.Unmarshal(resp.Result, &page); err != nil {
		t.Fatalf("unmarshal resource page: %v", err)
	}
	obs := envelopeToObservation(resp)
	ids := []any{}
	consumers := []any{}
	owner, locator := "", ""
	for _, resource := range page.Resources {
		ids = append(ids, resource.ResourceID)
		owner = resource.Owner.ProductID
		for _, consumer := range resource.Consumers {
			consumers = append(consumers, consumer.ProductID)
		}
		var metadata struct {
			DocumentationLocator string `json:"documentation_locator"`
		}
		if err := json.Unmarshal([]byte(resource.MetadataJSON), &metadata); err != nil {
			t.Fatalf("metadata_json is not an object: %v", err)
		}
		locator = metadata.DocumentationLocator
	}
	obs.Result = map[string]any{
		"resource_ids":          ids,
		"owner_product_id":      owner,
		"consumer_product_ids":  consumers,
		"documentation_locator": locator,
	}

	// The answer carries a locator, never a document. The closed schema bounds
	// metadata_json, and the raw bytes hold no fence or schema body.
	obs.Effects = map[string]any{}
	if strings.Contains(string(resp.Result), "```") || strings.Contains(string(resp.Result), `"openapi"`) {
		obs.Effects["vendor_content_inlined"] = "response carries a fenced or schema body"
	} else {
		obs.Effects["vendor_content_inlined"] = probedAbsent{}
	}
	// A read appends nothing to the authoritative log.
	if after := domainEventCountForTest(t, s); after != eventsBefore {
		obs.Effects["resource_mutation"] = after - eventsBefore
	} else {
		obs.Effects["resource_mutation"] = probedAbsent{}
	}
	return obs
}

func productVersionForTest(t *testing.T, s *store.Store, productID string) int64 {
	t.Helper()
	var version int64
	if err := s.DatabaseForTesting().QueryRowContext(context.Background(), `SELECT version FROM products WHERE id = ?`, productID).Scan(&version); err != nil {
		t.Fatalf("read product version: %v", err)
	}
	return version
}

func domainEventCountForTest(t *testing.T, s *store.Store) int64 {
	t.Helper()
	var count int64
	if err := s.DatabaseForTesting().QueryRowContext(context.Background(), `SELECT count(*) FROM domain_events`).Scan(&count); err != nil {
		t.Fatalf("count domain events: %v", err)
	}
	return count
}

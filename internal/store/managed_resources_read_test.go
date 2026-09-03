package store

import (
	"context"
	"testing"
	"time"
)

func seedTwoResources(t *testing.T, s *Store) {
	t.Helper()
	ctx := context.Background()
	setupProductWithProject(t, s, "owner-product", "owner-project")
	setupProductWithProject(t, s, "consumer-product", "consumer-project")
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	if _, err := CreateManagedResource(ctx, s, ManagedResourceCreateRequest{
		EventID: "res-api-created", ResourceID: "vendor-api", ProductID: "owner-product",
		DisplayName: "Vendor API", Class: "saas", Kind: "service", Purpose: "hosted pricing feed",
		StageMaturity: "production", StageAudienceCommitment: "limited", Environments: []string{"production", "staging"},
		MetadataSchemaVersion: "1", Metadata: []byte(`{"documentation_locator":"/vendor/api-docs"}`),
		OwnerPurpose: "operates the feed", OwnerEnvironments: []string{"production"},
		ExpectedProductVersion: 2, Actor: "operator", OccurredAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := AddManagedResourceConsumer(ctx, s, AddManagedResourceConsumerRequest{
		EventID: "res-api-consumer", ResourceID: "vendor-api", ProductID: "consumer-product",
		Purpose: "reads prices", Environments: []string{"staging"}, ExpectedResourceVersion: 1, Actor: "operator", OccurredAt: now.Add(time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := CreateManagedResource(ctx, s, ManagedResourceCreateRequest{
		EventID: "res-db-created", ResourceID: "owner-db", ProductID: "owner-product",
		DisplayName: "Owner DB", Class: "infrastructure", Kind: "database", Purpose: "stores rows",
		StageMaturity: "beta", StageAudienceCommitment: "operator_only", Environments: []string{"production"},
		MetadataSchemaVersion: "1", Metadata: []byte(`{}`),
		ExpectedProductVersion: 2, Actor: "operator", OccurredAt: now.Add(2 * time.Second),
	}); err != nil {
		t.Fatal(err)
	}
}

// C15 §5 direction 1: Product → owned plus consumed resources, filtered.
func TestResourcesForProductReturnsOwnedAndConsumedOnce(t *testing.T) {
	s := openTemp(t)
	seedTwoResources(t, s)
	ctx := context.Background()
	result, err := s.Resources(ctx, ResourcesRequest{ProductID: "owner-product", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Resources) != 2 || result.Resources[0].ResourceID != "owner-db" || result.Resources[1].ResourceID != "vendor-api" {
		t.Fatalf("owner resources = %+v", result.Resources)
	}
	api := result.Resources[1]
	if api.Owner.ProductID != "owner-product" || api.Owner.Role != "owner" || api.Owner.Purpose != "operates the feed" {
		t.Fatalf("owner link = %+v", api.Owner)
	}
	if len(api.Consumers) != 1 || api.Consumers[0].ProductID != "consumer-product" || api.Consumers[0].Environments[0] != "staging" {
		t.Fatalf("consumer links = %+v", api.Consumers)
	}
	if api.MetadataJSON != `{"documentation_locator":"/vendor/api-docs"}` {
		t.Fatalf("metadata = %q", api.MetadataJSON)
	}
	if result.QueryID != "C15.Resources" || result.Authority != "authoritative" {
		t.Fatalf("meta = %+v", result.ResultMeta)
	}
	consumed, err := s.Resources(ctx, ResourcesRequest{ProductID: "consumer-product", Limit: 10})
	if err != nil || len(consumed.Resources) != 1 || consumed.Resources[0].ResourceID != "vendor-api" {
		t.Fatalf("consumer resources = %+v err=%v", consumed.Resources, err)
	}
	filtered, err := s.Resources(ctx, ResourcesRequest{ProductID: "owner-product", Class: "saas", Limit: 10})
	if err != nil || len(filtered.Resources) != 1 || filtered.Resources[0].ResourceID != "vendor-api" {
		t.Fatalf("class filter = %+v err=%v", filtered.Resources, err)
	}
	byEnvironment, err := s.Resources(ctx, ResourcesRequest{ProductID: "owner-product", Environment: "staging", Limit: 10})
	if err != nil || len(byEnvironment.Resources) != 1 || byEnvironment.Resources[0].ResourceID != "vendor-api" {
		t.Fatalf("environment filter = %+v err=%v", byEnvironment.Resources, err)
	}
}

// C15 §5 direction 2: resource → owner plus consumers.
func TestResourcesByIDReturnsOneCanonicalResource(t *testing.T) {
	s := openTemp(t)
	seedTwoResources(t, s)
	ctx := context.Background()
	result, err := s.Resources(ctx, ResourcesRequest{ResourceID: "vendor-api", Limit: 10})
	if err != nil || len(result.Resources) != 1 {
		t.Fatalf("resource by id = %+v err=%v", result.Resources, err)
	}
	if result.Resources[0].Owner.ProductID != "owner-product" || len(result.Resources[0].Consumers) != 1 {
		t.Fatalf("resource links = %+v", result.Resources[0])
	}
	missing, err := s.Resources(ctx, ResourcesRequest{ResourceID: "nope", Limit: 10})
	if err != nil || len(missing.Resources) != 0 {
		t.Fatalf("unknown resource = %+v err=%v", missing.Resources, err)
	}
}

func TestResourcesRefusesAnUnboundedOrUnscopedRead(t *testing.T) {
	s := openTemp(t)
	seedTwoResources(t, s)
	ctx := context.Background()
	if _, err := s.Resources(ctx, ResourcesRequest{Limit: 10}); err == nil {
		t.Fatal("read with neither product nor resource passed")
	}
	if _, err := s.Resources(ctx, ResourcesRequest{ProductID: "owner-product", Class: "cloud", Limit: 10}); err == nil {
		t.Fatal("unknown class filter passed")
	}
	if _, err := s.Resources(ctx, ResourcesRequest{ProductID: "owner-product", Limit: 0}); err == nil {
		t.Fatal("zero limit passed")
	}
}

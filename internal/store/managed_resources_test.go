package store

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestCreateManagedResourceAndAddConsumerAreEventBacked(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	setupProductWithProject(t, s, "resource-product", "resource-project")
	setupProductWithProject(t, s, "consumer-product", "consumer-project")
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	metadata := []byte(`{"kind_detail":"owned test service"}`)
	if _, err := CreateManagedResource(ctx, s, ManagedResourceCreateRequest{
		EventID: "resource-created", ResourceID: "resource-1", ProductID: "resource-product",
		DisplayName: "API", Class: "infrastructure", Kind: "service", Purpose: "serves API traffic",
		StageMaturity: "production", StageAudienceCommitment: "limited", Environments: []string{"production"},
		MetadataSchemaVersion: "1", Metadata: metadata, OwnerPurpose: "operates API", OwnerEnvironments: []string{"production"},
		ExpectedProductVersion: 2, Actor: "operator", OccurredAt: now,
	}); err != nil {
		t.Fatalf("CreateManagedResource() error = %v", err)
	}
	if err := AddManagedResourceConsumer(ctx, s, AddManagedResourceConsumerRequest{
		EventID: "resource-consumer-added", ResourceID: "resource-1", ProductID: "consumer-product",
		Purpose: "calls API", Environments: []string{"production"}, ExpectedResourceVersion: 1, Actor: "operator", OccurredAt: now.Add(time.Second),
	}); err != nil {
		t.Fatalf("AddManagedResourceConsumer() error = %v", err)
	}
	var ownerCount, consumerCount, version, events int
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT count(*) FROM resource_products WHERE resource_id='resource-1' AND role='owner'`).Scan(&ownerCount); err != nil {
		t.Fatal(err)
	}
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT count(*) FROM resource_products WHERE resource_id='resource-1' AND role='consumer'`).Scan(&consumerCount); err != nil {
		t.Fatal(err)
	}
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT version FROM managed_resources WHERE resource_id='resource-1'`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT count(*) FROM domain_events WHERE kind IN ('managed_resource.created','managed_resource.consumer_added')`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if ownerCount != 1 || consumerCount != 1 || version != 2 || events != 2 {
		t.Fatalf("resource projection owner=%d consumer=%d version=%d events=%d", ownerCount, consumerCount, version, events)
	}
	if err := RebuildFromLog(ctx, s); err != nil {
		t.Fatalf("RebuildFromLog() error = %v", err)
	}
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT count(*) FROM resource_products WHERE resource_id='resource-1'`).Scan(&ownerCount); err != nil {
		t.Fatal(err)
	}
	if ownerCount != 2 {
		t.Fatalf("resource links after rebuild=%d, want 2", ownerCount)
	}
}

func TestManagedResourceRejectsDuplicateConsumerAndEnvironmentOutsideResource(t *testing.T) {
	s := openTemp(t)
	setupProductWithProject(t, s, "resource-product-duplicate", "resource-project-duplicate")
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	_, err := CreateManagedResource(context.Background(), s, ManagedResourceCreateRequest{EventID: "resource-created-duplicate", ResourceID: "resource-duplicate", ProductID: "resource-product-duplicate", DisplayName: "DB", Class: "infrastructure", Kind: "database", Purpose: "stores data", StageMaturity: "production", StageAudienceCommitment: "limited", Environments: []string{"production"}, MetadataSchemaVersion: "1", Metadata: []byte(`{}`), ExpectedProductVersion: 2, Actor: "operator", OccurredAt: now})
	if err != nil {
		t.Fatal(err)
	}
	if err := AddManagedResourceConsumer(context.Background(), s, AddManagedResourceConsumerRequest{EventID: "resource-consumer-bad-env", ResourceID: "resource-duplicate", ProductID: "resource-product-duplicate", Purpose: "bad", Environments: []string{"development"}, ExpectedResourceVersion: 1, Actor: "operator", OccurredAt: now.Add(time.Second)}); err == nil || !strings.Contains(err.Error(), "declared") {
		t.Fatalf("bad environment error=%v, want subset refusal", err)
	}
}

func TestManagedResourceMetadataBoundsAndOtherKindDetail(t *testing.T) {
	base := ManagedResource{ResourceID: "resource-metadata", DisplayName: "Metadata", Class: "infrastructure", Kind: "service", Purpose: "tests metadata", StageMaturity: "production", StageAudienceCommitment: "limited", Environments: []string{"production"}, MetadataSchemaVersion: "1", Metadata: []byte(`{}`)}
	if err := validateManagedResourceInput(base); err != nil {
		t.Fatalf("valid metadata rejected: %v", err)
	}
	base.Metadata = []byte(`{"value":"` + strings.Repeat("x", maxManagedResourceMetadataBytes) + `"}`)
	if err := validateManagedResourceInput(base); err == nil {
		t.Fatal("metadata larger than 16 KiB was accepted")
	}
	for name, metadata := range map[string]string{
		"non-string": `{"kind_detail":1}`,
		"blank":      `{"kind_detail":" "}`,
		"padded":     `{"kind_detail":" queue "}`,
		"oversized":  `{"kind_detail":"` + strings.Repeat("x", maxManagedResourceKindDetail+1) + `"}`,
	} {
		t.Run(name, func(t *testing.T) {
			resource := base
			resource.Kind, resource.Metadata = "other", []byte(metadata)
			if err := validateManagedResourceInput(resource); err == nil {
				t.Fatal("invalid other kind_detail was accepted")
			}
		})
	}
	base.Kind, base.Metadata = "other", []byte(`{"kind_detail":"queue"}`)
	if err := validateManagedResourceInput(base); err != nil {
		t.Fatalf("clean other kind_detail rejected: %v", err)
	}
}

func setupProductWithProject(t *testing.T, s *Store, productID, projectID string) {
	t.Helper()
	ctx := context.Background()
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{productCreatedEvent(productID, productID+"-created"), projectCreatedEvent(projectID, projectID+"-created"), membershipEvent(productID+"-membership", "product_project.added", SubjectProduct, productID, map[string]any{"product_id": productID, "project_id": projectID, "role": "primary", "reason": "test", "expected_version": 1, "resulting_version": 2})}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProduct, productID): 0, VersionRef(SubjectProject, projectID): 0}}); err != nil {
		t.Fatalf("setup Product %s: %v", productID, err)
	}
}

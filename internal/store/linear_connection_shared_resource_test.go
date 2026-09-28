package store

import (
	"context"
	"testing"
	"time"
)

// Two Products on one installation share one Linear workspace through a
// resource share: the consumer-linked Product resolves the same declared
// connection an owner link resolves, so no Product needs a duplicate
// saas_account carrying identical metadata.linear. More than one candidate
// resource for one Product refuses with ambiguous_scope naming every
// candidate resource id; the read never prefers one by id order.

func setupSharedLinearConnection(t *testing.T, s *Store, ownerProductID string) string {
	t.Helper()
	setupLinearProduct(t, s, ownerProductID)
	setupLinearConnectionResource(t, s, ownerProductID, map[string]any{
		"linear": map[string]any{
			"workspace_url": "https://linear.app/example",
			"team_id":       "team-uuid-shared",
			"auth_mode":     "personal_api_key",
		},
	})
	return "linear-conn-" + ownerProductID
}

func linkSharedLinearConsumer(t *testing.T, s *Store, resourceID, consumerProductID, eventID string, at time.Time) {
	t.Helper()
	if err := AddManagedResourceConsumer(context.Background(), s, AddManagedResourceConsumerRequest{
		EventID: eventID, ResourceID: resourceID, ProductID: consumerProductID,
		Purpose: "shares the planning workspace", Environments: []string{"production"},
		ExpectedResourceVersion: 1, Actor: "operator", OccurredAt: at,
	}); err != nil {
		t.Fatalf("AddManagedResourceConsumer() error = %v", err)
	}
}

func TestLinearConnectionResolvesThroughConsumerLink(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	ctx := context.Background()
	resourceID := setupSharedLinearConnection(t, s, "owner-product")

	setupLinearProduct(t, s, "consumer-product")
	linkSharedLinearConsumer(t, s, resourceID, "consumer-product", "shared-linear-consumer", time.Date(2026, 9, 9, 1, 0, 0, 0, time.UTC))
	if _, err := s.SetProductPlanningMode(ctx, "consumer-product", PlanningModeLinear, "shared workspace", "operator", 2); err != nil {
		t.Fatal(err)
	}

	connection, err := s.ReadLinearConnection(ctx, "consumer-product")
	if err != nil {
		t.Fatalf("ReadLinearConnection() error = %v", err)
	}
	if connection.State != LinearConnectionDeclared {
		t.Fatalf("consumer connection state = %s, want declared", connection.State)
	}
	if connection.ResourceID != resourceID || connection.WorkspaceURL != "https://linear.app/example" || connection.TeamID != "team-uuid-shared" || connection.AuthMode != "personal_api_key" {
		t.Fatalf("consumer connection = %+v", connection)
	}

	mode, err := s.ResolveLinearPlanningTarget(ctx, "consumer-product")
	if err != nil {
		t.Fatalf("ResolveLinearPlanningTarget() error = %v", err)
	}
	if mode.PlanningMode != PlanningModeLinear {
		t.Fatalf("resolved mode = %s, want linear_enabled", mode.PlanningMode)
	}
}

func TestLinearConnectionRefusesAmbiguityWhenTwoResourcesMatch(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	ctx := context.Background()
	firstID := setupSharedLinearConnection(t, s, "first-product")
	secondID := setupSharedLinearConnection(t, s, "second-product")

	linkSharedLinearConsumer(t, s, secondID, "first-product", "shared-linear-second-consumer", time.Date(2026, 9, 9, 2, 0, 0, 0, time.UTC))

	_, err := s.ReadLinearConnection(ctx, "first-product")
	if err == nil || !failureKindIs(err, KindAmbiguousScope) {
		t.Fatalf("two-candidate read error = %v, want ambiguous_scope", err)
	}
	var failure *Failure
	if !failureAs(err, &failure) || len(failure.CandidateIDs) != 2 || failure.CandidateIDs[0] != firstID || failure.CandidateIDs[1] != secondID {
		t.Fatalf("candidate ids = %v, want [%s %s]", failure.CandidateIDs, firstID, secondID)
	}

	if _, err := s.SetProductPlanningMode(ctx, "first-product", PlanningModeLinear, "shared workspace", "operator", 2); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ResolveLinearPlanningTarget(ctx, "first-product"); err == nil || !failureKindIs(err, KindAmbiguousScope) {
		t.Fatalf("two-candidate resolve error = %v, want ambiguous_scope", err)
	}
}

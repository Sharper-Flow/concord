package store

import (
	"context"
	"strings"
	"testing"
)

// The per-Product Linear reads scope by ownership, not by any-membership:
// the owner rule is resolveLinearProductCore's, so a shared work item is read
// only under the Product that owns its Linear identity.
func TestLinearOwnerScopeCrossProduct(t *testing.T) {
	s := openTemp(t)
	defer s.Close()
	ctx := context.Background()
	setupLinearProduct(t, s, "scope-owner-product")
	setupLinearProduct(t, s, "scope-secondary-product")
	setupLinearConnectionResource(t, s, "scope-owner-product", map[string]any{"linear": map[string]any{
		"workspace_url": "https://linear.app/example", "team_id": "team-uuid-1", "auth_mode": "personal_api_key",
	}})
	if _, err := s.SetProductPlanningMode(ctx, "scope-owner-product", PlanningModeLinear, "pilot", "operator", 2); err != nil {
		t.Fatal(err)
	}
	seedLinearWorkItem(t, s, "scope-cross-work", "scope-owner-product-project", "Cross title", "Cross value")
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := enterFold(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO work_projects(work_id,project_id,role) VALUES(?,?,'secondary')`, "scope-cross-work", "scope-secondary-product-project"); err != nil {
		t.Fatal(err)
	}
	if err := leaveFold(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	op, err := s.EnqueueLinearIssueForWork(ctx, "scope-cross-work", LinearOpIssueCreate)
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range []struct {
		state string
		hash  string
	}{{LinearLinkPending, ""}, {LinearLinkConfirmed, "sha256:" + strings.Repeat("cd", 32)}} {
		if err := s.RecordLinearLink(ctx, "scope-cross-work", "remote-scope-cross", "SCOPE-1", "https://linear.app/example/issue/SCOPE-1", "", step.hash, step.state); err != nil {
			t.Fatal(err)
		}
	}

	// Health counts the item's queued outbox row and confirmed link under
	// the owner only.
	health, err := s.ReadLinearIntegrationHealth(ctx, "scope-owner-product")
	if err != nil {
		t.Fatal(err)
	}
	if health.OutboxDepth != 1 || health.LinkCounts[LinearLinkConfirmed] != 1 {
		t.Fatalf("owner health = depth %d confirmed %d, want 1/1", health.OutboxDepth, health.LinkCounts[LinearLinkConfirmed])
	}
	health, err = s.ReadLinearIntegrationHealth(ctx, "scope-secondary-product")
	if err != nil {
		t.Fatal(err)
	}
	if health.OutboxDepth != 0 || health.LinkCounts[LinearLinkConfirmed] != 0 {
		t.Fatalf("secondary health = depth %d confirmed %d, want 0/0", health.OutboxDepth, health.LinkCounts[LinearLinkConfirmed])
	}

	// Divergence checks the item under its owner Product only.
	linked, err := s.ReadConfirmedLinearLinkedWorkForProduct(ctx, "scope-owner-product")
	if err != nil {
		t.Fatal(err)
	}
	if len(linked) != 1 || linked[0].WorkID != "scope-cross-work" {
		t.Fatalf("owner confirmed linked work = %+v, want scope-cross-work", linked)
	}
	linked, err = s.ReadConfirmedLinearLinkedWorkForProduct(ctx, "scope-secondary-product")
	if err != nil {
		t.Fatal(err)
	}
	if len(linked) != 0 {
		t.Fatalf("secondary confirmed linked work = %+v, want empty", linked)
	}

	// The outbox-drain identity sweep obeys the same owner scope.
	const staleBefore = "9999-01-01T00:00:00Z"
	links, err := s.ReadConfirmedLinearLinksForProduct(ctx, "scope-owner-product", staleBefore)
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 1 || links[0].WorkID != "scope-cross-work" {
		t.Fatalf("owner confirmed links = %+v, want scope-cross-work", links)
	}
	links, err = s.ReadConfirmedLinearLinksForProduct(ctx, "scope-secondary-product", staleBefore)
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 0 {
		t.Fatalf("secondary confirmed links = %+v, want empty", links)
	}

	// A disposition request from the secondary Product refuses, and its
	// failed listing stays empty; the owner still sees the failed row.
	if _, err := s.ClaimLinearOperations(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if err := s.FailLinearOperation(ctx, op.OperationID, "permanent", "credential rejected"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AcknowledgeFailedLinearOperations(ctx, "scope-secondary-product", []string{op.OperationID}, "secondary names the owner's operation"); err == nil || !failureKindIs(err, KindUnknownScope) {
		t.Fatalf("secondary disposition error = %v, want unknown_scope", err)
	}
	dispositions, err := s.AcknowledgeFailedLinearOperations(ctx, "scope-secondary-product", nil, "secondary sweep")
	if err != nil {
		t.Fatal(err)
	}
	if len(dispositions) != 0 {
		t.Fatalf("secondary failed listing = %+v, want empty", dispositions)
	}
	dispositions, err = s.AcknowledgeFailedLinearOperations(ctx, "scope-owner-product", nil, "owner sweep")
	if err != nil {
		t.Fatal(err)
	}
	if len(dispositions) != 1 || dispositions[0].OperationID != op.OperationID {
		t.Fatalf("owner failed listing = %+v, want %s", dispositions, op.OperationID)
	}
}

// A work item whose memberships reach exactly one Product still appears in
// every per-Product Linear read.
func TestLinearOwnerScopeSingleProduct(t *testing.T) {
	s := openTemp(t)
	defer s.Close()
	ctx := context.Background()
	setupLinearProduct(t, s, "scope-single-product")
	setupLinearConnectionResource(t, s, "scope-single-product", map[string]any{"linear": map[string]any{
		"workspace_url": "https://linear.app/example", "team_id": "team-uuid-1", "auth_mode": "personal_api_key",
	}})
	if _, err := s.SetProductPlanningMode(ctx, "scope-single-product", PlanningModeLinear, "pilot", "operator", 2); err != nil {
		t.Fatal(err)
	}
	seedLinearWorkItem(t, s, "scope-single-work", "scope-single-product-project", "Single title", "Single value")

	op, err := s.EnqueueLinearIssueForWork(ctx, "scope-single-work", LinearOpIssueCreate)
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range []struct {
		state string
		hash  string
	}{{LinearLinkPending, ""}, {LinearLinkConfirmed, "sha256:" + strings.Repeat("ef", 32)}} {
		if err := s.RecordLinearLink(ctx, "scope-single-work", "remote-scope-single", "SCOPE-2", "https://linear.app/example/issue/SCOPE-2", "", step.hash, step.state); err != nil {
			t.Fatal(err)
		}
	}
	// Health counts the queued outbox row and the confirmed link.
	health, err := s.ReadLinearIntegrationHealth(ctx, "scope-single-product")
	if err != nil {
		t.Fatal(err)
	}
	if health.OutboxDepth != 1 || health.LinkCounts[LinearLinkConfirmed] != 1 {
		t.Fatalf("health = depth %d confirmed %d, want 1/1", health.OutboxDepth, health.LinkCounts[LinearLinkConfirmed])
	}
	linked, err := s.ReadConfirmedLinearLinkedWorkForProduct(ctx, "scope-single-product")
	if err != nil {
		t.Fatal(err)
	}
	if len(linked) != 1 || linked[0].WorkID != "scope-single-work" {
		t.Fatalf("confirmed linked work = %+v, want scope-single-work", linked)
	}
	links, err := s.ReadConfirmedLinearLinksForProduct(ctx, "scope-single-product", "9999-01-01T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 1 || links[0].WorkID != "scope-single-work" {
		t.Fatalf("confirmed links = %+v, want scope-single-work", links)
	}

	// The failed operation stays visible to the failed listing and to the
	// disposition scope check.
	if _, err := s.ClaimLinearOperations(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if err := s.FailLinearOperation(ctx, op.OperationID, "permanent", "credential rejected"); err != nil {
		t.Fatal(err)
	}
	dispositions, err := s.AcknowledgeFailedLinearOperations(ctx, "scope-single-product", []string{op.OperationID}, "operator accepted the historical failure")
	if err != nil {
		t.Fatal(err)
	}
	if len(dispositions) != 1 || dispositions[0].WorkID != "scope-single-work" {
		t.Fatalf("dispositions = %+v, want scope-single-work", dispositions)
	}
}

// The rendered owner predicate binds the Product id exactly twice; a drift in
// the placeholder count silently misbinds every call site.
func TestLinearOwnerPredicateBindsProductTwice(t *testing.T) {
	if got := strings.Count(linearOwnedWorkIDs, "?"); got != 2 {
		t.Fatalf("owner predicate placeholder count = %d, want 2", got)
	}
}

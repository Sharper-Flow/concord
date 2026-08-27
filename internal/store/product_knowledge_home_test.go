package store

import (
	"context"
	"testing"
)

func designateFixture(t *testing.T, s *Store) (string, string) {
	t.Helper()
	if _, err := s.CreateProductWithProject(context.Background(), ProductCreation{
		ProductID: "home-product", DisplayName: "Home Product", StageMaturity: "prototype", StageAudienceCommitment: "operator_only",
		ProjectID: "home-project", ProjectDisplayName: "Home Project", Role: "primary",
	}); err != nil {
		t.Fatal(err)
	}
	homePath := t.TempDir()
	if err := s.AddProjectLocator(context.Background(), "home-project", ProjectLocator{
		ID: "home-locator", ProjectID: "home-project", Kind: LocatorCanonicalPath, Value: homePath, NormalizedValue: homePath,
	}, 1); err != nil {
		t.Fatal(err)
	}
	secondPath := t.TempDir()
	return homePath, secondPath
}

func countKnowledgeHomes(t *testing.T, s *Store) int {
	t.Helper()
	var count int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM product_knowledge_homes`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

// The designation is event-sourced operator configuration (PM6 §2/§3), so a
// log rebuild reconstructs it by replay, not by snapshot. This is the
// production-path proof floor readiness cites for fc2-domain-authority.
func TestProductKnowledgeHomeDesignationIsEventSourcedAndRebuildStable(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	designateFixture(t, s)

	if _, err := s.DesignateProductKnowledgeHome(ctx, ProductKnowledgeHomeDesignation{
		ProductID: "home-product", ProjectID: "home-project", LocatorID: "home-locator",
		Reason: "durable law home", ExpectedVersion: 2,
	}); err != nil {
		t.Fatal(err)
	}
	if countKnowledgeHomes(t, s) != 1 {
		t.Fatalf("knowledge homes = %d after designation, want 1", countKnowledgeHomes(t, s))
	}

	if err := RebuildFromLog(ctx, s); err != nil {
		t.Fatal(err)
	}
	if countKnowledgeHomes(t, s) != 1 {
		t.Fatalf("knowledge homes = %d after log rebuild, want 1 (replay must reconstruct the designation)", countKnowledgeHomes(t, s))
	}
	var project, locator string
	if err := s.DatabaseForTesting().QueryRow(`SELECT project_id, locator_id FROM product_knowledge_homes WHERE product_id='home-product'`).Scan(&project, &locator); err != nil {
		t.Fatal(err)
	}
	if project != "home-project" || locator != "home-locator" {
		t.Fatalf("rebuilt home = %s/%s, want home-project/home-locator", project, locator)
	}
}

func TestProductKnowledgeHomeDesignationReplacesThePriorRow(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	_, secondPath := designateFixture(t, s)
	if err := s.AddProjectLocator(ctx, "home-project", ProjectLocator{
		ID: "second-locator", ProjectID: "home-project", Kind: LocatorCanonicalPath, Value: secondPath, NormalizedValue: secondPath,
	}, 2); err != nil {
		t.Fatal(err)
	}

	if _, err := s.DesignateProductKnowledgeHome(ctx, ProductKnowledgeHomeDesignation{
		ProductID: "home-product", ProjectID: "home-project", LocatorID: "home-locator", ExpectedVersion: 2,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DesignateProductKnowledgeHome(ctx, ProductKnowledgeHomeDesignation{
		ProductID: "home-product", ProjectID: "home-project", LocatorID: "second-locator", ExpectedVersion: 3,
	}); err != nil {
		t.Fatal(err)
	}
	if countKnowledgeHomes(t, s) != 1 {
		t.Fatalf("knowledge homes = %d after re-designation, want 1", countKnowledgeHomes(t, s))
	}
	var locator string
	if err := s.DatabaseForTesting().QueryRow(`SELECT locator_id FROM product_knowledge_homes WHERE product_id='home-product'`).Scan(&locator); err != nil {
		t.Fatal(err)
	}
	if locator != "second-locator" {
		t.Fatalf("designation = %s after replacement, want second-locator", locator)
	}
}

func TestProductKnowledgeHomeDesignationEnforcesEligibility(t *testing.T) {
	ctx := context.Background()

	t.Run("non-member Project", func(t *testing.T) {
		s := openTemp(t)
		_, siblingPath := designateFixture(t, s)
		// A sibling Project with its own locator: the locator matches its
		// Project, so the refusal must come from the missing membership.
		if _, err := s.CreateProductWithProject(ctx, ProductCreation{
			ProductID: "sibling-owner", DisplayName: "Sibling Owner", StageMaturity: "prototype", StageAudienceCommitment: "operator_only",
			ProjectID: "sibling-project", ProjectDisplayName: "Sibling Project", Role: "primary",
		}); err != nil {
			t.Fatal(err)
		}
		if err := s.AddProjectLocator(ctx, "sibling-project", ProjectLocator{
			ID: "sibling-locator", ProjectID: "sibling-project", Kind: LocatorCanonicalPath, Value: siblingPath, NormalizedValue: siblingPath,
		}, 1); err != nil {
			t.Fatal(err)
		}
		_, err := s.DesignateProductKnowledgeHome(ctx, ProductKnowledgeHomeDesignation{
			ProductID: "home-product", ProjectID: "sibling-project", LocatorID: "sibling-locator", ExpectedVersion: 2,
		})
		assertFailureKind(t, err, KindMembershipConflict)
	})

	t.Run("locator of another Project", func(t *testing.T) {
		s := openTemp(t)
		designateFixture(t, s)
		if _, err := s.CreateProjectForProduct(ctx, ProjectCreation{
			ProjectID: "sibling-project", DisplayName: "Sibling", ProductID: "home-product", Role: "secondary", ExpectedProductVersion: 2,
		}); err != nil {
			t.Fatal(err)
		}
		_, err := s.DesignateProductKnowledgeHome(ctx, ProductKnowledgeHomeDesignation{
			ProductID: "home-product", ProjectID: "sibling-project", LocatorID: "home-locator", ExpectedVersion: 3,
		})
		assertFailureKind(t, err, KindProjectionConflict)
	})

	t.Run("non-canonical locator kind", func(t *testing.T) {
		s := openTemp(t)
		designateFixture(t, s)
		if err := s.AddProjectLocator(ctx, "home-project", ProjectLocator{
			ID: "remote-locator", ProjectID: "home-project", Kind: LocatorGitRemote, Value: "git@example.com:home.git", NormalizedValue: "git@example.com:home.git",
		}, 2); err != nil {
			t.Fatal(err)
		}
		_, err := s.DesignateProductKnowledgeHome(ctx, ProductKnowledgeHomeDesignation{
			ProductID: "home-product", ProjectID: "home-project", LocatorID: "remote-locator", ExpectedVersion: 2,
		})
		assertFailureKind(t, err, KindInvalidPayload)
	})

	t.Run("locator already another Product's home", func(t *testing.T) {
		s := openTemp(t)
		designateFixture(t, s)
		if _, err := s.CreateProjectForProduct(ctx, ProjectCreation{
			ProjectID: "second-project", DisplayName: "Second", ProductID: "home-product", Role: "secondary", ExpectedProductVersion: 2,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.CreateProductWithProject(ctx, ProductCreation{
			ProductID: "other-product", DisplayName: "Other", StageMaturity: "prototype", StageAudienceCommitment: "operator_only",
			ProjectID: "other-project", ProjectDisplayName: "Other Project", Role: "primary",
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.DesignateProductKnowledgeHome(ctx, ProductKnowledgeHomeDesignation{
			ProductID: "home-product", ProjectID: "home-project", LocatorID: "home-locator", ExpectedVersion: 3,
		}); err != nil {
			t.Fatal(err)
		}
		_, err := s.DesignateProductKnowledgeHome(ctx, ProductKnowledgeHomeDesignation{
			ProductID: "other-product", ProjectID: "other-project", LocatorID: "home-locator", ExpectedVersion: 2,
		})
		assertFailureKind(t, err, KindProjectionConflict)
	})

	t.Run("stale Product version", func(t *testing.T) {
		s := openTemp(t)
		designateFixture(t, s)
		_, err := s.DesignateProductKnowledgeHome(ctx, ProductKnowledgeHomeDesignation{
			ProductID: "home-product", ProjectID: "home-project", LocatorID: "home-locator", ExpectedVersion: 1,
		})
		assertFailureKind(t, err, KindVersionConflict)
	})
}

func TestProductKnowledgeHomeClearRemovesTheDesignation(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	designateFixture(t, s)
	if _, err := s.DesignateProductKnowledgeHome(ctx, ProductKnowledgeHomeDesignation{
		ProductID: "home-product", ProjectID: "home-project", LocatorID: "home-locator", ExpectedVersion: 2,
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := s.ClearProductKnowledgeHome(ctx, ProductKnowledgeHomeDesignation{
		ProductID: "home-product", Reason: "moving the law home", ExpectedVersion: 3,
	}); err != nil {
		t.Fatal(err)
	}
	if countKnowledgeHomes(t, s) != 0 {
		t.Fatalf("knowledge homes = %d after clear, want 0", countKnowledgeHomes(t, s))
	}

	_, err := s.ClearProductKnowledgeHome(ctx, ProductKnowledgeHomeDesignation{ProductID: "home-product", ExpectedVersion: 4})
	assertFailureKind(t, err, KindProjectionNotFound)
}

func TestRemovingADesignatedLocatorIsRefusedWithATypedFailure(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	designateFixture(t, s)
	if _, err := s.DesignateProductKnowledgeHome(ctx, ProductKnowledgeHomeDesignation{
		ProductID: "home-product", ProjectID: "home-project", LocatorID: "home-locator", ExpectedVersion: 2,
	}); err != nil {
		t.Fatal(err)
	}

	// The locator removal competes for the Project version, which the
	// designation does not touch: after create (1) and locator-add (2) the
	// Project sits at version 2.
	err := s.RemoveProjectLocator(ctx, "home-project", "home-locator", 2)
	assertFailureKind(t, err, KindProjectionConflict)

	// Clearing the designation releases the locator.
	if _, err := s.ClearProductKnowledgeHome(ctx, ProductKnowledgeHomeDesignation{ProductID: "home-product", ExpectedVersion: 3}); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveProjectLocator(ctx, "home-project", "home-locator", 2); err != nil {
		t.Fatalf("locator removal after clear failed: %v", err)
	}
}

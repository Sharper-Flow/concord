package pm1fixture

import (
	"context"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

func TestDomainEvidenceSeedsCurrentEventSchemas(t *testing.T) {
	ctx := context.Background()
	t.Run("Product and Project", func(t *testing.T) {
		s, err := OpenTemp(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close() })
		if err := SeedProductAndProject(ctx, s, "fixture-product", "fixture-project"); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("work item", func(t *testing.T) {
		s, err := OpenTemp(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close() })
		if _, err := s.CreateProductWithProject(ctx, store.ProductCreation{
			ProductID: "fixture-product", DisplayName: "Product",
			StageMaturity: "prototype", StageAudienceCommitment: "operator_only",
			ProjectID: "fixture-project", ProjectDisplayName: "Project", Role: "primary",
		}); err != nil {
			t.Fatal(err)
		}
		if err := SeedWorkItem(ctx, s, "fixture-project", "fixture-work", "Fixture work", 3); err != nil {
			t.Fatal(err)
		}
		var title string
		var version int
		if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT title,version FROM work_items WHERE id=?`, "fixture-work").Scan(&title, &version); err != nil {
			t.Fatal(err)
		}
		if title != "Fixture work" || version != 2 {
			t.Fatalf("seeded work = %q/v%d, want Fixture work/v2", title, version)
		}
	})
}

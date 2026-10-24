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
		var priority, version int
		if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT title,priority,version FROM work_items WHERE id=?`, "fixture-work").Scan(&title, &priority, &version); err != nil {
			t.Fatal(err)
		}
		if title != "Fixture work" || priority != 3 || version != 2 {
			t.Fatalf("seeded work = %q/%d/v%d, want Fixture work/3/v2", title, priority, version)
		}
	})
}

package store

import (
	"context"
	"fmt"
	"reflect"
	"testing"
)

func TestProductsForWorkIDsReturnsDistinctIdentities(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		otherProd bool
		want      []string
	}{
		{"same_product", false, []string{"prod"}},
		{"distinct_products", true, []string{"prod", "prod-other"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			s := seedQueryFixture(t)
			if err := ApplyOperation(ctx, s, Operation{Events: []Event{
				operationEvent("scope-project-sibling", "project.created", SubjectProject, "proj-2", map[string]any{"display_name": "Sibling"}),
				operationEvent("scope-membership-sibling", "product_project.added", SubjectProduct, "prod", map[string]any{
					"product_id": "prod", "project_id": "proj-2", "role": "secondary", "reason": "scope test", "expected_version": 2, "resulting_version": 3,
				}),
			}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProject, "proj-2"): 0, VersionRef(SubjectProduct, "prod"): 2}}); err != nil {
				t.Fatal(err)
			}
			if tc.otherProd {
				if err := ApplyOperation(ctx, s, Operation{Events: []Event{
					operationEvent("scope-product-other", "product.created", SubjectProduct, "prod-other", map[string]any{"display_name": "Other", "stage_maturity": "prototype", "stage_audience_commitment": "operator_only"}),
					operationEvent("scope-membership-other", "product_project.added", SubjectProduct, "prod-other", map[string]any{"product_id": "prod-other", "project_id": "proj-2", "role": "primary", "reason": "scope test", "expected_version": 1, "resulting_version": 2}),
				}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProduct, "prod-other"): 0}}); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := s.DatabaseForTesting().ExecContext(ctx, `INSERT INTO write_guard(active) VALUES(1); INSERT INTO work_projects(work_id,project_id,role) VALUES('blocked','proj-2','secondary'); DELETE FROM write_guard`); err != nil {
				t.Fatal(err)
			}
			ids := []string{"blocked", "missing"}
			surfaces := map[string]func() (map[string][]string, error){
				"database": func() (map[string][]string, error) { return s.ProductsForWorkIDs(ctx, ids) },
				"exported_transaction": func() (map[string][]string, error) {
					var products map[string][]string
					if err := s.Transact(ctx, func(tr *Transaction) error {
						byWork, err := ProductsForWorkIDsTx(ctx, tr, ids)
						if err != nil {
							return err
						}
						empty, err := ProductsForWorkIDsTx(ctx, tr, nil)
						if err != nil {
							return err
						}
						if len(empty) != 0 {
							return fmt.Errorf("empty work IDs returned %d identities, want 0", len(empty))
						}
						products = byWork
						return nil
					}); err != nil {
						return nil, err
					}
					return products, nil
				},
				"transaction_core": func() (map[string][]string, error) {
					tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
					if err != nil {
						return nil, err
					}
					defer tx.Rollback()
					return productsForWorkIDs(ctx, tx, ids)
				},
			}
			for _, surface := range []string{"database", "exported_transaction", "transaction_core"} {
				products, err := surfaces[surface]()
				if err != nil || len(products) != 1 || !reflect.DeepEqual(products["blocked"], tc.want) {
					t.Errorf("%s Product identities=%v err=%v, want blocked=%v", surface, products, err, tc.want)
				}
			}
			if empty, err := s.ProductsForWorkIDs(ctx, nil); err != nil || len(empty) != 0 {
				t.Errorf("empty work IDs returned %v err=%v, want no identities", empty, err)
			}
		})
	}
}

func TestScopeVersionMovesWhenProductMembershipChanges(t *testing.T) {
	s := seedQueryFixture(t)
	ctx := context.Background()
	before, products, err := s.ScopeVersion(ctx, "proj")
	if err != nil {
		t.Fatal(err)
	}
	if len(products) != 1 || products[0] != "prod" {
		t.Fatalf("initial products = %v, want [prod]", products)
	}
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{
		operationEvent("q-project-sibling", "project.created", SubjectProject, "proj-2", map[string]any{"display_name": "Sibling"}),
		operationEvent("q-membership-sibling", "product_project.added", SubjectProduct, "prod", map[string]any{
			"product_id": "prod", "project_id": "proj-2", "role": "secondary", "reason": "scope test", "expected_version": 2, "resulting_version": 3,
		}),
	}, ExpectedVersions: map[SubjectRef]int64{
		VersionRef(SubjectProject, "proj-2"): 0,
		VersionRef(SubjectProduct, "prod"):   2,
	}}); err != nil {
		t.Fatal(err)
	}
	after, products, err := s.ScopeVersion(ctx, "proj")
	if err != nil {
		t.Fatal(err)
	}
	if before == after {
		t.Fatal("adding a Product sibling did not change the scope version")
	}
	if len(products) != 1 || products[0] != "prod" {
		t.Fatalf("products after sibling add = %v, want [prod]", products)
	}
}

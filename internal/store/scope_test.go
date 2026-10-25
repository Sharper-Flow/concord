package store

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
)

func TestResolveCompactionHomeUsesProductThenPrimaryMembership(t *testing.T) {
	t.Parallel()
	s := seedQueryFixture(t)
	repo := initKnowledgeRepo(t)
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1); INSERT INTO project_locators(locator_id,project_id,kind,locator_value,normalized_value,created_at,updated_at) VALUES('locator-1','proj','canonical_path',?,?,'now','now'); DELETE FROM fold_guard`, repo, repo); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1); INSERT INTO product_knowledge_homes(product_id,project_id,locator_id) VALUES('prod','proj','locator-1'); DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	home, err := s.ResolveCompactionHome(context.Background(), "blocked")
	if err != nil || home.HomeProjectID != "proj" || home.HomeLocatorID != "locator-1" || home.RepoPath != repo {
		t.Fatalf("Product home=%#v err=%v", home, err)
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1); DELETE FROM product_knowledge_homes WHERE product_id='prod'; DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	fallback, err := s.ResolveCompactionHome(context.Background(), "blocked")
	if err != nil || fallback.HomeProjectID != "proj" || fallback.HomeLocatorID != "locator-1" {
		t.Fatalf("primary fallback=%#v err=%v", fallback, err)
	}
}

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
			if _, err := s.DatabaseForTesting().ExecContext(ctx, `INSERT INTO fold_guard(active) VALUES(1); INSERT INTO work_projects(work_id,project_id,role) VALUES('blocked','proj-2','secondary'); DELETE FROM fold_guard`); err != nil {
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

func researchScopeRevisionInput() ResearchRevisionInput {
	return ResearchRevisionInput{Question: "owner scope", ScopeIn: json.RawMessage(`{}`), ScopeOut: json.RawMessage(`{}`), DoneWhen: json.RawMessage(`{}`), Method: "docs"}
}

// The resolver returns one owner identity per distinct pack ID in pack-ID
// order. Input order never changes the output, duplicate pack IDs collapse,
// and two packs with one owner stay two identities: owners are not deduped.
func TestResearchPackOwnerWorkIDsResolvesDeterministicOwnerIdentities(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := seedQueryFixture(t)
	for _, pack := range []struct{ id, owner string }{
		{id: "pack-a", owner: "blocker"},
		{id: "pack-b", owner: "blocked"},
		{id: "pack-c", owner: "blocker"},
	} {
		if _, err := createResearchPack(ctx, s, CreateResearchPackRequest{
			PackID:      pack.id,
			OwnerWorkID: pack.owner,
			Revision:    researchScopeRevisionInput(),
		}); err != nil {
			t.Fatal(err)
		}
	}
	forward, err := s.ResearchPackOwnerWorkIDs(ctx, []string{"pack-a", "pack-b"})
	if err != nil || !reflect.DeepEqual(forward, []string{"blocker", "blocked"}) {
		t.Fatalf("ordered input=%v err=%v, want owners in pack-ID order", forward, err)
	}
	reversed, err := s.ResearchPackOwnerWorkIDs(ctx, []string{"pack-b", "pack-a"})
	if err != nil || !reflect.DeepEqual(reversed, forward) {
		t.Fatalf("reversed input=%v err=%v, want the same pack-ID order", reversed, err)
	}
	duplicated, err := s.ResearchPackOwnerWorkIDs(ctx, []string{"pack-a", "pack-a", "pack-b"})
	if err != nil || !reflect.DeepEqual(duplicated, forward) {
		t.Fatalf("duplicate pack IDs=%v err=%v, want one owner per distinct pack", duplicated, err)
	}
	sameOwner, err := s.ResearchPackOwnerWorkIDs(ctx, []string{"pack-c", "pack-a"})
	if err != nil || !reflect.DeepEqual(sameOwner, []string{"blocker", "blocker"}) {
		t.Fatalf("two packs one owner=%v err=%v, want no owner dedup", sameOwner, err)
	}
}

func TestResearchPackOwnerWorkIDsRefusesMissingPackAndBounds(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := seedQueryFixture(t)
	if _, err := createResearchPack(ctx, s, CreateResearchPackRequest{
		PackID:      "pack-control",
		OwnerWorkID: "blocked",
		Revision:    researchScopeRevisionInput(),
	}); err != nil {
		t.Fatal(err)
	}
	_, err := s.ResearchPackOwnerWorkIDs(ctx, []string{"pack-control", "pack-absent"})
	assertFailureKind(t, err, KindProjectionNotFound)
	if empty, err := s.ResearchPackOwnerWorkIDs(ctx, nil); err != nil || len(empty) != 0 {
		t.Fatalf("nil pack IDs=%v err=%v, want no identities", empty, err)
	}
	if empty, err := s.ResearchPackOwnerWorkIDs(ctx, []string{}); err != nil || len(empty) != 0 {
		t.Fatalf("empty pack IDs=%v err=%v, want no identities", empty, err)
	}
	bounds := make([]string, 0, 101)
	for i := 0; i < 101; i++ {
		bounds = append(bounds, fmt.Sprintf("pack-bound-%03d", i))
	}
	_, err = s.ResearchPackOwnerWorkIDs(ctx, bounds)
	assertFailureKind(t, err, KindLimitExceeded)
	_, err = s.ResearchPackOwnerWorkIDs(ctx, bounds[:100])
	assertFailureKind(t, err, KindProjectionNotFound)
}

// Resolution stays identities-only: a pack owner without any Product
// membership still resolves here, and the separate Product resolver, not this
// one, owns membership and authorization.
func TestResearchPackOwnerWorkIDsResolvesOwnerWithoutProductMembership(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := seedQueryFixture(t)
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{
		workCreatedEvent("owner-unmapped", "owner-ids-unmapped-work"),
		operationEvent("owner-ids-unmapped-membership", "work_project.added", SubjectWorkItem, "owner-unmapped", map[string]any{
			"work_id": "owner-unmapped", "project_id": "proj", "role": "primary", "reason": "scope test", "expected_version": 1, "resulting_version": 2,
		}),
	}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "owner-unmapped"): 0}}); err != nil {
		t.Fatal(err)
	}
	if _, err := createResearchPack(ctx, s, CreateResearchPackRequest{
		PackID:      "pack-unmapped",
		OwnerWorkID: "owner-unmapped",
		Revision:    researchScopeRevisionInput(),
	}); err != nil {
		t.Fatal(err)
	}
	// Admission refuses a membership-less work item, so the corrupt retained
	// state is isolated after the pack exists.
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1); DELETE FROM work_projects WHERE work_id='owner-unmapped'; DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	owners, err := s.ResearchPackOwnerWorkIDs(ctx, []string{"pack-unmapped"})
	if err != nil || !reflect.DeepEqual(owners, []string{"owner-unmapped"}) {
		t.Fatalf("membership-less owner=%v err=%v, want the owner identity", owners, err)
	}
	products, err := s.ProductsForWorkIDs(ctx, owners)
	if err != nil || len(products["owner-unmapped"]) != 0 {
		t.Fatalf("owner products=%v err=%v, want the separate resolver to own membership", products, err)
	}
}

// The tx form resolves a pack created inside the same transaction before it
// commits, including the uncommitted owner's Product membership, and no
// pooled store call runs while the transaction is open.
func TestResearchPackOwnerWorkIDsTxReadsUncommittedPackAndProducts(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := seedQueryFixture(t)
	raw, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	scope, err := beginWrite(ctx, raw)
	if err != nil {
		_ = raw.Rollback()
		t.Fatal(err)
	}
	transaction := &Transaction{tx: raw, write: scope}
	if _, err := ApplyOperationTx(ctx, transaction, Operation{Events: []Event{
		workCreatedEvent("owner-uncommitted", "owner-ids-tx-work"),
		operationEvent("owner-ids-tx-membership", "work_project.added", SubjectWorkItem, "owner-uncommitted", map[string]any{
			"work_id": "owner-uncommitted", "project_id": "proj", "role": "primary", "reason": "scope test", "expected_version": 1, "resulting_version": 2,
		}),
	}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "owner-uncommitted"): 0}}); err != nil {
		_ = raw.Rollback()
		t.Fatal(err)
	}
	if _, err := CreateResearchPackWithinTx(ctx, transaction, CreateResearchPackRequest{
		PackID:      "pack-uncommitted",
		OwnerWorkID: "owner-uncommitted",
		Revision:    researchScopeRevisionInput(),
	}); err != nil {
		_ = raw.Rollback()
		t.Fatal(err)
	}
	owners, err := ResearchPackOwnerWorkIDsTx(ctx, transaction, []string{"pack-uncommitted"})
	if err != nil || !reflect.DeepEqual(owners, []string{"owner-uncommitted"}) {
		_ = raw.Rollback()
		t.Fatalf("tx owner identities=%v err=%v, want the uncommitted owner", owners, err)
	}
	products, err := ProductsForWorkIDsTx(ctx, transaction, owners)
	if err != nil || !reflect.DeepEqual(products["owner-uncommitted"], []string{"prod"}) {
		_ = raw.Rollback()
		t.Fatalf("tx owner products=%v err=%v, want the uncommitted membership", products, err)
	}
	if err := scope.close(ctx); err != nil {
		_ = raw.Rollback()
		t.Fatal(err)
	}
	if err := raw.Rollback(); err != nil {
		t.Fatal(err)
	}
	_, err = s.ResearchPackOwnerWorkIDs(ctx, []string{"pack-uncommitted"})
	assertFailureKind(t, err, KindProjectionNotFound)
}

package store

// Product resolution for workflow law and architecture binding follows the
// work item's primary Project membership: the primary Project's Product owns
// the contract, so a secondary membership in another Product widens visibility
// only and must never refuse a continuity read, a law check, or completion.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

var crossProductRegistryHash = "sha256:" + strings.Repeat("b", 64)

func crossProductBindingApprovalFixture(t *testing.T, workID string) (*Store, WorkflowActor) {
	t.Helper()
	s := openTemp(t)
	seedWork(t, s, workID)
	seedWorkflowLaw(t, s)
	seedSecondaryProductMembership(t, s, workID)
	actor := WorkflowActor{PrincipalRef: "principal:" + workID, ClientRef: "client:" + workID, AgentRef: "agent:" + workID, SessionRef: "session:" + workID, ActorClass: ActorAgent}
	registered, err := BuiltinWorkflowDefinitionForRef("workflow.implementation")
	if err != nil {
		t.Fatal(err)
	}
	ctx, err := s.EstablishKnowledgeSourceSetProof(context.Background(), workID)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := initializeWorkflowRawTx(ctx, tx, WorkflowInitializationRequest{WorkID: workID, Definition: registered, Actor: actor, Now: time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC)}); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return s, actor
}

func crossProductBindingApproval(t *testing.T, s *Store, workID string, actor WorkflowActor) {
	t.Helper()
	version := int64(4)
	for _, action := range []string{"record_proposal", "record_alignment"} {
		version = issue31WorkflowAction(t, s, workID, version, action, "cross-product-"+workID+"-"+action, actor)
	}
	approval := json.RawMessage(`{"spec_mandate":[],"law_modifies":[],"architecture_binding":{"home_domain_id":"root","affected_domain_ids":["root"],"domain_modifies":[],"domain_relation_modifies":[],"law_additions":[],"verification_obligations":[]}}`)
	issue31WorkflowActionWithPayload(t, s, workID, version, "approve_contract", "cross-product-"+workID+"-approve", actor, approval)
}

// The binding scope resolves over the primary membership alone: a cross-
// Product work item keeps exactly one contract owner, and a complete binding
// validates against that Product's registry.
func TestWorkflowBindingProductFollowsThePrimaryMembership(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openTemp(t)
	workID := "binding-primary-cross-product"
	seedWork(t, s, workID)
	seedWorkflowLaw(t, s)
	seedSecondaryProductMembership(t, s, workID)
	registered, err := BuiltinWorkflowDefinitionForRef("workflow.implementation")
	if err != nil {
		t.Fatal(err)
	}
	// The binding validation reads the committed Domain registry inside the
	// transaction; the source-set proof is established before it opens.
	ctx, err = s.EstablishKnowledgeSourceSetProof(ctx, workID)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	productID, err := workflowBindingProductIDTx(ctx, tx, workID)
	if err != nil {
		t.Fatalf("cross-Product work refused its binding Product: %v", err)
	}
	if productID != "product" {
		t.Fatalf("binding Product = %q, want the primary Product product", productID)
	}
	binding := WorkflowArchitectureBinding{HomeDomainID: "root", AffectedDomainIDs: []string{"root"}, DomainModifies: []string{}, DomainRelationModifies: []WorkflowDomainRelationModification{}, LawAdditions: []WorkflowLawAddition{}, VerificationObligations: []WorkflowVerificationObligation{}}
	if err := validateArchitectureBindingTx(ctx, tx, workID, registered.Definition, &binding, []string{}, []string{}); err != nil {
		t.Fatalf("cross-Product work refused architecture binding validation: %v", err)
	}
}

// A contract approval carrying an architecture binding records even when a
// secondary Project in another Product joins the work: the approval persists,
// and the binding pins the primary Product.
func TestCrossProductWorkApprovesArchitectureBinding(t *testing.T) {
	t.Parallel()
	workID := "cross-product-binding-approval"
	s, actor := crossProductBindingApprovalFixture(t, workID)
	crossProductBindingApproval(t, s, workID, actor)
	var productID string
	if err := s.DatabaseForTesting().QueryRow(`SELECT product_id FROM workflow_architecture_bindings WHERE work_id=? AND contract_version=1`, workID).Scan(&productID); err != nil {
		t.Fatalf("the approved architecture binding did not record: %v", err)
	}
	if productID != "product" {
		t.Fatalf("recorded binding Product = %q, want the primary Product product", productID)
	}
}

// A cross-Product work item with an approved architecture binding reads its
// continuity, and the identity listing keeps every member Product.
func TestContinuityReadsCrossProductWorkWithArchitectureBinding(t *testing.T) {
	t.Parallel()
	workID := "cross-product-continuity"
	s, actor := crossProductBindingApprovalFixture(t, workID)
	crossProductBindingApproval(t, s, workID, actor)
	snapshot, err := ReadWorkflowContinuity(context.Background(), s, ContinuityRequest{Work: workID, Limit: 20})
	if err != nil {
		t.Fatalf("cross-Product continuity read refused: %v", err)
	}
	if snapshot.ArchitectureBinding == nil || snapshot.ArchitectureBinding.HomeDomainID != "root" {
		t.Fatalf("continuity binding = %+v, want the approved root binding", snapshot.ArchitectureBinding)
	}
	if len(snapshot.ProductIdentity) != 2 || snapshot.ProductIdentity[0] != "product" || snapshot.ProductIdentity[1] != "product-secondary" {
		t.Fatalf("ProductIdentity = %v, want [product product-secondary]", snapshot.ProductIdentity)
	}
}

// Both member Products carry a designated knowledge home here; the compaction
// home must resolve over the primary membership's Product alone instead of
// refusing the two candidates as ambiguous.
func TestCompactionHomeFollowsThePrimaryMembership(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	workID := "compaction-primary-cross-product"
	seedWork(t, s, workID)
	seedWorkflowLaw(t, s)
	seedSecondaryProductMembership(t, s, workID)
	home, err := s.ResolveCompactionHome(context.Background(), workID)
	if err != nil {
		t.Fatalf("cross-Product work refused its compaction home: %v", err)
	}
	if home.HomeProjectID != "project" || home.HomeLocatorID != "workflow-law-locator" || home.RepoPath == "" {
		t.Fatalf("compaction home = %s/%s repo=%q, want project/workflow-law-locator", home.HomeProjectID, home.HomeLocatorID, home.RepoPath)
	}
}

// workProductIDs derives exactly one Product over the primary membership, so
// a secondary membership in another Product does not widen the scope.
func TestWorkProductIDsFollowThePrimaryMembership(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openTemp(t)
	seedWork(t, s, "cross-product-work")
	seedSecondaryProductMembership(t, s, "cross-product-work")
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	products, err := seedCrossProductWorkProductIDs(ctx, tx, "cross-product-work")
	if err != nil {
		t.Fatalf("cross-Product work refused its Product scope: %v", err)
	}
	if len(products) != 1 || products[0] != "product" {
		t.Fatalf("Product scope = %v, want [product]", products)
	}
}

// Listing and visibility sites keep every member Product: the cross-Product
// work stays visible in the secondary Product's own work listing and its
// scope resolution carries both identities.
func TestWorkVisibilityListingKeepsEveryMemberProduct(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openTemp(t)
	workID := "listing-cross-product"
	seedWork(t, s, workID)
	seedSecondaryProductMembership(t, s, workID)
	products, err := s.ProductsForWorkIDs(ctx, []string{workID})
	if err != nil {
		t.Fatal(err)
	}
	got := products[workID]
	if len(got) != 2 || got[0] != "product" || got[1] != "product-secondary" {
		t.Fatalf("work scope listing = %v, want [product product-secondary]", got)
	}
	secondary, err := s.QueryQ2(ctx, Q2Request{Product: "product-secondary"})
	if err != nil {
		t.Fatalf("the secondary Product listing refused: %v", err)
	}
	if secondary.LifecycleCounts["needed"] != 1 {
		t.Fatalf("secondary Product needed count = %d, want the cross-Product work visible", secondary.LifecycleCounts["needed"])
	}
}

// seedCrossProductWorkProductIDs derives the Product membership of one work
// item over its primary Project, the same derivation the retired warm-up
// helper performed.
func seedCrossProductWorkProductIDs(ctx context.Context, q queryer, id string) ([]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT DISTINCT pp.product_id FROM work_projects wp JOIN product_projects pp ON pp.project_id=wp.project_id WHERE wp.work_id=? AND wp.role='primary' ORDER BY pp.product_id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

package store

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func readLawContextItem(ctx context.Context, s *Store, workID string) (WorkItem, error) {
	result, err := s.QueryQ3(ctx, Q3Request{Product: "product", WorkIDs: []string{workID}, Detail: "full"})
	if err != nil {
		return WorkItem{}, err
	}
	if len(result.Items) != 1 || result.Items[0].ID != workID {
		return WorkItem{}, fmt.Errorf("Q3 returned unexpected work items: %+v", result.Items)
	}
	return result.Items[0], nil
}

// workflowLawFixtureRepo returns the normalized repository path the law
// fixture registered as the Product knowledge home locator.
func workflowLawFixtureRepo(t *testing.T, s *Store) string {
	t.Helper()
	var repo string
	if err := s.DatabaseForTesting().QueryRow(`SELECT normalized_value FROM project_locators WHERE locator_id='workflow-law-locator'`).Scan(&repo); err != nil {
		t.Fatal(err)
	}
	return repo
}

// seedLawContextFixture initializes one implementation workflow whose approved
// contract binds spec:one (mandated and modified), law:new (added), and the
// root and child Domains, with one verification obligation on spec:one. The
// law source is the committed law folder and Domain registry seedWorkflowLaw
// writes; there is no SQL law projection to seed.
func seedLawContextFixture(t *testing.T, s *Store, workID string, beforeStart ...func()) {
	t.Helper()
	ctx := context.Background()
	seedWork(t, s, workID)
	seedWorkflowLaw(t, s)
	actor := WorkflowActor{PrincipalRef: "principal:law-context", ClientRef: "client:law-context", AgentRef: "agent:law-context", SessionRef: "session:law-context", ActorClass: ActorAgent}
	for _, setup := range beforeStart {
		setup()
	}
	ctx, err := s.EstablishKnowledgeSourceSetProof(ctx, workID)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := initializeWorkflowRawTx(ctx, tx, WorkflowInitializationRequest{WorkID: workID, Actor: actor, Now: time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC)}); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func approveLawContextContract(t *testing.T, s *Store, workID string, mandate, modifies []string, binding WorkflowArchitectureBinding) {
	t.Helper()
	seedWorkflowPlanFrontier(t, s, workID, WorkflowStepPlanChange, "plan")
	ctx, err := s.EstablishKnowledgeSourceSetProof(context.Background(), workID)
	if err != nil {
		t.Fatal(err)
	}
	actor := WorkflowActor{PrincipalRef: "principal:law-context", ClientRef: "client:law-context", AgentRef: "agent:law-context", SessionRef: "session:law-context", ActorClass: ActorAgent}
	version := readWorkVersion(t, s, workID)
	event := workflowEventWithActor("law-context-approval-"+workID, WorkflowContractApproved, workID, DeriveWorkflowActorRef(actor.PrincipalRef, actor.ClientRef, actor.AgentRef, actor.SessionRef), map[string]any{
		"work_id": workID, "expected_version": version, "resulting_version": version + 1, "contract_version": int64(1), "legislative_subjects": []string{}, "spec_mandate": mandate, "law_modifies": modifies, "law_boundary_version": 1, "consequence_class": "internal_sqlite", "architecture_binding": binding,
	})
	event = pOverlapContractFacts(t, s, event)
	if err := applyWorkflowTestOperation(ctx, s, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}}); err != nil {
		t.Fatalf("bound approval rejected: %v", err)
	}
}

func TestFullWorkListResolvesContractLawAndDomainContext(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openTemp(t)
	workID := "law-context-bound"
	seedLawContextFixture(t, s, workID)
	binding := WorkflowArchitectureBinding{HomeDomainID: "root", AffectedDomainIDs: []string{"root", "child"}, DomainAdditions: []string{}, DomainModifies: []string{}, DomainRelationModifies: []WorkflowDomainRelationModification{}, LawAdditions: []WorkflowLawAddition{{LawID: "law:new", HomeDomainID: "child"}}, VerificationObligations: []WorkflowVerificationObligation{{LawID: "spec:one", ObligationID: "verification"}}}
	approveLawContextContract(t, s, workID, []string{"spec:one", "law:new"}, []string{"spec:one"}, binding)
	snapshot, err := readLawContextItem(ctx, s, workID)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.LawContext == nil {
		t.Fatal("law context resolved nothing for a contract that binds law")
	}
	homeRepo := workflowLawFixtureRepo(t, s)
	// Each projected law path is home-qualified and openable inside the
	// knowledge home's committed law folder.
	wantLaws := []WorkflowLawContextLaw{
		{Roles: []string{"added", "mandated"}, LawID: "law:new"},
		{Roles: []string{"mandated", "modified", "obligation"}, LawID: "spec:one", Kind: "spec", Status: "accepted", Title: "Fixture folder law spec:one", Path: filepath.Join(homeRepo, ".concord/law/specs/spec-one-fixture.md"), ObligationIDs: []string{"verification"}},
	}
	if !reflect.DeepEqual(snapshot.LawContext.Laws, wantLaws) {
		t.Fatalf("law context laws = %+v, want %+v", snapshot.LawContext.Laws, wantLaws)
	}
	// The Domains resolve from the committed registry the home carries.
	wantDomains := []WorkflowLawContextDomain{
		{DomainID: "root", Name: "Root", Purpose: "Fixture root child Domain."},
		{DomainID: "child", Name: "Child", Purpose: "Fixture child Domain."},
	}
	if !reflect.DeepEqual(snapshot.LawContext.Domains, wantDomains) {
		t.Fatalf("law context Domains = %+v, want %+v", snapshot.LawContext.Domains, wantDomains)
	}
	// A contract that binds Domains carries an absolute registry locator
	// inside the home checkout, and the file it names opens.
	wantRegistry := filepath.Join(homeRepo, ".concord/docs/knowledge/domain-registry.json")
	if snapshot.LawContext.RegistryPath != wantRegistry {
		t.Fatalf("law context registry path = %q, want %q", snapshot.LawContext.RegistryPath, wantRegistry)
	}
	if !filepath.IsAbs(snapshot.LawContext.RegistryPath) {
		t.Fatalf("law context registry path = %q, want an absolute home-qualified locator", snapshot.LawContext.RegistryPath)
	}
	if _, err := os.ReadFile(snapshot.LawContext.RegistryPath); err != nil {
		t.Fatalf("the recorded registry locator does not open: %v", err)
	}
}

// A contract read that binds law but no Domain carries no registry path: the
// law entries already name their own repository paths, and the registry file
// would be a pointer the context gives the reader no reason to follow. The
// approval route always binds the home Domain, so this state is reachable
// only through the read function's nil-binding contract, which is what the
// field's presence rule keys on.
func TestFullWorkListLawOnlyContextCarriesNoRegistryPath(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openTemp(t)
	workID := "law-context-law-only"
	seedLawContextFixture(t, s, workID)
	// Read the fixture home path before the transaction opens: the store
	// pools one connection, so no s.db read may run while a tx is open. The
	// law context reads the verified snapshot, so the proof is established
	// here the way the item-scoped list route does.
	verified, err := s.EstablishKnowledgeSourceSetProof(ctx, workID)
	if err != nil {
		t.Fatal(err)
	}
	ctx = verified
	wantPath := filepath.Join(workflowLawFixtureRepo(t, s), ".concord/law/specs/spec-one-fixture.md")
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	lawContext, err := readWorkflowLawContext(ctx, tx, workID, &WorkflowReadContract{Version: 1, SpecMandate: []string{"spec:one"}})
	if err != nil {
		t.Fatal(err)
	}
	if lawContext == nil {
		t.Fatal("law-only contract resolved no law context")
	}
	if len(lawContext.Laws) == 0 {
		t.Fatal("law-only context carried no laws")
	}
	if lawContext.RegistryPath != "" {
		t.Fatalf("law-only context registry path = %q, want empty", lawContext.RegistryPath)
	}
	if len(lawContext.Domains) != 0 {
		t.Fatalf("law-only context Domains = %+v, want empty", lawContext.Domains)
	}
	// The law entries carry home-qualified locators even without a binding.
	if lawContext.Laws[0].Path != wantPath {
		t.Fatalf("law-only context law path = %q, want %q", lawContext.Laws[0].Path, wantPath)
	}
}

// Constitution records are law-bearing under the accepted law folder tiers,
// so an approved contract can mandate one. The resolved law context must
// carry the constitution kind, not only decision and spec.
func TestFullWorkListResolvesMandatedConstitutionLaw(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openTemp(t)
	workID := "law-context-constitution"
	seedLawContextFixture(t, s, workID, func() {
		home := KnowledgeHome{HomeProjectID: "project", HomeLocatorID: "workflow-law-locator", RepoPath: workflowLawFixtureRepo(t, s), HeadRef: "HEAD"}
		commitFixtureLawFile(t, home, "const:one", "---\n"+
			`{"id":"const:one","title":"Synthetic constitution","authority":{"tier":"derived"},"home_domain_id":"root","applies_to_domain_ids":[]}`+"\n"+
			"---\n\nSynthetic constitution body.\n")
	})
	binding := WorkflowArchitectureBinding{HomeDomainID: "root", AffectedDomainIDs: []string{"root"}, DomainAdditions: []string{}, DomainModifies: []string{}, DomainRelationModifies: []WorkflowDomainRelationModification{}, LawAdditions: []WorkflowLawAddition{}, VerificationObligations: []WorkflowVerificationObligation{}}
	approveLawContextContract(t, s, workID, []string{"const:one"}, []string{}, binding)
	snapshot, err := readLawContextItem(ctx, s, workID)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.LawContext == nil {
		t.Fatal("law context resolved nothing for a contract that mandates a constitution")
	}
	wantLaws := []WorkflowLawContextLaw{
		{Roles: []string{"mandated"}, LawID: "const:one", Kind: "constitution", Status: "accepted", Title: "Synthetic constitution", Path: filepath.Join(workflowLawFixtureRepo(t, s), ".concord/law/constitution/const-one-fixture.md")},
	}
	if !reflect.DeepEqual(snapshot.LawContext.Laws, wantLaws) {
		t.Fatalf("law context laws = %+v, want %+v", snapshot.LawContext.Laws, wantLaws)
	}
}

// A contract with no bound law still resolves: the law context resolves
// with an empty law list and the binding's Domains alone.
func TestFullWorkListContractWithoutBoundLawCarriesDomainsOnly(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openTemp(t)
	workID := "law-context-unbound"
	seedLawContextFixture(t, s, workID)
	binding := WorkflowArchitectureBinding{HomeDomainID: "root", AffectedDomainIDs: []string{"root"}, DomainAdditions: []string{}, DomainModifies: []string{}, DomainRelationModifies: []WorkflowDomainRelationModification{}, LawAdditions: []WorkflowLawAddition{}, VerificationObligations: []WorkflowVerificationObligation{}}
	approveLawContextContract(t, s, workID, []string{}, []string{}, binding)
	snapshot, err := readLawContextItem(ctx, s, workID)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.LawContext == nil {
		t.Fatal("law context resolved nothing for a bound contract")
	}
	if len(snapshot.LawContext.Laws) != 0 {
		t.Fatalf("unbound law list = %+v, want empty", snapshot.LawContext.Laws)
	}
	wantDomains := []WorkflowLawContextDomain{{DomainID: "root", Name: "Root", Purpose: "Fixture root child Domain."}}
	if !reflect.DeepEqual(snapshot.LawContext.Domains, wantDomains) {
		t.Fatalf("law context Domains = %+v, want %+v", snapshot.LawContext.Domains, wantDomains)
	}
}

func TestFullWorkListReportsAbsentDeletedModifiedLaw(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openTemp(t)
	workID := "law-context-drift-modified"
	seedLawContextFixture(t, s, workID)
	binding := WorkflowArchitectureBinding{HomeDomainID: "root", AffectedDomainIDs: []string{"root", "child"}, DomainAdditions: []string{}, DomainModifies: []string{}, DomainRelationModifies: []WorkflowDomainRelationModification{}, LawAdditions: []WorkflowLawAddition{{LawID: "law:new", HomeDomainID: "child"}}, VerificationObligations: []WorkflowVerificationObligation{}}
	approveLawContextContract(t, s, workID, []string{"spec:one", "law:new"}, []string{"spec:one"}, binding)
	removeCommittedFixtureLaw(t, KnowledgeHome{HomeProjectID: "project", HomeLocatorID: "workflow-law-locator", RepoPath: workflowLawFixtureRepo(t, s), HeadRef: "HEAD"})
	snapshot, err := readLawContextItem(ctx, s, workID)
	if err != nil {
		t.Fatal(err)
	}
	want := []WorkflowLawContextLaw{
		{Roles: []string{"added", "mandated"}, LawID: "law:new"},
		{Roles: []string{"mandated", "modified"}, LawID: "spec:one", Status: "absent"},
	}
	if snapshot.LawContext == nil || !reflect.DeepEqual(snapshot.LawContext.Laws, want) {
		t.Fatalf("deleted modified law context = %+v, want %+v", snapshot.LawContext, want)
	}
}

func TestFullWorkListReportsAbsentDeletedObligationLaw(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openTemp(t)
	workID := "law-context-drift-obligation"
	seedLawContextFixture(t, s, workID)
	binding := WorkflowArchitectureBinding{HomeDomainID: "root", AffectedDomainIDs: []string{"root"}, DomainAdditions: []string{}, DomainModifies: []string{}, DomainRelationModifies: []WorkflowDomainRelationModification{}, LawAdditions: []WorkflowLawAddition{}, VerificationObligations: []WorkflowVerificationObligation{{LawID: "spec:one", ObligationID: "verification"}}}
	approveLawContextContract(t, s, workID, []string{"spec:one"}, []string{}, binding)
	removeCommittedFixtureLaw(t, KnowledgeHome{HomeProjectID: "project", HomeLocatorID: "workflow-law-locator", RepoPath: workflowLawFixtureRepo(t, s), HeadRef: "HEAD"})
	snapshot, err := readLawContextItem(ctx, s, workID)
	if err != nil {
		t.Fatal(err)
	}
	want := []WorkflowLawContextLaw{{Roles: []string{"mandated", "obligation"}, LawID: "spec:one", Status: "absent", ObligationIDs: []string{"verification"}}}
	if snapshot.LawContext == nil || !reflect.DeepEqual(snapshot.LawContext.Laws, want) {
		t.Fatalf("deleted obligation law context = %+v, want %+v", snapshot.LawContext, want)
	}
}

// A home or affected Domain that later disappears from the committed registry
// would reach the context with an empty name and purpose, so the law-context
// read refuses fail-closed.
func TestFullWorkListRefusesDomainMissingFromRegistry(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openTemp(t)
	workID := "law-context-drift-domain"
	seedLawContextFixture(t, s, workID)
	binding := WorkflowArchitectureBinding{HomeDomainID: "root", AffectedDomainIDs: []string{"root", "child"}, DomainAdditions: []string{}, DomainModifies: []string{}, DomainRelationModifies: []WorkflowDomainRelationModification{}, LawAdditions: []WorkflowLawAddition{{LawID: "law:new", HomeDomainID: "child"}}, VerificationObligations: []WorkflowVerificationObligation{}}
	approveLawContextContract(t, s, workID, []string{"spec:one", "law:new"}, []string{"spec:one"}, binding)
	// The registry revision that retires the child Domain commits at the home:
	// a committed registry change is the only way the registry read changes.
	commitWorkflowFixtureRegistry(t, KnowledgeHome{HomeProjectID: "project", HomeLocatorID: "workflow-law-locator", RepoPath: workflowLawFixtureRepo(t, s), HeadRef: "HEAD"},
		`{"schema_version":"1.0","product_key":"product","root_domain_id":"product-root:product","domains":[{"domain_id":"product-root:product","name":"Product","purpose":"Governs the synthetic fixture Product.","status":"current","architecture_relations":[]},{"domain_id":"root","name":"Root","purpose":"Fixture root child Domain.","status":"current","parent_domain_id":"product-root:product","architecture_relations":[]}]}`+"\n")
	_, err := readLawContextItem(ctx, s, workID)
	assertLawContextDrift(t, err, "child", "commit the Domain under the registry at the Product knowledge home through an ordinary reviewed pull request")
}

// assertLawContextDrift proves the law-context read refuses fail-closed on a
// drifted projection with the typed diagnosis the law context owns.
func assertLawContextDrift(t *testing.T, err error, wantCandidate, wantRecovery string) {
	t.Helper()
	var failure *Failure
	if !failureAs(err, &failure) || failure.Kind != KindProjectionNotFound || failure.Op != "read_workflow_law_context" || len(failure.CandidateIDs) != 1 || failure.CandidateIDs[0] != wantCandidate || failure.RecoveryAction != wantRecovery {
		t.Fatalf("drift diagnosis = %v, want typed projection_not_found from the law context with candidate %s and recovery %s", err, wantCandidate, wantRecovery)
	}
}

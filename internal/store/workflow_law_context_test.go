package store

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

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
	definition, ok := BuiltinWorkflowRegistry().Current("workflow.implementation")
	if !ok {
		t.Fatal("latest implementation definition missing")
	}
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
	if err := initializeWorkflowRawTx(ctx, tx, WorkflowInitializationRequest{WorkID: workID, Definition: definition, Actor: actor, Now: time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC)}); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func approveLawContextContract(t *testing.T, s *Store, workID string, mandate, modifies []string, binding WorkflowArchitectureBinding) {
	t.Helper()
	ctx, err := s.EstablishKnowledgeSourceSetProof(context.Background(), workID)
	if err != nil {
		t.Fatal(err)
	}
	repo := workflowFixtureKnowledgeRepo(t, s)
	actor := WorkflowActor{PrincipalRef: "principal:law-context", ClientRef: "client:law-context", AgentRef: "agent:law-context", SessionRef: "session:law-context", ActorClass: ActorAgent}
	revisions := []WorkflowLawRevision{}
	for _, lawID := range mandate {
		if path, committed := workflowFixtureLawPaths[lawID]; committed {
			if _, err := os.Stat(filepath.Join(repo, filepath.FromSlash(path))); err != nil {
				if !os.IsNotExist(err) {
					t.Fatal(err)
				}
				continue
			}
			revisions = append(revisions, WorkflowLawRevision{LawID: lawID, ContentHash: workflowFixtureLawPin(t, repo, lawID)})
		}
	}
	event := workflowEventWithActor("law-context-approval-"+workID, WorkflowContractApproved, workID, DeriveWorkflowActorRef(actor.PrincipalRef, actor.ClientRef, actor.AgentRef, actor.SessionRef), map[string]any{
		"work_id": workID, "expected_version": int64(4), "resulting_version": int64(5), "contract_version": int64(1), "premise": "bind Product law", "outcome_kind": "check", "outcome_payload": map[string]any{"kind": "check", "check_ref": "check:law-context", "immutable_subject_ref": "commit:law-context", "expected_result": "pass"}, "required_evidence": []string{"verification", "review"}, "route_conventions": []string{}, "spec_mandate": mandate, "law_modifies": modifies, "law_revisions": revisions, "law_boundary_version": 1, "rigor_class": "prototype_internal", "consequence_class": "internal_sqlite", "architecture_binding": binding,
	})
	event = pOverlapContractFacts(t, s, event)
	if err := applyWorkflowTestOperation(ctx, s, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): 4}}); err != nil {
		t.Fatalf("bound approval rejected: %v", err)
	}
}

func TestContinuityResolvesContractLawAndDomainContext(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openTemp(t)
	workID := "law-context-bound"
	seedLawContextFixture(t, s, workID)
	binding := WorkflowArchitectureBinding{HomeDomainID: "root", AffectedDomainIDs: []string{"root", "child"}, DomainModifies: []string{}, DomainRelationModifies: []WorkflowDomainRelationModification{}, LawAdditions: []WorkflowLawAddition{{LawID: "law:new", HomeDomainID: "child"}}, VerificationObligations: []WorkflowVerificationObligation{{LawID: "spec:one", ObligationID: "verification"}}}
	approveLawContextContract(t, s, workID, []string{"spec:one", "law:new"}, []string{"spec:one"}, binding)
	snapshot, err := ReadWorkflowContinuity(ctx, s, ContinuityRequest{Work: workID})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.LawContext == nil {
		t.Fatal("continuity resolved no law context for a contract that binds law")
	}
	homeRepo := workflowLawFixtureRepo(t, s)
	// The lane reads each law from the knowledge home's committed law folder,
	// so the projected path is home-qualified and openable.
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
	// The lane reads Domain structure from the knowledge home's repository
	// file, so a contract that binds Domains carries an absolute registry
	// locator inside the home checkout, and the file it names opens.
	wantRegistry := filepath.Join(homeRepo, ".concord/docs/knowledge/domain-registry.json")
	if snapshot.LawContext.RegistryPath != wantRegistry {
		t.Fatalf("law context registry path = %q, want %q", snapshot.LawContext.RegistryPath, wantRegistry)
	}
	if !filepath.IsAbs(snapshot.LawContext.RegistryPath) {
		t.Fatalf("law context registry path = %q, want an absolute home-qualified locator", snapshot.LawContext.RegistryPath)
	}
	if _, err := os.ReadFile(snapshot.LawContext.RegistryPath); err != nil {
		t.Fatalf("the dispatched lane could not open the registry the packet named: %v", err)
	}
}

// A contract read that binds law but no Domain carries no registry path: the
// law entries already name their own repository paths, and the registry file
// would be a pointer the packet gives the lane no reason to follow. The
// approval route always binds the home Domain, so this state is reachable
// only through the read function's nil-binding contract, which is what the
// field's presence rule keys on.
func TestContinuityLawOnlyContextCarriesNoRegistryPath(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openTemp(t)
	workID := "law-context-law-only"
	seedLawContextFixture(t, s, workID)
	// Read the fixture home path before the transaction opens: the store
	// pools one connection, so no s.db read may run while a tx is open. The
	// law context reads the verified snapshot, so the proof is established
	// here the way the continuity route does.
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
// carry the constitution kind, not only decision and spec, or the dispatched
// lane packet refuses its own continuity.
func TestContinuityResolvesMandatedConstitutionLaw(t *testing.T) {
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
	binding := WorkflowArchitectureBinding{HomeDomainID: "root", AffectedDomainIDs: []string{"root"}, DomainModifies: []string{}, DomainRelationModifies: []WorkflowDomainRelationModification{}, LawAdditions: []WorkflowLawAddition{}, VerificationObligations: []WorkflowVerificationObligation{}}
	approveLawContextContract(t, s, workID, []string{"const:one"}, []string{}, binding)
	snapshot, err := ReadWorkflowContinuity(ctx, s, ContinuityRequest{Work: workID})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.LawContext == nil {
		t.Fatal("continuity resolved no law context for a contract that mandates a constitution")
	}
	wantLaws := []WorkflowLawContextLaw{
		{Roles: []string{"mandated"}, LawID: "const:one", Kind: "constitution", Status: "accepted", Title: "Synthetic constitution", Path: filepath.Join(workflowLawFixtureRepo(t, s), ".concord/law/constitution/const-one-fixture.md")},
	}
	if !reflect.DeepEqual(snapshot.LawContext.Laws, wantLaws) {
		t.Fatalf("law context laws = %+v, want %+v", snapshot.LawContext.Laws, wantLaws)
	}
}

// A contract with no bound law still dispatches: the law context resolves
// with an empty law list and the binding's Domains alone.
func TestContinuityContractWithoutBoundLawCarriesDomainsOnly(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openTemp(t)
	workID := "law-context-unbound"
	seedLawContextFixture(t, s, workID)
	binding := WorkflowArchitectureBinding{HomeDomainID: "root", AffectedDomainIDs: []string{"root"}, DomainModifies: []string{}, DomainRelationModifies: []WorkflowDomainRelationModification{}, LawAdditions: []WorkflowLawAddition{}, VerificationObligations: []WorkflowVerificationObligation{}}
	approveLawContextContract(t, s, workID, []string{}, []string{}, binding)
	snapshot, err := ReadWorkflowContinuity(ctx, s, ContinuityRequest{Work: workID})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.LawContext == nil {
		t.Fatal("continuity resolved no law context for a bound contract")
	}
	if len(snapshot.LawContext.Laws) != 0 {
		t.Fatalf("unbound law list = %+v, want empty", snapshot.LawContext.Laws)
	}
	wantDomains := []WorkflowLawContextDomain{{DomainID: "root", Name: "Root", Purpose: "Fixture root child Domain."}}
	if !reflect.DeepEqual(snapshot.LawContext.Domains, wantDomains) {
		t.Fatalf("law context Domains = %+v, want %+v", snapshot.LawContext.Domains, wantDomains)
	}
}

// Deleting a pinned modified law's committed file stops mutation, not
// read-only inspection (CD-0036 D3/D6). Continuity must still expose the
// contract and its stale pin.
func TestContinuityKeepsDeletedModifiedLawInspectable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openTemp(t)
	workID := "law-context-drift-modified"
	seedLawContextFixture(t, s, workID)
	binding := WorkflowArchitectureBinding{HomeDomainID: "root", AffectedDomainIDs: []string{"root", "child"}, DomainModifies: []string{}, DomainRelationModifies: []WorkflowDomainRelationModification{}, LawAdditions: []WorkflowLawAddition{{LawID: "law:new", HomeDomainID: "child"}}, VerificationObligations: []WorkflowVerificationObligation{}}
	approveLawContextContract(t, s, workID, []string{"spec:one", "law:new"}, []string{"spec:one"}, binding)
	pinnedHash := workflowFixtureLawPin(t, workflowLawFixtureRepo(t, s), "spec:one")
	removeCommittedFixtureLaw(t, KnowledgeHome{HomeProjectID: "project", HomeLocatorID: "workflow-law-locator", RepoPath: workflowLawFixtureRepo(t, s), HeadRef: "HEAD"})
	snapshot, err := ReadWorkflowContinuity(ctx, s, ContinuityRequest{Work: workID})
	if err != nil {
		t.Fatalf("read-only continuity of deleted modified law: %v", err)
	}
	if snapshot.Contract == nil || snapshot.Contract.Version != 1 {
		t.Fatalf("continuity lost active contract: %+v", snapshot.Contract)
	}
	if snapshot.StaleLawRevision == nil || snapshot.StaleLawRevision.OldLawID != "spec:one" || snapshot.StaleLawRevision.OldContentHash != pinnedHash {
		t.Fatalf("deleted modified law diagnosis = %+v", snapshot.StaleLawRevision)
	}
}

// An obligation's deleted law remains inspectable with the same stale pin.
func TestContinuityKeepsDeletedObligationLawInspectable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openTemp(t)
	workID := "law-context-drift-obligation"
	seedLawContextFixture(t, s, workID)
	binding := WorkflowArchitectureBinding{HomeDomainID: "root", AffectedDomainIDs: []string{"root"}, DomainModifies: []string{}, DomainRelationModifies: []WorkflowDomainRelationModification{}, LawAdditions: []WorkflowLawAddition{}, VerificationObligations: []WorkflowVerificationObligation{{LawID: "spec:one", ObligationID: "verification"}}}
	approveLawContextContract(t, s, workID, []string{"spec:one"}, []string{}, binding)
	pinnedHash := workflowFixtureLawPin(t, workflowLawFixtureRepo(t, s), "spec:one")
	removeCommittedFixtureLaw(t, KnowledgeHome{HomeProjectID: "project", HomeLocatorID: "workflow-law-locator", RepoPath: workflowLawFixtureRepo(t, s), HeadRef: "HEAD"})
	snapshot, err := ReadWorkflowContinuity(ctx, s, ContinuityRequest{Work: workID})
	if err != nil {
		t.Fatalf("read-only continuity of deleted obligation law: %v", err)
	}
	if snapshot.Contract == nil || snapshot.Contract.Version != 1 {
		t.Fatalf("continuity lost active contract: %+v", snapshot.Contract)
	}
	if snapshot.StaleLawRevision == nil || snapshot.StaleLawRevision.OldLawID != "spec:one" || snapshot.StaleLawRevision.OldContentHash != pinnedHash {
		t.Fatalf("deleted obligation law diagnosis = %+v", snapshot.StaleLawRevision)
	}
}

// The law context carries the mandated spec's criteria bound to the reading
// work item's own predicates (CD-0180), resolved out of the committed law
// file's authored bindings. Bindings naming another work item, and scenario
// or exemption bindings, stay off the packet's law context.
func TestContinuityLawContextCarriesThisWorkPredicateCriteria(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openTemp(t)
	workID := "work-0a1b2c3d"
	otherWork := "work-9f8e7d6c"
	seedLawContextFixture(t, s, workID, func() {
		home := KnowledgeHome{HomeProjectID: "project", HomeLocatorID: "workflow-law-locator", RepoPath: workflowLawFixtureRepo(t, s), HeadRef: "HEAD"}
		commitFixtureLawFile(t, home, "spec:one", fixtureLawFolderLawWithCriteria("spec:one",
			`[{"criterion":2,"work_id":"`+workID+`","predicate_id":"predicate:alpha"},`+
				`{"criterion":1,"work_id":"`+workID+`","predicate_id":"predicate:beta"},`+
				`{"criterion":3,"work_id":"`+otherWork+`","predicate_id":"predicate:gamma"},`+
				`{"criterion":4,"scenario":"scenario-one"}]`))
	})
	binding := WorkflowArchitectureBinding{HomeDomainID: "root", AffectedDomainIDs: []string{"root"}, DomainModifies: []string{}, DomainRelationModifies: []WorkflowDomainRelationModification{}, LawAdditions: []WorkflowLawAddition{}, VerificationObligations: []WorkflowVerificationObligation{}}
	approveLawContextContract(t, s, workID, []string{"spec:one"}, []string{}, binding)
	snapshot, err := ReadWorkflowContinuity(ctx, s, ContinuityRequest{Work: workID})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.LawContext == nil || len(snapshot.LawContext.Laws) != 1 {
		t.Fatalf("law context laws = %+v, want one mandated spec", snapshot.LawContext)
	}
	want := []WorkflowLawContextCriterionBinding{{Criterion: 1, PredicateID: "predicate:beta"}, {Criterion: 2, PredicateID: "predicate:alpha"}}
	if got := snapshot.LawContext.Laws[0].Criteria; !reflect.DeepEqual(got, want) {
		t.Fatalf("law context criteria = %+v, want %+v", got, want)
	}
}

// A home or affected Domain that later disappears from the committed registry
// would reach the packet with an empty name and purpose, so the continuity
// read refuses fail-closed.
func TestContinuityRefusesDomainMissingFromRegistry(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openTemp(t)
	workID := "law-context-drift-domain"
	seedLawContextFixture(t, s, workID)
	binding := WorkflowArchitectureBinding{HomeDomainID: "root", AffectedDomainIDs: []string{"root", "child"}, DomainModifies: []string{}, DomainRelationModifies: []WorkflowDomainRelationModification{}, LawAdditions: []WorkflowLawAddition{{LawID: "law:new", HomeDomainID: "child"}}, VerificationObligations: []WorkflowVerificationObligation{}}
	approveLawContextContract(t, s, workID, []string{"spec:one", "law:new"}, []string{"spec:one"}, binding)
	// The registry revision that retires the child Domain commits at the home:
	// a committed registry change is the only way the registry read changes.
	commitWorkflowFixtureRegistry(t, KnowledgeHome{HomeProjectID: "project", HomeLocatorID: "workflow-law-locator", RepoPath: workflowLawFixtureRepo(t, s), HeadRef: "HEAD"},
		`{"schema_version":"1.0","product_key":"product","root_domain_id":"product-root:product","domains":[{"domain_id":"product-root:product","name":"Product","purpose":"Governs the synthetic fixture Product.","status":"current","architecture_relations":[]},{"domain_id":"root","name":"Root","purpose":"Fixture root child Domain.","status":"current","parent_domain_id":"product-root:product","architecture_relations":[]}]}`+"\n")
	_, err := ReadWorkflowContinuity(ctx, s, ContinuityRequest{Work: workID})
	assertLawContextDrift(t, err, "child", "commit the Domain under the registry at the Product knowledge home through an ordinary reviewed pull request")
}

// assertLawContextDrift proves the continuity read refuses fail-closed on a
// drifted projection with the typed diagnosis the law context owns.
func assertLawContextDrift(t *testing.T, err error, wantCandidate, wantRecovery string) {
	t.Helper()
	var failure *Failure
	if !failureAs(err, &failure) || failure.Kind != KindProjectionNotFound || failure.Op != "read_workflow_law_context" || len(failure.CandidateIDs) != 1 || failure.CandidateIDs[0] != wantCandidate || failure.RecoveryAction != wantRecovery {
		t.Fatalf("drift diagnosis = %v, want typed projection_not_found from the law context with candidate %s and recovery %s", err, wantCandidate, wantRecovery)
	}
}

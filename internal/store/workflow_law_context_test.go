package store

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"
)

// seedLawContextFixture initializes one implementation workflow whose approved
// contract binds spec:one (mandated and modified), law:new (added), and the
// root and child Domains, with one verification obligation on spec:one.
func seedLawContextFixture(t *testing.T, s *Store, workID string) {
	t.Helper()
	ctx := context.Background()
	seedWork(t, s, workID)
	seedWorkflowLaw(t, s)
	hash := "sha256:" + strings.Repeat("b", 64)
	seedTx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := enterFold(ctx, seedTx); err != nil {
		seedTx.Rollback()
		t.Fatal(err)
	}
	seedSQL := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO domain_registries(product_id,home_project_id,home_locator_id,product_key,root_domain_id,schema_version,content_hash,scanned_commit_oid) VALUES('product','project','workflow-law-locator','product','root','1.0',?,'test')`, []any{hash}},
		{`INSERT INTO domains(home_project_id,home_locator_id,product_id,domain_id,name,purpose,status,registry_content_hash,scanned_commit_oid) VALUES('project','workflow-law-locator','product','root','Root','Product law','current',?,'test')`, []any{hash}},
		{`INSERT INTO domains(home_project_id,home_locator_id,product_id,domain_id,name,purpose,parent_domain_id,status,registry_content_hash,scanned_commit_oid) VALUES('project','workflow-law-locator','product','child','Child','Child law','root','current',?,'test')`, []any{hash}},
		{`INSERT INTO law_domain_homes(home_project_id,home_locator_id,law_id,product_id,domain_id,law_content_hash,scanned_commit_oid) SELECT 'project','workflow-law-locator','spec:one','product','root',content_hash,'test' FROM law_subjects WHERE home_project_id='project' AND home_locator_id='workflow-law-locator' AND law_id='spec:one'`, nil},
	}
	for _, statement := range seedSQL {
		if _, err := seedTx.ExecContext(ctx, statement.query, statement.args...); err != nil {
			seedTx.Rollback()
			t.Fatal(err)
		}
	}
	if err := leaveFold(ctx, seedTx); err != nil {
		seedTx.Rollback()
		t.Fatal(err)
	}
	if err := seedTx.Commit(); err != nil {
		t.Fatal(err)
	}
	actor := WorkflowActor{PrincipalRef: "principal:law-context", ClientRef: "client:law-context", AgentRef: "agent:law-context", SessionRef: "session:law-context", ActorClass: ActorAgent}
	definition, ok := BuiltinWorkflowRegistry().Lookup("workflow.implementation", 1)
	if !ok {
		t.Fatal("latest implementation definition missing")
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
	ctx := context.Background()
	actor := WorkflowActor{PrincipalRef: "principal:law-context", ClientRef: "client:law-context", AgentRef: "agent:law-context", SessionRef: "session:law-context", ActorClass: ActorAgent}
	pins := map[string]string{
		"spec:one":  "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"const:one": "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
	}
	revisions := []WorkflowLawRevision{}
	for _, lawID := range mandate {
		if hash, ok := pins[lawID]; ok {
			revisions = append(revisions, WorkflowLawRevision{LawID: lawID, ContentHash: hash})
		}
	}
	event := workflowEventWithActor("law-context-approval-"+workID, WorkflowContractApproved, workID, DeriveWorkflowActorRef(actor.PrincipalRef, actor.ClientRef, actor.AgentRef, actor.SessionRef), map[string]any{
		"work_id": workID, "expected_version": int64(4), "resulting_version": int64(5), "contract_version": int64(1), "premise": "bind Product law", "outcome_kind": "check", "outcome_payload": map[string]any{"kind": "check", "check_ref": "check:law-context", "immutable_subject_ref": "commit:law-context", "expected_result": "pass"}, "required_evidence": []string{"verification", "review"}, "route_conventions": []string{}, "spec_mandate": mandate, "law_modifies": modifies, "law_revisions": revisions, "law_boundary_version": 1, "rigor_class": "prototype_internal", "consequence_class": "internal_sqlite", "architecture_binding": binding,
	})
	event.PayloadVersion = 3
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
	binding := WorkflowArchitectureBinding{DomainRegistryContentHash: "sha256:" + strings.Repeat("b", 64), HomeDomainID: "root", AffectedDomainIDs: []string{"root", "child"}, DomainModifies: []string{}, DomainRelationModifies: []WorkflowDomainRelationModification{}, LawAdditions: []WorkflowLawAddition{{LawID: "law:new", HomeDomainID: "child"}}, VerificationObligations: []WorkflowVerificationObligation{{LawID: "spec:one", ObligationID: "verification"}}}
	approveLawContextContract(t, s, workID, []string{"spec:one", "law:new"}, []string{"spec:one"}, binding)
	snapshot, err := ReadWorkflowContinuity(ctx, s, ContinuityRequest{Work: workID})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.LawContext == nil {
		t.Fatal("continuity resolved no law context for a contract that binds law")
	}
	wantLaws := []WorkflowLawContextLaw{
		{Roles: []string{"added", "mandated"}, LawID: "law:new"},
		{Roles: []string{"mandated", "modified", "obligation"}, LawID: "spec:one", Kind: "spec", Status: "accepted", Title: "Synthetic test law", Path: "docs/spec.md", ObligationIDs: []string{"verification"}},
	}
	if !reflect.DeepEqual(snapshot.LawContext.Laws, wantLaws) {
		t.Fatalf("law context laws = %+v, want %+v", snapshot.LawContext.Laws, wantLaws)
	}
	wantDomains := []WorkflowLawContextDomain{
		{DomainID: "root", Name: "Root", Purpose: "Product law"},
		{DomainID: "child", Name: "Child", Purpose: "Child law"},
	}
	if !reflect.DeepEqual(snapshot.LawContext.Domains, wantDomains) {
		t.Fatalf("law context Domains = %+v, want %+v", snapshot.LawContext.Domains, wantDomains)
	}
}

// Constitution records are law-bearing under the accepted knowledge taxonomy
// and project into law_subjects, so an approved contract can mandate one. The
// resolved law context must carry the constitution kind, not only decision
// and spec, or the dispatched lane packet refuses its own continuity.
func TestContinuityResolvesMandatedConstitutionLaw(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openTemp(t)
	workID := "law-context-constitution"
	seedLawContextFixture(t, s, workID)
	constitutionHash := "sha256:" + strings.Repeat("c", 64)
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1); INSERT INTO law_subjects(home_project_id,home_locator_id,law_id,kind,status,path,title,content_hash,scanned_commit_oid) VALUES('project','workflow-law-locator','const:one','constitution','accepted','docs/constitution.md','Synthetic constitution',?,'test'); INSERT INTO law_domain_homes(home_project_id,home_locator_id,law_id,product_id,domain_id,law_content_hash,scanned_commit_oid) VALUES('project','workflow-law-locator','const:one','product','root',?,'test'); DELETE FROM fold_guard`, constitutionHash, constitutionHash); err != nil {
		t.Fatal(err)
	}
	binding := WorkflowArchitectureBinding{DomainRegistryContentHash: "sha256:" + strings.Repeat("b", 64), HomeDomainID: "root", AffectedDomainIDs: []string{"root"}, DomainModifies: []string{}, DomainRelationModifies: []WorkflowDomainRelationModification{}, LawAdditions: []WorkflowLawAddition{}, VerificationObligations: []WorkflowVerificationObligation{}}
	approveLawContextContract(t, s, workID, []string{"const:one"}, []string{}, binding)
	snapshot, err := ReadWorkflowContinuity(ctx, s, ContinuityRequest{Work: workID})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.LawContext == nil {
		t.Fatal("continuity resolved no law context for a contract that mandates a constitution")
	}
	wantLaws := []WorkflowLawContextLaw{
		{Roles: []string{"mandated"}, LawID: "const:one", Kind: "constitution", Status: "accepted", Title: "Synthetic constitution", Path: "docs/constitution.md"},
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
	binding := WorkflowArchitectureBinding{DomainRegistryContentHash: "sha256:" + strings.Repeat("b", 64), HomeDomainID: "root", AffectedDomainIDs: []string{"root"}, DomainModifies: []string{}, DomainRelationModifies: []WorkflowDomainRelationModification{}, LawAdditions: []WorkflowLawAddition{}, VerificationObligations: []WorkflowVerificationObligation{}}
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
	wantDomains := []WorkflowLawContextDomain{{DomainID: "root", Name: "Root", Purpose: "Product law"}}
	if !reflect.DeepEqual(snapshot.LawContext.Domains, wantDomains) {
		t.Fatalf("law context Domains = %+v, want %+v", snapshot.LawContext.Domains, wantDomains)
	}
}

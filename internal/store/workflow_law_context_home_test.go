package store

// The workflow law context must resolve its file locators against the
// Product's registered knowledge home, not against the repository a lane
// happens to be dispatched into. A lane holds no Concord tool access
// (CD-0223), so every locator it receives must open as a regular file in the
// knowledge home's checkout, under a shard layout that checkout actually
// carries (CD-0194 D5), and a required source that no longer resolves must
// refuse typed instead of advertising an unreadable locator.

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// seedCrossRepositoryLawFixture initializes one implementation workflow whose
// work item sits in Project B (the dispatch repository) while the Product's
// knowledge home is Project A. Project A carries a canonical-layout registry
// and the bound law document; Project B carries a checkout with no registry
// at all, so only a home-qualified locator can name a readable file.
func seedCrossRepositoryLawFixture(t *testing.T, s *Store, workID string) string {
	t.Helper()
	seedWork(t, s, workID)
	homeDir := initKnowledgeRepo(t)
	dispatchDir := t.TempDir()
	registryDir := filepath.Join(homeDir, ".concord", "docs", "knowledge")
	if err := os.MkdirAll(registryDir, 0o700); err != nil {
		t.Fatal(err)
	}
	registry := `{"schema_version":"1.0","product_key":"product","root_domain_id":"product-root:product","domains":[{"domain_id":"product-root:product","name":"Root","purpose":"Synthetic law","status":"current","architecture_relations":[]}]}`
	if err := os.WriteFile(filepath.Join(registryDir, "domain-registry.json"), []byte(registry+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	specDir := filepath.Join(homeDir, ".concord", "law", "specs")
	if err := os.MkdirAll(specDir, 0o700); err != nil {
		t.Fatal(err)
	}
	spec := `{"id":"spec:one","title":"Synthetic home law","authority":{"tier":"derived"},"home_domain_id":"product-root:product","applies_to_domain_ids":[],"product_wide_rationale":"Synthetic law spans the fixture Product."}`
	if err := os.WriteFile(filepath.Join(specDir, "spec-one-fixture.md"), []byte("---\n"+spec+"\n---\n\nSynthetic home law body.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	seed := func(query string, args ...any) {
		t.Helper()
		if _, err := s.DatabaseForTesting().Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	// The driver binds placeholders per statement, so distinct path arguments
	// must not share an Exec string.
	seed(`INSERT INTO write_guard(active) VALUES(1)`)
	seed(`INSERT INTO project_locators(locator_id,project_id,kind,locator_value,normalized_value,created_at,updated_at) VALUES('project-dispatch-locator','project','canonical_path',?,?,'now','now')`, dispatchDir, dispatchDir)
	seed(`INSERT INTO projects(id,display_name,version,created_at,updated_at) VALUES('project-home','Home',1,'now','now')`)
	seed(`INSERT INTO product_projects(product_id,project_id,role) VALUES('product','project-home','secondary')`)
	seed(`INSERT INTO project_locators(locator_id,project_id,kind,locator_value,normalized_value,created_at,updated_at) VALUES('project-home-locator','project-home','canonical_path',?,?,'now','now')`, homeDir, homeDir)
	seed(`INSERT INTO product_knowledge_homes(product_id,project_id,locator_id) VALUES('product','project-home','project-home-locator')`)
	seed(`DELETE FROM write_guard`)
	commitKnowledgeRepo(t, homeDir, "synthetic cross-repository law source")
	// The approval helper derives its actor ref from the law-context actor
	// identity, so initialization records exactly that actor.
	actor := WorkflowActor{PrincipalRef: "principal:law-context", ClientRef: "client:law-context", AgentRef: "agent:law-context", SessionRef: "session:law-context", ActorClass: ActorAgent}
	definition, ok := BuiltinWorkflowRegistry().Current("workflow.implementation")
	if !ok {
		t.Fatal("latest implementation definition missing")
	}
	ctx, err := s.EstablishKnowledgeSourceSetProof(context.Background(), workID)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := initializeWorkflowRawTx(ctx, tx, WorkflowInitializationRequest{WorkID: workID, Definition: definition, Actor: actor, Now: time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)}); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var homeRepo string
	if err := s.DatabaseForTesting().QueryRow(`SELECT normalized_value FROM project_locators WHERE locator_id='project-home-locator'`).Scan(&homeRepo); err != nil {
		t.Fatal(err)
	}
	return homeRepo
}

// Project A is the knowledge home and Project B is the dispatch repository:
// the law context's registry and law locators must name files inside Project
// A's checkout, and the lane must be able to open both.
func TestContinuityQualifiesLawContextWithTheRegisteredKnowledgeHome(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openTemp(t)
	workID := "law-context-cross-repo"
	homeRepo := seedCrossRepositoryLawFixture(t, s, workID)
	binding := WorkflowArchitectureBinding{HomeDomainID: "product-root:product", AffectedDomainIDs: []string{"product-root:product"}, DomainModifies: []string{}, DomainRelationModifies: []WorkflowDomainRelationModification{}, LawAdditions: []WorkflowLawAddition{}, VerificationObligations: []WorkflowVerificationObligation{}}
	approveLawContextContract(t, s, workID, []string{"spec:one"}, []string{}, binding)
	snapshot, err := ReadWorkflowContinuity(ctx, s, ContinuityRequest{Work: workID})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.LawContext == nil {
		t.Fatal("continuity resolved no law context for a contract that binds law")
	}
	wantRegistry := filepath.Join(homeRepo, ".concord/docs/knowledge/domain-registry.json")
	if snapshot.LawContext.RegistryPath != wantRegistry {
		t.Fatalf("law context registry path = %q, want the knowledge home's %q", snapshot.LawContext.RegistryPath, wantRegistry)
	}
	if !filepath.IsAbs(snapshot.LawContext.RegistryPath) {
		t.Fatalf("law context registry path = %q, want an absolute home-qualified locator", snapshot.LawContext.RegistryPath)
	}
	if _, err := os.ReadFile(snapshot.LawContext.RegistryPath); err != nil {
		t.Fatalf("the dispatched lane could not open the registry the packet named: %v", err)
	}
	if len(snapshot.LawContext.Laws) != 1 {
		t.Fatalf("law context laws = %+v, want one mandated spec", snapshot.LawContext.Laws)
	}
	wantLawPath := filepath.Join(homeRepo, ".concord/law/specs/spec-one-fixture.md")
	if snapshot.LawContext.Laws[0].Path != wantLawPath {
		t.Fatalf("law path = %q, want the knowledge home's %q", snapshot.LawContext.Laws[0].Path, wantLawPath)
	}
	if _, err := os.ReadFile(snapshot.LawContext.Laws[0].Path); err != nil {
		t.Fatalf("the dispatched lane could not open the law file the packet named: %v", err)
	}
}

// A knowledge home whose committed revision carries no registry under any
// supported layout refuses the law context typed: the packet must never
// advertise a locator no lane can open, and must never silently drop the
// binding. The contract is approved while the registry is committed; the
// registry's committed retirement is the next revision the read verifies.
func TestContinuityRefusesUnresolvableRegistrySource(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openTemp(t)
	workID := "law-context-missing-registry"
	seedLawContextFixture(t, s, workID)
	binding := WorkflowArchitectureBinding{HomeDomainID: "root", AffectedDomainIDs: []string{"root"}, DomainModifies: []string{}, DomainRelationModifies: []WorkflowDomainRelationModification{}, LawAdditions: []WorkflowLawAddition{}, VerificationObligations: []WorkflowVerificationObligation{}}
	approveLawContextContract(t, s, workID, []string{}, []string{}, binding)
	homeRepo := workflowLawFixtureRepo(t, s)
	if err := os.Remove(filepath.Join(homeRepo, ".concord/docs/knowledge/domain-registry.json")); err != nil {
		t.Fatal(err)
	}
	commitKnowledgeRepo(t, homeRepo, "retire the registry revision")
	_, err := ReadWorkflowContinuity(ctx, s, ContinuityRequest{Work: workID})
	var failure *Failure
	if !failureAs(err, &failure) || failure.Kind != KindDomainRegistryAbsent {
		t.Fatalf("missing registry diagnosis = %v, want typed domain_registry_absent from the committed read", err)
	}
}

// A resolved Git law home whose locator lost its canonical repository path
// refuses typed, rather than qualifying a locator the store cannot ground.
func TestWorkflowLawHomeRepoRefusesAnUnresolvedCanonicalPath(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openTemp(t)
	seedWork(t, s, "law-home-repo-missing")
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	_, err = workflowLawHomeRepo(ctx, tx, "project", "no-such-locator")
	var failure *Failure
	if !failureAs(err, &failure) || failure.Kind != KindUnknownScope || failure.Op != "read_workflow_law_context" {
		t.Fatalf("unresolved home repository diagnosis = %v, want typed unknown_scope", err)
	}
}

// readLawOnlyContext resolves a law-only contract through the owning read
// function under one transaction. The approval route always binds the home
// Domain, so a law-only context is reachable only through the nil-binding
// contract the read function accepts directly.
func readLawOnlyContext(t *testing.T, s *Store, workID string, mandate []string) (*WorkflowLawContext, error) {
	t.Helper()
	ctx := context.Background()
	// The law context reads the verified snapshot; the proof is established
	// the way the continuity route does before the read transaction opens.
	verified, err := s.EstablishKnowledgeSourceSetProof(ctx, workID)
	if err != nil {
		return nil, err
	}
	ctx = verified
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	return readWorkflowLawContext(ctx, tx, workID, &WorkflowReadContract{Version: 1, SpecMandate: mandate})
}

// A bound law document the projection still records must open as a regular
// file inside the knowledge home checkout: the context refuses typed rather
// than advertise a locator the dispatched lane cannot read.
func TestLawContextRefusesRemovedBoundLawDocument(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	workID := "law-context-law-file-removed"
	seedLawContextFixture(t, s, workID)
	homeRepo := workflowLawFixtureRepo(t, s)
	if err := os.Remove(filepath.Join(homeRepo, ".concord", "law", "specs", "spec-one-fixture.md")); err != nil {
		t.Fatal(err)
	}
	lawContext, err := readLawOnlyContext(t, s, workID, []string{"spec:one"})
	if lawContext != nil {
		t.Fatalf("removed law document still resolved a context: %+v", lawContext)
	}
	var failure *Failure
	if !failureAs(err, &failure) || failure.Kind != KindProjectionNotFound || failure.Op != "read_workflow_law_context" || len(failure.CandidateIDs) != 1 || failure.CandidateIDs[0] != "spec:one" {
		t.Fatalf("removed law document diagnosis = %v, want typed projection_not_found naming spec:one", err)
	}
}

// The whole knowledge home checkout disappearing before a law-only read must
// refuse typed too: an absent home cannot ground any locator the packet
// advertises, and the lane has no Concord route to repair it. The committed
// read resolves the home's git head first, so the refusal is the
// verification's git_unreachable, before any locator is qualified.
func TestLawContextRefusesRemovedKnowledgeHomeBeforeLawOnlyRead(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	workID := "law-context-home-removed"
	seedLawContextFixture(t, s, workID)
	if err := os.RemoveAll(workflowLawFixtureRepo(t, s)); err != nil {
		t.Fatal(err)
	}
	lawContext, err := readLawOnlyContext(t, s, workID, []string{"spec:one"})
	if lawContext != nil {
		t.Fatalf("removed knowledge home still resolved a context: %+v", lawContext)
	}
	var failure *Failure
	if !failureAs(err, &failure) || failure.Kind != KindGitUnreachable || failure.Op != "knowledge_home" {
		t.Fatalf("removed knowledge home diagnosis = %v, want typed git_unreachable from the pre-transaction verification", err)
	}
}

// A registry locator occupied by a directory is not a registry the lane can
// read: the context refuses typed instead of advertising the directory. The
// contract is approved while the registry file is committed; the committed
// revision that replaces it with a directory is what the read verifies.
func TestContinuityRefusesRegistryReplacedByDirectory(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openTemp(t)
	workID := "law-context-registry-directory"
	seedLawContextFixture(t, s, workID)
	binding := WorkflowArchitectureBinding{HomeDomainID: "root", AffectedDomainIDs: []string{"root"}, DomainModifies: []string{}, DomainRelationModifies: []WorkflowDomainRelationModification{}, LawAdditions: []WorkflowLawAddition{}, VerificationObligations: []WorkflowVerificationObligation{}}
	approveLawContextContract(t, s, workID, []string{}, []string{}, binding)
	homeRepo := workflowLawFixtureRepo(t, s)
	registryPath := filepath.Join(homeRepo, ".concord", "docs", "knowledge", "domain-registry.json")
	if err := os.Remove(registryPath); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(registryPath, 0o700); err != nil {
		t.Fatal(err)
	}
	commitKnowledgeRepo(t, homeRepo, "replace the registry with a directory")
	_, err := ReadWorkflowContinuity(ctx, s, ContinuityRequest{Work: workID})
	var failure *Failure
	if !failureAs(err, &failure) || failure.Kind != KindDomainRegistryAbsent {
		t.Fatalf("directory registry diagnosis = %v, want typed domain_registry_absent from the committed read", err)
	}
}

func TestLawContextLocatorsRequireReadableSources(t *testing.T) {
	t.Parallel()
	for _, source := range []string{"law", "registry"} {
		t.Run(source, func(t *testing.T) {
			s := openTemp(t)
			seedLawContextFixture(t, s, "law-context-readable-source")
			home := workflowLawFixtureRepo(t, s)
			relative := ".concord/law/specs/spec-one-fixture.md"
			if source == "registry" {
				relative = knowledgeRegistryPath
			}
			path := filepath.Join(home, relative)
			if err := os.Chmod(path, 0); err != nil {
				t.Fatal(err)
			}
			// Privileged processes can read mode-000 files. Compare against the
			// actual process's open result rather than assuming its privileges.
			probe, openErr := os.Open(path)
			if openErr == nil {
				if err := probe.Close(); err != nil {
					t.Fatal(err)
				}
			}
			var err error
			if source == "registry" {
				_, err = knowledgeRegistryLocator(home)
			} else {
				_, err = knowledgeLawLocator(home, "spec:one", relative)
			}
			if openErr == nil {
				if err != nil {
					t.Fatalf("readable source refused: %v", err)
				}
				return
			}
			var failure *Failure
			if !failureAs(err, &failure) || failure.Kind != KindUnavailable {
				t.Fatalf("unreadable %s locator diagnosis = %v, want typed unavailable", source, err)
			}
		})
	}
}

// The law context reads the one committed registry at its existing path: a
// leftover registry under a pre-migration layout tier never stands in for
// the registry file the committed revision does not carry.
func TestContinuityRegistryDoesNotFallBackAcrossTheSelectedLayout(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openTemp(t)
	workID := "law-context-selected-layout"
	seedLawContextFixture(t, s, workID)
	binding := WorkflowArchitectureBinding{HomeDomainID: "root", AffectedDomainIDs: []string{"root"}, DomainModifies: []string{}, DomainRelationModifies: []WorkflowDomainRelationModification{}, LawAdditions: []WorkflowLawAddition{}, VerificationObligations: []WorkflowVerificationObligation{}}
	approveLawContextContract(t, s, workID, []string{}, []string{}, binding)
	homeRepo := workflowLawFixtureRepo(t, s)
	if err := os.Remove(filepath.Join(homeRepo, filepath.FromSlash(knowledgeRegistryPath))); err != nil {
		t.Fatal(err)
	}
	commitKnowledgeRepo(t, homeRepo, "retire the canonical registry revision")
	legacyDir := filepath.Join(homeRepo, "docs", "knowledge")
	if err := os.MkdirAll(legacyDir, 0o700); err != nil {
		t.Fatal(err)
	}
	registry := `{"schema_version":"1.0","product_key":"product","root_domain_id":"root","domains":[]}`
	if err := os.WriteFile(filepath.Join(legacyDir, "domain-registry.json"), []byte(registry+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := ReadWorkflowContinuity(ctx, s, ContinuityRequest{Work: workID})
	var failure *Failure
	if !failureAs(err, &failure) || failure.Kind != KindDomainRegistryAbsent {
		t.Fatalf("selected-layout registry diagnosis = %v (context %+v), want typed domain_registry_absent", err, snapshot.LawContext)
	}
}

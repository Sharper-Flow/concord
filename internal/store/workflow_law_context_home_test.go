package store

// The workflow law context must resolve its file locators against the
// Product's registered knowledge home, not against the repository a lane
// happens to be dispatched into. A lane holds no Concord tool access
// (CD-0196), so every locator it receives must open as a regular file in the
// knowledge home's checkout, under a shard layout that checkout actually
// carries (CD-0194 D5), and a required source that no longer resolves must
// refuse typed instead of advertising an unreadable locator.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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
	ctx := context.Background()
	seedWork(t, s, workID)
	homeDir := t.TempDir()
	dispatchDir := t.TempDir()
	registryDir := filepath.Join(homeDir, ".concord", "docs", "knowledge")
	if err := os.MkdirAll(registryDir, 0o700); err != nil {
		t.Fatal(err)
	}
	registry := `{"schema_version":"1.0","product_key":"product","root_domain_id":"root","domains":[]}`
	if err := os.WriteFile(filepath.Join(registryDir, "domain-registry.json"), []byte(registry+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(registryDir, "manifest.json"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(homeDir, ".concord", "docs"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(homeDir, ".concord", "docs", "spec.md"), []byte("# Synthetic home law\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	hash := "sha256:" + strings.Repeat("b", 64)
	// The registry/domain rows carry mutually deferred foreign keys, so the
	// seeding commits inside one transaction, mirroring seedLawContextFixture.
	// One statement group per Exec: the driver binds placeholders per
	// statement, so distinct path arguments must not share an Exec string.
	seedTx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := enterFold(ctx, seedTx); err != nil {
		seedTx.Rollback()
		t.Fatal(err)
	}
	if _, err := seedTx.ExecContext(ctx, `INSERT INTO project_locators(locator_id,project_id,kind,locator_value,normalized_value,created_at,updated_at) VALUES('project-dispatch-locator','project','canonical_path',?,?,'now','now')`, dispatchDir, dispatchDir); err != nil {
		seedTx.Rollback()
		t.Fatal(err)
	}
	if _, err := seedTx.ExecContext(ctx, `INSERT INTO projects(id,display_name,version,created_at,updated_at) VALUES('project-home','Home',1,'now','now');
		INSERT INTO product_projects(product_id,project_id,role) VALUES('product','project-home','secondary')`); err != nil {
		seedTx.Rollback()
		t.Fatal(err)
	}
	if _, err := seedTx.ExecContext(ctx, `INSERT INTO project_locators(locator_id,project_id,kind,locator_value,normalized_value,created_at,updated_at) VALUES('project-home-locator','project-home','canonical_path',?,?,'now','now')`, homeDir, homeDir); err != nil {
		seedTx.Rollback()
		t.Fatal(err)
	}
	if _, err := seedTx.ExecContext(ctx, `INSERT INTO product_knowledge_homes(product_id,project_id,locator_id) VALUES('product','project-home','project-home-locator')`); err != nil {
		seedTx.Rollback()
		t.Fatal(err)
	}
	if _, err := seedTx.ExecContext(ctx, `INSERT INTO domain_registries(product_id,home_project_id,home_locator_id,product_key,root_domain_id,schema_version,content_hash,scanned_commit_oid) VALUES('product','project-home','project-home-locator','product','root','1.0',?,'test')`, hash); err != nil {
		seedTx.Rollback()
		t.Fatal(err)
	}
	if _, err := seedTx.ExecContext(ctx, `INSERT INTO domains(home_project_id,home_locator_id,product_id,domain_id,name,purpose,status,registry_content_hash,scanned_commit_oid) VALUES('project-home','project-home-locator','product','root','Root','Product law','current',?,'test')`, hash); err != nil {
		seedTx.Rollback()
		t.Fatal(err)
	}
	if _, err := seedTx.ExecContext(ctx, `INSERT INTO domains(home_project_id,home_locator_id,product_id,domain_id,name,purpose,parent_domain_id,status,registry_content_hash,scanned_commit_oid) VALUES('project-home','project-home-locator','product','child','Child','Child law','root','current',?,'test')`, hash); err != nil {
		seedTx.Rollback()
		t.Fatal(err)
	}
	if _, err := seedTx.ExecContext(ctx, `INSERT INTO law_subjects(home_project_id,home_locator_id,law_id,kind,status,path,title,content_hash,scanned_commit_oid) VALUES('project-home','project-home-locator','spec:one','spec','accepted','.concord/docs/spec.md','Synthetic home law','sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','test')`); err != nil {
		seedTx.Rollback()
		t.Fatal(err)
	}
	if _, err := seedTx.ExecContext(ctx, `INSERT INTO law_domain_homes(home_project_id,home_locator_id,law_id,product_id,domain_id,law_content_hash,scanned_commit_oid) VALUES('project-home','project-home-locator','spec:one','product','root','sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','test')`); err != nil {
		seedTx.Rollback()
		t.Fatal(err)
	}
	if err := leaveFold(ctx, seedTx); err != nil {
		seedTx.Rollback()
		t.Fatal(err)
	}
	if err := seedTx.Commit(); err != nil {
		t.Fatal(err)
	}
	// The approval helper derives its actor ref from the law-context actor
	// identity, so initialization records exactly that actor.
	actor := WorkflowActor{PrincipalRef: "principal:law-context", ClientRef: "client:law-context", AgentRef: "agent:law-context", SessionRef: "session:law-context", ActorClass: ActorAgent}
	definition, ok := BuiltinWorkflowRegistry().Lookup("workflow.implementation", 1)
	if !ok {
		t.Fatal("latest implementation definition missing")
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
	binding := WorkflowArchitectureBinding{DomainRegistryContentHash: "sha256:" + strings.Repeat("b", 64), HomeDomainID: "root", AffectedDomainIDs: []string{"root"}, DomainModifies: []string{}, DomainRelationModifies: []WorkflowDomainRelationModification{}, LawAdditions: []WorkflowLawAddition{}, VerificationObligations: []WorkflowVerificationObligation{}}
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
	wantLawPath := filepath.Join(homeRepo, ".concord/docs/spec.md")
	if snapshot.LawContext.Laws[0].Path != wantLawPath {
		t.Fatalf("law path = %q, want the knowledge home's %q", snapshot.LawContext.Laws[0].Path, wantLawPath)
	}
	if _, err := os.ReadFile(snapshot.LawContext.Laws[0].Path); err != nil {
		t.Fatalf("the dispatched lane could not open the law file the packet named: %v", err)
	}
}

// A knowledge home still on the pre-migration shard layout (CD-0194 D5)
// resolves its registry locator under docs/knowledge, never under the
// canonical .concord layout the home does not carry.
func TestContinuityResolvesRegistryUnderPreMigrationLayout(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openTemp(t)
	workID := "law-context-pre-migration"
	seedLawContextFixture(t, s, workID)
	homeRepo := workflowLawFixtureRepo(t, s)
	if err := os.RemoveAll(filepath.Join(homeRepo, ".concord", "docs", "knowledge")); err != nil {
		t.Fatal(err)
	}
	preMigrationDir := filepath.Join(homeRepo, "docs", "knowledge")
	if err := os.MkdirAll(preMigrationDir, 0o700); err != nil {
		t.Fatal(err)
	}
	registry := `{"schema_version":"1.0","product_key":"product","root_domain_id":"root","domains":[]}`
	if err := os.WriteFile(filepath.Join(preMigrationDir, "domain-registry.json"), []byte(registry+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(preMigrationDir, "manifest.json"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	binding := WorkflowArchitectureBinding{DomainRegistryContentHash: "sha256:" + strings.Repeat("b", 64), HomeDomainID: "root", AffectedDomainIDs: []string{"root"}, DomainModifies: []string{}, DomainRelationModifies: []WorkflowDomainRelationModification{}, LawAdditions: []WorkflowLawAddition{}, VerificationObligations: []WorkflowVerificationObligation{}}
	approveLawContextContract(t, s, workID, []string{}, []string{}, binding)
	snapshot, err := ReadWorkflowContinuity(ctx, s, ContinuityRequest{Work: workID})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.LawContext == nil {
		t.Fatal("continuity resolved no law context for a bound contract")
	}
	wantRegistry := filepath.Join(homeRepo, "docs/knowledge/domain-registry.json")
	if snapshot.LawContext.RegistryPath != wantRegistry {
		t.Fatalf("law context registry path = %q, want the carried pre-migration layout %q", snapshot.LawContext.RegistryPath, wantRegistry)
	}
	if _, err := os.ReadFile(snapshot.LawContext.RegistryPath); err != nil {
		t.Fatalf("the dispatched lane could not open the registry the packet named: %v", err)
	}
}

// A knowledge home whose checkout carries no registry under any supported
// layout refuses the law context typed: the packet must never advertise a
// locator no lane can open, and must never silently drop the binding.
func TestContinuityRefusesUnresolvableRegistrySource(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openTemp(t)
	workID := "law-context-missing-registry"
	seedLawContextFixture(t, s, workID)
	homeRepo := workflowLawFixtureRepo(t, s)
	if err := os.Remove(filepath.Join(homeRepo, ".concord/docs/knowledge/domain-registry.json")); err != nil {
		t.Fatal(err)
	}
	binding := WorkflowArchitectureBinding{DomainRegistryContentHash: "sha256:" + strings.Repeat("b", 64), HomeDomainID: "root", AffectedDomainIDs: []string{"root"}, DomainModifies: []string{}, DomainRelationModifies: []WorkflowDomainRelationModification{}, LawAdditions: []WorkflowLawAddition{}, VerificationObligations: []WorkflowVerificationObligation{}}
	approveLawContextContract(t, s, workID, []string{}, []string{}, binding)
	_, err := ReadWorkflowContinuity(ctx, s, ContinuityRequest{Work: workID})
	var failure *Failure
	if !failureAs(err, &failure) || failure.Kind != KindDomainRegistryAbsent || failure.Op != "read_workflow_law_context" {
		t.Fatalf("missing registry diagnosis = %v, want typed domain_registry_absent from the law context", err)
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
	if err := os.Remove(filepath.Join(homeRepo, ".concord", "docs", "spec.md")); err != nil {
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
// advertises, and the lane has no Concord route to repair it.
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
	if !failureAs(err, &failure) || failure.Kind != KindProjectionNotFound || failure.Op != "read_workflow_law_context" || len(failure.CandidateIDs) != 1 || failure.CandidateIDs[0] != "spec:one" {
		t.Fatalf("removed knowledge home diagnosis = %v, want typed projection_not_found naming spec:one", err)
	}
}

// A registry locator occupied by a directory is not a registry the lane can
// read: the context refuses typed instead of advertising the directory.
func TestContinuityRefusesRegistryReplacedByDirectory(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openTemp(t)
	workID := "law-context-registry-directory"
	seedLawContextFixture(t, s, workID)
	homeRepo := workflowLawFixtureRepo(t, s)
	registryPath := filepath.Join(homeRepo, ".concord", "docs", "knowledge", "domain-registry.json")
	if err := os.Remove(registryPath); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(registryPath, 0o700); err != nil {
		t.Fatal(err)
	}
	binding := WorkflowArchitectureBinding{DomainRegistryContentHash: "sha256:" + strings.Repeat("b", 64), HomeDomainID: "root", AffectedDomainIDs: []string{"root"}, DomainModifies: []string{}, DomainRelationModifies: []WorkflowDomainRelationModification{}, LawAdditions: []WorkflowLawAddition{}, VerificationObligations: []WorkflowVerificationObligation{}}
	approveLawContextContract(t, s, workID, []string{}, []string{}, binding)
	_, err := ReadWorkflowContinuity(ctx, s, ContinuityRequest{Work: workID})
	var failure *Failure
	if !failureAs(err, &failure) || failure.Kind != KindDomainRegistryAbsent || failure.Op != "read_workflow_law_context" {
		t.Fatalf("directory registry diagnosis = %v, want typed domain_registry_absent from the law context", err)
	}
}

// A bound law recorded under the supported pre-migration layout qualifies
// against the same knowledge home: the recorded path is home-qualified and
// opens, whatever layout tier the home carries (CD-0194 D5).
func TestContinuityQualifiesPreMigrationBoundLawDocument(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	workID := "law-context-pre-migration-law"
	seedLawContextFixture(t, s, workID)
	homeRepo := workflowLawFixtureRepo(t, s)
	if err := os.MkdirAll(filepath.Join(homeRepo, "docs"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(homeRepo, "docs", "spec-pre.md"), []byte("# Pre-migration law\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1); INSERT INTO law_subjects(home_project_id,home_locator_id,law_id,kind,status,path,title,content_hash,scanned_commit_oid) VALUES('project','workflow-law-locator','spec:pre','spec','accepted','docs/spec-pre.md','Pre-migration law','sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd','test'); INSERT INTO law_domain_homes(home_project_id,home_locator_id,law_id,product_id,domain_id,law_content_hash,scanned_commit_oid) VALUES('project','workflow-law-locator','spec:pre','product','root','sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd','test'); DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	lawContext, err := readLawOnlyContext(t, s, workID, []string{"spec:pre"})
	if err != nil {
		t.Fatal(err)
	}
	if lawContext == nil || len(lawContext.Laws) != 1 {
		t.Fatalf("law context = %+v, want one mandated pre-migration spec", lawContext)
	}
	wantPath := filepath.Join(homeRepo, "docs", "spec-pre.md")
	if lawContext.Laws[0].Path != wantPath {
		t.Fatalf("law path = %q, want the knowledge home's %q", lawContext.Laws[0].Path, wantPath)
	}
	if _, err := os.ReadFile(lawContext.Laws[0].Path); err != nil {
		t.Fatalf("the dispatched lane could not open the law file the packet named: %v", err)
	}
}

func TestLawContextLocatorsRequireReadableSources(t *testing.T) {
	t.Parallel()
	for _, source := range []string{"law", "registry"} {
		t.Run(source, func(t *testing.T) {
			s := openTemp(t)
			seedLawContextFixture(t, s, "law-context-readable-source")
			home := workflowLawFixtureRepo(t, s)
			relative := ".concord/docs/spec.md"
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

// The newest layout whose manifest head the home carries selects the tier
// (CD-0194 D5), the same rule the committed-shard reader applies. A leftover
// registry under an older tier never stands in for the selected tier's
// missing registry.
func TestContinuityRegistryDoesNotFallBackAcrossTheSelectedLayout(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openTemp(t)
	workID := "law-context-selected-layout"
	seedLawContextFixture(t, s, workID)
	homeRepo := workflowLawFixtureRepo(t, s)
	if err := os.Remove(filepath.Join(homeRepo, filepath.FromSlash(knowledgeRegistryPath))); err != nil {
		t.Fatal(err)
	}
	legacyDir := filepath.Join(homeRepo, "docs", "knowledge")
	if err := os.MkdirAll(legacyDir, 0o700); err != nil {
		t.Fatal(err)
	}
	registry := `{"schema_version":"1.0","product_key":"product","root_domain_id":"root","domains":[]}`
	if err := os.WriteFile(filepath.Join(legacyDir, "domain-registry.json"), []byte(registry+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	binding := WorkflowArchitectureBinding{DomainRegistryContentHash: "sha256:" + strings.Repeat("b", 64), HomeDomainID: "root", AffectedDomainIDs: []string{"root"}, DomainModifies: []string{}, DomainRelationModifies: []WorkflowDomainRelationModification{}, LawAdditions: []WorkflowLawAddition{}, VerificationObligations: []WorkflowVerificationObligation{}}
	approveLawContextContract(t, s, workID, []string{}, []string{}, binding)
	snapshot, err := ReadWorkflowContinuity(ctx, s, ContinuityRequest{Work: workID})
	var failure *Failure
	if !failureAs(err, &failure) || failure.Kind != KindDomainRegistryAbsent || failure.Op != "read_workflow_law_context" {
		t.Fatalf("selected-layout registry diagnosis = %v (context %+v), want typed domain_registry_absent", err, snapshot.LawContext)
	}
}

// A knowledge home carrying no manifest head under any supported layout has
// no selected tier, so a stray registry file does not resolve it.
func TestContinuityRefusesHomeWithoutManifestHead(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openTemp(t)
	workID := "law-context-no-head"
	seedLawContextFixture(t, s, workID)
	homeRepo := workflowLawFixtureRepo(t, s)
	if err := os.Remove(filepath.Join(homeRepo, filepath.FromSlash(knowledgeHeadPath))); err != nil {
		t.Fatal(err)
	}
	binding := WorkflowArchitectureBinding{DomainRegistryContentHash: "sha256:" + strings.Repeat("b", 64), HomeDomainID: "root", AffectedDomainIDs: []string{"root"}, DomainModifies: []string{}, DomainRelationModifies: []WorkflowDomainRelationModification{}, LawAdditions: []WorkflowLawAddition{}, VerificationObligations: []WorkflowVerificationObligation{}}
	approveLawContextContract(t, s, workID, []string{}, []string{}, binding)
	_, err := ReadWorkflowContinuity(ctx, s, ContinuityRequest{Work: workID})
	var failure *Failure
	if !failureAs(err, &failure) || failure.Kind != KindDomainRegistryAbsent || failure.Op != "read_workflow_law_context" {
		t.Fatalf("headless home diagnosis = %v, want typed domain_registry_absent", err)
	}
}

// A knowledge home that predates the shard homes carries the aggregate
// manifest itself, and its domain_registry member is the registry the lane
// reads, so the packet names the aggregate file instead of refusing a shape
// the Product still carries.
func TestContinuityNamesAggregateManifestAsRegistryLocator(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openTemp(t)
	workID := "law-context-aggregate-home"
	seedLawContextFixture(t, s, workID)
	homeRepo := workflowLawFixtureRepo(t, s)
	if err := os.RemoveAll(filepath.Join(homeRepo, ".concord", "docs", "knowledge")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(homeRepo, "docs"), 0o700); err != nil {
		t.Fatal(err)
	}
	aggregate := `{"schema_version":"1.0","product_key":"product","knowledge_roots":["docs/"],"domain_registry":{"schema_version":"1.0","product_key":"product","root_domain_id":"root","domains":[]},"records":[]}`
	if err := os.WriteFile(filepath.Join(homeRepo, filepath.FromSlash(knowledgeManifestPath)), []byte(aggregate+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	binding := WorkflowArchitectureBinding{DomainRegistryContentHash: "sha256:" + strings.Repeat("b", 64), HomeDomainID: "root", AffectedDomainIDs: []string{"root"}, DomainModifies: []string{}, DomainRelationModifies: []WorkflowDomainRelationModification{}, LawAdditions: []WorkflowLawAddition{}, VerificationObligations: []WorkflowVerificationObligation{}}
	approveLawContextContract(t, s, workID, []string{}, []string{}, binding)
	snapshot, err := ReadWorkflowContinuity(ctx, s, ContinuityRequest{Work: workID})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.LawContext == nil {
		t.Fatal("continuity resolved no law context for a bound contract")
	}
	wantRegistry := filepath.Join(homeRepo, filepath.FromSlash(knowledgeManifestPath))
	if snapshot.LawContext.RegistryPath != wantRegistry {
		t.Fatalf("law context registry path = %q, want the aggregate manifest %q", snapshot.LawContext.RegistryPath, wantRegistry)
	}
	if _, err := os.ReadFile(snapshot.LawContext.RegistryPath); err != nil {
		t.Fatalf("the dispatched lane could not open the registry the packet named: %v", err)
	}
}

// The aggregate fallback names the aggregate manifest only when it is a
// regular readable file: a directory or a broken entry at the aggregate path
// is not a registry the packet may advertise.
func TestContinuityRefusesAggregateManifestReplacedByDirectory(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openTemp(t)
	workID := "law-context-aggregate-directory"
	seedLawContextFixture(t, s, workID)
	homeRepo := workflowLawFixtureRepo(t, s)
	if err := os.RemoveAll(filepath.Join(homeRepo, ".concord", "docs", "knowledge")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(homeRepo, filepath.Dir(filepath.FromSlash(knowledgeManifestPath))), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(homeRepo, filepath.FromSlash(knowledgeManifestPath)), 0o700); err != nil {
		t.Fatal(err)
	}
	binding := WorkflowArchitectureBinding{DomainRegistryContentHash: "sha256:" + strings.Repeat("b", 64), HomeDomainID: "root", AffectedDomainIDs: []string{"root"}, DomainModifies: []string{}, DomainRelationModifies: []WorkflowDomainRelationModification{}, LawAdditions: []WorkflowLawAddition{}, VerificationObligations: []WorkflowVerificationObligation{}}
	approveLawContextContract(t, s, workID, []string{}, []string{}, binding)
	_, err := ReadWorkflowContinuity(ctx, s, ContinuityRequest{Work: workID})
	var failure *Failure
	if !failureAs(err, &failure) || failure.Kind != KindDomainRegistryAbsent || failure.Op != "read_workflow_law_context" {
		t.Fatalf("aggregate directory diagnosis = %v, want typed domain_registry_absent from the law context", err)
	}
}

// The registry locator names the file the Domain projection was read from:
// the Domain registry's own recorded home. A Product whose knowledge home
// designation is gone must not fall back to the dispatch Project's checkout,
// even when that checkout carries a readable registry of its own.
func TestContinuityRegistryLocatorFollowsTheDomainRegistryHome(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openTemp(t)
	workID := "law-context-registry-home"
	homeRepo := seedCrossRepositoryLawFixture(t, s, workID)
	binding := WorkflowArchitectureBinding{DomainRegistryContentHash: "sha256:" + strings.Repeat("b", 64), HomeDomainID: "root", AffectedDomainIDs: []string{"root"}, DomainModifies: []string{}, DomainRelationModifies: []WorkflowDomainRelationModification{}, LawAdditions: []WorkflowLawAddition{}, VerificationObligations: []WorkflowVerificationObligation{}}
	approveLawContextContract(t, s, workID, []string{}, []string{}, binding)
	var dispatchDir string
	if err := s.DatabaseForTesting().QueryRow(`SELECT normalized_value FROM project_locators WHERE locator_id='project-dispatch-locator'`).Scan(&dispatchDir); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dispatchDir, ".concord", "docs", "knowledge"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, relative := range []string{knowledgeHeadPath, knowledgeRegistryPath} {
		if err := os.WriteFile(filepath.Join(dispatchDir, filepath.FromSlash(relative)), []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1); DELETE FROM product_knowledge_homes WHERE product_id='product'; DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	snapshot, err := ReadWorkflowContinuity(ctx, s, ContinuityRequest{Work: workID})
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(homeRepo, filepath.FromSlash(knowledgeRegistryPath))
	if snapshot.LawContext == nil || snapshot.LawContext.RegistryPath != want {
		t.Fatalf("law context = %+v, want registry locator %q from the Domain registry's home", snapshot.LawContext, want)
	}
}

// A Domain-binding context whose Product has no Domain registry projection
// refuses typed instead of naming any repository's registry file.
func TestContinuityRefusesDomainBindingWithoutRegistryProjection(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openTemp(t)
	workID := "law-context-no-registry-row"
	seedLawContextFixture(t, s, workID)
	binding := WorkflowArchitectureBinding{DomainRegistryContentHash: "sha256:" + strings.Repeat("b", 64), HomeDomainID: "root", AffectedDomainIDs: []string{"root"}, DomainModifies: []string{}, DomainRelationModifies: []WorkflowDomainRelationModification{}, LawAdditions: []WorkflowLawAddition{}, VerificationObligations: []WorkflowVerificationObligation{}}
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := enterFold(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `PRAGMA defer_foreign_keys=ON; DELETE FROM domain_registries WHERE product_id='product'`); err != nil {
		t.Fatal(err)
	}
	_, err = readWorkflowLawContext(ctx, tx, workID, &WorkflowReadContract{Version: 1, ArchitectureBinding: &binding})
	var failure *Failure
	if !failureAs(err, &failure) || failure.Kind != KindDomainRegistryAbsent || failure.Op != "read_workflow_law_context" {
		t.Fatalf("missing registry projection diagnosis = %v, want typed domain_registry_absent", err)
	}
}

// A projected law path that escapes the knowledge home checkout is not a
// home locator: the open is confined to the home, so the context refuses
// rather than advertising a file outside it.
func TestLawContextRefusesLawPathEscapingTheHome(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	workID := "law-context-escape"
	seedLawContextFixture(t, s, workID)
	homeRepo := workflowLawFixtureRepo(t, s)
	if err := os.WriteFile(filepath.Join(filepath.Dir(homeRepo), "escape.md"), []byte("# Outside\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1); INSERT INTO law_subjects(home_project_id,home_locator_id,law_id,kind,status,path,title,content_hash,scanned_commit_oid) VALUES('project','workflow-law-locator','spec:escape','spec','accepted','../escape.md','Escaping law','sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee','test'); INSERT INTO law_domain_homes(home_project_id,home_locator_id,law_id,product_id,domain_id,law_content_hash,scanned_commit_oid) VALUES('project','workflow-law-locator','spec:escape','product','root','sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee','test'); DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	lawContext, err := readLawOnlyContext(t, s, workID, []string{"spec:escape"})
	var failure *Failure
	if lawContext != nil || !failureAs(err, &failure) || failure.Kind != KindUnavailable {
		t.Fatalf("escaping law path = %+v, %v; want typed unavailable", lawContext, err)
	}
}

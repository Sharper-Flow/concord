package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// Work-context declarations and retained finding facts are claims, not Product
// law or criterion evidence. Readers preserve their attribution and selection
// without promoting their content into Git or knowledge.

// Domain identities of the seeded registry fixture. The contract approves
// root and child-alpha; child-unaffected is current but outside the approved
// affected scope; deprecated-domain is not current. A registry-hash mismatch
// is structurally impossible beside the one current registry (the domains
// foreign key pins every row to a registry hash), so the hash check stays
// defense-in-depth in the validator and the fixture covers the reachable
// staleness: a deprecated Domain.
const (
	workContextTestRoot          = "root"
	workContextTestChild         = "child-alpha"
	workContextTestUnaffected    = "child-unaffected"
	workContextTestDeprecated    = "deprecated-domain"
	workContextTestForeignDomain = "foreign-child"
)

func workContextTestCommit() string { return strings.Repeat("a1", 20) }

// seedWorkContextDomainRegistry installs the actual registry fixture: one
// current registry for the product, the domains above in their named
// states, and a second product with one current foreign domain reached
// through its own registered Project locator.
func seedWorkContextDomainRegistry(t *testing.T, s *Store) {
	t.Helper()
	ctx := context.Background()
	current := "sha256:" + strings.Repeat("b", 64)
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := enterWrite(ctx, tx); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO domain_registries(product_id,home_project_id,home_locator_id,product_key,root_domain_id,schema_version,content_hash,scanned_commit_oid) VALUES('product','project','workflow-law-locator','product',?,'1.0',?,'test')`, workContextTestRoot, current); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	domains := []struct{ id, parent, status string }{
		{workContextTestRoot, "", "current"},
		{workContextTestChild, workContextTestRoot, "current"},
		{workContextTestUnaffected, workContextTestRoot, "current"},
		{workContextTestDeprecated, workContextTestRoot, "deprecated"},
	}
	for _, domain := range domains {
		if _, err := tx.ExecContext(ctx, `INSERT INTO domains(home_project_id,home_locator_id,product_id,domain_id,name,purpose,parent_domain_id,status,registry_content_hash,scanned_commit_oid) VALUES('project','workflow-law-locator','product',?,?,?,NULLIF(?,''),?,?,'test')`, domain.id, domain.id, "seeded domain "+domain.id, domain.parent, domain.status, current); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
	}
	if err := leaveWrite(ctx, tx); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	// The second Product, its Project, its own registered Project locator,
	// and one current foreign domain, for the cross-Product refusal.
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{
		productCreatedEvent("product-2", "create-product-2"),
		projectCreatedEvent("project-2", "create-project-2"),
		operationEvent("product-2-project", "product_project.added", SubjectProduct, "product-2", map[string]any{
			"product_id": "product-2", "project_id": "project-2", "role": "primary", "reason": "test",
			"expected_version": 1, "resulting_version": 2,
		}),
	}, ExpectedVersions: map[SubjectRef]int64{
		VersionRef(SubjectProduct, "product-2"): 0,
		VersionRef(SubjectProject, "project-2"): 0,
	}}); err != nil {
		t.Fatal(err)
	}
	var projectVersion int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM projects WHERE id='project'`).Scan(&projectVersion); err != nil {
		t.Fatal(err)
	}
	foreignHome := t.TempDir()
	foreignNormalized, err := NormalizeProjectLocator(LocatorCanonicalPath, foreignHome)
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{operationEvent("work-context-foreign-locator", "project.locator_added", SubjectProject, "project", map[string]any{
		"project_id": "project", "locator_id": "work-context-foreign-locator", "kind": string(LocatorCanonicalPath), "value": foreignHome, "normalized_value": foreignNormalized,
		"expected_version": projectVersion, "resulting_version": projectVersion + 1,
	})}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProject, "project"): projectVersion}}); err != nil {
		t.Fatal(err)
	}
	tx, err = s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := enterWrite(ctx, tx); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	foreign := "sha256:" + strings.Repeat("d", 64)
	if _, err := tx.ExecContext(ctx, `INSERT INTO domain_registries(product_id,home_project_id,home_locator_id,product_key,root_domain_id,schema_version,content_hash,scanned_commit_oid) VALUES('product-2','project','work-context-foreign-locator','product-2',?,'1.0',?,'test')`, workContextTestForeignDomain, foreign); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO domains(home_project_id,home_locator_id,product_id,domain_id,name,purpose,parent_domain_id,status,registry_content_hash,scanned_commit_oid) VALUES('project','work-context-foreign-locator','product-2',?,?,?,NULL,'current',?,'test')`, workContextTestForeignDomain, workContextTestForeignDomain, "foreign domain", foreign); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := leaveWrite(ctx, tx); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

type workContextFixture struct {
	store    *Store
	workID   string
	owner    WorkflowActor
	operator WorkflowActor
	sources  workContextReadingSources
}

// workContextReadingSources holds the real prerequisites the fixture's
// declared readings resolve against at dispatch: two authorized Projects of
// the work's Product, each with one canonical-path repository locator, and
// the designated knowledge home's committed law record with its exact
// canonical locator.
type workContextReadingSources struct {
	repoProject  string
	repoPath     string
	repoCommit   string
	otherProject string
	otherPath    string
	otherCommit  string
	// lawProject/lawLocator/lawRepo/lawCommit identify the registered
	// knowledge source whose committed manifest and law blob the fixture's
	// knowledge reading pins.
	lawProject string
	lawLocator string
	lawID      string
	lawHash    string
	lawRepo    string
	lawCommit  string
}

// seedWorkContextReadingSources gives the fixture's declared readings real
// prerequisites: one synthetic Git repository per authorized Project, and
// the knowledge home's committed manifest, law blob, projection locator, and
// watermark for the exact record the fixture's knowledge reading pins.
func seedWorkContextReadingSources(t *testing.T, s *Store, workID string) workContextReadingSources {
	t.Helper()
	ctx := context.Background()
	out := workContextReadingSources{
		repoProject: "project-repo", otherProject: "project-repo-2",
		repoPath: "internal/store/work_context.go", otherPath: "pkg/other/source.go",
		lawProject: "project", lawID: "spec:one", lawLocator: "workflow-law-locator",
	}
	// The workflow law home keeps its own committed copy of the seeded law
	// document, so the workflow law context opens a real file.
	const homeProject, homeLocator = "project", "workflow-law-locator"
	var lawRepo string
	if err := s.DatabaseForTesting().QueryRow(`SELECT locator_value FROM project_locators WHERE project_id=? AND locator_id=?`, homeProject, homeLocator).Scan(&lawRepo); err != nil {
		t.Fatal(err)
	}
	runKnowledgeGit(t, lawRepo, "init", "--initial-branch=main")
	runKnowledgeGit(t, lawRepo, "config", "user.email", "test@example.invalid")
	runKnowledgeGit(t, lawRepo, "config", "user.name", "Concord Test")
	manifestRoot := "product-root:product"
	registry := KnowledgeDomainRegistry{
		SchemaVersion: "1.0", ProductKey: "product", RootDomainID: manifestRoot,
		Domains: []KnowledgeDomain{
			{DomainID: manifestRoot, Name: "Root", Purpose: "Product law", Status: "current", ArchitectureRelations: []KnowledgeArchitectureRelation{}},
			{DomainID: workContextTestChild, Name: "Child", Purpose: "Child law", ParentDomainID: manifestRoot, Status: "current", ArchitectureRelations: []KnowledgeArchitectureRelation{}},
		},
	}
	registryJSON, err := json.Marshal(registry)
	if err != nil {
		t.Fatal(err)
	}
	body := "# Synthetic dispatch law\n\nThe fixture's committed law record for declared reading resolution.\n"
	sum := sha256.Sum256([]byte(body))
	out.lawHash = "sha256:" + hex.EncodeToString(sum[:])
	writeKnowledgeFile(t, lawRepo, ".concord/docs/spec.md", body)
	head := map[string]any{
		"schema_version":  "1.2",
		"supported_kinds": []string{"decision", "spec"},
		"indexed_kinds":   []string{"decision", "spec"},
	}
	headJSON, err := json.Marshal(head)
	if err != nil {
		t.Fatal(err)
	}
	writeKnowledgeFile(t, lawRepo, knowledgeHeadPath, string(headJSON)+"\n")
	writeKnowledgeFile(t, lawRepo, knowledgeRegistryPath, string(registryJSON)+"\n")
	record := KnowledgeRecord{
		ID: out.lawID, Kind: "spec", Path: ".concord/docs/spec.md", Status: "accepted",
		Date: "2026-10-01T00:00:00Z", Title: "Synthetic dispatch law",
		Summary: "the fixture committed law record for declared reading resolution",
		Tags:    []string{},
		Scopes:  KnowledgeRecordScopes{Mode: "home", ProductIDs: []string{}, ProjectIDs: []string{}, DomainIDs: []string{}, TagIDs: []string{}},
		SHA256:  out.lawHash, HomeDomainID: workContextTestChild,
	}
	recordJSON, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	writeKnowledgeFile(t, lawRepo, knowledgeRecordTree+"/spec-one.json", string(recordJSON)+"\n")
	out.lawCommit = commitKnowledgeRepo(t, lawRepo, "synthetic law home with manifest record")
	out.lawRepo = lawRepo
	home := KnowledgeHome{HomeProjectID: homeProject, HomeLocatorID: homeLocator, RepoPath: lawRepo, HeadRef: "HEAD"}
	digest, err := knowledgeContentDigest(ctx, home, out.lawCommit)
	if err != nil {
		t.Fatal(err)
	}
	digest, err = domainShapeKnowledgeDigest(ctx, home, out.lawCommit, digest, nil)
	if err != nil {
		t.Fatal(err)
	}
	// The git-derived projection rows pin the exact committed identity the
	// reading resolution discovers in the snapshot. The knowledge tables are
	// write-seam-only, so the writes run under the write guard and release it.
	stmts := []struct {
		tag  string
		sql  string
		args []any
	}{
		{"guard", `INSERT INTO write_guard(active) VALUES(1) ON CONFLICT(active) DO NOTHING`, nil},
		{"law update", `UPDATE law_subjects SET content_hash=?, scanned_commit_oid=? WHERE home_project_id=? AND home_locator_id=? AND law_id=?`, []any{out.lawHash, out.lawCommit, homeProject, homeLocator, out.lawID}},
		{"archived", `INSERT OR REPLACE INTO archived_work(id,type,title,completed_at,outcome_tag,lesson_tags,terminal_state,priority,summary,home_project_id,home_locator_id,note_path,commit_oid,content_hash,scope_mode,manifest_schema_version)
			VALUES(?, 'spec', 'Synthetic dispatch law', '2026-10-01T00:00:00Z', 'accepted', '[]', 'completed', 2, 'the fixture committed law record for declared reading resolution', ?, ?, ?, ?, ?, 'home', '1.2')`, []any{out.lawID, homeProject, homeLocator, ".concord/docs/spec.md", out.lawCommit, out.lawHash}},
		{"watermark", `INSERT OR REPLACE INTO knowledge_index_watermark(home_project_id,home_locator_id,head_ref,scanned_commit_oid,scanned_content_digest,scanned_at,complete,projection_version)
			VALUES(?,?,'HEAD',?,?,'test',1,?)`, []any{homeProject, homeLocator, out.lawCommit, digest, knowledgeProjectionVersion}},
		{"law domain home", `INSERT OR REPLACE INTO law_domain_homes(home_project_id,home_locator_id,law_id,product_id,domain_id,law_content_hash,scanned_commit_oid,product_wide_rationale) VALUES(?,?,?,?,?,?, 'test', '')`,
			[]any{homeProject, homeLocator, out.lawID, "product", workContextTestChild, out.lawHash}},
		{"guard release", `DELETE FROM write_guard`, nil},
	}
	for _, stmt := range stmts {
		if _, err := s.DatabaseForTesting().Exec(stmt.sql, stmt.args...); err != nil {
			_, _ = s.DatabaseForTesting().Exec(`DELETE FROM write_guard`)
			t.Fatalf("law home projection statement %q failed: %v", stmt.tag, err)
		}
	}
	var productVersion int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM products WHERE id='product'`).Scan(&productVersion); err != nil {
		t.Fatal(err)
	}
	// Two member Projects of the work's Product, each with one canonical
	// repository locator over a real synthetic repository.
	for _, member := range []struct {
		project, locator, path, file string
		commit                       *string
	}{
		{out.repoProject, "repo-locator-" + workID, out.repoPath, "package synthetic\n\nconst declared = true\n", &out.repoCommit},
		{out.otherProject, "other-locator-" + workID, out.otherPath, "package other\n\nconst other = true\n", &out.otherCommit},
	} {
		memberEvents := []Event{projectCreatedEvent(member.project, "create-"+member.project)}
		expected := map[SubjectRef]int64{VersionRef(SubjectProject, member.project): 0}
		var exists int
		if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM projects WHERE id=?`, member.project).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if exists == 0 {
			memberEvents = append(memberEvents, operationEvent("add-"+member.project+"-to-product", "product_project.added", SubjectProduct, "product", map[string]any{
				"product_id": "product", "project_id": member.project, "role": "secondary", "reason": "test",
				"expected_version": productVersion, "resulting_version": productVersion + 1,
			}))
			expected[VersionRef(SubjectProduct, "product")] = productVersion
			productVersion++
		}
		if err := ApplyOperation(ctx, s, Operation{Events: memberEvents, ExpectedVersions: expected}); err != nil {
			t.Fatal(err)
		}
		repo := initKnowledgeRepo(t)
		writeKnowledgeFile(t, repo, member.path, member.file)
		*member.commit = commitKnowledgeRepo(t, repo, "synthetic reading repository")
		var projectVersion int64
		if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM projects WHERE id=?`, member.project).Scan(&projectVersion); err != nil {
			t.Fatal(err)
		}
		if err := s.AddProjectLocator(ctx, member.project, ProjectLocator{ID: member.locator, Kind: LocatorCanonicalPath, Value: repo}, projectVersion); err != nil {
			t.Fatal(err)
		}
	}
	return out
}

// repoReading builds one resolvable repository_file reading against the
// fixture's synthetic repositories. which selects the primary or the second
// authorized Project.
func (f workContextFixture) repoReading(domain, rationale, which string) map[string]any {
	project, path, commit := f.sources.repoProject, f.sources.repoPath, f.sources.repoCommit
	if which == "other" {
		project, path, commit = f.sources.otherProject, f.sources.otherPath, f.sources.otherCommit
	}
	reading := map[string]any{"domain_id": domain, "reason": "the reading carries the bounded reason", "source": map[string]any{"kind": "repository_file", "project_id": project, "path": path, "commit_oid": commit}}
	if rationale != "" {
		reading["product_wide_rationale"] = rationale
	}
	return reading
}

// knowledgeReading builds one resolvable knowledge reading pinned to the
// fixture's committed law record: the designated home Project, the record
// ID, and the exact committed content hash.
func (f workContextFixture) knowledgeReading() map[string]any {
	return map[string]any{"domain_id": workContextTestChild, "reason": "the law reading carries the bounded reason", "source": map[string]any{"kind": "knowledge", "source_id": f.sources.lawProject, "law_id": f.sources.lawID, "content_hash": f.sources.lawHash}}
}

// seedWorkContextFixture seeds one break-fix work item whose approved
// contract affects root and child-alpha, advanced to the repair step, with
// the registry fixture above installed.
func seedWorkContextFixture(t *testing.T, workID string) workContextFixture {
	t.Helper()
	registered, err := BuiltinWorkflowDefinitionForRef("workflow.break_fix")
	if err != nil {
		t.Fatal(err)
	}
	return seedWorkContextFixtureWithDefinition(t, workID, registered)
}

// Initialization retains the registered identity supplied by the caller.
func seedWorkContextFixtureWithDefinition(t *testing.T, workID string, registered RegisteredDefinition) workContextFixture {
	t.Helper()
	ctx := context.Background()
	s := openTemp(t)
	seedWork(t, s, workID)
	seedWorkflowLaw(t, s)
	seedWorkContextDomainRegistry(t, s)
	sources := seedWorkContextReadingSources(t, s, workID)
	owner := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/owner", SessionRef: "session/" + workID, ActorClass: ActorAgent}
	operator := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/operator", SessionRef: "session/" + workID + "-operator", ActorClass: ActorOperator}
	ownerRef, err := WorkflowActorRef(owner)
	if err != nil {
		t.Fatal(err)
	}
	ctx, err = s.EstablishKnowledgeSourceSetProof(ctx, workID)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := initializeWorkflowRawTx(ctx, tx, WorkflowInitializationRequest{WorkID: workID, Definition: registered, Actor: owner, Now: time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC)}); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	version := int64(4)
	events := []Event{workflowEventWithActor("ctx-actor-"+workID, WorkflowActorRecorded, workID, ownerRef, map[string]any{
		"work_id": workID, "expected_version": version, "resulting_version": version + 1,
		"actor_ref": ownerRef, "principal_ref": owner.PrincipalRef, "client_ref": owner.ClientRef,
		"agent_ref": owner.AgentRef, "session_ref": owner.SessionRef, "actor_class": string(owner.ActorClass),
	})}
	version++
	events = append(events, workflowTypedEvent("ctx-contract-"+workID, WorkflowContractApproved, workID, ownerRef, time.Unix(5, 0).UTC(), version, map[string]any{
		"contract_version": 1, "premise": "deliver the checked change", "decision_domains": []string{},
		"outcome_predicates": []map[string]any{{"predicate_id": "predicate:work-context", "ordinal": 0, "outcome_kind": "check", "outcome_payload": map[string]any{"kind": "check", "check_ref": "check:work-context", "immutable_subject_ref": "commit:" + workContextTestCommit(), "expected_result": "pass"}}},
		"required_evidence":  []string{"verification"}, "route_conventions": []string{}, "spec_mandate": []string{}, "law_modifies": []string{},
		"law_revisions": []WorkflowLawRevision{}, "law_boundary_version": 1, "rigor_class": "prototype_internal", "consequence_class": "internal_sqlite",
		"architecture_binding": WorkflowArchitectureBinding{
			HomeDomainID: workContextTestRoot, AffectedDomainIDs: []string{workContextTestRoot, workContextTestChild},
			DomainModifies: []string{}, DomainRelationModifies: []WorkflowDomainRelationModification{}, LawAdditions: []WorkflowLawAddition{}, VerificationObligations: []WorkflowVerificationObligation{},
		},
	}))
	if err := applyWorkflowTestOperation(ctx, s, Operation{Events: events, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): 4}}); err != nil {
		t.Fatal(err)
	}
	if err := advanceWorkflowTestInstanceToStep(ctx, s, workID, "repair", ownerRef); err != nil {
		t.Fatal(err)
	}
	return workContextFixture{store: s, workID: workID, owner: owner, operator: operator, sources: sources}
}

func (f workContextFixture) recordWorkContext(t *testing.T, payload any) error {
	t.Helper()
	return runVerdictActionAs(t, f.store, f.workID, "record_work_context", mustJSONValue(payload), 0, f.owner)
}

// workContextRepoReading builds one repository_file reading; rationale is
// written only when non-empty.
func workContextRepoReading(domain, rationale string) map[string]any {
	reading := map[string]any{"domain_id": domain, "reason": "the reading carries the bounded reason", "source": map[string]any{"kind": "repository_file", "project_id": "project", "path": "internal/store/work_context.go", "commit_oid": workContextTestCommit()}}
	if rationale != "" {
		reading["product_wide_rationale"] = rationale
	}
	return reading
}

func workContextKnowledgeReading(domain string) map[string]any {
	return map[string]any{"domain_id": domain, "reason": "the law reading carries the bounded reason", "source": map[string]any{"kind": "knowledge", "source_id": "concord_knowledge", "law_id": "CD-0001", "content_hash": "sha256:" + strings.Repeat("e", 64)}}
}

// workContextFinding builds one action finding; rationale is written only
// when non-empty.
func workContextFinding(domain, kind, statement, rationale string) map[string]any {
	finding := map[string]any{"kind": kind, "statement": statement, "subject_ref": "internal/store/work_context.go", "evidence_refs": []string{"internal/store/work_context_test.go"}, "domain_id": domain}
	if rationale != "" {
		finding["product_wide_rationale"] = rationale
	}
	return finding
}

const workContextRootRationale = "the claim spans every Domain of the Product"

// The action records one typed declaration: the event retains the authored
// bytes, the work version advances, and the step holds.
func TestRecordWorkContextAppendsTypedDeclaration(t *testing.T) {
	t.Parallel()
	fixture := seedWorkContextFixture(t, "ctx-action-declaration")
	defer fixture.store.Close()
	versionBefore := readWorkVersion(t, fixture.store, fixture.workID)
	if err := fixture.recordWorkContext(t, map[string]any{
		"required_reading": []map[string]any{
			workContextRepoReading(workContextTestRoot, workContextRootRationale),
			workContextKnowledgeReading(workContextTestChild),
		},
		"finding_refs": []string{},
		"context_findings": []map[string]any{
			workContextFinding(workContextTestChild, "rejected_approach", "the unbounded log scan was rejected", ""),
			workContextFinding(workContextTestRoot, "open_question", "which Domain owns the reader boundary", workContextRootRationale),
		},
	}); err != nil {
		t.Fatalf("record_work_context refused: %v", err)
	}
	var stored string
	if err := fixture.store.DatabaseForTesting().QueryRow(`SELECT declaration_payload FROM work_context_state WHERE work_id=?`, fixture.workID).Scan(&stored); err != nil {
		t.Fatalf("declaration event missing: %v", err)
	}
	if !strings.Contains(stored, "the unbounded log scan was rejected") || !strings.Contains(stored, "the reading carries the bounded reason") {
		t.Fatalf("declaration payload lost authored content: %s", stored)
	}
	if got := readWorkVersion(t, fixture.store, fixture.workID); got <= versionBefore {
		t.Fatalf("work version after declaration = %d, want past %d", got, versionBefore)
	}
	if got := currentStep(t, fixture.store, fixture.workID); got != "repair" {
		t.Fatalf("step after declaration = %q, want repair (hold)", got)
	}
}

// The reusable domain validation the parent terminal fold shares: every
// refusal class names its own failure kind against the actual registry
// fixture.
func TestValidateWorkContextDomainTxRefusals(t *testing.T) {
	t.Parallel()
	fixture := seedWorkContextFixture(t, "ctx-domain-validation")
	defer fixture.store.Close()
	cases := []struct {
		name      string
		domain    string
		rationale string
		kind      FailureKind
		detail    string
	}{
		{"unknown domain", "never-seeded", "", KindUnknownScope, "unknown Domain"},
		{"cross-Product domain", workContextTestForeignDomain, "", KindUnknownScope, "another Product"},
		{"deprecated domain", workContextTestDeprecated, "", KindStaleRequiresReview, "non-current or stale Domain"},
		{"outside approved affected scope", workContextTestUnaffected, "", KindUnknownScope, "affected Domain scope"},
		{"root without product-wide rationale", workContextTestRoot, "", KindInvalidPayload, "product-wide rationale"},
		{"child with product-wide rationale", workContextTestChild, workContextRootRationale, KindInvalidPayload, "product-wide rationale"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			tx, err := fixture.store.DatabaseForTesting().BeginTx(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			err = ValidateWorkContextDomainTx(context.Background(), tx, fixture.workID, testCase.domain, testCase.rationale)
			if !hasFailureKind(err, testCase.kind) || !strings.Contains(fmt.Sprint(err), testCase.detail) {
				t.Fatalf("%s error = %v, want %s containing %q", testCase.name, err, testCase.kind, testCase.detail)
			}
		})
	}
	// The two admissible arms: an affected child without rationale and the
	// approved root with one.
	tx, err := fixture.store.DatabaseForTesting().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := ValidateWorkContextDomainTx(context.Background(), tx, fixture.workID, workContextTestChild, ""); err != nil {
		t.Fatalf("affected child refused: %v", err)
	}
	if err := ValidateWorkContextDomainTx(context.Background(), tx, fixture.workID, workContextTestRoot, workContextRootRationale); err != nil {
		t.Fatalf("approved root with rationale refused: %v", err)
	}
}

// A Product with no projected registry refuses before any domain question.
func TestValidateWorkContextDomainTxRefusesWithoutRegistry(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	seedWork(t, s, "ctx-no-registry")
	defer s.Close()
	tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	err = ValidateWorkContextDomainTx(context.Background(), tx, "ctx-no-registry", workContextTestRoot, workContextRootRationale)
	if !hasFailureKind(err, KindUnknownScope) || !strings.Contains(fmt.Sprint(err), "no current Domain registry") {
		t.Fatalf("registry-absent error = %v, want %s naming the missing registry", err, KindUnknownScope)
	}
}

// The action admission applies the same domain validation to every reading
// and finding: each refusal names the domain and the rule.
func TestRecordWorkContextActionDomainRefusals(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		domain    string
		rationale string
		arm       string
		detail    string
	}{
		{"unknown domain on a reading", "never-seeded", "", "reading", "unknown Domain"},
		{"cross-Product domain on a reading", workContextTestForeignDomain, "", "reading", "another Product"},
		{"deprecated domain on a finding", workContextTestDeprecated, "", "finding", "stale Domain"},
		{"outside affected scope on a reading", workContextTestUnaffected, "", "reading", "affected Domain scope"},
		{"root reading without rationale", workContextTestRoot, "", "reading", "product-wide rationale"},
		{"child finding with rationale", workContextTestChild, workContextRootRationale, "finding", "product-wide rationale"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := seedWorkContextFixture(t, "ctx-action-domain-"+strings.ReplaceAll(testCase.name, " ", "-"))
			defer fixture.store.Close()
			payload := map[string]any{"finding_refs": []string{}}
			if testCase.arm == "reading" {
				payload["required_reading"] = []map[string]any{workContextRepoReading(testCase.domain, testCase.rationale)}
			} else {
				payload["context_findings"] = []map[string]any{workContextFinding(testCase.domain, "observation", "statement", testCase.rationale)}
			}
			err := fixture.recordWorkContext(t, payload)
			if err == nil || !strings.Contains(err.Error(), testCase.detail) {
				t.Fatalf("%s error = %v, want refusal containing %q", testCase.name, err, testCase.detail)
			}
		})
	}
}

// The closed reading source: repository containment, digest shapes, and the
// closed union arms. Shapes the generated item schema sees refuse at the
// declared-schema boundary; the tmp-source rule and the containment rules
// the schema cannot express surface from the semantic constructor.
func TestRecordWorkContextShapeRefusals(t *testing.T) {
	t.Parallel()
	repoReading := func(path, commit string) map[string]any {
		return map[string]any{"domain_id": workContextTestChild, "reason": "bounded", "source": map[string]any{"kind": "repository_file", "project_id": "project", "path": path, "commit_oid": commit}}
	}
	knowledgeReading := func(hash string) map[string]any {
		return map[string]any{"domain_id": workContextTestChild, "reason": "bounded", "source": map[string]any{"kind": "knowledge", "source_id": "concord_knowledge", "law_id": "CD-0001", "content_hash": hash}}
	}
	schemaRefusal := "does not satisfy its declared schema"
	cases := []struct {
		name    string
		detail  string
		payload any
	}{
		{"absolute repository path", "normalized relative", map[string]any{"required_reading": []map[string]any{repoReading("/etc/passwd", workContextTestCommit())}}},
		{"parent traversal path", "normalized relative", map[string]any{"required_reading": []map[string]any{repoReading("internal/../adapter", workContextTestCommit())}}},
		{"tmp source path", "tmp source", map[string]any{"required_reading": []map[string]any{repoReading("tmp/opencode/con887/scratch.md", workContextTestCommit())}}},
		{"short commit oid", schemaRefusal, map[string]any{"required_reading": []map[string]any{repoReading("internal/store/work_context.go", "a1b2")}}},
		{"knowledge content hash shape", schemaRefusal, map[string]any{"required_reading": []map[string]any{knowledgeReading("not-a-digest")}}},
		{"unknown source kind", schemaRefusal, map[string]any{"required_reading": []map[string]any{map[string]any{"domain_id": workContextTestChild, "reason": "bounded", "source": map[string]any{"kind": "oracle"}}}}},
		{"repository arm carrying knowledge fields", schemaRefusal, map[string]any{"required_reading": []map[string]any{map[string]any{"domain_id": workContextTestChild, "reason": "bounded", "source": map[string]any{"kind": "repository_file", "project_id": "project", "path": "internal/store/work_context.go", "commit_oid": workContextTestCommit(), "law_id": "CD-0001"}}}}},
		{"thirty-three readings", "wrong registered type or bounds", map[string]any{"required_reading": func() []map[string]any {
			readings := make([]map[string]any, 33)
			for i := range readings {
				readings[i] = repoReading("internal/store/work_context.go", workContextTestCommit())
			}
			return readings
		}()}},
		{"seventeen findings", "wrong registered type or bounds", map[string]any{"context_findings": func() []map[string]any {
			findings := make([]map[string]any, 17)
			for i := range findings {
				findings[i] = workContextFinding(workContextTestChild, "observation", "bounded statement", "")
			}
			return findings
		}()}},
		{"malformed finding ref", "finding ref", map[string]any{"finding_refs": []string{"finding:not-ids"}}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := seedWorkContextFixture(t, "ctx-shape-"+strings.ReplaceAll(testCase.name, " ", "-"))
			defer fixture.store.Close()
			err := fixture.recordWorkContext(t, testCase.payload)
			if err == nil || !strings.Contains(err.Error(), testCase.detail) {
				t.Fatalf("%s error = %v, want refusal containing %q", testCase.name, err, testCase.detail)
			}
		})
	}
}

// The combined bound: the declaration's new context fields together refuse
// past 64 KiB instead of truncating. The action route's own bounded input
// (64 KiB on the whole payload) refuses an over-bound call before the
// declaration's content-specific bound, so the content bound itself is
// proven at the fold, where a raw declaration event answers to the closed
// shape alone.
func TestRecordWorkContextCombinedBoundRefuses(t *testing.T) {
	t.Parallel()
	fixture := seedWorkContextFixture(t, "ctx-combined-bound")
	defer fixture.store.Close()
	fatReading := func() map[string]any {
		return map[string]any{"domain_id": workContextTestChild, "reason": strings.Repeat("r", 512), "source": map[string]any{"kind": "knowledge", "source_id": "concord_knowledge", "law_id": "CD-0002", "content_hash": "sha256:" + strings.Repeat("e", 64)}}
	}
	fullRefs := func() []string {
		refs := make([]string, 8)
		for i := range refs {
			refs[i] = strings.Repeat("r", 256)
		}
		return refs
	}
	overPayload := map[string]any{
		"required_reading": func() []map[string]any {
			readings := make([]map[string]any, 32)
			for i := range readings {
				readings[i] = fatReading()
			}
			return readings
		}(),
		"context_findings": func() []map[string]any {
			findings := make([]map[string]any, 16)
			for i := range findings {
				findings[i] = map[string]any{"kind": "direction", "statement": strings.Repeat("s", 1024), "subject_ref": strings.Repeat("u", 128), "evidence_refs": fullRefs(), "domain_id": workContextTestChild}
			}
			return findings
		}(),
	}
	encoded, err := json.Marshal(overPayload)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) <= WorkContextCombinedMaxBytes {
		t.Fatalf("fixture serialized to %d bytes; the over-bound case must exceed %d", len(encoded), WorkContextCombinedMaxBytes)
	}
	if err := fixture.recordWorkContext(t, overPayload); err == nil {
		t.Fatal("an over-bound action payload was admitted")
	}
	foldView := map[string]any{
		"required_reading": overPayload["required_reading"],
		"finding_refs":     []string{},
		"context_findings": overPayload["context_findings"],
	}
	raw := fixture.rawWorkContextEvent(t, foldView)
	if err := applyWorkflowTestOperation(context.Background(), fixture.store, Operation{Events: []Event{raw}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, fixture.workID): readWorkVersion(t, fixture.store, fixture.workID)}}); !hasFailureKind(err, KindLimitExceeded) {
		t.Fatalf("fold-level combined bound error = %v, want %s", err, KindLimitExceeded)
	}
}

// finding_refs select exact earlier findings. A ref names a real earlier
// entry by event sequence and ordinal; a missing sequence, an out-of-range
// ordinal, and a not-yet-recorded sequence all refuse.
func TestRecordWorkContextFindingRefsSelectEarlierFindings(t *testing.T) {
	t.Parallel()
	fixture := seedWorkContextFixture(t, "ctx-finding-refs")
	defer fixture.store.Close()
	if err := fixture.recordWorkContext(t, map[string]any{
		"required_reading": []map[string]any{workContextRepoReading(workContextTestChild, "")},
		"finding_refs":     []string{},
		"context_findings": []map[string]any{
			workContextFinding(workContextTestChild, "rejected_approach", "the first declaration finding", ""),
			workContextFinding(workContextTestChild, "open_question", "the second declaration finding", ""),
		},
	}); err != nil {
		t.Fatalf("first declaration refused: %v", err)
	}
	var firstSeq int64
	if err := fixture.store.DatabaseForTesting().QueryRow(`SELECT declaration_seq FROM work_context_state WHERE work_id=?`, fixture.workID).Scan(&firstSeq); err != nil {
		t.Fatal(err)
	}
	valid0 := fmt.Sprintf("finding:%d:0", firstSeq)
	valid1 := fmt.Sprintf("finding:%d:1", firstSeq)
	if err := fixture.recordWorkContext(t, map[string]any{
		"required_reading": []map[string]any{workContextRepoReading(workContextTestChild, "")},
		"finding_refs":     []string{valid0, valid1},
		"context_findings": []map[string]any{},
	}); err != nil {
		t.Fatalf("selecting earlier findings refused: %v", err)
	}
	for _, ref := range []string{fmt.Sprintf("finding:%d:2", firstSeq), fmt.Sprintf("finding:%d:0", firstSeq+100)} {
		if err := fixture.recordWorkContext(t, map[string]any{"finding_refs": []string{ref}}); err == nil {
			t.Fatalf("finding ref %s admitted; want the resolution refusal", ref)
		}
	}
}

// The reader assembles the current view: the latest declaration anchor's
// readings, its selected earlier findings, its new findings, and only the
// terminal-report findings recorded after it — grouped by the contract's
// affected-Domain order.
func TestReadWorkContextViewAssemblesAnchorRefsAndReports(t *testing.T) {
	t.Parallel()
	fixture := seedWorkContextFixture(t, "ctx-reader")
	defer fixture.store.Close()
	if err := fixture.recordWorkContext(t, map[string]any{
		"required_reading": []map[string]any{workContextRepoReading(workContextTestChild, "")},
		"finding_refs":     []string{},
		"context_findings": []map[string]any{workContextFinding(workContextTestChild, "rejected_approach", "the first declaration finding", "")},
	}); err != nil {
		t.Fatalf("first declaration refused: %v", err)
	}
	var firstSeq int64
	if err := fixture.store.DatabaseForTesting().QueryRow(`SELECT declaration_seq FROM work_context_state WHERE work_id=?`, fixture.workID).Scan(&firstSeq); err != nil {
		t.Fatal(err)
	}
	// Historical report claims remain selectable, but an unselected claim
	// before a later declaration does not ride that declaration's view.
	reported := []WorkContextFinding{{Kind: "observation", Statement: "the worker reported between declarations", SubjectRef: "internal/store/fold.go", EvidenceRefs: []string{}, DomainID: workContextTestChild}}
	seedContextFactTerminal(t, fixture, "worker.completed", map[string]any{"context_findings": reported})
	// The anchor selects the first declaration's finding and declares a new
	// one; the between-declarations report finding drops out of the view.
	if err := fixture.recordWorkContext(t, map[string]any{
		"required_reading": []map[string]any{workContextRepoReading(workContextTestChild, ""), workContextRepoReading(workContextTestRoot, workContextRootRationale)},
		"finding_refs":     []string{fmt.Sprintf("finding:%d:0", firstSeq)},
		"context_findings": []map[string]any{workContextFinding(workContextTestRoot, "direction", "the anchor direction", workContextRootRationale)},
	}); err != nil {
		t.Fatalf("anchor declaration refused: %v", err)
	}
	// Retained historical claims after the anchor ride the current view.
	subsequent := []WorkContextFinding{{Kind: "contradiction", Statement: "the later worker contradicted the direction", SubjectRef: "internal/store/work_context.go", EvidenceRefs: []string{}, DomainID: workContextTestChild}}
	seedContextFactTerminal(t, fixture, "worker.completed", map[string]any{"context_findings": subsequent})

	view, err := readWorkContextView(context.Background(), fixture.store.DatabaseForTesting(), fixture.workID)
	if err != nil {
		t.Fatal(err)
	}
	if view == nil {
		t.Fatal("view is absent for a work item with declarations")
	}
	if len(view.RequiredReading) != 2 || view.RequiredReading[0].DomainID != workContextTestChild || view.RequiredReading[1].DomainID != workContextTestRoot {
		t.Fatalf("required_reading = %+v, want the anchor's two readings in order", view.RequiredReading)
	}
	statements := make(map[string]string, len(view.Findings))
	for _, finding := range view.Findings {
		statements[finding.FindingID] = finding.Statement
	}
	if len(view.Findings) != 3 {
		t.Fatalf("findings = %+v, want the anchor finding, the selected earlier finding, and the subsequent report finding", view.Findings)
	}
	for _, want := range []string{"the anchor direction", "the first declaration finding", "the later worker contradicted the direction"} {
		if !strings.Contains(strings.Join(mapValues(statements), "\n"), want) {
			t.Fatalf("findings missing %q: %+v", want, view.Findings)
		}
	}
	if strings.Contains(strings.Join(mapValues(statements), "\n"), "the worker reported between declarations") {
		t.Fatal("a report finding superseded by a later declaration rode the current view")
	}
	var frontier int64
	if err := fixture.store.DatabaseForTesting().QueryRow(`SELECT source_frontier FROM work_context_state WHERE work_id=?`, fixture.workID).Scan(&frontier); err != nil {
		t.Fatal(err)
	}
	if view.SourceEventFrontier != frontier {
		t.Fatalf("frontier = %d, want the context-source frontier %d", view.SourceEventFrontier, frontier)
	}
	// A session checkpoint is not a context-finding source.
	recordFixtureCheckpoint(t, fixture, "keep the declared context")
	reread, err := readWorkContextView(context.Background(), fixture.store.DatabaseForTesting(), fixture.workID)
	if err != nil {
		t.Fatal(err)
	}
	if reread.SourceEventFrontier != frontier {
		t.Fatalf("frontier after an unrelated work event = %d, want %d", reread.SourceEventFrontier, frontier)
	}
	// The affected order is the contract's canonical affected list.
	if len(view.DomainGroups) != 2 || view.DomainGroups[0].DomainID != workContextTestChild || view.DomainGroups[1].DomainID != workContextTestRoot {
		t.Fatalf("domain_groups = %+v, want child-alpha then root in the contract's affected order", view.DomainGroups)
	}
	child, root := view.DomainGroups[0], view.DomainGroups[1]
	if len(child.RequiredReadingOrdinals) != 1 || child.RequiredReadingOrdinals[0] != 0 {
		t.Fatalf("child required_reading_ordinals = %v, want [0]", child.RequiredReadingOrdinals)
	}
	if len(root.RequiredReadingOrdinals) != 1 || root.RequiredReadingOrdinals[0] != 1 {
		t.Fatalf("root required_reading_ordinals = %v, want [1]", root.RequiredReadingOrdinals)
	}
	if len(child.FindingIDs) != 2 || len(root.FindingIDs) != 1 {
		t.Fatalf("group finding ids = %+v / %+v, want two child findings and one root finding", child.FindingIDs, root.FindingIDs)
	}
	for _, finding := range view.Findings {
		if finding.Status != "reported" {
			t.Fatalf("finding %s status = %q, want reported", finding.FindingID, finding.Status)
		}
		if finding.SourceEventSeq <= 0 || finding.SourceEventID == "" || finding.Ordinal < 0 {
			t.Fatalf("finding %+v lost its core-derived identity", finding)
		}
	}
}

func mapValues(values map[string]string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = append(out, value)
	}
	return out
}

// A work item with no declaration and no findings reads as the typed absent
// view; continuity leaves the field off and the pinned payload keeps its
// legacy bytes.
func TestReadWorkContextAbsentForLegacyWork(t *testing.T) {
	t.Parallel()
	fixture := seedWorkContextFixture(t, "ctx-legacy-absent")
	defer fixture.store.Close()
	view, err := readWorkContextView(context.Background(), fixture.store.DatabaseForTesting(), fixture.workID)
	if err != nil {
		t.Fatal(err)
	}
	if view != nil {
		t.Fatalf("view = %+v, want the typed absent view", view)
	}
	snapshot, err := ReadWorkflowContinuity(context.Background(), fixture.store, ContinuityRequest{Work: fixture.workID})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.WorkContext != nil {
		t.Fatalf("continuity work_context = %+v, want absent", snapshot.WorkContext)
	}
}

// The current-view bounds refuse the read explicitly: more than 32
// work-wide findings or more than 64 KiB of findings content refuses the
// context read without discarding retained historical claims.
func TestReadWorkContextHistoricalClaimOverflowRefusesReadNotRetention(t *testing.T) {
	t.Parallel()
	fixture := seedWorkContextFixture(t, "ctx-overflow")
	defer fixture.store.Close()
	// Two declarations of 16 findings each; the second selects the first
	// sixteen, so the work-wide current view holds 33.
	declare := func(statement string) {
		t.Helper()
		if err := fixture.recordWorkContext(t, map[string]any{
			"context_findings": func() []map[string]any {
				findings := make([]map[string]any, 16)
				for i := range findings {
					findings[i] = workContextFinding(workContextTestChild, "observation", statement, "")
				}
				return findings
			}(),
		}); err != nil {
			t.Fatalf("declaration %s refused: %v", statement, err)
		}
	}
	declare("first")
	var firstSeq int64
	if err := fixture.store.DatabaseForTesting().QueryRow(`SELECT declaration_seq FROM work_context_state WHERE work_id=?`, fixture.workID).Scan(&firstSeq); err != nil {
		t.Fatal(err)
	}
	refs := make([]string, 16)
	for i := range refs {
		refs[i] = fmt.Sprintf("finding:%d:%d", firstSeq, i)
	}
	// The anchor selects all sixteen earlier findings beside its own
	// sixteen, and the subsequent report adds one more: 33 work-wide.
	if err := fixture.recordWorkContext(t, map[string]any{
		"finding_refs": refs,
		"context_findings": func() []map[string]any {
			findings := make([]map[string]any, 16)
			for i := range findings {
				findings[i] = workContextFinding(workContextTestChild, "observation", "second", "")
			}
			return findings
		}(),
	}); err != nil {
		t.Fatalf("second declaration refused: %v", err)
	}
	retained := []WorkContextFinding{{Kind: "observation", Statement: "retained but unselected", SubjectRef: "internal/store/fold.go", EvidenceRefs: []string{}, DomainID: workContextTestChild}}
	seq := seedContextFactTerminal(t, fixture, "worker.completed", map[string]any{"context_findings": retained})
	var stored int
	if err := fixture.store.db.QueryRow(`SELECT count(*) FROM work_context_findings WHERE work_id=? AND source_seq=?`, fixture.workID, seq).Scan(&stored); err != nil || stored != 1 {
		t.Fatalf("historical claim retention count=%d, want 1: %v", stored, err)
	}
	view, err := readWorkContextView(context.Background(), fixture.store.DatabaseForTesting(), fixture.workID)
	if !hasFailureKind(err, KindLimitExceeded) {
		t.Fatalf("overflow read error = %v, want %s", err, KindLimitExceeded)
	}
	if view != nil {
		t.Fatalf("overflow read returned a view: %+v", view)
	}
}

func TestWorkContextFoldBoundsClosed(t *testing.T) {
	t.Parallel()
	fixture := seedWorkContextFixture(t, "ctx-fold-bounds")
	defer fixture.store.Close()
	tampered := fixture.rawWorkContextEvent(t, map[string]any{"context_findings": []map[string]any{workContextFinding(workContextTestChild, "vibes", "unknown kind", "")}})
	if err := applyWorkflowTestOperation(context.Background(), fixture.store, Operation{Events: []Event{tampered}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, fixture.workID): readWorkVersion(t, fixture.store, fixture.workID)}}); !hasFailureKind(err, KindInvalidPayload) {
		t.Fatalf("tampered declaration error = %v, want %s", err, KindInvalidPayload)
	}
}

// rawWorkContextEvent builds one declaration event at the current payload
// version with the instance-derived identity the fold binds, authored by
// the fixture's recorded owner actor.
func (f workContextFixture) rawWorkContextEvent(t *testing.T, content map[string]any) Event {
	t.Helper()
	ownerRef, err := WorkflowActorRef(f.owner)
	if err != nil {
		t.Fatal(err)
	}
	version := readWorkVersion(t, f.store, f.workID)
	resulting := version + 1
	var stepID, workflowRef, digest string
	var attemptEpoch int64
	if err := f.store.DatabaseForTesting().QueryRow(`SELECT current_step,definition_ref,definition_digest,(SELECT COALESCE(MAX(attempt_epoch),1) FROM durable_operations WHERE work_id=workflow_instances.work_id) FROM workflow_instances WHERE work_id=?`, f.workID).Scan(&stepID, &workflowRef, &digest, &attemptEpoch); err != nil {
		t.Fatal(err)
	}
	content["work_id"] = f.workID
	content["expected_version"] = version
	content["resulting_version"] = resulting
	content["step_id"] = stepID
	content["attempt_epoch"] = attemptEpoch
	content["workflow_ref"] = workflowRef
	content["workflow_definition_digest"] = digest
	content["actor_ref"] = ownerRef
	content["request_id"] = "request:raw-work-context"
	if _, present := content["required_reading"]; !present {
		content["required_reading"] = []map[string]any{}
	}
	if _, present := content["finding_refs"]; !present {
		content["finding_refs"] = []string{}
	}
	if _, present := content["context_findings"]; !present {
		content["context_findings"] = []map[string]any{}
	}
	return Event{EventID: fmt.Sprintf("raw-work-context-%d", version), Kind: WorkflowWorkContextRecorded, SubjectType: SubjectWorkItem, SubjectID: f.workID, Actor: ownerRef, OccurredAt: time.Unix(9, 0).UTC(), PayloadVersion: 1, Payload: mustJSONValue(content)}
}

// Continuity pins the current view when records exist, and the pinned
// projection carries it under work_context.
func TestContinuityPinsWorkContextWhenRecordsExist(t *testing.T) {
	t.Parallel()
	fixture := seedWorkContextFixture(t, "ctx-continuity")
	defer fixture.store.Close()
	snapshot, err := ReadWorkflowContinuity(context.Background(), fixture.store, ContinuityRequest{Work: fixture.workID})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.WorkContext != nil {
		t.Fatal("continuity pinned work_context before any record existed")
	}
	if err := fixture.recordWorkContext(t, map[string]any{
		"required_reading": []map[string]any{workContextRepoReading(workContextTestChild, "")},
		"context_findings": []map[string]any{workContextFinding(workContextTestChild, "open_question", "the continuity question", "")},
	}); err != nil {
		t.Fatalf("declaration refused: %v", err)
	}
	snapshot, err = ReadWorkflowContinuity(context.Background(), fixture.store, ContinuityRequest{Work: fixture.workID})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.WorkContext == nil {
		t.Fatal("continuity left work_context absent after a declaration")
	}
	if len(snapshot.WorkContext.Findings) != 1 || snapshot.WorkContext.Findings[0].Statement != "the continuity question" {
		t.Fatalf("pinned work_context findings = %+v", snapshot.WorkContext.Findings)
	}
}

// Working memory never auto-promotes: after typed context records and a
// valid terminal report, ordinary completion and the context-boundary
// summary write no context content anywhere outside the declaration and
// worker-report events, and no knowledge-side row appears.
func TestCompletionAndBoundaryWriteNoContextContent(t *testing.T) {
	t.Parallel()
	fixture := seedWorkContextFixture(t, "ctx-no-promotion")
	defer fixture.store.Close()
	var archivedBefore int
	if err := fixture.store.DatabaseForTesting().QueryRow(`SELECT count(*) FROM archived_work`).Scan(&archivedBefore); err != nil {
		t.Fatal(err)
	}
	const readingMarker = "CON887-READING-MARKER"
	const findingMarker = "CON887-FINDING-MARKER"
	const workerMarker = "CON887-WORKER-MARKER"
	reading := fixture.repoReading(workContextTestChild, "", "primary")
	reading["reason"] = readingMarker
	if err := fixture.recordWorkContext(t, map[string]any{
		"required_reading": []map[string]any{reading},
		"context_findings": []map[string]any{
			workContextFinding(workContextTestChild, "rejected_approach", findingMarker+"-A", ""),
			workContextFinding(workContextTestChild, "open_question", findingMarker+"-B", ""),
		},
	}); err != nil {
		t.Fatalf("declaration refused: %v", err)
	}
	repo, _ := reviewSubjectClaim(t, fixture.store, fixture.workID)
	var branch string
	if err := fixture.store.db.QueryRow(`SELECT branch FROM worktree_entries WHERE set_id=? AND state='active'`, WorktreeSetID(fixture.workID)).Scan(&branch); err != nil {
		t.Fatal(err)
	}
	gitRunStore(t, repo, "branch", "-m", branch)
	journey := &candidateJourney{fixture: fixture, s: fixture.store, workID: fixture.workID, repo: repo}
	if err := journey.action(t, "checkpoint_repair", map[string]any{}, fixture.owner); err != nil {
		t.Fatalf("start ordinary repair: %v", err)
	}
	conformanceImplementChange(t, journey, "ctx-no-promotion-candidate")
	request := journey.request("ctx-no-promotion-proof", nil)
	request.Command = []string{"git", "diff", "--exit-code", "HEAD"}
	proof, err := fixture.store.VerifyWorktree(t.Context(), request)
	if err != nil || proof.ExitCode != 0 || proof.SubjectRef != "commit:"+journey.head(t) {
		t.Fatalf("capture criterion proof on the current candidate: %+v err=%v", proof, err)
	}
	retained := []WorkContextFinding{{Kind: "observation", Statement: workerMarker, SubjectRef: "internal/store/work_context.go", EvidenceRefs: []string{}, DomainID: workContextTestChild}}
	seedContextFactTerminal(t, fixture, "worker.completed", map[string]any{"context_findings": retained})
	completeCurrentReviewForTest(t, fixture, repo, "ready_for_final_proof", func(report *ReviewBlock) {
		report.Assessments = []ReviewAssessment{{PredicateID: "predicate:work-context", Assessment: "supported", ReceiptRefs: []string{proof.OperationRef}}}
	})
	// The compaction-draft-adjacent boundary: a durable checkpoint and a
	// summary boundary carry only their own content, before the terminal
	// completion freezes the instance.
	if err := runVerdictActionAs(t, fixture.store, fixture.workID, "checkpoint_context", mustJSONValue(map[string]any{
		"checkpoint_id": "ctx-session-checkpoint", "active_unit": "the bounded unit", "hypothesis": "the bounded hypothesis", "diagnosis": "the bounded diagnosis", "strategy": "the bounded strategy",
		"touched_refs": []string{"internal/store/work_context.go"}, "evidence_refs": []string{"evidence:ctx"}, "pending_questions": []string{}, "pending_decisions": []string{},
	}), 0, fixture.owner); err != nil {
		t.Fatalf("checkpoint after context records refused: %v", err)
	}
	if err := runVerdictActionAs(t, fixture.store, fixture.workID, "cross_context_boundary", mustJSONValue(map[string]any{
		"boundary_kind": "summary", "mode": "summary", "checkpoint_id": "ctx-session-checkpoint", "summary": "the bounded session summary names no finding",
	}), 0, fixture.owner); err != nil {
		t.Fatalf("context boundary refused: %v", err)
	}
	// Main associates the captured proof and completes through the declared
	// exits. Context claims cannot stand in for criterion evidence.
	if err := journey.action(t, "bind_evidence", map[string]any{"evidence_kind": "verification", "evidence_ref": proof.OperationRef}, fixture.owner); err != nil {
		t.Fatalf("bind captured proof: %v", err)
	}
	delivery := map[string]any{"delivery_artifact": "commit:" + journey.head(t), "delivery_state": "asserted"}
	for _, action := range []string{"record_delivery", "checkpoint_refine", "record_delivery", "record_delivery"} {
		fields := delivery
		if action == "checkpoint_refine" {
			fields = map[string]any{}
		}
		if err := journey.action(t, action, fields, fixture.owner); err != nil {
			t.Fatalf("current completion route %s: %v", action, err)
		}
	}
	if err := journey.action(t, "bind_evidence", map[string]any{"evidence_kind": "artifact", "evidence_ref": delivery["delivery_artifact"]}, fixture.owner); err != nil {
		t.Fatalf("bind delivered artifact: %v", err)
	}
	if err := journey.action(t, "record_verdict", criterionAssociationPayload(proof.OperationRef, "predicate:work-context"), fixture.owner); err != nil {
		t.Fatalf("associate captured criterion proof: %v", err)
	}
	seedComparisonObservation(t, fixture.store, fixture.workID)
	freshness, err := PrepareWorkflowAcceptanceFreshness(t.Context(), fixture.store, PrepareWorkflowSubjectRequest{WorkID: fixture.workID})
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithWorkflowSubjectPreparation(t.Context(), freshness)
	confirmation := WorkflowActionExecutionRequest{
		WorkID: fixture.workID, ExpectedVersion: readWorkVersion(t, fixture.store, fixture.workID), ActionID: "confirm_premise",
		Payload: json.RawMessage(`{}`), Actor: fixture.owner,
		AcceptedInputsDigest: nativeDigest([]byte("ctx-no-promotion-confirm")), IdempotencyIdentity: "ctx-no-promotion-confirm", OperationID: "ctx-no-promotion-confirm",
		PrincipalRef: fixture.owner.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: "ctx-no-promotion-confirm", RequestID: "request:ctx-no-promotion-confirm", ContractDigest: testManifestDigest, Now: time.Now().UTC(),
	}
	if err := fixture.store.Transact(ctx, func(tx *Transaction) error {
		_, err := ApplyWorkflowActionTx(ctx, tx, BuiltinWorkflowRegistry(), confirmation)
		return err
	}); err != nil {
		t.Fatalf("confirm the evidenced premise: %v", err)
	}
	bindCompletionClosure(t, fixture.store, fixture.workID, fixture.owner)
	if err := journey.action(t, "complete", map[string]any{"impact_verdict": "non-breaking"}, fixture.owner); err != nil {
		t.Fatalf("completion after context records refused: %v", err)
	}
	// Closing the lifecycle keeps the context as claims: the closed item
	// still answers every declared and historically reported finding verbatim,
	// each still reported, and the reading reference unchanged.
	var lifecycle string
	if err := fixture.store.DatabaseForTesting().QueryRow(`SELECT lifecycle FROM work_items WHERE id=?`, fixture.workID).Scan(&lifecycle); err != nil {
		t.Fatal(err)
	}
	if lifecycle != "completed" {
		t.Fatalf("lifecycle after the terminal completion = %q, want completed", lifecycle)
	}
	closed, err := readWorkContextView(context.Background(), fixture.store.DatabaseForTesting(), fixture.workID)
	if err != nil {
		t.Fatalf("read work context after close: %v", err)
	}
	if closed == nil || len(closed.RequiredReading) != 1 || closed.RequiredReading[0].Reason != readingMarker {
		t.Fatalf("required reading after close = %+v", closed)
	}
	statements := map[string]string{}
	for _, finding := range closed.Findings {
		statements[finding.Statement] = finding.Status
	}
	for _, statement := range []string{findingMarker + "-A", findingMarker + "-B", workerMarker} {
		if status, ok := statements[statement]; !ok || status != WorkContextFindingStatusReported {
			t.Fatalf("finding %q after close has status %q (present %v), want reported; findings %v", statement, status, ok, statements)
		}
	}
	rows, err := fixture.store.DatabaseForTesting().Query(`SELECT ? AS kind,declaration_payload FROM work_context_state WHERE work_id=? AND declaration_payload LIKE '%CON887-%'
UNION ALL SELECT r.kind,f.finding FROM work_context_findings f JOIN effect_receipts r ON r.effect_seq=f.source_seq WHERE f.work_id=? AND f.finding LIKE '%CON887-%'`, WorkflowWorkContextRecorded, fixture.workID, fixture.workID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	carrying := map[string]bool{}
	for rows.Next() {
		var kind, payload string
		if err := rows.Scan(&kind, &payload); err != nil {
			t.Fatal(err)
		}
		carrying[kind] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(carrying) != 2 || !carrying[WorkflowWorkContextRecorded] || !carrying["worker.completed"] {
		t.Fatalf("retained context marker sources = %v, want the declaration and historical report", carrying)
	}
	for _, probe := range []string{
		`SELECT count(*) FROM workflow_context_checkpoints WHERE work_id=? AND (active_unit LIKE '%CON887-%' OR hypothesis LIKE '%CON887-%' OR diagnosis LIKE '%CON887-%' OR strategy LIKE '%CON887-%')`,
		`SELECT count(*) FROM workflow_context_boundaries WHERE work_id=? AND summary LIKE '%CON887-%'`,
		`SELECT count(*) FROM workflow_contracts WHERE work_id=? AND premise LIKE '%CON887-%'`,
		`SELECT count(*) FROM law_subjects WHERE title LIKE '%CON887-%' OR path LIKE '%CON887-%'`,
	} {
		var count int
		if err := fixture.store.DatabaseForTesting().QueryRow(probe, fixture.workID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("probe %q found %d rows; context content was promoted", probe, count)
		}
	}
	var archivedAfter int
	if err := fixture.store.DatabaseForTesting().QueryRow(`SELECT count(*) FROM archived_work`).Scan(&archivedAfter); err != nil {
		t.Fatal(err)
	}
	if archivedAfter != archivedBefore {
		t.Fatalf("context completion changed archived-work rows from %d to %d", archivedBefore, archivedAfter)
	}
	output, err := (ExecGitRunner{}).Run(t.Context(), repo, "grep", "-n", "-e", readingMarker, "-e", findingMarker, "-e", workerMarker, "HEAD", "--")
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 {
		t.Fatalf("committed context-marker search must find no match: output=%q err=%v", output, err)
	}
}

func TestWorkContextFindingAttributionBindsRegistryDomains(t *testing.T) {
	t.Parallel()
	fixture := seedWorkContextFixture(t, "ctx-terminal-domain")
	defer fixture.store.Close()
	finding := func(domain, rationale string) []WorkContextFinding {
		return []WorkContextFinding{{Kind: "observation", Statement: "a reported claim", SubjectRef: "internal/store/fold.go", EvidenceRefs: []string{}, DomainID: domain, ProductWideRationale: rationale}}
	}
	refusals := []struct {
		name     string
		findings []WorkContextFinding
		want     FailureKind
	}{
		{"missing domain", finding("", ""), KindInvalidPayload},
		{"unknown domain", finding("domain:absent", ""), KindUnknownScope},
		{"root without rationale", finding(workContextTestRoot, ""), KindInvalidPayload},
		{"child with rationale", finding(workContextTestChild, workContextRootRationale), KindInvalidPayload},
	}
	for _, refusal := range refusals {
		before := outsideRepairDatabaseSnapshot(t, fixture.store)
		if err := fixture.recordWorkContext(t, map[string]any{"context_findings": refusal.findings}); !hasFailureKind(err, refusal.want) {
			t.Fatalf("declaration %s error = %v, want %s", refusal.name, err, refusal.want)
		}
		if after := outsideRepairDatabaseSnapshot(t, fixture.store); after != before {
			t.Fatalf("refused %s declaration changed database state", refusal.name)
		}
	}
	if err := fixture.recordWorkContext(t, map[string]any{"context_findings": finding(workContextTestChild, "")}); err != nil {
		t.Fatalf("child-domain declaration refused: %v", err)
	}
	view, err := readWorkContextView(t.Context(), fixture.store.DatabaseForTesting(), fixture.workID)
	if err != nil || view == nil || len(view.Findings) != 1 {
		t.Fatalf("declaration findings=%+v, want one current reported claim: %v", view, err)
	}
	var eventID string
	if err := fixture.store.db.QueryRow(`SELECT event_id FROM effect_receipts WHERE kind=? AND subject_id=? ORDER BY effect_seq DESC LIMIT 1`, WorkflowWorkContextRecorded, fixture.workID).Scan(&eventID); err != nil {
		t.Fatal(err)
	}
	got := view.Findings[0]
	if got.DomainID != workContextTestChild || got.Statement != "a reported claim" || got.Origin != WorkContextOriginDeclaration || got.Status != WorkContextFindingStatusReported || got.SourceEventID != eventID {
		t.Fatalf("declaration claim lost its Domain, attribution or reported status: %+v", got)
	}
}

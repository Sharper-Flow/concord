package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// CON-887 store work-context foundation. The record_work_context action
// appends one typed declaration event; the tx-scoped reader assembles the
// current view from the latest declaration anchor, its referenced earlier
// findings, and subsequent terminal-report findings. Nothing here promotes
// context content into Git, knowledge, or any projection table: the event
// log is the only context authority.

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
	if err := enterFold(ctx, tx); err != nil {
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
	if err := leaveFold(ctx, tx); err != nil {
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
	if err := enterFold(ctx, tx); err != nil {
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
	if err := leaveFold(ctx, tx); err != nil {
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
}

// seedWorkContextFixture seeds one break-fix work item whose approved
// contract affects root and child-alpha, advanced to the repair step, with
// the registry fixture above installed.
func seedWorkContextFixture(t *testing.T, workID string) workContextFixture {
	t.Helper()
	ctx := context.Background()
	registered, err := BuiltinWorkflowDefinitionForRef("workflow.break_fix")
	if err != nil {
		t.Fatal(err)
	}
	s := openTemp(t)
	seedWork(t, s, workID)
	seedWorkflowLaw(t, s)
	seedWorkContextDomainRegistry(t, s)
	owner := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/owner", SessionRef: "session/" + workID, ActorClass: ActorAgent}
	operator := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/operator", SessionRef: "session/" + workID + "-operator", ActorClass: ActorOperator}
	ownerRef, err := WorkflowActorRef(owner)
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
	events = append(events, Event{
		EventID: "ctx-contract-" + workID, Kind: WorkflowContractApproved, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: ownerRef, OccurredAt: time.Unix(5, 0).UTC(), PayloadVersion: 3,
		Payload: mustJSONValue(map[string]any{
			"work_id": workID, "expected_version": version, "resulting_version": version + 1, "contract_version": 1, "premise": "deliver the checked change", "outcome_kind": "check",
			"outcome_predicates": []map[string]any{{"predicate_id": "predicate:work-context", "ordinal": 0, "outcome_kind": "check", "outcome_payload": map[string]any{"kind": "check", "check_ref": "check:work-context", "immutable_subject_ref": "commit:" + workContextTestCommit(), "expected_result": "pass"}}},
			"required_evidence":  []string{"verification"}, "route_conventions": []string{}, "spec_mandate": []string{}, "law_modifies": []string{},
			"law_revisions": []WorkflowLawRevision{}, "law_boundary_version": 1, "rigor_class": "prototype_internal", "consequence_class": "internal_sqlite",
			"architecture_binding": WorkflowArchitectureBinding{
				DomainRegistryContentHash: "sha256:" + strings.Repeat("b", 64), HomeDomainID: workContextTestRoot, AffectedDomainIDs: []string{workContextTestRoot, workContextTestChild},
				DomainModifies: []string{}, DomainRelationModifies: []WorkflowDomainRelationModification{}, LawAdditions: []WorkflowLawAddition{}, VerificationObligations: []WorkflowVerificationObligation{},
			},
		}),
	})
	if err := applyWorkflowTestOperation(ctx, s, Operation{Events: events, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): 4}}); err != nil {
		t.Fatal(err)
	}
	if err := advanceWorkflowTestInstanceToStep(ctx, s, workID, "repair", ownerRef); err != nil {
		t.Fatal(err)
	}
	return workContextFixture{store: s, workID: workID, owner: owner, operator: operator}
}

func (f workContextFixture) recordWorkContext(t *testing.T, payload any) error {
	t.Helper()
	return runVerdictActionAs(t, f.store, f.workID, "record_work_context", json.RawMessage(mustJSONValue(payload)), 0, f.owner)
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
	versionBefore := verdictItemVersion(t, fixture.store, fixture.workID)
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
	if err := fixture.store.DatabaseForTesting().QueryRow(`SELECT payload FROM domain_events WHERE subject_id=? AND kind=?`, fixture.workID, WorkflowWorkContextRecorded).Scan(&stored); err != nil {
		t.Fatalf("declaration event missing: %v", err)
	}
	if !strings.Contains(stored, "the unbounded log scan was rejected") || !strings.Contains(stored, "the reading carries the bounded reason") {
		t.Fatalf("declaration payload lost authored content: %s", stored)
	}
	if got := verdictItemVersion(t, fixture.store, fixture.workID); got <= versionBefore {
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
	if err := applyWorkflowTestOperation(context.Background(), fixture.store, Operation{Events: []Event{raw}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, fixture.workID): verdictItemVersion(t, fixture.store, fixture.workID)}}); !hasFailureKind(err, KindLimitExceeded) {
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
	if err := fixture.store.DatabaseForTesting().QueryRow(`SELECT seq FROM domain_events WHERE subject_id=? AND kind=? ORDER BY seq DESC LIMIT 1`, fixture.workID, WorkflowWorkContextRecorded).Scan(&firstSeq); err != nil {
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
// affected-Domain order with the reserved empty domain_cards slot.
func TestReadWorkContextViewAssemblesAnchorRefsAndReports(t *testing.T) {
	t.Parallel()
	fixture := seedWorkContextFixture(t, "ctx-reader")
	defer fixture.store.Close()
	lane := BuiltinLaneDefinitions()[0]
	if err := fixture.recordWorkContext(t, map[string]any{
		"required_reading": []map[string]any{workContextRepoReading(workContextTestChild, "")},
		"finding_refs":     []string{},
		"context_findings": []map[string]any{workContextFinding(workContextTestChild, "rejected_approach", "the first declaration finding", "")},
	}); err != nil {
		t.Fatalf("first declaration refused: %v", err)
	}
	var firstSeq int64
	if err := fixture.store.DatabaseForTesting().QueryRow(`SELECT seq FROM domain_events WHERE subject_id=? AND kind=? ORDER BY seq DESC LIMIT 1`, fixture.workID, WorkflowWorkContextRecorded).Scan(&firstSeq); err != nil {
		t.Fatal(err)
	}
	// A terminal report between the declarations contributes findings only
	// while it is not superseded by a later declaration selecting nothing.
	if err := ApplyOperation(context.Background(), fixture.store, Operation{Events: []Event{workerDispatchEvent(fixture.workID, "attempt-ctx-1", lane, nil)}}); err != nil {
		t.Fatal(err)
	}
	reported := []WorkerContextFinding{{Kind: "observation", Statement: "the worker reported between declarations", SubjectRef: "internal/store/fold.go", EvidenceRefs: []string{}, DomainID: workContextTestChild}}
	completion := workerCompletedContextFindingsEvent(fixture.workID, "ctx-complete-1", "attempt-ctx-1", preferredModelForLane(lane), lane, reported, WorkerEvidenceEventPayloadVersion(WorkerCompleted))
	if err := ApplyOperation(context.Background(), fixture.store, Operation{Events: []Event{completion}}); err != nil {
		t.Fatalf("worker report with findings refused: %v", err)
	}
	// The anchor selects the first declaration's finding and declares a new
	// one; the between-declarations report finding drops out of the view.
	if err := fixture.recordWorkContext(t, map[string]any{
		"required_reading": []map[string]any{workContextRepoReading(workContextTestChild, ""), workContextRepoReading(workContextTestRoot, workContextRootRationale)},
		"finding_refs":     []string{fmt.Sprintf("finding:%d:0", firstSeq)},
		"context_findings": []map[string]any{workContextFinding(workContextTestRoot, "direction", "the anchor direction", workContextRootRationale)},
	}); err != nil {
		t.Fatalf("anchor declaration refused: %v", err)
	}
	// A subsequent terminal report rides the current view.
	if err := ApplyOperation(context.Background(), fixture.store, Operation{Events: []Event{workerDispatchEvent(fixture.workID, "attempt-ctx-2", lane, nil)}}); err != nil {
		t.Fatal(err)
	}
	subsequent := []WorkerContextFinding{{Kind: "contradiction", Statement: "the later worker contradicted the direction", SubjectRef: "internal/store/work_context.go", EvidenceRefs: []string{}, DomainID: workContextTestChild}}
	if err := ApplyOperation(context.Background(), fixture.store, Operation{Events: []Event{workerCompletedContextFindingsEvent(fixture.workID, "ctx-complete-2", "attempt-ctx-2", preferredModelForLane(lane), lane, subsequent, WorkerEvidenceEventPayloadVersion(WorkerCompleted))}}); err != nil {
		t.Fatal(err)
	}

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
	if err := fixture.store.DatabaseForTesting().QueryRow(`SELECT COALESCE(MAX(seq),0) FROM domain_events WHERE subject_type='work_item' AND subject_id=?`, fixture.workID).Scan(&frontier); err != nil {
		t.Fatal(err)
	}
	if view.SourceEventFrontier != frontier {
		t.Fatalf("frontier = %d, want %d", view.SourceEventFrontier, frontier)
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
	for _, group := range view.DomainGroups {
		if group.DomainCards == nil || len(group.DomainCards) != 0 {
			t.Fatalf("domain_cards = %+v, want the reserved empty slot", group.DomainCards)
		}
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
// context read while the terminal-report retention itself never blocks.
func TestReadWorkContextOverflowRefusesReadNotRetention(t *testing.T) {
	t.Parallel()
	fixture := seedWorkContextFixture(t, "ctx-overflow")
	defer fixture.store.Close()
	lane := BuiltinLaneDefinitions()[0]
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
	if err := fixture.store.DatabaseForTesting().QueryRow(`SELECT seq FROM domain_events WHERE subject_id=? AND kind=? ORDER BY seq DESC LIMIT 1`, fixture.workID, WorkflowWorkContextRecorded).Scan(&firstSeq); err != nil {
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
	if err := ApplyOperation(context.Background(), fixture.store, Operation{Events: []Event{workerDispatchEvent(fixture.workID, "attempt-ctx-overflow", lane, nil)}}); err != nil {
		t.Fatal(err)
	}
	retained := []WorkerContextFinding{{Kind: "observation", Statement: "retained but unselected", SubjectRef: "internal/store/fold.go", EvidenceRefs: []string{}, DomainID: workContextTestChild}}
	if err := ApplyOperation(context.Background(), fixture.store, Operation{Events: []Event{workerCompletedContextFindingsEvent(fixture.workID, "ctx-complete-overflow", "attempt-ctx-overflow", preferredModelForLane(lane), lane, retained, WorkerEvidenceEventPayloadVersion(WorkerCompleted))}}); err != nil {
		t.Fatalf("terminal report retention blocked by the view bound: %v", err)
	}
	view, err := readWorkContextView(context.Background(), fixture.store.DatabaseForTesting(), fixture.workID)
	if !hasFailureKind(err, KindLimitExceeded) {
		t.Fatalf("overflow read error = %v, want %s", err, KindLimitExceeded)
	}
	if view != nil {
		t.Fatalf("overflow read returned a view: %+v", view)
	}
}

// Replay needs no registry: after the registry rows are gone, the stored
// declaration event replays through RebuildFromLog without refusal, and a
// raw tampered declaration refuses at the fold's closed bounds.
func TestWorkContextReplayNeedsNoRegistryAndFoldBoundsClosed(t *testing.T) {
	t.Parallel()
	fixture := seedWorkContextFixture(t, "ctx-replay")
	defer fixture.store.Close()
	if err := fixture.recordWorkContext(t, map[string]any{
		"required_reading": []map[string]any{workContextRepoReading(workContextTestChild, "")},
		"context_findings": []map[string]any{workContextFinding(workContextTestChild, "direction", "the replay direction", "")},
	}); err != nil {
		t.Fatalf("declaration refused: %v", err)
	}
	// Remove every registry row inside one transaction under the fold
	// guard, so the deferred registry/domain foreign keys resolve to a
	// consistent empty state.
	tx, err := fixture.store.DatabaseForTesting().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := enterFold(context.Background(), tx); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(context.Background(), `DELETE FROM domain_registries; DELETE FROM domains;`); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := leaveFold(context.Background(), tx); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := RebuildFromLog(context.Background(), fixture.store); err != nil {
		t.Fatalf("replay refused without the registry: %v", err)
	}
	var retained int
	if err := fixture.store.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id=? AND kind=?`, fixture.workID, WorkflowWorkContextRecorded).Scan(&retained); err != nil {
		t.Fatal(err)
	}
	if retained != 1 {
		t.Fatalf("retained declarations = %d, want 1", retained)
	}
	// The fold's own closed bounds: a tampered raw event refuses regardless
	// of the registry.
	tampered := fixture.rawWorkContextEvent(t, map[string]any{"context_findings": []map[string]any{workContextFinding(workContextTestChild, "vibes", "unknown kind", "")}})
	if err := applyWorkflowTestOperation(context.Background(), fixture.store, Operation{Events: []Event{tampered}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, fixture.workID): verdictItemVersion(t, fixture.store, fixture.workID)}}); !hasFailureKind(err, KindInvalidPayload) {
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
	version := verdictItemVersion(t, f.store, f.workID)
	resulting := version + 1
	var stepID, workflowRef, digest string
	var definitionVersion, attemptEpoch int64
	if err := f.store.DatabaseForTesting().QueryRow(`SELECT current_step,definition_ref,definition_digest,definition_version,(SELECT COALESCE(MAX(attempt_epoch),1) FROM durable_operations WHERE work_id=workflow_instances.work_id) FROM workflow_instances WHERE work_id=?`, f.workID).Scan(&stepID, &workflowRef, &digest, &definitionVersion, &attemptEpoch); err != nil {
		t.Fatal(err)
	}
	content["work_id"] = f.workID
	content["expected_version"] = version
	content["resulting_version"] = resulting
	content["step_id"] = stepID
	content["attempt_epoch"] = attemptEpoch
	content["workflow_ref"] = workflowRef
	content["workflow_definition_version"] = definitionVersion
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
	const readingMarker = "CON887-READING-MARKER"
	const findingMarker = "CON887-FINDING-MARKER"
	const workerMarker = "CON887-WORKER-MARKER"
	lane := BuiltinLaneDefinitions()[0]
	if err := fixture.recordWorkContext(t, map[string]any{
		"required_reading": []map[string]any{{"domain_id": workContextTestChild, "reason": readingMarker, "source": map[string]any{"kind": "repository_file", "project_id": "project", "path": "internal/store/work_context.go", "commit_oid": workContextTestCommit()}}},
		"context_findings": []map[string]any{
			workContextFinding(workContextTestChild, "rejected_approach", findingMarker+"-A", ""),
			workContextFinding(workContextTestChild, "open_question", findingMarker+"-B", ""),
		},
	}); err != nil {
		t.Fatalf("declaration refused: %v", err)
	}
	if err := ApplyOperation(context.Background(), fixture.store, Operation{Events: []Event{workerDispatchEvent(fixture.workID, "attempt-ctx-promote", lane, nil)}}); err != nil {
		t.Fatal(err)
	}
	retained := []WorkerContextFinding{{Kind: "observation", Statement: workerMarker, SubjectRef: "internal/store/fold.go", EvidenceRefs: []string{}, DomainID: workContextTestChild}}
	if err := ApplyOperation(context.Background(), fixture.store, Operation{Events: []Event{workerCompletedContextFindingsEvent(fixture.workID, "ctx-complete-promote", "attempt-ctx-promote", preferredModelForLane(lane), lane, retained, WorkerEvidenceEventPayloadVersion(WorkerCompleted))}}); err != nil {
		t.Fatal(err)
	}
	// The compaction-draft-adjacent boundary: a durable checkpoint and a
	// summary boundary carry only their own content, before the terminal
	// completion freezes the instance.
	if err := runVerdictActionAs(t, fixture.store, fixture.workID, "checkpoint_context", json.RawMessage(mustJSONValue(map[string]any{
		"checkpoint_id": "ctx-session-checkpoint", "active_unit": "the bounded unit", "hypothesis": "the bounded hypothesis", "diagnosis": "the bounded diagnosis", "strategy": "the bounded strategy",
		"touched_refs": []string{"internal/store/work_context.go"}, "evidence_refs": []string{"evidence:ctx"}, "pending_questions": []string{}, "pending_decisions": []string{},
	})), 0, fixture.owner); err != nil {
		t.Fatalf("checkpoint after context records refused: %v", err)
	}
	if err := runVerdictActionAs(t, fixture.store, fixture.workID, "cross_context_boundary", json.RawMessage(mustJSONValue(map[string]any{
		"boundary_kind": "summary", "mode": "summary", "checkpoint_id": "ctx-session-checkpoint", "summary": "the bounded session summary names no finding",
	})), 0, fixture.owner); err != nil {
		t.Fatalf("context boundary refused: %v", err)
	}
	// Ordinary completion: the terminal completion event derives from the
	// log's verdicts, never from context content. A distinct recorded
	// reviewer authors the verdict the completion records, mirroring the
	// self-evaluation refusal the corrected independence law pins.
	reviewer := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/ctx-reviewer", SessionRef: "session/" + fixture.workID + "-reviewer", ActorClass: ActorOperator}
	reviewerRef, err := WorkflowActorRef(reviewer)
	if err != nil {
		t.Fatal(err)
	}
	version := verdictItemVersion(t, fixture.store, fixture.workID)
	reviewerEvent := workflowEventWithActor("ctx-reviewer-"+fixture.workID, WorkflowActorRecorded, fixture.workID, reviewerRef, map[string]any{
		"work_id": fixture.workID, "expected_version": version, "resulting_version": version + 1,
		"actor_ref": reviewerRef, "principal_ref": reviewer.PrincipalRef, "client_ref": reviewer.ClientRef,
		"agent_ref": reviewer.AgentRef, "session_ref": reviewer.SessionRef, "actor_class": string(reviewer.ActorClass),
	})
	if err := applyWorkflowTestOperation(context.Background(), fixture.store, Operation{Events: []Event{reviewerEvent}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, fixture.workID): version}}); err != nil {
		t.Fatalf("record reviewer: %v", err)
	}
	version = verdictItemVersion(t, fixture.store, fixture.workID)
	verdict := workflowEventWithActor("ctx-verdict-"+fixture.workID, WorkflowVerdictRecorded, fixture.workID, reviewerRef, map[string]any{
		"work_id": fixture.workID, "expected_version": version, "resulting_version": version + 1, "contract_version": 1,
		"predicate_id": "predicate:work-context", "verdict_kind": "ok", "verdict_actor_ref": reviewerRef,
		"evaluation_evidence": []string{"evidence:ctx-verdict"},
	})
	verdict.PayloadVersion = 2
	if err := applyWorkflowTestOperation(context.Background(), fixture.store, Operation{Events: []Event{verdict}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, fixture.workID): version}}); err != nil {
		t.Fatalf("record reviewer verdict: %v", err)
	}
	version = verdictItemVersion(t, fixture.store, fixture.workID)
	completion := workflowEventWithActor("ctx-completed-"+fixture.workID, WorkflowCompleted, fixture.workID, reviewerRef, map[string]any{
		"work_id": fixture.workID, "expected_version": version, "resulting_version": version + 1, "terminal_state": "completed", "final_verdict_kind": "ok", "verdict_actor_ref": reviewerRef, "premise_confirmed": true, "evidence_count": 1, "changed_refs_digest": WorkflowChangedRefsDigest([]string{fixture.workID}), "impact_verdict": "non-breaking",
	})
	completion.PayloadVersion = 2
	if err := applyWorkflowTestOperation(context.Background(), fixture.store, Operation{Events: []Event{completion}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, fixture.workID): version}}); err != nil {
		t.Fatalf("completion after context records refused: %v", err)
	}
	// No event outside the declaration and worker-report kinds carries the
	// markers, and no knowledge-side promotion row exists.
	rows, err := fixture.store.DatabaseForTesting().Query(`SELECT kind, payload FROM domain_events WHERE subject_id=? AND (payload LIKE '%CON887-%')`, fixture.workID)
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
	if len(carrying) != 2 || !carrying[WorkflowWorkContextRecorded] || !carrying[WorkerCompleted] {
		t.Fatalf("event kinds carrying context markers = %v, want only the declaration and worker report", carrying)
	}
	for _, probe := range []string{
		`SELECT count(*) FROM workflow_context_checkpoints WHERE work_id=? AND (active_unit LIKE '%CON887-%' OR hypothesis LIKE '%CON887-%' OR diagnosis LIKE '%CON887-%' OR strategy LIKE '%CON887-%')`,
		`SELECT count(*) FROM workflow_context_boundaries WHERE work_id=? AND summary LIKE '%CON887-%'`,
		`SELECT count(*) FROM workflow_contracts WHERE work_id=? AND premise LIKE '%CON887-%'`,
		`SELECT count(*) FROM law_subjects WHERE title LIKE '%CON887-%' OR path LIKE '%CON887-%'`,
		`SELECT count(*) FROM archived_work`,
	} {
		var count int
		if err := fixture.store.DatabaseForTesting().QueryRow(probe, fixture.workID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("probe %q found %d rows; context content was promoted", probe, count)
		}
	}
}

// TestTerminalReportFindingsBindRegistryDomainsLiveNotOnReplay is the
// CON-892 terminal-report admission: each reported finding names a Domain,
// the live fold validates it against the current registry and approved
// affected scope through ValidateWorkContextDomainTx, and replay folds the
// retained report without today's registry.
func TestTerminalReportFindingsBindRegistryDomainsLiveNotOnReplay(t *testing.T) {
	t.Parallel()
	fixture := seedWorkContextFixture(t, "ctx-terminal-domain")
	defer fixture.store.Close()
	lane := BuiltinLaneDefinitions()[0]
	model := preferredModelForLane(lane)
	finding := func(domain, rationale string) []WorkerContextFinding {
		return []WorkerContextFinding{{Kind: "observation", Statement: "a reported claim", SubjectRef: "internal/store/fold.go", EvidenceRefs: []string{}, DomainID: domain, ProductWideRationale: rationale}}
	}
	dispatch := func(attempt string) {
		t.Helper()
		if err := ApplyOperation(context.Background(), fixture.store, Operation{Events: []Event{workerDispatchEvent(fixture.workID, attempt, lane, nil)}}); err != nil {
			t.Fatal(err)
		}
	}
	dispatch("attempt-domain-1")
	refusals := []struct {
		name     string
		findings []WorkerContextFinding
		want     FailureKind
	}{
		{"missing domain", finding("", ""), KindInvalidPayload},
		{"unknown domain", finding("domain:absent", ""), KindUnknownScope},
		{"root without rationale", finding(workContextTestRoot, ""), KindInvalidPayload},
		{"child with rationale", finding(workContextTestChild, workContextRootRationale), KindInvalidPayload},
	}
	for index, refusal := range refusals {
		completion := workerCompletedContextFindingsEvent(fixture.workID, fmt.Sprintf("ctx-domain-refused-%d", index), "attempt-domain-1", model, lane, refusal.findings, WorkerEvidenceEventPayloadVersion(WorkerCompleted))
		if err := ApplyOperation(context.Background(), fixture.store, Operation{Events: []Event{completion}}); !hasFailureKind(err, refusal.want) {
			t.Fatalf("completion %s error = %v, want %s", refusal.name, err, refusal.want)
		}
		failure := workerFailedContextFindingsEvent(fixture.workID, fmt.Sprintf("ctx-domain-refused-f-%d", index), "attempt-domain-1", model, WorkerFailureWorkerError, refusal.findings, WorkerEvidenceEventPayloadVersion(WorkerFailed))
		if err := ApplyOperation(context.Background(), fixture.store, Operation{Events: []Event{failure}}); !hasFailureKind(err, refusal.want) {
			t.Fatalf("failure %s error = %v, want %s", refusal.name, err, refusal.want)
		}
	}
	if err := ApplyOperation(context.Background(), fixture.store, Operation{Events: []Event{workerCompletedContextFindingsEvent(fixture.workID, "ctx-domain-ok", "attempt-domain-1", model, lane, finding(workContextTestChild, ""), WorkerEvidenceEventPayloadVersion(WorkerCompleted))}}); err != nil {
		t.Fatalf("child-domain completion refused: %v", err)
	}
	dispatch("attempt-domain-2")
	if err := ApplyOperation(context.Background(), fixture.store, Operation{Events: []Event{workerFailedContextFindingsEvent(fixture.workID, "ctx-domain-ok-f", "attempt-domain-2", model, WorkerFailureWorkerError, finding(workContextTestRoot, workContextRootRationale), WorkerEvidenceEventPayloadVersion(WorkerFailed))}}); err != nil {
		t.Fatalf("root-domain failure with rationale refused: %v", err)
	}
	tx, err := fixture.store.DatabaseForTesting().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := enterFold(context.Background(), tx); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(context.Background(), `DELETE FROM domain_registries; DELETE FROM domains;`); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := leaveFold(context.Background(), tx); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := RebuildFromLog(context.Background(), fixture.store); err != nil {
		t.Fatalf("replay of retained terminal reports refused without the registry: %v", err)
	}
}

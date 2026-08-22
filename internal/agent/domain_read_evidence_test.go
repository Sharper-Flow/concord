package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

// singleDomainRootID mirrors the accepted registry shape: Concord is a
// single-Domain Product whose one root Domain has no children, so the
// architecture relation set is legitimately empty.
const singleDomainRootID = "product-root:concord"

// domainEvidenceFixture seeds a store carrying the real single-Domain registry
// shape: one root Domain projected from a committed knowledge manifest, one
// accepted decision and one superseded decision homed to it, two nonterminal
// contracts bound to it, and typed local attachments. The registry, Domains and
// law rows come from the live knowledge projection rather than direct inserts,
// so the reads under test observe the same rows a real index rebuild produces.
func domainEvidenceFixture(t *testing.T) (*store.Store, *Service, CallEnvelope) {
	t.Helper()
	ctx := context.Background()
	s, service, grant, _ := mutationDispatchFixture(t, []Capability{"product_read"})

	events := []store.Event{
		{EventID: "domain-evidence-work-2", Kind: "work.created", SubjectType: store.SubjectWorkItem, SubjectID: "work-2", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 2, Payload: json.RawMessage(`{"work_kind":"task","title":"Second work","priority":2}`)},
		{EventID: "domain-evidence-work-2-membership", Kind: "work.memberships_replaced", SubjectType: store.SubjectWorkItem, SubjectID: "work-2", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"memberships":[{"project_id":"project-1","role":"primary"}],"expected_version":1,"resulting_version":2}`)},
	}
	if err := store.ApplyOperation(ctx, s, store.Operation{Events: events, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectWorkItem, "work-2"): 0}}); err != nil {
		t.Fatal(err)
	}

	repo := domainEvidenceRepo(t)
	home := store.KnowledgeHome{HomeProjectID: "project-1", HomeLocatorID: "domain-evidence-locator", RepoPath: repo, HeadRef: "HEAD"}
	execFold(t, s,
		fixtureStatement{"locator", `INSERT INTO project_locators(locator_id,project_id,kind,locator_value,normalized_value,created_at,updated_at) VALUES('domain-evidence-locator','project-1','canonical_path',?,?,'now','now')`, []any{repo, repo}},
		fixtureStatement{"knowledge home", `INSERT INTO product_knowledge_homes(product_id,project_id,locator_id) VALUES('product-1','project-1','domain-evidence-locator')`, nil},
	)
	if err := s.RebuildKnowledgeIndex(ctx, home); err != nil {
		t.Fatalf("rebuild knowledge index: %v", err)
	}

	actorRef := store.DeriveWorkflowActorRef("human-1", "client-1", "agent-1", "session-1")
	contract := `INSERT INTO workflow_contracts(work_id,contract_version,premise,outcome_kind,outcome_payload,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class) VALUES(?,1,'domain evidence','check','{"kind":"check"}','internal_sqlite','[]','[]','now',?,'[]','["CD-0041"]',1,'prototype_internal')`
	execFold(t, s,
		fixtureStatement{"actor", `INSERT INTO workflow_actors(actor_ref,principal_ref,client_ref,agent_ref,session_ref,actor_class,first_seen_at) VALUES(?,'human-1','client-1','agent-1','session-1','operator','now')`, []any{actorRef}},
		fixtureStatement{"work-1 contract", contract, []any{"work-1", actorRef}},
		fixtureStatement{"work-2 contract", contract, []any{"work-2", actorRef}},
		fixtureStatement{"bindings", `INSERT INTO workflow_architecture_bindings(work_id,contract_version,product_id,domain_registry_content_hash,home_domain_id,projection_hash) SELECT 'work-1',1,'product-1',content_hash,?,content_hash FROM domain_registries WHERE product_id='product-1' UNION ALL SELECT 'work-2',1,'product-1',content_hash,?,content_hash FROM domain_registries WHERE product_id='product-1'`, []any{singleDomainRootID, singleDomainRootID}},
		fixtureStatement{"affected domains", `INSERT INTO workflow_contract_affected_domains(work_id,contract_version,domain_id) VALUES('work-1',1,?),('work-2',1,?)`, []any{singleDomainRootID, singleDomainRootID}},
		fixtureStatement{"law modifications", `INSERT INTO workflow_contract_law_modifications(work_id,contract_version,law_id) VALUES('work-1',1,'CD-0041'),('work-2',1,'CD-0041')`, nil},
	)

	if err := store.ReplaceDomainProjectAttachments(ctx, s, store.DomainProjectAttachmentsRequest{EventID: "domain-evidence-project-edges", ProductID: "product-1", DomainID: singleDomainRootID, ExpectedVersion: 0, Attachments: []store.DomainProjectAttachment{{ProjectID: "project-1", Role: "primary"}}, Actor: "operator", OccurredAt: fixedTime()}); err != nil {
		t.Fatalf("seed Project attachments: %v", err)
	}

	return s, service, mutationEnvelope(grant, scopeVersionForProject(t, s, "project-1"))
}

// domainRegistryAbsentFixture seeds the same in-scope Product without ever
// projecting a Domain registry for it, which is the state the read surface must
// keep distinguishable from an authoritative empty answer.
func domainRegistryAbsentFixture(t *testing.T) (*store.Store, *Service, CallEnvelope) {
	t.Helper()
	s, service, grant, _ := mutationDispatchFixture(t, []Capability{"product_read"})
	return s, service, mutationEnvelope(grant, scopeVersionForProject(t, s, "project-1"))
}

type fixtureStatement struct {
	name  string
	query string
	args  []any
}

// execFold applies raw projection seeding inside one fold-guarded transaction.
// The guard is what the projection tables require for direct writes.
func execFold(t *testing.T, s *store.Store, statements ...fixtureStatement) {
	t.Helper()
	ctx := context.Background()
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	all := append([]fixtureStatement{{"fold guard", `INSERT INTO fold_guard(active) VALUES(1)`, nil}}, statements...)
	all = append(all, fixtureStatement{"leave fold", `DELETE FROM fold_guard`, nil})
	for _, statement := range all {
		if _, err := tx.ExecContext(ctx, statement.query, statement.args...); err != nil {
			tx.Rollback()
			t.Fatalf("seed %s: %v", statement.name, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

// domainEvidenceRepo commits a knowledge manifest declaring exactly one root
// Domain with an empty architecture relation set, plus an accepted decision
// homed to that Domain and the superseded decision it replaced.
func domainEvidenceRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	runRuntimeGit(t, repo, "init", "--quiet", "--initial-branch=main")
	runRuntimeGit(t, repo, "config", "user.email", "test@example.invalid")
	runRuntimeGit(t, repo, "config", "user.name", "Concord Test")

	accepted := "Domains are the only architecture authority.\n"
	superseded := "Components were the prior architecture authority.\n"
	writeRepoFile(t, repo, "docs/decisions/CD-0041.md", accepted)
	writeRepoFile(t, repo, "docs/decisions/CD-0002.md", superseded)

	manifest := store.KnowledgeManifest{
		SchemaVersion:  "1.2",
		SupportedKinds: []string{"decision"},
		IndexedKinds:   []string{"decision"},
		DomainRegistry: store.KnowledgeDomainRegistry{
			SchemaVersion: "1.0",
			ProductKey:    "concord",
			RootDomainID:  singleDomainRootID,
			Domains: []store.KnowledgeDomain{
				{DomainID: singleDomainRootID, Name: "Concord", Purpose: "Product law", Status: "current", ArchitectureRelations: []store.KnowledgeArchitectureRelation{}},
			},
		},
		Records: []store.KnowledgeRecord{
			{
				ID: "CD-0041", Kind: "decision", Path: "docs/decisions/CD-0041.md", Status: "accepted", Date: "2026-08-18T00:00:00Z",
				Title: "Domain authority", Summary: "Domains carry architecture authority", Tags: []string{},
				LawRelations: []store.KnowledgeRelation{{Kind: "supersedes", TargetID: "CD-0002"}},
				Scopes:       store.KnowledgeRecordScopes{Mode: "explicit", ProductIDs: []string{}, ProjectIDs: []string{}, DomainIDs: []string{singleDomainRootID}, TagIDs: []string{}},
				HomeDomainID: singleDomainRootID, SHA256: contentDigest(accepted),
			},
			{
				ID: "CD-0002", Kind: "decision", Path: "docs/decisions/CD-0002.md", Status: "superseded", Date: "2026-08-17T00:00:00Z",
				Title: "Component authority", Summary: "Retired architecture authority", Tags: []string{}, Successor: "CD-0041",
				Scopes:       store.KnowledgeRecordScopes{Mode: "explicit", ProductIDs: []string{}, ProjectIDs: []string{}, DomainIDs: []string{singleDomainRootID}, TagIDs: []string{}},
				HomeDomainID: singleDomainRootID, SHA256: contentDigest(superseded),
			},
		},
	}
	writeRepoFile(t, repo, "docs/concord-knowledge-index.v1.json", encodeDomainManifest(t, manifest))
	runRuntimeGit(t, repo, "add", "--", ".")
	runRuntimeGit(t, repo, "commit", "--quiet", "-m", "domain evidence knowledge")
	return repo
}

// encodeDomainManifest drops the retired component_ids scope key, which the
// v1.2 manifest schema rejects, from the marshalled record scopes.
func encodeDomainManifest(t *testing.T, manifest store.KnowledgeManifest) string {
	t.Helper()
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		t.Fatal(err)
	}
	for _, record := range object["records"].([]any) {
		delete(record.(map[string]any)["scopes"].(map[string]any), "component_ids")
	}
	encoded, err := json.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded) + "\n"
}

func writeRepoFile(t *testing.T, repo, path, content string) {
	t.Helper()
	full := filepath.Join(repo, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func contentDigest(content string) string {
	sum := sha256.Sum256([]byte(content))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// domainRead dispatches one concord_domain read across the agent plane and
// requires it to succeed, so every caller asserts against a real envelope.
func domainRead(t *testing.T, s *store.Store, service *Service, env CallEnvelope, operation, input string) Envelope {
	t.Helper()
	response, err := Dispatch(context.Background(), s, service, InvokeRequest{Tool: "concord_domain", Operation: operation, Input: json.RawMessage(input)}, env)
	if err != nil {
		t.Fatalf("concord_domain.%s dispatch: %v", operation, err)
	}
	if response.Outcome != OutcomeOK {
		t.Fatalf("concord_domain.%s outcome=%s error=%#v", operation, response.Outcome, response.Error)
	}
	return response
}

// assertAuthoritativeCoverage binds the two carriers the floor requirement
// names: the envelope's explicit authority, and the registry watermark that
// says which projected registry commit the answer covers.
func assertAuthoritativeCoverage(t *testing.T, response Envelope, operation string) store.DomainRegistryView {
	t.Helper()
	if response.Authority != Authority("authoritative") {
		t.Fatalf("concord_domain.%s authority=%q, want %q", operation, response.Authority, "authoritative")
	}
	var payload struct {
		Registry *store.DomainRegistryView `json:"registry"`
	}
	if err := json.Unmarshal(response.Result, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Registry == nil {
		t.Fatalf("concord_domain.%s omitted the registry coverage watermark: %s", operation, response.Result)
	}
	if payload.Registry.RootDomainID != singleDomainRootID || payload.Registry.ProductKey != "concord" {
		t.Fatalf("concord_domain.%s registry identity = %#v", operation, payload.Registry)
	}
	if !strings.HasPrefix(payload.Registry.ContentHash, "sha256:") || payload.Registry.ScannedCommit == "" {
		t.Fatalf("concord_domain.%s coverage watermark is not Git-anchored: %#v", operation, payload.Registry)
	}
	return *payload.Registry
}

// Element 1: bounded Domain identity. The list read names the Product's current
// Domains, flags the architectural home, and carries authority plus coverage.
func TestAgentDomainListReturnsBoundedIdentityWithAuthorityAndCoverage(t *testing.T) {
	s, service, env := domainEvidenceFixture(t)
	response := domainRead(t, s, service, env, "list", `{"product_id":"product-1","page":{"cursor":null,"limit":20}}`)
	assertAuthoritativeCoverage(t, response, "list")

	var payload struct {
		Domains []store.DomainSummary `json:"domains"`
	}
	if err := json.Unmarshal(response.Result, &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Domains) != 1 {
		t.Fatalf("single-Domain Product listed %d Domains: %#v", len(payload.Domains), payload.Domains)
	}
	only := payload.Domains[0]
	if only.DomainID != singleDomainRootID || only.Name != "Concord" || only.Purpose != "Product law" {
		t.Fatalf("Domain identity is not the seeded root: %#v", only)
	}
	if !only.HomeDomain || only.Status != "current" || only.ParentID != "" {
		t.Fatalf("root Domain is not reported as the current parentless home: %#v", only)
	}
	if response.QueryID != "C22.DomainList" {
		t.Fatalf("query id = %q", response.QueryID)
	}
}

// Elements 2, 5 and 6: current law, its Git evidence, and decision records.
// Superseded law is absent, so "current" is a real filter rather than a label.
func TestAgentDomainDetailReturnsCurrentLawDecisionsAndGitEvidence(t *testing.T) {
	s, service, env := domainEvidenceFixture(t)
	response := domainRead(t, s, service, env, "detail", `{"product_id":"product-1","domain_id":"`+singleDomainRootID+`"}`)
	registry := assertAuthoritativeCoverage(t, response, "detail")

	var payload struct {
		Domain     store.DomainSummary     `json:"domain"`
		CurrentLaw []store.DomainLawRecord `json:"current_law"`
	}
	if err := json.Unmarshal(response.Result, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Domain.DomainID != singleDomainRootID || !payload.Domain.HomeDomain {
		t.Fatalf("detail identity is not the seeded home Domain: %#v", payload.Domain)
	}
	if len(payload.CurrentLaw) != 1 {
		t.Fatalf("current law = %d records, want the one accepted decision (superseded law must be absent): %#v", len(payload.CurrentLaw), payload.CurrentLaw)
	}
	law := payload.CurrentLaw[0]
	if law.LawID != "CD-0041" {
		t.Fatalf("current law is not the accepted seeded decision: %#v", law)
	}
	if law.Kind != "decision" || law.Title != "Domain authority" {
		t.Fatalf("decision record is not typed as a decision: %#v", law)
	}
	if law.Path != "docs/decisions/CD-0041.md" {
		t.Fatalf("law path = %q", law.Path)
	}
	if law.ContentHash != contentDigest("Domains are the only architecture authority.\n") {
		t.Fatalf("law evidence hash does not match the committed blob: %#v", law)
	}
	if law.ScannedCommit == "" || law.ScannedCommit != registry.ScannedCommit {
		t.Fatalf("law evidence commit %q is not the registry's scanned commit %q", law.ScannedCommit, registry.ScannedCommit)
	}
	// The manifest refuses a law whose applicability repeats its own home, so a
	// single-Domain Product has no fan-out beyond the home Domain that carries
	// the law. The read reports that as an empty set, not as a missing answer.
	if len(law.AppliesTo) != 0 {
		t.Fatalf("single-Domain law reported applicability beyond its home: %#v", law.AppliesTo)
	}
}

// Element 3, and the defect this row exists for. A single-Domain Product has no
// architecture relations because a self-edge is refused by schema, so the read
// must report an authoritative empty set. The contrast case proves the surface
// distinguishes that from a Product whose registry was never projected, which
// is a typed error carrying no authority and no coverage at all.
func TestAgentDomainRelationsAreAuthoritativeEmptyNotUnavailable(t *testing.T) {
	s, service, env := domainEvidenceFixture(t)
	response := domainRead(t, s, service, env, "detail", `{"product_id":"product-1","domain_id":"`+singleDomainRootID+`"}`)
	registry := assertAuthoritativeCoverage(t, response, "detail")

	var payload struct {
		CurrentLaw []store.DomainLawRecord     `json:"current_law"`
		Relations  *[]store.DomainRelationView `json:"relations"`
	}
	if err := json.Unmarshal(response.Result, &payload); err != nil {
		t.Fatal(err)
	}
	// Non-vacuity: the same envelope carries seeded law and a Git-anchored
	// registry, so the empty relation set is a read of populated state.
	if len(payload.CurrentLaw) != 1 || registry.ContentHash == "" {
		t.Fatalf("relation emptiness was read from an unpopulated registry: law=%#v registry=%#v", payload.CurrentLaw, registry)
	}
	if payload.Relations == nil {
		t.Fatalf("relations key is absent; an authoritative empty set must be an explicit empty array: %s", response.Result)
	}
	if len(*payload.Relations) != 0 {
		t.Fatalf("single-Domain Product reported %d architecture relations: %#v", len(*payload.Relations), *payload.Relations)
	}
	if response.Authority != Authority("authoritative") {
		t.Fatalf("empty relation set was not reported as authoritative: authority=%q", response.Authority)
	}
	if response.Error != nil {
		t.Fatalf("empty relation set surfaced as an error: %#v", response.Error)
	}

	// The contrast: the same in-scope Product, with no registry ever projected,
	// cannot answer at all. Reusing the Product keeps grant scope satisfied, so
	// the difference measured here is registry coverage and nothing else.
	absentStore, absentService, absentEnv := domainRegistryAbsentFixture(t)
	absent, err := Dispatch(context.Background(), absentStore, absentService, InvokeRequest{Tool: "concord_domain", Operation: "detail", Input: json.RawMessage(`{"product_id":"product-1","domain_id":"` + singleDomainRootID + `"}`)}, absentEnv)
	if err != nil {
		t.Fatal(err)
	}
	if absent.Outcome == OutcomeOK {
		t.Fatalf("absent registry answered as if authoritative: %s", absent.Result)
	}
	if absent.Error == nil || absent.Error.Kind != "unknown_scope" {
		t.Fatalf("absent registry was not typed as an unresolvable scope: %#v", absent.Error)
	}
	if len(absent.Result) != 0 {
		t.Fatalf("absent registry returned a result body: %s", absent.Result)
	}
	// Both answers are authoritative statements about the Product; a typed
	// "no registry" is as authoritative as an empty relation set. Coverage is
	// what separates them: only the projected registry yields a watermark, so
	// an empty relation set can never be mistaken for an unreadable one.
	if response.Outcome == absent.Outcome {
		t.Fatalf("authoritative-empty and absent-registry share outcome %q", response.Outcome)
	}
	var absentPayload struct {
		Registry *store.DomainRegistryView `json:"registry"`
	}
	_ = json.Unmarshal(absent.Result, &absentPayload)
	if absentPayload.Registry != nil {
		t.Fatalf("absent registry reported coverage: %#v", absentPayload.Registry)
	}
	if registry.ContentHash == "" {
		t.Fatal("the authoritative-empty answer carried no coverage watermark to contrast against")
	}

	// The list read takes no Domain argument, so it cannot fail for any reason
	// except missing registry coverage. If an unprojected registry degraded to
	// an empty view it would answer OK here with an empty Domain set, which is
	// byte-for-byte the shape a genuinely empty registry would produce. Refusing
	// that answer is what keeps authoritative-empty and unreadable distinct.
	absentList, err := Dispatch(context.Background(), absentStore, absentService, InvokeRequest{Tool: "concord_domain", Operation: "list", Input: json.RawMessage(`{"product_id":"product-1","page":{"cursor":null,"limit":20}}`)}, absentEnv)
	if err != nil {
		t.Fatal(err)
	}
	if absentList.Outcome == OutcomeOK {
		t.Fatalf("unprojected registry answered the Domain list as if it were an authoritative empty Product: %s", absentList.Result)
	}
	if absentList.Error == nil || absentList.Error.Kind != "unknown_scope" {
		t.Fatalf("unprojected registry was not typed at the Domain list: %#v", absentList.Error)
	}
	if len(absentList.Result) != 0 {
		t.Fatalf("unprojected registry returned a Domain list body: %s", absentList.Result)
	}
}

// Element 4: active work bound to the Domain, distinguishing the architectural
// home from mere footprint.
func TestAgentDomainActiveWorkReturnsDomainBoundWorkWithCoverage(t *testing.T) {
	s, service, env := domainEvidenceFixture(t)
	response := domainRead(t, s, service, env, "active_work", `{"product_id":"product-1","domain_id":"`+singleDomainRootID+`","page":{"cursor":null,"limit":20}}`)
	assertAuthoritativeCoverage(t, response, "active_work")

	var payload struct {
		Work []store.DomainActiveWorkItem `json:"work"`
	}
	if err := json.Unmarshal(response.Result, &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Work) != 2 {
		t.Fatalf("active Domain work = %d items, want the two seeded contracts: %#v", len(payload.Work), payload.Work)
	}
	seen := map[string]store.DomainActiveWorkItem{}
	for _, item := range payload.Work {
		seen[item.WorkID] = item
	}
	for _, id := range []string{"work-1", "work-2"} {
		item, ok := seen[id]
		if !ok {
			t.Fatalf("active work omitted %s: %#v", id, payload.Work)
		}
		if !item.HomeDomain {
			t.Fatalf("%s is homed at the root Domain but was not flagged: %#v", id, item)
		}
		if item.ContractVersion != 1 || item.Lifecycle == "" || item.Kind == "" {
			t.Fatalf("%s carries no bounded contract identity: %#v", id, item)
		}
	}
	if seen["work-1"].Title != "Work" || seen["work-2"].Title != "Second work" {
		t.Fatalf("active work titles are not the seeded ones: %#v", payload.Work)
	}
}

// Element 7: typed local attachments with their optimistic set version.
func TestAgentDomainAttachmentsReturnTypedLocalEdgesWithCoverage(t *testing.T) {
	s, service, env := domainEvidenceFixture(t)
	response := domainRead(t, s, service, env, "attachments", `{"product_id":"product-1","domain_id":"`+singleDomainRootID+`"}`)
	assertAuthoritativeCoverage(t, response, "attachments")

	var payload struct {
		Domain      store.DomainSummary        `json:"domain"`
		Attachments store.DomainAttachmentView `json:"attachments"`
	}
	if err := json.Unmarshal(response.Result, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Domain.DomainID != singleDomainRootID {
		t.Fatalf("attachments were reported for %q", payload.Domain.DomainID)
	}
	if len(payload.Attachments.ProjectEdges) != 1 {
		t.Fatalf("Project attachments = %d edges, want the one seeded edge: %#v", len(payload.Attachments.ProjectEdges), payload.Attachments.ProjectEdges)
	}
	edge := payload.Attachments.ProjectEdges[0]
	if edge.ProjectID != "project-1" || edge.Role != "primary" {
		t.Fatalf("Project attachment edge is not the seeded one: %#v", edge)
	}
	if payload.Attachments.ProjectVersion != 1 {
		t.Fatalf("Project attachment set version = %d, want the version the seeding replace produced", payload.Attachments.ProjectVersion)
	}
}

// Element 8: unresolved architecture overlap between the Domain-bound contracts,
// reported with its resolution state rather than silently omitted.
func TestAgentDomainOverlapsReturnUnresolvedPairsWithCoverage(t *testing.T) {
	s, service, env := domainEvidenceFixture(t)
	response := domainRead(t, s, service, env, "overlaps", `{"product_id":"product-1","domain_id":"`+singleDomainRootID+`"}`)
	assertAuthoritativeCoverage(t, response, "overlaps")

	var payload struct {
		Pairs     []store.DomainOverlapPair `json:"pairs"`
		Truncated bool                      `json:"truncated"`
	}
	if err := json.Unmarshal(response.Result, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Truncated {
		t.Fatal("two seeded contracts cannot exceed the overlap bound")
	}
	if len(payload.Pairs) != 1 {
		t.Fatalf("overlap pairs = %d, want the one seeded unresolved pair: %#v", len(payload.Pairs), payload.Pairs)
	}
	pair := payload.Pairs[0]
	if pair.FromWorkID != "work-1" || pair.ToWorkID != "work-2" {
		t.Fatalf("overlap pair endpoints are not the seeded contracts: %#v", pair)
	}
	if len(pair.SharedDomainIDs) != 1 || pair.SharedDomainIDs[0] != singleDomainRootID {
		t.Fatalf("overlap shared Domains = %#v", pair.SharedDomainIDs)
	}
	if len(pair.SharedLawIDs) != 1 || pair.SharedLawIDs[0] != "CD-0041" {
		t.Fatalf("overlap shared law = %#v", pair.SharedLawIDs)
	}
	if pair.ResolutionState != "absent" || pair.ResolutionKind != "" {
		t.Fatalf("seeded pair has no operator resolution but reported state %q kind %q", pair.ResolutionState, pair.ResolutionKind)
	}
}

package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/pm1fixture"
	"github.com/sharper-flow/concord/internal/store"
)

// singleDomainRootID mirrors the accepted registry shape: Concord is a
// single-Domain Product whose one root Domain has no children, so the
// architecture relation set is legitimately empty.
const singleDomainRootID = pm1fixture.SingleDomainRootID

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

	if err := pm1fixture.SeedWorkItem(ctx, s, "project-1", "work-2", "Second work", 2); err != nil {
		t.Fatal(err)
	}
	options := pm1fixture.DomainEvidenceOptions{Dir: t.TempDir(), ProductID: "product-1", ProjectID: "project-1", LocatorID: "domain-evidence-locator", WorkIDs: []string{"work-1", "work-2"}}
	if err := pm1fixture.SeedDomainEvidence(ctx, s, options); err != nil {
		t.Fatal(err)
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
	if response.Authority != AuthorityLevel("authoritative") {
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
	if law.ContentHash != pm1fixture.ContentDigest(pm1fixture.DomainEvidenceLawBody) {
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
	if response.Authority != AuthorityLevel("authoritative") {
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

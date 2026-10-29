package pm1fixture

import (
	"context"
	"fmt"

	"github.com/sharper-flow/concord/internal/store"
)

// This file carries the maximally populated Domain fixture. Where the
// single-Domain evidence fixture holds Concord's own registry shape, this one
// exists so every optional field a Domain read can carry is non-zero at once: a
// child Domain with a parent, an architecture relation with governing law, a law
// with applicability beyond its home. A read of this state exercises every
// declared property rather than the subset a minimal Product populates.

const (
	// PayloadRootDomainID is the registry root and the child Domain's parent.
	PayloadRootDomainID = "product-root:payload"
	// PayloadChildDomainID is the Domain every populated read is taken against.
	PayloadChildDomainID = "sync"
	// PayloadLawID is the accepted decision homed to the child Domain.
	PayloadLawID = "CD-0101"

	payloadProductKey = "payload"
	payloadLawPath    = ".concord/docs/decisions/CD-0101.md"
	payloadLawBody    = "The child Domain owns synchronization law.\n"
)

// DomainPayloadEvidenceOptions names the identities the populated registry binds
// to. Every field is required.
type DomainPayloadEvidenceOptions struct {
	// Dir is a caller-owned directory that receives the manifest Git repo.
	Dir string
	// ProductID owns the projected registry and the managed resource.
	ProductID string
	// ProjectID is the knowledge home Project and the attached Project.
	ProjectID string
	// LocatorID names the canonical-path locator for that home.
	LocatorID string
	// WorkIDs are existing work items bound to the child Domain. Two or more
	// produce an unresolved overlap pair.
	WorkIDs []string
}

func (o DomainPayloadEvidenceOptions) validate() error {
	missing := ""
	switch {
	case o.Dir == "":
		missing = "Dir"
	case o.ProductID == "":
		missing = "ProductID"
	case o.ProjectID == "":
		missing = "ProjectID"
	case o.LocatorID == "":
		missing = "LocatorID"
	case len(o.WorkIDs) < 2:
		missing = "WorkIDs"
	}
	if missing != "" {
		return fmt.Errorf("pm1fixture: DomainPayloadEvidenceOptions.%s is required", missing)
	}
	return nil
}

// SeedDomainPayloadEvidence projects the two-Domain registry for opts.ProductID,
// binds a nonterminal contract per work item to the child Domain.
// The Product and Project must already exist;
// SeedProductAndProject creates them at the version this fixture expects.
func SeedDomainPayloadEvidence(ctx context.Context, s *store.Store, opts DomainPayloadEvidenceOptions) error {
	if err := opts.validate(); err != nil {
		return err
	}
	repo, err := domainPayloadRepo(opts.Dir)
	if err != nil {
		return err
	}
	if err := execDomainFold(ctx, s,
		domainStatement{"knowledge locator", `INSERT INTO project_locators(locator_id,project_id,kind,locator_value,normalized_value,created_at,updated_at) VALUES(?,?,'canonical_path',?,?,'now','now')`, []any{opts.LocatorID, opts.ProjectID, repo, repo}},
		domainStatement{"knowledge home", `INSERT INTO product_knowledge_homes(product_id,project_id,locator_id) VALUES(?,?,?)`, []any{opts.ProductID, opts.ProjectID, opts.LocatorID}},
	); err != nil {
		return err
	}
	home := store.KnowledgeHome{HomeProjectID: opts.ProjectID, HomeLocatorID: opts.LocatorID, RepoPath: repo, HeadRef: "HEAD"}
	if err := s.RebuildKnowledgeIndex(ctx, home); err != nil {
		return fmt.Errorf("pm1fixture: rebuild populated knowledge index: %w", err)
	}

	actorRef := store.DeriveWorkflowActorRef("human-1", "client-1", "agent-1", "session-1")
	statements := []domainStatement{
		{"workflow actor", `INSERT INTO workflow_actors(actor_ref,principal_ref,client_ref,agent_ref,session_ref,actor_class,first_seen_at) VALUES(?,'human-1','client-1','agent-1','session-1','operator','now')`, []any{actorRef}},
	}
	const contract = `INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class) VALUES(?,1,'populated domain payload','internal_sqlite','[]','[]','now',?,'[]','["` + PayloadLawID + `"]',1,'prototype_internal')`
	const binding = `INSERT INTO workflow_architecture_bindings(work_id,contract_version,product_id,domain_registry_content_hash,home_domain_id,projection_hash) SELECT ?,1,?,content_hash,?,content_hash FROM domain_registries WHERE product_id=?`
	for _, workID := range opts.WorkIDs {
		statements = append(statements,
			domainStatement{"contract " + workID, contract, []any{workID, actorRef}},
			domainStatement{"architecture binding " + workID, binding, []any{workID, opts.ProductID, PayloadChildDomainID, opts.ProductID}},
			domainStatement{"affected Domain " + workID, `INSERT INTO workflow_contract_affected_domains(work_id,contract_version,domain_id) VALUES(?,1,?)`, []any{workID, PayloadChildDomainID}},
			domainStatement{"law modification " + workID, `INSERT INTO workflow_contract_law_modifications(work_id,contract_version,law_id) VALUES(?,1,?)`, []any{workID, PayloadLawID}},
		)
	}
	if err := execDomainFold(ctx, s, statements...); err != nil {
		return err
	}

	return nil
}

// domainPayloadRepo commits a manifest declaring a root Domain and one child
// that depends on it under a governing law, plus the accepted decision homed to
// the child with applicability out to the root.
func domainPayloadRepo(dir string) (string, error) {
	repo, err := initKnowledgeRepo(dir)
	if err != nil {
		return "", fmt.Errorf("pm1fixture: init populated Domain repo: %w", err)
	}
	if err := writeKnowledgeFile(repo, payloadLawPath, payloadLawBody); err != nil {
		return "", fmt.Errorf("pm1fixture: write populated accepted decision: %w", err)
	}
	manifest := store.KnowledgeManifest{
		SchemaVersion:  "1.2",
		SupportedKinds: []string{"decision"},
		IndexedKinds:   []string{"decision"},
		DomainRegistry: store.KnowledgeDomainRegistry{
			SchemaVersion: "1.0",
			ProductKey:    payloadProductKey,
			RootDomainID:  PayloadRootDomainID,
			Domains: []store.KnowledgeDomain{
				{DomainID: PayloadRootDomainID, Name: "Payload", Purpose: "Product law", Status: "current", ArchitectureRelations: []store.KnowledgeArchitectureRelation{}},
				{
					DomainID: PayloadChildDomainID, Name: "Sync", Purpose: "Synchronization", Status: "current", ParentDomainID: PayloadRootDomainID,
					ArchitectureRelations: []store.KnowledgeArchitectureRelation{{Kind: "depends_on", TargetDomainID: PayloadRootDomainID, GoverningLawIDs: []string{PayloadLawID}}},
				},
			},
		},
		Records: []store.KnowledgeRecord{
			{
				ID: PayloadLawID, Kind: "decision", Path: payloadLawPath, Status: "accepted", Date: "2026-08-18T00:00:00Z",
				Title: "Synchronization authority", Summary: "The child Domain owns synchronization law", Tags: []string{},
				Authority:    store.KnowledgeAuthority{Tier: "legislated", LegislatedBy: "work-0f1e2d3c4b5a6978", ContractVersion: 1},
				Scopes:       store.KnowledgeRecordScopes{Mode: "explicit", ProductIDs: []string{}, ProjectIDs: []string{}, DomainIDs: []string{PayloadChildDomainID}, TagIDs: []string{}},
				HomeDomainID: PayloadChildDomainID, AppliesToDomainIDs: []string{PayloadRootDomainID}, SHA256: ContentDigest(payloadLawBody),
			},
		},
	}
	if err := writeKnowledgeShards(repo, manifest); err != nil {
		return "", err
	}
	if _, err := commitKnowledgeRepo(repo, "populated domain payload knowledge"); err != nil {
		return "", fmt.Errorf("pm1fixture: commit populated Domain repo: %w", err)
	}
	return repo, nil
}

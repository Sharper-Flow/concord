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
	// ProductID owns the projected registry.
	ProductID string
	// ProjectID is the knowledge home Project.
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
	return seedDomainEvidence(ctx, s, DomainEvidenceOptions(opts), repo, PayloadChildDomainID, PayloadLawID, "populated domain payload")
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

package store

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
)

// UnmarshalJSON keeps a required reading's source union closed even on internal
// replay paths that do not pass through the public authoring
// schema. The closure belongs to the reading entry: oracle law and recipe
// sources share the source type and keep their own closed-arm validators.
func (reading *WorkContextReading) UnmarshalJSON(raw []byte) error {
	var decoded struct {
		DomainID             string          `json:"domain_id"`
		Reason               string          `json:"reason"`
		ProductWideRationale string          `json:"product_wide_rationale,omitempty"`
		Source               json.RawMessage `json:"source"`
	}
	if err := decodeStrictKnowledgeJSON(raw, &decoded); err != nil {
		return err
	}
	source, err := decodeClosedReadingSource(decoded.Source)
	if err != nil {
		return err
	}
	*reading = WorkContextReading{DomainID: decoded.DomainID, Reason: decoded.Reason, ProductWideRationale: decoded.ProductWideRationale, Source: source}
	return nil
}

func decodeClosedReadingSource(raw []byte) (WorkContextReadingSource, error) {
	var decoded WorkContextReadingSource
	if err := decodeStrictKnowledgeJSON(raw, &decoded); err != nil {
		return decoded, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return decoded, err
	}
	allowed := []string{"kind"}
	switch decoded.Kind {
	case WorkContextSourceRepositoryFile:
		allowed = append(allowed, "project_id", "path", "commit_oid")
	case WorkContextSourceKnowledge:
		allowed = append(allowed, "source_id", "law_id", "content_hash")
	case WorkContextSourceDomainShape:
		allowed = append(allowed, "source_id", "commit_oid", "content_hash", "selector")
		var selector map[string]json.RawMessage
		if err := json.Unmarshal(fields["selector"], &selector); err != nil || len(selector) != 1 {
			return decoded, fmt.Errorf("domain_shape requires exactly one selector field")
		}
		for field := range selector {
			if field != "path" && field != "domain_id" && field != "subject" {
				return decoded, fmt.Errorf("domain_shape selector forbids %s", field)
			}
		}
	default:
		return decoded, fmt.Errorf("unknown work-context reading source kind %q", decoded.Kind)
	}
	for field := range fields {
		if !slices.Contains(allowed, field) {
			return decoded, fmt.Errorf("reading source kind %q forbids %s", decoded.Kind, field)
		}
	}
	return decoded, nil
}

func validateDomainShapeReadingSource(source WorkContextReadingSource) error {
	if source.ProjectID != "" || source.Path != "" || source.LawID != "" ||
		len(source.SourceID) < 2 || len(source.SourceID) > workContextProjectIDMaxBytes ||
		!workContextCommitOID(source.CommitOID) || !workflowDigest(source.ContentHash) || source.Selector == nil {
		return workContextFailure(KindInvalidPayload, "work_context", "domain_shape requires only source_id, commit_oid, content_hash and an explicit selector", "pin the selected Product home, commit and exact selected JSON hash")
	}
	return validateDomainShapeSelector(*source.Selector)
}

const domainShapeReadingOp = "work_context_reading"

type domainShapeReadingWitness struct {
	arm, identity                            string
	sourceProject, sourceLocator, sourceRepo string
	commitOID, contentHash, sourceSetDigest  string
	shape                                    *DomainShapeResult
	shapeDomain                              string
	locatorCreatedAt, locatorUpdatedAt       string
	projectVersion                           int64
	watermarkCommit, watermarkDigest         string
	watermarkVersion                         int64
	watermarkComplete                        bool
}

// sampleDomainShapeReading uses the normal selected-read core and samples SQL
// only. Committed-content proof is separate from this query.
func sampleDomainShapeReading(ctx context.Context, q queryer, product string, reading WorkContextReading) (domainShapeReadingWitness, error) {
	var w domainShapeReadingWitness
	if err := validateWorkContextReading(reading); err != nil {
		return w, err
	}
	home, err := resolveKnowledgeQueryHome(ctx, q, product, "", KnowledgeHome{}, domainShapeReadingOp)
	if err != nil {
		return w, err
	}
	if home.HomeProjectID != reading.Source.SourceID {
		return w, newFailure(KindUnknownScope, domainShapeReadingOp, "domain_shape source_id is not the work Product's designated home Project", false, "select the work Product's designated home")
	}
	sources, err := resolveKnowledgeQuerySources(ctx, q, product, domainShapeReadingOp)
	if err != nil {
		return w, err
	}
	if !slices.ContainsFunc(sources, func(source KnowledgeHome) bool {
		return source.HomeProjectID == home.HomeProjectID && source.HomeLocatorID == home.HomeLocatorID
	}) {
		return w, newFailure(KindUnknownScope, domainShapeReadingOp, "domain_shape home is outside the Product knowledge-source set", false, "restore the Product home registration")
	}
	selected, err := selectedDomainShapeTx(ctx, q, home, product, *reading.Source.Selector)
	if err != nil {
		return w, err
	}
	raw, err := json.Marshal(selected)
	if err != nil {
		return w, err
	}
	if selected.KnowledgeCommit != reading.Source.CommitOID || domainShapeBytesHash(raw) != reading.Source.ContentHash {
		return w, newFailure(KindProjectionConflict, domainShapeReadingOp, "domain_shape selection does not match the pinned commit and exact JSON hash", false, "read and explicitly pin the current selection")
	}
	// A root grouping explicitly covers the Product. A child grouping cannot
	// conceal facts or mixed-root owners belonging to other affected Domains.
	var root string
	if err := q.QueryRowContext(ctx, `SELECT root_domain_id FROM domain_registries WHERE product_id=?`, product).Scan(&root); err != nil {
		return w, err
	}
	if reading.DomainID != root && (len(selected.DomainIDs) != 1 || selected.DomainIDs[0] != reading.DomainID) {
		return w, newFailure(KindUnknownScope, domainShapeReadingOp, "domain_shape selection does not fit the declared Domain group", false, "select one owning Domain or explicitly declare the approved Product-wide group")
	}
	w = domainShapeReadingWitness{arm: WorkContextSourceDomainShape, identity: home.HomeProjectID + "/domain_shape@" + reading.Source.CommitOID + "/" + reading.Source.ContentHash,
		sourceProject: home.HomeProjectID, sourceLocator: home.HomeLocatorID, sourceRepo: home.RepoPath,
		commitOID: selected.KnowledgeCommit, contentHash: reading.Source.ContentHash, sourceSetDigest: knowledgeSourceSetDigest(sources), shape: &selected, shapeDomain: reading.DomainID}
	if err := sampleDomainShapeSourceAuthority(ctx, q, home, product, &w); err != nil {
		return w, err
	}
	if err := q.QueryRowContext(ctx, `SELECT scanned_commit_oid,scanned_content_digest,projection_version,complete FROM knowledge_index_watermark WHERE home_project_id=? AND home_locator_id=? AND head_ref=?`, home.HomeProjectID, home.HomeLocatorID, home.HeadRef).Scan(&w.watermarkCommit, &w.watermarkDigest, &w.watermarkVersion, &w.watermarkComplete); err != nil {
		return w, err
	}
	return w, nil
}

func sampleDomainShapeSourceAuthority(ctx context.Context, q queryer, home KnowledgeHome, product string, w *domainShapeReadingWitness) error {
	if err := q.QueryRowContext(ctx, `SELECT created_at,updated_at FROM project_locators WHERE project_id=? AND locator_id=?`, home.HomeProjectID, home.HomeLocatorID).Scan(&w.locatorCreatedAt, &w.locatorUpdatedAt); err != nil {
		return err
	}
	if err := q.QueryRowContext(ctx, `SELECT version FROM projects WHERE id=?`, home.HomeProjectID).Scan(&w.projectVersion); err != nil {
		return err
	}
	var member bool
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM product_projects WHERE product_id=? AND project_id=?)`, product, home.HomeProjectID).Scan(&member); err != nil {
		return err
	}
	if !member {
		return newFailure(KindUnknownScope, domainShapeReadingOp, "domain_shape home Project is outside the work Product", false, "restore the Product's home Project membership")
	}
	return nil
}

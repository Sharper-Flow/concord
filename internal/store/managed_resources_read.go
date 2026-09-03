package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
)

// ResourcesRequest selects the C15 §5 bounded directions a read may take:
// Product → owned plus consumed resources (direction 1), or resource →
// owner plus consumers (direction 2). Exactly one of ProductID or ResourceID
// scopes the read; the filters narrow direction 1.
type ResourcesRequest struct {
	ProductID   string
	ResourceID  string
	Class       string
	Kind        string
	Environment string
	Limit       int
}

// ResourceProductLink is one Product's relationship to a resource.
type ResourceProductLink struct {
	ProductID    string   `json:"product_id"`
	Role         string   `json:"role"`
	Purpose      string   `json:"purpose"`
	Environments []string `json:"environments"`
}

// ResourceView is one canonical resource with its singular owner and bounded
// consumers. Metadata travels as its stored JSON text because the per-kind
// extension object is versioned by metadata_schema_version, not by this
// payload (PM3 §4).
type ResourceView struct {
	ResourceID              string                `json:"resource_id"`
	DisplayName             string                `json:"display_name"`
	Class                   string                `json:"class"`
	Kind                    string                `json:"kind"`
	Purpose                 string                `json:"purpose"`
	StageMaturity           string                `json:"stage_maturity"`
	StageAudienceCommitment string                `json:"stage_audience_commitment"`
	Environments            []string              `json:"environments"`
	LocatorAbsenceReason    string                `json:"locator_absence_reason,omitempty"`
	MetadataSchemaVersion   string                `json:"metadata_schema_version"`
	MetadataJSON            string                `json:"metadata_json"`
	Version                 int64                 `json:"version"`
	Owner                   ResourceProductLink   `json:"owner"`
	Consumers               []ResourceProductLink `json:"consumers"`
}

// ResourcesResult is the bounded resource page with its read meta.
type ResourcesResult struct {
	ResultMeta
	Resources []ResourceView `json:"resources"`
}

const resourcesQueryID = "C15.Resources"

// Resources answers the read-only inventory directions C15 §5 requires. It
// never infers live provider state and never mutates: identity is declared
// by the operator through the CLI, and agents read it (CD-0106 D4).
func (s *Store) Resources(ctx context.Context, req ResourcesRequest) (ResourcesResult, error) {
	var out ResourcesResult
	if (req.ProductID == "") == (req.ResourceID == "") {
		return out, newFailure(KindInvalidFilter, resourcesQueryID, "exactly one of product_id or resource_id scopes a resource read", false, "supply a Product or a resource identity")
	}
	if req.Limit < 1 || req.Limit > 100 {
		return out, newFailure(KindInvalidFilter, resourcesQueryID, "limit must be between 1 and 100", false, "supply a bounded limit")
	}
	if req.Class != "" && req.Class != "infrastructure" && req.Class != "saas" {
		return out, newFailure(KindInvalidFilter, resourcesQueryID, "unknown resource class "+req.Class, false, "use infrastructure or saas")
	}
	if req.Kind != "" && !validManagedResourceKind(req.Kind) {
		return out, newFailure(KindInvalidFilter, resourcesQueryID, "unknown resource kind "+req.Kind, false, "use a declared resource kind")
	}
	if strings.TrimSpace(req.Environment) != req.Environment || len(req.Environment) > 64 {
		return out, newFailure(KindInvalidFilter, resourcesQueryID, "environment filter must be a trimmed bounded name", false, "supply a declared environment")
	}
	tx, err := beginRead(ctx, s, resourcesQueryID)
	if err != nil {
		return out, err
	}
	defer func() { _ = tx.Rollback() }()
	resources, err := resourcesTx(ctx, tx, req)
	if err != nil {
		return out, err
	}
	meta, err := queryMeta(ctx, tx, resourcesQueryID, ResolvedScope{ProductID: req.ProductID}, []string{"resource_id"})
	if err != nil {
		return out, err
	}
	out.ResultMeta = meta
	out.Resources = resources
	return out, nil
}

func resourcesTx(ctx context.Context, tx *sql.Tx, req ResourcesRequest) ([]ResourceView, error) {
	where := []string{}
	args := []any{}
	if req.ResourceID != "" {
		where = append(where, "r.resource_id = ?")
		args = append(args, req.ResourceID)
	} else {
		where = append(where, "EXISTS (SELECT 1 FROM resource_products s WHERE s.resource_id = r.resource_id AND s.product_id = ?)")
		args = append(args, req.ProductID)
	}
	if req.Class != "" {
		where = append(where, "r.class = ?")
		args = append(args, req.Class)
	}
	if req.Kind != "" {
		where = append(where, "r.kind = ?")
		args = append(args, req.Kind)
	}
	if req.Environment != "" {
		where = append(where, "EXISTS (SELECT 1 FROM json_each(r.environments) e WHERE e.value = ?)")
		args = append(args, req.Environment)
	}
	args = append(args, req.Limit)
	const columns = `SELECT r.resource_id, r.display_name, r.class, r.kind, r.purpose, r.stage_maturity, r.stage_audience_commitment, r.environments, COALESCE(r.locator_absence_reason, ''), r.metadata_schema_version, r.metadata, r.version FROM managed_resources r WHERE `
	query := columns + strings.Join(where, " AND ") + ` ORDER BY r.resource_id LIMIT ?` //nolint:gosec // where holds only the fixed predicate fragments chosen above; every caller value stays parameter-bound.
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, resourcesQueryID, "cannot read managed resources", true, "retry once the database is readable", err)
	}
	defer rows.Close()
	resources := []ResourceView{}
	for rows.Next() {
		var view ResourceView
		var environments string
		if err := rows.Scan(&view.ResourceID, &view.DisplayName, &view.Class, &view.Kind, &view.Purpose, &view.StageMaturity, &view.StageAudienceCommitment, &environments, &view.LocatorAbsenceReason, &view.MetadataSchemaVersion, &view.MetadataJSON, &view.Version); err != nil {
			return nil, wrapFailure(KindUnavailable, resourcesQueryID, "cannot decode a managed resource", true, "retry once the database is readable", err)
		}
		if err := json.Unmarshal([]byte(environments), &view.Environments); err != nil {
			return nil, newFailure(KindInvariantViolation, resourcesQueryID, "resource environments are not a JSON array", false, "rebuild the projections from the log")
		}
		if view.Environments == nil {
			view.Environments = []string{}
		}
		view.Consumers = []ResourceProductLink{}
		resources = append(resources, view)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapFailure(KindUnavailable, resourcesQueryID, "cannot finish reading managed resources", true, "retry once the database is readable", err)
	}
	for i := range resources {
		if err := resourceLinksTx(ctx, tx, &resources[i]); err != nil {
			return nil, err
		}
	}
	return resources, nil
}

func resourceLinksTx(ctx context.Context, tx *sql.Tx, view *ResourceView) error {
	rows, err := tx.QueryContext(ctx, `SELECT product_id, role, purpose, environments FROM resource_products WHERE resource_id = ? ORDER BY CASE role WHEN 'owner' THEN 0 ELSE 1 END, product_id`, view.ResourceID)
	if err != nil {
		return wrapFailure(KindUnavailable, resourcesQueryID, "cannot read resource Product links", true, "retry once the database is readable", err)
	}
	defer rows.Close()
	owners := 0
	for rows.Next() {
		var link ResourceProductLink
		var environments string
		if err := rows.Scan(&link.ProductID, &link.Role, &link.Purpose, &environments); err != nil {
			return wrapFailure(KindUnavailable, resourcesQueryID, "cannot decode a resource Product link", true, "retry once the database is readable", err)
		}
		if err := json.Unmarshal([]byte(environments), &link.Environments); err != nil {
			return newFailure(KindInvariantViolation, resourcesQueryID, "resource link environments are not a JSON array", false, "rebuild the projections from the log")
		}
		if link.Environments == nil {
			link.Environments = []string{}
		}
		if link.Role == "owner" {
			owners++
			view.Owner = link
			continue
		}
		view.Consumers = append(view.Consumers, link)
	}
	if err := rows.Err(); err != nil {
		return wrapFailure(KindUnavailable, resourcesQueryID, "cannot finish reading resource Product links", true, "retry once the database is readable", err)
	}
	if owners != 1 {
		return newFailure(KindInvariantViolation, resourcesQueryID, "resource "+view.ResourceID+" does not hold exactly one owner", false, "rebuild the projections from the log")
	}
	return nil
}

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// DomainProjectAttachment is one local Domain→Project edge.
type DomainProjectAttachment struct {
	ProjectID string `json:"project_id"`
	Role      string `json:"role"`
}

// DomainProjectAttachmentsRequest replaces a Domain's complete Project edge
// set using an optimistic attachment-set version.
type DomainProjectAttachmentsRequest struct {
	EventID         string
	ProductID       string
	DomainID        string
	ExpectedVersion int64
	Attachments     []DomainProjectAttachment
	Actor           string
	OccurredAt      time.Time
}

// DomainResourceAttachment is one local Domain→managed-resource edge.
type DomainResourceAttachment struct {
	ResourceID   string   `json:"resource_id"`
	Purpose      string   `json:"purpose"`
	Environments []string `json:"environments"`
}

// DomainResourceAttachmentsRequest replaces a Domain's complete resource edge
// set using an optimistic attachment-set version.
type DomainResourceAttachmentsRequest struct {
	EventID         string
	ProductID       string
	DomainID        string
	ExpectedVersion int64
	Attachments     []DomainResourceAttachment
	Actor           string
	OccurredAt      time.Time
}

type domainProjectAttachmentsReplacedPayload struct {
	ProductID        string                    `json:"product_id"`
	DomainID         string                    `json:"domain_id"`
	ExpectedVersion  int64                     `json:"expected_version"`
	ResultingVersion int64                     `json:"resulting_version"`
	Attachments      []DomainProjectAttachment `json:"attachments"`
}

type domainResourceAttachmentsReplacedPayload struct {
	ProductID        string                     `json:"product_id"`
	DomainID         string                     `json:"domain_id"`
	ExpectedVersion  int64                      `json:"expected_version"`
	ResultingVersion int64                      `json:"resulting_version"`
	Attachments      []DomainResourceAttachment `json:"attachments"`
}

func ReplaceDomainProjectAttachments(ctx context.Context, s *Store, req DomainProjectAttachmentsRequest) error {
	if req.EventID == "" || req.ProductID == "" || req.DomainID == "" || req.Actor == "" || req.OccurredAt.IsZero() || req.ExpectedVersion < 0 {
		return newFailure(KindInvalidOperation, "replace_domain_project_attachments", "Domain Project attachment operation is missing bounded identity or version fields", false, "supply Product, Domain, event, actor, time, and a non-negative expected version")
	}
	attachments := append([]DomainProjectAttachment(nil), req.Attachments...)
	if attachments == nil {
		attachments = []DomainProjectAttachment{}
	}
	if err := validateDomainProjectAttachments(attachments); err != nil {
		return err
	}
	payload, err := json.Marshal(domainProjectAttachmentsReplacedPayload{ProductID: req.ProductID, DomainID: req.DomainID, ExpectedVersion: req.ExpectedVersion, ResultingVersion: req.ExpectedVersion + 1, Attachments: attachments})
	if err != nil {
		return wrapFailure(KindInvalidPayload, "replace_domain_project_attachments", "cannot encode Domain Project attachment payload", false, "supply bounded attachment links", err)
	}
	return ApplyOperation(ctx, s, Operation{Events: []Event{{EventID: req.EventID, Kind: "domain.project_attachments_replaced", SubjectType: SubjectProduct, SubjectID: req.ProductID, Actor: req.Actor, OccurredAt: req.OccurredAt, PayloadVersion: 1, Payload: payload}}})
}

func ReplaceDomainResourceAttachments(ctx context.Context, s *Store, req DomainResourceAttachmentsRequest) error {
	if req.EventID == "" || req.ProductID == "" || req.DomainID == "" || req.Actor == "" || req.OccurredAt.IsZero() || req.ExpectedVersion < 0 {
		return newFailure(KindInvalidOperation, "replace_domain_resource_attachments", "Domain resource attachment operation is missing bounded identity or version fields", false, "supply Product, Domain, event, actor, time, and a non-negative expected version")
	}
	attachments := append([]DomainResourceAttachment(nil), req.Attachments...)
	if attachments == nil {
		attachments = []DomainResourceAttachment{}
	}
	for i := range attachments {
		if attachments[i].Environments == nil {
			attachments[i].Environments = []string{}
		}
	}
	if err := validateDomainResourceAttachments(attachments); err != nil {
		return err
	}
	payload, err := json.Marshal(domainResourceAttachmentsReplacedPayload{ProductID: req.ProductID, DomainID: req.DomainID, ExpectedVersion: req.ExpectedVersion, ResultingVersion: req.ExpectedVersion + 1, Attachments: attachments})
	if err != nil {
		return wrapFailure(KindInvalidPayload, "replace_domain_resource_attachments", "cannot encode Domain resource attachment payload", false, "supply bounded attachment links", err)
	}
	return ApplyOperation(ctx, s, Operation{Events: []Event{{EventID: req.EventID, Kind: "domain.resource_attachments_replaced", SubjectType: SubjectProduct, SubjectID: req.ProductID, Actor: req.Actor, OccurredAt: req.OccurredAt, PayloadVersion: 1, Payload: payload}}})
}

func foldDomainProjectAttachmentsReplaced(ctx context.Context, tx *sql.Tx, event Event) error {
	if err := checkSubject(event, SubjectProduct); err != nil {
		return err
	}
	var p domainProjectAttachmentsReplacedPayload
	if err := decodePayload(event, &p); err != nil {
		return err
	}
	if p.ProductID != event.SubjectID || p.ProductID == "" || p.DomainID == "" || p.Attachments == nil || p.ExpectedVersion < 0 || p.ResultingVersion != p.ExpectedVersion+1 {
		return newFailure(KindInvalidPayload, "fold_event", "Domain Project attachment payload is incomplete", false, "supply matching Product/Domain identity and consecutive versions")
	}
	if err := validateDomainProjectAttachments(p.Attachments); err != nil {
		return err
	}
	if err := validateCurrentDomain(ctx, tx, p.ProductID, p.DomainID); err != nil {
		return err
	}
	if err := validateDomainProjectMembers(ctx, tx, p.ProductID, p.Attachments); err != nil {
		return err
	}
	if err := replaceDomainAttachmentSet(ctx, tx, "domain_project_attachment_sets", "domain_project_attachment_edges", p.ProductID, p.DomainID, p.ExpectedVersion, p.ResultingVersion); err != nil {
		return err
	}
	for _, link := range p.Attachments {
		if _, err := tx.ExecContext(ctx, `INSERT INTO domain_project_attachment_edges(product_id,domain_id,project_id,role) VALUES(?,?,?,?)`, p.ProductID, p.DomainID, link.ProjectID, link.Role); err != nil {
			if isUniqueViolation(err) {
				return newFailure(KindProjectionConflict, "fold_event", "duplicate Domain Project attachment or primary role", false, "supply unique Project links and at most one primary")
			}
			return wrapFailure(KindUnavailable, "fold_event", "cannot write Domain Project attachment", true, "retry once the database is writable", err)
		}
	}
	return nil
}

func foldDomainResourceAttachmentsReplaced(ctx context.Context, tx *sql.Tx, event Event) error {
	if err := checkSubject(event, SubjectProduct); err != nil {
		return err
	}
	var p domainResourceAttachmentsReplacedPayload
	if err := decodePayload(event, &p); err != nil {
		return err
	}
	if p.ProductID != event.SubjectID || p.ProductID == "" || p.DomainID == "" || p.Attachments == nil || p.ExpectedVersion < 0 || p.ResultingVersion != p.ExpectedVersion+1 {
		return newFailure(KindInvalidPayload, "fold_event", "Domain resource attachment payload is incomplete", false, "supply matching Product/Domain identity and consecutive versions")
	}
	if err := validateDomainResourceAttachments(p.Attachments); err != nil {
		return err
	}
	if err := validateCurrentDomain(ctx, tx, p.ProductID, p.DomainID); err != nil {
		return err
	}
	if err := validateDomainResourceMembers(ctx, tx, p.ProductID, p.Attachments); err != nil {
		return err
	}
	if err := replaceDomainAttachmentSet(ctx, tx, "domain_resource_attachment_sets", "domain_resource_attachment_edges", p.ProductID, p.DomainID, p.ExpectedVersion, p.ResultingVersion); err != nil {
		return err
	}
	for _, link := range p.Attachments {
		if _, err := tx.ExecContext(ctx, `INSERT INTO domain_resource_attachment_edges(product_id,domain_id,resource_id,purpose,environments) VALUES(?,?,?,?,?)`, p.ProductID, p.DomainID, link.ResourceID, link.Purpose, marshalStrings(link.Environments)); err != nil {
			if isUniqueViolation(err) {
				return newFailure(KindProjectionConflict, "fold_event", "duplicate Domain resource attachment", false, "supply unique resource links")
			}
			return wrapFailure(KindUnavailable, "fold_event", "cannot write Domain resource attachment", true, "retry once the database is writable", err)
		}
	}
	return nil
}

func validateCurrentDomain(ctx context.Context, tx *sql.Tx, productID, domainID string) error {
	var status string
	if err := tx.QueryRowContext(ctx, `SELECT status FROM domains WHERE product_id=? AND domain_id=?`, productID, domainID).Scan(&status); err == sql.ErrNoRows {
		return newFailure(KindProjectionNotFound, "fold_event", "Domain does not exist in the current Product registry", false, "refresh the Git Domain projection before attaching local state")
	} else if err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot inspect current Domain", true, "retry once the database is readable", err)
	}
	if status != "current" {
		return newFailure(KindInvalidTransition, "fold_event", "deprecated Domain cannot receive local attachments", false, "attach only to a current Domain")
	}
	return nil
}

func validateDomainProjectMembers(ctx context.Context, tx *sql.Tx, productID string, links []DomainProjectAttachment) error {
	for _, link := range links {
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM product_projects WHERE product_id=? AND project_id=?`, productID, link.ProjectID).Scan(&exists); err == sql.ErrNoRows {
			return newFailure(KindInvalidRelation, "fold_event", fmt.Sprintf("Project %s is not a member of Product %s", link.ProjectID, productID), false, "attach only Projects already owned by the Product")
		} else if err != nil {
			return wrapFailure(KindUnavailable, "fold_event", "cannot inspect Product Project membership", true, "retry once the database is readable", err)
		}
	}
	return nil
}

func validateDomainResourceMembers(ctx context.Context, tx *sql.Tx, productID string, links []DomainResourceAttachment) error {
	for _, link := range links {
		var resourceEnvironments, productEnvironments string
		if err := tx.QueryRowContext(ctx, `SELECT r.environments,rp.environments FROM managed_resources r JOIN resource_products rp ON rp.resource_id=r.resource_id WHERE r.resource_id=? AND rp.product_id=? AND rp.role IN ('owner','consumer')`, link.ResourceID, productID).Scan(&resourceEnvironments, &productEnvironments); err == sql.ErrNoRows {
			return newFailure(KindInvalidRelation, "fold_event", fmt.Sprintf("Product %s does not own or consume resource %s", productID, link.ResourceID), false, "attach only a resource linked to this Product")
		} else if err != nil {
			return wrapFailure(KindUnavailable, "fold_event", "cannot inspect Product resource membership", true, "retry once the database is readable", err)
		}
		if err := validateResourceEnvironments(link.Environments, resourceEnvironments, productEnvironments); err != nil {
			return err
		}
	}
	return nil
}

func replaceDomainAttachmentSet(ctx context.Context, tx *sql.Tx, setTable, edgeTable, productID, domainID string, expected, resulting int64) error {
	var current int64
	err := tx.QueryRowContext(ctx, "SELECT version FROM "+setTable+" WHERE product_id=? AND domain_id=?", productID, domainID).Scan(&current)
	if err == sql.ErrNoRows {
		current = 0
	} else if err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot inspect Domain attachment-set version", true, "retry once the database is readable", err)
	}
	if current != expected {
		return versionConflict(SubjectProduct, productID, expected, current, true)
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO "+setTable+"(product_id,domain_id,version) VALUES(?,?,?) ON CONFLICT(product_id,domain_id) DO UPDATE SET version=excluded.version", productID, domainID, resulting); err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot advance Domain attachment-set version", true, "retry once the database is writable", err)
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM "+edgeTable+" WHERE product_id=? AND domain_id=?", productID, domainID); err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot replace Domain attachment edges", true, "retry once the database is writable", err)
	}
	return nil
}

func validateDomainProjectAttachments(links []DomainProjectAttachment) error {
	if len(links) > 64 {
		return newFailure(KindLimitExceeded, "domain_project_attachments", "Domain Project attachment set exceeds 64 links", false, "reduce the attachment set")
	}
	seen := make(map[string]struct{}, len(links))
	primaries := 0
	for _, link := range links {
		if link.ProjectID == "" || (link.Role != "primary" && link.Role != "supporting") {
			return newFailure(KindInvalidOperation, "domain_project_attachments", "Domain Project attachment has an invalid Project or role", false, "supply a Project ID and primary or supporting role")
		}
		if _, exists := seen[link.ProjectID]; exists {
			return newFailure(KindProjectionConflict, "domain_project_attachments", "Domain Project attachment set contains a duplicate Project", false, "supply each Project once")
		}
		seen[link.ProjectID] = struct{}{}
		if link.Role == "primary" {
			primaries++
		}
	}
	if primaries > 1 {
		return newFailure(KindProjectionConflict, "domain_project_attachments", "Domain Project attachment set has more than one primary", false, "supply at most one primary Project")
	}
	return nil
}

func validateDomainResourceAttachments(links []DomainResourceAttachment) error {
	if len(links) > 64 {
		return newFailure(KindLimitExceeded, "domain_resource_attachments", "Domain resource attachment set exceeds 64 links", false, "reduce the attachment set")
	}
	seen := make(map[string]struct{}, len(links))
	for _, link := range links {
		if link.ResourceID == "" || link.Purpose == "" || len(link.Purpose) > 512 {
			return newFailure(KindInvalidOperation, "domain_resource_attachments", "Domain resource attachment has an invalid resource or purpose", false, "supply a resource ID and purpose of at most 512 characters")
		}
		if _, exists := seen[link.ResourceID]; exists {
			return newFailure(KindProjectionConflict, "domain_resource_attachments", "Domain resource attachment set contains a duplicate resource", false, "supply each resource once")
		}
		seen[link.ResourceID] = struct{}{}
		if err := validateResourceEnvironments(link.Environments, nil, nil); err != nil {
			return err
		}
	}
	return nil
}

func validateDomainAttachmentInvariantsTx(ctx context.Context, tx *sql.Tx) error {
	var schemaVersion int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(version),0) FROM schema_migrations`).Scan(&schemaVersion); err != nil {
		return wrapFailure(KindUnavailable, "domain_attachment_invariants", "cannot inspect schema compatibility", true, "retry once the database is readable", err)
	}
	if schemaVersion < 38 {
		return nil
	}
	var resourceID string
	if err := tx.QueryRowContext(ctx, `SELECT resource_id FROM managed_resources WHERE (SELECT count(*) FROM resource_products WHERE resource_id=managed_resources.resource_id AND role='owner') <> 1 LIMIT 1`).Scan(&resourceID); err == nil {
		return newFailure(KindInvariantViolation, "domain_attachment_invariants", "managed resource does not have exactly one owner: "+resourceID, false, "rebuild from a valid managed_resource.created event")
	} else if err != sql.ErrNoRows {
		return wrapFailure(KindUnavailable, "domain_attachment_invariants", "cannot validate managed resource ownership", true, "retry once the database is readable", err)
	}
	var edgeResource string
	if err := tx.QueryRowContext(ctx, `SELECT resource_id FROM domain_resource_attachment_edges WHERE NOT EXISTS (SELECT 1 FROM managed_resources r WHERE r.resource_id=domain_resource_attachment_edges.resource_id) LIMIT 1`).Scan(&edgeResource); err == nil {
		return newFailure(KindInvariantViolation, "domain_attachment_invariants", "Domain resource attachment has no resource endpoint: "+edgeResource, false, "rebuild from a valid attachment event log")
	} else if err != sql.ErrNoRows {
		return wrapFailure(KindUnavailable, "domain_attachment_invariants", "cannot validate Domain resource endpoints", true, "retry once the database is readable", err)
	}
	var domainID string
	if err := tx.QueryRowContext(ctx, `SELECT domain_id FROM domain_project_attachment_edges WHERE NOT EXISTS (SELECT 1 FROM domain_project_attachment_sets s WHERE s.product_id=domain_project_attachment_edges.product_id AND s.domain_id=domain_project_attachment_edges.domain_id) LIMIT 1`).Scan(&domainID); err == nil {
		return newFailure(KindInvariantViolation, "domain_attachment_invariants", "Domain Project attachment has no attachment set: "+domainID, false, "rebuild from a valid attachment event log")
	} else if err != sql.ErrNoRows {
		return wrapFailure(KindUnavailable, "domain_attachment_invariants", "cannot validate Domain Project attachment sets", true, "retry once the database is readable", err)
	}
	return nil
}

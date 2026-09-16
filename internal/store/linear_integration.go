package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Linear integration Phase 0 (issue #971, CD-0121). This file owns the typed
// local surface: Product planning modes, declarative connection resolution,
// the outbound operation queue, and the work-to-issue link table. It performs
// no network calls, reads no credentials, and never mutates Linear.

const (
	PlanningModeLocalOnly    = "local_only"
	PlanningModeLinear       = "linear_enabled"
	LinearLinkUnpublished    = "unpublished"
	LinearLinkPending        = "pending"
	LinearLinkConfirmed      = "confirmed"
	LinearLinkDegraded       = "degraded"
	LinearOutboxQueued       = "queued"
	LinearOutboxInFlight     = "in_flight"
	LinearOutboxDone         = "done"
	LinearOutboxFailed       = "failed"
	LinearOpIssueCreate      = "issue_create"
	LinearOpIssueUpdate      = "issue_update"
	LinearOpIssueAdopt       = "issue_adopt"
	LinearConnectionDeclared = "declared"
	LinearConnectionPartial  = "partial"
	LinearConnectionAbsent   = "absent"
)

// ProductPlanningMode is the operator-set planning authority for one Product
// (CD-0121). local_only is the default and never touches Linear code paths.
type ProductPlanningMode struct {
	ProductID    string `json:"product_id"`
	PlanningMode string `json:"planning_mode"`
	Version      int64  `json:"version"`
}

// LinearConnection is the declarative view of a Product's Linear connection:
// the C15 managed saas_account resource carrying the linear metadata
// convention. No credential material is read or stored here.
type LinearConnection struct {
	ResourceID   string            `json:"resource_id"`
	Version      int64             `json:"version"`
	WorkspaceURL string            `json:"workspace_url"`
	TeamID       string            `json:"team_id"`
	AuthMode     string            `json:"auth_mode"`
	ProjectIDs   map[string]string `json:"project_ids,omitempty"`
	StatusIDs    map[string]string `json:"status_ids,omitempty"`
	State        string            `json:"state"` // declared | partial | absent
}

// LinearOutboxEntry is one queued outbound operation.
type LinearOutboxEntry struct {
	OperationID    string          `json:"operation_id"`
	WorkID         string          `json:"work_id"`
	OpKind         string          `json:"op_kind"`
	IdempotencyKey string          `json:"idempotency_key"`
	Payload        json.RawMessage `json:"payload"`
	State          string          `json:"state"`
	Attempts       int             `json:"attempts"`
	LastError      string          `json:"last_error"`
	CreatedAt      string          `json:"created_at"`
	UpdatedAt      string          `json:"updated_at"`
}

// LinearIntegrationHealth is the bounded operator read.
type LinearIntegrationHealth struct {
	ProductID                     string            `json:"product_id"`
	PlanningMode                  string            `json:"planning_mode"`
	ConnectionState               string            `json:"connection_state"`
	Connection                    *LinearConnection `json:"connection,omitempty"`
	OutboxDepth                   int               `json:"outbox_depth"`
	OutboxOldestPendingAgeSeconds int64             `json:"outbox_oldest_pending_age_seconds"`
	LinkCounts                    map[string]int    `json:"link_counts"`
	UnmappedLifecycles            []string          `json:"unmapped_lifecycles"`
}

type productPlanningModeSetPayload struct {
	ProductID        string `json:"product_id"`
	PlanningMode     string `json:"planning_mode"`
	Reason           string `json:"reason"`
	ExpectedVersion  int64  `json:"expected_version"`
	ResultingVersion int64  `json:"resulting_version"`
}

// SetProductPlanningMode records the operator's CD-0121 mode selection for one
// Product as a domain event and folds it into products.planning_mode.
func (s *Store) SetProductPlanningMode(ctx context.Context, productID, planningMode, reason, actor string, expectedVersion int64) (ApplyOperationResult, error) {
	if planningMode != PlanningModeLocalOnly && planningMode != PlanningModeLinear {
		return ApplyOperationResult{}, newFailure(KindInvalidOperation, "product_mode_set", "planning mode is not recognized", false, "use local_only or linear_enabled")
	}
	if reason == "" {
		return ApplyOperationResult{}, newFailure(KindInvalidOperation, "product_mode_set", "reason is required", false, "state why the mode is set")
	}
	if expectedVersion < 1 {
		return ApplyOperationResult{}, newFailure(KindInvalidOperation, "product_mode_set", "positive expected Product version is required", false, "supply the Product's current version")
	}
	var current string
	if err := s.db.QueryRowContext(ctx, `SELECT planning_mode FROM products WHERE id=?`, productID).Scan(&current); err != nil {
		if err == sql.ErrNoRows {
			return ApplyOperationResult{}, newFailure(KindUnknownScope, "product_mode_set", "Product does not exist", false, "supply an existing Product")
		}
		return ApplyOperationResult{}, wrapFailure(KindUnavailable, "product_mode_set", "cannot read Product planning mode", true, "retry once the database is readable", err)
	}
	if current == planningMode {
		return ApplyOperationResult{}, newFailure(KindInvalidOperation, "product_mode_set", "Product already uses that planning mode", false, "reload the Product and set a different mode")
	}
	payload, _ := json.Marshal(productPlanningModeSetPayload{
		ProductID: productID, PlanningMode: planningMode, Reason: reason,
		ExpectedVersion: expectedVersion, ResultingVersion: expectedVersion + 1,
	})
	return ApplyOperationWithResult(ctx, s, Operation{
		Events: []Event{{
			EventID: operatorEventID("product.planning_mode_set", fmt.Sprintf("%s:%d:%s", productID, expectedVersion, planningMode)),
			Kind:    "product.planning_mode_set", SubjectType: SubjectProduct, SubjectID: productID,
			Actor: actor, OccurredAt: s.now(), PayloadVersion: 1, Payload: payload,
		}},
		ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProduct, productID): expectedVersion},
	})
}

func foldProductPlanningModeSet(ctx context.Context, tx *sql.Tx, event Event) error {
	if err := checkSubject(event, SubjectProduct); err != nil {
		return err
	}
	var payload productPlanningModeSetPayload
	if err := decodePayload(event, &payload); err != nil {
		return err
	}
	if payload.ProductID != event.SubjectID {
		return newFailure(KindInvalidPayload, "fold_event", "planning mode subject does not match its event subject", false, "use the event subject as the Product")
	}
	if payload.PlanningMode != PlanningModeLocalOnly && payload.PlanningMode != PlanningModeLinear {
		return newFailure(KindInvalidPayload, "fold_event", "planning mode is not recognized", false, "use local_only or linear_enabled")
	}
	if payload.ExpectedVersion < 1 || payload.ResultingVersion != payload.ExpectedVersion+1 {
		return newFailure(KindInvalidPayload, "fold_event", "planning mode version evidence is not consecutive", false, "supply expected_version and resulting_version with resulting_version one greater")
	}
	result, err := tx.ExecContext(ctx, `UPDATE products SET planning_mode=?, updated_at=? WHERE id=?`, payload.PlanningMode, event.OccurredAt.UTC().Format(time.RFC3339Nano), event.SubjectID)
	if err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot update Product planning mode", true, "retry once the database is writable", err)
	}
	if affected, err := result.RowsAffected(); err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot verify Product planning mode update", true, "retry once the database is readable", err)
	} else if affected == 0 {
		return newFailure(KindUnknownScope, "fold_event", "Product does not exist", false, "supply an existing Product")
	}
	return bumpVersion(ctx, tx, "products", event, payload.ExpectedVersion, payload.ResultingVersion, "Product")
}

// ReadProductPlanningMode returns one Product's mode with a typed refusal for
// an unknown Product.
func (s *Store) ReadProductPlanningMode(ctx context.Context, productID string) (ProductPlanningMode, error) {
	return readProductPlanningModeCore(ctx, s.db, productID)
}

func readProductPlanningModeCore(ctx context.Context, q queryer, productID string) (ProductPlanningMode, error) {
	var mode ProductPlanningMode
	err := q.QueryRowContext(ctx, `SELECT id, planning_mode, version FROM products WHERE id=?`, productID).Scan(&mode.ProductID, &mode.PlanningMode, &mode.Version)
	if err == sql.ErrNoRows {
		return ProductPlanningMode{}, newFailure(KindUnknownScope, "planning_mode_read", "Product does not exist", false, "supply an existing Product")
	}
	if err != nil {
		return ProductPlanningMode{}, wrapFailure(KindUnavailable, "planning_mode_read", "cannot read Product planning mode", true, "retry once the database is readable", err)
	}
	return mode, nil
}

// ResolveLinearPlanningTarget is the single planning-path boundary for mode
// resolution. An unknown Product refuses with unknown_scope; an empty product
// id refuses with ambiguous_scope naming the operator choice, never inferring
// from repository path or installation (CD-0121 D1).
func (s *Store) ResolveLinearPlanningTarget(ctx context.Context, productID string) (ProductPlanningMode, error) {
	return resolveLinearPlanningTargetCore(ctx, s.db, productID)
}

func resolveLinearPlanningTargetCore(ctx context.Context, q queryer, productID string) (ProductPlanningMode, error) {
	if productID == "" {
		return ProductPlanningMode{}, newFailure(KindAmbiguousScope, "planning_mode_resolve", "planning resolution requires exactly one Product", false, "name the Product explicitly; mode is never inferred from repository path or installation")
	}
	mode, err := readProductPlanningModeCore(ctx, q, productID)
	if err != nil {
		return ProductPlanningMode{}, err
	}
	if mode.PlanningMode == PlanningModeLinear {
		connection, connErr := readLinearConnectionCore(ctx, q, productID)
		if connErr != nil {
			return ProductPlanningMode{}, connErr
		}
		if connection.State != LinearConnectionDeclared {
			// CD-0121 D3: missing setup is reported as missing setup. The
			// mode never silently falls back to local-only operation.
			return ProductPlanningMode{}, newFailure(KindInvalidOperation, "planning_mode_resolve", "linear_enabled Product has no declared Linear connection", false, "declare the connection as a managed saas_account resource or set the mode back to local_only")
		}
	}
	return mode, nil
}

// ReadLinearConnection resolves the Product's declarative Linear connection
// from the C15 inventory: the owner-role managed resource of kind saas_account
// whose metadata carries the linear convention. declared requires workspace
// URL, team id, and auth mode; partial means some are missing; absent means no
// such resource exists.
func (s *Store) ReadLinearConnection(ctx context.Context, productID string) (LinearConnection, error) {
	return readLinearConnectionCore(ctx, s.db, productID)
}

func readLinearConnectionCore(ctx context.Context, q queryer, productID string) (LinearConnection, error) {
	rows, err := q.QueryContext(ctx, `
SELECT r.resource_id, r.version, r.metadata
FROM managed_resources r
JOIN resource_products rp ON rp.resource_id = r.resource_id
WHERE rp.product_id = ? AND rp.role = 'owner' AND r.class = 'saas' AND r.kind = 'saas_account'
ORDER BY r.resource_id`, productID)
	if err != nil {
		return LinearConnection{}, wrapFailure(KindUnavailable, "linear_connection_read", "cannot read Linear connection resources", true, "retry once the database is readable", err)
	}
	defer rows.Close()
	var connection *LinearConnection
	for rows.Next() {
		var resourceID, metadataJSON string
		var version int64
		if err := rows.Scan(&resourceID, &version, &metadataJSON); err != nil {
			return LinearConnection{}, wrapFailure(KindUnavailable, "linear_connection_read", "cannot scan Linear connection resource", true, "retry once the database is readable", err)
		}
		var metadata map[string]any
		if json.Unmarshal([]byte(metadataJSON), &metadata) != nil {
			continue
		}
		linear, ok := metadata["linear"].(map[string]any)
		if !ok {
			continue
		}
		candidate := LinearConnection{ResourceID: resourceID, Version: version}
		if v, ok := linear["workspace_url"].(string); ok {
			candidate.WorkspaceURL = v
		}
		if v, ok := linear["team_id"].(string); ok {
			candidate.TeamID = v
		}
		if v, ok := linear["auth_mode"].(string); ok {
			candidate.AuthMode = v
		}
		if encoded, exists := linear["project_ids"]; exists {
			rawProjects, marshalErr := json.Marshal(encoded)
			var projectIDs map[string]json.RawMessage
			if marshalErr != nil || json.Unmarshal(rawProjects, &projectIDs) != nil || projectIDs == nil {
				return LinearConnection{}, newFailure(KindInvalidPayload, "linear_connection_read", "Linear project_ids must be an object", false, "supply Concord project id to Linear project id mappings")
			}
			candidate.ProjectIDs = make(map[string]string, len(projectIDs))
			for projectID, value := range projectIDs {
				if err := validateLinearConnectionID(projectID, "Concord project id"); err != nil {
					return LinearConnection{}, err
				}
				var linearProjectID string
				if err := json.Unmarshal(value, &linearProjectID); err != nil {
					return LinearConnection{}, newFailure(KindInvalidPayload, "linear_connection_read", "Linear project_ids values must be strings", false, "supply Concord project id to Linear project id mappings")
				}
				if err := validateLinearConnectionID(linearProjectID, "Linear project id"); err != nil {
					return LinearConnection{}, err
				}
				candidate.ProjectIDs[projectID] = linearProjectID
			}
		}
		if encoded, exists := linear["status_ids"]; exists {
			rawStatus, marshalErr := json.Marshal(encoded)
			var statusIDs map[string]json.RawMessage
			if marshalErr != nil || json.Unmarshal(rawStatus, &statusIDs) != nil || statusIDs == nil {
				return LinearConnection{}, newFailure(KindInvalidPayload, "linear_connection_read", "Linear status_ids must be an object", false, "supply persistable lifecycle to status id mappings")
			}
			candidate.StatusIDs = make(map[string]string, len(statusIDs))
			for lifecycle, value := range statusIDs {
				if !lifecycleStates[lifecycle] {
					return LinearConnection{}, newFailure(KindInvalidPayload, "linear_connection_read", "Linear status mapping lifecycle is not persistable", false, "map needed, in_progress, completed, cancelled, or superseded")
				}
				var statusID string
				if err := json.Unmarshal(value, &statusID); err != nil {
					return LinearConnection{}, newFailure(KindInvalidPayload, "linear_connection_read", "Linear status id must be a string", false, "supply persistable lifecycle to status id mappings")
				}
				if err := validateLinearConnectionID(statusID, "status id"); err != nil {
					return LinearConnection{}, err
				}
				candidate.StatusIDs[lifecycle] = statusID
			}
			if err := validateLinearStatusMappingRead(candidate.StatusIDs, "linear_connection_read"); err != nil {
				return LinearConnection{}, err
			}
		}
		connection = &candidate
		break
	}
	if err := rows.Err(); err != nil {
		return LinearConnection{}, wrapFailure(KindUnavailable, "linear_connection_read", "cannot finish Linear connection read", true, "retry once the database is readable", err)
	}
	if connection == nil {
		return LinearConnection{State: LinearConnectionAbsent}, nil
	}
	if connection.WorkspaceURL != "" && connection.TeamID != "" && connection.AuthMode != "" {
		connection.State = LinearConnectionDeclared
	} else {
		connection.State = LinearConnectionPartial
	}
	return *connection, nil
}

// LinearConnectionUpdateRequest changes the destination and status mapping
// owned by one Product's managed Linear resource. Nil maps keep their existing
// values. Non-nil maps replace the complete mapping.
type LinearConnectionUpdateRequest struct {
	EventID                 string
	ResourceID              string
	ProductID               string
	TeamID                  string
	ProjectIDs              map[string]string
	StatusIDs               map[string]string
	ExpectedResourceVersion int64
	Actor                   string
	OccurredAt              time.Time
}

// UpdateLinearConnection records a version-checked, Product-scoped metadata
// update. It merges the Linear extension with the current object and leaves all
// unrelated managed-resource metadata unchanged.
func (s *Store) UpdateLinearConnection(ctx context.Context, req LinearConnectionUpdateRequest) error {
	if req.EventID == "" || req.ResourceID == "" || req.ProductID == "" || req.Actor == "" || req.OccurredAt.IsZero() || req.ExpectedResourceVersion < 1 {
		return newFailure(KindInvalidOperation, "linear_connection_update", "connection update is missing bounded identity or version fields", false, "supply resource, Product, event, actor, time, and a positive expected version")
	}
	if req.TeamID != "" {
		if err := validateLinearConnectionID(req.TeamID, "team id"); err != nil {
			return err
		}
	}
	if req.ProjectIDs != nil {
		for projectID, linearProjectID := range req.ProjectIDs {
			if err := validateLinearConnectionID(projectID, "Concord project id"); err != nil {
				return err
			}
			if err := validateLinearConnectionID(linearProjectID, "Linear project id"); err != nil {
				return err
			}
		}
	}
	if req.StatusIDs != nil {
		if err := validateLinearStatusMapping(req.StatusIDs, "linear_connection_update"); err != nil {
			return err
		}
	}
	var schemaVersion string
	var raw []byte
	err := s.db.QueryRowContext(ctx, `SELECT metadata_schema_version, metadata FROM managed_resources r JOIN resource_products rp ON rp.resource_id=r.resource_id WHERE r.resource_id=? AND rp.product_id=? AND rp.role='owner' AND r.class='saas' AND r.kind='saas_account'`, req.ResourceID, req.ProductID).Scan(&schemaVersion, &raw)
	if err == sql.ErrNoRows {
		return newFailure(KindUnknownScope, "linear_connection_update", "the Product does not own a Linear connection resource", false, "supply the owning Product and managed resource")
	}
	if err != nil {
		return wrapFailure(KindUnavailable, "linear_connection_update", "cannot read the Linear connection metadata", true, "retry once the database is readable", err)
	}
	var metadata map[string]json.RawMessage
	if err := json.Unmarshal(raw, &metadata); err != nil || metadata == nil {
		return newFailure(KindInvalidPayload, "linear_connection_update", "stored resource metadata is not a JSON object", false, "repair the managed resource metadata")
	}
	var linear map[string]json.RawMessage
	if encoded := metadata["linear"]; len(encoded) > 0 {
		if err := json.Unmarshal(encoded, &linear); err != nil || linear == nil {
			return newFailure(KindInvalidPayload, "linear_connection_update", "stored Linear metadata is not a JSON object", false, "repair the managed resource metadata")
		}
	} else {
		return newFailure(KindInvalidOperation, "linear_connection_update", "managed resource does not declare Linear metadata", false, "update an existing Linear connection resource")
	}
	var existingTeamID string
	if encoded := linear["team_id"]; len(encoded) > 0 {
		if err := json.Unmarshal(encoded, &existingTeamID); err != nil {
			return newFailure(KindInvalidPayload, "linear_connection_update", "stored Linear team_id is not a string", false, "repair the managed resource metadata")
		}
	}
	if req.TeamID != "" && existingTeamID != "" && req.TeamID != existingTeamID && req.StatusIDs == nil {
		return newFailure(KindInvalidOperation, "linear_connection_update", "a team change requires a replacement status mapping", false, "supply status_ids that belong to the selected team")
	}
	if req.StatusIDs != nil && req.TeamID != "" && req.TeamID != existingTeamID {
		var existingStatusIDs map[string]string
		if encoded := linear["status_ids"]; len(encoded) > 0 && json.Unmarshal(encoded, &existingStatusIDs) == nil {
			for _, statusID := range req.StatusIDs {
				for _, existingStatusID := range existingStatusIDs {
					if statusID == existingStatusID {
						return newFailure(KindInvalidRelation, "linear_connection_update", "status mapping contains an identifier from the previous team", false, "supply status identifiers belonging to the selected team")
					}
				}
			}
		}
	}
	if req.TeamID != "" {
		linear["team_id"], _ = json.Marshal(req.TeamID)
	}
	if req.ProjectIDs != nil {
		linear["project_ids"], _ = json.Marshal(req.ProjectIDs)
	}
	delete(linear, "project_id")
	if req.StatusIDs != nil {
		linear["status_ids"], _ = json.Marshal(req.StatusIDs)
	}
	metadata["linear"], _ = json.Marshal(linear)
	merged, err := json.Marshal(metadata)
	if err != nil {
		return wrapFailure(KindInvalidPayload, "linear_connection_update", "cannot encode merged Linear metadata", false, "supply valid connection metadata", err)
	}
	return UpdateManagedResourceMetadata(ctx, s, ManagedResourceMetadataUpdateRequest{
		EventID: req.EventID, ResourceID: req.ResourceID, ProductID: req.ProductID,
		MetadataSchemaVersion: schemaVersion, Metadata: merged, ExpectedResourceVersion: req.ExpectedResourceVersion,
		Actor: req.Actor, OccurredAt: req.OccurredAt,
	})
}

func validateLinearConnectionID(value, name string) error {
	if len(value) < 2 || len(value) > 128 || value != strings.TrimSpace(value) {
		return newFailure(KindInvalidPayload, "linear_connection_update", name+" must be a trimmed value of 2 to 128 characters", false, "supply a bounded connection identifier")
	}
	return nil
}

func validateLinearStatusMapping(statusIDs map[string]string, operation string) error {
	if len(statusIDs) != len(lifecycleStates) {
		return newFailure(KindInvalidPayload, operation, "status mapping must contain all lifecycles", false, "map needed, in_progress, completed, cancelled, and superseded")
	}
	for lifecycle, statusID := range statusIDs {
		if !lifecycleStates[lifecycle] {
			return newFailure(KindInvalidPayload, operation, "status mapping lifecycle is not persistable", false, "map needed, in_progress, completed, cancelled, and superseded")
		}
		if err := validateLinearConnectionID(statusID, "status id"); err != nil {
			return err
		}
	}
	// Distinctness is deliberately not required: a workspace may hold one
	// status that two lifecycles share, and a default team has exactly one
	// cancelled-category status for both cancelled and superseded.
	for lifecycle := range lifecycleStates {
		if statusIDs[lifecycle] == "" {
			return newFailure(KindInvalidPayload, operation, "status mapping must contain all lifecycles", false, "map needed, in_progress, completed, cancelled, and superseded")
		}
	}
	return nil
}

func validateLinearStatusMappingRead(statusIDs map[string]string, operation string) error {
	if len(statusIDs) > len(lifecycleStates) {
		return newFailure(KindInvalidPayload, operation, "status mapping contains too many lifecycles", false, "map only persistable lifecycles")
	}
	for lifecycle, statusID := range statusIDs {
		if !lifecycleStates[lifecycle] {
			return newFailure(KindInvalidPayload, operation, "status mapping lifecycle is not persistable", false, "map only persistable lifecycles")
		}
		if err := validateLinearConnectionID(statusID, "status id"); err != nil {
			return err
		}
	}
	return nil
}

// EnqueueLinearOperation queues one intended outbound operation. Phase 0 owns
// the typed refusal surface; the drain arrives with Phase 1.
func (s *Store) EnqueueLinearOperation(ctx context.Context, entry LinearOutboxEntry) error {
	if entry.OperationID == "" || len(entry.OperationID) > 128 {
		return newFailure(KindInvalidPayload, "linear_outbox_enqueue", "operation id must be 2 to 128 characters", false, "supply a bounded operation id")
	}
	if entry.OpKind != LinearOpIssueCreate && entry.OpKind != LinearOpIssueUpdate && entry.OpKind != LinearOpIssueAdopt {
		return newFailure(KindInvalidPayload, "linear_outbox_enqueue", "operation kind is not recognized", false, "use issue_create, issue_update, or issue_adopt")
	}
	if entry.IdempotencyKey == "" || len(entry.IdempotencyKey) > 128 {
		return newFailure(KindInvalidPayload, "linear_outbox_enqueue", "idempotency key must be 2 to 128 characters", false, "supply a bounded idempotency key")
	}
	if len(entry.Payload) == 0 || json.Unmarshal(entry.Payload, &map[string]any{}) != nil {
		return newFailure(KindInvalidPayload, "linear_outbox_enqueue", "payload must be a JSON object", false, "supply a JSON object payload")
	}
	var payloadFields map[string]json.RawMessage
	if err := json.Unmarshal(entry.Payload, &payloadFields); err != nil {
		return newFailure(KindInvalidPayload, "linear_outbox_enqueue", "payload must be a JSON object", false, "supply a JSON object payload")
	}
	if encoded, exists := payloadFields["product_id"]; exists {
		var productID string
		if json.Unmarshal(encoded, &productID) != nil || productID == "" {
			return newFailure(KindInvalidPayload, "linear_outbox_enqueue", "payload product_id must be a non-empty string", false, "supply the immutable owning Product")
		}
		owner, err := s.resolveLinearProduct(ctx, entry.WorkID)
		if err != nil {
			return err
		}
		if owner != productID {
			return newFailure(KindInvalidRelation, "linear_outbox_enqueue", "payload Product does not own the work item", false, "supply the work item's owning Product")
		}
	}
	var exists int
	if err := s.db.QueryRowContext(ctx, `SELECT 1 FROM work_items WHERE id=?`, entry.WorkID).Scan(&exists); err == sql.ErrNoRows {
		return newFailure(KindUnknownScope, "linear_outbox_enqueue", "work item does not exist", false, "supply an existing work item")
	} else if err != nil {
		return wrapFailure(KindUnavailable, "linear_outbox_enqueue", "cannot read work item", true, "retry once the database is readable", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return wrapFailure(KindUnavailable, "linear_outbox_enqueue", "cannot open queue transaction", true, "retry once the database is writable", err)
	}
	defer tx.Rollback()
	if err := enterFold(ctx, tx); err != nil {
		return err
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	var duplicate int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM linear_outbox WHERE idempotency_key=?`, entry.IdempotencyKey).Scan(&duplicate); err == nil {
		if err := leaveFold(ctx, tx); err != nil {
			return err
		}
		return newFailure(KindIdempotencyConflict, "linear_outbox_enqueue", "idempotency key is already queued", false, "reuse the queued operation or supply a new key")
	} else if err != sql.ErrNoRows {
		return wrapFailure(KindUnavailable, "linear_outbox_enqueue", "cannot inspect queue", true, "retry once the database is readable", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO linear_outbox(operation_id, work_id, op_kind, idempotency_key, payload, state, attempts, last_error, created_at, updated_at) VALUES (?,?,?,?,?,'queued',0,'',?,?)`,
		entry.OperationID, entry.WorkID, entry.OpKind, entry.IdempotencyKey, string(entry.Payload), now, now); err != nil {
		return wrapFailure(KindUnavailable, "linear_outbox_enqueue", "cannot queue operation", true, "retry once the database is writable", err)
	}
	if err := leaveFold(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}

// linearOutboxTransition moves one queued operation through its typed states.
func (s *Store) linearOutboxTransition(ctx context.Context, operationID, from, to, lastError string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return wrapFailure(KindUnavailable, "linear_outbox_transition", "cannot open queue transaction", true, "retry once the database is writable", err)
	}
	defer tx.Rollback()
	if err := enterFold(ctx, tx); err != nil {
		return err
	}
	var state string
	var attempts int
	if err := tx.QueryRowContext(ctx, `SELECT state, attempts FROM linear_outbox WHERE operation_id=?`, operationID).Scan(&state, &attempts); err == sql.ErrNoRows {
		if err := leaveFold(ctx, tx); err != nil {
			return err
		}
		return newFailure(KindUnknownScope, "linear_outbox_transition", "queued operation does not exist", false, "supply a queued operation id")
	} else if err != nil {
		return wrapFailure(KindUnavailable, "linear_outbox_transition", "cannot read queued operation", true, "retry once the database is readable", err)
	}
	if state != from {
		if err := leaveFold(ctx, tx); err != nil {
			return err
		}
		return newFailure(KindInvalidTransition, "linear_outbox_transition", fmt.Sprintf("outbox state is %s, not %s", state, from), false, "reload the operation and transition from its current state")
	}
	nextAttempts := attempts
	if to == LinearOutboxInFlight || to == LinearOutboxFailed {
		nextAttempts = attempts + 1
	}
	if _, err := tx.ExecContext(ctx, `UPDATE linear_outbox SET state=?, attempts=?, last_error=?, updated_at=? WHERE operation_id=?`,
		to, nextAttempts, lastError, s.now().UTC().Format(time.RFC3339Nano), operationID); err != nil {
		return wrapFailure(KindUnavailable, "linear_outbox_transition", "cannot update queued operation", true, "retry once the database is writable", err)
	}
	if err := leaveFold(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}

// ClaimLinearOperation moves a queued operation to in_flight.
func (s *Store) ClaimLinearOperation(ctx context.Context, operationID string) error {
	return s.linearOutboxTransition(ctx, operationID, LinearOutboxQueued, LinearOutboxInFlight, "")
}

// RequeueLinearOperation returns a failed operation to queued for retry.
func (s *Store) RequeueLinearOperation(ctx context.Context, operationID string) error {
	return s.linearOutboxTransition(ctx, operationID, LinearOutboxFailed, LinearOutboxQueued, "")
}

var linearLinkTransitions = map[string]map[string]bool{
	LinearLinkUnpublished: {LinearLinkPending: true},
	LinearLinkPending:     {LinearLinkConfirmed: true, LinearLinkDegraded: true},
	LinearLinkConfirmed:   {LinearLinkPending: true, LinearLinkDegraded: true},
	LinearLinkDegraded:    {LinearLinkConfirmed: true},
}

// RecordLinearLink creates or advances a work item's link record through its
// typed states, refusing every transition the closed map does not admit.
func (s *Store) RecordLinearLink(ctx context.Context, workID, remoteUUID, humanKey, url, remoteUpdatedAt, contentHash, targetState string) error {
	if len(workID) < 2 || len(workID) > 128 {
		return newFailure(KindInvalidPayload, "linear_link_record", "work id must be 2 to 128 characters", false, "supply a bounded work id")
	}
	if len(remoteUUID) < 2 || len(remoteUUID) > 128 {
		return newFailure(KindInvalidPayload, "linear_link_record", "remote issue uuid must be 2 to 128 characters", false, "supply a bounded remote uuid")
	}
	if contentHash != "" && (len(contentHash) != 71 || contentHash[:7] != "sha256:") {
		return newFailure(KindInvalidPayload, "linear_link_record", "content hash must be a sha256 digest", false, "supply sha256:<hex> or leave empty")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return wrapFailure(KindUnavailable, "linear_link_record", "cannot open link transaction", true, "retry once the database is writable", err)
	}
	defer tx.Rollback()
	if err := enterFold(ctx, tx); err != nil {
		return err
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	var currentState string
	err = tx.QueryRowContext(ctx, `SELECT link_state FROM linear_issue_links WHERE work_id=?`, workID).Scan(&currentState)
	switch {
	case err == sql.ErrNoRows:
		if targetState != LinearLinkUnpublished && targetState != LinearLinkPending {
			if leaveErr := leaveFold(ctx, tx); leaveErr != nil {
				return leaveErr
			}
			return newFailure(KindInvalidTransition, "linear_link_record", "a new link starts unpublished or pending", false, "record the link before advancing it")
		}
		var linkedWorkID string
		if err := tx.QueryRowContext(ctx, `SELECT work_id FROM linear_issue_links WHERE remote_issue_uuid=?`, remoteUUID).Scan(&linkedWorkID); err == nil {
			if leaveErr := leaveFold(ctx, tx); leaveErr != nil {
				return leaveErr
			}
			return newFailure(KindInvalidRelation, "linear_link_record", "another work item already links that issue", false, "record an issue no other work item links")
		} else if err != sql.ErrNoRows {
			return wrapFailure(KindUnavailable, "linear_link_record", "cannot inspect existing links", true, "retry once the database is readable", err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO linear_issue_links(work_id, remote_issue_uuid, human_key, url, remote_updated_at, content_hash, link_state, created_at, updated_at) VALUES (?,?,?,?,?,?,?, ?, ?)`,
			workID, remoteUUID, humanKey, url, remoteUpdatedAt, contentHash, targetState, now, now); err != nil {
			return wrapFailure(KindUnavailable, "linear_link_record", "cannot create link", true, "retry once the database is writable", err)
		}
	case err != nil:
		return wrapFailure(KindUnavailable, "linear_link_record", "cannot read link", true, "retry once the database is readable", err)
	default:
		allowed := linearLinkTransitions[currentState]
		if !allowed[targetState] {
			if leaveErr := leaveFold(ctx, tx); leaveErr != nil {
				return leaveErr
			}
			return newFailure(KindInvalidTransition, "linear_link_record", fmt.Sprintf("link state %s cannot move to %s", currentState, targetState), false, "reload the link and use an admitted transition")
		}
		var linkedWorkID string
		if err := tx.QueryRowContext(ctx, `SELECT work_id FROM linear_issue_links WHERE remote_issue_uuid=?`, remoteUUID).Scan(&linkedWorkID); err == nil && linkedWorkID != workID {
			if leaveErr := leaveFold(ctx, tx); leaveErr != nil {
				return leaveErr
			}
			return newFailure(KindInvalidRelation, "linear_link_record", "another work item already links that issue", false, "record an issue no other work item links")
		} else if err != nil && err != sql.ErrNoRows {
			return wrapFailure(KindUnavailable, "linear_link_record", "cannot inspect existing links", true, "retry once the database is readable", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE linear_issue_links SET remote_issue_uuid=?, human_key=?, url=?, remote_updated_at=?, content_hash=?, link_state=?, updated_at=? WHERE work_id=?`,
			remoteUUID, humanKey, url, remoteUpdatedAt, contentHash, targetState, now, workID); err != nil {
			return wrapFailure(KindUnavailable, "linear_link_record", "cannot update link", true, "retry once the database is writable", err)
		}
	}
	if err := leaveFold(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}

// ReadLinearIntegrationHealth is the bounded operator read: mode, connection
// declaration state, outbox depth with oldest pending age, and link counts.
func (s *Store) ReadLinearIntegrationHealth(ctx context.Context, productID string) (LinearIntegrationHealth, error) {
	mode, err := s.ReadProductPlanningMode(ctx, productID)
	if err != nil {
		return LinearIntegrationHealth{}, err
	}
	connection, err := s.ReadLinearConnection(ctx, productID)
	if err != nil {
		return LinearIntegrationHealth{}, err
	}
	health := LinearIntegrationHealth{
		ProductID:       productID,
		PlanningMode:    mode.PlanningMode,
		ConnectionState: connection.State,
		LinkCounts:      map[string]int{LinearLinkUnpublished: 0, LinearLinkPending: 0, LinearLinkConfirmed: 0, LinearLinkDegraded: 0},
	}
	if connection.State != LinearConnectionAbsent {
		health.Connection = &connection
	}
	// A declared connection may predate the widened mapping and carry only
	// the terminal lifecycles. Health names the gap so the operator can
	// re-declare without reading the resource record.
	health.UnmappedLifecycles = []string{}
	if connection.State == LinearConnectionDeclared {
		for lifecycle := range lifecycleStates {
			if connection.StatusIDs[lifecycle] == "" {
				health.UnmappedLifecycles = append(health.UnmappedLifecycles, lifecycle)
			}
		}
		sort.Strings(health.UnmappedLifecycles)
	}
	var oldestPending string
	if err := s.db.QueryRowContext(ctx, `SELECT count(*), coalesce(min(created_at), '') FROM linear_outbox o WHERE state IN ('queued','in_flight') AND EXISTS (SELECT 1 FROM work_projects wp JOIN product_projects pp ON pp.project_id=wp.project_id WHERE wp.work_id=o.work_id AND pp.product_id=?)`, productID).Scan(&health.OutboxDepth, &oldestPending); err != nil {
		return LinearIntegrationHealth{}, wrapFailure(KindUnavailable, "linear_health_read", "cannot read outbox depth", true, "retry once the database is readable", err)
	}
	if oldestPending != "" {
		if created, parseErr := time.Parse(time.RFC3339Nano, oldestPending); parseErr == nil {
			health.OutboxOldestPendingAgeSeconds = int64(time.Since(created).Seconds())
			if health.OutboxOldestPendingAgeSeconds < 0 {
				health.OutboxOldestPendingAgeSeconds = 0
			}
		}
	}
	rows, err := s.db.QueryContext(ctx, `SELECT l.link_state, count(*) FROM linear_issue_links l WHERE EXISTS (SELECT 1 FROM work_projects wp JOIN product_projects pp ON pp.project_id=wp.project_id WHERE wp.work_id=l.work_id AND pp.product_id=?) GROUP BY l.link_state`, productID)
	if err != nil {
		return LinearIntegrationHealth{}, wrapFailure(KindUnavailable, "linear_health_read", "cannot read link counts", true, "retry once the database is readable", err)
	}
	defer rows.Close()
	for rows.Next() {
		var state string
		var count int
		if err := rows.Scan(&state, &count); err != nil {
			return LinearIntegrationHealth{}, wrapFailure(KindUnavailable, "linear_health_read", "cannot scan link counts", true, "retry once the database is readable", err)
		}
		health.LinkCounts[state] = count
	}
	if err := rows.Err(); err != nil {
		return LinearIntegrationHealth{}, wrapFailure(KindUnavailable, "linear_health_read", "cannot finish link count read", true, "retry once the database is readable", err)
	}
	return health, nil
}

// LinearIssueLink is one work item's durable link record.
type LinearIssueLink struct {
	WorkID          string `json:"work_id"`
	RemoteIssueUUID string `json:"remote_issue_uuid"`
	HumanKey        string `json:"human_key"`
	URL             string `json:"url"`
	LinkState       string `json:"link_state"`
	ContentHash     string `json:"content_hash"`
}

// ReadLinearLink returns one work item's link, refusing when none exists.
func (s *Store) ReadLinearLink(ctx context.Context, workID string) (LinearIssueLink, error) {
	var link LinearIssueLink
	err := s.db.QueryRowContext(ctx, `SELECT work_id, remote_issue_uuid, human_key, url, link_state, content_hash FROM linear_issue_links WHERE work_id=?`, workID).Scan(&link.WorkID, &link.RemoteIssueUUID, &link.HumanKey, &link.URL, &link.LinkState, &link.ContentHash)
	if err == sql.ErrNoRows {
		return LinearIssueLink{}, newFailure(KindUnknownScope, "linear_link_read", "no link exists for the work item", false, "enqueue issue_create first")
	} else if err != nil {
		return LinearIssueLink{}, wrapFailure(KindUnavailable, "linear_link_read", "cannot read link", true, "retry once the database is readable", err)
	}
	return link, nil
}

// Linear Phase 1 (issue 990): the enqueue, batch claim, and completion surface
// the drain executes against. The store still performs no network calls; the
// drain verb in the CLI owns the Linear client and maps its typed failures onto
// FailLinearOperation classes.

// linearMaxAttempts bounds one operation's retries before it lands in failed.
const linearMaxAttempts = 5

// LinearRemoteIdentity is the confirmed remote state of one work item's issue.
type LinearRemoteIdentity struct {
	RemoteUUID      string
	HumanKey        string
	URL             string
	RemoteUpdatedAt string
	ContentHash     string
}

// ClaimedLinearOperation is one operation handed to the drain under claim.
type ClaimedLinearOperation struct {
	OperationID    string          `json:"operation_id"`
	WorkID         string          `json:"work_id"`
	OpKind         string          `json:"op_kind"`
	IdempotencyKey string          `json:"idempotency_key"`
	Attempts       int             `json:"attempts"`
	Payload        json.RawMessage `json:"payload"`
}

// linearPayload is the JSON convention every outbox payload carries. The
// client UUID is the idempotency identity: Linear's IssueCreateInput.id, so a
// re-drain after an ambiguous outcome converges on the same remote issue.
type linearPayload struct {
	ClientUUID        string `json:"client_uuid"`
	ProductID         string `json:"product_id"`
	Title             string `json:"title"`
	Description       string `json:"description"`
	TeamID            string `json:"team_id"`
	ProjectID         string `json:"project_id,omitempty"`
	ConnectionVersion int64  `json:"connection_version"`
	Lifecycle         string `json:"lifecycle,omitempty"`
	StatusID          string `json:"status_id,omitempty"`
	// RemoteIssueUUID names the existing issue an issue_adopt operation
	// resolves at drain time. Create and update leave it empty.
	RemoteIssueUUID string `json:"remote_issue_uuid,omitempty"`
}

// linearLifecycleStatusID resolves the lifecycle mapping declared on the
// Product's connection resource. It never guesses a workspace state.
func linearLifecycleStatusID(connection LinearConnection, lifecycle string) (string, error) {
	if !lifecycleStates[lifecycle] {
		return "", newFailure(KindInvalidPayload, "linear_issue_enqueue", "lifecycle is not persistable", false, "supply a persistable work lifecycle")
	}
	statusID := connection.StatusIDs[lifecycle]
	if statusID == "" {
		return "", newFailure(KindInvalidOperation, "linear_issue_enqueue", "no declared Linear status id for lifecycle "+lifecycle, false, "declare status_ids."+lifecycle+" on the Linear connection resource")
	}
	return statusID, nil
}

// newLinearClientUUID mints a version-4 UUID with crypto/rand. First-party by
// design: the Linear client carries no third-party dependency (issue 990).
func newLinearClientUUID() string {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return fmt.Sprintf("fallback-%d", time.Now().UnixNano())
	}
	bytes[6] = (bytes[6] & 0x0f) | 0x40
	bytes[8] = (bytes[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", bytes[0:4], bytes[4:6], bytes[6:8], bytes[8:10], bytes[10:16])
}

// resolveLinearProduct resolves the exactly one Product a work item belongs
// to. Zero or many Products refuse: planning authority is never inferred.
func (s *Store) resolveLinearProduct(ctx context.Context, workID string) (string, error) {
	return resolveLinearProductCore(ctx, s.db, workID)
}

func resolveLinearProductCore(ctx context.Context, q queryer, workID string) (string, error) {
	rows, err := q.QueryContext(ctx, `
SELECT DISTINCT pp.product_id
FROM work_items w
JOIN work_projects wp ON wp.work_id = w.id
JOIN product_projects pp ON pp.project_id = wp.project_id
WHERE w.id = ?`, workID)
	if err != nil {
		return "", wrapFailure(KindUnavailable, "linear_product_resolve", "cannot resolve the work item's Product", true, "retry once the database is readable", err)
	}
	defer rows.Close()
	var products []string
	for rows.Next() {
		var productID string
		if err := rows.Scan(&productID); err != nil {
			return "", wrapFailure(KindUnavailable, "linear_product_resolve", "cannot scan the work item's Product", true, "retry once the database is readable", err)
		}
		products = append(products, productID)
	}
	if err := rows.Err(); err != nil {
		return "", wrapFailure(KindUnavailable, "linear_product_resolve", "cannot finish the Product read", true, "retry once the database is readable", err)
	}
	switch len(products) {
	case 1:
		return products[0], nil
	case 0:
		return "", newFailure(KindUnknownScope, "linear_product_resolve", "work item does not exist or belongs to no Product", false, "supply a work item with exactly one Product")
	default:
		return "", newFailure(KindAmbiguousScope, "linear_product_resolve", "work item belongs to more than one Product", false, "supply a work item with exactly one Product")
	}
}

func resolveLinearProjectCore(ctx context.Context, q queryer, workID, productID string) (string, string, error) {
	var projectID, displayName string
	err := q.QueryRowContext(ctx, `
SELECT wp.project_id, p.display_name
FROM work_items w
JOIN work_projects wp ON wp.work_id = w.id AND wp.role = 'primary'
JOIN product_projects pp ON pp.project_id = wp.project_id AND pp.product_id = ?
JOIN projects p ON p.id = wp.project_id
WHERE w.id = ?`, productID, workID).Scan(&projectID, &displayName)
	if err == sql.ErrNoRows {
		return "", "", newFailure(KindUnknownScope, "linear_project_resolve", "work item has no owning Concord project in the Product", false, "supply a work item with one primary project in the owning Product")
	}
	if err != nil {
		return "", "", wrapFailure(KindUnavailable, "linear_project_resolve", "cannot resolve the work item's Concord project", true, "retry once the database is readable", err)
	}
	return projectID, displayName, nil
}

// EnqueueLinearIssueForWork queues one outbound issue operation for a work
// item and records its link as unpublished. The guards are the CD-0121
// boundaries: local_only refuses, linear_enabled without a declared
// connection refuses as missing setup, and neither is inferred.
func (s *Store) EnqueueLinearIssueForWork(ctx context.Context, workID, opKind string) (ClaimedLinearOperation, error) {
	return s.enqueueLinearIssueForWork(ctx, "", workID, opKind)
}

// EnqueueLinearIssueForProduct applies the caller's Product scope before it
// persists an operation. This prevents a Product argument from routing work
// that belongs to another Product.
func (s *Store) EnqueueLinearIssueForProduct(ctx context.Context, productID, workID, opKind string) (ClaimedLinearOperation, error) {
	return s.enqueueLinearIssueForWork(ctx, productID, workID, opKind)
}

func (s *Store) enqueueLinearIssueForWork(ctx context.Context, expectedProductID, workID, opKind string) (ClaimedLinearOperation, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ClaimedLinearOperation{}, wrapFailure(KindUnavailable, "linear_issue_enqueue", "cannot open enqueue transaction", true, "retry once the database is writable", err)
	}
	defer tx.Rollback()
	if err := enterFold(ctx, tx); err != nil {
		return ClaimedLinearOperation{}, err
	}
	plan, err := enqueueLinearIssueForWorkCore(ctx, tx, expectedProductID, workID, opKind)
	if err != nil {
		return ClaimedLinearOperation{}, err
	}
	entry, err := persistLinearIssueEnqueueTx(ctx, tx, plan, s.now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return ClaimedLinearOperation{}, err
	}
	if err := leaveFold(ctx, tx); err != nil {
		return ClaimedLinearOperation{}, err
	}
	if err := tx.Commit(); err != nil {
		return ClaimedLinearOperation{}, wrapFailure(KindUnavailable, "linear_issue_enqueue", "cannot commit queued issue", true, "retry once the database is writable", err)
	}
	return entry, nil
}

type linearIssueEnqueuePlan struct {
	entry      ClaimedLinearOperation
	payload    []byte
	createLink bool
}

// composeLinearIssueBody renders the issue Description at the single point
// both enqueue paths share. Each source is omitted when absent so the body
// never shows an empty label. The footer carries the Concord work id and the
// documented resume line `concord zl <work id> --` from the CLI help.
func composeLinearIssueBody(valueStatement, premise, workID string) string {
	sections := make([]string, 0, 3)
	if trimmed := strings.TrimSpace(valueStatement); trimmed != "" {
		sections = append(sections, trimmed)
	}
	if trimmed := strings.TrimSpace(premise); trimmed != "" {
		sections = append(sections, "## Premise\n\n"+trimmed)
	}
	sections = append(sections, "Concord work: "+workID+"\nResume: `concord zl "+workID+" --`")
	return strings.Join(sections, "\n\n")
}

// readCurrentWorkflowPremiseCore reads the newest unsuperseded contract
// premise through the caller's queryer so it stays inside the caller's
// transaction. A work item without a current contract yields an empty
// premise, which composeLinearIssueBody omits.
func readCurrentWorkflowPremiseCore(ctx context.Context, q queryer, workID string) (string, error) {
	contractVersion, err := activeWorkflowContractVersion(ctx, q, workID, "linear_issue_enqueue")
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var premise string
	err = q.QueryRowContext(ctx, `SELECT premise FROM workflow_contracts WHERE work_id=? AND contract_version=? AND superseded_by IS NULL`, workID, contractVersion).Scan(&premise)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", wrapFailure(KindUnavailable, "linear_issue_enqueue", "cannot read workflow contract", true, "retry once the database is readable", err)
	}
	return premise, nil
}

func enqueueLinearIssueForWorkCore(ctx context.Context, q queryer, expectedProductID, workID, opKind string) (linearIssueEnqueuePlan, error) {
	if opKind != LinearOpIssueCreate && opKind != LinearOpIssueUpdate {
		if opKind == LinearOpIssueAdopt {
			return linearIssueEnqueuePlan{}, newFailure(KindInvalidPayload, "linear_issue_enqueue", "issue adoption names an existing remote issue", false, "enqueue the adoption through EnqueueLinearIssueAdoption with the remote issue uuid")
		}
		return linearIssueEnqueuePlan{}, newFailure(KindInvalidPayload, "linear_issue_enqueue", "operation kind is not recognized", false, "use issue_create or issue_update")
	}
	if len(workID) < 2 || len(workID) > 128 {
		return linearIssueEnqueuePlan{}, newFailure(KindInvalidPayload, "linear_issue_enqueue", "work id must be 2 to 128 characters", false, "supply a bounded work id")
	}
	var title, valueStatement, lifecycle string
	err := q.QueryRowContext(ctx, `SELECT title, coalesce(json_extract(intent_json, '$.value_statement'), ''), lifecycle FROM work_items WHERE id=?`, workID).Scan(&title, &valueStatement, &lifecycle)
	if err == sql.ErrNoRows {
		return linearIssueEnqueuePlan{}, newFailure(KindUnknownScope, "linear_issue_enqueue", "work item does not exist", false, "supply an existing work item")
	} else if err != nil {
		return linearIssueEnqueuePlan{}, wrapFailure(KindUnavailable, "linear_issue_enqueue", "cannot read work item", true, "retry once the database is readable", err)
	}
	productID, err := resolveLinearProductCore(ctx, q, workID)
	if err != nil {
		return linearIssueEnqueuePlan{}, err
	}
	if expectedProductID != "" && expectedProductID != productID {
		return linearIssueEnqueuePlan{}, newFailure(KindInvalidRelation, "linear_issue_enqueue", "work item belongs to a different Product", false, "supply a work item in the requested Product")
	}
	mode, err := resolveLinearPlanningTargetCore(ctx, q, productID)
	if err != nil {
		return linearIssueEnqueuePlan{}, err
	}
	if mode.PlanningMode == PlanningModeLocalOnly {
		return linearIssueEnqueuePlan{}, newFailure(KindInvalidOperation, "linear_issue_enqueue", "planning mode is local_only", false, "set planning_mode to linear_enabled before enqueueing Linear issues")
	}
	connection, err := readLinearConnectionCore(ctx, q, productID)
	if err != nil {
		return linearIssueEnqueuePlan{}, err
	}
	if connection.State != LinearConnectionDeclared {
		return linearIssueEnqueuePlan{}, newFailure(KindInvalidOperation, "linear_issue_enqueue", "linear_enabled Product has no declared Linear connection", false, "declare the connection as a managed saas_account resource or set the mode back to local_only")
	}
	projectID, projectName, err := resolveLinearProjectCore(ctx, q, workID, productID)
	if err != nil {
		return linearIssueEnqueuePlan{}, err
	}
	linearProjectID := connection.ProjectIDs[projectID]
	if linearProjectID == "" {
		return linearIssueEnqueuePlan{}, newFailure(KindInvalidRelation, "linear_issue_enqueue", fmt.Sprintf("Concord project %q is not mapped in Linear project_ids", projectName), false, "add the owning Concord project to project_ids before enqueueing Linear issues")
	}
	createLink := false
	if opKind == LinearOpIssueUpdate {
		var linkState string
		err := q.QueryRowContext(ctx, `SELECT link_state FROM linear_issue_links WHERE work_id=?`, workID).Scan(&linkState)
		if err == sql.ErrNoRows {
			return linearIssueEnqueuePlan{}, newFailure(KindInvalidOperation, "linear_issue_enqueue", "no link exists to update", false, "enqueue issue_create first; an update addresses the linked remote issue")
		} else if err != nil {
			return linearIssueEnqueuePlan{}, wrapFailure(KindUnavailable, "linear_issue_enqueue", "cannot read link", true, "retry once the database is readable", err)
		}
	}
	clientUUID := newLinearClientUUID()
	statusID, err := linearLifecycleStatusID(connection, lifecycle)
	if err != nil {
		return linearIssueEnqueuePlan{}, err
	}
	if opKind == LinearOpIssueCreate {
		var linkState string
		if err := q.QueryRowContext(ctx, `SELECT link_state FROM linear_issue_links WHERE work_id=?`, workID).Scan(&linkState); err == sql.ErrNoRows {
			createLink = true
		} else if err != nil {
			return linearIssueEnqueuePlan{}, wrapFailure(KindUnavailable, "linear_issue_enqueue", "cannot read link", true, "retry once the database is readable", err)
		}
	}
	premise, err := readCurrentWorkflowPremiseCore(ctx, q, workID)
	if err != nil {
		return linearIssueEnqueuePlan{}, err
	}
	description := composeLinearIssueBody(valueStatement, premise, workID)
	payload, err := json.Marshal(linearPayload{ClientUUID: clientUUID, ProductID: productID, Title: title, Description: description, TeamID: connection.TeamID, ProjectID: linearProjectID, ConnectionVersion: connection.Version, Lifecycle: lifecycle, StatusID: statusID})
	if err != nil {
		return linearIssueEnqueuePlan{}, wrapFailure(KindUnavailable, "linear_issue_enqueue", "cannot encode payload", true, "retry the enqueue", err)
	}
	entry := ClaimedLinearOperation{OperationID: "linear-" + clientUUID, WorkID: workID, OpKind: opKind, IdempotencyKey: clientUUID, Payload: payload}
	return linearIssueEnqueuePlan{entry: entry, payload: payload, createLink: createLink}, nil
}

func persistLinearIssueEnqueueTx(ctx context.Context, tx *sql.Tx, plan linearIssueEnqueuePlan, now string) (ClaimedLinearOperation, error) {
	entry := plan.entry
	if _, err := tx.ExecContext(ctx, `INSERT INTO linear_outbox(operation_id, work_id, op_kind, idempotency_key, payload, state, attempts, last_error, created_at, updated_at) VALUES (?,?,?,?,?,'queued',0,'',?,?)`,
		entry.OperationID, entry.WorkID, entry.OpKind, entry.IdempotencyKey, string(plan.payload), now, now); err != nil {
		return ClaimedLinearOperation{}, wrapFailure(KindUnavailable, "linear_issue_enqueue", "cannot queue operation", true, "retry once the database is writable", err)
	}
	// The link starts unpublished carrying the client UUID as its placeholder
	// remote identity; the drain replaces it with the confirmed identity. A
	// re-enqueue keeps the existing link — one link per work item, many
	// operations.
	if plan.createLink {
		if _, err := tx.ExecContext(ctx, `INSERT INTO linear_issue_links(work_id, remote_issue_uuid, human_key, url, remote_updated_at, content_hash, link_state, created_at, updated_at) VALUES (?,?,?,?,?,?,?, ?, ?)`,
			entry.WorkID, entry.IdempotencyKey, "", "", "", "", LinearLinkUnpublished, now, now); err != nil {
			return ClaimedLinearOperation{}, wrapFailure(KindUnavailable, "linear_issue_enqueue", "cannot create link", true, "retry once the database is writable", err)
		}
	}
	return entry, nil
}

// EnqueueLinearIssueAdoption queues one issue_adopt operation that adopts an
// existing Linear issue for an unlinked work item. The drain resolves the
// named issue, verifies its team, and completes a confirmed link carrying the
// resolved identity, so identifying the correct existing issue never forces
// duplicate issue creation.
func (s *Store) EnqueueLinearIssueAdoption(ctx context.Context, productID, workID, remoteIssueUUID string) (ClaimedLinearOperation, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ClaimedLinearOperation{}, wrapFailure(KindUnavailable, "linear_issue_adopt_enqueue", "cannot open adoption transaction", true, "retry once the database is writable", err)
	}
	defer tx.Rollback()
	if err := enterFold(ctx, tx); err != nil {
		return ClaimedLinearOperation{}, err
	}
	entry, err := enqueueLinearIssueAdoptionCore(ctx, tx, productID, workID, remoteIssueUUID)
	if err != nil {
		return ClaimedLinearOperation{}, err
	}
	if _, err := persistLinearIssueEnqueueTx(ctx, tx, linearIssueEnqueuePlan{entry: entry, payload: entry.Payload}, s.now().UTC().Format(time.RFC3339Nano)); err != nil {
		return ClaimedLinearOperation{}, err
	}
	if err := leaveFold(ctx, tx); err != nil {
		return ClaimedLinearOperation{}, err
	}
	if err := tx.Commit(); err != nil {
		return ClaimedLinearOperation{}, wrapFailure(KindUnavailable, "linear_issue_adopt_enqueue", "cannot commit queued adoption", true, "retry once the database is writable", err)
	}
	return entry, nil
}

// EnqueueLinearIssueAdoptionTx is the transaction-scoped adoption enqueue the
// agent mutation seam runs inside its own fold-guarded transaction.
func EnqueueLinearIssueAdoptionTx(ctx context.Context, transaction *Transaction, workID, remoteIssueUUID string) (ClaimedLinearOperation, error) {
	tx, err := transactionSQL(transaction, "linear_issue_adopt_enqueue")
	if err != nil {
		return ClaimedLinearOperation{}, err
	}
	if err := enterFold(ctx, tx); err != nil {
		return ClaimedLinearOperation{}, err
	}
	entry, err := enqueueLinearIssueAdoptionCore(ctx, tx, "", workID, remoteIssueUUID)
	if err != nil {
		return ClaimedLinearOperation{}, err
	}
	if _, err := persistLinearIssueEnqueueTx(ctx, tx, linearIssueEnqueuePlan{entry: entry, payload: entry.Payload}, transaction.now().UTC().Format(time.RFC3339Nano)); err != nil {
		return ClaimedLinearOperation{}, err
	}
	if err := leaveFold(ctx, tx); err != nil {
		return ClaimedLinearOperation{}, err
	}
	return entry, nil
}

func enqueueLinearIssueAdoptionCore(ctx context.Context, q queryer, expectedProductID, workID, remoteIssueUUID string) (ClaimedLinearOperation, error) {
	if len(workID) < 2 || len(workID) > 128 {
		return ClaimedLinearOperation{}, newFailure(KindInvalidPayload, "linear_issue_adopt_enqueue", "work id must be 2 to 128 characters", false, "supply a bounded work id")
	}
	if len(remoteIssueUUID) < 2 || len(remoteIssueUUID) > 128 {
		return ClaimedLinearOperation{}, newFailure(KindInvalidPayload, "linear_issue_adopt_enqueue", "remote issue uuid must be 2 to 128 characters", false, "supply the bounded remote issue uuid to adopt")
	}
	var exists int
	if err := q.QueryRowContext(ctx, `SELECT 1 FROM work_items WHERE id=?`, workID).Scan(&exists); err == sql.ErrNoRows {
		return ClaimedLinearOperation{}, newFailure(KindUnknownScope, "linear_issue_adopt_enqueue", "work item does not exist", false, "supply an existing work item")
	} else if err != nil {
		return ClaimedLinearOperation{}, wrapFailure(KindUnavailable, "linear_issue_adopt_enqueue", "cannot read work item", true, "retry once the database is readable", err)
	}
	productID, err := resolveLinearProductCore(ctx, q, workID)
	if err != nil {
		return ClaimedLinearOperation{}, err
	}
	if expectedProductID != "" && expectedProductID != productID {
		return ClaimedLinearOperation{}, newFailure(KindInvalidRelation, "linear_issue_adopt_enqueue", "work item belongs to a different Product", false, "supply a work item in the requested Product")
	}
	mode, err := resolveLinearPlanningTargetCore(ctx, q, productID)
	if err != nil {
		return ClaimedLinearOperation{}, err
	}
	if mode.PlanningMode == PlanningModeLocalOnly {
		return ClaimedLinearOperation{}, newFailure(KindInvalidOperation, "linear_issue_adopt_enqueue", "planning mode is local_only", false, "set planning_mode to linear_enabled before adopting Linear issues")
	}
	connection, err := readLinearConnectionCore(ctx, q, productID)
	if err != nil {
		return ClaimedLinearOperation{}, err
	}
	if connection.State != LinearConnectionDeclared {
		return ClaimedLinearOperation{}, newFailure(KindInvalidOperation, "linear_issue_adopt_enqueue", "linear_enabled Product has no declared Linear connection", false, "declare the connection as a managed saas_account resource or set the mode back to local_only")
	}
	var linkState string
	err = q.QueryRowContext(ctx, `SELECT link_state FROM linear_issue_links WHERE work_id=?`, workID).Scan(&linkState)
	if err == nil && linkState == LinearLinkConfirmed {
		return ClaimedLinearOperation{}, newFailure(KindInvalidOperation, "linear_issue_adopt_enqueue", "the work item already holds a confirmed link", false, "record updates through issue_update instead of adopting another issue")
	} else if err != nil && err != sql.ErrNoRows {
		return ClaimedLinearOperation{}, wrapFailure(KindUnavailable, "linear_issue_adopt_enqueue", "cannot read link", true, "retry once the database is readable", err)
	}
	var linkedWorkID string
	err = q.QueryRowContext(ctx, `SELECT work_id FROM linear_issue_links WHERE remote_issue_uuid=?`, remoteIssueUUID).Scan(&linkedWorkID)
	if err == nil {
		return ClaimedLinearOperation{}, newFailure(KindInvalidRelation, "linear_issue_adopt_enqueue", "another work item already links that issue", false, "adopt an issue no other work item links")
	} else if err != sql.ErrNoRows {
		return ClaimedLinearOperation{}, wrapFailure(KindUnavailable, "linear_issue_adopt_enqueue", "cannot inspect existing links", true, "retry once the database is readable", err)
	}
	clientUUID := newLinearClientUUID()
	payload, err := json.Marshal(linearPayload{ClientUUID: clientUUID, ProductID: productID, TeamID: connection.TeamID, ConnectionVersion: connection.Version, RemoteIssueUUID: remoteIssueUUID})
	if err != nil {
		return ClaimedLinearOperation{}, wrapFailure(KindUnavailable, "linear_issue_adopt_enqueue", "cannot encode payload", true, "retry the adoption", err)
	}
	return ClaimedLinearOperation{OperationID: "linear-" + clientUUID, WorkID: workID, OpKind: LinearOpIssueAdopt, IdempotencyKey: clientUUID, Payload: payload}, nil
}

func enqueueLinearIssueForLifecycleTx(ctx context.Context, tx *sql.Tx, workID, lifecycle string, at time.Time) error {
	var table string
	if err := tx.QueryRowContext(ctx, `SELECT name FROM sqlite_schema WHERE type='table' AND name='linear_issue_links'`).Scan(&table); err == sql.ErrNoRows {
		return nil
	} else if err != nil {
		return wrapFailure(KindUnavailable, "linear_issue_enqueue", "cannot inspect Linear link schema", true, "retry once the database is readable", err)
	}
	var linkState string
	err := tx.QueryRowContext(ctx, `SELECT link_state FROM linear_issue_links WHERE work_id=?`, workID).Scan(&linkState)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil
		}
		return wrapFailure(KindUnavailable, "linear_issue_enqueue", "cannot read link", true, "retry once the database is readable", err)
	}
	if linkState != LinearLinkConfirmed {
		return nil
	}
	productID, err := resolveLinearProductCore(ctx, tx, workID)
	if err != nil {
		return err
	}
	mode, err := readProductPlanningModeCore(ctx, tx, productID)
	if err != nil {
		return err
	}
	if mode.PlanningMode == PlanningModeLocalOnly {
		return nil
	}
	connection, err := readLinearConnectionCore(ctx, tx, productID)
	if err != nil {
		return err
	}
	if connection.State != LinearConnectionDeclared || connection.StatusIDs[lifecycle] == "" {
		return nil
	}
	plan, err := enqueueLinearIssueForWorkCore(ctx, tx, productID, workID, LinearOpIssueUpdate)
	if err != nil {
		return err
	}
	_, err = persistLinearIssueEnqueueTx(ctx, tx, plan, at.UTC().Format(time.RFC3339Nano))
	return err
}

var linearIssueKeyPattern = regexp.MustCompile(`^[A-Z][A-Z0-9]*-[0-9]+$`)

// normalizeLinearIssueExternalRef recognizes the supported Linear issue
// identity forms and returns their canonical issue key or identity value.
func normalizeLinearIssueExternalRef(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "linear:") {
		identity := strings.TrimPrefix(value, "linear:")
		if identity != "" && !strings.ContainsAny(identity, " \t\r\n") {
			return identity, true
		}
		return "", false
	}
	if linearIssueKeyPattern.MatchString(value) {
		return value, true
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() != "linear.app" || parsed.User != nil {
		return "", false
	}
	// Linear renders an issue as /<workspace>/issue/<KEY>/<slug>, and the slug
	// is free text that may itself contain slashes. The key is the segment
	// after "issue"; everything beyond it is presentation and carries no
	// identity, so an exact segment count would reject the canonical form.
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) >= 3 && parts[0] != "" && parts[1] == "issue" && linearIssueKeyPattern.MatchString(parts[2]) {
		return parts[2], true
	}
	return "", false
}

// linearCaptureConfigurationRefusal reports whether a typed failure names a
// Linear configuration shape the capture enqueue treats as a silent no-op
// rather than a capture failure. Only availability failures propagate: a
// misconfigured Linear connection can never fail a capture.
func linearCaptureConfigurationRefusal(err error) bool {
	f, ok := err.(*Failure)
	if !ok {
		return false
	}
	switch f.Kind {
	case KindInvalidPayload, KindInvalidOperation, KindInvalidRelation, KindUnknownScope, KindAmbiguousScope:
		return true
	}
	return false
}

// enqueueLinearIssueForCaptureTx queues the issue_create that puts a freshly
// captured work item on the operator's Linear board from the instant its
// capture transaction commits. The capture membership fold precedes this call,
// so the owning Product resolves only here. Every configuration gap is a
// silent no-op — local_only mode, a missing declared connection, an unmapped
// owning Concord project, an unresolvable Product scope, an existing link row,
// an initiative kind, and an external_ref that already names a Linear issue —
// and terminal items are never published. It reports whether an operation was
// queued, with the queued entry when it was.
func enqueueLinearIssueForCaptureTx(ctx context.Context, tx *sql.Tx, workID string, at time.Time) (ClaimedLinearOperation, bool, error) {
	var table string
	if err := tx.QueryRowContext(ctx, `SELECT name FROM sqlite_schema WHERE type='table' AND name='linear_issue_links'`).Scan(&table); err == sql.ErrNoRows {
		return ClaimedLinearOperation{}, false, nil
	} else if err != nil {
		return ClaimedLinearOperation{}, false, wrapFailure(KindUnavailable, "linear_issue_enqueue", "cannot inspect Linear link schema", true, "retry once the database is readable", err)
	}
	var kind, externalRef string
	err := tx.QueryRowContext(ctx, `SELECT kind, coalesce(json_extract(intent_json, '$.external_ref'), '') FROM work_items WHERE id=? AND terminal_time IS NULL`, workID).Scan(&kind, &externalRef)
	if err == sql.ErrNoRows {
		return ClaimedLinearOperation{}, false, nil
	} else if err != nil {
		return ClaimedLinearOperation{}, false, wrapFailure(KindUnavailable, "linear_issue_enqueue", "cannot read work item", true, "retry once the database is readable", err)
	}
	if kind == "initiative" {
		return ClaimedLinearOperation{}, false, nil
	}
	if _, exists := normalizeLinearIssueExternalRef(externalRef); exists {
		return ClaimedLinearOperation{}, false, nil
	}
	var linked bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM linear_issue_links WHERE work_id=?)`, workID).Scan(&linked); err != nil {
		return ClaimedLinearOperation{}, false, wrapFailure(KindUnavailable, "linear_issue_enqueue", "cannot read link", true, "retry once the database is readable", err)
	}
	if linked {
		return ClaimedLinearOperation{}, false, nil
	}
	productID, err := resolveLinearProductCore(ctx, tx, workID)
	if err != nil {
		if linearCaptureConfigurationRefusal(err) {
			return ClaimedLinearOperation{}, false, nil
		}
		return ClaimedLinearOperation{}, false, err
	}
	mode, err := readProductPlanningModeCore(ctx, tx, productID)
	if err != nil {
		if linearCaptureConfigurationRefusal(err) {
			return ClaimedLinearOperation{}, false, nil
		}
		return ClaimedLinearOperation{}, false, err
	}
	if mode.PlanningMode == PlanningModeLocalOnly {
		return ClaimedLinearOperation{}, false, nil
	}
	connection, err := readLinearConnectionCore(ctx, tx, productID)
	if err != nil {
		if linearCaptureConfigurationRefusal(err) {
			return ClaimedLinearOperation{}, false, nil
		}
		return ClaimedLinearOperation{}, false, err
	}
	if connection.State != LinearConnectionDeclared {
		return ClaimedLinearOperation{}, false, nil
	}
	projectID, _, err := resolveLinearProjectCore(ctx, tx, workID, productID)
	if err != nil {
		if linearCaptureConfigurationRefusal(err) {
			return ClaimedLinearOperation{}, false, nil
		}
		return ClaimedLinearOperation{}, false, err
	}
	if connection.ProjectIDs[projectID] == "" {
		return ClaimedLinearOperation{}, false, nil
	}
	plan, err := enqueueLinearIssueForWorkCore(ctx, tx, productID, workID, LinearOpIssueCreate)
	if err != nil {
		return ClaimedLinearOperation{}, false, err
	}
	entry, err := persistLinearIssueEnqueueTx(ctx, tx, plan, at.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return ClaimedLinearOperation{}, false, err
	}
	return entry, true, nil
}

// BackfillLinearIssueCreates queues issue_create operations for every work
// item of one Product that a capture predating the capture-time enqueue left
// unlinked. Eligibility matches the capture enqueue exactly: non-terminal,
// not an initiative, no link row, and no Linear identity in the external_ref.
// Configuration gaps skip an item instead of failing the backfill. Publication
// stays with the operator-run linear outbox-drain command.
func (s *Store) BackfillLinearIssueCreates(ctx context.Context, productID string) ([]ClaimedLinearOperation, error) {
	mode, err := readProductPlanningModeCore(ctx, s.db, productID)
	if err != nil {
		return nil, err
	}
	if mode.PlanningMode == PlanningModeLocalOnly {
		return nil, newFailure(KindInvalidOperation, "linear_backfill", "planning mode is local_only", false, "set planning_mode to linear_enabled before backfilling Linear issues")
	}
	if _, err := resolveLinearPlanningTargetCore(ctx, s.db, productID); err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "linear_backfill", "cannot open backfill transaction", true, "retry once the database is writable", err)
	}
	defer tx.Rollback()
	if err := enterFold(ctx, tx); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `
SELECT DISTINCT w.id
FROM work_items w
JOIN work_projects wp ON wp.work_id = w.id
JOIN product_projects pp ON pp.project_id = wp.project_id
WHERE pp.product_id = ? AND w.terminal_time IS NULL
ORDER BY w.created_at, w.id`, productID)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "linear_backfill", "cannot read backfill candidates", true, "retry once the database is readable", err)
	}
	var workIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, wrapFailure(KindUnavailable, "linear_backfill", "cannot scan backfill candidates", true, "retry once the database is readable", err)
		}
		workIDs = append(workIDs, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, wrapFailure(KindUnavailable, "linear_backfill", "cannot finish backfill candidate read", true, "retry once the database is readable", err)
	}
	rows.Close()
	now := s.now()
	enqueued := make([]ClaimedLinearOperation, 0, len(workIDs))
	for _, workID := range workIDs {
		entry, queued, err := enqueueLinearIssueForCaptureTx(ctx, tx, workID, now)
		if err != nil {
			return nil, err
		}
		if queued {
			enqueued = append(enqueued, entry)
		}
	}
	if err := leaveFold(ctx, tx); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, wrapFailure(KindUnavailable, "linear_backfill", "cannot commit backfill", true, "retry once the database is writable", err)
	}
	return enqueued, nil
}

// ClaimLinearOperations moves up to limit queued operations to in_flight and
// returns exactly the operations this caller claimed. The state guard in the
// UPDATE makes the claim exclusive: an operation another caller already
// claimed stays out of this result.
func (s *Store) ClaimLinearOperations(ctx context.Context, limit int64) ([]ClaimedLinearOperation, error) {
	return s.claimLinearOperations(ctx, "", limit)
}

// ClaimLinearOperationsForProduct claims only operations whose work item is
// a member of the requested Product. The Product filter is inside the claim
// transaction so one drain cannot send another Product's operation.
func (s *Store) ClaimLinearOperationsForProduct(ctx context.Context, productID string, limit int64) ([]ClaimedLinearOperation, error) {
	if productID == "" {
		return nil, newFailure(KindInvalidPayload, "linear_outbox_claim", "Product id is required", false, "supply the Product being drained")
	}
	return s.claimLinearOperations(ctx, productID, limit)
}

func (s *Store) claimLinearOperations(ctx context.Context, productID string, limit int64) ([]ClaimedLinearOperation, error) {
	if limit < 1 || limit > 25 {
		return nil, newFailure(KindInvalidPayload, "linear_outbox_claim", "limit must be 1 to 25", false, "bound each drain pass to 25 operations")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "linear_outbox_claim", "cannot open claim transaction", true, "retry once the database is writable", err)
	}
	defer tx.Rollback()
	if err := enterFold(ctx, tx); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT operation_id FROM linear_outbox WHERE state=? AND (?='' OR json_extract(payload, '$.product_id')=? OR json_type(payload, '$.product_id') IS NULL) ORDER BY created_at, rowid LIMIT ?`, LinearOutboxQueued, productID, productID, limit)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "linear_outbox_claim", "cannot read the queue", true, "retry once the database is readable", err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, wrapFailure(KindUnavailable, "linear_outbox_claim", "cannot scan the queue", true, "retry once the database is readable", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, wrapFailure(KindUnavailable, "linear_outbox_claim", "cannot finish the queue read", true, "retry once the database is readable", err)
	}
	rows.Close()
	now := s.now().UTC().Format(time.RFC3339Nano)
	claimed := make([]ClaimedLinearOperation, 0, len(ids))
	for _, id := range ids {
		result, err := tx.ExecContext(ctx, `UPDATE linear_outbox SET state=?, attempts=attempts+1, updated_at=? WHERE operation_id=? AND state=?`, LinearOutboxInFlight, now, id, LinearOutboxQueued)
		if err != nil {
			return nil, wrapFailure(KindUnavailable, "linear_outbox_claim", "cannot claim operation", true, "retry once the database is writable", err)
		}
		changed, err := result.RowsAffected()
		if err != nil || changed == 0 {
			continue
		}
		var op ClaimedLinearOperation
		var payload string
		if err := tx.QueryRowContext(ctx, `SELECT operation_id, work_id, op_kind, idempotency_key, attempts, payload FROM linear_outbox WHERE operation_id=?`, id).Scan(&op.OperationID, &op.WorkID, &op.OpKind, &op.IdempotencyKey, &op.Attempts, &payload); err != nil {
			return nil, wrapFailure(KindUnavailable, "linear_outbox_claim", "cannot read claimed operation", true, "retry once the database is readable", err)
		}
		op.Payload = json.RawMessage(payload)
		claimed = append(claimed, op)
	}
	if err := leaveFold(ctx, tx); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, wrapFailure(KindUnavailable, "linear_outbox_claim", "cannot commit claim", true, "retry once the database is writable", err)
	}
	return claimed, nil
}

// CompleteLinearOperation atomically marks an in_flight operation done and
// records the remote identity on its link.
func (s *Store) CompleteLinearOperation(ctx context.Context, operationID string, identity LinearRemoteIdentity) error {
	return s.completeLinearOperation(ctx, operationID, &identity)
}

// CompleteSupersededLinearOperation marks an older in-flight update done while
// leaving the link owned by the newer operation unchanged.
func (s *Store) CompleteSupersededLinearOperation(ctx context.Context, operationID string) error {
	return s.completeLinearOperation(ctx, operationID, nil)
}

func (s *Store) completeLinearOperation(ctx context.Context, operationID string, identity *LinearRemoteIdentity) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return wrapFailure(KindUnavailable, "linear_outbox_complete", "cannot open completion transaction", true, "retry once the database is writable", err)
	}
	defer tx.Rollback()
	if err := enterFold(ctx, tx); err != nil {
		return err
	}
	var workID, opKind, state string
	if err := tx.QueryRowContext(ctx, `SELECT work_id, op_kind, state FROM linear_outbox WHERE operation_id=?`, operationID).Scan(&workID, &opKind, &state); err == sql.ErrNoRows {
		return newFailure(KindUnknownScope, "linear_outbox_complete", "queued operation does not exist", false, "supply an in-flight operation id")
	} else if err != nil {
		return wrapFailure(KindUnavailable, "linear_outbox_complete", "cannot read the completed operation", true, "retry once the database is readable", err)
	}
	if state != LinearOutboxInFlight {
		return newFailure(KindInvalidTransition, "linear_outbox_complete", fmt.Sprintf("outbox state is %s, not %s", state, LinearOutboxInFlight), false, "reload the operation and complete it only while in flight")
	}
	if _, err := tx.ExecContext(ctx, `UPDATE linear_outbox SET state=?, last_error='', updated_at=? WHERE operation_id=? AND state=?`, LinearOutboxDone, s.now().UTC().Format(time.RFC3339Nano), operationID, LinearOutboxInFlight); err != nil {
		return wrapFailure(KindUnavailable, "linear_outbox_complete", "cannot mark operation done", true, "retry once the database is writable", err)
	}
	if identity != nil {
		if err := completeLinearLinkTx(ctx, tx, workID, opKind, *identity, s.now().UTC().Format(time.RFC3339Nano)); err != nil {
			return err
		}
	}
	if err := leaveFold(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return wrapFailure(KindUnavailable, "linear_outbox_complete", "cannot commit completed operation", true, "retry once the database is writable", err)
	}
	return nil
}

// HasNewerLinearIssueUpdate reports whether a non-failed update for the same
// work item was queued after operationID. The rowid is the persisted
// insertion order, so equal created_at values keep the order the operations
// were enqueued in; a random operation id would order them arbitrarily.
func (s *Store) HasNewerLinearIssueUpdate(ctx context.Context, operationID string) (bool, error) {
	var workID, opKind string
	var rowID int64
	err := s.db.QueryRowContext(ctx, `SELECT work_id, op_kind, rowid FROM linear_outbox WHERE operation_id=?`, operationID).Scan(&workID, &opKind, &rowID)
	if err == sql.ErrNoRows {
		return false, newFailure(KindUnknownScope, "linear_outbox_staleness", "queued operation does not exist", false, "supply an existing operation id")
	}
	if err != nil {
		return false, wrapFailure(KindUnavailable, "linear_outbox_staleness", "cannot read queued operation", true, "retry once the database is readable", err)
	}
	if opKind != LinearOpIssueUpdate {
		return false, nil
	}
	var newer bool
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM linear_outbox
		WHERE work_id=? AND op_kind=? AND state<>? AND rowid>?
	)`, workID, LinearOpIssueUpdate, LinearOutboxFailed, rowID).Scan(&newer); err != nil {
		return false, wrapFailure(KindUnavailable, "linear_outbox_staleness", "cannot inspect newer issue updates", true, "retry once the database is readable", err)
	}
	return newer, nil
}

func completeLinearLinkTx(ctx context.Context, tx *sql.Tx, workID, opKind string, identity LinearRemoteIdentity, now string) error {
	var currentState string
	err := tx.QueryRowContext(ctx, `SELECT link_state FROM linear_issue_links WHERE work_id=?`, workID).Scan(&currentState)
	if err == sql.ErrNoRows {
		if _, err := tx.ExecContext(ctx, `INSERT INTO linear_issue_links(work_id, remote_issue_uuid, human_key, url, remote_updated_at, content_hash, link_state, created_at, updated_at) VALUES (?,?,?,?,?,?,?, ?, ?)`, workID, identity.RemoteUUID, identity.HumanKey, identity.URL, identity.RemoteUpdatedAt, identity.ContentHash, LinearLinkConfirmed, now, now); err != nil {
			return wrapFailure(KindUnavailable, "linear_outbox_complete", "cannot create completed link", true, "retry once the database is writable", err)
		}
		return nil
	}
	if err != nil {
		return wrapFailure(KindUnavailable, "linear_outbox_complete", "cannot read link", true, "retry once the database is readable", err)
	}
	if currentState != LinearLinkUnpublished && currentState != LinearLinkPending && currentState != LinearLinkConfirmed && currentState != LinearLinkDegraded {
		return newFailure(KindInvalidTransition, "linear_outbox_complete", "link state is not recognized", false, "repair the stored Linear link state")
	}
	if currentState == LinearLinkConfirmed && opKind != LinearOpIssueUpdate {
		detail := "a create operation cannot complete an already confirmed link"
		if opKind == LinearOpIssueAdopt {
			detail = "an adopt operation cannot complete an already confirmed link"
		}
		return newFailure(KindInvalidTransition, "linear_outbox_complete", detail, false, "complete the operation that owns the linked issue")
	}
	var linkedWorkID string
	err = tx.QueryRowContext(ctx, `SELECT work_id FROM linear_issue_links WHERE remote_issue_uuid=?`, identity.RemoteUUID).Scan(&linkedWorkID)
	if err == nil && linkedWorkID != workID {
		return newFailure(KindInvalidRelation, "linear_outbox_complete", "another work item already links that issue", false, "complete the operation that owns the linked issue")
	} else if err != nil && err != sql.ErrNoRows {
		return wrapFailure(KindUnavailable, "linear_outbox_complete", "cannot inspect existing links", true, "retry once the database is readable", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE linear_issue_links SET remote_issue_uuid=?, human_key=?, url=?, remote_updated_at=?, content_hash=?, link_state=?, updated_at=? WHERE work_id=?`, identity.RemoteUUID, identity.HumanKey, identity.URL, identity.RemoteUpdatedAt, identity.ContentHash, LinearLinkConfirmed, now, workID); err != nil {
		return wrapFailure(KindUnavailable, "linear_outbox_complete", "cannot confirm completed link", true, "retry once the database is writable", err)
	}
	return nil
}

// FailLinearOperation records one failed attempt. A retryable class returns
// the operation to queued with attempts and last_error advanced; a permanent
// class, or a retryable class at the attempt bound, lands in failed.
func (s *Store) FailLinearOperation(ctx context.Context, operationID, class, detail string) error {
	if class != "retryable" && class != "permanent" {
		return newFailure(KindInvalidPayload, "linear_outbox_transition", "failure class is not recognized", false, "use retryable or permanent")
	}
	if detail == "" {
		return newFailure(KindInvalidPayload, "linear_outbox_transition", "failure reason is required", false, "state why the operation failed")
	}
	var state string
	var attempts int
	err := s.db.QueryRowContext(ctx, `SELECT state, attempts FROM linear_outbox WHERE operation_id=?`, operationID).Scan(&state, &attempts)
	if err == sql.ErrNoRows {
		return newFailure(KindUnknownScope, "linear_outbox_transition", "queued operation does not exist", false, "supply a queued operation id")
	} else if err != nil {
		return wrapFailure(KindUnavailable, "linear_outbox_transition", "cannot read queued operation", true, "retry once the database is readable", err)
	}
	if state != LinearOutboxInFlight {
		return newFailure(KindInvalidTransition, "linear_outbox_transition", fmt.Sprintf("outbox state is %s, not %s", state, LinearOutboxInFlight), false, "reload the operation and transition from its current state")
	}
	target := LinearOutboxFailed
	if class == "retryable" && attempts < linearMaxAttempts {
		target = LinearOutboxQueued
	}
	return s.linearOutboxTransition(ctx, operationID, LinearOutboxInFlight, target, detail)
}

// ImportedLinearInitiative is the result of a one-way drafting-pad import.
type ImportedLinearInitiative struct {
	WorkID      string `json:"work_id"`
	ExternalRef string `json:"external_ref"`
	Title       string `json:"title"`
}

// ImportLinearInitiative imports one Linear initiative as a Concord initiative
// work item with external_ref linear:<uuid>. The import is one-way and once:
// a repeated import of the same remote identity refuses with a typed
// duplicate, and nothing here ever writes back to Linear.
func (s *Store) ImportLinearInitiative(ctx context.Context, productID, remoteUUID, name, description string) (ImportedLinearInitiative, error) {
	if len(remoteUUID) < 2 || len(remoteUUID) > 128 {
		return ImportedLinearInitiative{}, newFailure(KindInvalidPayload, "linear_initiative_import", "remote initiative uuid must be 2 to 128 characters", false, "supply the Linear initiative uuid")
	}
	if name == "" || len(name) > 256 {
		return ImportedLinearInitiative{}, newFailure(KindInvalidPayload, "linear_initiative_import", "initiative name must be 1 to 256 characters", false, "supply the Linear initiative name")
	}
	mode, err := s.ResolveLinearPlanningTarget(ctx, productID)
	if err != nil {
		return ImportedLinearInitiative{}, err
	}
	if mode.PlanningMode == PlanningModeLocalOnly {
		return ImportedLinearInitiative{}, newFailure(KindInvalidOperation, "linear_initiative_import", "planning mode is local_only", false, "set planning_mode to linear_enabled before importing Linear initiatives")
	}
	externalRef := "linear:" + remoteUUID
	var existing string
	err = s.db.QueryRowContext(ctx, `SELECT id FROM work_items WHERE json_extract(intent_json, '$.external_ref')=? LIMIT 1`, externalRef).Scan(&existing)
	if err == nil {
		return ImportedLinearInitiative{}, newFailure(KindIdempotencyConflict, "linear_initiative_import", "initiative is already imported", false, "reuse the imported work item "+existing)
	} else if err != sql.ErrNoRows {
		return ImportedLinearInitiative{}, wrapFailure(KindUnavailable, "linear_initiative_import", "cannot read existing imports", true, "retry once the database is readable", err)
	}
	var projectID string
	err = s.db.QueryRowContext(ctx, `SELECT project_id FROM product_projects WHERE product_id=? AND role='primary'`, productID).Scan(&projectID)
	if err == sql.ErrNoRows {
		return ImportedLinearInitiative{}, newFailure(KindAmbiguousScope, "linear_initiative_import", "Product has no primary Project", false, "give the Product a primary Project before importing")
	} else if err != nil {
		return ImportedLinearInitiative{}, wrapFailure(KindUnavailable, "linear_initiative_import", "cannot read the primary Project", true, "retry once the database is readable", err)
	}
	valueStatement := description
	if valueStatement == "" {
		valueStatement = "Imported one-way from Linear initiative " + name + "; no outbound sync."
	}
	if len(valueStatement) > 256 {
		valueStatement = valueStatement[:253] + "..."
	}
	digest := sha256.Sum256([]byte("linear-initiative-import:" + externalRef))
	workID := "initiative-" + hex.EncodeToString(digest[:])[7:31]
	payload, err := json.Marshal(map[string]any{"work_kind": "initiative", "title": name, "value_statement": valueStatement, "priority": 0, "urgency": "standard", "tags": []string{"linear-import"}, "external_ref": externalRef})
	if err != nil {
		return ImportedLinearInitiative{}, wrapFailure(KindUnavailable, "linear_initiative_import", "cannot encode payload", true, "retry the import", err)
	}
	membershipPayload, err := json.Marshal(map[string]any{"memberships": []map[string]any{{"project_id": projectID, "role": "primary"}}, "expected_version": 1, "resulting_version": 2})
	if err != nil {
		return ImportedLinearInitiative{}, wrapFailure(KindUnavailable, "linear_initiative_import", "cannot encode membership payload", true, "retry the import", err)
	}
	now := s.now().UTC()
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{
		{EventID: "linear-initiative-import:" + externalRef + ":create", Kind: "work.created", SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "operator", OccurredAt: now, PayloadVersion: 2, Payload: payload},
		{EventID: "linear-initiative-import:" + externalRef + ":memberships", Kind: "work.memberships_replaced", SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "operator", OccurredAt: now, PayloadVersion: 1, Payload: membershipPayload},
	}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): 0}}); err != nil {
		return ImportedLinearInitiative{}, err
	}
	return ImportedLinearInitiative{WorkID: workID, ExternalRef: externalRef, Title: name}, nil
}

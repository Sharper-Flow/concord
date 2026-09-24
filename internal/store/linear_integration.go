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
	LinearOpProjectCreate    = "project_create"
	LinearOpProjectUpdate    = "project_update"
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
	StatusIDs    map[string]string `json:"status_ids,omitempty"`
	LabelIDs     map[string]string `json:"label_ids,omitempty"`
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
		if encoded, exists := linear["label_ids"]; exists {
			rawLabels, marshalErr := json.Marshal(encoded)
			var labelIDs map[string]json.RawMessage
			if marshalErr != nil || json.Unmarshal(rawLabels, &labelIDs) != nil || labelIDs == nil {
				return LinearConnection{}, newFailure(KindInvalidPayload, "linear_connection_read", "Linear label_ids must be an object", false, "supply work kind and urgency to Linear label id mappings")
			}
			candidate.LabelIDs = make(map[string]string, len(labelIDs))
			for key, value := range labelIDs {
				if !linearLabelMappingKeyRecognized(key) {
					return LinearConnection{}, newFailure(KindInvalidPayload, "linear_connection_read", "Linear label mapping key is not recognized", false, "map task, bug, decision, research, other, expedite, optional, or project:<concord project id>")
				}
				var labelID string
				if err := json.Unmarshal(value, &labelID); err != nil {
					return LinearConnection{}, newFailure(KindInvalidPayload, "linear_connection_read", "Linear label id must be a string", false, "supply work kind and urgency to Linear label id mappings")
				}
				if err := validateLinearConnectionID(labelID, "label id"); err != nil {
					return LinearConnection{}, err
				}
				candidate.LabelIDs[key] = labelID
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

// LinearConnectionUpdateRequest changes the destination, status mapping, and label mapping
// owned by one Product's managed Linear resource. Nil maps keep their existing
// values. Non-nil maps replace the complete mapping.
type LinearConnectionUpdateRequest struct {
	EventID                 string
	ResourceID              string
	ProductID               string
	TeamID                  string
	StatusIDs               map[string]string
	LabelIDs                map[string]string
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
	if req.StatusIDs != nil {
		if err := validateLinearStatusMapping(req.StatusIDs, "linear_connection_update"); err != nil {
			return err
		}
	}
	if req.LabelIDs != nil {
		if err := validateLinearLabelMapping(ctx, s.db, req.LabelIDs, "linear_connection_update"); err != nil {
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
	// CD-0171 removed the repository-to-Linear-Project mapping. Migration
	// 100 strips the retired project_id and project_ids members from every
	// stored document, and a document re-entering afterward through the
	// generic resource surface loses them here, so the stored resource
	// converges on the current convention whenever the connection is
	// updated.
	delete(linear, "project_id")
	delete(linear, "project_ids")
	if req.StatusIDs != nil {
		linear["status_ids"], _ = json.Marshal(req.StatusIDs)
	}
	if req.LabelIDs != nil {
		linear["label_ids"], _ = json.Marshal(req.LabelIDs)
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

var linearLabelMappingKeys = map[string]bool{
	"task": true, "bug": true, "decision": true, "research": true, "other": true, "expedite": true,
}

// LinearLabelOptionalKey maps the label that marks a non-required Initiative
// entry (CD-0171 D5), and LinearLabelProjectPrefix names the repository label
// keys: project:<concord project id> carries one Concord project membership
// of the work item (CD-0171 D3). The drain reads the same keys to decide
// which stale remote labels it may remove.
const (
	LinearLabelOptionalKey   = "optional"
	LinearLabelProjectPrefix = "project:"
)

// linearLabelMappingKeyRecognized reports whether a label mapping key is a
// fixed work-kind or urgency key, the optional key, or a repository key whose
// suffix is a bounded Concord project id. Write-path validation additionally
// requires the project to be registered (validateLinearLabelMapping); the
// read path checks shape only, so a connection stays readable while a mapped
// project is being re-registered.
func linearLabelMappingKeyRecognized(key string) bool {
	if linearLabelMappingKeys[key] || key == LinearLabelOptionalKey {
		return true
	}
	if projectID, ok := strings.CutPrefix(key, LinearLabelProjectPrefix); ok {
		return validateLinearConnectionID(projectID, "Concord project id") == nil
	}
	return false
}

// validateLinearLabelMapping accepts the fixed work-kind and urgency keys,
// the optional key, and project:<concord project id> keys that name a
// registered Concord project.
func validateLinearLabelMapping(ctx context.Context, q queryer, labelIDs map[string]string, operation string) error {
	for key, labelID := range labelIDs {
		if !linearLabelMappingKeyRecognized(key) {
			return newFailure(KindInvalidPayload, operation, "label mapping key is not recognized", false, "map task, bug, decision, research, other, expedite, optional, or project:<concord project id>")
		}
		if projectID, ok := strings.CutPrefix(key, LinearLabelProjectPrefix); ok {
			var registered int
			if err := q.QueryRowContext(ctx, `SELECT 1 FROM projects WHERE id=?`, projectID).Scan(&registered); err == sql.ErrNoRows {
				return newFailure(KindUnknownScope, operation, "label mapping names a Concord project that does not exist", false, "register the Concord project before mapping it to a Linear label")
			} else if err != nil {
				return wrapFailure(KindUnavailable, operation, "cannot read the mapped Concord project", true, "retry once the database is readable", err)
			}
		}
		if err := validateLinearConnectionID(labelID, "label id"); err != nil {
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
	if entry.OpKind != LinearOpIssueCreate && entry.OpKind != LinearOpIssueUpdate && entry.OpKind != LinearOpIssueAdopt && entry.OpKind != LinearOpProjectCreate && entry.OpKind != LinearOpProjectUpdate {
		return newFailure(KindInvalidPayload, "linear_outbox_enqueue", "operation kind is not recognized", false, "use issue_create, issue_update, issue_adopt, project_create, or project_update")
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

// LinearLinkedWork is the local state paired with one confirmed Linear link.
// The remote state is deliberately absent. The divergence route owns that
// comparison after it reads the remote issue.
type LinearLinkedWork struct {
	LinearIssueLink
	Lifecycle string `json:"lifecycle"`
}

// LinearOutboxDisposition records an operator decision about a failed
// operation. It does not change the failed outbox row or create a retry path.
type LinearOutboxDisposition struct {
	OperationID string `json:"operation_id"`
	WorkID      string `json:"work_id"`
	Disposition string `json:"disposition"`
	Reason      string `json:"reason"`
	CreatedAt   string `json:"created_at"`
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

// ReadConfirmedLinearLinkedWorkForProduct returns confirmed links and the
// authoritative local lifecycle for one Product.
func (s *Store) ReadConfirmedLinearLinkedWorkForProduct(ctx context.Context, productID string) ([]LinearLinkedWork, error) {
	if _, err := readProductPlanningModeCore(ctx, s.db, productID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT l.work_id, l.remote_issue_uuid, l.human_key, l.url, l.link_state, l.content_hash, w.lifecycle
FROM linear_issue_links l
JOIN work_items w ON w.id=l.work_id
WHERE l.link_state=? AND EXISTS (
    SELECT 1 FROM work_projects wp
    JOIN product_projects pp ON pp.project_id=wp.project_id
    WHERE wp.work_id=l.work_id AND pp.product_id=?
)
ORDER BY l.work_id`, LinearLinkConfirmed, productID)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "linear_divergence_read", "cannot read confirmed Linear links", true, "retry once the database is readable", err)
	}
	defer rows.Close()
	linked := make([]LinearLinkedWork, 0)
	for rows.Next() {
		var item LinearLinkedWork
		if err := rows.Scan(&item.WorkID, &item.RemoteIssueUUID, &item.HumanKey, &item.URL, &item.LinkState, &item.ContentHash, &item.Lifecycle); err != nil {
			return nil, wrapFailure(KindUnavailable, "linear_divergence_read", "cannot scan confirmed Linear link", true, "retry once the database is readable", err)
		}
		linked = append(linked, item)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapFailure(KindUnavailable, "linear_divergence_read", "cannot finish confirmed Linear link read", true, "retry once the database is readable", err)
	}
	return linked, nil
}

// sweepLinkBatch bounds one anti-join statement's JSON payload. The statement
// text is a compile-time constant with one bound parameter, so no SQL text is
// ever composed at runtime.
const sweepLinkBatch = 100

const linkedRemoteIssueQuery = `SELECT DISTINCT j.value FROM json_each(?) j JOIN linear_issue_links l ON l.remote_issue_uuid = j.value`

// LinkedRemoteIssueUUIDs reports which of the given remote issue UUIDs hold a
// link row in any state. The unlinked remote sweep uses it as the inner side
// of its anti-join: an enumerated remote issue absent from the result holds no
// link and is unknown to Concord.
func (s *Store) LinkedRemoteIssueUUIDs(ctx context.Context, remoteIssueUUIDs []string) (map[string]bool, error) {
	linked := make(map[string]bool)
	for start := 0; start < len(remoteIssueUUIDs); start += sweepLinkBatch {
		encoded, err := json.Marshal(remoteIssueUUIDs[start:min(start+sweepLinkBatch, len(remoteIssueUUIDs))])
		if err != nil {
			return nil, wrapFailure(KindInvalidPayload, "linear_unlinked_remote_read", "cannot encode the remote issue uuids", false, "supply plain remote issue uuids", err)
		}
		rows, err := s.db.QueryContext(ctx, linkedRemoteIssueQuery, string(encoded))
		if err != nil {
			return nil, wrapFailure(KindUnavailable, "linear_unlinked_remote_read", "cannot read link rows for the remote issues", true, "retry once the database is readable", err)
		}
		for rows.Next() {
			var uuid string
			if err := rows.Scan(&uuid); err != nil {
				rows.Close()
				return nil, wrapFailure(KindUnavailable, "linear_unlinked_remote_read", "cannot scan link rows for the remote issues", true, "retry once the database is readable", err)
			}
			linked[uuid] = true
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, wrapFailure(KindUnavailable, "linear_unlinked_remote_read", "cannot finish link row read", true, "retry once the database is readable", err)
		}
		rows.Close()
	}
	return linked, nil
}

// ReadConfirmedLinearLinksForProduct returns the confirmed issue links whose
// work items belong to the Product through any project membership and whose
// last identity check is older than staleBefore (or was never recorded).
func (s *Store) ReadConfirmedLinearLinksForProduct(ctx context.Context, productID, staleBefore string) ([]LinearIssueLink, error) {
	if _, err := readProductPlanningModeCore(ctx, s.db, productID); err != nil {
		return nil, err
	}
	return readConfirmedLinearLinksForProductCore(ctx, s.db, productID, staleBefore)
}

func readConfirmedLinearLinksForProductCore(ctx context.Context, q queryer, productID, staleBefore string) ([]LinearIssueLink, error) {
	rows, err := q.QueryContext(ctx, `
SELECT l.work_id, l.remote_issue_uuid, l.human_key, l.url, l.link_state, l.content_hash
FROM linear_issue_links l
WHERE l.link_state=? AND (l.refreshed_at='' OR l.refreshed_at<?) AND EXISTS (
	SELECT 1
	FROM work_projects wp
	JOIN product_projects pp ON pp.project_id=wp.project_id
	WHERE wp.work_id=l.work_id AND pp.product_id=?
)
ORDER BY l.work_id`, LinearLinkConfirmed, staleBefore, productID)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "linear_link_identity_refresh", "cannot read confirmed Linear links", true, "retry once the database is readable", err)
	}
	defer rows.Close()
	links := make([]LinearIssueLink, 0)
	for rows.Next() {
		var link LinearIssueLink
		if err := rows.Scan(&link.WorkID, &link.RemoteIssueUUID, &link.HumanKey, &link.URL, &link.LinkState, &link.ContentHash); err != nil {
			return nil, wrapFailure(KindUnavailable, "linear_link_identity_refresh", "cannot scan confirmed Linear link", true, "retry once the database is readable", err)
		}
		links = append(links, link)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapFailure(KindUnavailable, "linear_link_identity_refresh", "cannot finish confirmed Linear link read", true, "retry once the database is readable", err)
	}
	return links, nil
}

// RefreshConfirmedLinearLink rewrites only the presentation identity on a
// confirmed link and records that the identity was checked now. The remote
// timestamp and synchronized content hash stay unchanged because this
// operation does not reconcile remote content.
func (s *Store) RefreshConfirmedLinearLink(ctx context.Context, workID, humanKey, url string) error {
	if len(workID) < 2 || len(workID) > 128 {
		return newFailure(KindInvalidPayload, "linear_link_identity_refresh", "work id must be 2 to 128 characters", false, "supply a bounded work id")
	}
	if strings.TrimSpace(humanKey) == "" || len(humanKey) > 64 {
		return newFailure(KindInvalidPayload, "linear_link_identity_refresh", "human key is empty or longer than 64 characters", false, "supply the Linear issue key")
	}
	if strings.TrimSpace(url) == "" || len(url) > 2048 {
		return newFailure(KindInvalidPayload, "linear_link_identity_refresh", "Linear issue URL is empty or longer than 2048 characters", false, "supply the Linear issue URL")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return wrapFailure(KindUnavailable, "linear_link_identity_refresh", "cannot open link refresh transaction", true, "retry once the database is writable", err)
	}
	defer tx.Rollback()
	if err := enterFold(ctx, tx); err != nil {
		return err
	}
	var state string
	err = tx.QueryRowContext(ctx, `SELECT link_state FROM linear_issue_links WHERE work_id=?`, workID).Scan(&state)
	if err == sql.ErrNoRows {
		return newFailure(KindUnknownScope, "linear_link_identity_refresh", "no link exists for the work item", false, "refresh an existing confirmed Linear link")
	}
	if err != nil {
		return wrapFailure(KindUnavailable, "linear_link_identity_refresh", "cannot read link", true, "retry once the database is readable", err)
	}
	if state != LinearLinkConfirmed {
		return newFailure(KindInvalidTransition, "linear_link_identity_refresh", "only a confirmed link can refresh its Linear identity", false, "wait for the linked issue operation to complete")
	}
	checkedAt := s.now().UTC().Format(time.RFC3339Nano)
	result, err := tx.ExecContext(ctx, `UPDATE linear_issue_links SET human_key=?, url=?, refreshed_at=?, updated_at=? WHERE work_id=? AND link_state=?`, humanKey, url, checkedAt, checkedAt, workID, LinearLinkConfirmed)
	if err != nil {
		return wrapFailure(KindUnavailable, "linear_link_identity_refresh", "cannot refresh Linear link identity", true, "retry once the database is writable", err)
	}
	if affected, err := result.RowsAffected(); err != nil {
		return wrapFailure(KindUnavailable, "linear_link_identity_refresh", "cannot verify Linear link identity refresh", true, "retry once the database is readable", err)
	} else if affected != 1 {
		return newFailure(KindInvalidTransition, "linear_link_identity_refresh", "confirmed Linear link disappeared during refresh", false, "reload the confirmed Linear links")
	}
	if err := leaveFold(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return wrapFailure(KindUnavailable, "linear_link_identity_refresh", "cannot commit Linear link identity refresh", true, "retry once the database is writable", err)
	}
	return nil
}

// MarkLinearLinkRefreshed records that a confirmed link's Linear identity was
// checked now and matched, so the refresh interval can skip it until it is
// due again. The identity itself is unchanged and stays untouched.
func (s *Store) MarkLinearLinkRefreshed(ctx context.Context, workID string) error {
	if len(workID) < 2 || len(workID) > 128 {
		return newFailure(KindInvalidPayload, "linear_link_identity_refresh", "work id must be 2 to 128 characters", false, "supply a bounded work id")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return wrapFailure(KindUnavailable, "linear_link_identity_refresh", "cannot open link refresh transaction", true, "retry once the database is writable", err)
	}
	defer tx.Rollback()
	if err := enterFold(ctx, tx); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE linear_issue_links SET refreshed_at=? WHERE work_id=? AND link_state=?`, s.now().UTC().Format(time.RFC3339Nano), workID, LinearLinkConfirmed)
	if err != nil {
		return wrapFailure(KindUnavailable, "linear_link_identity_refresh", "cannot record Linear link identity check", true, "retry once the database is writable", err)
	}
	if affected, err := result.RowsAffected(); err != nil {
		return wrapFailure(KindUnavailable, "linear_link_identity_refresh", "cannot verify Linear link identity check", true, "retry once the database is readable", err)
	} else if affected != 1 {
		return newFailure(KindInvalidTransition, "linear_link_identity_refresh", "confirmed Linear link disappeared during refresh", false, "reload the confirmed Linear links")
	}
	if err := leaveFold(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return wrapFailure(KindUnavailable, "linear_link_identity_refresh", "cannot commit Linear link identity check", true, "retry once the database is writable", err)
	}
	return nil
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
// client UUID is the idempotency identity: Linear's IssueCreateInput.id or
// ProjectCreateInput.id (CD-0171 D2), stored as the remote entity's own id,
// so a re-drain after an ambiguous outcome converges on the same remote
// object and the drain resolves the entity by it.
type linearPayload struct {
	ClientUUID  string `json:"client_uuid"`
	ProductID   string `json:"product_id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	// Content carries the Initiative narrative that project_create and
	// project_update write to the Linear Project's markdown content field
	// (CD-0171 d3). Issue payloads leave it empty.
	Content  string   `json:"content,omitempty"`
	LabelIDs []string `json:"label_ids,omitempty"`
	TeamID   string   `json:"team_id"`
	// The payload never carries the issue's Linear Project: both drains
	// resolve the owning Initiative's confirmed Project at send time
	// (CD-0171 D2, D6), so no enqueue-time snapshot can go stale. It never
	// carried a repository mapping either; repository identity rides the
	// project:<id> label instead.
	ConnectionVersion int64  `json:"connection_version"`
	Lifecycle         string `json:"lifecycle,omitempty"`
	StatusID          string `json:"status_id,omitempty"`
	// Priority is the Linear priority seeded at issue creation from the
	// work item's urgency band (expedite 1, standard 3). Update payloads
	// leave it empty: Linear owns backlog triage after creation.
	Priority int `json:"priority,omitempty"`
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

// resolveLinearProduct resolves the Product that owns a work item's Linear
// identity. A work item in exactly one Product resolves to it. The approved
// shared shape — one work identity with explicit Project memberships across
// Products — resolves to the Product holding the work item's primary
// project. Zero memberships, or a shared scope with no single primary
// owner, refuse: planning authority is never inferred.
func (s *Store) resolveLinearProduct(ctx context.Context, workID string) (string, error) {
	return resolveLinearProductCore(ctx, s.db, workID)
}

func resolveLinearProductCore(ctx context.Context, q queryer, workID string) (string, error) {
	products, err := linearProductMemberships(ctx, q, workID, false)
	if err != nil {
		return "", err
	}
	if len(products) == 1 {
		return products[0], nil
	}
	if len(products) == 0 {
		return "", newFailure(KindUnknownScope, "linear_product_resolve", "work item does not exist or belongs to no Product", false, "supply a work item with exactly one Product")
	}
	// A shared work item holds memberships in more than one Product; its
	// primary project names the Product that owns the Linear identity.
	primary, err := linearProductMemberships(ctx, q, workID, true)
	if err != nil {
		return "", err
	}
	switch len(primary) {
	case 1:
		return primary[0], nil
	case 0:
		return "", newFailure(KindUnknownScope, "linear_product_resolve", "shared work item has no primary project in any Product", false, "give the work item one primary project in the Product that owns its Linear issue")
	default:
		return "", newFailure(KindAmbiguousScope, "linear_product_resolve", "shared work item's primary projects span more than one Product", false, "give the work item one primary project in exactly one Product")
	}
}

// linearProductMemberships lists the distinct Products a work item's project
// memberships reach; primaryOnly narrows the read to primary memberships.
func linearProductMemberships(ctx context.Context, q queryer, workID string, primaryOnly bool) ([]string, error) {
	query := `
SELECT DISTINCT pp.product_id
FROM work_items w
JOIN work_projects wp ON wp.work_id = w.id`
	if primaryOnly {
		query += ` AND wp.role = 'primary'`
	}
	query += `
JOIN product_projects pp ON pp.project_id = wp.project_id
WHERE w.id = ?`
	rows, err := q.QueryContext(ctx, query, workID)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "linear_product_resolve", "cannot resolve the work item's Product", true, "retry once the database is readable", err)
	}
	defer rows.Close()
	var products []string
	for rows.Next() {
		var productID string
		if err := rows.Scan(&productID); err != nil {
			return nil, wrapFailure(KindUnavailable, "linear_product_resolve", "cannot scan the work item's Product", true, "retry once the database is readable", err)
		}
		products = append(products, productID)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapFailure(KindUnavailable, "linear_product_resolve", "cannot finish the Product read", true, "retry once the database is readable", err)
	}
	return products, nil
}

// resolveLinearProjectIDsCore returns every Concord project the work item
// belongs to inside the owning Product. CD-0171 D3: each membership becomes
// one repository label key, so a work item that spans two repositories
// carries both labels. A work item with no membership in the Product cannot
// carry a repository label and refuses.
func resolveLinearProjectIDsCore(ctx context.Context, q queryer, workID, productID string) ([]string, error) {
	rows, err := q.QueryContext(ctx, `
SELECT wp.project_id
FROM work_projects wp
JOIN product_projects pp ON pp.project_id = wp.project_id AND pp.product_id = ?
WHERE wp.work_id = ?
ORDER BY wp.project_id`, productID, workID)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "linear_project_resolve", "cannot resolve the work item's Concord projects", true, "retry once the database is readable", err)
	}
	defer rows.Close()
	var projectIDs []string
	for rows.Next() {
		var projectID string
		if err := rows.Scan(&projectID); err != nil {
			return nil, wrapFailure(KindUnavailable, "linear_project_resolve", "cannot scan the work item's Concord projects", true, "retry once the database is readable", err)
		}
		projectIDs = append(projectIDs, projectID)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapFailure(KindUnavailable, "linear_project_resolve", "cannot finish the Concord project read", true, "retry once the database is readable", err)
	}
	if len(projectIDs) == 0 {
		return nil, newFailure(KindUnknownScope, "linear_project_resolve", "work item has no Concord project membership in the Product", false, "supply a work item with a project membership in the owning Product")
	}
	return projectIDs, nil
}

// resolveLinearInitiativeOwnershipCore applies CD-0171 D6: Linear carries one
// Project per issue, so among the Initiatives holding this work item the
// earliest-joined one owns the Project field and the others are reported for
// the issue description. initiative_entries is the folded projection whose
// row order is the durable join order: the entry-added fold inserts each row
// once and removal deletes it, so a re-added entry rejoins at the end.
func resolveLinearInitiativeOwnershipCore(ctx context.Context, q queryer, workID string) (owner string, others []string, err error) {
	rows, err := q.QueryContext(ctx, `SELECT initiative_work_id FROM initiative_entries WHERE child_work_id=? ORDER BY rowid`, workID)
	if err != nil {
		return "", nil, wrapFailure(KindUnavailable, "linear_issue_enqueue", "cannot read Initiative entries", true, "retry once the database is readable", err)
	}
	defer rows.Close()
	var initiatives []string
	for rows.Next() {
		var initiative string
		if err := rows.Scan(&initiative); err != nil {
			return "", nil, wrapFailure(KindUnavailable, "linear_issue_enqueue", "cannot scan Initiative entries", true, "retry once the database is readable", err)
		}
		initiatives = append(initiatives, initiative)
	}
	if err := rows.Err(); err != nil {
		return "", nil, wrapFailure(KindUnavailable, "linear_issue_enqueue", "cannot finish the Initiative entry read", true, "retry once the database is readable", err)
	}
	if len(initiatives) == 0 {
		return "", nil, nil
	}
	return initiatives[0], initiatives[1:], nil
}

// linearWorkOptionalEntryCore reports whether any Initiative holds the work
// item as a non-required entry (CD-0171 D5); only that fact puts the
// optional label on the issue.
func linearWorkOptionalEntryCore(ctx context.Context, q queryer, workID string) (bool, error) {
	var optional int
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM initiative_entries WHERE child_work_id=? AND required=0)`, workID).Scan(&optional); err != nil {
		return false, wrapFailure(KindUnavailable, "linear_issue_enqueue", "cannot read Initiative entry requiredness", true, "retry once the database is readable", err)
	}
	return optional == 1, nil
}

// readLinearProjectLinkUUIDCore returns the confirmed Linear Project uuid of
// one Initiative, or an empty string before its project_create completes.
func readLinearProjectLinkUUIDCore(ctx context.Context, q queryer, initiativeWorkID string) (string, error) {
	var remoteUUID string
	err := q.QueryRowContext(ctx, `SELECT remote_project_uuid FROM linear_project_links WHERE work_id=?`, initiativeWorkID).Scan(&remoteUUID)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", wrapFailure(KindUnavailable, "linear_issue_enqueue", "cannot read the Initiative's Linear Project link", true, "retry once the database is readable", err)
	}
	return remoteUUID, nil
}

// ResolveLinearProjectIDForWork returns the confirmed Linear Project uuid of
// the work item's earliest-joined Initiative at read time (CD-0171 D6). It is
// empty for a work item outside every Initiative and before the owning
// Initiative's project_create completes, so a drain sends the field's current
// truth instead of a stale enqueue-time snapshot (CD-0171 review correction).
func (s *Store) ResolveLinearProjectIDForWork(ctx context.Context, workID string) (string, error) {
	owner, _, err := resolveLinearInitiativeOwnershipCore(ctx, s.db, workID)
	if err != nil {
		return "", err
	}
	if owner == "" {
		return "", nil
	}
	return readLinearProjectLinkUUIDCore(ctx, s.db, owner)
}

// LinearInitiativeProjectState is the Initiative content a Linear Project
// operation sends: the title for name, the value statement for description,
// and the narrative for content (CD-0171 d3).
type LinearInitiativeProjectState struct {
	Title          string `json:"title"`
	ValueStatement string `json:"value_statement"`
	Narrative      string `json:"narrative"`
}

// ReadLinearInitiativeProjectState reads the Initiative content a Project
// operation sends. The drain calls it at send time, so a revision that lands
// after enqueue still ships (CD-0171 review correction).
func (s *Store) ReadLinearInitiativeProjectState(ctx context.Context, initiativeWorkID string) (LinearInitiativeProjectState, error) {
	return readLinearInitiativeProjectStateCore(ctx, s.db, initiativeWorkID)
}

func readLinearInitiativeProjectStateCore(ctx context.Context, q queryer, initiativeWorkID string) (LinearInitiativeProjectState, error) {
	var state LinearInitiativeProjectState
	var kind string
	err := q.QueryRowContext(ctx, `SELECT kind, title, coalesce(json_extract(intent_json, '$.value_statement'), ''), coalesce(narrative, '') FROM work_items WHERE id=?`, initiativeWorkID).Scan(&kind, &state.Title, &state.ValueStatement, &state.Narrative)
	if err == sql.ErrNoRows {
		return LinearInitiativeProjectState{}, newFailure(KindUnknownScope, "linear_project_state_read", "work item does not exist", false, "supply an existing Initiative work item")
	} else if err != nil {
		return LinearInitiativeProjectState{}, wrapFailure(KindUnavailable, "linear_project_state_read", "cannot read work item", true, "retry once the database is readable", err)
	}
	if kind != "initiative" {
		return LinearInitiativeProjectState{}, newFailure(KindInitiativeScopeViolation, "linear_project_state_read", "work item is not an Initiative", false, "read Linear Project state only for Initiative work items")
	}
	return state, nil
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
	// skipPersist marks a plan whose entry is an operation already queued or
	// in flight: the enqueue returns it without inserting a second row.
	skipPersist bool
	// reviveFailed marks a plan whose entry reuses a failed project_create's
	// identity: persistence requeues that row with a refreshed payload
	// instead of inserting a second one (CD-0171 D2).
	reviveFailed bool
}

// linearInitiativeMention is one Initiative named in a shared entry's issue
// description: its title, its Linear Project URL when the Project exists, and
// its Concord work id as the fallback reference.
type linearInitiativeMention struct {
	workID string
	title  string
	url    string
}

// linearInitiativeMentionsCore resolves the description reference data for
// Initiative work ids through the caller's queryer, so it stays inside the
// caller's transaction.
func linearInitiativeMentionsCore(ctx context.Context, q queryer, initiativeWorkIDs []string) ([]linearInitiativeMention, error) {
	mentions := make([]linearInitiativeMention, 0, len(initiativeWorkIDs))
	for _, initiativeWorkID := range initiativeWorkIDs {
		var mention linearInitiativeMention
		mention.workID = initiativeWorkID
		err := q.QueryRowContext(ctx, `SELECT w.title, coalesce(l.url, '') FROM work_items w LEFT JOIN linear_project_links l ON l.work_id = w.id WHERE w.id=?`, initiativeWorkID).Scan(&mention.title, &mention.url)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return nil, wrapFailure(KindUnavailable, "linear_issue_enqueue", "cannot read Initiative mention", true, "retry once the database is readable", err)
		}
		mentions = append(mentions, mention)
	}
	return mentions, nil
}

type linearIssueProposal struct {
	Problem       string
	Affected      []string
	Stakes        string
	UserOutcomes  []string
	Constraints   []string
	OpenQuestions []string
}

// composeLinearIssueBody renders the issue Description at the single point
// both enqueue paths share. Each intake source is published only when the
// work item holds it, so the body grows as intake evidence is recorded and
// never shows an empty label or an invented section. Heading the value
// statement stops a reader taking it for a description of the work. The
// Initiatives section names the earliest-joined owner and links every other
// Initiative the work item joined (CD-0171 D6): Linear carries one Project
// field, so the description is the only surface the remaining memberships
// reach. A linked other Initiative carries its Linear Project URL; an
// unlinked one carries its title and work id. The single footer line carries
// the kind and the documented resume line `concord zl <work id> --` from the
// CLI help. The kind rides the footer rather than its own section because it
// is one word, and a heading above one word costs two lines to say it.
func composeLinearIssueBody(valueStatement, task, premise string, proposal linearIssueProposal, workID, kind, ownerInitiative string, otherInitiatives []linearInitiativeMention) string {
	sections := make([]string, 0, 10)
	if trimmed := strings.TrimSpace(valueStatement); trimmed != "" {
		sections = append(sections, "## Value statement\n\n"+trimmed)
	}
	if trimmed := strings.TrimSpace(task); trimmed != "" {
		sections = append(sections, "## Task\n\n"+trimmed)
	}
	if trimmed := strings.TrimSpace(proposal.Problem); trimmed != "" {
		sections = append(sections, "## Problem\n\n"+trimmed)
	}
	if section := linearIssueListSection("Affected", proposal.Affected); section != "" {
		sections = append(sections, section)
	}
	if trimmed := strings.TrimSpace(proposal.Stakes); trimmed != "" {
		sections = append(sections, "## Stakes\n\n"+trimmed)
	}
	if section := linearIssueListSection("User outcomes", proposal.UserOutcomes); section != "" {
		sections = append(sections, section)
	}
	if section := linearIssueListSection("Constraints", proposal.Constraints); section != "" {
		sections = append(sections, section)
	}
	if section := linearIssueListSection("Open questions", proposal.OpenQuestions); section != "" {
		sections = append(sections, section)
	}
	if trimmed := strings.TrimSpace(premise); trimmed != "" {
		sections = append(sections, "## Premise\n\n"+trimmed)
	}
	if ownerInitiative != "" {
		lines := make([]string, 0, len(otherInitiatives)+1)
		lines = append(lines, "Owned by `"+ownerInitiative+"`.")
		for _, other := range otherInitiatives {
			switch {
			case other.url != "" && other.title != "":
				lines = append(lines, "Also in ["+other.title+"]("+other.url+").")
			case other.title != "":
				lines = append(lines, "Also in "+other.title+" (`"+other.workID+"`).")
			default:
				lines = append(lines, "Also in `"+other.workID+"`.")
			}
		}
		sections = append(sections, "## Initiatives\n\n"+strings.Join(lines, "\n"))
	}
	footer := "Resume: `concord zl " + workID + " --`"
	if trimmed := strings.TrimSpace(kind); trimmed != "" {
		footer = trimmed + " · " + footer
	}
	sections = append(sections, footer)
	return strings.Join(sections, "\n\n")
}

// linearIssueListSection renders one intake list section when the work item
// holds it.
func linearIssueListSection(title string, items []string) string {
	lines := make([]string, 0, len(items))
	for _, item := range items {
		if trimmed := strings.TrimSpace(item); trimmed != "" {
			lines = append(lines, "- "+trimmed)
		}
	}
	if len(lines) == 0 {
		return ""
	}
	return "## " + title + "\n\n" + strings.Join(lines, "\n")
}

func readLatestLinearIssueProposalCore(ctx context.Context, q queryer, workID string) (linearIssueProposal, error) {
	var proposal linearIssueProposal
	var affected, outcomes, constraints, questions string
	err := q.QueryRowContext(ctx, `SELECT problem, affected, stakes, user_outcomes, constraints, open_questions FROM workflow_proposal_records WHERE work_id=? ORDER BY work_version DESC LIMIT 1`, workID).Scan(
		&proposal.Problem, &affected, &proposal.Stakes, &outcomes, &constraints, &questions)
	if err == sql.ErrNoRows {
		return proposal, nil
	}
	if err != nil {
		return linearIssueProposal{}, wrapFailure(KindUnavailable, "linear_issue_enqueue", "cannot read intake proposal", true, "retry once the database is readable", err)
	}
	for _, entry := range []struct {
		name string
		raw  string
		into *[]string
	}{{"affected", affected, &proposal.Affected}, {"user_outcomes", outcomes, &proposal.UserOutcomes}, {"constraints", constraints, &proposal.Constraints}, {"open_questions", questions, &proposal.OpenQuestions}} {
		if err := json.Unmarshal([]byte(entry.raw), entry.into); err != nil {
			return linearIssueProposal{}, wrapFailure(KindUnavailable, "linear_issue_enqueue", "cannot decode intake proposal "+entry.name, true, "repair the stored proposal projection", err)
		}
	}
	return proposal, nil
}

// linearIssueLabelIDs resolves the label set one synced issue carries: the
// work kind, one repository label per Concord project membership
// (CD-0171 D3, so a work item spanning two repositories carries both
// labels), the expedite band, and the optional label when a non-required
// Initiative entry holds the work item (CD-0171 D5). The decision-mandated
// keys are never resolved here: enqueueLinearIssueForWorkCore refuses the
// enqueue before this lookup runs when a repository or optional key lacks its
// mapping, so an unmapped mandated key cannot silently drop its label. The
// remaining keys (kind, expedite) stay best-effort: no decision mandates
// them, and a team that maps none still syncs every issue.
func linearIssueLabelIDs(connection LinearConnection, kind, urgency string, projectIDs []string, optionalEntry bool) []string {
	keys := make([]string, 0, len(projectIDs)+3)
	keys = append(keys, kind)
	for _, projectID := range projectIDs {
		keys = append(keys, LinearLabelProjectPrefix+projectID)
	}
	if urgency == "expedite" {
		keys = append(keys, "expedite")
	}
	if optionalEntry {
		keys = append(keys, LinearLabelOptionalKey)
	}
	labels := make([]string, 0, len(keys))
	seen := make(map[string]bool, len(keys))
	for _, key := range keys {
		if labelID := connection.LabelIDs[key]; labelID != "" && !seen[labelID] {
			labels = append(labels, labelID)
			seen[labelID] = true
		}
	}
	return labels
}

// linearPriorityForUrgency maps the work item's declared urgency band to the
// Linear priority seeded at issue creation: expedite seeds 1 (Urgent) and
// standard seeds 3 (Medium). The work item's -100..100 priority integer is
// local sequencing only (CD-0018) and never becomes a Linear priority, and
// update drains never resend the value: Linear owns triage after creation.
func linearPriorityForUrgency(urgency string) int {
	if urgency == "expedite" {
		return 1
	}
	return 3
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

// linearIssueSyncState is the Linear state one work item's issue currently
// carries. The enqueue marshals it into its payload — without the Project:
// both drains resolve that field at send time — and the issue completion
// re-derives it for the full-state comparison that closes the
// enqueue-to-completion window (CD-0171 review correction).
type linearIssueSyncState struct {
	ProductID         string
	ConnectionVersion int64
	TeamID            string
	StatusID          string
	ProjectID         string
	LabelIDs          []string
	Title             string
	Description       string
}

// buildLinearIssueSyncStateCore resolves, through the caller's queryer, the
// state one Linear issue carries for a work item right now: the Product's
// connection and planning target, one repository label per Concord project
// membership plus the kind, expedite, and optional labels, the owning
// Initiative's confirmed Project, and the synchronized content. The mandated
// keys refuse here, so neither the enqueue nor the completion comparison can
// silently drop a decision-mandated label (CD-0171 D3, D5).
func buildLinearIssueSyncStateCore(ctx context.Context, q queryer, expectedProductID, workID, title, valueStatement, task, kind, lifecycle, urgency string) (linearIssueSyncState, error) {
	productID, err := resolveLinearProductCore(ctx, q, workID)
	if err != nil {
		return linearIssueSyncState{}, err
	}
	if expectedProductID != "" && expectedProductID != productID {
		return linearIssueSyncState{}, newFailure(KindInvalidRelation, "linear_issue_enqueue", "work item belongs to a different Product", false, "supply a work item in the requested Product")
	}
	mode, err := resolveLinearPlanningTargetCore(ctx, q, productID)
	if err != nil {
		return linearIssueSyncState{}, err
	}
	if mode.PlanningMode == PlanningModeLocalOnly {
		return linearIssueSyncState{}, newFailure(KindInvalidOperation, "linear_issue_enqueue", "planning mode is local_only", false, "set planning_mode to linear_enabled before enqueueing Linear issues")
	}
	connection, err := readLinearConnectionCore(ctx, q, productID)
	if err != nil {
		return linearIssueSyncState{}, err
	}
	if connection.State != LinearConnectionDeclared {
		return linearIssueSyncState{}, newFailure(KindInvalidOperation, "linear_issue_enqueue", "linear_enabled Product has no declared Linear connection", false, "declare the connection as a managed saas_account resource or set the mode back to local_only")
	}
	projectIDs, err := resolveLinearProjectIDsCore(ctx, q, workID, productID)
	if err != nil {
		return linearIssueSyncState{}, err
	}
	// CD-0171 D3: every synced issue carries its repository label, so each
	// member Project needs its project:<id> mapping. A missing mapping
	// refuses with the typed failure the former repository mapping used,
	// matching that refusal's surface: the capture path absorbs it as a
	// configuration no-op, and an explicit enqueue reports it.
	for _, projectID := range projectIDs {
		if connection.LabelIDs[LinearLabelProjectPrefix+projectID] == "" {
			return linearIssueSyncState{}, newFailure(KindInvalidRelation, "linear_issue_enqueue", fmt.Sprintf("Concord project %q has no project:<id> Linear label mapping", projectID), false, "map label_ids.project:<concord project id> on the Linear connection resource before enqueueing Linear issues")
		}
	}
	ownerInitiative, otherInitiativeIDs, err := resolveLinearInitiativeOwnershipCore(ctx, q, workID)
	if err != nil {
		return linearIssueSyncState{}, err
	}
	otherInitiatives, err := linearInitiativeMentionsCore(ctx, q, otherInitiativeIDs)
	if err != nil {
		return linearIssueSyncState{}, err
	}
	optionalEntry, err := linearWorkOptionalEntryCore(ctx, q, workID)
	if err != nil {
		return linearIssueSyncState{}, err
	}
	// CD-0171 D5: a non-required entry carries the optional label, so the
	// key needs its mapping before any issue of this Product syncs. The
	// refusal matches the repository label's surface exactly: the same typed
	// failure, the capture path absorbs it as a configuration no-op, and an
	// explicit enqueue reports it. linearIssueLabelIDs never decides a
	// mandated label's presence (CD-0171 review correction).
	if optionalEntry && connection.LabelIDs[LinearLabelOptionalKey] == "" {
		return linearIssueSyncState{}, newFailure(KindInvalidRelation, "linear_issue_enqueue", fmt.Sprintf("the non-required Initiative entry has no %q Linear label mapping", LinearLabelOptionalKey), false, "map label_ids.optional on the Linear connection resource before enqueueing Linear issues")
	}
	// CD-0171 D2/D6: the earliest-joined Initiative entry owns the issue's
	// Linear Project, and its confirmed link is the state both drains resolve
	// at send time.
	var initiativeProjectID string
	if ownerInitiative != "" {
		initiativeProjectID, err = readLinearProjectLinkUUIDCore(ctx, q, ownerInitiative)
		if err != nil {
			return linearIssueSyncState{}, err
		}
	}
	premise, err := readCurrentWorkflowPremiseCore(ctx, q, workID)
	if err != nil {
		return linearIssueSyncState{}, err
	}
	proposal, err := readLatestLinearIssueProposalCore(ctx, q, workID)
	if err != nil {
		return linearIssueSyncState{}, err
	}
	statusID, err := linearLifecycleStatusID(connection, lifecycle)
	if err != nil {
		return linearIssueSyncState{}, err
	}
	return linearIssueSyncState{
		ProductID:         productID,
		ConnectionVersion: connection.Version,
		TeamID:            connection.TeamID,
		StatusID:          statusID,
		ProjectID:         initiativeProjectID,
		LabelIDs:          linearIssueLabelIDs(connection, kind, urgency, projectIDs, optionalEntry),
		Title:             title,
		Description:       composeLinearIssueBody(valueStatement, task, premise, proposal, workID, kind, ownerInitiative, otherInitiatives),
	}, nil
}

// linearIssueSyncStateDiverged reports whether the state a drain sent differs
// from the state the completion re-derives: the Project the drain resolved at
// send time, the managed label set, or the synchronized content. Lifecycle
// status and priority stay out of the comparison: Linear owns triage after
// creation (CD-0171 review correction).
func linearIssueSyncStateDiverged(sent, desired linearIssueSyncState) bool {
	if sent.ProjectID != desired.ProjectID {
		return true
	}
	if len(sent.LabelIDs) != len(desired.LabelIDs) {
		return true
	}
	sentLabels := make(map[string]bool, len(sent.LabelIDs))
	for _, labelID := range sent.LabelIDs {
		sentLabels[labelID] = true
	}
	for _, labelID := range desired.LabelIDs {
		if !sentLabels[labelID] {
			return true
		}
	}
	return sent.Title != desired.Title || sent.Description != desired.Description
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
	var title, valueStatement, task, kind, lifecycle, urgency string
	err := q.QueryRowContext(ctx, `SELECT title, coalesce(json_extract(intent_json, '$.value_statement'), ''), coalesce(json_extract(intent_json, '$.task'), ''), kind, lifecycle, urgency FROM work_items WHERE id=?`, workID).Scan(&title, &valueStatement, &task, &kind, &lifecycle, &urgency)
	if err == sql.ErrNoRows {
		return linearIssueEnqueuePlan{}, newFailure(KindUnknownScope, "linear_issue_enqueue", "work item does not exist", false, "supply an existing work item")
	} else if err != nil {
		return linearIssueEnqueuePlan{}, wrapFailure(KindUnavailable, "linear_issue_enqueue", "cannot read work item", true, "retry once the database is readable", err)
	}
	if kind == "initiative" {
		// CD-0171 D2/D7: an Initiative has no Linear issue of its own, so
		// syncing it means syncing its Linear Project. Before the Project
		// exists the route is project_create; afterwards project_update.
		// This is the route existing Initiatives use, because only a fresh
		// capture enqueues a project_create on its own.
		projectOp := LinearOpProjectCreate
		if remoteUUID, err := readLinearProjectLinkUUIDCore(ctx, q, workID); err != nil {
			return linearIssueEnqueuePlan{}, err
		} else if remoteUUID != "" {
			projectOp = LinearOpProjectUpdate
		}
		entry, existing, reviveFailed, err := enqueueLinearProjectForInitiativeCore(ctx, q, expectedProductID, workID, projectOp)
		if err != nil {
			return linearIssueEnqueuePlan{}, err
		}
		// An idempotent create returns the operation already queued; the plan
		// carries it without persisting a second row. A failed create comes
		// back revived: the same row requeued under its own client UUID.
		return linearIssueEnqueuePlan{entry: entry, payload: entry.Payload, skipPersist: existing, reviveFailed: reviveFailed}, nil
	}
	state, err := buildLinearIssueSyncStateCore(ctx, q, expectedProductID, workID, title, valueStatement, task, kind, lifecycle, urgency)
	if err != nil {
		return linearIssueEnqueuePlan{}, err
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
	if opKind == LinearOpIssueCreate {
		var linkState string
		if err := q.QueryRowContext(ctx, `SELECT link_state FROM linear_issue_links WHERE work_id=?`, workID).Scan(&linkState); err == sql.ErrNoRows {
			createLink = true
		} else if err != nil {
			return linearIssueEnqueuePlan{}, wrapFailure(KindUnavailable, "linear_issue_enqueue", "cannot read link", true, "retry once the database is readable", err)
		} else {
			// One issue per work item is enforced where the create is
			// requested, not where the drain meets Linear's insert conflict.
			// Every link state means a create was already queued or already
			// confirmed; the capture path keeps its silent skip.
			return linearIssueEnqueuePlan{}, newFailure(KindInvalidOperation, "linear_issue_enqueue", fmt.Sprintf("work item already links Linear issue state %s", linkState), false, "enqueue issue_update to address the linked issue")
		}
	}
	// The priority rides the create payload only: seeding happens once, at
	// creation, from the urgency the work item holds at enqueue time.
	priority := 0
	if opKind == LinearOpIssueCreate {
		priority = linearPriorityForUrgency(urgency)
	}
	payload, err := json.Marshal(linearPayload{ClientUUID: clientUUID, ProductID: state.ProductID, Title: state.Title, Description: state.Description, LabelIDs: state.LabelIDs, TeamID: state.TeamID, ConnectionVersion: state.ConnectionVersion, Lifecycle: lifecycle, StatusID: state.StatusID, Priority: priority})
	if err != nil {
		return linearIssueEnqueuePlan{}, wrapFailure(KindUnavailable, "linear_issue_enqueue", "cannot encode payload", true, "retry the enqueue", err)
	}
	entry := ClaimedLinearOperation{OperationID: "linear-" + clientUUID, WorkID: workID, OpKind: opKind, IdempotencyKey: clientUUID, Payload: payload}
	return linearIssueEnqueuePlan{entry: entry, payload: payload, createLink: createLink}, nil
}

func persistLinearIssueEnqueueTx(ctx context.Context, tx *sql.Tx, plan linearIssueEnqueuePlan, now string) (ClaimedLinearOperation, error) {
	if plan.skipPersist {
		return plan.entry, nil
	}
	entry := plan.entry
	if plan.reviveFailed {
		// The failed create keeps its operation id and client UUID: the
		// revive requeues the same row with a refreshed payload, so the
		// drain's replayed send targets the same remote Project.
		if _, err := tx.ExecContext(ctx, `UPDATE linear_outbox SET payload=?, state='queued', attempts=0, last_error='', updated_at=? WHERE operation_id=? AND op_kind=?`,
			string(plan.payload), now, entry.OperationID, entry.OpKind); err != nil {
			return ClaimedLinearOperation{}, wrapFailure(KindUnavailable, "linear_issue_enqueue", "cannot requeue the failed Project create", true, "retry once the database is writable", err)
		}
		return entry, nil
	}
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
	if err == nil {
		if linkState == LinearLinkConfirmed {
			return ClaimedLinearOperation{}, newFailure(KindInvalidOperation, "linear_issue_adopt_enqueue", "the work item already holds a confirmed link", false, "record updates through issue_update instead of adopting another issue")
		}
		return ClaimedLinearOperation{}, newFailure(KindInvalidOperation, "linear_issue_adopt_enqueue", "the work item already holds a pending Linear link", false, "wait for the queued Linear operation to reconcile")
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

// Linear Project outbox operations (CD-0171 D2). One Linear Project per
// Concord Initiative: project_create mints the remote Project with a
// Concord-generated UUID sent as ProjectCreateInput.id, so a replayed create
// converges on the same remote Project; project_update resyncs the name, the
// value statement in description, and the narrative in content. Issue
// payloads carry the owning Initiative's Project once it exists.

// EnqueueLinearProjectForInitiative queues one project_create or
// project_update operation for an Initiative work item.
func (s *Store) EnqueueLinearProjectForInitiative(ctx context.Context, productID, initiativeWorkID, opKind string) (ClaimedLinearOperation, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ClaimedLinearOperation{}, wrapFailure(KindUnavailable, "linear_project_enqueue", "cannot open Project enqueue transaction", true, "retry once the database is writable", err)
	}
	defer tx.Rollback()
	if err := enterFold(ctx, tx); err != nil {
		return ClaimedLinearOperation{}, err
	}
	entry, existing, reviveFailed, err := enqueueLinearProjectForInitiativeCore(ctx, tx, productID, initiativeWorkID, opKind)
	if err != nil {
		return ClaimedLinearOperation{}, err
	}
	if !existing {
		// A repeated create returns the operation already queued; persisting
		// it again would double the remote call the drain makes. A failed
		// create persists as a revive: the same row requeued under its own
		// client UUID.
		if _, err := persistLinearIssueEnqueueTx(ctx, tx, linearIssueEnqueuePlan{entry: entry, payload: entry.Payload, reviveFailed: reviveFailed}, s.now().UTC().Format(time.RFC3339Nano)); err != nil {
			return ClaimedLinearOperation{}, err
		}
	}
	if err := leaveFold(ctx, tx); err != nil {
		return ClaimedLinearOperation{}, err
	}
	if err := tx.Commit(); err != nil {
		return ClaimedLinearOperation{}, wrapFailure(KindUnavailable, "linear_project_enqueue", "cannot commit queued Project operation", true, "retry once the database is writable", err)
	}
	return entry, nil
}

// enqueueLinearProjectForInitiativeCore builds one project_create or
// project_update operation for an Initiative. A create is idempotent per
// Initiative (CD-0171 review correction): when the Project link already
// exists the create addresses it with a project_update, when a project_create
// is already queued or in flight that queued operation is returned with
// existing set, and when a project_create failed the same operation is
// returned revived — its client UUID stays the Initiative's Project identity,
// so an ambiguous remote success can never mint a second Project (CD-0171
// D2). reviveFailed reports the revive: persistence requeues the failed row
// instead of inserting one.
func enqueueLinearProjectForInitiativeCore(ctx context.Context, q queryer, expectedProductID, initiativeWorkID, opKind string) (entry ClaimedLinearOperation, existing bool, reviveFailed bool, err error) {
	if opKind != LinearOpProjectCreate && opKind != LinearOpProjectUpdate {
		return ClaimedLinearOperation{}, false, false, newFailure(KindInvalidPayload, "linear_project_enqueue", "operation kind is not recognized", false, "use project_create or project_update")
	}
	if len(initiativeWorkID) < 2 || len(initiativeWorkID) > 128 {
		return ClaimedLinearOperation{}, false, false, newFailure(KindInvalidPayload, "linear_project_enqueue", "work id must be 2 to 128 characters", false, "supply a bounded Initiative work id")
	}
	var kind, title, valueStatement, narrative string
	err = q.QueryRowContext(ctx, `SELECT kind, title, coalesce(json_extract(intent_json, '$.value_statement'), ''), coalesce(narrative, '') FROM work_items WHERE id=?`, initiativeWorkID).Scan(&kind, &title, &valueStatement, &narrative)
	if err == sql.ErrNoRows {
		return ClaimedLinearOperation{}, false, false, newFailure(KindUnknownScope, "linear_project_enqueue", "work item does not exist", false, "supply an existing Initiative work item")
	} else if err != nil {
		return ClaimedLinearOperation{}, false, false, wrapFailure(KindUnavailable, "linear_project_enqueue", "cannot read work item", true, "retry once the database is readable", err)
	}
	if kind != "initiative" {
		return ClaimedLinearOperation{}, false, false, newFailure(KindInitiativeScopeViolation, "linear_project_enqueue", "work item is not an Initiative", false, "queue Linear Projects only for Initiative work items")
	}
	productID, err := resolveLinearProductCore(ctx, q, initiativeWorkID)
	if err != nil {
		return ClaimedLinearOperation{}, false, false, err
	}
	if expectedProductID != "" && expectedProductID != productID {
		return ClaimedLinearOperation{}, false, false, newFailure(KindInvalidRelation, "linear_project_enqueue", "Initiative belongs to a different Product", false, "supply an Initiative in the requested Product")
	}
	mode, err := resolveLinearPlanningTargetCore(ctx, q, productID)
	if err != nil {
		return ClaimedLinearOperation{}, false, false, err
	}
	if mode.PlanningMode == PlanningModeLocalOnly {
		return ClaimedLinearOperation{}, false, false, newFailure(KindInvalidOperation, "linear_project_enqueue", "planning mode is local_only", false, "set planning_mode to linear_enabled before queueing Linear Projects")
	}
	connection, err := readLinearConnectionCore(ctx, q, productID)
	if err != nil {
		return ClaimedLinearOperation{}, false, false, err
	}
	if connection.State != LinearConnectionDeclared {
		return ClaimedLinearOperation{}, false, false, newFailure(KindInvalidOperation, "linear_project_enqueue", "linear_enabled Product has no declared Linear connection", false, "declare the connection as a managed saas_account resource or set the mode back to local_only")
	}
	var clientUUID string
	if opKind == LinearOpProjectCreate {
		remoteUUID, err := readLinearProjectLinkUUIDCore(ctx, q, initiativeWorkID)
		if err != nil {
			return ClaimedLinearOperation{}, false, false, err
		}
		if remoteUUID != "" {
			// The Initiative's Project already exists; a repeated create
			// addresses it with an update instead of minting a second one.
			opKind = LinearOpProjectUpdate
		} else if queued, found, err := queuedLinearProjectOperation(ctx, q, initiativeWorkID, LinearOpProjectCreate); err != nil {
			return ClaimedLinearOperation{}, false, false, err
		} else if found {
			return queued, true, false, nil
		} else if failed, found, err := failedLinearProjectCreate(ctx, q, initiativeWorkID); err != nil {
			return ClaimedLinearOperation{}, false, false, err
		} else if found {
			// The failed create's client UUID is this Initiative's stable
			// Project identity: the re-enqueue revives that operation so a
			// replayed send converges on the same remote Project.
			clientUUID = failed.IdempotencyKey
			reviveFailed = true
		}
	}
	if opKind == LinearOpProjectUpdate {
		remoteUUID, err := readLinearProjectLinkUUIDCore(ctx, q, initiativeWorkID)
		if err != nil {
			return ClaimedLinearOperation{}, false, false, err
		}
		if remoteUUID == "" {
			return ClaimedLinearOperation{}, false, false, newFailure(KindInvalidOperation, "linear_project_enqueue", "the Initiative holds no created Linear Project to update", false, "enqueue project_create first; the update addresses the created Project")
		}
	}
	if clientUUID == "" {
		clientUUID = newLinearClientUUID()
	}
	payload, err := json.Marshal(linearPayload{ClientUUID: clientUUID, ProductID: productID, Title: title, Description: valueStatement, Content: narrative, TeamID: connection.TeamID, ConnectionVersion: connection.Version})
	if err != nil {
		return ClaimedLinearOperation{}, false, false, wrapFailure(KindUnavailable, "linear_project_enqueue", "cannot encode payload", true, "retry the enqueue", err)
	}
	return ClaimedLinearOperation{OperationID: "linear-" + clientUUID, WorkID: initiativeWorkID, OpKind: opKind, IdempotencyKey: clientUUID, Payload: payload}, false, reviveFailed, nil
}

// queuedLinearProjectOperation returns the newest queued or in-flight Linear
// Project operation of one kind for an Initiative, so a repeated enqueue
// returns the operation already heading remote instead of queueing a second.
func queuedLinearProjectOperation(ctx context.Context, q queryer, initiativeWorkID, opKind string) (ClaimedLinearOperation, bool, error) {
	var op ClaimedLinearOperation
	var payload string
	err := q.QueryRowContext(ctx, `SELECT operation_id, work_id, op_kind, idempotency_key, payload FROM linear_outbox WHERE work_id=? AND op_kind=? AND state IN (?,?) ORDER BY rowid DESC LIMIT 1`, initiativeWorkID, opKind, LinearOutboxQueued, LinearOutboxInFlight).Scan(&op.OperationID, &op.WorkID, &op.OpKind, &op.IdempotencyKey, &payload)
	if err == sql.ErrNoRows {
		return ClaimedLinearOperation{}, false, nil
	}
	if err != nil {
		return ClaimedLinearOperation{}, false, wrapFailure(KindUnavailable, "linear_project_enqueue", "cannot inspect the queued Project operations", true, "retry once the database is readable", err)
	}
	op.Payload = json.RawMessage(payload)
	return op, true, nil
}

// failedLinearProjectCreate returns the newest project_create for an
// Initiative that is neither queued nor in flight. The caller has already
// established that no Project link exists, so a done row cannot be present:
// the row this returns is a failed create whose remote effect is unconfirmed.
// Its client UUID stays the Initiative's Project identity, and a re-enqueue
// revives the operation instead of minting a second one (CD-0171 D2).
func failedLinearProjectCreate(ctx context.Context, q queryer, initiativeWorkID string) (ClaimedLinearOperation, bool, error) {
	var op ClaimedLinearOperation
	var payload string
	err := q.QueryRowContext(ctx, `SELECT operation_id, work_id, op_kind, idempotency_key, payload FROM linear_outbox WHERE work_id=? AND op_kind=? AND state NOT IN (?,?) ORDER BY rowid DESC LIMIT 1`, initiativeWorkID, LinearOpProjectCreate, LinearOutboxQueued, LinearOutboxInFlight).Scan(&op.OperationID, &op.WorkID, &op.OpKind, &op.IdempotencyKey, &payload)
	if err == sql.ErrNoRows {
		return ClaimedLinearOperation{}, false, nil
	}
	if err != nil {
		return ClaimedLinearOperation{}, false, wrapFailure(KindUnavailable, "linear_project_enqueue", "cannot inspect the failed Project operations", true, "retry once the database is readable", err)
	}
	op.Payload = json.RawMessage(payload)
	return op, true, nil
}

// enqueueLinearProjectCreateForCaptureTx queues the project_create that puts
// a freshly captured Initiative's Linear Project in the outbox from the
// instant its capture commits. Every configuration gap is a silent no-op,
// and an Initiative imported from Linear keeps the Project it already has.
func enqueueLinearProjectCreateForCaptureTx(ctx context.Context, tx *sql.Tx, initiativeWorkID string, at time.Time) error {
	if isWorkflowReplay(ctx) {
		// The outbox is direct authority the log never carries. Replay
		// records no enqueue.
		return nil
	}
	var table string
	if err := tx.QueryRowContext(ctx, `SELECT name FROM sqlite_schema WHERE type='table' AND name='linear_outbox'`).Scan(&table); err == sql.ErrNoRows {
		return nil
	} else if err != nil {
		return wrapFailure(KindUnavailable, "linear_project_enqueue", "cannot inspect Linear outbox schema", true, "retry once the database is readable", err)
	}
	var externalRef string
	if err := tx.QueryRowContext(ctx, `SELECT coalesce(json_extract(intent_json, '$.external_ref'), '') FROM work_items WHERE id=?`, initiativeWorkID).Scan(&externalRef); err != nil {
		return wrapFailure(KindUnavailable, "linear_project_enqueue", "cannot read Initiative external ref", true, "retry once the database is readable", err)
	}
	if _, exists := normalizeLinearIssueExternalRef(externalRef); exists {
		// An imported Initiative mirrors a Linear Project that already
		// exists; creating a second one is never wanted.
		return nil
	}
	entry, existing, reviveFailed, err := enqueueLinearProjectForInitiativeCore(ctx, tx, "", initiativeWorkID, LinearOpProjectCreate)
	if err != nil {
		if linearCaptureConfigurationRefusal(err) {
			return nil
		}
		return err
	}
	if existing {
		// A create for this Initiative is already queued or in flight; the
		// capture records no second one.
		return nil
	}
	_, err = persistLinearIssueEnqueueTx(ctx, tx, linearIssueEnqueuePlan{entry: entry, payload: entry.Payload, reviveFailed: reviveFailed}, at.UTC().Format(time.RFC3339Nano))
	return err
}

// enqueueLinearProjectUpdateForNarrativeTx queues the project_update that
// carries a narrative revision to the Initiative's Linear Project content.
// It is a silent no-op until the Initiative's Project exists, so the first
// sync after capture is always the create.
func enqueueLinearProjectUpdateForNarrativeTx(ctx context.Context, tx *sql.Tx, initiativeWorkID string, at time.Time) error {
	if isWorkflowReplay(ctx) {
		return nil
	}
	var table string
	if err := tx.QueryRowContext(ctx, `SELECT name FROM sqlite_schema WHERE type='table' AND name='linear_project_links'`).Scan(&table); err == sql.ErrNoRows {
		return nil
	} else if err != nil {
		return wrapFailure(KindUnavailable, "linear_project_enqueue", "cannot inspect Linear Project link schema", true, "retry once the database is readable", err)
	}
	var linked int
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM linear_project_links WHERE work_id=?)`, initiativeWorkID).Scan(&linked); err != nil {
		return wrapFailure(KindUnavailable, "linear_project_enqueue", "cannot read the Linear Project link", true, "retry once the database is readable", err)
	}
	if linked == 0 {
		return nil
	}
	entry, _, _, err := enqueueLinearProjectForInitiativeCore(ctx, tx, "", initiativeWorkID, LinearOpProjectUpdate)
	if err != nil {
		if linearCaptureConfigurationRefusal(err) {
			return nil
		}
		return err
	}
	_, err = persistLinearIssueEnqueueTx(ctx, tx, linearIssueEnqueuePlan{entry: entry, payload: entry.Payload}, at.UTC().Format(time.RFC3339Nano))
	return err
}

// enqueueLinearIssueUpdateForEntryTx queues the issue_update that re-labels
// the child work item and re-points its Linear Project after its Initiative
// membership changed: an entry added, removed, or re-required can change the
// owning Initiative (CD-0171 D6) and the optional label (D5). It reuses the
// lifecycle update path, so the same confirmed-link and declared-mapping
// guards decide whether an operation is queued.
func enqueueLinearIssueUpdateForEntryTx(ctx context.Context, tx *sql.Tx, childWorkID string, at time.Time) error {
	if isWorkflowReplay(ctx) {
		return nil
	}
	var lifecycle string
	if err := tx.QueryRowContext(ctx, `SELECT lifecycle FROM work_items WHERE id=?`, childWorkID).Scan(&lifecycle); err != nil {
		if err == sql.ErrNoRows {
			return nil
		}
		return wrapFailure(KindUnavailable, "linear_issue_enqueue", "cannot read work item lifecycle", true, "retry once the database is readable", err)
	}
	return enqueueLinearIssueForLifecycleTx(ctx, tx, childWorkID, lifecycle, at)
}

// enqueueLinearIssueUpdatesForInitiativeEntriesTx queues an issue_update for
// every entry of one Initiative whose issue link is confirmed, so the issues
// move into the Initiative's Linear Project and pick up their labels once the
// Project exists. CompleteLinearProjectOperation runs it inside the completion
// transaction, so a confirmed entry can never sit outside the new Project in
// the window between a completed create and a separate refresh (CD-0171 D2
// review correction). Entries without a confirmed link skip: their next
// enqueue carries the Project from the start.
func enqueueLinearIssueUpdatesForInitiativeEntriesTx(ctx context.Context, tx *sql.Tx, initiativeWorkID string, at time.Time) ([]ClaimedLinearOperation, error) {
	rows, err := tx.QueryContext(ctx, `SELECT child_work_id FROM initiative_entries WHERE initiative_work_id=? ORDER BY rowid`, initiativeWorkID)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "linear_issue_entry_refresh", "cannot read Initiative entries", true, "retry once the database is readable", err)
	}
	var children []string
	for rows.Next() {
		var child string
		if err := rows.Scan(&child); err != nil {
			rows.Close()
			return nil, wrapFailure(KindUnavailable, "linear_issue_entry_refresh", "cannot scan Initiative entries", true, "retry once the database is readable", err)
		}
		children = append(children, child)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, wrapFailure(KindUnavailable, "linear_issue_entry_refresh", "cannot finish the Initiative entry read", true, "retry once the database is readable", err)
	}
	rows.Close()
	enqueued := make([]ClaimedLinearOperation, 0, len(children))
	for _, child := range children {
		var linkState string
		err := tx.QueryRowContext(ctx, `SELECT link_state FROM linear_issue_links WHERE work_id=?`, child).Scan(&linkState)
		if err == sql.ErrNoRows || (err == nil && linkState != LinearLinkConfirmed) {
			continue
		} else if err != nil {
			return nil, wrapFailure(KindUnavailable, "linear_issue_entry_refresh", "cannot read the entry's issue link", true, "retry once the database is readable", err)
		}
		if err := enqueueLinearIssueUpdateForEntryTx(ctx, tx, child, at); err != nil {
			return nil, err
		}
		var latest string
		if err := tx.QueryRowContext(ctx, `SELECT operation_id FROM linear_outbox WHERE work_id=? AND op_kind=? ORDER BY rowid DESC LIMIT 1`, child, LinearOpIssueUpdate).Scan(&latest); err == nil {
			enqueued = append(enqueued, ClaimedLinearOperation{OperationID: latest, WorkID: child, OpKind: LinearOpIssueUpdate})
		} else if err != sql.ErrNoRows {
			return nil, wrapFailure(KindUnavailable, "linear_issue_entry_refresh", "cannot read the queued entry update", true, "retry once the database is readable", err)
		}
	}
	return enqueued, nil
}

// LinearProjectLink is one Initiative's durable Linear Project link.
type LinearProjectLink struct {
	WorkID            string `json:"work_id"`
	RemoteProjectUUID string `json:"remote_project_uuid"`
	Name              string `json:"name"`
	URL               string `json:"url"`
}

// ReadLinearProjectLink returns one Initiative's Linear Project link,
// refusing when none exists.
func (s *Store) ReadLinearProjectLink(ctx context.Context, workID string) (LinearProjectLink, error) {
	var link LinearProjectLink
	link.WorkID = workID
	err := s.db.QueryRowContext(ctx, `SELECT remote_project_uuid, name, url FROM linear_project_links WHERE work_id=?`, workID).Scan(&link.RemoteProjectUUID, &link.Name, &link.URL)
	if err == sql.ErrNoRows {
		return LinearProjectLink{}, newFailure(KindUnknownScope, "linear_project_link_read", "no Linear Project link exists for the Initiative", false, "drain the queued project_create first")
	} else if err != nil {
		return LinearProjectLink{}, wrapFailure(KindUnavailable, "linear_project_link_read", "cannot read the Linear Project link", true, "retry once the database is readable", err)
	}
	return link, nil
}

// CompleteLinearProjectOperation atomically marks an in-flight project
// operation done and, for a completed project_create, records the remote
// Project identity on the Initiative's link and queues the issue_update each
// confirmed entry needs to join it (CD-0171 D2), all in one transaction.
// sent is the Initiative state the drain read when it sent the operation: when
// the stored state has moved on since that read, completion queues one
// project_update, so a revision that lands inside the drain-to-completion
// window is never lost (CD-0171 review correction).
func (s *Store) CompleteLinearProjectOperation(ctx context.Context, operationID, remoteProjectUUID, name, url string, sent LinearInitiativeProjectState) error {
	if len(remoteProjectUUID) < 2 || len(remoteProjectUUID) > 128 {
		return newFailure(KindInvalidPayload, "linear_outbox_complete", "remote project uuid must be 2 to 128 characters", false, "supply the bounded remote project uuid")
	}
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
	if opKind == LinearOpProjectCreate {
		now := s.now().UTC().Format(time.RFC3339Nano)
		if _, err := tx.ExecContext(ctx, `INSERT INTO linear_project_links(work_id, remote_project_uuid, name, url, created_at, updated_at) VALUES(?,?,?,?,?,?)
			ON CONFLICT(work_id) DO UPDATE SET remote_project_uuid=excluded.remote_project_uuid, name=excluded.name, url=excluded.url, updated_at=excluded.updated_at`, workID, remoteProjectUUID, name, url, now, now); err != nil {
			return wrapFailure(KindUnavailable, "linear_outbox_complete", "cannot record the Linear Project link", true, "retry once the database is writable", err)
		}
		current, err := readLinearInitiativeProjectStateCore(ctx, tx, workID)
		if err != nil {
			return err
		}
		if current.Title != sent.Title || current.ValueStatement != sent.ValueStatement || current.Narrative != sent.Narrative {
			// The Initiative moved on while the create was in flight; the
			// revision rides one project_update. A configuration gap stays a
			// silent no-op: it cannot fail a completion whose provider effect
			// already happened.
			if err := enqueueLinearProjectUpdateForNarrativeTx(ctx, tx, workID, s.now()); err != nil {
				return err
			}
		}
		// The Project now exists: its confirmed entry issues enqueue the
		// update that moves them into it and picks up their labels
		// (CD-0171 D2) inside this same transaction, so no exit between a
		// completed create and a separate refresh can strand a confirmed
		// entry outside the Project (CD-0171 review correction).
		if _, err := enqueueLinearIssueUpdatesForInitiativeEntriesTx(ctx, tx, workID, s.now()); err != nil {
			return err
		}
	}
	if err := leaveFold(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return wrapFailure(KindUnavailable, "linear_outbox_complete", "cannot commit completed Project operation", true, "retry once the database is writable", err)
	}
	return nil
}

func enqueueLinearIssueForLifecycleTx(ctx context.Context, tx *sql.Tx, workID, lifecycle string, at time.Time) error {
	if isWorkflowReplay(ctx) {
		// The outbox is direct authority the log never carries, and the
		// Linear mapping is current configuration. Replay records no enqueue.
		return nil
	}
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
// silent no-op — local_only mode, a missing declared connection, an unresolvable
// Product scope, an existing link row, an initiative kind, and an
// external_ref that already names a Linear issue — and terminal items are
// never published. It reports whether an operation was queued, with the queued
// entry when it was.
func enqueueLinearIssueForCaptureTx(ctx context.Context, tx *sql.Tx, workID string, at time.Time) (ClaimedLinearOperation, bool, error) {
	if isWorkflowReplay(ctx) {
		// The outbox is direct authority the log never carries, and the
		// Linear mapping is current configuration. Replay records no enqueue.
		return ClaimedLinearOperation{}, false, nil
	}
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
	plan, err := enqueueLinearIssueForWorkCore(ctx, tx, productID, workID, LinearOpIssueCreate)
	if err != nil {
		if linearCaptureConfigurationRefusal(err) {
			return ClaimedLinearOperation{}, false, nil
		}
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
	return s.completeLinearOperation(ctx, operationID, &identity, nil)
}

// CompleteLinearIssueOperation atomically marks an in-flight issue operation
// done, records the remote identity on its link, and closes the
// enqueue-to-completion window (CD-0171 review correction). sentProjectID is
// the owning Initiative's Project the drain resolved before its remote call;
// this transaction re-derives the issue's full desired state — Project,
// managed labels, synchronized content — from current data and compares it
// with what the drain sent: the outbox payload, with the Project replaced by
// the drain-time resolution. An entry change between enqueue and drain (an
// optional flag, a second Initiative) or a project_create that completed
// inside the drain-to-completion window diverges, and exactly one
// issue_update converges inside the same transaction.
func (s *Store) CompleteLinearIssueOperation(ctx context.Context, operationID string, identity LinearRemoteIdentity, sentProjectID string) error {
	return s.completeLinearOperation(ctx, operationID, &identity, func(ctx context.Context, tx *sql.Tx, workID string) error {
		var rawPayload []byte
		if err := tx.QueryRowContext(ctx, `SELECT payload FROM linear_outbox WHERE operation_id=?`, operationID).Scan(&rawPayload); err != nil {
			return wrapFailure(KindUnavailable, "linear_outbox_complete", "cannot read the sent issue payload", true, "retry once the database is readable", err)
		}
		var sent linearPayload
		if err := json.Unmarshal(rawPayload, &sent); err != nil {
			return newFailure(KindInvalidPayload, "linear_outbox_complete", "the sent issue payload does not decode", false, "complete only operations the enqueue wrote")
		}
		sentState := linearIssueSyncState{ProjectID: sentProjectID, LabelIDs: sent.LabelIDs, Title: sent.Title, Description: sent.Description}
		var title, valueStatement, task, kind, lifecycle, urgency string
		if err := tx.QueryRowContext(ctx, `SELECT title, coalesce(json_extract(intent_json, '$.value_statement'), ''), coalesce(json_extract(intent_json, '$.task'), ''), kind, lifecycle, urgency FROM work_items WHERE id=?`, workID).Scan(&title, &valueStatement, &task, &kind, &lifecycle, &urgency); err != nil {
			return wrapFailure(KindUnavailable, "linear_outbox_complete", "cannot re-read the work item", true, "retry once the database is readable", err)
		}
		desired, err := buildLinearIssueSyncStateCore(ctx, tx, "", workID, title, valueStatement, task, kind, lifecycle, urgency)
		if err != nil {
			// The remote effect already succeeded, so a configuration gap on
			// the connection cannot fail this completion; the capture path
			// absorbs the same refusals. Availability errors propagate and
			// the drain retries the completion.
			if linearCaptureConfigurationRefusal(err) {
				return nil
			}
			return err
		}
		if !linearIssueSyncStateDiverged(sentState, desired) {
			return nil
		}
		if err := enqueueLinearIssueUpdateForEntryTx(ctx, tx, workID, s.now()); err != nil {
			// The remote effect already succeeded, so a configuration gap on
			// the connection cannot fail this completion; the capture path
			// absorbs the same refusals. Availability errors propagate and
			// the drain retries the completion.
			if linearCaptureConfigurationRefusal(err) {
				return nil
			}
			return err
		}
		return nil
	})
}

// CompleteSupersededLinearOperation marks an older in-flight update done while
// leaving the link owned by the newer operation unchanged.
func (s *Store) CompleteSupersededLinearOperation(ctx context.Context, operationID string) error {
	return s.completeLinearOperation(ctx, operationID, nil, nil)
}

func (s *Store) completeLinearOperation(ctx context.Context, operationID string, identity *LinearRemoteIdentity, afterLink func(context.Context, *sql.Tx, string) error) error {
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
	if afterLink != nil {
		if err := afterLink(ctx, tx, workID); err != nil {
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
	var currentState, recordedHash string
	err := tx.QueryRowContext(ctx, `SELECT link_state, content_hash FROM linear_issue_links WHERE work_id=?`, workID).Scan(&currentState, &recordedHash)
	if err == sql.ErrNoRows {
		if _, err := tx.ExecContext(ctx, `INSERT INTO linear_issue_links(work_id, remote_issue_uuid, human_key, url, remote_updated_at, content_hash, link_state, created_at, updated_at) VALUES (?,?,?,?,?,?,?, ?, ?)`, workID, identity.RemoteUUID, identity.HumanKey, identity.URL, identity.RemoteUpdatedAt, identity.ContentHash, LinearLinkConfirmed, now, now); err != nil {
			return wrapFailure(KindUnavailable, "linear_outbox_complete", "cannot create completed link", true, "retry once the database is writable", err)
		}
		return nil
	}
	if err != nil {
		return wrapFailure(KindUnavailable, "linear_outbox_complete", "cannot read link", true, "retry once the database is readable", err)
	}
	// content_hash records the digest of the composed title and description
	// Concord last published to the remote issue: the body written at
	// creation, or the revision comment an approved later composition was
	// published as. A completion advances the digest only when it claims
	// published content; a routing-only update or an adoption writes no
	// remote text and leaves the recorded digest standing.
	contentHash := recordedHash
	if identity.ContentHash != "" {
		contentHash = identity.ContentHash
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
	if _, err := tx.ExecContext(ctx, `UPDATE linear_issue_links SET remote_issue_uuid=?, human_key=?, url=?, remote_updated_at=?, content_hash=?, link_state=?, updated_at=? WHERE work_id=?`, identity.RemoteUUID, identity.HumanKey, identity.URL, identity.RemoteUpdatedAt, contentHash, LinearLinkConfirmed, now, workID); err != nil {
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

// AcknowledgeFailedLinearOperations records a disposition for failed rows in
// one Product. Failed rows stay failed, so this route cannot silently retry or
// change the one-way authority boundary.
func (s *Store) AcknowledgeFailedLinearOperations(ctx context.Context, productID string, operationIDs []string, reason string) ([]LinearOutboxDisposition, error) {
	if strings.TrimSpace(reason) == "" || len(reason) > 4096 {
		return nil, newFailure(KindInvalidPayload, "linear_outbox_disposition", "disposition reason is empty or longer than 4096 characters", false, "state why the failed operations are acknowledged")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "linear_outbox_disposition", "cannot open disposition transaction", true, "retry once the database is writable", err)
	}
	defer tx.Rollback()
	if err := enterFold(ctx, tx); err != nil {
		return nil, err
	}
	if _, err := readProductPlanningModeCore(ctx, tx, productID); err != nil {
		return nil, err
	}
	ids := append([]string(nil), operationIDs...)
	if len(ids) == 0 {
		rows, queryErr := tx.QueryContext(ctx, `
SELECT o.operation_id
FROM linear_outbox o
JOIN work_projects wp ON wp.work_id=o.work_id
JOIN product_projects pp ON pp.project_id=wp.project_id
WHERE o.state=? AND pp.product_id=?
  AND NOT EXISTS (SELECT 1 FROM linear_outbox_dispositions d WHERE d.operation_id=o.operation_id)
ORDER BY o.created_at, o.operation_id`, LinearOutboxFailed, productID)
		if queryErr != nil {
			return nil, wrapFailure(KindUnavailable, "linear_outbox_disposition", "cannot read failed operations", true, "retry once the database is readable", queryErr)
		}
		for rows.Next() {
			var operationID string
			if scanErr := rows.Scan(&operationID); scanErr != nil {
				rows.Close()
				return nil, wrapFailure(KindUnavailable, "linear_outbox_disposition", "cannot scan failed operation", true, "retry once the database is readable", scanErr)
			}
			ids = append(ids, operationID)
		}
		if rowsErr := rows.Err(); rowsErr != nil {
			rows.Close()
			return nil, wrapFailure(KindUnavailable, "linear_outbox_disposition", "cannot finish failed operation read", true, "retry once the database is readable", rowsErr)
		}
		rows.Close()
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	result := make([]LinearOutboxDisposition, 0, len(ids))
	for _, operationID := range ids {
		if len(operationID) < 2 || len(operationID) > 128 {
			return nil, newFailure(KindInvalidPayload, "linear_outbox_disposition", "operation id must be 2 to 128 characters", false, "supply a failed operation id")
		}
		var workID, state string
		err := tx.QueryRowContext(ctx, `
SELECT o.work_id, o.state
FROM linear_outbox o
JOIN work_projects wp ON wp.work_id=o.work_id
JOIN product_projects pp ON pp.project_id=wp.project_id
WHERE o.operation_id=? AND pp.product_id=?`, operationID, productID).Scan(&workID, &state)
		if err == sql.ErrNoRows {
			return nil, newFailure(KindUnknownScope, "linear_outbox_disposition", "failed operation does not belong to the Product", false, "supply a failed operation from the requested Product")
		}
		if err != nil {
			return nil, wrapFailure(KindUnavailable, "linear_outbox_disposition", "cannot read failed operation", true, "retry once the database is readable", err)
		}
		if state != LinearOutboxFailed {
			return nil, newFailure(KindInvalidTransition, "linear_outbox_disposition", fmt.Sprintf("outbox state is %s, not %s", state, LinearOutboxFailed), false, "acknowledge only failed operations")
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO linear_outbox_dispositions(operation_id, work_id, disposition, reason, created_at) VALUES(?,?,?,?,?)`, operationID, workID, "acknowledged", reason, now); err != nil {
			if strings.Contains(err.Error(), "UNIQUE constraint failed") {
				return nil, newFailure(KindIdempotencyConflict, "linear_outbox_disposition", "failed operation already has a disposition", false, "reuse the existing disposition")
			}
			return nil, wrapFailure(KindUnavailable, "linear_outbox_disposition", "cannot record failed operation disposition", true, "retry once the database is writable", err)
		}
		result = append(result, LinearOutboxDisposition{OperationID: operationID, WorkID: workID, Disposition: "acknowledged", Reason: reason, CreatedAt: now})
	}
	if err := leaveFold(ctx, tx); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, wrapFailure(KindUnavailable, "linear_outbox_disposition", "cannot commit failed operation dispositions", true, "retry once the database is writable", err)
	}
	return result, nil
}

// ImportedLinearInitiative is the result of a one-way drafting-pad import.
type ImportedLinearInitiative struct {
	WorkID      string `json:"work_id"`
	ExternalRef string `json:"external_ref"`
	Title       string `json:"title"`
}

// EnsureLinearIssueWork creates the local work identity needed by the shipped
// issue_adopt enqueue path. The external reference suppresses issue_create.
func (s *Store) EnsureLinearIssueWork(ctx context.Context, productID, remoteUUID, title, description string) (string, error) {
	if len(remoteUUID) < 2 || len(remoteUUID) > 128 {
		return "", newFailure(KindInvalidPayload, "linear_issue_adopt", "remote issue uuid must be 2 to 128 characters", false, "supply the Linear issue uuid")
	}
	if strings.TrimSpace(title) == "" || len(title) > 256 {
		return "", newFailure(KindInvalidPayload, "linear_issue_adopt", "issue title must be 1 to 256 characters", false, "supply the Linear issue title")
	}
	if _, err := s.ResolveLinearPlanningTarget(ctx, productID); err != nil {
		return "", err
	}
	externalRef := "linear:" + remoteUUID
	var existing string
	err := s.db.QueryRowContext(ctx, `SELECT id FROM work_items WHERE json_extract(intent_json, '$.external_ref')=? LIMIT 1`, externalRef).Scan(&existing)
	if err == nil {
		return existing, nil
	}
	if err != sql.ErrNoRows {
		return "", wrapFailure(KindUnavailable, "linear_issue_adopt", "cannot read existing issue work", true, "retry once the database is readable", err)
	}
	var projectID string
	err = s.db.QueryRowContext(ctx, `SELECT project_id FROM product_projects WHERE product_id=? AND role='primary'`, productID).Scan(&projectID)
	if err == sql.ErrNoRows {
		return "", newFailure(KindAmbiguousScope, "linear_issue_adopt", "Product has no primary Project", false, "give the Product a primary Project before adopting Linear issues")
	}
	if err != nil {
		return "", wrapFailure(KindUnavailable, "linear_issue_adopt", "cannot read the primary Project", true, "retry once the database is readable", err)
	}
	valueStatement := strings.TrimSpace(description)
	if valueStatement == "" {
		valueStatement = "Adopted from Linear issue " + strings.TrimSpace(title)
	}
	if len(valueStatement) > 256 {
		valueStatement = valueStatement[:253] + "..."
	}
	digest := sha256.Sum256([]byte("linear-issue-adopt:" + externalRef))
	workID := "linear-issue-" + hex.EncodeToString(digest[:])[7:31]
	payload, err := json.Marshal(map[string]any{
		"work_kind": "task", "title": strings.TrimSpace(title), "value_statement": valueStatement,
		"priority": 0, "urgency": "standard", "tags": []string{"linear-adopted"}, "external_ref": externalRef,
	})
	if err != nil {
		return "", wrapFailure(KindUnavailable, "linear_issue_adopt", "cannot encode issue work", true, "retry the adoption", err)
	}
	membershipPayload, err := json.Marshal(map[string]any{
		"memberships":      []map[string]any{{"project_id": projectID, "role": "primary"}},
		"expected_version": 1, "resulting_version": 2,
	})
	if err != nil {
		return "", wrapFailure(KindUnavailable, "linear_issue_adopt", "cannot encode issue membership", true, "retry the adoption", err)
	}
	now := s.now().UTC()
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{
		{EventID: "linear-issue-adopt:" + externalRef + ":create", Kind: "work.created", SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "operator", OccurredAt: now, PayloadVersion: 2, Payload: payload},
		{EventID: "linear-issue-adopt:" + externalRef + ":memberships", Kind: "work.memberships_replaced", SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "operator", OccurredAt: now, PayloadVersion: 1, Payload: membershipPayload},
	}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): 0}}); err != nil {
		return "", err
	}
	return workID, nil
}

// ImportLinearInitiative imports one Linear Project as a Concord Initiative
// work item with external_ref linear:<uuid>, records the imported remote
// Project on the Initiative's project link in the same transaction, so the
// entries of an imported Initiative acquire its Project on their next sync,
// and lands the Project's markdown content as the Initiative narrative
// through the folded revision event (CD-0171 d3). The import is one-way and
// once: a repeated import of the same remote identity refuses with a typed
// duplicate, and nothing here ever writes back to Linear.
func (s *Store) ImportLinearInitiative(ctx context.Context, productID, remoteUUID, name, description, narrative, url string) (ImportedLinearInitiative, error) {
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
	events := []Event{
		{EventID: "linear-initiative-import:" + externalRef + ":create", Kind: "work.created", SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "operator", OccurredAt: now, PayloadVersion: 2, Payload: payload},
		{EventID: "linear-initiative-import:" + externalRef + ":memberships", Kind: "work.memberships_replaced", SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "operator", OccurredAt: now, PayloadVersion: 1, Payload: membershipPayload},
	}
	// The imported Project's content is the Initiative narrative the drain
	// sends as the Linear Project's markdown content (CD-0171 d3). The
	// narrative fold refuses an empty revision, so an import without content
	// starts at the empty default and a drained project_update keeps Linear's
	// current content only when the Initiative holds none.
	if narrative != "" {
		narrativeEvent, err := InitiativeNarrativeEvent("linear-initiative-import:"+externalRef+":narrative", workID, narrative, "imported from Linear", "operator", now, 2)
		if err != nil {
			return ImportedLinearInitiative{}, err
		}
		events = append(events, narrativeEvent)
	}
	operation := Operation{Events: events, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): 0}}
	stampedAt := now.UTC().Format(time.RFC3339Nano)
	if err := s.Transact(ctx, func(transaction *Transaction) error {
		return importLinearInitiativeTx(ctx, transaction, operation, workID, remoteUUID, name, url, stampedAt)
	}); err != nil {
		return ImportedLinearInitiative{}, err
	}
	return ImportedLinearInitiative{WorkID: workID, ExternalRef: externalRef, Title: name}, nil
}

// importLinearInitiativeTx applies the import events and records the imported
// Linear Project link inside one transaction, so the work identity and its
// Project link exist together or not at all.
func importLinearInitiativeTx(ctx context.Context, transaction *Transaction, operation Operation, workID, remoteUUID, name, url, now string) error {
	if _, err := ApplyOperationTx(ctx, transaction, operation); err != nil {
		return err
	}
	tx, err := transactionSQL(transaction, "linear_initiative_import")
	if err != nil {
		return err
	}
	// The imported remote Project IS the Initiative's Project (CD-0171 D7).
	// The link table is fold-only, so the write runs inside its own fold
	// region. DO NOTHING keeps a row a completed project_create recorded,
	// because that completion owns the link it wrote.
	if err := enterFold(ctx, tx); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO linear_project_links(work_id, remote_project_uuid, name, url, created_at, updated_at) VALUES(?,?,?,?,?,?)
		ON CONFLICT(work_id) DO NOTHING`, workID, remoteUUID, name, url, now, now); err != nil {
		return wrapFailure(KindUnavailable, "linear_initiative_import", "cannot record the imported Linear Project link", true, "retry the import", err)
	}
	return leaveFold(ctx, tx)
}

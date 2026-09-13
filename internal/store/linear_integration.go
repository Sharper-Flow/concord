package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
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
// the C15 managed saas_account resource carrying workspace, team, project and
// status bindings. No credential material is read or stored here.
type LinearConnection struct {
	ResourceID   string            `json:"resource_id"`
	WorkspaceURL string            `json:"workspace_url"`
	TeamID       string            `json:"team_id"`
	ProjectID    string            `json:"project_id"`
	AuthMode     string            `json:"auth_mode"`
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
	var mode ProductPlanningMode
	err := s.db.QueryRowContext(ctx, `SELECT id, planning_mode, version FROM products WHERE id=?`, productID).Scan(&mode.ProductID, &mode.PlanningMode, &mode.Version)
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
	if productID == "" {
		return ProductPlanningMode{}, newFailure(KindAmbiguousScope, "planning_mode_resolve", "planning resolution requires exactly one Product", false, "name the Product explicitly; mode is never inferred from repository path or installation")
	}
	mode, err := s.ReadProductPlanningMode(ctx, productID)
	if err != nil {
		return ProductPlanningMode{}, err
	}
	if mode.PlanningMode == PlanningModeLinear {
		connection, connErr := s.ReadLinearConnection(ctx, productID)
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
// such resource exists. Multiple matching owner resources refuse.
func (s *Store) ReadLinearConnection(ctx context.Context, productID string) (LinearConnection, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT r.resource_id, r.metadata
FROM managed_resources r
JOIN resource_products rp ON rp.resource_id = r.resource_id
WHERE rp.product_id = ? AND rp.role = 'owner' AND r.kind = 'saas_account'
ORDER BY r.resource_id`, productID)
	if err != nil {
		return LinearConnection{}, wrapFailure(KindUnavailable, "linear_connection_read", "cannot read Linear connection resources", true, "retry once the database is readable", err)
	}
	defer rows.Close()
	var candidates []LinearConnection
	for rows.Next() {
		var resourceID, metadataJSON string
		if err := rows.Scan(&resourceID, &metadataJSON); err != nil {
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
		candidate := LinearConnection{ResourceID: resourceID}
		if v, ok := linear["workspace_url"].(string); ok {
			candidate.WorkspaceURL = v
		}
		if v, ok := linear["team_id"].(string); ok {
			candidate.TeamID = v
		}
		if v, ok := linear["project_id"].(string); ok {
			candidate.ProjectID = v
		}
		if v, ok := linear["auth_mode"].(string); ok {
			candidate.AuthMode = v
		}
		if statusIDs, ok := linear["status_ids"].(map[string]any); ok {
			candidate.StatusIDs = make(map[string]string, len(statusIDs))
			for lifecycle, value := range statusIDs {
				if statusID, ok := value.(string); ok {
					candidate.StatusIDs[lifecycle] = statusID
				}
			}
		}
		candidates = append(candidates, candidate)
	}
	if err := rows.Err(); err != nil {
		return LinearConnection{}, wrapFailure(KindUnavailable, "linear_connection_read", "cannot finish Linear connection read", true, "retry once the database is readable", err)
	}
	if len(candidates) == 0 {
		return LinearConnection{State: LinearConnectionAbsent}, nil
	}
	if len(candidates) > 1 {
		return LinearConnection{}, newFailure(KindAmbiguousScope, "linear_connection_read", "Product has multiple Linear owner connections", false, "retain exactly one owner connection for this Product")
	}
	connection := &candidates[0]
	if connection.WorkspaceURL != "" && connection.TeamID != "" && connection.AuthMode != "" {
		connection.State = LinearConnectionDeclared
	} else {
		connection.State = LinearConnectionPartial
	}
	return *connection, nil
}

// EnqueueLinearOperation queues one intended outbound operation. Phase 0 owns
// the typed refusal surface; the drain arrives with Phase 1.
func (s *Store) EnqueueLinearOperation(ctx context.Context, entry LinearOutboxEntry) error {
	if entry.OperationID == "" || len(entry.OperationID) > 128 {
		return newFailure(KindInvalidPayload, "linear_outbox_enqueue", "operation id must be 2 to 128 characters", false, "supply a bounded operation id")
	}
	if entry.OpKind != LinearOpIssueCreate && entry.OpKind != LinearOpIssueUpdate {
		return newFailure(KindInvalidPayload, "linear_outbox_enqueue", "operation kind is not recognized", false, "use issue_create or issue_update")
	}
	if entry.IdempotencyKey == "" || len(entry.IdempotencyKey) > 128 {
		return newFailure(KindInvalidPayload, "linear_outbox_enqueue", "idempotency key must be 2 to 128 characters", false, "supply a bounded idempotency key")
	}
	if len(entry.Payload) == 0 || json.Unmarshal(entry.Payload, &map[string]any{}) != nil {
		return newFailure(KindInvalidPayload, "linear_outbox_enqueue", "payload must be a JSON object", false, "supply a JSON object payload")
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
	var oldestPending string
	if err := s.db.QueryRowContext(ctx, `SELECT count(*), coalesce(min(CASE WHEN o.state IN ('queued','in_flight') THEN o.created_at END), '') FROM linear_outbox o WHERE EXISTS (SELECT 1 FROM work_projects wp JOIN product_projects pp ON pp.project_id=wp.project_id WHERE wp.work_id=o.work_id AND pp.product_id=?) OR NOT EXISTS (SELECT 1 FROM work_projects wp JOIN product_projects pp ON pp.project_id=wp.project_id WHERE wp.work_id=o.work_id)`, productID).Scan(&health.OutboxDepth, &oldestPending); err != nil {
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
	rows, err := s.db.QueryContext(ctx, `SELECT l.link_state, count(*) FROM linear_issue_links l WHERE EXISTS (SELECT 1 FROM work_projects wp JOIN product_projects pp ON pp.project_id=wp.project_id WHERE wp.work_id=l.work_id AND pp.product_id=?) OR NOT EXISTS (SELECT 1 FROM work_projects wp JOIN product_projects pp ON pp.project_id=wp.project_id WHERE wp.work_id=l.work_id) GROUP BY l.link_state`, productID)
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

func isLinearFailureKind(err error, kind FailureKind) bool {
	failure, ok := err.(*Failure)
	return ok && failure.Kind == kind
}

// AdoptLinearIssue records a provider-verified issue as the canonical link for
// one Product work item. It never creates or updates a remote issue.
func (s *Store) AdoptLinearIssue(ctx context.Context, productID, workID string, identity LinearRemoteIdentity) error {
	if len(productID) < 2 || len(productID) > 128 || len(workID) < 2 || len(workID) > 128 {
		return newFailure(KindInvalidPayload, "linear_issue_adopt", "Product and work ids must be bounded", false, "supply bounded identifiers")
	}
	if len(identity.RemoteUUID) < 2 || len(identity.RemoteUUID) > 128 {
		return newFailure(KindInvalidPayload, "linear_issue_adopt", "remote issue uuid must be bounded", false, "supply the provider issue uuid")
	}
	if _, err := s.ResolveLinearPlanningTarget(ctx, productID); err != nil {
		return err
	}
	connection, err := s.ReadLinearConnection(ctx, productID)
	if err != nil {
		return err
	}
	resolvedProduct, err := s.resolveLinearProduct(ctx, workID)
	if err != nil {
		return err
	}
	if resolvedProduct != productID {
		return newFailure(KindAmbiguousScope, "linear_issue_adopt", "work item belongs to a different Product", false, "adopt the issue through the work item's Product")
	}
	if connection.TeamID == "" || identity.TeamID != connection.TeamID {
		return newFailure(KindProjectionConflict, "linear_issue_adopt", "remote issue team does not match the Product destination", false, "verify the issue against the declared team")
	}
	if connection.ProjectID != "" && identity.ProjectID != connection.ProjectID {
		return newFailure(KindProjectionConflict, "linear_issue_adopt", "remote issue project does not match the Product destination", false, "verify the issue against the declared project")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return wrapFailure(KindUnavailable, "linear_issue_adopt", "cannot open adoption transaction", true, "retry once the database is writable", err)
	}
	defer tx.Rollback()
	if err := enterFold(ctx, tx); err != nil {
		return err
	}
	var existingWork string
	if err := tx.QueryRowContext(ctx, `SELECT work_id FROM linear_issue_links WHERE remote_issue_uuid=? AND work_id<>? AND link_state IN ('pending','confirmed')`, identity.RemoteUUID, workID).Scan(&existingWork); err == nil {
		return newFailure(KindIdempotencyConflict, "linear_issue_adopt", "remote issue is already linked to another work item", false, "resolve the conflicting canonical mapping")
	} else if err != sql.ErrNoRows {
		return wrapFailure(KindUnavailable, "linear_issue_adopt", "cannot inspect remote identity mappings", true, "retry once the database is readable", err)
	}
	var currentUUID, currentState string
	err = tx.QueryRowContext(ctx, `SELECT remote_issue_uuid, link_state FROM linear_issue_links WHERE work_id=?`, workID).Scan(&currentUUID, &currentState)
	now := s.now().UTC().Format(time.RFC3339Nano)
	switch {
	case err == sql.ErrNoRows:
		if _, err := tx.ExecContext(ctx, `INSERT INTO linear_issue_links(work_id, remote_issue_uuid, human_key, url, remote_updated_at, content_hash, link_state, created_at, updated_at) VALUES (?,?,?,?,?,?,?, ?, ?)`, workID, identity.RemoteUUID, identity.HumanKey, identity.URL, identity.RemoteUpdatedAt, identity.ContentHash, LinearLinkConfirmed, now, now); err != nil {
			return wrapFailure(KindUnavailable, "linear_issue_adopt", "cannot record the native link", true, "retry once the database is writable", err)
		}
	case err != nil:
		return wrapFailure(KindUnavailable, "linear_issue_adopt", "cannot read the existing link", true, "retry once the database is readable", err)
	case currentUUID != identity.RemoteUUID:
		return newFailure(KindIdempotencyConflict, "linear_issue_adopt", "work item already maps to a different remote issue", false, "resolve the conflicting canonical mapping")
	case currentState == LinearLinkConfirmed:
		if err := leaveFold(ctx, tx); err != nil {
			return err
		}
		return tx.Commit()
	default:
		if _, err := tx.ExecContext(ctx, `UPDATE linear_issue_links SET human_key=?, url=?, remote_updated_at=?, content_hash=?, link_state=?, updated_at=? WHERE work_id=?`, identity.HumanKey, identity.URL, identity.RemoteUpdatedAt, identity.ContentHash, LinearLinkConfirmed, now, workID); err != nil {
			return wrapFailure(KindUnavailable, "linear_issue_adopt", "cannot confirm the native link", true, "retry once the database is writable", err)
		}
	}
	if err := leaveFold(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
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
	WorkspaceURL    string
	TeamID          string
	ProjectID       string
	StateID         string
}

// ClaimedLinearOperation is one operation handed to the drain under claim.
type ClaimedLinearOperation struct {
	OperationID    string          `json:"operation_id"`
	WorkID         string          `json:"work_id"`
	OpKind         string          `json:"op_kind"`
	IdempotencyKey string          `json:"idempotency_key"`
	State          string          `json:"state"`
	Attempts       int             `json:"attempts"`
	Payload        json.RawMessage `json:"payload"`
}

// linearPayload is the JSON convention every outbox payload carries. The
// client UUID is the idempotency identity: Linear's IssueCreateInput.id, so a
// re-drain after an ambiguous outcome converges on the same remote issue.
type linearPayload struct {
	ClientUUID  string `json:"client_uuid"`
	Title       string `json:"title"`
	Description string `json:"description"`
	TeamID      string `json:"team_id"`
	ProjectID   string `json:"project_id,omitempty"`
	Lifecycle   string `json:"lifecycle,omitempty"`
	StatusID    string `json:"status_id,omitempty"`
}

// linearTerminalStatusID resolves only the terminal lifecycle mapping declared
// on the Product's connection resource. It never guesses a workspace state.
func linearTerminalStatusID(connection LinearConnection, lifecycle string) (string, error) {
	if !isTerminalLifecycle(lifecycle) {
		return "", nil
	}
	statusID := connection.StatusIDs[lifecycle]
	if statusID == "" {
		return "", newFailure(KindInvalidOperation, "linear_issue_enqueue", "no declared Linear status id for terminal lifecycle "+lifecycle, false, "declare status_ids."+lifecycle+" on the Linear connection resource")
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

// linearCreationUUID derives one stable UUID from the local work identity.
// The work identity is the creation intent, so a retry cannot mint a second
// remote issue after an uncertain response.
func linearCreationUUID(workID string) string {
	sum := sha256.Sum256([]byte("linear-create:" + workID))
	bytes := sum[:16]
	bytes[6] = (bytes[6] & 0x0f) | 0x50
	bytes[8] = (bytes[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", bytes[0:4], bytes[4:6], bytes[6:8], bytes[8:10], bytes[10:16])
}

// resolveLinearProduct resolves the exactly one Product a work item belongs
// to. Zero or many Products refuse: planning authority is never inferred.
func (s *Store) resolveLinearProduct(ctx context.Context, workID string) (string, error) {
	rows, err := s.db.QueryContext(ctx, `
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

// VerifyLinearWorkProduct refuses a command whose declared Product does not
// own the work item. The command boundary must not ignore its Product field.
func (s *Store) VerifyLinearWorkProduct(ctx context.Context, productID, workID string) error {
	resolved, err := s.resolveLinearProduct(ctx, workID)
	if err != nil {
		return err
	}
	if resolved != productID {
		return newFailure(KindProjectionConflict, "linear_product_resolve", "work item does not belong to the declared Product", false, "supply the Product that owns the work item")
	}
	return nil
}

// EnqueueLinearIssueForWork queues one outbound issue operation for a work
// item and records its link as unpublished. The guards are the CD-0121
// boundaries: local_only refuses, linear_enabled without a declared
// connection refuses as missing setup, and neither is inferred.
func (s *Store) EnqueueLinearIssueForWork(ctx context.Context, workID, opKind string) (ClaimedLinearOperation, error) {
	if opKind != LinearOpIssueCreate && opKind != LinearOpIssueUpdate {
		return ClaimedLinearOperation{}, newFailure(KindInvalidPayload, "linear_issue_enqueue", "operation kind is not recognized", false, "use issue_create or issue_update")
	}
	if len(workID) < 2 || len(workID) > 128 {
		return ClaimedLinearOperation{}, newFailure(KindInvalidPayload, "linear_issue_enqueue", "work id must be 2 to 128 characters", false, "supply a bounded work id")
	}
	var title, valueStatement, lifecycle string
	err := s.db.QueryRowContext(ctx, `SELECT title, coalesce(json_extract(intent_json, '$.value_statement'), ''), lifecycle FROM work_items WHERE id=?`, workID).Scan(&title, &valueStatement, &lifecycle)
	if err == sql.ErrNoRows {
		return ClaimedLinearOperation{}, newFailure(KindUnknownScope, "linear_issue_enqueue", "work item does not exist", false, "supply an existing work item")
	} else if err != nil {
		return ClaimedLinearOperation{}, wrapFailure(KindUnavailable, "linear_issue_enqueue", "cannot read work item", true, "retry once the database is readable", err)
	}
	productID, err := s.resolveLinearProduct(ctx, workID)
	if err != nil {
		return ClaimedLinearOperation{}, err
	}
	mode, err := s.ResolveLinearPlanningTarget(ctx, productID)
	if err != nil {
		return ClaimedLinearOperation{}, err
	}
	if mode.PlanningMode == PlanningModeLocalOnly {
		return ClaimedLinearOperation{}, newFailure(KindInvalidOperation, "linear_issue_enqueue", "planning mode is local_only", false, "set planning_mode to linear_enabled before enqueueing Linear issues")
	}
	connection, err := s.ReadLinearConnection(ctx, productID)
	if err != nil {
		return ClaimedLinearOperation{}, err
	}
	if connection.State != LinearConnectionDeclared {
		return ClaimedLinearOperation{}, newFailure(KindInvalidOperation, "linear_issue_enqueue", "linear_enabled Product has no declared Linear connection", false, "declare the connection as a managed saas_account resource or set the mode back to local_only")
	}
	if opKind == LinearOpIssueUpdate {
		var linkState string
		err := s.db.QueryRowContext(ctx, `SELECT link_state FROM linear_issue_links WHERE work_id=?`, workID).Scan(&linkState)
		if err == sql.ErrNoRows {
			return ClaimedLinearOperation{}, newFailure(KindInvalidOperation, "linear_issue_enqueue", "no link exists to update", false, "enqueue issue_create first; an update addresses the linked remote issue")
		} else if err != nil {
			return ClaimedLinearOperation{}, wrapFailure(KindUnavailable, "linear_issue_enqueue", "cannot read link", true, "retry once the database is readable", err)
		}
	}
	clientUUID := newLinearClientUUID()
	if opKind == LinearOpIssueCreate {
		clientUUID = linearCreationUUID(workID)
	}
	statusID := ""
	if opKind == LinearOpIssueUpdate {
		statusID = connection.StatusIDs[lifecycle]
		if isTerminalLifecycle(lifecycle) {
			statusID, err = linearTerminalStatusID(connection, lifecycle)
			if err != nil {
				return ClaimedLinearOperation{}, err
			}
		}
	}
	payload, err := json.Marshal(linearPayload{ClientUUID: clientUUID, Title: title, Description: valueStatement, TeamID: connection.TeamID, ProjectID: connection.ProjectID, Lifecycle: lifecycle, StatusID: statusID})
	if err != nil {
		return ClaimedLinearOperation{}, wrapFailure(KindUnavailable, "linear_issue_enqueue", "cannot encode payload", true, "retry the enqueue", err)
	}
	entry := ClaimedLinearOperation{OperationID: "linear-" + clientUUID, WorkID: workID, OpKind: opKind, IdempotencyKey: clientUUID, Payload: payload, State: LinearOutboxQueued}
	if opKind == LinearOpIssueCreate {
		var existingState string
		if err := s.db.QueryRowContext(ctx, `SELECT link_state FROM linear_issue_links WHERE work_id=?`, workID).Scan(&existingState); err == nil {
			var existing ClaimedLinearOperation
			var existingPayload string
			readErr := s.db.QueryRowContext(ctx, `SELECT operation_id, work_id, op_kind, idempotency_key, state, attempts, payload FROM linear_outbox WHERE work_id=? AND op_kind=? ORDER BY created_at LIMIT 1`, workID, LinearOpIssueCreate).Scan(&existing.OperationID, &existing.WorkID, &existing.OpKind, &existing.IdempotencyKey, &existing.State, &existing.Attempts, &existingPayload)
			if readErr == nil {
				existing.Payload = json.RawMessage(existingPayload)
				return existing, nil
			}
			if readErr != sql.ErrNoRows {
				return ClaimedLinearOperation{}, wrapFailure(KindUnavailable, "linear_issue_enqueue", "cannot read existing creation", true, "retry once the database is readable", readErr)
			}
			if existingState == LinearLinkConfirmed {
				entry.State = LinearOutboxDone
				return entry, nil
			}
			return ClaimedLinearOperation{}, newFailure(KindIdempotencyConflict, "linear_issue_enqueue", "work item already has an unresolved native link", false, "resolve the existing pending link before creating an issue")
		} else if err != nil && err != sql.ErrNoRows {
			return ClaimedLinearOperation{}, wrapFailure(KindUnavailable, "linear_issue_enqueue", "cannot read link", true, "retry once the database is readable", err)
		}
	}
	if err := s.EnqueueLinearOperation(ctx, LinearOutboxEntry{OperationID: entry.OperationID, WorkID: workID, OpKind: opKind, IdempotencyKey: entry.IdempotencyKey, Payload: payload}); err != nil {
		if opKind != LinearOpIssueCreate || !isLinearFailureKind(err, KindIdempotencyConflict) {
			return ClaimedLinearOperation{}, err
		}
		var existingPayload string
		if readErr := s.db.QueryRowContext(ctx, `SELECT operation_id, work_id, op_kind, idempotency_key, state, attempts, payload FROM linear_outbox WHERE idempotency_key=?`, clientUUID).Scan(&entry.OperationID, &entry.WorkID, &entry.OpKind, &entry.IdempotencyKey, &entry.State, &entry.Attempts, &existingPayload); readErr != nil {
			return ClaimedLinearOperation{}, wrapFailure(KindUnavailable, "linear_issue_enqueue", "cannot read concurrent creation", true, "retry once the database is readable", readErr)
		}
		entry.Payload = json.RawMessage(existingPayload)
		return entry, nil
	}
	// The link starts unpublished carrying the client UUID as its placeholder
	// remote identity; the drain replaces it with the confirmed identity. A
	// re-enqueue keeps the existing link — one link per work item, many
	// operations.
	var existingLink string
	if err := s.db.QueryRowContext(ctx, `SELECT link_state FROM linear_issue_links WHERE work_id=?`, workID).Scan(&existingLink); err == sql.ErrNoRows {
		if recordErr := s.RecordLinearLink(ctx, workID, clientUUID, "", "", "", "", LinearLinkUnpublished); recordErr != nil {
			return ClaimedLinearOperation{}, recordErr
		}
	} else if err != nil {
		return ClaimedLinearOperation{}, wrapFailure(KindUnavailable, "linear_issue_enqueue", "cannot read link", true, "retry once the database is readable", err)
	}
	return entry, nil
}

// ClaimLinearOperations moves up to limit queued operations to in_flight and
// returns exactly the operations this caller claimed. The state guard in the
// UPDATE makes the claim exclusive: an operation another caller already
// claimed stays out of this result.
func (s *Store) ClaimLinearOperations(ctx context.Context, limit int64) ([]ClaimedLinearOperation, error) {
	return s.claimLinearOperations(ctx, "", limit)
}

// ClaimLinearOperationsForProduct claims only operations whose work belongs to
// the selected Product. The Product is resolved before the transaction opens.
func (s *Store) ClaimLinearOperationsForProduct(ctx context.Context, productID string, limit int64) ([]ClaimedLinearOperation, error) {
	mode, err := s.ResolveLinearPlanningTarget(ctx, productID)
	if err != nil {
		return nil, err
	}
	if mode.PlanningMode == PlanningModeLocalOnly {
		return nil, newFailure(KindInvalidOperation, "linear_outbox_claim", "planning mode is local_only", false, "set planning_mode to linear_enabled before draining")
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
	queueQuery := `SELECT operation_id FROM linear_outbox WHERE state=?`
	queueArgs := []any{LinearOutboxQueued}
	if productID != "" {
		queueQuery += ` AND operation_id IN (SELECT o.operation_id FROM linear_outbox o JOIN work_projects wp ON wp.work_id=o.work_id JOIN product_projects pp ON pp.project_id=wp.project_id WHERE pp.product_id=?)`
		queueArgs = append(queueArgs, productID)
	}
	queueQuery += ` ORDER BY created_at LIMIT ?`
	queueArgs = append(queueArgs, limit)
	rows, err := tx.QueryContext(ctx, queueQuery, queueArgs...)
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
		if err := tx.QueryRowContext(ctx, `SELECT operation_id, work_id, op_kind, idempotency_key, state, attempts, payload FROM linear_outbox WHERE operation_id=?`, id).Scan(&op.OperationID, &op.WorkID, &op.OpKind, &op.IdempotencyKey, &op.State, &op.Attempts, &payload); err != nil {
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
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return wrapFailure(KindUnavailable, "linear_outbox_complete", "cannot open completion transaction", true, "retry once the database is writable", err)
	}
	defer tx.Rollback()
	if err := enterFold(ctx, tx); err != nil {
		return err
	}
	var workID, opKind, state, payload string
	if err := tx.QueryRowContext(ctx, `SELECT work_id, op_kind, state, payload FROM linear_outbox WHERE operation_id=?`, operationID).Scan(&workID, &opKind, &state, &payload); err == sql.ErrNoRows {
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
	if err := completeLinearLinkTx(ctx, tx, workID, opKind, identity, s.now().UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	if err := leaveFold(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return wrapFailure(KindUnavailable, "linear_outbox_complete", "cannot commit completed operation", true, "retry once the database is writable", err)
	}
	return nil
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
		return newFailure(KindInvalidTransition, "linear_outbox_complete", "a create operation cannot complete an already confirmed link", false, "complete the operation that owns the linked issue")
	}
	if currentState == LinearLinkConfirmed && opKind == LinearOpIssueUpdate {
		var currentUUID string
		if err := tx.QueryRowContext(ctx, `SELECT remote_issue_uuid FROM linear_issue_links WHERE work_id=?`, workID).Scan(&currentUUID); err != nil {
			return wrapFailure(KindUnavailable, "linear_outbox_complete", "cannot read linked remote identity", true, "retry once the database is readable", err)
		}
		if identity.RemoteUUID != currentUUID {
			return newFailure(KindProjectionConflict, "linear_outbox_complete", "update completion returned a different remote issue", false, "complete the operation against the verified linked issue")
		}
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

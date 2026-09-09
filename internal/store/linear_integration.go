package store

import (
	"context"
	"database/sql"
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
// the C15 managed saas_account resource carrying the linear metadata
// convention. No credential material is read or stored here.
type LinearConnection struct {
	ResourceID   string `json:"resource_id"`
	WorkspaceURL string `json:"workspace_url"`
	TeamID       string `json:"team_id"`
	AuthMode     string `json:"auth_mode"`
	State        string `json:"state"` // declared | partial | absent
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
// such resource exists.
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
	var connection *LinearConnection
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
		if v, ok := linear["auth_mode"].(string); ok {
			candidate.AuthMode = v
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

// CompleteLinearOperation moves an in_flight operation to done.
func (s *Store) CompleteLinearOperation(ctx context.Context, operationID string) error {
	return s.linearOutboxTransition(ctx, operationID, LinearOutboxInFlight, LinearOutboxDone, "")
}

// FailLinearOperation moves an in_flight operation to failed with its error.
func (s *Store) FailLinearOperation(ctx context.Context, operationID, lastError string) error {
	if lastError == "" {
		return newFailure(KindInvalidPayload, "linear_outbox_transition", "failure reason is required", false, "state why the operation failed")
	}
	return s.linearOutboxTransition(ctx, operationID, LinearOutboxInFlight, LinearOutboxFailed, lastError)
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
	if err := s.db.QueryRowContext(ctx, `SELECT count(*), coalesce(min(CASE WHEN state IN ('queued','in_flight') THEN created_at END), '') FROM linear_outbox`).Scan(&health.OutboxDepth, &oldestPending); err != nil {
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
	rows, err := s.db.QueryContext(ctx, `SELECT link_state, count(*) FROM linear_issue_links GROUP BY link_state`)
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

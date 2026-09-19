package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// WorkRemovalHandoff is the complete preservation record required before an
// execution item can leave the operational projections.
type WorkRemovalHandoff struct {
	Findings          []string `json:"findings"`
	RemainingScope    []string `json:"remaining_scope"`
	Blockers          []string `json:"blockers"`
	Artifacts         []string `json:"artifacts"`
	RenewalConditions []string `json:"renewal_conditions"`
}

// LinearHandoffConfirmation binds a remote handoff to one Product, issue, and
// exact local handoff digest. The store never calls Linear.
type LinearHandoffConfirmation struct {
	ProductID       string `json:"product_id"`
	RemoteIssueUUID string `json:"remote_issue_uuid"`
	Destination     string `json:"destination"`
	HandoffDigest   string `json:"handoff_digest"`
	CreationIntent  string `json:"creation_intent,omitempty"`
}

// WorkRemovalSession describes host evidence that still prevents removal.
type WorkRemovalSession struct {
	SessionRef string `json:"session_ref"`
	State      string `json:"state"` // active | unknown | vacated
}

// WorkRemovalRequest is one operator-approved, per-item removal operation.
// The booleans are explicit evidence gates, not stored liveness flags.
type WorkRemovalRequest struct {
	OperationID           string                     `json:"operation_id"`
	IdempotencyKey        string                     `json:"idempotency_key"`
	WorkID                string                     `json:"work_id"`
	ExpectedVersion       int64                      `json:"expected_version"`
	Reason                string                     `json:"reason"` // shelved | cancelled
	Actor                 string                     `json:"actor"`
	ProductID             string                     `json:"product_id,omitempty"`
	Handoff               WorkRemovalHandoff         `json:"handoff"`
	Linear                *LinearHandoffConfirmation `json:"linear,omitempty"`
	ExecutionRelinquished bool                       `json:"execution_relinquished"`
	WritesReconciled      bool                       `json:"writes_reconciled"`
	EffectsReconciled     bool                       `json:"effects_reconciled"`
	DependenciesResolved  bool                       `json:"dependencies_resolved"`
	ArtifactsVerified     bool                       `json:"artifacts_verified"`
	Sessions              []WorkRemovalSession       `json:"sessions,omitempty"`
}

// WorkRemovalReceipt is retained as a durable receipt and audit handoff after
// the WorkItem and its owned execution projections are removed.
type WorkRemovalReceipt struct {
	OperationID     string                     `json:"operation_id"`
	IdempotencyKey  string                     `json:"idempotency_key"`
	WorkID          string                     `json:"work_id"`
	ExpectedVersion int64                      `json:"expected_version"`
	Reason          string                     `json:"reason"`
	Actor           string                     `json:"actor"`
	ProductID       string                     `json:"product_id,omitempty"`
	Linear          *LinearHandoffConfirmation `json:"linear,omitempty"`
	Handoff         WorkRemovalHandoff         `json:"handoff"`
	HandoffDigest   string                     `json:"handoff_digest"`
	State           string                     `json:"state"`
	EventID         string                     `json:"event_id,omitempty"`
	Replayed        bool                       `json:"replayed"`
}

const WorkRemoved = "work.removed"

type workRemovedPayload struct {
	OperationID     string                     `json:"operation_id"`
	IdempotencyKey  string                     `json:"idempotency_key"`
	WorkID          string                     `json:"work_id"`
	ExpectedVersion int64                      `json:"expected_version"`
	Reason          string                     `json:"reason"`
	ProductID       string                     `json:"product_id,omitempty"`
	Actor           string                     `json:"actor"`
	Linear          *LinearHandoffConfirmation `json:"linear,omitempty"`
	Handoff         WorkRemovalHandoff         `json:"handoff"`
	HandoffDigest   string                     `json:"handoff_digest"`
}

// MarshalWorkRemovedPayload encodes the payload for a work.removed event.
// Keep this schema beside the fold that validates it so event producers do not
// maintain a second, incomplete payload shape.
func MarshalWorkRemovedPayload(req WorkRemovalRequest, handoffDigest string) ([]byte, error) {
	return json.Marshal(workRemovedPayload{
		OperationID:     req.OperationID,
		IdempotencyKey:  req.IdempotencyKey,
		WorkID:          req.WorkID,
		ExpectedVersion: req.ExpectedVersion,
		Reason:          req.Reason,
		ProductID:       req.ProductID,
		Actor:           req.Actor,
		Linear:          req.Linear,
		Handoff:         req.Handoff,
		HandoffDigest:   handoffDigest,
	})
}

func validateRemovalHandoff(h WorkRemovalHandoff) error {
	for name, values := range map[string][]string{
		"findings": h.Findings, "remaining scope": h.RemainingScope,
		"blockers": h.Blockers, "artifacts": h.Artifacts,
		"renewal conditions": h.RenewalConditions,
	} {
		if len(values) == 0 {
			return newFailure(KindInvalidOperation, "work_removal", "handoff is missing "+name, false, "supply every handoff section")
		}
		if len(values) > 64 {
			return newFailure(KindLimitExceeded, "work_removal", name+" has too many entries", false, "supply at most 64 entries per handoff section")
		}
		for _, value := range values {
			if strings.TrimSpace(value) == "" || len(value) > 4096 {
				return newFailure(KindInvalidOperation, "work_removal", name+" contains an empty or oversized entry", false, "supply bounded non-empty handoff entries")
			}
		}
	}
	return nil
}

func removalHandoffDigest(h WorkRemovalHandoff) string {
	b, _ := json.Marshal(h)
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func linearConfirmationJSON(linear *LinearHandoffConfirmation) (string, error) {
	if linear == nil {
		return "{}", nil
	}
	b, err := json.Marshal(linear)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func validateRemovalRequest(req WorkRemovalRequest) error {
	if len(req.OperationID) < 2 || len(req.OperationID) > 128 || len(req.IdempotencyKey) < 2 || len(req.IdempotencyKey) > 128 || len(req.WorkID) < 2 || len(req.WorkID) > 128 {
		return newFailure(KindInvalidOperation, "work_removal", "operation, idempotency, and work identities must be bounded", false, "supply identities from 2 to 128 characters")
	}
	if req.ExpectedVersion < 1 || strings.TrimSpace(req.Actor) == "" {
		return newFailure(KindInvalidOperation, "work_removal", "positive expected version and actor are required", false, "pin the current work version and operator")
	}
	if req.Reason != "shelved" && req.Reason != "cancelled" {
		return newFailure(KindInvalidOperation, "work_removal", "removal reason is not recognized", false, "use shelved or cancelled")
	}
	if !req.ExecutionRelinquished || !req.WritesReconciled || !req.EffectsReconciled || !req.DependenciesResolved || !req.ArtifactsVerified {
		return newFailure(KindInvalidOperation, "work_removal", "removal safety evidence is incomplete", false, "reconcile execution, writes, effects, dependencies, and artifacts")
	}
	if err := validateRemovalHandoff(req.Handoff); err != nil {
		return err
	}
	for _, session := range req.Sessions {
		if session.SessionRef == "" || (session.State != "vacated" && session.State != "active" && session.State != "unknown") {
			return newFailure(KindInvalidOperation, "work_removal", "session evidence is malformed", false, "supply a bounded session state")
		}
		if session.State != "vacated" {
			return newFailure(KindResourceClaimHeld, "work_removal", "a live or unknown host session still names the work", false, "vacate and verify every host session before removal")
		}
	}
	return nil
}

// ShelveWork performs an operator-directed preservation-first removal.
func (s *Store) ShelveWork(ctx context.Context, req WorkRemovalRequest) (WorkRemovalReceipt, error) {
	req.Reason = "shelved"
	return s.RemoveWork(ctx, req)
}

// CancelWork uses the same safety gates but records a cancellation reason.
func (s *Store) CancelWork(ctx context.Context, req WorkRemovalRequest) (WorkRemovalReceipt, error) {
	req.Reason = "cancelled"
	return s.RemoveWork(ctx, req)
}

// PrepareWorkRemoval records the stable operation and pinned handoff without
// deleting anything. It is the reconciliation boundary for remote publication.
func (s *Store) PrepareWorkRemoval(ctx context.Context, req WorkRemovalRequest) (WorkRemovalReceipt, error) {
	if err := validateRemovalRequest(req); err != nil {
		return WorkRemovalReceipt{}, err
	}
	if s == nil || s.db == nil {
		return WorkRemovalReceipt{}, newFailure(KindUnavailable, "work_removal_prepare", "store is not open", false, "open the authority database")
	}
	requestedLinearJSON, err := linearConfirmationJSON(req.Linear)
	if err != nil {
		return WorkRemovalReceipt{}, newFailure(KindInvalidOperation, "work_removal_prepare", "Linear confirmation cannot be encoded", false, "supply a JSON-encodable Linear confirmation")
	}
	var priorState string
	err = s.db.QueryRowContext(ctx, `SELECT state FROM work_removal_operations WHERE operation_id=?`, req.OperationID).Scan(&priorState)
	if err == nil && priorState == "committed" {
		receipt, readErr := s.readWorkRemovalAuditByOperation(ctx, req.OperationID)
		if readErr != nil {
			return WorkRemovalReceipt{}, readErr
		}
		storedLinearJSON, _ := linearConfirmationJSON(receipt.Linear)
		if receipt.IdempotencyKey != req.IdempotencyKey || receipt.WorkID != req.WorkID || receipt.ExpectedVersion != req.ExpectedVersion || receipt.Reason != req.Reason || receipt.Actor != req.Actor || receipt.ProductID != req.ProductID || receipt.HandoffDigest != removalHandoffDigest(req.Handoff) || storedLinearJSON != requestedLinearJSON {
			return WorkRemovalReceipt{}, newFailure(KindIdempotencyConflict, "work_removal_prepare", "committed operation identity is bound to different removal input", false, "reuse the original removal request")
		}
		receipt.Replayed = true
		return receipt, nil
	}
	if err != nil && err != sql.ErrNoRows {
		return WorkRemovalReceipt{}, wrapFailure(KindUnavailable, "work_removal_prepare", "cannot read removal operation", true, "retry once the database is readable", err)
	}
	if err := s.validateRemovalDestination(ctx, req); err != nil {
		return WorkRemovalReceipt{}, err
	}
	if err := s.validateRemovalGates(ctx, req); err != nil {
		return WorkRemovalReceipt{}, err
	}
	digest := removalHandoffDigest(req.Handoff)
	if req.Linear != nil {
		if req.Linear.HandoffDigest != digest || req.Linear.Destination != "linear" || req.Linear.RemoteIssueUUID == "" {
			return WorkRemovalReceipt{}, newFailure(KindInvalidOperation, "work_removal_prepare", "Linear confirmation does not match the handoff", false, "confirm the exact Product, issue, destination, and handoff digest")
		}
	}
	linearJSON := requestedLinearJSON
	receipt := WorkRemovalReceipt{OperationID: req.OperationID, IdempotencyKey: req.IdempotencyKey, WorkID: req.WorkID, ExpectedVersion: req.ExpectedVersion, Reason: req.Reason, Actor: req.Actor, ProductID: req.ProductID, Linear: req.Linear, Handoff: req.Handoff, HandoffDigest: digest, State: "prepared"}
	handoffJSON, err := json.Marshal(req.Handoff)
	if err != nil {
		return WorkRemovalReceipt{}, newFailure(KindInvalidOperation, "work_removal_prepare", "handoff cannot be encoded", false, "supply a JSON-encodable handoff")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return WorkRemovalReceipt{}, wrapFailure(KindUnavailable, "work_removal_prepare", "cannot begin removal preparation", true, "retry once the database is writable", err)
	}
	defer tx.Rollback()
	if err := enterFold(ctx, tx); err != nil {
		return WorkRemovalReceipt{}, err
	}
	var stored WorkRemovalReceipt
	var handoffRaw string
	var storedLinearRaw string
	err = tx.QueryRowContext(ctx, `SELECT operation_id,idempotency_key,work_id,expected_version,reason,actor,product_id,linear_confirmation_json,handoff_json,handoff_digest,state,coalesce(event_id,'') FROM work_removal_operations WHERE operation_id=?`, req.OperationID).Scan(&stored.OperationID, &stored.IdempotencyKey, &stored.WorkID, &stored.ExpectedVersion, &stored.Reason, &stored.Actor, &stored.ProductID, &storedLinearRaw, &handoffRaw, &stored.HandoffDigest, &stored.State, &stored.EventID)
	if err == nil {
		if stored.IdempotencyKey != req.IdempotencyKey || stored.WorkID != req.WorkID || stored.ExpectedVersion != req.ExpectedVersion || stored.Reason != req.Reason || stored.Actor != req.Actor || stored.ProductID != req.ProductID || stored.HandoffDigest != digest || storedLinearRaw != requestedLinearJSON {
			return WorkRemovalReceipt{}, newFailure(KindIdempotencyConflict, "work_removal_prepare", "operation identity is bound to different removal input", false, "reuse the original removal request")
		}
		if json.Unmarshal([]byte(handoffRaw), &stored.Handoff) != nil {
			return WorkRemovalReceipt{}, newFailure(KindInvalidPayload, "work_removal_prepare", "stored handoff is malformed", false, "repair the removal receipt")
		}
		if storedLinearRaw != "{}" {
			stored.Linear = &LinearHandoffConfirmation{}
			if json.Unmarshal([]byte(storedLinearRaw), stored.Linear) != nil {
				return WorkRemovalReceipt{}, newFailure(KindInvalidPayload, "work_removal_prepare", "stored Linear confirmation is malformed", false, "repair the removal receipt")
			}
		}
		if stored.State == "prepared" {
			if err := validateRemovalDestinationQ(ctx, tx, req); err != nil {
				return WorkRemovalReceipt{}, err
			}
			if err := validateRemovalGatesQ(ctx, tx, req); err != nil {
				return WorkRemovalReceipt{}, err
			}
		}
		if err := leaveFold(ctx, tx); err != nil {
			return WorkRemovalReceipt{}, err
		}
		if err := tx.Commit(); err != nil {
			return WorkRemovalReceipt{}, err
		}
		stored.Replayed = true
		return stored, nil
	}
	if err != sql.ErrNoRows {
		return WorkRemovalReceipt{}, wrapFailure(KindUnavailable, "work_removal_prepare", "cannot read removal operation", true, "retry once the database is readable", err)
	}
	var existingWorkOperation string
	if err := tx.QueryRowContext(ctx, `SELECT operation_id FROM work_removal_operations WHERE work_id=?`, req.WorkID).Scan(&existingWorkOperation); err == nil && existingWorkOperation != req.OperationID {
		return WorkRemovalReceipt{}, newFailure(KindIdempotencyConflict, "work_removal_prepare", "work item already has another removal operation", false, "resume the existing removal operation")
	} else if err != nil && err != sql.ErrNoRows {
		return WorkRemovalReceipt{}, wrapFailure(KindUnavailable, "work_removal_prepare", "cannot inspect work removal identity", true, "retry once the receipt is readable", err)
	}
	var existingOperation string
	if err := tx.QueryRowContext(ctx, `SELECT operation_id FROM work_removal_operations WHERE idempotency_key=?`, req.IdempotencyKey).Scan(&existingOperation); err == nil && existingOperation != req.OperationID {
		return WorkRemovalReceipt{}, newFailure(KindIdempotencyConflict, "work_removal_prepare", "idempotency key belongs to another removal operation", false, "reuse the original operation identity")
	} else if err != nil && err != sql.ErrNoRows {
		return WorkRemovalReceipt{}, wrapFailure(KindUnavailable, "work_removal_prepare", "cannot inspect removal idempotency", true, "retry once the receipt is readable", err)
	}
	if err := validateRemovalDestinationQ(ctx, tx, req); err != nil {
		return WorkRemovalReceipt{}, err
	}
	if err := validateRemovalGatesQ(ctx, tx, req); err != nil {
		return WorkRemovalReceipt{}, err
	}
	now := s.now().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `INSERT INTO work_removal_operations(operation_id,idempotency_key,work_id,expected_version,reason,actor,product_id,linear_confirmation_json,handoff_json,handoff_digest,state,event_id,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,'prepared','',?,?)`, req.OperationID, req.IdempotencyKey, req.WorkID, req.ExpectedVersion, req.Reason, req.Actor, req.ProductID, linearJSON, string(handoffJSON), digest, now, now); err != nil {
		return WorkRemovalReceipt{}, wrapFailure(KindUnavailable, "work_removal_prepare", "cannot persist removal operation", true, "retry the same idempotency key", err)
	}
	if err := leaveFold(ctx, tx); err != nil {
		return WorkRemovalReceipt{}, err
	}
	if err := tx.Commit(); err != nil {
		return WorkRemovalReceipt{}, wrapFailure(KindUnavailable, "work_removal_prepare", "cannot commit removal preparation", true, "retry the same idempotency key", err)
	}
	return receipt, nil
}

// PrepareWorkRemovalTx performs the preparation inside a caller-owned store
// transaction. Mutation routes use this form so approval, fencing, and final
// removal share one SQLite transaction and never nest a Store query.
func PrepareWorkRemovalTx(ctx context.Context, transaction *Transaction, req WorkRemovalRequest) (WorkRemovalReceipt, error) {
	tx, err := transactionSQL(transaction, "work_removal_prepare")
	if err != nil {
		return WorkRemovalReceipt{}, err
	}
	if err := validateRemovalRequest(req); err != nil {
		return WorkRemovalReceipt{}, err
	}
	linearJSON, err := linearConfirmationJSON(req.Linear)
	if err != nil {
		return WorkRemovalReceipt{}, newFailure(KindInvalidOperation, "work_removal_prepare", "Linear confirmation cannot be encoded", false, "supply a JSON-encodable Linear confirmation")
	}
	digest := removalHandoffDigest(req.Handoff)
	var state string
	err = tx.QueryRowContext(ctx, `SELECT state FROM work_removal_operations WHERE operation_id=?`, req.OperationID).Scan(&state)
	if err == nil {
		stored, readErr := readWorkRemovalAuditQ(ctx, tx, "operation_id", req.OperationID)
		if readErr != nil {
			return WorkRemovalReceipt{}, readErr
		}
		storedLinear, _ := linearConfirmationJSON(stored.Linear)
		if stored.IdempotencyKey != req.IdempotencyKey || stored.WorkID != req.WorkID || stored.ExpectedVersion != req.ExpectedVersion || stored.Reason != req.Reason || stored.Actor != req.Actor || stored.ProductID != req.ProductID || stored.HandoffDigest != digest || storedLinear != linearJSON {
			return WorkRemovalReceipt{}, newFailure(KindIdempotencyConflict, "work_removal_prepare", "operation identity is bound to different removal input", false, "reuse the original removal operation")
		}
		if state == "committed" {
			stored.Replayed = true
			return stored, nil
		}
		if err := validateRemovalDestinationQ(ctx, tx, req); err != nil {
			return WorkRemovalReceipt{}, err
		}
		if err := validateRemovalGatesQ(ctx, tx, req); err != nil {
			return WorkRemovalReceipt{}, err
		}
		if req.Linear != nil && (req.Linear.HandoffDigest != digest || req.Linear.Destination != "linear" || req.Linear.RemoteIssueUUID == "") {
			return WorkRemovalReceipt{}, newFailure(KindInvalidOperation, "work_removal_prepare", "Linear confirmation does not match the handoff", false, "confirm the exact Product, issue, destination, and handoff digest")
		}
		return stored, nil
	}
	if err != sql.ErrNoRows {
		return WorkRemovalReceipt{}, wrapFailure(KindUnavailable, "work_removal_prepare", "cannot read removal operation", true, "retry once the removal operation is readable", err)
	}
	var existing string
	if err := tx.QueryRowContext(ctx, `SELECT operation_id FROM work_removal_operations WHERE idempotency_key=? OR work_id=? LIMIT 1`, req.IdempotencyKey, req.WorkID).Scan(&existing); err == nil && existing != req.OperationID {
		return WorkRemovalReceipt{}, newFailure(KindIdempotencyConflict, "work_removal_prepare", "removal identity is already bound to another operation", false, "reuse the existing removal operation")
	} else if err != nil && err != sql.ErrNoRows {
		return WorkRemovalReceipt{}, wrapFailure(KindUnavailable, "work_removal_prepare", "cannot inspect removal identity", true, "retry once the removal receipt is readable", err)
	}
	if err := validateRemovalDestinationQ(ctx, tx, req); err != nil {
		return WorkRemovalReceipt{}, err
	}
	if err := validateRemovalGatesQ(ctx, tx, req); err != nil {
		return WorkRemovalReceipt{}, err
	}
	if req.Linear != nil && (req.Linear.HandoffDigest != digest || req.Linear.Destination != "linear" || req.Linear.RemoteIssueUUID == "") {
		return WorkRemovalReceipt{}, newFailure(KindInvalidOperation, "work_removal_prepare", "Linear confirmation does not match the handoff", false, "confirm the exact Product, issue, destination, and handoff digest")
	}
	handoffJSON, err := json.Marshal(req.Handoff)
	if err != nil {
		return WorkRemovalReceipt{}, newFailure(KindInvalidOperation, "work_removal_prepare", "handoff cannot be encoded", false, "supply a JSON-encodable handoff")
	}
	if err := enterFold(ctx, tx); err != nil {
		return WorkRemovalReceipt{}, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `INSERT INTO work_removal_operations(operation_id,idempotency_key,work_id,expected_version,reason,actor,product_id,linear_confirmation_json,handoff_json,handoff_digest,state,event_id,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,'prepared','',?,?)`, req.OperationID, req.IdempotencyKey, req.WorkID, req.ExpectedVersion, req.Reason, req.Actor, req.ProductID, linearJSON, string(handoffJSON), digest, now, now); err != nil {
		return WorkRemovalReceipt{}, wrapFailure(KindUnavailable, "work_removal_prepare", "cannot persist removal operation", true, "retry the same idempotency key", err)
	}
	if err := leaveFold(ctx, tx); err != nil {
		return WorkRemovalReceipt{}, err
	}
	return WorkRemovalReceipt{OperationID: req.OperationID, IdempotencyKey: req.IdempotencyKey, WorkID: req.WorkID, ExpectedVersion: req.ExpectedVersion, Reason: req.Reason, Actor: req.Actor, ProductID: req.ProductID, Linear: req.Linear, Handoff: req.Handoff, HandoffDigest: digest, State: "prepared"}, nil
}

func (s *Store) validateRemovalDestination(ctx context.Context, req WorkRemovalRequest) error {
	return validateRemovalDestinationQ(ctx, s.db, req)
}

func validateRemovalDestinationQ(ctx context.Context, q queryer, req WorkRemovalRequest) error {
	if req.ProductID == "" {
		if req.Linear != nil {
			return newFailure(KindAmbiguousScope, "work_removal_prepare", "Linear confirmation has no Product destination", false, "supply the authorized Product")
		}
		return nil
	}
	var planningMode string
	if err := q.QueryRowContext(ctx, `SELECT planning_mode FROM products WHERE id=?`, req.ProductID).Scan(&planningMode); err == sql.ErrNoRows {
		return newFailure(KindUnknownScope, "planning_mode_resolve", "Product does not exist", false, "supply an existing Product")
	} else if err != nil {
		return wrapFailure(KindUnavailable, "planning_mode_resolve", "cannot read Product planning mode", true, "retry once the Product projection is readable", err)
	}
	if planningMode == PlanningModeLocalOnly {
		if req.Linear != nil {
			return newFailure(KindInvalidOperation, "work_removal_prepare", "local-only work cannot use a Linear destination", false, "preserve the handoff in an operator-selected non-Linear record")
		}
		return nil
	}
	if req.Linear == nil {
		return newFailure(KindInvalidOperation, "work_removal_prepare", "Linear-enabled work has no confirmed preservation", false, "confirm the authorized Linear issue and handoff before removal")
	}
	var resourceID, metadataJSON string
	var resourceVersion int64
	err := q.QueryRowContext(ctx, `SELECT r.resource_id,r.version,r.metadata FROM managed_resources r JOIN resource_products rp ON rp.resource_id=r.resource_id WHERE rp.product_id=? AND rp.role='owner' AND r.class='saas' AND r.kind='saas_account' ORDER BY r.resource_id LIMIT 1`, req.ProductID).Scan(&resourceID, &resourceVersion, &metadataJSON)
	if err == sql.ErrNoRows {
		return newFailure(KindInvalidOperation, "work_removal_prepare", "Linear destination is not fully declared", false, "declare the Product Linear connection before removal")
	}
	if err != nil {
		return wrapFailure(KindUnavailable, "work_removal_prepare", "cannot read the Linear destination", true, "retry once the Linear connection is readable", err)
	}
	var metadata map[string]any
	var linear map[string]any
	if json.Unmarshal([]byte(metadataJSON), &metadata) == nil {
		linear, _ = metadata["linear"].(map[string]any)
	}
	workspace, _ := linear["workspace_url"].(string)
	team, _ := linear["team_id"].(string)
	authMode, _ := linear["auth_mode"].(string)
	if resourceID == "" || resourceVersion < 1 || workspace == "" || team == "" || authMode == "" {
		return newFailure(KindInvalidOperation, "work_removal_prepare", "Linear destination is not fully declared", false, "declare the Product Linear connection before removal")
	}
	if req.Linear.ProductID != req.ProductID || req.Linear.Destination != "linear" || req.Linear.RemoteIssueUUID == "" {
		return newFailure(KindInvalidRelation, "work_removal_prepare", "Linear confirmation does not name the authorized destination", false, "confirm the exact Product and remote issue")
	}
	var linkedUUID, state string
	err = q.QueryRowContext(ctx, `SELECT remote_issue_uuid,link_state FROM linear_issue_links WHERE work_id=?`, req.WorkID).Scan(&linkedUUID, &state)
	if err == nil && state == LinearLinkConfirmed && linkedUUID != req.Linear.RemoteIssueUUID {
		return newFailure(KindInvalidRelation, "work_removal_prepare", "Linear confirmation does not match the local issue identity", false, "confirm the locally linked remote issue")
	}
	if err != nil && err != sql.ErrNoRows {
		return wrapFailure(KindUnavailable, "work_removal_prepare", "cannot read the local Linear identity", true, "retry once the Linear link is readable", err)
	}
	return nil
}

// CommitWorkRemoval appends the typed removal event. The event fold performs
// the final atomic deletion, so replay reconstructs absence rather than a row.
func (s *Store) CommitWorkRemoval(ctx context.Context, req WorkRemovalRequest) (WorkRemovalReceipt, error) {
	prepared, err := s.PrepareWorkRemoval(ctx, req)
	if err != nil {
		return WorkRemovalReceipt{}, err
	}
	if prepared.State == "committed" {
		prepared.Replayed = true
		return prepared, nil
	}
	payload, _ := json.Marshal(workRemovedPayload{OperationID: req.OperationID, IdempotencyKey: req.IdempotencyKey, WorkID: req.WorkID, ExpectedVersion: req.ExpectedVersion, Reason: req.Reason, ProductID: req.ProductID, Actor: req.Actor, Linear: req.Linear, Handoff: req.Handoff, HandoffDigest: removalHandoffDigest(req.Handoff)})
	event := Event{EventID: "work.removed:" + req.OperationID, Kind: WorkRemoved, SubjectType: SubjectWorkItem, SubjectID: req.WorkID, Actor: req.Actor, OccurredAt: s.now(), PayloadVersion: 1, Payload: payload}
	result, err := ApplyOperationWithResult(ctx, s, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, req.WorkID): req.ExpectedVersion}})
	if err != nil {
		return WorkRemovalReceipt{}, err
	}
	prepared.State = "committed"
	prepared.EventID = result.EventIDs[0]
	return prepared, nil
}

// RemoveWork is the complete local path. A caller that must reconcile an
// external write first uses PrepareWorkRemoval and then CommitWorkRemoval.
func (s *Store) RemoveWork(ctx context.Context, req WorkRemovalRequest) (WorkRemovalReceipt, error) {
	return s.CommitWorkRemoval(ctx, req)
}

func (s *Store) validateRemovalGates(ctx context.Context, req WorkRemovalRequest) error {
	return validateRemovalGatesQ(ctx, s.db, req)
}

func validateRemovalGatesQ(ctx context.Context, q queryer, req WorkRemovalRequest) error {
	var lifecycle string
	if err := q.QueryRowContext(ctx, `SELECT lifecycle FROM work_items WHERE id=?`, req.WorkID).Scan(&lifecycle); err == sql.ErrNoRows {
		return newFailure(KindProjectionNotFound, "work_removal", "work item does not exist", false, "supply the current work item")
	} else if err != nil {
		return wrapFailure(KindUnavailable, "work_removal", "cannot read work item", true, "retry once the database is readable", err)
	}
	if lifecycle == "completed" || lifecycle == "superseded" {
		return newFailure(KindInvalidOperation, "work_removal", "completed and superseded work cannot be removed by this operation", false, "retain terminal work or use a separately accepted retention operation")
	}
	var got int64
	if err := q.QueryRowContext(ctx, `SELECT version FROM work_items WHERE id=?`, req.WorkID).Scan(&got); err != nil {
		return err
	}
	if got != req.ExpectedVersion {
		conflict, conflictErr := versionConflictForQuery(ctx, q, SubjectWorkItem, req.WorkID, req.ExpectedVersion, got, true)
		if conflictErr != nil {
			return conflictErr
		}
		return conflict
	}
	checks := []struct{ query, detail string }{
		{`SELECT count(*) FROM relations WHERE work_id_from=? OR work_id_to=?`, "work has unresolved relation dependencies"},
		{`SELECT count(*) FROM workflow_impact_edges WHERE work_id=? OR target_work_id=?`, "work has unresolved workflow impact dependencies"},
		{`SELECT count(*) FROM workflow_external_conditions WHERE work_id=? AND condition_state='open'`, "work has an unresolved external wait"},
		{`SELECT count(*) FROM worker_attempts WHERE work_id=? AND lifecycle_state='dispatched'`, "work has a live or unknown worker attempt"},
		{`SELECT count(*) FROM resource_claims WHERE holder_work_id=? AND state='held'`, "work has an active resource claim"},
		{`SELECT count(*) FROM work_messages WHERE (sender_work_id=? OR recipient_work_id=?) AND state='sent'`, "work has an undelivered message"},
		{`SELECT count(*) FROM worktree_claims WHERE work_id=? AND state IN ('pending','verified')`, "work has an active worktree claim"},
		{`SELECT count(*) FROM worktree_entries WHERE set_id=? AND state='active'`, "work has an active worktree"},
		{`SELECT count(*) FROM worktree_verify_leases WHERE work_id=? AND state='held'`, "work has a held worktree verification lease"},
		{`SELECT count(*) FROM linear_outbox WHERE work_id=? AND state IN ('queued','in_flight')`, "work has an unreconciled external write"},
		{`SELECT count(*) FROM linear_issue_links WHERE work_id=? AND link_state IN ('pending','degraded')`, "work has an unreconciled Linear identity"},
		{`SELECT count(*) FROM bootstrap_operations WHERE work_id=? AND state IN ('pending','creating','native_ready','rolling_back')`, "work has an incomplete bootstrap effect"},
		{`SELECT count(*) FROM active_research_consumers WHERE consumer_work_id=? AND required=1`, "work has a required research consumer"},
		{`SELECT count(*) FROM active_research_consumers c JOIN active_research_packs p ON p.pack_id=c.pack_id WHERE p.owner_work_id=? AND c.required=1 AND c.consumer_work_id<>?`, "work has a required research consumer that depends on its findings"},
		{`SELECT count(*) FROM durable_operations WHERE work_id=? AND (result_kind IS NULL OR result_kind IN ('pending','partial'))`, "work has an unreconciled durable operation"},
	}
	for _, check := range checks {
		args := []any{req.WorkID}
		if strings.Count(check.query, "?") > 1 {
			args = []any{req.WorkID, req.WorkID}
		}
		if strings.Contains(check.query, "set_id=?") {
			args = []any{WorktreeSetID(req.WorkID)}
		}
		var count int
		if err := q.QueryRowContext(ctx, check.query, args...).Scan(&count); err != nil {
			return wrapFailure(KindUnavailable, "work_removal", "cannot evaluate removal gate", true, "retry once the operational projections are readable", err)
		}
		if count > 0 {
			return newFailure(KindResourceClaimHeld, "work_removal", check.detail, false, "reconcile the named execution projection before removal")
		}
	}
	if req.Linear != nil {
		if req.ProductID != "" && req.Linear.ProductID != req.ProductID {
			return newFailure(KindInvalidRelation, "work_removal", "Linear confirmation names a different Product", false, "confirm the configured Product destination")
		}
	}
	return nil
}

func foldWorkRemoved(ctx context.Context, tx *sql.Tx, event Event) error {
	if err := checkSubject(event, SubjectWorkItem); err != nil {
		return err
	}
	var payload workRemovedPayload
	if err := decodePayload(event, &payload); err != nil {
		return err
	}
	if payload.WorkID != event.SubjectID || payload.OperationID == "" || payload.Actor == "" || payload.Actor != event.Actor || payload.Reason != "shelved" && payload.Reason != "cancelled" || payload.HandoffDigest != removalHandoffDigest(payload.Handoff) {
		return newFailure(KindInvalidPayload, "fold_event", "work.removed payload is not a verified removal", false, "supply the pinned removal operation and handoff")
	}
	var state, idempotencyKey, storedWorkID, storedLinearRaw, storedDigest, storedEventID string
	err := tx.QueryRowContext(ctx, `SELECT state,idempotency_key,work_id,linear_confirmation_json,handoff_digest,event_id FROM work_removal_operations WHERE operation_id=?`, payload.OperationID).Scan(&state, &idempotencyKey, &storedWorkID, &storedLinearRaw, &storedDigest, &storedEventID)
	if err == sql.ErrNoRows {
		return newFailure(KindProjectionNotFound, "fold_event", "removal operation is not prepared", false, "prepare the removal operation before committing it")
	}
	if err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot read removal operation", true, "retry once the removal operation is readable", err)
	}
	payloadLinearJSON, linearErr := linearConfirmationJSON(payload.Linear)
	var storedActor string
	if err := tx.QueryRowContext(ctx, `SELECT actor FROM work_removal_operations WHERE operation_id=?`, payload.OperationID).Scan(&storedActor); err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot read removal operation actor", true, "retry once the removal operation is readable", err)
	}
	if linearErr != nil || (state != "prepared" && !(state == "committed" && storedEventID == event.EventID)) || idempotencyKey != payload.IdempotencyKey || storedWorkID != event.SubjectID || storedActor != payload.Actor || storedDigest != payload.HandoffDigest || storedLinearRaw != payloadLinearJSON {
		return newFailure(KindIdempotencyConflict, "fold_event", "removal event does not match its prepared operation", false, "reuse the prepared removal operation")
	}
	if err := validateRemovalGatesQ(ctx, tx, WorkRemovalRequest{WorkID: event.SubjectID, ExpectedVersion: payload.ExpectedVersion}); err != nil {
		return err
	}
	if err := deleteWorkOwnedProjections(ctx, tx, event.SubjectID); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE work_removal_operations SET state='committed',event_id=?,updated_at=? WHERE operation_id=? AND work_id=?`, event.EventID, event.OccurredAt.UTC().Format(time.RFC3339Nano), payload.OperationID, event.SubjectID)
	if err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot close removal receipt", true, "retry once the database is writable", err)
	}
	if n, _ := result.RowsAffected(); n == 0 {
		handoff, _ := json.Marshal(payload.Handoff)
		linearJSON, _ := linearConfirmationJSON(payload.Linear)
		if _, err := tx.ExecContext(ctx, `INSERT INTO work_removal_operations(operation_id,idempotency_key,work_id,expected_version,reason,actor,product_id,linear_confirmation_json,handoff_json,handoff_digest,state,event_id,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,'committed',?,?,?)`, payload.OperationID, payload.IdempotencyKey, event.SubjectID, payload.ExpectedVersion, payload.Reason, payload.Actor, payload.ProductID, linearJSON, string(handoff), payload.HandoffDigest, event.EventID, event.OccurredAt.UTC().Format(time.RFC3339Nano), event.OccurredAt.UTC().Format(time.RFC3339Nano)); err != nil {
			return wrapFailure(KindUnavailable, "fold_event", "cannot preserve removal receipt", true, "repair the removal operation and rebuild", err)
		}
	}
	return nil
}

func deleteWorkOwnedProjections(ctx context.Context, tx *sql.Tx, workID string) error {
	// This order follows the FK graph. The table names are closed source-owned
	// identifiers, not caller input. Shared Product, Project, actor, and law
	// rows have no work-item ownership predicate and remain untouched.
	if _, err := tx.ExecContext(ctx, `DELETE FROM workflow_overlap_resolutions WHERE from_work_id=? OR to_work_id=?`, workID, workID); err != nil {
		return projectionDeleteFailure("workflow_overlap_resolutions", err)
	}
	// An alignment row names the searching item and, for a related_found
	// outcome, one item the search found. Both columns carry a RESTRICT
	// foreign key, so removing either end must clear the row first.
	if _, err := tx.ExecContext(ctx, `DELETE FROM workflow_backlog_alignment WHERE work_id=? OR related_work_id=?`, workID, workID); err != nil {
		return projectionDeleteFailure("workflow_backlog_alignment", err)
	}
	tables := []string{"workflow_impact_notices", "workflow_candidate_sets", "workflow_contract_predicates", "workflow_contract_law_revisions", "workflow_contract_law_modifications", "workflow_contract_verification_obligations", "workflow_contract_law_additions", "workflow_contract_domain_relation_modifications", "workflow_contract_domain_modifications", "workflow_contract_affected_domains", "workflow_law_addition_reservations", "workflow_architecture_bindings", "workflow_premise_confirmations", "workflow_context_boundaries", "workflow_context_checkpoints", "workflow_impact_edges", "workflow_external_conditions", "workflow_checkpoints", "workflow_decision_records", "workflow_native_runs", "workflow_contracts", "workflow_design_records", "workflow_proposal_records", "workflow_instances", "resource_claims", "work_messages", "work_observations", "external_observations", "worker_attempts", "initiative_entries", "relations", "work_projects", "linear_outbox", "linear_issue_links"}
	for _, table := range tables {
		_, deleteErr := tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE work_id=?`, workID) //nolint:gosec // table comes only from the closed FK-order projection list above and the work ID stays parameter-bound.
		if deleteErr == nil {
			continue
		}
		// Tables with a different owner column are handled explicitly below.
		switch table {
		case "workflow_impact_notices":
			if _, err := tx.ExecContext(ctx, `DELETE FROM workflow_impact_notices WHERE source_work_id=? OR target_work_id=? OR edge_owner_work_id=?`, workID, workID, workID); err != nil {
				return projectionDeleteFailure(table, err)
			}
		case "workflow_impact_edges":
			if _, err := tx.ExecContext(ctx, `DELETE FROM workflow_impact_edges WHERE work_id=? OR target_work_id=?`, workID, workID); err != nil {
				return projectionDeleteFailure(table, err)
			}
		case "work_messages":
			if _, err := tx.ExecContext(ctx, `DELETE FROM work_messages WHERE sender_work_id=? OR recipient_work_id=?`, workID, workID); err != nil {
				return projectionDeleteFailure(table, err)
			}
		case "relations":
			if _, err := tx.ExecContext(ctx, `DELETE FROM relations WHERE work_id_from=? OR work_id_to=?`, workID, workID); err != nil {
				return projectionDeleteFailure(table, err)
			}
		case "initiative_entries":
			if _, err := tx.ExecContext(ctx, `DELETE FROM initiative_entries WHERE initiative_work_id=? OR child_work_id=?`, workID, workID); err != nil {
				return projectionDeleteFailure(table, err)
			}
		case "workflow_law_addition_reservations":
			if _, err := tx.ExecContext(ctx, `DELETE FROM workflow_law_addition_reservations WHERE owner_work_id=?`, workID); err != nil {
				return projectionDeleteFailure(table, err)
			}
		case "work_projects":
			if _, err := tx.ExecContext(ctx, `DELETE FROM work_projects WHERE work_id=?`, workID); err != nil {
				return projectionDeleteFailure(table, err)
			}
		case "resource_claims":
			if _, err := tx.ExecContext(ctx, `DELETE FROM resource_claims WHERE holder_work_id=?`, workID); err != nil {
				return projectionDeleteFailure(table, err)
			}
		default:
			return projectionDeleteFailure(table, deleteErr)
		}
	}
	for _, statement := range []struct {
		table string
		query string
	}{
		{"active_research_consumers", `DELETE FROM active_research_consumers WHERE consumer_work_id=?`},
		{"active_research_finding_scopes", `DELETE FROM active_research_finding_scopes WHERE pack_id IN (SELECT pack_id FROM active_research_packs WHERE owner_work_id=?)`},
		{"bootstrap_operations", `DELETE FROM bootstrap_operations WHERE work_id=?`},
		{"worktree_verify_leases", `DELETE FROM worktree_verify_leases WHERE work_id=?`},
		{"durable_operations", `DELETE FROM durable_operations WHERE work_id=?`},
	} {
		var err error
		_, err = tx.ExecContext(ctx, statement.query, workID)
		if err != nil {
			return projectionDeleteFailure(statement.table, err)
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM worktree_claims WHERE work_id=?`, workID); err != nil {
		return projectionDeleteFailure("worktree_claims", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM worktree_entries WHERE set_id=?`, WorktreeSetID(workID)); err != nil {
		return projectionDeleteFailure("worktree_entries", err)
	}
	var packs []string
	packRows, err := tx.QueryContext(ctx, `SELECT pack_id FROM active_research_packs WHERE owner_work_id=?`, workID)
	if err != nil {
		return projectionDeleteFailure("active_research_packs", err)
	}
	for packRows.Next() {
		var pack string
		if err := packRows.Scan(&pack); err != nil {
			packRows.Close()
			return projectionDeleteFailure("active_research_packs", err)
		}
		packs = append(packs, pack)
	}
	if err := packRows.Err(); err != nil {
		packRows.Close()
		return projectionDeleteFailure("active_research_packs", err)
	}
	packRows.Close()
	for _, pack := range packs {
		for _, table := range []string{"active_research_consumers", "active_research_finding_sources", "active_research_sources", "active_research_findings", "active_research_revisions", "active_research_packs"} {
			if _, err := tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE pack_id=?`, pack); err != nil { //nolint:gosec // table comes only from the closed active-research projection list above and the pack ID stays parameter-bound.
				return projectionDeleteFailure(table, err)
			}
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM work_items WHERE id=?`, workID); err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot remove the work item", false, "reconcile remaining foreign-key owners", err)
	}
	return nil
}

func projectionDeleteFailure(table string, err error) error {
	return wrapFailure(KindUnavailable, "fold_event", fmt.Sprintf("cannot remove %s projection", table), false, "reconcile the projection before removal", err)
}

// ReadWorkRemovalAudit returns the durable receipt without exposing a live
// WorkItem or a resume action.
func (s *Store) ReadWorkRemovalAudit(ctx context.Context, workID string) (WorkRemovalReceipt, error) {
	if s == nil || s.db == nil {
		return WorkRemovalReceipt{}, newFailure(KindUnavailable, "work_removal_audit", "store is not open", false, "open the authority database")
	}
	return readWorkRemovalAuditQ(ctx, s.db, "work_id", workID)
}

func (s *Store) readWorkRemovalAuditByOperation(ctx context.Context, operationID string) (WorkRemovalReceipt, error) {
	if s == nil || s.db == nil {
		return WorkRemovalReceipt{}, newFailure(KindUnavailable, "work_removal_audit", "store is not open", false, "open the authority database")
	}
	return readWorkRemovalAuditQ(ctx, s.db, "operation_id", operationID)
}

func readWorkRemovalAuditQ(ctx context.Context, q queryer, column, identity string) (WorkRemovalReceipt, error) {
	var receipt WorkRemovalReceipt
	var handoff, linearRaw string
	// column is selected only from the two fixed call sites above.
	err := q.QueryRowContext(ctx, `SELECT operation_id,idempotency_key,work_id,expected_version,reason,actor,product_id,linear_confirmation_json,handoff_json,handoff_digest,state,coalesce(event_id,'') FROM work_removal_operations WHERE `+column+`=?`, identity).Scan(&receipt.OperationID, &receipt.IdempotencyKey, &receipt.WorkID, &receipt.ExpectedVersion, &receipt.Reason, &receipt.Actor, &receipt.ProductID, &linearRaw, &handoff, &receipt.HandoffDigest, &receipt.State, &receipt.EventID)
	if err == sql.ErrNoRows {
		return WorkRemovalReceipt{}, newFailure(KindProjectionNotFound, "work_removal_audit", "removal receipt does not exist", false, "supply a removed work identity")
	}
	if err != nil {
		return WorkRemovalReceipt{}, wrapFailure(KindUnavailable, "work_removal_audit", "cannot read removal receipt", true, "retry once the receipt is readable", err)
	}
	if err := json.Unmarshal([]byte(handoff), &receipt.Handoff); err != nil {
		return WorkRemovalReceipt{}, newFailure(KindInvalidPayload, "work_removal_audit", "removal receipt handoff is malformed", false, "repair the durable receipt")
	}
	if linearRaw != "{}" {
		receipt.Linear = &LinearHandoffConfirmation{}
		if err := json.Unmarshal([]byte(linearRaw), receipt.Linear); err != nil {
			return WorkRemovalReceipt{}, newFailure(KindInvalidPayload, "work_removal_audit", "removal receipt Linear confirmation is malformed", false, "repair the durable receipt")
		}
	}
	return receipt, nil
}

func refuseRemovedWorkTx(ctx context.Context, q queryer, workID, operation string) error {
	var state string
	err := q.QueryRowContext(ctx, `SELECT state FROM work_removal_operations WHERE work_id=? ORDER BY CASE state WHEN 'committed' THEN 0 ELSE 1 END LIMIT 1`, workID).Scan(&state)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		// Older stores can execute operations before the additive removal
		// migration has run. They cannot contain a removal receipt, so the
		// fencing check is a no-op until the table exists.
		if strings.Contains(err.Error(), "no such table: work_removal_operations") {
			return nil
		}
		return wrapFailure(KindUnavailable, operation, "cannot inspect removed work identity", true, "retry once the removal receipt is readable", err)
	}
	if state == "committed" {
		return newFailure(KindProjectionNotFound, operation, "removed work identity is audit-only and cannot be resumed", false, "start fresh work from the surviving planning record")
	}
	return newFailure(KindIdempotencyConflict, operation, "work identity is fenced by a pending removal operation", true, "resume the prepared removal operation or reconcile it before retrying")
}

// WorkLiveness is read-time evidence. It is never persisted and never grants
// removal permission.
type WorkLiveness struct {
	WorkID       string   `json:"work_id"`
	State        string   `json:"state"`
	Evidence     []string `json:"evidence"`
	Attempts     int      `json:"attempts"`
	OpenWaits    int      `json:"open_waits"`
	LastProgress string   `json:"last_progress,omitempty"`
}

func DeriveWorkLiveness(ctx context.Context, s *Store, workID string) (WorkLiveness, error) {
	if s == nil || s.db == nil {
		return WorkLiveness{}, newFailure(KindUnavailable, "work_liveness", "store is not open", false, "open the authority database")
	}
	var exists int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM work_items WHERE id=?`, workID).Scan(&exists); err != nil {
		return WorkLiveness{}, err
	}
	if exists == 0 {
		return WorkLiveness{}, newFailure(KindProjectionNotFound, "work_liveness", "work item does not exist", false, "supply an existing work item")
	}
	return deriveWorkLivenessQ(ctx, s.db, workID)
}

func (s *Store) ReadWorkLiveness(ctx context.Context, workID string) (WorkLiveness, error) {
	return DeriveWorkLiveness(ctx, s, workID)
}

func deriveWorkLivenessQ(ctx context.Context, q queryer, workID string) (WorkLiveness, error) {
	var attempts, openWaits, unboundedWaits, dispatched, failed, decisions int64
	if err := q.QueryRowContext(ctx, `SELECT count(*) FROM worker_attempts WHERE work_id=?`, workID).Scan(&attempts); err != nil {
		return WorkLiveness{}, wrapFailure(KindUnavailable, "work_liveness", "cannot read worker attempts", true, "retry once the worker projection is readable", err)
	}
	if err := q.QueryRowContext(ctx, `SELECT count(*) FROM worker_attempts WHERE work_id=? AND lifecycle_state='dispatched'`, workID).Scan(&dispatched); err != nil {
		return WorkLiveness{}, err
	}
	if err := q.QueryRowContext(ctx, `SELECT count(*) FROM worker_attempts WHERE work_id=? AND lifecycle_state='failed'`, workID).Scan(&failed); err != nil {
		return WorkLiveness{}, err
	}
	if err := q.QueryRowContext(ctx, `SELECT count(*) FROM workflow_external_conditions WHERE work_id=? AND condition_state='open' AND expected_within_seconds IS NOT NULL`, workID).Scan(&openWaits); err != nil {
		return WorkLiveness{}, err
	}
	if err := q.QueryRowContext(ctx, `SELECT count(*) FROM workflow_external_conditions WHERE work_id=? AND condition_state='open' AND expected_within_seconds IS NULL`, workID).Scan(&unboundedWaits); err != nil {
		return WorkLiveness{}, err
	}
	if err := q.QueryRowContext(ctx, `SELECT count(*) FROM workflow_decision_records WHERE work_id=?`, workID).Scan(&decisions); err != nil {
		return WorkLiveness{}, err
	}
	var latest sql.NullString
	if err := q.QueryRowContext(ctx, `SELECT max(occurred_at) FROM domain_events WHERE subject_type='work_item' AND subject_id=? AND kind NOT IN ('work.message_sent','work.message_withdrawn','workflow.overlap_resolved')`, workID).Scan(&latest); err != nil {
		return WorkLiveness{}, err
	}
	return workLivenessFromCounts(workID, attempts, dispatched, failed, openWaits, unboundedWaits, decisions, latest), nil
}

func workLivenessFromCounts(workID string, attempts, dispatched, failed, openWaits, unboundedWaits, decisions int64, latest sql.NullString) WorkLiveness {
	out := WorkLiveness{WorkID: workID, Attempts: int(attempts), OpenWaits: int(openWaits)}
	if latest.Valid {
		out.LastProgress = latest.String
	}
	switch {
	case dispatched > 0:
		out.State = "unknown"
		out.Evidence = []string{fmt.Sprintf("%d dispatched worker attempt(s) have unknown host state", dispatched)}
	case openWaits > 0:
		out.State = "waiting"
		out.Evidence = []string{fmt.Sprintf("%d bounded external wait(s)", openWaits)}
	case unboundedWaits > 0:
		out.State = "unknown"
		out.Evidence = []string{fmt.Sprintf("%d external wait(s) have no declared bound", unboundedWaits)}
	case failed > 0:
		out.State = "needs_attention"
		out.Evidence = []string{fmt.Sprintf("%d failed worker attempt(s)", failed)}
	default:
		out.State = "unknown"
		out.Evidence = []string{"host liveness has no verified active or waiting evidence"}
	}
	if unboundedWaits > 0 && openWaits > 0 {
		out.Evidence = append(out.Evidence, fmt.Sprintf("%d external wait(s) have no declared bound", unboundedWaits))
	}
	if decisions > 0 {
		out.Evidence = append(out.Evidence, fmt.Sprintf("%d operator decision(s)", decisions))
	}
	return out
}

func workLivenessPtrFromCounts(workID string, attempts, dispatched, failed, openWaits, unboundedWaits, decisions int64, latest sql.NullString) *WorkLiveness {
	value := workLivenessFromCounts(workID, attempts, dispatched, failed, openWaits, unboundedWaits, decisions, latest)
	return &value
}

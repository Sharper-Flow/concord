package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
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
				field := "handoff." + strings.ReplaceAll(name, " ", "_")
				return newFailure(KindInvalidPayload, "work_removal", field+" contains an empty or oversized entry", false, "supply bounded non-empty entries in "+field)
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
		return newFailure(KindInvalidPayload, "work_removal", "operation_id, idempotency_key, and work_id identities must be bounded", false, "supply operation_id, idempotency_key, and work_id from 2 to 128 characters")
	}
	if req.ExpectedVersion < 1 || strings.TrimSpace(req.Actor) == "" {
		return newFailure(KindInvalidOperation, "work_removal", "positive expected version and actor are required", false, "pin the current work version and operator")
	}
	if req.Reason != "shelved" && req.Reason != "cancelled" {
		return newFailure(KindInvalidOperation, "work_removal", "removal reason is not recognized", false, "use shelved or cancelled")
	}
	if !req.ExecutionRelinquished || !req.WritesReconciled || !req.EffectsReconciled || !req.DependenciesResolved || !req.ArtifactsVerified {
		return newFailure(KindInvalidPayload, "work_removal", "removal safety evidence is incomplete: execution_relinquished, writes_reconciled, effects_reconciled, dependencies_resolved, and artifacts_verified must be true", false, "supply true for execution_relinquished, writes_reconciled, effects_reconciled, dependencies_resolved, and artifacts_verified after reconciliation")
	}
	if err := validateRemovalHandoff(req.Handoff); err != nil {
		return err
	}
	return nil
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
		return replayPreparedWorkRemovalTx(ctx, tx, req, state, digest, linearJSON)
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
		return WorkRemovalReceipt{}, newFailure(KindInvalidPayload, "work_removal_prepare", "Linear confirmation does not match the handoff: check linear.handoff_digest", false, "supply the exact linear.handoff_digest for the handoff")
	}
	handoffJSON, err := json.Marshal(req.Handoff)
	if err != nil {
		return WorkRemovalReceipt{}, newFailure(KindInvalidOperation, "work_removal_prepare", "handoff cannot be encoded", false, "supply a JSON-encodable handoff")
	}
	if err := enterWrite(ctx, tx); err != nil {
		return WorkRemovalReceipt{}, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `INSERT INTO work_removal_operations(operation_id,idempotency_key,work_id,expected_version,reason,actor,product_id,linear_confirmation_json,handoff_json,handoff_digest,state,event_id,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,'prepared','',?,?)`, req.OperationID, req.IdempotencyKey, req.WorkID, req.ExpectedVersion, req.Reason, req.Actor, req.ProductID, linearJSON, string(handoffJSON), digest, now, now); err != nil {
		return WorkRemovalReceipt{}, wrapFailure(KindUnavailable, "work_removal_prepare", "cannot persist removal operation", true, "retry the same idempotency key", err)
	}
	if err := leaveWrite(ctx, tx); err != nil {
		return WorkRemovalReceipt{}, err
	}
	return WorkRemovalReceipt{OperationID: req.OperationID, IdempotencyKey: req.IdempotencyKey, WorkID: req.WorkID, ExpectedVersion: req.ExpectedVersion, Reason: req.Reason, Actor: req.Actor, ProductID: req.ProductID, Linear: req.Linear, Handoff: req.Handoff, HandoffDigest: digest, State: "prepared"}, nil
}

func replayPreparedWorkRemovalTx(ctx context.Context, tx *sql.Tx, req WorkRemovalRequest, state, digest, linearJSON string) (WorkRemovalReceipt, error) {
	stored, readErr := readWorkRemovalAuditQ(ctx, tx, req.OperationID)
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

func validateRemovalDestinationQ(ctx context.Context, q queryer, req WorkRemovalRequest) error {
	// A work item that records a Linear issue
	// identity hands its preservation to that issue. The agent confirms the
	// handoff through the Linear MCP server; the store checks the
	// confirmation against the recorded identity and makes no Linear call.
	// Linear is the only planning authority, so a removal
	// without a recorded issue identity and confirmation has no planning
	// destination and refuses.
	link, recorded, err := readLinearLinkCore(ctx, q, req.WorkID)
	if err != nil {
		return err
	}
	if req.Linear == nil {
		if recorded {
			return newFailure(KindInvalidPayload, "work_removal_prepare", "the work item records a Linear issue but the removal carries no Linear handoff confirmation in linear", false, "confirm the handoff on the recorded Linear issue and supply linear before removal")
		}
		return newFailure(KindInvalidPayload, "work_removal_prepare", "the work item has no recorded Linear issue identity and the removal carries no Linear handoff confirmation in linear", false, "create or adopt the Linear issue through the planning authority, record its identity, and supply linear confirming the handoff before removal")
	}
	if !recorded {
		return newFailure(KindInvalidRelation, "work_removal_prepare", "the work item has no recorded Linear issue identity", false, "record the issue identity before confirming its handoff")
	}
	products, productErr := productsForWorkIDs(ctx, q, []string{req.WorkID})
	if productErr != nil {
		return wrapFailure(KindUnavailable, "work_removal_prepare", "cannot read the work item's Product destinations", true, "retry once the database is readable", productErr)
	}
	if req.ProductID == "" {
		return newAmbiguousScopeFailure("work_removal_prepare", "Linear confirmation has no Product destination", "supply the authorized Product", products[req.WorkID])
	}
	if !slices.Contains(products[req.WorkID], req.ProductID) {
		return newFailure(KindInvalidRelation, "work_removal_prepare", "Linear confirmation names a Product outside the work item's membership", false, "confirm a Product that contains the work item")
	}
	if req.Linear.ProductID != req.ProductID || req.Linear.Destination != "linear" || req.Linear.RemoteIssueUUID == "" {
		return newFailure(KindInvalidRelation, "work_removal_prepare", "Linear confirmation does not name the authorized destination", false, "confirm the exact Product and remote issue")
	}
	if link.RemoteIssueUUID != req.Linear.RemoteIssueUUID {
		return newFailure(KindInvalidRelation, "work_removal_prepare", "Linear confirmation does not match the recorded issue identity", false, "confirm the handoff on the recorded Linear issue")
	}
	return nil
}

func validateRemovalGatesQ(ctx context.Context, q queryer, req WorkRemovalRequest) error {
	var lifecycle string
	if err := q.QueryRowContext(ctx, `SELECT lifecycle FROM work_items WHERE id=?`, req.WorkID).Scan(&lifecycle); err == sql.ErrNoRows {
		return newFailure(KindProjectionNotFound, "work_removal", "work item does not exist", false, "supply the current work item")
	} else if err != nil {
		return wrapFailure(KindUnavailable, "work_removal", "cannot read work item", true, "retry once the database is readable", err)
	}
	if lifecycle == "completed" {
		return newFailure(KindInvalidOperation, "work_removal", "completed work cannot be removed by this operation", false, "retain terminal work or use a separately accepted retention operation")
	}
	var got int64
	if err := q.QueryRowContext(ctx, `SELECT version FROM work_items WHERE id=?`, req.WorkID).Scan(&got); err != nil {
		return err
	}
	if got != req.ExpectedVersion {
		conflict, conflictErr := versionConflictForQuery(ctx, q, req.WorkID, req.ExpectedVersion, got)
		if conflictErr != nil {
			return conflictErr
		}
		return conflict
	}
	checks := []struct{ query, detail string }{
		{`SELECT count(*) FROM relations WHERE work_id_from=? OR work_id_to=?`, "work has unresolved relation dependencies"},
		{`SELECT count(*) FROM workflow_impact_edges WHERE work_id=? OR target_work_id=?`, "work has unresolved workflow impact dependencies"},
	}
	for _, check := range checks {
		args := []any{req.WorkID}
		if strings.Count(check.query, "?") > 1 {
			args = []any{req.WorkID, req.WorkID}
		}
		var count int
		if err := q.QueryRowContext(ctx, check.query, args...).Scan(&count); err != nil {
			return wrapFailure(KindUnavailable, "work_removal", "cannot evaluate removal gate", true, "retry once the operational projections are readable", err)
		}
		if count > 0 {
			return newFailure(KindNotTerminal, "work_removal", check.detail, false, "reconcile the named execution projection before removal")
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
	if _, err := tx.ExecContext(ctx, `DELETE FROM durable_operations WHERE work_id=?`, event.SubjectID); err != nil {
		return projectionDeleteFailure("durable_operations", err)
	}
	result, err := tx.ExecContext(ctx, `UPDATE work_removal_operations SET state='committed',event_id=?,updated_at=? WHERE operation_id=? AND work_id=?`, event.EventID, event.OccurredAt.UTC().Format(time.RFC3339Nano), payload.OperationID, event.SubjectID)
	if err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot close removal receipt", true, "retry once the database is writable", err)
	}
	if n, _ := result.RowsAffected(); n == 0 {
		handoff, _ := json.Marshal(payload.Handoff)
		linearJSON, _ := linearConfirmationJSON(payload.Linear)
		if _, err := tx.ExecContext(ctx, `INSERT INTO work_removal_operations(operation_id,idempotency_key,work_id,expected_version,reason,actor,product_id,linear_confirmation_json,handoff_json,handoff_digest,state,event_id,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,'committed',?,?,?)`, payload.OperationID, payload.IdempotencyKey, event.SubjectID, payload.ExpectedVersion, payload.Reason, payload.Actor, payload.ProductID, linearJSON, string(handoff), payload.HandoffDigest, event.EventID, event.OccurredAt.UTC().Format(time.RFC3339Nano), event.OccurredAt.UTC().Format(time.RFC3339Nano)); err != nil {
			return wrapFailure(KindUnavailable, "fold_event", "cannot preserve removal receipt", true, "restore a verified backup into a new path with concord restore, then swap", err)
		}
	}
	return nil
}

// workRemovalProjectionTables names, in foreign-key-graph order, every
// projection table whose work-item rows deleteWorkOwnedProjections deletes.
// Table names are closed source-owned identifiers, not caller input. Tables
// whose work-item owner columns are not a bare work_id appear in
// workRemovalProjectionOwnerColumns, and deleteWorkProjectionTx builds their
// delete predicate from that map. TestWorkRemovalClearsEveryForeignKeyIntoWorkItems
// requires every entry to exist in the live schema and requires every live
// foreign-key owner of work_items that is not ON DELETE CASCADE to be cleared
// through this list or that map, so a new uncleared owner fails the suite
// instead of failing removal at runtime.
var workRemovalProjectionTables = []string{"workflow_overlap_resolutions", "workflow_backlog_alignment", "workflow_contract_law_modifications", "workflow_contract_verification_obligations", "workflow_contract_law_additions", "workflow_contract_domain_relation_modifications", "workflow_contract_domain_modifications", "workflow_contract_affected_domains", "workflow_law_addition_reservations", "workflow_architecture_bindings", "workflow_premise_confirmations", "workflow_impact_edges", "workflow_checkpoints", "workflow_decision_records", "workflow_native_runs", "workflow_contracts", "workflow_design_records", "workflow_proposal_records", "workflow_clarify_records", "workflow_removal_scopes", "workflow_plan_records", "workflow_instances", "project_handoffs", "external_observations", "relations", "work_projects", "linear_issue_links"}

// workRemovalProjectionOwnerColumns maps each projection table whose
// work-item owner columns are not a bare work_id to those exact columns.
// deleteWorkProjectionTx deletes rows matching ANY listed column, so a row
// naming the removed work at either end clears: an alignment row names the
// searching item and, for a related_found outcome, one item the search found;
// an overlap resolution, an impact edge, and a relation each name two ends.
// Every key must also appear in workRemovalProjectionTables: only that list
// drives the deletes, so an entry missing from it builds a predicate that
// never runs. TestWorkRemovalClearsEveryForeignKeyIntoWorkItems binds the
// live non-CASCADE foreign-key owners of work_items to this map and enforces
// the list membership, so a stale, split, or never-executed entry fails the
// suite.
var workRemovalProjectionOwnerColumns = map[string][]string{
	"workflow_overlap_resolutions": {"from_work_id", "to_work_id"},
	"workflow_backlog_alignment":   {"work_id", "related_work_id"},
	"workflow_impact_edges":        {"work_id", "target_work_id"},
	"relations":                    {"work_id_from", "work_id_to"},
}

func deleteWorkOwnedProjections(ctx context.Context, tx migrationRunner, workID string) error {
	// This order follows the FK graph. Shared Product, Project, actor, and law
	// rows have no work-item ownership predicate and remain untouched.
	for _, table := range workRemovalProjectionTables {
		if err := deleteWorkProjectionTx(ctx, tx, table, workID); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM work_items WHERE id=?`, workID); err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot remove the work item", false, "reconcile remaining foreign-key owners", err)
	}
	return nil
}

func deleteWorkProjectionTx(ctx context.Context, tx migrationRunner, table, workID string) error {
	if columns, special := workRemovalProjectionOwnerColumns[table]; special {
		// The table and column names come only from the closed declarations
		// above; the work ID stays parameter-bound.
		predicates := make([]string, len(columns))
		args := make([]any, len(columns))
		for i, column := range columns {
			predicates[i] = column + "=?"
			args[i] = workID
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE `+strings.Join(predicates, " OR "), args...); err != nil {
			return projectionDeleteFailure(table, err)
		}
		return nil
	}
	_, deleteErr := tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE work_id=?`, workID) //nolint:gosec // table comes only from the closed projection list in workRemovalProjectionTables and the work ID stays parameter-bound.
	if deleteErr == nil {
		return nil
	}
	// Migration 182 runs this cleanup before the plan and removal tables in
	// migration 183 and the clarification table in migration 189 can exist.
	if (table == "workflow_clarify_records" || table == "workflow_plan_records" || table == "workflow_removal_scopes") && strings.Contains(deleteErr.Error(), "no such table: "+table) {
		return nil
	}
	// workflow_law_addition_reservations carries no direct foreign key into
	// work_items (its owner references workflow_contracts), so it stays out of
	// workRemovalProjectionOwnerColumns and clears by its own owner column.
	if table == "workflow_law_addition_reservations" {
		if _, err := tx.ExecContext(ctx, `DELETE FROM workflow_law_addition_reservations WHERE owner_work_id=?`, workID); err != nil {
			return projectionDeleteFailure(table, err)
		}
		return nil
	}
	return projectionDeleteFailure(table, deleteErr)
}

func projectionDeleteFailure(table string, err error) error {
	return wrapFailure(KindUnavailable, "fold_event", fmt.Sprintf("cannot remove %s projection", table), false, "reconcile the projection before removal", err)
}

func readWorkRemovalAuditQ(ctx context.Context, q queryer, operationID string) (WorkRemovalReceipt, error) {
	var receipt WorkRemovalReceipt
	var handoff, linearRaw string
	err := q.QueryRowContext(ctx, `SELECT operation_id,idempotency_key,work_id,expected_version,reason,actor,product_id,linear_confirmation_json,handoff_json,handoff_digest,state,coalesce(event_id,'') FROM work_removal_operations WHERE operation_id=?`, operationID).Scan(&receipt.OperationID, &receipt.IdempotencyKey, &receipt.WorkID, &receipt.ExpectedVersion, &receipt.Reason, &receipt.Actor, &receipt.ProductID, &linearRaw, &handoff, &receipt.HandoffDigest, &receipt.State, &receipt.EventID)
	if err == sql.ErrNoRows {
		return WorkRemovalReceipt{}, newFailure(KindProjectionNotFound, "work_removal_audit", "removal receipt does not exist", false, "supply a removed work identity")
	}
	if err != nil {
		return WorkRemovalReceipt{}, wrapFailure(KindUnavailable, "work_removal_audit", "cannot read removal receipt", true, "retry once the receipt is readable", err)
	}
	if err := json.Unmarshal([]byte(handoff), &receipt.Handoff); err != nil {
		return WorkRemovalReceipt{}, newFailure(KindInvariantViolation, "work_removal_audit", "removal receipt handoff is malformed", false, "repair the durable receipt")
	}
	if linearRaw != "{}" {
		receipt.Linear = &LinearHandoffConfirmation{}
		if err := json.Unmarshal([]byte(linearRaw), receipt.Linear); err != nil {
			return WorkRemovalReceipt{}, newFailure(KindInvariantViolation, "work_removal_audit", "removal receipt Linear confirmation is malformed", false, "repair the durable receipt")
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
	LastProgress string   `json:"last_progress,omitempty"`
}

func deriveWorkLivenessQ(ctx context.Context, q queryer, workID string) (WorkLiveness, error) {
	var decisions int64
	if err := q.QueryRowContext(ctx, `SELECT count(*) FROM workflow_decision_records WHERE work_id=?`, workID).Scan(&decisions); err != nil {
		return WorkLiveness{}, err
	}
	var latest sql.NullString
	if err := q.QueryRowContext(ctx, `SELECT max(NULLIF(last_progress_at,'')) FROM work_items WHERE id=?`, workID).Scan(&latest); err != nil {
		return WorkLiveness{}, err
	}
	return workLivenessFromCounts(workID, decisions, latest), nil
}

func workLivenessFromCounts(workID string, decisions int64, latest sql.NullString) WorkLiveness {
	out := WorkLiveness{WorkID: workID}
	if latest.Valid {
		out.LastProgress = latest.String
	}
	out.State = "unknown"
	out.Evidence = []string{"host liveness has no verified active or waiting evidence"}
	if decisions > 0 {
		out.Evidence = append(out.Evidence, fmt.Sprintf("%d operator decision(s)", decisions))
	}
	return out
}

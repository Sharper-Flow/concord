package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
)

type workflowReplayContextKey struct{}

// A replay folds events serially in one transaction. The running identity
// count therefore matches the event-log order without a query for each edge.
type workflowReplayState struct {
	relationIdentity int64
}

func workflowReplayContext(ctx context.Context) context.Context {
	return context.WithValue(ctx, workflowReplayContextKey{}, &workflowReplayState{})
}

func isWorkflowReplay(ctx context.Context) bool {
	_, ok := ctx.Value(workflowReplayContextKey{}).(*workflowReplayState)
	return ok
}

func advanceWorkflowReplay(ctx context.Context, event Event) {
	state, ok := ctx.Value(workflowReplayContextKey{}).(*workflowReplayState)
	if !ok {
		return
	}
	for _, kind := range relationIdentityEventKinds {
		if event.Kind == kind {
			state.relationIdentity++
			return
		}
	}
}

func workflowReplayRelationIdentity(ctx context.Context) (int64, bool) {
	state, ok := ctx.Value(workflowReplayContextKey{}).(*workflowReplayState)
	if !ok {
		return 0, false
	}
	return state.relationIdentity, true
}

// WorkflowLawRevision is the immutable law proof captured when a workflow
// contract is approved. The Git commit is deliberately absent: it is audit
// context, not revision identity (CD-0036 D1).
type WorkflowLawRevision struct {
	LawID       string `json:"law_id"`
	ContentHash string `json:"content_hash"`
}

// StaleLawRevision is the structured recovery diagnosis for a consumer whose
// mandated law ID has been superseded. Same-ID content amendments do not
// produce this value.
type StaleLawRevision struct {
	OldLawID                     string   `json:"old_law_id"`
	OldContentHash               string   `json:"old_content_hash"`
	AcceptedSuccessorLawID       string   `json:"accepted_successor_law_id"`
	AcceptedSuccessorContentHash string   `json:"accepted_successor_content_hash"`
	RecoveryActions              []string `json:"recovery_actions"`
}

const staleLawRecoveryActions = "supersede_contract,terminal_work"

func workflowContractRecoveryActionDefinition() WorkflowActionDefinition {
	return WorkflowActionDefinition{
		ID: "supersede_contract", Consequence: ActionInternalSQLite, Approval: ActionApprovalRequired, ExecutionMode: ActionAdvance,
		// The recovery payload contains a registered outcome predicate object;
		// semantic validation below owns this closed object rather than lying
		// about it as one of the scalar payload field types.
		Payload: WorkflowPayloadDefinition{Fields: []WorkflowPayloadField{}},
	}
}

func validateWorkflowLawRevisions(mandated []string, revisions []WorkflowLawRevision) error {
	if len(mandated) > 32 || len(revisions) > 32 {
		return newFailure(KindInvalidPayload, "validate_workflow_law_revisions", "workflow law revision pins exceed the bounded list size", false, "supply at most 32 mandated law revisions")
	}
	mandate := make(map[string]struct{}, len(mandated))
	for _, lawID := range mandated {
		if lawID == "" {
			return newFailure(KindInvalidPayload, "validate_workflow_law_revisions", "workflow law mandate contains an empty ID", false, "supply bounded law IDs")
		}
		if _, exists := mandate[lawID]; exists {
			return newFailure(KindInvalidPayload, "validate_workflow_law_revisions", "workflow law mandate contains a duplicate ID", false, "supply unique law IDs")
		}
		mandate[lawID] = struct{}{}
	}
	seen := make(map[string]struct{}, len(revisions))
	for _, revision := range revisions {
		if revision.LawID == "" || revision.LawID != strings.TrimSpace(revision.LawID) {
			return newFailure(KindInvalidPayload, "validate_workflow_law_revisions", "workflow law revision has an invalid law ID", false, "supply one pin for each mandated law ID")
		}
		if _, exists := mandate[revision.LawID]; !exists {
			return newFailure(KindInvalidPayload, "validate_workflow_law_revisions", "workflow law revision is not in spec_mandate", false, "supply pins matching spec_mandate exactly")
		}
		if _, exists := seen[revision.LawID]; exists {
			return newFailure(KindInvalidPayload, "validate_workflow_law_revisions", "workflow law revisions contain a duplicate law ID", false, "supply one pin for each mandated law ID")
		}
		if err := validateContentHash(revision.ContentHash); err != nil {
			return err
		}
		seen[revision.LawID] = struct{}{}
	}
	if len(revisions) != len(mandated) {
		return newFailure(KindInvalidPayload, "validate_workflow_law_revisions", "workflow law revision pins do not correspond one-to-one with spec_mandate", false, "capture one current Git law hash for every mandated law ID")
	}
	return nil
}

func deriveWorkflowLawRevisionsTx(ctx context.Context, tx *sql.Tx, workID string, mandated, modified []string) ([]WorkflowLawRevision, error) {
	if err := validateLawModificationSubset(mandated, modified); err != nil {
		return nil, err
	}
	if len(mandated) == 0 {
		return []WorkflowLawRevision{}, nil
	}
	homeProjectID, homeLocatorID, err := workflowLawHome(ctx, tx, workID)
	if err != nil {
		return nil, err
	}
	if _, err := checkMandatedLawsTxAtHome(ctx, tx, homeProjectID, homeLocatorID, mandated, modified, true); err != nil {
		return nil, err
	}
	revisions := make([]WorkflowLawRevision, 0, len(mandated))
	for _, lawID := range mandated {
		var hash string
		if err := tx.QueryRowContext(ctx, `SELECT content_hash FROM law_subjects WHERE home_project_id=? AND home_locator_id=? AND law_id=? AND status='accepted'`, homeProjectID, homeLocatorID, lawID).Scan(&hash); err != nil {
			if err == sql.ErrNoRows {
				return nil, newFailure(KindProjectionNotFound, "derive_workflow_law_revisions", "mandated law is not currently accepted", false, "publish and rebuild the accepted Git law projection")
			}
			return nil, wrapFailure(KindUnavailable, "derive_workflow_law_revisions", "cannot read the accepted law content hash", true, "retry once the law projection is readable", err)
		}
		revisions = append(revisions, WorkflowLawRevision{LawID: lawID, ContentHash: hash})
	}
	return revisions, nil
}

func validateCurrentWorkflowLawRevisionsTx(ctx context.Context, tx *sql.Tx, workID string, mandated, modified []string, supplied []WorkflowLawRevision) error {
	expected, err := deriveWorkflowLawRevisionsTx(ctx, tx, workID, mandated, modified)
	if err != nil {
		return err
	}
	if len(expected) != len(supplied) {
		return newFailure(KindInvalidPayload, "validate_workflow_law_revisions", "successor contract law pins do not match the current accepted law projection", false, "pin every mandated successor law to its current content hash")
	}
	for index := range expected {
		if expected[index] != supplied[index] {
			return newFailure(KindInvalidPayload, "validate_workflow_law_revisions", "successor contract law pin does not match the current accepted law projection", false, "pin the accepted successor law revision")
		}
	}
	return nil
}

// validateStaleWorkflowContractRecoverySuccessorTx prevents a recovery from
// escaping a cutover by dropping the accepted successor law from its mandate.
// It is deliberately a live-transaction check: event replay validates recorded
// pin shape without consulting today's Git-derived law projection.
func validateStaleWorkflowContractRecoverySuccessorTx(ctx context.Context, tx *sql.Tx, workID string, previousContractVersion int64, successor []WorkflowLawRevision) error {
	var mandateJSON string
	if err := tx.QueryRowContext(ctx, `SELECT spec_mandate FROM workflow_contracts WHERE work_id=? AND contract_version=? AND superseded_by IS NULL`, workID, previousContractVersion).Scan(&mandateJSON); err != nil {
		if err == sql.ErrNoRows {
			return newFailure(KindProjectionNotFound, "validate_stale_workflow_recovery", "previous active workflow contract is unavailable", false, "reload the current contract")
		}
		return wrapFailure(KindUnavailable, "validate_stale_workflow_recovery", "cannot read the stale workflow contract", true, "retry once the workflow projection is readable", err)
	}
	var mandated []string
	if err := json.Unmarshal([]byte(mandateJSON), &mandated); err != nil {
		return newFailure(KindInvariantViolation, "validate_stale_workflow_recovery", "previous workflow contract law mandate is malformed", false, "rebuild projections from the event log")
	}
	var mandateErr error
	mandated, mandateErr = currentWorkflowLawMandateFromProjection(ctx, tx, workID, previousContractVersion, mandated)
	if mandateErr != nil {
		return mandateErr
	}
	homeProjectID, homeLocatorID, err := workflowLawHome(ctx, tx, workID)
	if err != nil {
		return err
	}
	stale, err := findStaleWorkflowLawRevision(ctx, tx, homeProjectID, homeLocatorID, workID, previousContractVersion, mandated)
	if err != nil || stale == nil {
		return err
	}
	for _, revision := range successor {
		if revision.LawID == stale.AcceptedSuccessorLawID && revision.ContentHash == stale.AcceptedSuccessorContentHash {
			return nil
		}
	}
	return newFailure(KindInvalidPayload, "validate_stale_workflow_recovery", "successor contract must pin the accepted successor law revision", false, "include the accepted successor law and current content hash in spec_mandate")
}

// findStaleWorkflowLawRevision is read-only and accepts either *sql.DB or
// *sql.Tx. It consults only the current Git-derived law projection and the
// event-folded contract pins; it never changes either authority.
func findStaleWorkflowLawRevision(ctx context.Context, q queryer, homeProjectID, homeLocatorID, workID string, contractVersion int64, mandated []string) (*StaleLawRevision, error) {
	if len(mandated) == 0 {
		return nil, nil
	}
	if err := validateWorkflowLawMandate(mandated); err != nil {
		return nil, err
	}
	for _, lawID := range mandated {
		var pinnedHash string
		err := q.QueryRowContext(ctx, `SELECT content_hash FROM workflow_contract_law_revisions WHERE work_id=? AND contract_version=? AND law_id=?`, workID, contractVersion, lawID).Scan(&pinnedHash)
		pinned := err == nil
		if err != nil && err != sql.ErrNoRows {
			return nil, wrapFailure(KindUnavailable, "check_workflow_law_revision", "cannot read workflow law revision pins", true, "retry once the workflow projection is readable", err)
		}
		var status, oldHash string
		err = q.QueryRowContext(ctx, `SELECT status,content_hash FROM law_subjects WHERE home_project_id=? AND home_locator_id=? AND law_id=?`, homeProjectID, homeLocatorID, lawID).Scan(&status, &oldHash)
		if err == sql.ErrNoRows {
			failure := newFailure(KindProjectionNotFound, "check_workflow_law_revision", "mandated law is missing from the current Git-derived projection", false, "rebuild the accepted Git law projection")
			failure.CandidateIDs = []string{lawID}
			return nil, failure
		}
		if err != nil {
			return nil, wrapFailure(KindUnavailable, "check_workflow_law_revision", "cannot read current law revision", true, "retry once the law projection is readable", err)
		}
		if status != "superseded" {
			// A changed hash under the same accepted law ID is a compatible
			// amendment, regardless of the pinned hash.
			continue
		}
		var successorID, successorHash string
		err = q.QueryRowContext(ctx, `SELECT s.law_id,s.content_hash FROM law_relations r JOIN law_subjects s ON s.home_project_id=r.home_project_id AND s.home_locator_id=r.home_locator_id AND s.law_id=r.source_law_id WHERE r.home_project_id=? AND r.home_locator_id=? AND r.kind='supersedes' AND r.target_law_id=? AND s.status='accepted' ORDER BY s.law_id LIMIT 1`, homeProjectID, homeLocatorID, lawID).Scan(&successorID, &successorHash)
		if err == sql.ErrNoRows {
			failure := newFailure(KindProjectionNotFound, "check_workflow_law_revision", "superseded mandated law has no valid accepted successor in the current projection", false, "publish and rebuild the accepted successor law projection")
			failure.CandidateIDs = []string{lawID}
			return nil, failure
		}
		if err != nil {
			return nil, wrapFailure(KindUnavailable, "check_workflow_law_revision", "cannot read accepted successor law revision", true, "retry once the law projection is readable", err)
		}
		if !pinned {
			pinnedHash = oldHash
		}
		return &StaleLawRevision{
			OldLawID:                     lawID,
			OldContentHash:               pinnedHash,
			AcceptedSuccessorLawID:       successorID,
			AcceptedSuccessorContentHash: successorHash,
			RecoveryActions:              strings.Split(staleLawRecoveryActions, ","),
		}, nil
	}
	return nil, nil
}

func validateWorkflowLawMandate(mandated []string) error {
	seen := map[string]struct{}{}
	for _, lawID := range mandated {
		if lawID == "" {
			return newFailure(KindInvalidPayload, "check_workflow_law_revision", "workflow law mandate contains an empty ID", false, "supply bounded law IDs")
		}
		if _, exists := seen[lawID]; exists {
			return newFailure(KindInvalidPayload, "check_workflow_law_revision", "workflow law mandate contains a duplicate ID", false, "supply unique law IDs")
		}
		seen[lawID] = struct{}{}
	}
	return nil
}

func readWorkflowLawRevisions(ctx context.Context, q queryer, workID string, contractVersion int64) ([]WorkflowLawRevision, error) {
	rows, err := q.QueryContext(ctx, `SELECT law_id,content_hash FROM workflow_contract_law_revisions WHERE work_id=? AND contract_version=? ORDER BY law_id`, workID, contractVersion)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "read_workflow_law_revision", "cannot read workflow law revision pins", true, "retry once the workflow projection is readable", err)
	}
	defer rows.Close()
	revisions := []WorkflowLawRevision{}
	for rows.Next() {
		var revision WorkflowLawRevision
		if err := rows.Scan(&revision.LawID, &revision.ContentHash); err != nil {
			return nil, wrapFailure(KindUnavailable, "read_workflow_law_revision", "cannot decode workflow law revision pin", true, "retry once the workflow projection is readable", err)
		}
		revisions = append(revisions, revision)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapFailure(KindUnavailable, "read_workflow_law_revision", "cannot enumerate workflow law revision pins", true, "retry once the workflow projection is readable", err)
	}
	return revisions, nil
}

func checkWorkflowLawRevisionStalenessTx(ctx context.Context, tx *sql.Tx, workID string) error {
	var contractVersion int64
	var mandateJSON string
	if err := tx.QueryRowContext(ctx, `SELECT contract_version,spec_mandate FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL ORDER BY contract_version DESC LIMIT 1`, workID).Scan(&contractVersion, &mandateJSON); err == sql.ErrNoRows {
		return nil
	} else if err != nil {
		return wrapFailure(KindUnavailable, "check_workflow_law_revision", "cannot read active workflow contract", true, "retry once the workflow projection is readable", err)
	}
	var mandated []string
	if err := json.Unmarshal([]byte(mandateJSON), &mandated); err != nil {
		return newFailure(KindInvariantViolation, "check_workflow_law_revision", "workflow contract law mandate is malformed", false, "rebuild projections from the event log")
	}
	var mandateErr error
	mandated, mandateErr = currentWorkflowLawMandateFromProjection(ctx, tx, workID, contractVersion, mandated)
	if mandateErr != nil {
		return mandateErr
	}
	if len(mandated) == 0 {
		return CheckWorkflowDomainOverlapTx(ctx, tx, workID)
	}
	homeProjectID, homeLocatorID, err := workflowLawHome(ctx, tx, workID)
	if err != nil {
		return err
	}
	stale, err := findStaleWorkflowLawRevision(ctx, tx, homeProjectID, homeLocatorID, workID, contractVersion, mandated)
	if err != nil {
		return err
	}
	if stale == nil {
		return CheckWorkflowDomainOverlapTx(ctx, tx, workID)
	}
	failure := newFailure(KindStaleLawRevision, "check_workflow_law_revision", "workflow contract consumes a superseded law revision", false, "request_approval")
	failure.StaleLawRevision = stale
	return failure
}

// CheckWorkflowConsequentialBoundaryTx is the CD-0041 D7 preflight for a
// caller-owned mutation transaction. It validates both halves the boundary
// owes — the contract's law revision pins and its active Domain overlaps — in
// the transaction that owns the write, so neither can change between the check
// and the effect. It is read-only and returns a typed refusal.
func CheckWorkflowConsequentialBoundaryTx(ctx context.Context, transaction *Transaction, workID string) error {
	tx, err := transactionSQL(transaction, "check_workflow_law_revision")
	if err != nil {
		return err
	}
	return checkWorkflowLawRevisionStalenessTx(ctx, tx, workID)
}

// checkWorkflowLawRevisionStalenessReadTx is the advisory-read form of the
// staleness boundary: the same single implementation as the mutation form, run
// under a short-lived read transaction the caller opens and rolls back. A
// non-transactional twin deliberately does not exist — a second
// implementation of the same boundary is how the overlap half of the check
// once went missing from the read path (issue #376).
func checkWorkflowLawRevisionStalenessReadTx(ctx context.Context, db *sql.DB, workID string) (err error) {
	tx, beginErr := db.BeginTx(ctx, nil)
	if beginErr != nil {
		return wrapFailure(KindUnavailable, "check_workflow_law_revision", "cannot open the read transaction", true, "retry once the store is readable", beginErr)
	}
	defer func() {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && err == nil {
			err = wrapFailure(KindUnavailable, "check_workflow_law_revision", "cannot close the read transaction", true, "retry once the store is readable", rollbackErr)
		}
	}()
	return checkWorkflowLawRevisionStalenessTx(ctx, tx, workID)
}

func workflowActionAllowsTerminalRecovery(request WorkflowActionPreflightRequest) bool {
	if request.ActionID != "complete" {
		return false
	}
	fields, err := workflowActionObject(request.Payload)
	if err != nil {
		return false
	}
	terminalState := workflowFieldStringDefault(fields, "terminal_state", "completed")
	return terminalState == "cancelled" || terminalState == "superseded"
}

func workflowExecutionAllowsStaleRecovery(actionID string, payload []byte) bool {
	if actionID != "complete" {
		return false
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(payload, &fields) != nil {
		return false
	}
	var terminalState string
	_ = json.Unmarshal(fields["terminal_state"], &terminalState)
	return terminalState == "cancelled" || terminalState == "superseded"
}

func validateWorkflowContractRecoveryPayload(raw json.RawMessage) error {
	fields, err := workflowActionObject(raw)
	if err != nil {
		return err
	}
	for _, name := range []string{"contract_version", "premise", "outcome_kind", "outcome_payload", "required_evidence", "route_conventions", "spec_mandate", "law_modifies", "rigor_class"} {
		if _, ok := fields[name]; !ok {
			return newFailure(KindInvalidPayload, "workflow_action", "stale-law recovery requires a fully supplied successor contract", false, "supply every successor contract field")
		}
	}
	if workflowFieldInt(fields, "contract_version", 0) <= 0 || workflowFieldStringDefault(fields, "premise", "") == "" || workflowFieldStringDefault(fields, "outcome_kind", "") == "" || workflowFieldStringDefault(fields, "rigor_class", "") == "" {
		return newFailure(KindInvalidPayload, "workflow_action", "stale-law recovery successor contract has an empty required field", false, "supply the complete successor contract")
	}
	for _, name := range []string{"required_evidence", "route_conventions", "spec_mandate", "law_modifies"} {
		var values []string
		if err := json.Unmarshal(workflowFieldRaw(fields, name), &values); err != nil || values == nil {
			return newFailure(KindInvalidPayload, "workflow_action", "stale-law recovery successor contract contains an invalid list", false, "supply JSON string lists for the successor contract")
		}
	}
	if rawOutcome := workflowFieldRaw(fields, "outcome_payload"); len(rawOutcome) == 0 || string(rawOutcome) == "null" {
		return newFailure(KindInvalidPayload, "workflow_action", "stale-law recovery successor contract requires an outcome predicate", false, "supply the successor outcome predicate")
	}
	return nil
}

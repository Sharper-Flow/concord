package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type workflowReplayContextKey struct{}

type workflowContractSupersessionContextKey struct{}

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

func workflowContractSupersessionContext(ctx context.Context) context.Context {
	return context.WithValue(ctx, workflowContractSupersessionContextKey{}, true)
}

func inWorkflowContractSupersessionContext(ctx context.Context) bool {
	value, _ := ctx.Value(workflowContractSupersessionContextKey{}).(bool)
	return value
}

func advanceWorkflowReplay(ctx context.Context, event Event) {
	state, ok := ctx.Value(workflowReplayContextKey{}).(*workflowReplayState)
	if !ok {
		return
	}
	if event.Kind == "work.created" {
		var payload workCreatedPayload
		if json.Unmarshal(event.Payload, &payload) == nil && payload.RaisedFromWorkID != "" {
			state.relationIdentity++
		}
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

// CompatibleLawAmendment reports a same-ID accepted law change that does not
// block the workflow, but needs a fresh read before consequential work.
type CompatibleLawAmendment struct {
	LawID       string `json:"law_id"`
	PinnedHash  string `json:"pinned_hash"`
	CurrentHash string `json:"current_hash"`
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
		Payload: WorkflowPayloadDefinition{Closed: true, Fields: workflowContractRecoveryPayloadFields()},
	}
}

func workflowContractRecoveryPayloadFields() []WorkflowPayloadField {
	return []WorkflowPayloadField{
		actionIntegerField("contract_version", true, 2147483647),
		actionArrayField("predecessor_contract_versions", false, 32, "workflow_contract_version"),
		actionPremiseField(),
		actionArrayField("outcome_predicates", false, 8, "workflow_action_outcome_predicates"),
		actionEnumField("outcome_kind", false, "exists", "absent", "outcome", "check"),
		actionObjectField("outcome_payload", false, "workflow_action_outcome"),
		actionEnumListField("required_evidence", true, 0, 7, "verification", "review", "approval", "commit", "durable_note", "native_run", "artifact"),
		actionListField("route_conventions", true, 0, 16),
		actionLawListField("spec_mandate", true),
		actionLawListField("law_modifies", true),
		actionEnumField("rigor_class", true, "prototype_internal", "prototype_trusted", "prototype_public", "prototype_safety_critical", "production_internal", "production_trusted", "production_public", "production_safety_critical", "critical_internal", "critical_trusted", "critical_public", "critical_safety_critical"),
		actionStringField("supersede_reason", true, 4096),
		actionListField("audit_evidence", true, 1, 32),
		actionObjectField("architecture_binding", false, "architecture_binding"),
		actionObjectField("self_repair", false, "workflow_self_repair"),
		actionObjectField("design_record", false, "workflow_design_content"),
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
	if err := checkMandatedLawsTxAtHome(ctx, tx, homeProjectID, homeLocatorID, mandated, modified, true); err != nil {
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
func validateStaleWorkflowContractRecoverySuccessorTx(ctx context.Context, tx *sql.Tx, workID string, predecessors []int64, successor []WorkflowLawRevision) error {
	homeResolved := false
	var homeProjectID, homeLocatorID string
	for _, predecessor := range predecessors {
		var mandateJSON string
		if err := tx.QueryRowContext(ctx, `SELECT spec_mandate FROM workflow_contracts WHERE work_id=? AND contract_version=? AND superseded_by IS NULL`, workID, predecessor).Scan(&mandateJSON); err != nil {
			if err == sql.ErrNoRows {
				return newFailure(KindProjectionNotFound, "validate_stale_workflow_recovery", "previous active workflow contract is unavailable", false, "reload the current contract")
			}
			return wrapFailure(KindUnavailable, "validate_stale_workflow_recovery", "cannot read the stale workflow contract", true, "retry once the workflow projection is readable", err)
		}
		var mandated []string
		if err := json.Unmarshal([]byte(mandateJSON), &mandated); err != nil {
			return newFailure(KindInvariantViolation, "validate_stale_workflow_recovery", "previous workflow contract law mandate is malformed", false, "rebuild projections from the event log")
		}
		var err error
		mandated, err = currentWorkflowLawMandateFromProjection(ctx, tx, workID, predecessor, mandated)
		if err != nil {
			return err
		}
		if len(mandated) == 0 {
			continue
		}
		if !homeResolved {
			homeProjectID, homeLocatorID, err = workflowLawHome(ctx, tx, workID)
			if err != nil {
				return err
			}
			homeResolved = true
		}
		stale, err := findStaleWorkflowLawRevision(ctx, tx, homeProjectID, homeLocatorID, workID, predecessor, mandated)
		if err != nil {
			return err
		}
		if stale == nil {
			continue
		}
		found := false
		for _, revision := range successor {
			if revision.LawID == stale.AcceptedSuccessorLawID && revision.ContentHash == stale.AcceptedSuccessorContentHash {
				found = true
				break
			}
		}
		if !found {
			return newFailure(KindInvalidPayload, "validate_stale_workflow_recovery", "successor contract must pin every accepted successor law revision", false, "include every accepted successor law and current content hash in spec_mandate")
		}
	}
	return nil
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

func findCompatibleWorkflowLawAmendments(ctx context.Context, q queryer, homeProjectID, homeLocatorID, workID string, contractVersion int64, mandated []string) ([]CompatibleLawAmendment, error) {
	amendments := []CompatibleLawAmendment{}
	for _, lawID := range mandated {
		var pinnedHash string
		err := q.QueryRowContext(ctx, `SELECT content_hash FROM workflow_contract_law_revisions WHERE work_id=? AND contract_version=? AND law_id=?`, workID, contractVersion, lawID).Scan(&pinnedHash)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return nil, wrapFailure(KindUnavailable, "check_workflow_law_revision", "cannot read workflow law revision pins", true, "retry once the workflow projection is readable", err)
		}
		var status, currentHash string
		if err := q.QueryRowContext(ctx, `SELECT status,content_hash FROM law_subjects WHERE home_project_id=? AND home_locator_id=? AND law_id=?`, homeProjectID, homeLocatorID, lawID).Scan(&status, &currentHash); err == sql.ErrNoRows {
			failure := newFailure(KindProjectionNotFound, "check_workflow_law_revision", "mandated law is missing from the current Git-derived projection", false, "rebuild the accepted Git law projection")
			failure.CandidateIDs = []string{lawID}
			return nil, failure
		} else if err != nil {
			return nil, wrapFailure(KindUnavailable, "check_workflow_law_revision", "cannot read current law revision", true, "retry once the law projection is readable", err)
		}
		if status == "accepted" && pinnedHash != currentHash {
			amendments = append(amendments, CompatibleLawAmendment{LawID: lawID, PinnedHash: pinnedHash, CurrentHash: currentHash})
		}
	}
	return amendments, nil
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

// workflowContractRecoveryStaleness is the one predicate that decides whether
// a staleness refusal admits operator-approved contract recovery for the named
// subject: a stale law revision, an unresolved Domain overlap, or the
// subject's own stale Domain registry pin. guardSupersedeContractRecovery and
// both workflow_action preflights share it, so no surface can widen the
// recovery admission on its own (CD-0041 D7). A marker naming another item, a
// Product with no registry, and every other refusal stay refused.
func workflowContractRecoveryStaleness(err error, workID string) bool {
	var failure *Failure
	if !failureAs(err, &failure) {
		return false
	}
	switch failure.Kind {
	case KindStaleLawRevision, KindDomainOverlap:
		return true
	case KindStaleRequiresReview:
		return failure.StaleDomainRegistryPin != nil && failure.StaleDomainRegistryPin.WorkID == workID
	default:
		return false
	}
}

func checkWorkflowLawRevisionStalenessTx(ctx context.Context, tx *sql.Tx, workID string) error {
	return checkWorkflowLawRevisionStalenessAdmittingTx(ctx, tx, workID, workflowStalePinAdmitNone)
}

// checkWorkflowLawRevisionStalenessAdmittingTx is the same staleness boundary
// with one admission extension the workflow-action surfaces pass. Attempt
// disposition admits a stale registry pin whose marker names workID itself;
// a peer's stale pin, a stale law revision, and every other refusal stand.
func checkWorkflowLawRevisionStalenessAdmittingTx(ctx context.Context, tx *sql.Tx, workID string, admission workflowStalePinAdmission) error {
	var mandateJSON string
	// This boundary reads the law revisions that one approved contract pins.
	// An absent projection pins nothing, and an ambiguous one names no single
	// contract to read, so neither can carry a stale pin this check could
	// evaluate. An ambiguous item also reaches no implementation-bearing
	// action while it stays ambiguous, so admitting it here surrenders no
	// guard that another one is not already holding.
	//
	// Refusing the ambiguous projection instead makes the duplicate-contract
	// recovery unreachable. Every mutation path runs this boundary first, so
	// supersede_contract is refused before resolveWorkflowContractPredecessors
	// can retire the versions it replaces, and the refusal protects the state
	// it exists to clear.
	activeVersions, err := activeWorkflowContractVersions(ctx, tx, workID)
	if err != nil {
		return err
	}
	if len(activeVersions) != 1 {
		return nil
	}
	contractVersion := activeVersions[0]
	if err := tx.QueryRowContext(ctx, `SELECT spec_mandate FROM workflow_contracts WHERE work_id=? AND contract_version=?`, workID, contractVersion).Scan(&mandateJSON); err != nil {
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
		return checkWorkflowDomainOverlapTxAdmitting(ctx, tx, workID, admission)
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
		return checkWorkflowDomainOverlapTxAdmitting(ctx, tx, workID, admission)
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

// WorkflowLawContext is the bounded, typed resolution of the approved
// contract's binding law and Domain references. The core turns every
// contract-bound law ID and home or affected Domain ID into its projection
// state at continuity read time, so a dispatched packet carries the title of
// each binding law and a file locator that opens inside the Product's
// registered knowledge home, not inside whatever repository the lane was
// dispatched into.
type WorkflowLawContext struct {
	Laws    []WorkflowLawContextLaw    `json:"laws"`
	Domains []WorkflowLawContextDomain `json:"domains"`
	// RegistryPath is the knowledge home repository's absolute locator for
	// the Domain registry shard the home checkout actually carries, set when
	// the context binds at least one Domain. A dispatched lane holds no
	// Concord tool access (CD-0017 D4), so the path is how it reads Domain
	// structure: from the file, never from a tool call.
	RegistryPath string `json:"registry_path,omitempty"`
}

type WorkflowLawContextLaw struct {
	Roles  []string `json:"roles"`
	LawID  string   `json:"law_id"`
	Kind   string   `json:"kind,omitempty"`
	Status string   `json:"status,omitempty"`
	Title  string   `json:"title,omitempty"`
	// Path is the knowledge home repository's absolute locator for the law
	// document the projection recorded, so the lane opens it inside the home
	// checkout rather than its own dispatch repository.
	Path          string   `json:"path,omitempty"`
	ObligationIDs []string `json:"obligation_ids,omitempty"`
	// Criteria lists the law's acceptance criteria bound to the reading
	// work item's own outcome predicates (CD-0180). Bindings naming another
	// work item, and scenario or exemption bindings, never surface here:
	// the law context carries the chaining this work item owes, not the
	// law's whole binding table.
	Criteria []WorkflowLawContextCriterionBinding `json:"criteria,omitempty"`
}

// WorkflowLawContextCriterionBinding is one acceptance criterion of the law
// that the reading work item's own outcome predicate discharges. The reading
// work ID is implicit: every entry the law context carries binds the
// dispatching work item, so the packet renders criterion and predicate only.
type WorkflowLawContextCriterionBinding struct {
	Criterion   int    `json:"criterion"`
	PredicateID string `json:"predicate_id"`
}

type WorkflowLawContextDomain struct {
	DomainID string `json:"domain_id"`
	Name     string `json:"name"`
	Purpose  string `json:"purpose"`
}

// Role vocabulary for the law context. The sources overlap — a modified law
// is also mandated, and a mandated or modified law may carry verification
// obligations — so each law carries every role its contract binds, sorted and
// deduplicated, not one most-specific winner.
const (
	lawContextRoleMandated   = "mandated"
	lawContextRoleModified   = "modified"
	lawContextRoleAdded      = "added"
	lawContextRoleObligation = "obligation"
)

// readWorkflowLawContext resolves the approved contract's bound law and
// Domain references against the law_subjects and domains projections inside
// the caller's transaction. It returns nil when the contract binds no law and
// no Domain, so a contract with no bound law still dispatches. A law listed in
// law_additions — a reserved addition before its subject is published —
// carries its role and identity only. Any other bound law without a subject,
// and any home or affected Domain missing from the registry, refuses with
// KindProjectionNotFound: the packet would otherwise bind a document no lane
// can read. Every file locator the context carries is qualified with the
// Product knowledge home's repository and verified to open there, so a lane
// dispatched into any member Project reads the home's checkout.
func readWorkflowLawContext(ctx context.Context, tx *sql.Tx, workID string, contract *WorkflowReadContract) (*WorkflowLawContext, error) {
	if contract == nil {
		return nil, nil
	}
	roleSet := map[string]map[string]bool{}
	obligationSet := map[string]map[string]bool{}
	markRole := func(lawID, role string) {
		if roleSet[lawID] == nil {
			roleSet[lawID] = map[string]bool{}
		}
		roleSet[lawID][role] = true
	}
	for _, lawID := range contract.SpecMandate {
		markRole(lawID, lawContextRoleMandated)
	}
	if binding := contract.ArchitectureBinding; binding != nil {
		for _, obligation := range binding.VerificationObligations {
			markRole(obligation.LawID, lawContextRoleObligation)
			if obligationSet[obligation.LawID] == nil {
				obligationSet[obligation.LawID] = map[string]bool{}
			}
			obligationSet[obligation.LawID][obligation.ObligationID] = true
		}
		for _, addition := range binding.LawAdditions {
			markRole(addition.LawID, lawContextRoleAdded)
		}
	}
	for _, lawID := range contract.LawModifies {
		markRole(lawID, lawContextRoleModified)
	}
	bindingDomains := 0
	if contract.ArchitectureBinding != nil {
		bindingDomains = 1 + len(contract.ArchitectureBinding.AffectedDomainIDs)
	}
	if len(roleSet) == 0 && bindingDomains == 0 {
		return nil, nil
	}
	context := &WorkflowLawContext{Laws: []WorkflowLawContextLaw{}, Domains: []WorkflowLawContextDomain{}}
	lawIDs := make([]string, 0, len(roleSet))
	for lawID := range roleSet {
		lawIDs = append(lawIDs, lawID)
	}
	sort.Strings(lawIDs)
	if len(lawIDs) > 0 {
		var laws []WorkflowLawContextLaw
		homeProjectID, homeLocatorID, err := workflowLawHome(ctx, tx, workID)
		if err != nil {
			return nil, err
		}
		// CD-0200: a bound law resolves across the Product's registered
		// source set; the designated home stays the first resolution target
		// and a bare ID held by more than one source refuses as ambiguous.
		productID, _, roleErr := resolveKnowledgeSourceRole(ctx, tx, KnowledgeHome{HomeProjectID: homeProjectID, HomeLocatorID: homeLocatorID})
		if roleErr != nil {
			return nil, roleErr
		}
		federated := false
		if productID != "" {
			sources, srcErr := resolveKnowledgeQuerySources(ctx, tx, productID, "read_workflow_law_context")
			if srcErr != nil {
				return nil, srcErr
			}
			if len(sources) > 1 {
				federated = true
				laws, err = resolveLawContextSubjectsAcrossSources(ctx, tx, sources, workID, lawContextLaws(roleSet, obligationSet, lawIDs))
				if err != nil {
					return nil, err
				}
			}
		}
		if !federated {
			homeRepo, err := workflowLawHomeRepo(ctx, tx, homeProjectID, homeLocatorID)
			if err != nil {
				return nil, err
			}
			laws, err = resolveLawContextSubjects(ctx, tx, homeProjectID, homeLocatorID, homeRepo, workID, lawContextLaws(roleSet, obligationSet, lawIDs))
			if err != nil {
				return nil, err
			}
		}
		context.Laws = laws
	}
	if contract.ArchitectureBinding != nil {
		productID, err := workflowBindingProductIDTx(ctx, tx, workID)
		if err != nil {
			return nil, err
		}
		domains, err := resolveLawContextDomains(ctx, tx, productID, *contract.ArchitectureBinding)
		if err != nil {
			return nil, err
		}
		context.Domains = domains
		registryRepo, err := domainRegistryHomeRepo(ctx, tx, productID)
		if err != nil {
			return nil, err
		}
		context.RegistryPath, err = knowledgeRegistryLocator(registryRepo)
		if err != nil {
			return nil, err
		}
	}
	return context, nil
}

// domainRegistryHomeRepo reads the checkout of the knowledge home the
// Product's Domain registry projection was scanned from, so the registry
// locator names the file the rendered Domains came from. A Product with no
// registry projection refuses typed rather than naming any repository's file.
func domainRegistryHomeRepo(ctx context.Context, tx *sql.Tx, productID string) (string, error) {
	var homeProjectID, homeLocatorID string
	err := tx.QueryRowContext(ctx, `SELECT home_project_id,home_locator_id FROM domain_registries WHERE product_id=?`, productID).Scan(&homeProjectID, &homeLocatorID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", newFailure(KindDomainRegistryAbsent, "read_workflow_law_context", "the Product has no Domain registry projection", false, "rebuild the Domain registry projection from the Product knowledge home")
	}
	if err != nil {
		return "", wrapFailure(KindUnavailable, "read_workflow_law_context", "cannot read the Domain registry home", true, "retry once the Domain registry is readable", err)
	}
	return workflowLawHomeRepo(ctx, tx, homeProjectID, homeLocatorID)
}

// workflowLawHomeRepo reads the canonical repository path of one resolved Git
// law home through whichever queryer the caller already holds. A knowledge
// home locator is a canonical-path locator by designation rule, so its
// normalized value is the absolute checkout a dispatched lane can open.
func workflowLawHomeRepo(ctx context.Context, q queryer, homeProjectID, homeLocatorID string) (string, error) {
	var repo string
	err := q.QueryRowContext(ctx, `SELECT normalized_value FROM project_locators WHERE project_id=? AND locator_id=? AND kind='canonical_path'`, homeProjectID, homeLocatorID).Scan(&repo)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && repo == "") {
		return "", newFailure(KindUnknownScope, "read_workflow_law_context", "the Git law home has no canonical repository path", false, "designate the Product knowledge home's canonical-path locator")
	}
	if err != nil {
		return "", wrapFailure(KindUnavailable, "read_workflow_law_context", "cannot read the Git law home repository path", true, "retry once the Project locators are readable", err)
	}
	return repo, nil
}

// knowledgeRegistryLocator resolves the Domain registry shard of the layout
// tier the knowledge home checkout carries and returns it as a home-qualified
// absolute locator the lane can open. The tier is the newest layout whose
// manifest head the checkout holds (CD-0194 D5), the rule the committed-shard
// reader applies; a registry under any other tier is not this home's
// registry. A checkout that predates the shard homes carries the aggregate
// manifest itself, so the aggregate file is the locator: its domain_registry
// member is the registry the lane reads. A home carrying none of the three
// shapes, or whose selected shape holds no regular registry file, refuses
// typed: the packet would otherwise advertise a locator no lane can read, or
// silently drop the Domain binding.
func knowledgeRegistryLocator(homeRepo string) (string, error) {
	for _, layout := range knowledgeShardLayouts {
		head := filepath.Join(homeRepo, filepath.FromSlash(layout.headPath))
		if _, err := os.Stat(head); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return "", wrapFailure(KindUnavailable, "read_workflow_law_context", "cannot inspect the knowledge manifest head", true, "restore access to the Product knowledge home and retry", err)
		}
		registry := filepath.Join(homeRepo, filepath.FromSlash(layout.registryPath))
		info, err := os.Stat(registry)
		if errors.Is(err, os.ErrNotExist) || (err == nil && !info.Mode().IsRegular()) {
			return "", newFailure(KindDomainRegistryAbsent, "read_workflow_law_context", "the knowledge home's selected layout carries no Domain registry file: "+registry, false, "publish the Domain registry shard beside the knowledge manifest head")
		}
		if err != nil {
			return "", wrapFailure(KindUnavailable, "read_workflow_law_context", "cannot inspect the Domain registry shard", true, "restore access to the Product knowledge home and retry", err)
		}
		if err := checkKnowledgeLocatorReadable(homeRepo, layout.registryPath); err != nil {
			return "", wrapFailure(KindUnavailable, "read_workflow_law_context", "cannot open the Domain registry shard", true, "restore access to the Product knowledge home and retry", err)
		}
		return registry, nil
	}
	aggregate := filepath.Join(homeRepo, filepath.FromSlash(knowledgeManifestPath))
	aggregateInfo, err := os.Stat(aggregate)
	if errors.Is(err, os.ErrNotExist) {
		return "", newFailure(KindDomainRegistryAbsent, "read_workflow_law_context", "the Product knowledge home repository carries no knowledge manifest head under a supported layout", false, "publish the knowledge shards in the knowledge home repository")
	}
	if err != nil {
		return "", wrapFailure(KindUnavailable, "read_workflow_law_context", "cannot inspect the aggregate knowledge manifest", true, "restore access to the Product knowledge home and retry", err)
	}
	if !aggregateInfo.Mode().IsRegular() {
		return "", newFailure(KindDomainRegistryAbsent, "read_workflow_law_context", "the aggregate knowledge manifest is not a regular file: "+aggregate, false, "commit a regular aggregate manifest file")
	}
	if err := checkKnowledgeLocatorReadable(homeRepo, knowledgeManifestPath); err != nil {
		return "", wrapFailure(KindUnavailable, "read_workflow_law_context", "cannot open the aggregate knowledge manifest", true, "restore access to the Product knowledge home and retry", err)
	}
	return aggregate, nil
}

// knowledgeLawLocator qualifies one projected law document path with its
// knowledge home repository and refuses typed unless the qualified locator is
// a regular file there: a bound law is a required source, so the packet must
// never advertise a document the dispatched lane cannot open. The projection's
// freshness watermark still owns whether the recorded content hash is current;
// this check owns only the locator's readability.
func knowledgeLawLocator(homeRepo, lawID, subjectPath string) (string, error) {
	if subjectPath == "" {
		return "", nil
	}
	qualified := filepath.Join(homeRepo, filepath.FromSlash(subjectPath))
	info, err := os.Stat(qualified)
	if errors.Is(err, os.ErrNotExist) {
		failure := newFailure(KindProjectionNotFound, "read_workflow_law_context", "bound law document is missing from the knowledge home checkout: "+qualified, false, "rebuild the accepted Git law projection")
		failure.CandidateIDs = []string{lawID}
		return "", failure
	}
	if err != nil {
		return "", wrapFailure(KindUnavailable, "read_workflow_law_context", "cannot inspect the bound law document in the knowledge home checkout", true, "restore access to the Product knowledge home and retry", err)
	}
	if !info.Mode().IsRegular() {
		failure := newFailure(KindProjectionNotFound, "read_workflow_law_context", "bound law locator is not a regular file in the knowledge home checkout: "+qualified, false, "rebuild the accepted Git law projection")
		failure.CandidateIDs = []string{lawID}
		return "", failure
	}
	if err := checkKnowledgeLocatorReadable(homeRepo, subjectPath); err != nil {
		return "", wrapFailure(KindUnavailable, "read_workflow_law_context", "cannot open the bound law document in the knowledge home checkout", true, "restore access to the Product knowledge home and retry", err)
	}
	return qualified, nil
}

// checkKnowledgeLocatorReadable opens one repository-relative knowledge path
// confined to the knowledge home checkout, so a projected path that escapes
// the home through ".." or a symlink cannot be advertised as readable.
func checkKnowledgeLocatorReadable(homeRepo, relative string) error {
	file, err := os.OpenInRoot(homeRepo, filepath.FromSlash(relative))
	if err != nil {
		return err
	}
	return file.Close()
}

// lawContextLaws builds each bound law's identity with its sorted, deduplicated
// roles and obligation IDs, ahead of the subject lookup.
func lawContextLaws(roleSet, obligationSet map[string]map[string]bool, lawIDs []string) []WorkflowLawContextLaw {
	laws := make([]WorkflowLawContextLaw, 0, len(lawIDs))
	for _, lawID := range lawIDs {
		law := WorkflowLawContextLaw{LawID: lawID}
		for role := range roleSet[lawID] {
			law.Roles = append(law.Roles, role)
		}
		sort.Strings(law.Roles)
		for obligationID := range obligationSet[lawID] {
			law.ObligationIDs = append(law.ObligationIDs, obligationID)
		}
		sort.Strings(law.ObligationIDs)
		laws = append(laws, law)
	}
	return laws
}

// resolveLawContextSubjects reads the law_subjects row for each bound law in
// one bounded batch. The 128-entry ceiling is the write-side sum of the
// mandate (32), additions (32), and obligations (64) bounds. Each law's
// authored criterion bindings ride the row; the ones naming the reading work
// item's predicates become that law's Criteria (CD-0180). Each subject's
// recorded path is qualified with the knowledge home repository and verified
// to open there before it reaches the packet.
func resolveLawContextSubjects(ctx context.Context, tx *sql.Tx, homeProjectID, homeLocatorID, homeRepo, workID string, laws []WorkflowLawContextLaw) ([]WorkflowLawContextLaw, error) {
	if len(laws) > 128 {
		return nil, newFailure(KindLimitExceeded, "read_workflow_law_context", "workflow contract binds more laws than the law context carries", false, "reduce_limit")
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(laws)), ",")
	args := make([]any, 0, len(laws)+2)
	args = append(args, homeProjectID, homeLocatorID)
	for _, law := range laws {
		args = append(args, law.LawID)
	}
	rows, err := tx.QueryContext(ctx, `SELECT law_id,kind,status,title,path,criterion_bindings FROM law_subjects WHERE home_project_id=? AND home_locator_id=? AND law_id IN (`+placeholders+`)`, args...) //nolint:gosec // the fragment contains only generated question-mark placeholders and every law ID stays parameter-bound.
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "read_workflow_law_context", "cannot read the law subjects for the bound laws", true, "retry once the law projection is readable", err)
	}
	defer rows.Close()
	type subjectRow struct {
		subject  WorkflowLawContextLaw
		bindings []KnowledgeCriterionBinding
	}
	subjects := map[string]subjectRow{}
	for rows.Next() {
		var row subjectRow
		var bindingsJSON string
		if err := rows.Scan(&row.subject.LawID, &row.subject.Kind, &row.subject.Status, &row.subject.Title, &row.subject.Path, &bindingsJSON); err != nil {
			return nil, wrapFailure(KindUnavailable, "read_workflow_law_context", "cannot decode a bound law subject", true, "retry once the law projection is readable", err)
		}
		if err := json.Unmarshal([]byte(bindingsJSON), &row.bindings); err != nil {
			return nil, wrapFailure(KindInvariantViolation, "read_workflow_law_context", "a bound law carries criterion bindings the projection cannot decode", false, "rebuild the accepted Git law projection", err)
		}
		subjects[row.subject.LawID] = row
	}
	if err := rows.Err(); err != nil {
		return nil, wrapFailure(KindUnavailable, "read_workflow_law_context", "cannot enumerate the bound law subjects", true, "retry once the law projection is readable", err)
	}
	for index := range laws {
		row, exists := subjects[laws[index].LawID]
		if !exists {
			if listedAsAddition(laws[index].Roles) {
				continue
			}
			failure := newFailure(KindProjectionNotFound, "read_workflow_law_context", "bound law is missing from the current Git-derived projection", false, "rebuild the accepted Git law projection")
			failure.CandidateIDs = []string{laws[index].LawID}
			return nil, failure
		}
		laws[index].Kind, laws[index].Status, laws[index].Title = row.subject.Kind, row.subject.Status, row.subject.Title
		path, err := knowledgeLawLocator(homeRepo, laws[index].LawID, row.subject.Path)
		if err != nil {
			return nil, err
		}
		laws[index].Path = path
		laws[index].Criteria = lawContextCriteria(row.bindings, workID)
	}
	return laws, nil
}

// resolveLawContextSubjectsAcrossSources resolves each bound law across the
// Product's registered source set (CD-0200). A bare law ID must resolve to
// exactly one source's projection; the resolved source's own repository
// qualifies the law's file locator, so a lane dispatched into any member
// Project opens the checkout that holds the law.
func resolveLawContextSubjectsAcrossSources(ctx context.Context, tx *sql.Tx, sources []KnowledgeHome, workID string, laws []WorkflowLawContextLaw) ([]WorkflowLawContextLaw, error) {
	if len(laws) > 128 {
		return nil, newFailure(KindLimitExceeded, "read_workflow_law_context", "workflow contract binds more laws than the law context carries", false, "reduce_limit")
	}
	type subjectRow struct {
		subject  WorkflowLawContextLaw
		bindings []KnowledgeCriterionBinding
		home     KnowledgeHome
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(laws)), ",")
	subjects := map[string]subjectRow{}
	repos := make(map[string]string, len(sources))
	for _, source := range sources {
		repo, err := workflowLawHomeRepo(ctx, tx, source.HomeProjectID, source.HomeLocatorID)
		if err != nil {
			return nil, err
		}
		repos[source.HomeProjectID+"/"+source.HomeLocatorID] = repo
		args := []any{source.HomeProjectID, source.HomeLocatorID}
		for _, law := range laws {
			args = append(args, law.LawID)
		}
		rows, err := tx.QueryContext(ctx, `SELECT law_id,kind,status,title,path,criterion_bindings FROM law_subjects WHERE home_project_id=? AND home_locator_id=? AND law_id IN (`+placeholders+`)`, args...) //nolint:gosec // the fragment contains only generated question-mark placeholders and every law ID stays parameter-bound.
		if err != nil {
			return nil, wrapFailure(KindUnavailable, "read_workflow_law_context", "cannot read the law subjects for the bound laws", true, "retry once the law projection is readable", err)
		}
		for rows.Next() {
			var row subjectRow
			var bindingsJSON string
			if err := rows.Scan(&row.subject.LawID, &row.subject.Kind, &row.subject.Status, &row.subject.Title, &row.subject.Path, &bindingsJSON); err != nil {
				rows.Close()
				return nil, wrapFailure(KindUnavailable, "read_workflow_law_context", "cannot decode a bound law subject", true, "retry once the law projection is readable", err)
			}
			if err := json.Unmarshal([]byte(bindingsJSON), &row.bindings); err != nil {
				rows.Close()
				return nil, newFailure(KindInvariantViolation, "read_workflow_law_context", "a bound law carries criterion bindings the projection cannot decode", false, "rebuild the accepted Git law projection")
			}
			if _, clash := subjects[row.subject.LawID]; clash {
				rows.Close()
				failure := newFailure(KindKnowledgeAmbiguous, "read_workflow_law_context", "bound law is held by more than one registered source: "+row.subject.LawID, false, "qualify the reference as project_id/law_id or remove the duplicate law")
				failure.CandidateIDs = []string{source.HomeProjectID + "/" + row.subject.LawID}
				return nil, failure
			}
			row.home = source
			subjects[row.subject.LawID] = row
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, wrapFailure(KindUnavailable, "read_workflow_law_context", "cannot enumerate the bound law subjects", true, "retry once the law projection is readable", err)
		}
		if err := rows.Close(); err != nil {
			return nil, wrapFailure(KindUnavailable, "read_workflow_law_context", "cannot enumerate the bound law subjects", true, "retry once the law projection is readable", err)
		}
	}
	for index := range laws {
		row, exists := subjects[laws[index].LawID]
		if !exists {
			if listedAsAddition(laws[index].Roles) {
				continue
			}
			failure := newFailure(KindProjectionNotFound, "read_workflow_law_context", "bound law is missing from the current Git-derived projection", false, "rebuild the accepted Git law projection")
			failure.CandidateIDs = []string{laws[index].LawID}
			return nil, failure
		}
		laws[index].Kind, laws[index].Status, laws[index].Title = row.subject.Kind, row.subject.Status, row.subject.Title
		repo := repos[row.home.HomeProjectID+"/"+row.home.HomeLocatorID]
		path, err := knowledgeLawLocator(repo, laws[index].LawID, row.subject.Path)
		if err != nil {
			return nil, err
		}
		laws[index].Path = path
		laws[index].Criteria = lawContextCriteria(row.bindings, workID)
	}
	return laws, nil
}

// lawContextCriteria keeps the law's predicate bindings that name the reading
// work item, in criterion order. A binding naming another work item, and a
// scenario or exemption binding, never surface in the law context.
func lawContextCriteria(bindings []KnowledgeCriterionBinding, workID string) []WorkflowLawContextCriterionBinding {
	var criteria []WorkflowLawContextCriterionBinding
	for _, binding := range bindings {
		if binding.WorkID != workID || binding.PredicateID == "" {
			continue
		}
		criteria = append(criteria, WorkflowLawContextCriterionBinding{Criterion: binding.Criterion, PredicateID: binding.PredicateID})
	}
	sort.Slice(criteria, func(left, right int) bool { return criteria[left].Criterion < criteria[right].Criterion })
	return criteria
}

// listedAsAddition reports whether the contract lists the law in
// law_additions, the one listing the approved design allows to precede its
// law_subjects row: an addition is barred from modifying or pinning an
// existing law ID, so its absent subject at continuity read time is the
// designed pre-publication state. Every other bound role promises a document
// that already exists.
func listedAsAddition(roles []string) bool {
	for _, role := range roles {
		if role == lawContextRoleAdded {
			return true
		}
	}
	return false
}

// resolveLawContextDomains reads the domains projection for the binding's
// home Domain first, then its affected Domains in ID order.
func resolveLawContextDomains(ctx context.Context, tx *sql.Tx, productID string, binding WorkflowArchitectureBinding) ([]WorkflowLawContextDomain, error) {
	ordered := make([]string, 0, 1+len(binding.AffectedDomainIDs))
	seen := map[string]bool{}
	for _, domainID := range append([]string{binding.HomeDomainID}, binding.AffectedDomainIDs...) {
		if !seen[domainID] {
			seen[domainID] = true
			ordered = append(ordered, domainID)
		}
	}
	sort.Strings(ordered[1:])
	domains := make([]WorkflowLawContextDomain, 0, len(ordered))
	for _, domainID := range ordered {
		var domain WorkflowLawContextDomain
		err := tx.QueryRowContext(ctx, `SELECT name,purpose FROM domains WHERE product_id=? AND domain_id=?`, productID, domainID).Scan(&domain.Name, &domain.Purpose)
		if err == sql.ErrNoRows {
			failure := newFailure(KindProjectionNotFound, "read_workflow_law_context", "bound Domain is missing from the Domain registry projection", false, "rebuild the Domain registry projection")
			failure.CandidateIDs = []string{domainID}
			return nil, failure
		}
		if err != nil {
			return nil, wrapFailure(KindUnavailable, "read_workflow_law_context", "cannot read a bound Domain", true, "retry once the Domain registry is readable", err)
		}
		domain.DomainID = domainID
		domains = append(domains, domain)
	}
	return domains, nil
}

func validateWorkflowContractRecoveryPayload(raw json.RawMessage) error {
	fields, err := workflowActionObject(raw)
	if err != nil {
		return err
	}
	allowed := make(map[string]WorkflowPayloadField)
	for _, field := range workflowContractRecoveryPayloadFields() {
		allowed[field.Name] = field
	}
	for name, value := range fields {
		field, ok := allowed[name]
		if !ok || !validateWorkflowPayloadValue(field, value) {
			return newFailure(KindInvalidPayload, "workflow_action", "successor contract contains an undeclared or invalid field", false, "supply the typed successor contract")
		}
	}
	for _, name := range []string{"contract_version", "premise", "required_evidence", "route_conventions", "spec_mandate", "law_modifies", "rigor_class", "supersede_reason", "audit_evidence"} {
		if _, ok := fields[name]; !ok {
			return newFailure(KindInvalidPayload, "workflow_action", "successor contract requires a fully supplied contract", false, "supply every successor contract field")
		}
	}
	hasPredicates := fields["outcome_predicates"] != nil
	hasLegacyOutcome := fields["outcome_kind"] != nil && fields["outcome_payload"] != nil
	if !hasPredicates && !hasLegacyOutcome {
		return newFailure(KindInvalidPayload, "workflow_action", "successor contract requires outcome_predicates or the outcome pair", false, "supply the complete successor outcome")
	}
	if hasPredicates && hasLegacyOutcome {
		return newFailure(KindInvalidPayload, "workflow_action", "successor contract must supply one outcome shape", false, "supply outcome_predicates or the outcome pair")
	}
	if workflowFieldInt(fields, "contract_version", 0) <= 0 || workflowFieldStringDefault(fields, "premise", "") == "" || workflowFieldStringDefault(fields, "rigor_class", "") == "" || workflowFieldStringDefault(fields, "supersede_reason", "") == "" {
		return newFailure(KindInvalidPayload, "workflow_action", "successor contract has an empty required field", false, "supply the complete successor contract")
	}
	return nil
}

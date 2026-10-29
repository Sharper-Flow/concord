package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/sharper-flow/concord/internal/store/internal/sqlitedb"
)

// The closed work-relation kinds resolve_overlap records between two items
// that each hold an approved Product-changing contract (CD-0222, CD-0013 D11).
const (
	ResolutionDependsOn  = "depends_on"
	ResolutionBlocks     = "blocks"
	ResolutionMergedInto = "merged_into"
	ResolutionSupersedes = "supersedes"
)

type WorkflowDomainOverlapResolutionRequest struct {
	EventID                 string
	FromWorkID              string
	ToWorkID                string
	FromExpectedVersion     int64
	ToExpectedVersion       int64
	FromContractVersion     int64
	ToContractVersion       int64
	ResolutionKind          string
	Reason                  string
	ApprovalRef             string
	ApprovalOperationDigest string
	ApprovalScopeJSON       string
	ApprovalVersionsJSON    string
	ApprovalConsequence     string
	// RequestedBudgetSeconds is the caller's actual parsed effect budget;
	// 0 means absent under this input contract. It is request metadata,
	// not authorization, and never supplies Product context.
	RequestedBudgetSeconds int
	Actor                  string
	OccurredAt             time.Time
}

// ResolveWorkflowDomainOverlapTx appends the immutable resolution event. The
// supplied terminal-resolution approval verifies its exact consumed row before
// any write. All current-version, overlap, relation, and
// terminal checks fold in this same transaction.
func ResolveWorkflowDomainOverlapTx(ctx context.Context, tx *Transaction, request WorkflowDomainOverlapResolutionRequest) (ApplyOperationResult, error) {
	sqlTx, err := transactionSQL(tx, "workflow_domain_overlap_resolution")
	if err != nil {
		return ApplyOperationResult{}, err
	}
	if request.OccurredAt.IsZero() {
		request.OccurredAt = tx.now()
	}
	if request.Actor == "" {
		return ApplyOperationResult{}, overlapResolutionFailure("overlap resolution requires an authenticated actor")
	}
	binding := workflowApprovalBinding{ApprovalRef: request.ApprovalRef, OperationDigest: request.ApprovalOperationDigest, ScopeJSON: request.ApprovalScopeJSON, VersionsJSON: request.ApprovalVersionsJSON, Consequence: request.ApprovalConsequence}
	if binding != (workflowApprovalBinding{}) {
		if request.ResolutionKind != ResolutionMergedInto && request.ResolutionKind != ResolutionSupersedes {
			return ApplyOperationResult{}, overlapResolutionFailure("a routine overlap resolution accepts no approval claim")
		}
		if _, err := workflowOperatorFromConsumedApprovalTx(ctx, sqlTx, binding, "workflow_domain_overlap"); err != nil {
			return ApplyOperationResult{}, err
		}
		if err := workflowOverlapDigestBinding(request); err != nil {
			return ApplyOperationResult{}, err
		}
	}
	if err := checkOverlapGoverningScope(ctx, sqlTx, request); err != nil {
		return ApplyOperationResult{}, err
	}
	payload, err := json.Marshal(map[string]any{
		"work_id": request.FromWorkID, "expected_version": request.FromExpectedVersion, "resulting_version": request.FromExpectedVersion + 1,
		"to_work_id": request.ToWorkID, "to_expected_version": request.ToExpectedVersion, "to_resulting_version": request.ToExpectedVersion + 1,
		"from_contract_version": request.FromContractVersion, "to_contract_version": request.ToContractVersion,
		"resolution_kind": request.ResolutionKind, "reason": request.Reason, "approval_ref": request.ApprovalRef,
		"approval_operation_digest": request.ApprovalOperationDigest,
		"approval_scope_json":       request.ApprovalScopeJSON,
		"approval_versions_json":    request.ApprovalVersionsJSON,
		"approval_consequence":      request.ApprovalConsequence,
	})
	if err != nil {
		return ApplyOperationResult{}, err
	}
	return applyOperationTx(ctx, sqlTx, Operation{Events: []Event{{EventID: request.EventID, Kind: WorkflowOverlapResolved, SubjectType: SubjectWorkItem, SubjectID: request.FromWorkID, Actor: request.Actor, OccurredAt: request.OccurredAt.UTC(), PayloadVersion: eventKindRegistry[WorkflowOverlapResolved].CurrentVersion, Payload: payload}}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, request.FromWorkID): request.FromExpectedVersion, VersionRef(SubjectWorkItem, request.ToWorkID): request.ToExpectedVersion}}, newWriteScope(sqlTx), true)
}

func validateWorkflowOverlapResolutionKind(kind string) bool {
	return kind == ResolutionDependsOn || kind == ResolutionBlocks || kind == ResolutionMergedInto || kind == ResolutionSupersedes
}

func workflowOverlapResolutionRelationKind(kind string) string {
	switch kind {
	case ResolutionDependsOn, ResolutionBlocks, ResolutionMergedInto, ResolutionSupersedes:
		return kind
	default:
		return ""
	}
}

// workflowOverlapApprovalScope is the typed planner scope a destructive
// overlap approval binds: the endpoint work_ids membership the store
// re-derives. Extra outer scope fields stay legal and unread.
type workflowOverlapApprovalScope struct {
	WorkIDs []string `json:"work_ids"`
}

// workflowOverlapApprovalVersions is the typed four-version pin a destructive
// overlap approval binds.
type workflowOverlapApprovalVersions struct {
	From         int64 `json:"from"`
	To           int64 `json:"to"`
	FromContract int64 `json:"from_contract"`
	ToContract   int64 `json:"to_contract"`
}

func workflowOverlapApprovalUnauthorized(mismatches string) error {
	return newFailure(KindUnauthorized, "workflow_domain_overlap", "the consumed operator approval is not bound to this overlap resolution: "+mismatches, false, "request approval for the exact destructive resolution")
}

// workflowOverlapDigestContext recovers the canonical Product and Project
// from the request's approval scope, trusted because the consumed-row check
// just proved it identical to the durable approval row. An absent or empty
// product_id is meaningful and passes through as ""; project_ids must carry
// exactly the operation's one ambient Project. Ambiguous scope refuses
// rather than choosing arbitrarily.
func workflowOverlapDigestContext(scopeJSON string) (string, string, error) {
	var scope map[string]any
	if err := json.Unmarshal([]byte(scopeJSON), &scope); err != nil || scope == nil {
		return "", "", overlapResolutionFailure("destructive overlap approval scope is malformed")
	}
	productID := ""
	if raw, present := scope["product_id"]; present && raw != nil {
		typed, ok := raw.(string)
		if !ok {
			return "", "", overlapResolutionFailure("destructive overlap approval scope product_id is malformed")
		}
		productID = typed
	}
	projects, ok := scope["project_ids"].([]any)
	if !ok || len(projects) != 1 {
		return "", "", overlapResolutionFailure("destructive overlap approval scope does not carry exactly one ambient Project")
	}
	projectID, ok := projects[0].(string)
	if !ok {
		return "", "", overlapResolutionFailure("destructive overlap approval scope project_ids is malformed")
	}
	return productID, projectID, nil
}

// workflowOverlapDigestBinding recomputes the consumed approval's canonical
// digest from the actual resolved request — the eight exact domain fields
// plus an explicit positive effect budget — under the recovered canonical
// context, so a changed reason, kind, direction, version pin, or effect
// budget cannot retain a consumed binding.
func workflowOverlapDigestBinding(request WorkflowDomainOverlapResolutionRequest) error {
	if request.RequestedBudgetSeconds < 0 {
		return workflowOverlapApprovalUnauthorized("budget")
	}
	productID, projectID, err := workflowOverlapDigestContext(request.ApprovalScopeJSON)
	if err != nil {
		return err
	}
	input := map[string]any{
		"from_work_id":          request.FromWorkID,
		"to_work_id":            request.ToWorkID,
		"from_expected_version": request.FromExpectedVersion,
		"to_expected_version":   request.ToExpectedVersion,
		"from_contract_version": request.FromContractVersion,
		"to_contract_version":   request.ToContractVersion,
		"resolution_kind":       request.ResolutionKind,
		"reason":                request.Reason,
	}
	if request.RequestedBudgetSeconds > 0 {
		input["requested_budget_seconds"] = request.RequestedBudgetSeconds
	}
	raw, err := json.Marshal(input)
	if err != nil {
		return overlapResolutionFailure("destructive overlap resolution input is malformed")
	}
	if AgentMutationDigest("concord_work_relate", "resolve_overlap", productID, projectID, raw) != request.ApprovalOperationDigest {
		return workflowOverlapApprovalUnauthorized("digest")
	}
	return nil
}

// workflowOverlapResolutionBindingAdmission re-derives one v2 payload's
// approval class from its own bytes on replay, exactly as the live route
// verified it against the consumed row. The store re-derives the endpoint
// membership and the four version pins the payload binds.
func workflowOverlapResolutionBindingAdmission(event Event, payload workflowOverlapResolvedPayload) error {
	// CD-0243 D3/D7 remove kind-only consent. An existing approval claim
	// still binds its exact endpoints and versions on replay.
	if payload.ApprovalRef == "" && payload.ApprovalOperationDigest == "" && payload.ApprovalScopeJSON == "" && payload.ApprovalVersionsJSON == "" && payload.ApprovalConsequence == "" {
		return nil
	}
	if payload.ResolutionKind != ResolutionMergedInto && payload.ResolutionKind != ResolutionSupersedes {
		return overlapResolutionFailure("a routine overlap resolution accepts no approval claim")
	}
	if payload.ApprovalRef == "" || !validDigest(payload.ApprovalOperationDigest) || payload.ApprovalScopeJSON == "" || payload.ApprovalVersionsJSON == "" || payload.ApprovalConsequence == "" {
		return overlapResolutionFailure("a destructive overlap resolution carries no complete approval binding")
	}
	var scope workflowOverlapApprovalScope
	if err := json.Unmarshal([]byte(payload.ApprovalScopeJSON), &scope); err != nil {
		return overlapResolutionFailure("destructive overlap approval scope is malformed")
	}
	var versions workflowOverlapApprovalVersions
	if err := json.Unmarshal([]byte(payload.ApprovalVersionsJSON), &versions); err != nil {
		return overlapResolutionFailure("destructive overlap approval versions are malformed")
	}
	if !slices.Contains(scope.WorkIDs, event.SubjectID) || !slices.Contains(scope.WorkIDs, payload.ToWorkID) {
		return workflowOverlapApprovalUnauthorized("work_ids")
	}
	if versions != (workflowOverlapApprovalVersions{From: *payload.ExpectedVersion, To: payload.ToExpectedVersion, FromContract: payload.FromContractVersion, ToContract: payload.ToContractVersion}) {
		return workflowOverlapApprovalUnauthorized("versions")
	}
	return nil
}

func overlapResolutionFailure(detail string) error {
	return newFailure(KindInvalidPayload, "workflow_domain_overlap", detail, false, "supply a current operator-approved overlap resolution")
}

func workflowOverlapResolutionProjectionError(err error) error {
	if err == nil {
		return nil
	}
	if sqlitedb.IsConstraintViolation(err) {
		return newFailure(KindProjectionConflict, "workflow_domain_overlap", "overlap resolution projection conflicts with current history", false, "reread the active overlap and resolve it again")
	}
	return wrapFailure(KindUnavailable, "workflow_domain_overlap", fmt.Sprintf("cannot update overlap resolution projection: %v", err), true, "retry once the database is writable", err)
}

func invalidateWorkflowOverlapResolutionPairTx(ctx context.Context, tx *sql.Tx, eventID, fromWorkID, toWorkID string) error {
	seq, err := eventSequenceTx(ctx, tx, eventID)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM relations WHERE resolution_id IN (
		SELECT resolution_id FROM workflow_overlap_resolutions
		WHERE invalidated_seq IS NULL
		  AND ((from_work_id=? AND to_work_id=?) OR (from_work_id=? AND to_work_id=?))
	)`, fromWorkID, toWorkID, toWorkID, fromWorkID); err != nil {
		return workflowOverlapResolutionProjectionError(err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE workflow_overlap_resolutions SET invalidated_seq=?
		WHERE invalidated_seq IS NULL
		  AND ((from_work_id=? AND to_work_id=?) OR (from_work_id=? AND to_work_id=?))`, seq, fromWorkID, toWorkID, toWorkID, fromWorkID); err != nil {
		return workflowOverlapResolutionProjectionError(err)
	}
	return nil
}

func eventSequenceTx(ctx context.Context, tx *sql.Tx, eventID string) (int64, error) {
	var seq int64
	if err := tx.QueryRowContext(ctx, `SELECT effect_seq FROM effect_receipts WHERE event_id=?`, eventID).Scan(&seq); err != nil {
		return 0, wrapFailure(KindUnavailable, "workflow_domain_overlap", "cannot read authoritative effect sequence", true, "retry once the effect receipt is readable", err)
	}
	return seq, nil
}

func terminalizeWorkflowOverlapWork(ctx context.Context, tx *sql.Tx, event Event, workID string, current, resulting int64) error {
	terminalEvent := event
	terminalEvent.SubjectID = workID
	if err := updateWorkLifecycle(ctx, tx, terminalEvent, "superseded", current, resulting, nil); err != nil {
		return err
	}
	if err := removeTerminalResearchBindings(ctx, tx, workID, event.OccurredAt); err != nil {
		return err
	}
	if err := foldTerminalReleasesResourceClaims(ctx, tx, terminalEvent); err != nil {
		return wrapFailure(KindUnavailable, "workflow_domain_overlap", "cannot release terminal overlap resource claims", true, "retry once the database is writable", err)
	}
	return nil
}

func foldWorkflowOverlapResolved(ctx context.Context, tx *sql.Tx, event Event) error {
	var payload workflowOverlapResolvedPayload
	if err := decodeWorkflowPayload(event, &payload); err != nil {
		return err
	}
	if err := workflowBase(event, payload.WorkflowVersionFields); err != nil {
		return err
	}
	if payload.ToWorkID == "" || payload.ToWorkID == event.SubjectID || payload.ExpectedVersion == nil || payload.ResultingVersion == nil || payload.FromContractVersion <= 0 || payload.ToContractVersion <= 0 || payload.ToExpectedVersion <= 0 || payload.ToResultingVersion != payload.ToExpectedVersion+1 || payload.ResolutionKind == "" || !validateWorkflowOverlapResolutionKind(payload.ResolutionKind) || !workflowString(payload.Reason, 4096) {
		return overlapResolutionFailure("overlap resolution has invalid endpoint, version, kind, or reason")
	}
	if err := workflowOverlapResolutionBindingAdmission(event, payload); err != nil {
		return err
	}
	fromExpected, fromResulting := *payload.ExpectedVersion, *payload.ResultingVersion
	from, to, err := readWorkflowOverlapResolutionWorks(ctx, tx, event, payload)
	if err != nil {
		return err
	}
	if err := checkOverlapGoverningScope(ctx, tx, WorkflowDomainOverlapResolutionRequest{FromWorkID: event.SubjectID, ToWorkID: payload.ToWorkID, ResolutionKind: payload.ResolutionKind, ApprovalRef: payload.ApprovalRef, ApprovalOperationDigest: payload.ApprovalOperationDigest, ApprovalScopeJSON: payload.ApprovalScopeJSON, ApprovalVersionsJSON: payload.ApprovalVersionsJSON, ApprovalConsequence: payload.ApprovalConsequence}); err != nil {
		return err
	}
	productID, err := readWorkflowOverlapResolutionProductTx(ctx, tx, event, payload)
	if err != nil {
		return err
	}
	resolutionFromWorkID, resolutionToWorkID := event.SubjectID, payload.ToWorkID
	if err := invalidateWorkflowOverlapResolutionPairTx(ctx, tx, event.EventID, event.SubjectID, payload.ToWorkID); err != nil {
		return err
	}
	if err := workflowOverlapResolutionConflictsTx(ctx, tx, event, payload); err != nil {
		return err
	}
	if relationKind := workflowOverlapResolutionRelationKind(payload.ResolutionKind); relationKind != "" {
		if cycle, err := relationWouldCycle(ctx, tx, resolutionFromWorkID, resolutionToWorkID, relationKind); err != nil {
			return err
		} else if cycle {
			return cycleFailure("workflow_domain_overlap", "overlap resolution would create a relation cycle", "choose a non-cyclic resolution direction", relationKind, resolutionFromWorkID, resolutionToWorkID)
		}
	}
	return recordWorkflowOverlapResolutionTx(ctx, tx, event, payload, productID, from, to, fromExpected, fromResulting, resolutionFromWorkID, resolutionToWorkID, payload.FromContractVersion, payload.ToContractVersion)
}

// readWorkflowOverlapResolutionWorks reads both endpoint work items and
// validates their expected and resulting versions.
func readWorkflowOverlapResolutionWorks(ctx context.Context, tx *sql.Tx, event Event, payload workflowOverlapResolvedPayload) (workProjection, workProjection, error) {
	from, err := readWork(ctx, tx, event.SubjectID)
	if err != nil {
		return workProjection{}, workProjection{}, err
	}
	to, err := readWork(ctx, tx, payload.ToWorkID)
	if err != nil {
		return workProjection{}, workProjection{}, err
	}
	if err := validateWorkVersion(event.SubjectID, from.version, *payload.ExpectedVersion, *payload.ResultingVersion); err != nil {
		return workProjection{}, workProjection{}, err
	}
	if err := validateWorkVersion(payload.ToWorkID, to.version, payload.ToExpectedVersion, payload.ToResultingVersion); err != nil {
		return workProjection{}, workProjection{}, err
	}
	return from, to, nil
}

// readWorkflowOverlapResolutionProductTx returns the one Product both
// endpoints belong to. Each endpoint must hold the named active contract
// version with an architecture binding in that Product.
func readWorkflowOverlapResolutionProductTx(ctx context.Context, tx *sql.Tx, event Event, payload workflowOverlapResolvedPayload) (string, error) {
	fromProduct, err := readWorkflowOverlapEndpointProductTx(ctx, tx, event.SubjectID, payload.FromContractVersion)
	if err != nil {
		return "", err
	}
	toProduct, err := readWorkflowOverlapEndpointProductTx(ctx, tx, payload.ToWorkID, payload.ToContractVersion)
	if err != nil {
		return "", err
	}
	if fromProduct == "" || toProduct == "" || fromProduct != toProduct {
		return "", overlapResolutionFailure("overlap resolution is not pinned to both current Product contract versions")
	}
	return fromProduct, nil
}

// readWorkflowOverlapEndpointProductTx reads the Product of a nonterminal
// item's active Product-changing contract at contractVersion, and "" when the
// item holds no such single active contract.
func readWorkflowOverlapEndpointProductTx(ctx context.Context, tx *sql.Tx, workID string, contractVersion int64) (string, error) {
	activeVersions, err := activeWorkflowContractVersions(ctx, tx, workID)
	if err != nil {
		return "", err
	}
	if len(activeVersions) != 1 || activeVersions[0] != contractVersion {
		return "", nil
	}
	var productID string
	err = tx.QueryRowContext(ctx, `
		SELECT b.product_id
		FROM workflow_contracts c
		JOIN workflow_architecture_bindings b ON b.work_id=c.work_id AND b.contract_version=c.contract_version
		JOIN work_items w ON w.id=c.work_id
		WHERE c.work_id=? AND c.contract_version=? AND c.superseded_by IS NULL
		  AND w.lifecycle NOT IN ('completed','cancelled','superseded')`, workID, contractVersion).Scan(&productID)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", wrapFailure(KindUnavailable, "workflow_domain_overlap", "cannot read the resolution endpoint contract", true, "retry once the workflow projection is readable", err)
	}
	return productID, nil
}

// workflowOverlapResolutionConflictsTx refuses a second supersession
// successor for the target and a second merger target for the declarer.
func workflowOverlapResolutionConflictsTx(ctx context.Context, tx *sql.Tx, event Event, payload workflowOverlapResolvedPayload) error {
	if payload.ResolutionKind == ResolutionSupersedes {
		var existingSuccessor string
		err := tx.QueryRowContext(ctx, `SELECT work_id_from FROM relations WHERE work_id_to=? AND kind='supersedes'`, payload.ToWorkID).Scan(&existingSuccessor)
		if err == nil {
			return newFailure(KindSupersessionSecondSuccessor, "workflow_domain_overlap", "supersession target already has a direct successor", false, "reopen the target and replace its existing successor explicitly")
		}
		if err != sql.ErrNoRows {
			return wrapFailure(KindUnavailable, "workflow_domain_overlap", "cannot inspect supersession successors", true, "retry once the relation projection is readable", err)
		}
	}
	if payload.ResolutionKind == ResolutionMergedInto {
		var existingTarget string
		err := tx.QueryRowContext(ctx, `SELECT work_id_to FROM relations WHERE work_id_from=? AND kind='merged_into'`, event.SubjectID).Scan(&existingTarget)
		if err == nil {
			return newFailure(KindRelationConflict, "workflow_domain_overlap", "merged work already has a direct target", false, "restore the merged work before choosing another target")
		}
		if err != sql.ErrNoRows {
			return wrapFailure(KindUnavailable, "workflow_domain_overlap", "cannot inspect merger targets", true, "retry once the relation projection is readable", err)
		}
	}
	return nil
}

// recordWorkflowOverlapResolutionTx writes the resolution row, its derived
// relation, the terminal transition a supersession or merger closes with,
// and both endpoints' version bumps.
func recordWorkflowOverlapResolutionTx(ctx context.Context, tx *sql.Tx, event Event, payload workflowOverlapResolvedPayload, productID string, from, to workProjection, fromExpected, fromResulting int64, resolutionFromWorkID, resolutionToWorkID string, resolutionFromContractVersion, resolutionToContractVersion int64) error {
	seq, err := eventSequenceTx(ctx, tx, event.EventID)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_overlap_resolutions(resolution_id,event_seq,product_id,from_work_id,to_work_id,from_contract_version,to_contract_version,resolution_kind,reason,approval_ref,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, event.EventID, seq, productID, resolutionFromWorkID, resolutionToWorkID, resolutionFromContractVersion, resolutionToContractVersion, payload.ResolutionKind, payload.Reason, payload.ApprovalRef, event.OccurredAt.UTC().Format(time.RFC3339Nano)); err != nil {
		return workflowOverlapResolutionProjectionError(err)
	}
	if relationKind := workflowOverlapResolutionRelationKind(payload.ResolutionKind); relationKind != "" {
		if err := insertRelation(ctx, tx, event, relationPayload{From: resolutionFromWorkID, To: resolutionToWorkID, Kind: relationKind, Reason: payload.Reason}); err != nil {
			return err
		}
	}
	if payload.ResolutionKind == ResolutionMergedInto || payload.ResolutionKind == ResolutionSupersedes {
		terminalID := payload.ToWorkID
		if payload.ResolutionKind == ResolutionMergedInto {
			terminalID = event.SubjectID
		}
		terminalWork, terminalVersion := to, payload.ToExpectedVersion
		if terminalID == event.SubjectID {
			terminalWork, terminalVersion = from, fromExpected
		}
		if err := terminalizeWorkflowOverlapWork(ctx, tx, event, terminalID, terminalWork.version, terminalVersion+1); err != nil {
			return err
		}
	}
	if payload.ResolutionKind != ResolutionMergedInto && payload.ResolutionKind != ResolutionSupersedes {
		if err := updateWorkVersionByID(ctx, tx, event.SubjectID, from.version, fromResulting, event.OccurredAt); err != nil {
			return err
		}
		return updateWorkVersionByID(ctx, tx, payload.ToWorkID, to.version, payload.ToResultingVersion, event.OccurredAt)
	}
	otherID, otherWork, otherVersion := payload.ToWorkID, to, payload.ToResultingVersion
	if payload.ResolutionKind == ResolutionSupersedes {
		otherID, otherWork, otherVersion = event.SubjectID, from, fromResulting
	}
	return updateWorkVersionByID(ctx, tx, otherID, otherWork.version, otherVersion, event.OccurredAt)
}

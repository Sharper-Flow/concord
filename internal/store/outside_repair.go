package store

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	OutsideRepairStateActive      = "active"
	OutsideRepairStateCompleted   = "completed"
	OutsideRepairStateResumed     = "resumed"
	RecoveryUseOutsideRepairRoute = "use_outside_repair_route"
	OutsideRepairEvidenceSource   = "boundary_authenticated"
)

// OutsideRepairRequiredCheck records a successful required check on the PR's
// immutable head, not a check name or a caller-supplied success boolean. The
// native identities name the exact check-run, workflow run and job the
// boundary authenticated (CD-0210 D2), so distinct checks inside one run stay
// distinguishable and a later rerun cannot rewrite what the receipt proves.
type OutsideRepairRequiredCheck struct {
	Name       string `json:"name"`
	URL        string `json:"url"`
	CommitSHA  string `json:"commit_sha"`
	Conclusion string `json:"conclusion"`
	CheckRunID int64  `json:"check_run_id"`
	RunID      int64  `json:"run_id"`
	JobID      int64  `json:"job_id"`
}

type OutsideRepairPullRequestEvidence struct {
	URL            string                       `json:"url"`
	Number         int64                        `json:"number"`
	HeadSHA        string                       `json:"head_sha"`
	MergeSHA       string                       `json:"merge_sha"`
	MergedAt       time.Time                    `json:"merged_at"`
	RequiredChecks []OutsideRepairRequiredCheck `json:"required_checks"`
}

// OutsideRepairEvidence is supplied only by the authenticated external boundary.
// That boundary must fetch the merged PRs, the complete required-check set and
// published release, and prove every merge is an ancestor of ReleaseSHA. The
// store validates and preserves the receipt; it does not contact the forge or
// infer ancestry from strings. AuthorityRef and ObservedAt identify the receipt.
type OutsideRepairEvidence struct {
	AuthorityRef string                             `json:"authority_ref"`
	ObservedAt   time.Time                          `json:"observed_at"`
	Repository   string                             `json:"repository"`
	PullRequests []OutsideRepairPullRequestEvidence `json:"pull_requests"`
	ReleaseTag   string                             `json:"release_tag"`
	ReleaseURL   string                             `json:"release_url"`
	ReleaseSHA   string                             `json:"release_sha"`
	PublishedAt  time.Time                          `json:"published_at"`
}

type OutsideRepairDisposition struct {
	WorkID      string                 `json:"work_id"`
	Reason      string                 `json:"reason"`
	ApprovalRef string                 `json:"approval_ref"`
	State       string                 `json:"state"`
	Evidence    *OutsideRepairEvidence `json:"evidence,omitempty"`
	RecordedAt  string                 `json:"recorded_at"`
}

// OutsideRepairApproval binds a consumed core one-use approval. The boundary
// consumes it in the same Transaction as the typed mutation. An opaque token
// alone is insufficient. Replay checks the recorded binding and operator actor.
type OutsideRepairApproval struct {
	ApprovalRef             string `json:"approval_ref"`
	ApprovalOperationDigest string `json:"approval_operation_digest"`
	ApprovalScopeJSON       string `json:"approval_scope_json"`
	ApprovalVersionsJSON    string `json:"approval_versions_json"`
	ApprovalConsequence     string `json:"approval_consequence"`
}

func (a OutsideRepairApproval) binding() workflowApprovalBinding {
	return workflowApprovalBinding{ApprovalRef: a.ApprovalRef, OperationDigest: a.ApprovalOperationDigest, ScopeJSON: a.ApprovalScopeJSON, VersionsJSON: a.ApprovalVersionsJSON, Consequence: a.ApprovalConsequence}
}

// OutsideRepairRequest selects one live work item and one approved mutation.
// A hold needs no completion evidence; it exists before repair starts.
type OutsideRepairRequest struct {
	OutsideRepairApproval
	WorkID          string
	Reason          string
	EventID         string
	ExpectedVersion int64
	OccurredAt      time.Time
}

type OutsideRepairReconcileRequest struct {
	OutsideRepairRequest
	Evidence OutsideRepairEvidence
}

// SetOutsideRepairDispositionTx durably holds needed/in_progress work while
// preserving its workflow step, instance state and historical evidence. Commit
// with TransactDurable; the caller owns approval consumption and idempotency.
func SetOutsideRepairDispositionTx(ctx context.Context, transaction *Transaction, request OutsideRepairRequest) (ApplyOperationResult, error) {
	return applyOutsideRepairMutationTx(ctx, transaction, request, WorkflowOutsideRepairDispositionSet, nil)
}

// ReconcileOutsideRepairTx closes the held work using boundary-authenticated
// external evidence. It emits no workflow.completed, verdict or premise event.
func ReconcileOutsideRepairTx(ctx context.Context, transaction *Transaction, request OutsideRepairReconcileRequest) (ApplyOperationResult, error) {
	if err := validateOutsideRepairEvidenceShape(request.Evidence); err != nil {
		return ApplyOperationResult{}, err
	}
	return applyOutsideRepairMutationTx(ctx, transaction, request.OutsideRepairRequest, WorkflowOutsideRepairReconciled, &request.Evidence)
}

// ResumeOutsideRepairTx clears the hold without changing lifecycle or step.
func ResumeOutsideRepairTx(ctx context.Context, transaction *Transaction, request OutsideRepairRequest) (ApplyOperationResult, error) {
	return applyOutsideRepairMutationTx(ctx, transaction, request, WorkflowOutsideRepairResumed, nil)
}

func applyOutsideRepairMutationTx(ctx context.Context, transaction *Transaction, request OutsideRepairRequest, kind string, evidence *OutsideRepairEvidence) (ApplyOperationResult, error) {
	tx, err := transactionSQL(transaction, "outside_repair")
	if err != nil {
		return ApplyOperationResult{}, err
	}
	if !ValidReference(request.WorkID) || !workflowString(request.Reason, 4096) || !workflowString(request.EventID, 128) || request.ExpectedVersion <= 0 || request.ExpectedVersion > (1<<63-1)-2 {
		return ApplyOperationResult{}, newFailure(KindInvalidPayload, "outside_repair", "work, reason, event identity or version is invalid", false, "supply bounded outside-repair fields")
	}
	operator, err := workflowOperatorFromConsumedApprovalTx(ctx, tx, request.binding(), "outside_repair")
	if err != nil {
		return ApplyOperationResult{}, err
	}
	if request.OccurredAt.IsZero() {
		request.OccurredAt = transaction.now()
	}
	version := request.ExpectedVersion
	actor := workflowTypedEvent(request.EventID+":operator", WorkflowActorRecorded, request.WorkID, operator.ref, request.OccurredAt, version, map[string]any{
		"actor_ref": operator.ref, "principal_ref": operator.PrincipalRef, "client_ref": operator.ClientRef,
		"agent_ref": operator.AgentRef, "session_ref": operator.SessionRef, "actor_class": string(ActorOperator),
	})
	state := OutsideRepairStateActive
	if kind == WorkflowOutsideRepairReconciled {
		state = OutsideRepairStateCompleted
	}
	if kind == WorkflowOutsideRepairResumed {
		state = OutsideRepairStateResumed
	}
	payload := map[string]any{
		"reason": request.Reason, "state": state,
		"approval_ref": request.ApprovalRef, "approval_operation_digest": request.ApprovalOperationDigest,
		"approval_scope_json": request.ApprovalScopeJSON, "approval_versions_json": request.ApprovalVersionsJSON,
		"approval_consequence": request.ApprovalConsequence,
	}
	if evidence != nil {
		payload["evidence"] = evidence
		payload["evidence_source"] = OutsideRepairEvidenceSource
	}
	event := workflowTypedEvent(request.EventID, kind, request.WorkID, operator.ref, request.OccurredAt, version+1, payload)
	scope := transaction.fold
	if scope == nil {
		scope = newFoldScope(tx)
	}
	return applyWorkflowOperationTx(ctx, tx, Operation{Events: []Event{actor, event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, request.WorkID): version}}, scope)
}

func outsideRepairGitSHA(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil && value == strings.ToLower(value)
}

func outsideRepairURL(value, repository, suffix string) bool {
	u, err := url.Parse(value)
	return err == nil && u.Scheme == "https" && u.Host == "github.com" && u.User == nil && u.RawQuery == "" && u.Fragment == "" && u.Path == "/"+repository+suffix
}

func validateOutsideRepairEvidenceShape(e OutsideRepairEvidence) error {
	return validateOutsideRepairEvidenceShapeWithReference(e, ValidReference)
}

func validateOutsideRepairEvidenceShapeForReplay(e OutsideRepairEvidence) error {
	return validateOutsideRepairEvidenceShapeWithReference(e, replayValidReference)
}

func validateOutsideRepairEvidenceShapeWithReference(e OutsideRepairEvidence, validReference func(string) bool) error {
	invalid := func(detail string) error {
		return newFailure(KindInvalidPayload, "outside_repair", detail, false, "supply the authenticated merged PR, required checks and published release receipt")
	}
	parts := strings.Split(e.Repository, "/")
	if len(parts) != 2 || !workflowString(e.Repository, 256) || !validReference(parts[0]) || !validReference(parts[1]) || strings.ContainsAny(e.Repository, "?#%\\ ") {
		return invalid("outside repair repository must be owner/repo")
	}
	if !validReference(e.AuthorityRef) || e.ObservedAt.IsZero() || e.PublishedAt.IsZero() || e.ObservedAt.Before(e.PublishedAt) {
		return invalid("outside repair requires an authenticated authority and publication/observation timestamps")
	}
	if !workflowString(e.ReleaseTag, 128) || !outsideRepairGitSHA(e.ReleaseSHA) || !outsideRepairURL(e.ReleaseURL, e.Repository, "/releases/tag/"+e.ReleaseTag) {
		return invalid("outside repair requires a released tag, canonical release URL and native git SHA")
	}
	if len(e.PullRequests) == 0 || len(e.PullRequests) > 32 {
		return invalid("outside repair must contain 1..32 merged pull requests")
	}
	seen := map[int64]bool{}
	for _, pr := range e.PullRequests {
		if pr.Number <= 0 || seen[pr.Number] || !outsideRepairURL(pr.URL, e.Repository, fmt.Sprintf("/pull/%d", pr.Number)) || !outsideRepairGitSHA(pr.HeadSHA) || !outsideRepairGitSHA(pr.MergeSHA) || pr.MergedAt.IsZero() || pr.MergedAt.After(e.PublishedAt) {
			return invalid("outside repair pull request identity, merge SHA or merge timestamp is invalid or duplicated")
		}
		seen[pr.Number] = true
		if len(pr.RequiredChecks) == 0 || len(pr.RequiredChecks) > 64 {
			return invalid("outside repair requires 1..64 required check results per pull request")
		}
		names := map[string]bool{}
		checkRuns := map[int64]bool{}
		jobs := map[int64]bool{}
		for _, check := range pr.RequiredChecks {
			u, err := url.Parse(check.URL)
			if !workflowString(check.Name, 128) || names[check.Name] || check.CommitSHA != pr.HeadSHA || check.Conclusion != "success" || err != nil || u.Scheme != "https" || u.Host != "github.com" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || !strings.HasPrefix(u.Path, "/"+e.Repository+"/actions/runs/") || !workflowString(check.URL, 1024) {
				return invalid("outside repair required check is not a unique successful result on the PR head")
			}
			// The native identities must ride the receipt and bind it: the
			// run the URL names is the run the identity records, the job the
			// URL names (when it names one) is the recorded job, and one
			// check-run or job cannot prove two required checks.
			rest := strings.TrimPrefix(u.Path, "/"+e.Repository+"/actions/runs/")
			runPart, jobPart, hasJob := strings.Cut(rest, "/")
			if check.CheckRunID <= 0 || check.RunID <= 0 || check.JobID <= 0 || runPart != strconv.FormatInt(check.RunID, 10) {
				return invalid("outside repair required check lacks its exact native check-run, run and job identity")
			}
			if hasJob && jobPart != "job/"+strconv.FormatInt(check.JobID, 10) {
				return invalid("outside repair required check job identity disagrees with its URL")
			}
			if checkRuns[check.CheckRunID] || jobs[check.JobID] {
				return invalid("outside repair required checks reuse one native check-run or job identity")
			}
			checkRuns[check.CheckRunID] = true
			jobs[check.JobID] = true
			names[check.Name] = true
		}
	}
	return nil
}

var outsideRepairApprovalSubject = workflowApprovalBindingSubject{operation: "outside repair", actorHint: "use the approved outside-repair boundary", freshHint: "request approval for the exact outside-repair operation"}

func validateOutsideRepairFold(ctx context.Context, tx *sql.Tx, event Event, p workflowOutsideRepairDispositionPayload, state string) (workProjection, error) {
	if err := workflowBase(event, p.WorkflowVersionFields); err != nil {
		return workProjection{}, err
	}
	if p.State != state || !workflowString(p.Reason, 4096) {
		return workProjection{}, newFailure(KindInvalidPayload, "outside_repair", "invalid disposition state or reason", false, "use the typed outside-repair route")
	}
	if err := authorizeWorkflowOperatorApprovalTx(ctx, tx, event, p.binding(), outsideRepairApprovalSubject); err != nil {
		return workProjection{}, err
	}
	work, err := readWork(ctx, tx, event.SubjectID)
	if err != nil {
		return work, err
	}
	if work.lifecycle != "needed" && work.lifecycle != "in_progress" {
		return work, newFailure(KindIllegalLifecycleTransition, "outside_repair", "outside repair requires needed or in_progress work", false, "select live work")
	}
	kind, err := readWorkKind(ctx, tx, event.SubjectID)
	if err != nil {
		return work, err
	}
	if WorkKindRequiresDedicatedOperation(kind) {
		return work, newFailure(KindInvalidOperation, "outside_repair", "outside repair cannot close an initiative", false, "select the bounded repair work item")
	}
	return work, nil
}

func foldOutsideRepairDispositionSet(ctx context.Context, tx *sql.Tx, event Event) error {
	var p workflowOutsideRepairDispositionPayload
	if err := decodeWorkflowPayload(event, &p); err != nil {
		return err
	}
	if _, err := validateOutsideRepairFold(ctx, tx, event, p, OutsideRepairStateActive); err != nil {
		return err
	}
	if active, err := outsideRepairActiveTx(ctx, tx, event.SubjectID); err != nil {
		return err
	} else if active {
		return newOutsideRepairRouteFailure("outside_repair", "an outside-repair hold is already active")
	}
	if err := requireOutsideRepairExecutionStoppedTx(ctx, tx, event.SubjectID); err != nil {
		return err
	}
	if err := advanceWorkflowVersion(ctx, tx, event, p.WorkflowVersionFields); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE workflow_instances SET outside_repair_state=? WHERE work_id=?`, OutsideRepairStateActive, event.SubjectID); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO outside_repair_dispositions(work_id,approval_ref,state,reason,evidence_json,recorded_at) VALUES(?,?,?,?,'{}',?) ON CONFLICT(work_id) DO UPDATE SET approval_ref=excluded.approval_ref,state=excluded.state,reason=excluded.reason,evidence_json=excluded.evidence_json,recorded_at=excluded.recorded_at`, event.SubjectID, p.ApprovalRef, p.State, p.Reason, event.OccurredAt.UTC().Format(time.RFC3339Nano))
	return err
}

func foldOutsideRepairReconciled(ctx context.Context, tx *sql.Tx, event Event, scope *foldScope) error {
	var p workflowOutsideRepairReconcilePayload
	if err := decodeWorkflowPayload(event, &p); err != nil {
		return err
	}
	work, err := validateOutsideRepairFold(ctx, tx, event, p.workflowOutsideRepairDispositionPayload, OutsideRepairStateCompleted)
	if err != nil {
		return err
	}
	if p.EvidenceSource != OutsideRepairEvidenceSource {
		return newFailure(KindInvalidPayload, "outside_repair", "reconciliation requires boundary-authenticated provenance", false, "authenticate external evidence at the owning boundary")
	}
	if err := validateOutsideRepairEvidenceShapeForReplay(p.Evidence); err != nil {
		return err
	}
	if err := requireOutsideRepairActiveTx(ctx, tx, event.SubjectID); err != nil {
		return err
	}
	if err := requireOutsideRepairExecutionStoppedTx(ctx, tx, event.SubjectID); err != nil {
		return err
	}
	if scope == nil || scope.tx != tx || scope.depth == 0 {
		return newFailure(KindInvalidOperation, "outside_repair", "reconciliation requires its owning fold scope", false, "use the typed outside-repair reconciliation")
	}
	previous := scope.outsideRepairLifecycleEvent
	scope.outsideRepairLifecycleEvent = &event
	defer func() { scope.outsideRepairLifecycleEvent = previous }()
	raw, err := json.Marshal(p.Evidence)
	if err != nil {
		return err
	}
	// This typed fold owns external completion. It does not invoke the ordinary
	// workflow gate or invent the workflow evidence that gate would require.
	if err := validateWorkVersion(event.SubjectID, work.version, *p.ExpectedVersion, *p.ResultingVersion); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE workflow_instances SET outside_repair_state=?,instance_state='outside_repair',completed_at=? WHERE work_id=?`, p.State, event.OccurredAt.UTC().Format(time.RFC3339Nano), event.SubjectID); err != nil {
		return err
	}
	if err := updateWorkLifecycle(ctx, tx, event, "completed", work.version, *p.ResultingVersion, scope); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE outside_repair_dispositions SET state=?,evidence_json=? WHERE work_id=?`, p.State, string(raw), event.SubjectID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO outside_repair_reconciliations(reconciliation_id,work_id,approval_ref,reason,evidence_json,recorded_at) VALUES(?,?,?,?,?,?)`, event.EventID, event.SubjectID, p.ApprovalRef, p.Reason, string(raw), event.OccurredAt.UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	if err := removeTerminalResearchBindings(ctx, tx, event.SubjectID, event.OccurredAt); err != nil {
		return err
	}
	return foldTerminalReleasesResourceClaims(ctx, tx, event)
}

func foldOutsideRepairResumed(ctx context.Context, tx *sql.Tx, event Event) error {
	var p workflowOutsideRepairResumePayload
	if err := decodeWorkflowPayload(event, &p); err != nil {
		return err
	}
	if _, err := validateOutsideRepairFold(ctx, tx, event, p, OutsideRepairStateResumed); err != nil {
		return err
	}
	if err := requireOutsideRepairActiveTx(ctx, tx, event.SubjectID); err != nil {
		return err
	}
	if err := advanceWorkflowVersion(ctx, tx, event, p.WorkflowVersionFields); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE workflow_instances SET outside_repair_state=NULL WHERE work_id=?`, event.SubjectID); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `UPDATE outside_repair_dispositions SET state=? WHERE work_id=?`, p.State, event.SubjectID)
	return err
}

func requireOutsideRepairActiveTx(ctx context.Context, tx *sql.Tx, workID string) error {
	active, err := outsideRepairActiveTx(ctx, tx, workID)
	if err != nil {
		return err
	}
	if !active {
		return newFailure(KindInvalidOperation, "outside_repair", "no outside-repair hold is active", false, "record an operator-approved hold first")
	}
	return nil
}

func requireNoOutsideRepairTx(ctx context.Context, q queryer, workID, op string) error {
	active, err := outsideRepairActiveTx(ctx, q, workID)
	if err != nil {
		return err
	}
	if active {
		return newOutsideRepairRouteFailure(op, "managed effect refused: outside-repair disposition is active")
	}
	return nil
}

// An outside hold cannot strand a worker or an unfinished operation. Failed
// and completed attempts are terminal evidence, not execution blockers.
func requireOutsideRepairExecutionStoppedTx(ctx context.Context, tx *sql.Tx, workID string) error {
	if isWorkflowReplay(ctx) {
		return nil
	}
	stopped, err := workExecutionStoppedTx(ctx, tx, workID, "")
	if err != nil {
		return err
	}
	if stopped {
		_, err = pendingOperationForWork(ctx, tx, workID)
		var failure *Failure
		if failureAs(err, &failure) && failure.Kind == KindProjectionNotFound {
			return nil
		}
		if err != nil {
			return err
		}
	}
	return newFailure(KindNotTerminal, "outside_repair", "work has an outstanding worker attempt or workflow operation", false, "settle the existing execution through its declared terminal route before outside repair")
}

func newOutsideRepairRouteFailure(op, detail string) *Failure {
	f := newFailure(KindOutsideRepairActive, op, detail, false, RecoveryUseOutsideRepairRoute)
	f.RecoveryRefs = outsideRepairRouteNames()
	return f
}

func outsideRepairRouteNames() []string {
	return []string{"outside_repair_reconcile"}
}

func outsideRepairActiveTx(ctx context.Context, q queryer, workID string) (bool, error) {
	var count int
	if err := q.QueryRowContext(ctx, `SELECT count(*) FROM outside_repair_dispositions WHERE work_id=? AND state='active'`, workID).Scan(&count); err != nil {
		return false, wrapFailure(KindUnavailable, "outside_repair", "cannot read the outside-repair hold", true, "retry once the projection is readable", err)
	}
	return count != 0, nil
}

func outsideRepairDispositionTx(ctx context.Context, tx *sql.Tx, workID string) (*OutsideRepairDisposition, error) {
	var p OutsideRepairDisposition
	var raw string
	err := tx.QueryRowContext(ctx, `SELECT approval_ref,state,reason,evidence_json,recorded_at FROM outside_repair_dispositions WHERE work_id=?`, workID).Scan(&p.ApprovalRef, &p.State, &p.Reason, &raw, &p.RecordedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, workflowProjectionError(err, "cannot read outside-repair disposition")
	}
	p.WorkID = workID
	if p.State == OutsideRepairStateCompleted {
		p.Evidence = &OutsideRepairEvidence{}
		if err := json.Unmarshal([]byte(raw), p.Evidence); err != nil {
			return nil, workflowProjectionError(err, "cannot decode outside-repair evidence")
		}
	}
	return &p, nil
}

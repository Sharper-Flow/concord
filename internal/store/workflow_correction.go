package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// workflowCompleteStepCorrectionRoute is the reserved contract route
// convention a successor declares when the operator approved it at the pinned
// complete step. It is the durable marker that cuts predecessor verdicts off
// the successor's predicates: the route exists because durable evidence
// disproved the predecessor's premise, so the verdicts that predecessor
// carried hold no authority, however identical the predicate payloads remain.
const workflowCompleteStepCorrectionRoute = "complete_step_correction"

func workflowFoldCorrectionReturnTarget(entry RegisteredDefinition, currentStep string) (string, error) {
	target := workflowCorrectionReturnTarget(entry.Definition, currentStep)
	if target == "" {
		return "", newFailure(KindIllegalLifecycleTransition, "fold_event", "correction has no declared recovery route to a producer step", false, "repair the pinned workflow definition")
	}
	return target, nil
}

// workflowCompleteStepCorrectionStep reports whether the pinned step declares
// the CD-0172 complete-step correction route: a disproved_premise_at_complete
// recovery route whose target is the producer the corrected contract returns
// to. Only the implementation and break-fix tables declare the trigger, and
// registration seats it on a step that declares the complete action, so the
// resolved route is both the shape test and the return's one owner.
func workflowCompleteStepCorrectionStep(definition WorkflowDefinition, currentStep string) bool {
	return workflowDisprovedPremiseAtCompleteRouteTarget(definition, currentStep) != ""
}

// workflowCompletedInstanceSupersedeOffShape reports a completed workflow
// instance whose pinned shape does not carry the complete-step correction
// step. That step is the only shape whose supersession admission reopens the
// instance, so on any other shape a completed instance keeps every recovery
// route closed: a supersession there folds without the return that reopens
// the work.
func workflowCompletedInstanceSupersedeOffShape(state string, definition WorkflowDefinition, currentStep string) bool {
	return state == "completed" && !workflowCompleteStepCorrectionStep(definition, currentStep)
}

// workflowCompletedInstanceOffShapeFailure is the refusal every admission
// surface names for a completed instance off the supported shape, so a
// divergence between the surfaces cannot reopen the route.
func workflowCompletedInstanceOffShapeFailure(subject string) *Failure {
	return newFailure(KindInvalidOperation, subject, "a completed workflow instance supersedes its contract only on the pinned complete-step correction shape", false, "start a successor workflow")
}

// workflowCompleteStepCorrectionEvidenceCutoff returns the domain-event
// sequence of the latest supersession in contractVersion's ancestry that
// produced a contract declaring the reserved complete-step correction route,
// and zero when the ancestry declares none. The cutoff persists across later
// ordinary successors: once a correction cut the predecessor's verdicts and
// evidence off, no descendant reads them again — only fresh post-cutoff
// evidence and verdicts re-establish a contract. Evidence bound at or before
// the cutoff carried the disproved premise, so the evidence-requirement
// surfaces accept only bindings recorded after it.
func workflowCompleteStepCorrectionEvidenceCutoff(ctx context.Context, q queryer, workID string, contractVersion int64) (int64, error) {
	rows, err := q.QueryContext(ctx, `SELECT contract_version,route_conventions FROM workflow_contracts WHERE work_id=? AND contract_version<=? ORDER BY contract_version DESC`, workID, contractVersion)
	if err != nil {
		return 0, wrapFailure(KindUnavailable, "workflow_correction", "cannot read the workflow contract ancestry", true, "retry once the workflow projection is readable", err)
	}
	routeVersion := int64(0)
	for rows.Next() {
		var version int64
		var routesJSON string
		if err := rows.Scan(&version, &routesJSON); err != nil {
			rows.Close()
			return 0, wrapFailure(KindUnavailable, "workflow_correction", "cannot scan the workflow contract ancestry", true, "retry once the workflow projection is readable", err)
		}
		var routes []string
		if json.Unmarshal([]byte(routesJSON), &routes) == nil && slices.Contains(routes, workflowCompleteStepCorrectionRoute) {
			routeVersion = version
			break
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, wrapFailure(KindUnavailable, "workflow_correction", "cannot enumerate the workflow contract ancestry", true, "retry once the workflow projection is readable", err)
	}
	if err := rows.Close(); err != nil {
		return 0, wrapFailure(KindUnavailable, "workflow_correction", "cannot close the workflow contract ancestry", true, "retry once the workflow projection is readable", err)
	}
	if routeVersion == 0 {
		return 0, nil
	}
	var seq int64
	if err := q.QueryRowContext(ctx, `SELECT supersession_seq FROM workflow_contracts WHERE work_id=? AND contract_version=?`, workID, routeVersion).Scan(&seq); err != nil {
		return 0, wrapFailure(KindUnavailable, "workflow_correction", "cannot read the complete-step correction cutoff", true, "retry once the workflow projection is readable", err)
	}
	if seq == 0 {
		return 0, newFailure(KindInvariantViolation, "workflow_correction", "complete-step correction contract has no supersession event", false, "restore a verified backup into a new path with concord restore, then swap")
	}
	return seq, nil
}

// WorkflowCorrectionContext is the bounded correction record projected for a
// work pin and for the next worker packet.
type WorkflowCorrectionContext struct {
	Disposition        string   `json:"disposition"`
	PredicateIDs       []string `json:"predicate_ids"`
	EvidenceRefs       []string `json:"evidence_refs"`
	Diagnosis          string   `json:"diagnosis,omitempty"`
	Strategy           string   `json:"strategy,omitempty"`
	FailureKind        string   `json:"failure_kind,omitempty"`
	FailureDetail      string   `json:"failure_detail,omitempty"`
	FailedAttemptID    string   `json:"failed_attempt_id,omitempty"`
	FailedAttemptEpoch int64    `json:"failed_attempt_epoch,omitempty"`
	// SourceEventID and SourceEventSeq name the durable failure, rejection,
	// or correction-request event that opened this correction. The diagnosis
	// and strategy are that event's directions; the identity lets a packet
	// consumer qualify them by source and lets admission refuse a packet
	// built from a different correction record carrying equal prose.
	SourceEventID  string `json:"source_event_id"`
	SourceEventSeq int64  `json:"source_event_seq"`
}

// validateFailedWorkerRetryIdentity refuses a retry that reuses the failed
// worker identity. The dispatch fold mints the next step epoch separately.
func validateFailedWorkerRetryIdentity(ctx context.Context, q queryer, workID, currentStep string, payload json.RawMessage) error {
	correction, err := workflowCorrectionContextForDispatch(ctx, q, workID, currentStep, "")
	if err != nil {
		return err
	}
	if correction == nil || correction.FailedAttemptID == "" {
		return nil
	}
	fields, err := workflowActionObject(payload)
	if err != nil {
		return err
	}
	attemptID := workflowFieldStringDefault(fields, "attempt_id", "")
	if attemptID == "" || attemptID == correction.FailedAttemptID {
		return newFailure(KindStaleAttempt, "workflow_action", "worker retry must use a fresh attempt identity", false, "mint a new fenced worker attempt")
	}
	return nil
}

func workflowCorrectionActionDefinition() WorkflowActionDefinition {
	action := currentActionDefinition("reject_worker_result", true)
	action.RequiredCapability = "work_transition"
	return action
}

// validateRejectCorrectionPinValues refuses a reject whose correction values
// the closed response schema cannot represent. The request-level evidence
// refs are not payload fields, so the payload declaration alone cannot cover
// every value the correction pin will carry; the apply boundary checks the
// full set here before any effect is recorded.
func validateRejectCorrectionPinValues(payload json.RawMessage, evidenceRefs []string) error {
	fields, err := workflowActionObject(payload)
	if err != nil {
		return err
	}
	if err := validateWorkflowOpenFindingsPayload(fields); err != nil {
		return err
	}
	fault := workflowCorrectionSchemaValuesFaultForAdmission(workflowFieldStrings(fields, "predicate_ids"), workflowFieldStrings(fields, "evidence_refs"), evidenceRefs)
	if fault == "" {
		return nil
	}
	return newFailure(KindInvalidPayload, "workflow_action", "reject_worker_result carries correction values the closed response schema cannot represent: "+fault, false, "supply correction values the closed work_pin response schema accepts")
}

func workflowCorrectionRequestActionDefinition() WorkflowActionDefinition {
	return workTransitionAction(currentActionDefinition("request_correction", true))
}

// workflowCorrectionRouteStrategy renders the strategy line the correction
// contexts carry: the declared route's producer step is where the artifact is
// re-produced. The target comes from the one return-target reader — the
// step's own route, the served verdict step's route at a gate or a
// late-verdict step — so the pin, the fold, and the dispatched packet name
// the same step.
func workflowCorrectionRouteStrategy(definition WorkflowDefinition, currentStep string) string {
	return fmt.Sprintf("return to the producer step %q and re-produce the artifact", workflowCorrectionReturnTarget(definition, currentStep))
}

// workflowAcceptedWorkerDelivery requires both a completed worker attempt and
// its folded accept action before a verdict can request correction. Either
// accept action binds the delivery: accept_worker_result accepts a delivered
// result, and accept_worker_evidence binds the evidence a review dispatch
// delivered at its confirmation step.
func workflowAcceptedWorkerDelivery(ctx context.Context, q queryer, workID string, throughSeq int64, subject string) (int64, bool, error) {
	var dispatchSeq int64
	var attemptID string
	if err := q.QueryRowContext(ctx, `SELECT d.seq,json_extract(d.payload,'$.attempt_id') FROM domain_events d WHERE d.subject_type=? AND d.subject_id=? AND d.kind=? AND d.seq<=? ORDER BY d.seq DESC LIMIT 1`, string(SubjectWorkItem), workID, WorkerDispatched, throughSeq).Scan(&dispatchSeq, &attemptID); err != nil {
		if err == sql.ErrNoRows {
			return 0, false, nil
		}
		return 0, false, wrapFailure(KindUnavailable, subject, "cannot inspect worker delivery", true, "retry once the worker delivery projection is readable", err)
	}
	var accepted int
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM worker_attempts a JOIN domain_events accepted ON accepted.subject_type=? AND accepted.subject_id=a.work_id AND accepted.kind=? AND accepted.seq>? AND accepted.seq<=? AND json_extract(accepted.payload,'$.action_id') IN ('accept_worker_result','accept_worker_evidence') AND json_extract(accepted.payload,'$.worker_attempt_id')=a.attempt_id WHERE a.work_id=? AND a.attempt_id=? AND a.lifecycle_state='completed')`, string(SubjectWorkItem), WorkflowActionCompleted, dispatchSeq, throughSeq, workID, attemptID).Scan(&accepted); err != nil {
		return 0, false, wrapFailure(KindUnavailable, subject, "cannot inspect accepted worker delivery", true, "retry once the worker delivery projection is readable", err)
	}
	return dispatchSeq, accepted != 0, nil
}

// workflowLatestComparableHealthySequence returns the latest sequence before
// beforeSeq at which every active predicate had a comparable healthy verdict.
// A single ok verdict cannot reset a sequence when another predicate remains
// unhealthy or has no verdict.
func workflowLatestComparableHealthySequence(ctx context.Context, q queryer, workID string, contractVersion, beforeSeq int64) (int64, error) {
	rows, err := q.QueryContext(ctx, `SELECT predicate_id FROM workflow_contract_predicates WHERE work_id=? AND contract_version=?`, workID, contractVersion)
	if err != nil {
		return 0, wrapFailure(KindUnavailable, "workflow_correction", "cannot read active workflow predicates", true, "retry once the workflow contract is readable", err)
	}
	predicates := make(map[string]bool)
	for rows.Next() {
		var predicateID string
		if err := rows.Scan(&predicateID); err != nil {
			rows.Close()
			return 0, wrapFailure(KindUnavailable, "workflow_correction", "cannot scan active workflow predicate", true, "retry once the workflow contract is readable", err)
		}
		predicates[predicateID] = true
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, wrapFailure(KindUnavailable, "workflow_correction", "cannot enumerate active workflow predicates", true, "retry once the workflow contract is readable", err)
	}
	if err := rows.Close(); err != nil {
		return 0, wrapFailure(KindUnavailable, "workflow_correction", "cannot close active workflow predicates", true, "retry once the workflow contract is readable", err)
	}
	if len(predicates) == 0 {
		return 0, nil
	}
	history, err := workflowContractPredicateHistory(ctx, q, workID, contractVersion)
	if err != nil {
		return 0, err
	}
	pins, err := workflowContractDefinitionPins(ctx, q, workID, contractVersion)
	if err != nil {
		return 0, err
	}
	type verdictAtSequence struct {
		seq     int64
		verdict workflowVerdictRecordedPayload
	}
	latest := make(map[string]verdictAtSequence, len(predicates))
	verdictRows, err := q.QueryContext(ctx, `SELECT seq,payload FROM domain_events WHERE subject_type='work_item' AND subject_id=? AND kind=? AND seq<? ORDER BY seq DESC`, workID, WorkflowVerdictRecorded, beforeSeq)
	if err != nil {
		return 0, wrapFailure(KindUnavailable, "workflow_correction", "cannot read workflow verdict history", true, "retry once the workflow verdict projection is readable", err)
	}
	defer verdictRows.Close()
	for verdictRows.Next() {
		var seq int64
		var raw []byte
		if err := verdictRows.Scan(&seq, &raw); err != nil {
			return 0, wrapFailure(KindUnavailable, "workflow_correction", "cannot scan workflow verdict history", true, "retry once the workflow verdict projection is readable", err)
		}
		var verdict workflowVerdictRecordedPayload
		if err := json.Unmarshal(raw, &verdict); err != nil {
			return 0, newFailure(KindInvariantViolation, "workflow_correction", "workflow verdict payload is malformed", false, "restore a verified backup into a new path with concord restore, then swap")
		}
		if !predicates[verdict.PredicateID] || verdict.ContractVersion <= 0 || verdict.ContractVersion > contractVersion {
			continue
		}
		if verdict.ContractVersion < contractVersion && (!workflowPredicateHistoryCompatible(history, verdict.ContractVersion, contractVersion, verdict.PredicateID, verdict) || !workflowContractDefinitionPinsCompatible(pins, verdict.ContractVersion, contractVersion)) {
			continue
		}
		latest[verdict.PredicateID] = verdictAtSequence{seq: seq, verdict: verdict}
		healthy := len(latest) == len(predicates)
		if healthy {
			var healthySeq int64
			for predicateID := range predicates {
				candidate := latest[predicateID]
				if candidate.verdict.VerdictKind != "ok" || candidate.verdict.IncomparableWithApproved {
					healthy = false
					break
				}
				if candidate.seq > healthySeq {
					healthySeq = candidate.seq
				}
			}
			if healthy {
				return healthySeq, nil
			}
		}
	}
	if err := verdictRows.Err(); err != nil {
		return 0, wrapFailure(KindUnavailable, "workflow_correction", "cannot scan workflow verdict history", true, "retry once the workflow verdict projection is readable", err)
	}
	return 0, nil
}

// workflowCorrectionHealthyBaseline returns the sequence a correction request
// window opens at: the later of the latest comparable-healthy verdict set
// under contractVersion (CD-0143 D3's sequence-ending state) and the
// contract's complete-step supersession cutoff. An ok verdict that is
// incomparable with the approved result, or an ok verdict beside another
// predicate's stale verdict, is not a comparable-healthy set and ends neither
// health nor the counted window, so every counting surface derives its
// baseline here.
func workflowCorrectionHealthyBaseline(ctx context.Context, q queryer, workID string, contractVersion, beforeSeq int64) (int64, error) {
	lastHealthySeq, err := workflowLatestComparableHealthySequence(ctx, q, workID, contractVersion, beforeSeq)
	if err != nil {
		return 0, err
	}
	cutoff, err := workflowCompleteStepCorrectionEvidenceCutoff(ctx, q, workID, contractVersion)
	if err != nil {
		return 0, err
	}
	if lastHealthySeq < cutoff {
		lastHealthySeq = cutoff
	}
	return lastHealthySeq, nil
}

// workflowCorrectionAcceptanceBefore finds the satisfied-obligation acceptance
// preceding a checkpoint's failure. The in-flight completion is excluded.
func workflowCorrectionAcceptanceBefore(ctx context.Context, q queryer, workID string, seq int64, subject string) (int64, error) {
	if seq == 0 {
		seq = int64(1) << 62
	}
	history, err := workflowCorrectionWalkWorkerSatisfaction(ctx, q, workID, seq-1, subject)
	if err != nil {
		return 0, err
	}
	return history.boundary, nil
}

// workflowCorrectionWalkWorkerSatisfaction reads current obligation state.
// Folds place their update before or after admission to preserve self-exclusion.
func workflowCorrectionWalkWorkerSatisfaction(ctx context.Context, q queryer, workID string, _ int64, subject string) (*workflowCorrectionHistory, error) {
	return readWorkflowCorrectionFacts(ctx, q, workID, subject)
}

func readWorkflowCorrectionStateForMigration(ctx context.Context, q queryer, workID string, prefix int64, subject string) (*workflowCorrectionHistory, error) {
	rows, err := q.QueryContext(ctx, `SELECT e.seq, e.kind, COALESCE(json_extract(e.payload,'$.attempt_id'),''), COALESCE(json_extract(e.payload,'$.worker_attempt_id'),''), COALESCE(json_extract(e.payload,'$.action_id'),''), COALESCE(json_extract(e.payload,'$.worker_job.job_id'),''), COALESCE(json_extract(e.payload,'$.worker_job.revision'),0), COALESCE(json_extract(e.payload,'$.worker_job.digest'),''), e.payload, e.event_id FROM domain_events e WHERE e.subject_type=? AND e.subject_id=? AND e.seq<=? ORDER BY e.seq`, string(SubjectWorkItem), workID, prefix)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, subject, "cannot read the correction window history", true, "retry once the workflow event log is readable", err)
	}
	history := &workflowCorrectionHistory{
		unresolved:        make(map[string]bool),
		dispatchJob:       make(map[string]string),
		materialized:      make(map[string]bool),
		obligations:       make(map[string]workflowJobObligationState),
		blockingReview:    make(map[string]bool),
		reviewDisposition: make(map[string]string),
		checksRepairs:     make(map[string]*workflowChecksRepairState),
	}
	for rows.Next() {
		var row workflowCorrectionHistoryRow
		if err := rows.Scan(&row.seq, &row.kind, &row.dispatchAttempt, &row.actionAttempt, &row.actionID, &row.jobID, &row.jobRevision, &row.jobDigest, &row.payload, &row.eventID); err != nil {
			_ = rows.Close()
			return nil, wrapFailure(KindUnavailable, subject, "cannot scan the correction window history", true, "retry once the workflow event log is readable", err)
		}
		if err := history.observe(row, subject); err != nil {
			_ = rows.Close()
			return nil, err
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, wrapFailure(KindUnavailable, subject, "cannot scan the correction window history", true, "retry once the workflow event log is readable", err)
	}
	if err := rows.Close(); err != nil {
		return nil, wrapFailure(KindUnavailable, subject, "cannot close the correction window history", true, "retry once the workflow event log is readable", err)
	}
	return history, nil
}

type workflowCorrectionHistory struct {
	boundary       int64
	unresolved     map[string]bool
	dispatchJob    map[string]string
	materialized   map[string]bool
	obligations    map[string]workflowJobObligationState
	blockingReview map[string]bool
	// reviewDisposition keeps each attempt's recorded Review disposition, and
	// checksRepairs is the one walked owner of the checks repair state
	// (CD-0205 D4): each recorded repair's binding, named obligations,
	// parent contract, its own acceptance, the exact confirming dispatch
	// its admission marker names, and the confirm or void that ended it.
	reviewDisposition map[string]string
	checksRepairs     map[string]*workflowChecksRepairState
	records           []workflowCorrectionRecordState
	completions       []workflowCorrectionDispatchCompletion
}

// workflowChecksRepairState is one recorded checks repair's state at the
// walked prefix: recorded under Binding with its parent contract, accepted
// through its own repairing result, dispatched for confirmation through the
// one attempt its admission marker names, and ended confirmed or voided.
type workflowChecksRepairState struct {
	binding         WorkerJobBinding
	namedKeys       []string
	contractVersion int64
	acceptanceSeq   int64
	confirmAttempt  string
	confirmed       bool
	voided          bool
}

// confirmingAttemptOf finds the repair whose confirming review the
// attempt dispatched, if any.
func (h *workflowCorrectionHistory) confirmingAttemptOf(attempt string) *workflowChecksRepairState {
	for _, repair := range h.checksRepairs {
		if repair.confirmAttempt == attempt {
			return repair
		}
	}
	return nil
}

// voidConfirmingRepair ends an unconfirmed repair whose confirming attempt
// cannot validate it.
func (h *workflowCorrectionHistory) voidConfirmingRepair(attempt string) {
	repair := h.confirmingAttemptOf(attempt)
	if repair == nil || repair.confirmed {
		return
	}
	repair.voided = true
}

type workflowCorrectionHistoryRow struct {
	seq             int64
	kind            string
	dispatchAttempt string
	actionAttempt   string
	actionID        string
	jobID           string
	jobRevision     int64
	jobDigest       string
	payload         string
	eventID         string
}

func (h *workflowCorrectionHistory) observe(row workflowCorrectionHistoryRow, subject string) error {
	switch row.kind {
	case string(WorkflowContractSuperseded):
		var successor struct {
			ContractVersion int64 `json:"new_contract_version"`
		}
		if err := json.Unmarshal([]byte(row.payload), &successor); err != nil {
			return newFailure(KindInvariantViolation, subject, "contract supersession payload is malformed", false, "restore a verified backup into a new path with concord restore, then swap")
		}
		for _, repair := range h.checksRepairs {
			if !repair.confirmed && repair.contractVersion != successor.ContractVersion {
				repair.voided = true
			}
		}
	case string(WorkerCompleted):
		var report WorkerCompletedPayload
		if err := json.Unmarshal([]byte(row.payload), &report); err != nil {
			return newFailure(KindInvariantViolation, subject, "worker completion payload is malformed", false, "restore a verified backup into a new path with concord restore, then swap")
		}
		if report.Review != nil {
			h.reviewDisposition[row.dispatchAttempt] = report.Review.Disposition
			if report.Review.Disposition == "correction_required" || report.Review.Disposition == "revision_required" {
				h.blockingReview[row.dispatchAttempt] = true
				h.fail(row.dispatchAttempt)
			}
		}
	case string(WorkerFailed):
		// A failure creates debt before any coordinator disposition, including
		// an authorized attempt that never supplied dispatch evidence. A
		// failed or abandoned confirming review voids its repair.
		h.fail(row.dispatchAttempt)
		h.voidConfirmingRepair(row.dispatchAttempt)
	case string(WorkerDispatched):
		h.materialized[row.dispatchAttempt] = true
		h.bind(row.dispatchAttempt, WorkerJobBinding{JobID: row.jobID, Revision: row.jobRevision, Digest: row.jobDigest})
	case string(WorkerJobRecorded):
		var recorded WorkerJobRecordedPayload
		if err := json.Unmarshal([]byte(row.payload), &recorded); err == nil {
			key := workerJobKey(recorded.JobID, recorded.Revision)
			h.obligations[key] = workflowJobObligationState{base: workerJobObligationKey(recorded), oracle: recorded.AcceptanceOracle, contractVersion: recorded.ContractVersion}
			if recorded.ChecksRepair != nil {
				named := make([]string, 0, len(recorded.ChecksRepair.Revisions))
				for _, entry := range recorded.ChecksRepair.Revisions {
					named = append(named, workerJobKey(entry.WorkerJob.JobID, entry.WorkerJob.Revision))
				}
				h.checksRepairs[key] = &workflowChecksRepairState{
					binding:         WorkerJobBinding{JobID: recorded.JobID, Revision: recorded.Revision, Digest: recorded.Digest},
					namedKeys:       named,
					contractVersion: recorded.ContractVersion,
				}
			}
		}
	}
	if row.kind != string(WorkflowActionCompleted) {
		return nil
	}
	switch row.actionID {
	case "dispatch_worker":
		// Authorization owns the binding even without worker evidence. Only
		// materialized completions can consume an unbound correction record.
		if row.actionAttempt != "" {
			h.bind(row.actionAttempt, WorkerJobBinding{JobID: row.jobID, Revision: row.jobRevision, Digest: row.jobDigest})
		}
		// The confirming-review marker binds the exact repairing revision
		// beside the repairing acceptance.
		if row.actionAttempt != "" {
			var completion struct {
				ChecksRepairReview *WorkerChecksRepairReviewAdmission `json:"checks_repair_review"`
			}
			if err := json.Unmarshal([]byte(row.payload), &completion); err == nil && completion.ChecksRepairReview != nil {
				key := workerJobKey(completion.ChecksRepairReview.WorkerJob.JobID, completion.ChecksRepairReview.WorkerJob.Revision)
				if repair, recorded := h.checksRepairs[key]; recorded && repair.confirmAttempt == "" {
					repair.confirmAttempt = row.actionAttempt
				}
			}
		}
		h.completions = append(h.completions, workflowCorrectionDispatchCompletion{seq: row.seq, attempt: row.actionAttempt})
	case "record_worker_failure", "reject_worker_result", "request_correction":
		var fields workflowCorrectionCompletionFields
		if err := json.Unmarshal([]byte(row.payload), &fields); err != nil {
			return newFailure(KindInvariantViolation, subject, "correction completion payload is malformed", false, "restore a verified backup into a new path with concord restore, then swap")
		}
		if row.actionID == "reject_worker_result" {
			h.voidConfirmingRepair(row.actionAttempt)
		}
		key := ""
		if row.actionID != "request_correction" {
			key = h.fail(row.actionAttempt)
		}
		record := workflowCorrectionRecordState{seq: row.seq, sourceID: row.eventID, fields: fields, jobKey: key}
		h.records = append(h.records, record)
	case "accept_worker_result":
		// Only a result acceptance counts for repair state (CD-0205 D4):
		// the repairing result's own acceptance opens the repair, and the
		// exact confirming attempt's accepted Review confirms it when that
		// Review requires no corrective work (continue or
		// ready_for_final_proof) — never another attempt bound to the same
		// repairing revision.
		if repair := h.repairOfBinding(row.jobID, row.jobRevision); repair != nil && repair.acceptanceSeq == 0 {
			repair.acceptanceSeq = row.seq
		}
		confirming := h.confirmingAttemptOf(row.actionAttempt)
		disposition := h.reviewDisposition[row.actionAttempt]
		if confirming != nil && (disposition == "correction_required" || disposition == "revision_required") {
			h.voidConfirmingRepair(row.actionAttempt)
		}
		if confirming != nil && (disposition == "continue" || disposition == "ready_for_final_proof") && !confirming.voided {
			confirming.confirmed = true
			for _, key := range confirming.namedKeys {
				delete(h.unresolved, key)
			}
			if len(h.unresolved) == 0 {
				h.boundary = row.seq
			}
		} else if !h.blockingReview[row.actionAttempt] && h.discharge(row.jobID, row.jobRevision) {
			h.boundary = row.seq
		}
	}
	return nil
}

// repairOfBinding finds the recorded repair whose repairing revision the
// binding names.
func (h *workflowCorrectionHistory) repairOfBinding(jobID string, revision int64) *workflowChecksRepairState {
	if jobID == "" || revision == 0 {
		return nil
	}
	return h.checksRepairs[workerJobKey(jobID, revision)]
}

// openUnconfirmedRepairBeside shares the at-most-one-open guard between
// recording and acceptance. The lowest revision deterministically names the
// blocker if a corrupt history contains several.
func (h *workflowCorrectionHistory) openUnconfirmedRepairBeside(jobID string, revision int64) *workflowChecksRepairState {
	var open *workflowChecksRepairState
	for _, repair := range h.checksRepairs {
		if repair.binding.JobID == jobID && repair.binding.Revision != revision && repair.openUnconfirmed() && (open == nil || repair.binding.Revision < open.binding.Revision) {
			open = repair
		}
	}
	return open
}

func (h *workflowCorrectionHistory) bind(attempt string, binding WorkerJobBinding) {
	if binding.JobID == "" {
		return
	}
	if _, bound := h.dispatchJob[attempt]; !bound {
		h.dispatchJob[attempt] = workerJobKey(binding.JobID, binding.Revision)
	}
}

func (h *workflowCorrectionHistory) fail(attempt string) string {
	key := h.dispatchJob[attempt]
	if key != "" {
		h.unresolved[key] = true
	}
	return key
}

// workflowJobObligationState is one recorded revision's corrective
// obligation: the base obligation key over objective, scope, predicates, and
// checks, plus the recorded acceptance oracle. The oracle participates in
// obligation identity: a renamed or rewritten oracle does not
// reset earlier debt, and a newer revision satisfies an earlier revision's
// unresolved debt only when the base obligation is unchanged and every
// previous owner, case, and control is retained byte-identically under the
// same recorded contract. A strengthened inventory may add cases and controls.
type workflowJobObligationState struct {
	base            string
	oracle          *AcceptanceOracle
	contractVersion int64
}

// discharge owns acceptance satisfaction for both projections. A reset needs
// every debt cleared by the same job's equal base obligation with retained
// oracle owners, cases, controls, and contract authority. With no job-bound
// debt, any acceptance resets the window. The accepted dispatch
// binds the revision being compared; it does not supply semantic proof.
func (h *workflowCorrectionHistory) discharge(jobID string, revision int64) bool {
	if len(h.unresolved) == 0 {
		return true
	}
	accepted := h.obligations[workerJobKey(jobID, revision)]
	if jobID == "" || accepted.base == "" {
		return false
	}
	for open := range h.unresolved {
		if !strings.HasPrefix(open, jobID+"|") {
			continue
		}
		prior := h.obligations[open]
		if prior.base != accepted.base || (prior.oracle != nil && prior.contractVersion != accepted.contractVersion) || !oracleControlsRetained(prior.oracle, accepted.oracle) {
			continue
		}
		delete(h.unresolved, open)
	}
	return len(h.unresolved) == 0
}

func (h *workflowCorrectionHistory) openRecord(excludeAttempt string) *workflowCorrectionRecordState {
	// Materialization can follow its authorization anywhere in this prefix.
	// The latest eligible completion consumes all earlier unbound records.
	var consumedThrough int64
	for _, completion := range h.completions {
		if h.materialized[completion.attempt] && completion.attempt != excludeAttempt && completion.seq > consumedThrough {
			consumedThrough = completion.seq
		}
	}
	for i := len(h.records) - 1; i >= 0; i-- {
		record := h.records[i]
		if record.jobKey != "" {
			if h.unresolved[record.jobKey] {
				return &record
			}
			continue
		}
		if record.seq >= consumedThrough {
			return &record
		}
	}
	return nil
}

type workflowCorrectionDispatchCompletion struct {
	seq     int64
	attempt string
}

// workerJobKey names one recorded revision in the correction walk's maps. The
// separator cannot occur in a job id, so a job-prefixed key matches exactly
// its own job's revisions.
func workerJobKey(jobID string, revision int64) string {
	return jobID + "|" + strconv.FormatInt(revision, 10)
}

// workerJobObligationKey derives the base obligation content of one recorded
// revision: every content field that defines what the job owes, excluding
// the identity fields (job id, revision, parent contract version), the
// coordinator's readiness assertion, and the acceptance oracle — the oracle
// joins obligation identity structurally in workflowJobObligationState, so
// a strengthened-but-retaining oracle can satisfy old debt while a renamed
// or control-rewritten oracle never resets it. Two revisions of one job
// carry the same base obligation exactly when this key equals.
func workerJobObligationKey(payload WorkerJobRecordedPayload) string {
	obligation := struct {
		Objective           string                  `json:"objective"`
		StoppingCondition   string                  `json:"stopping_condition"`
		ProjectScope        string                  `json:"project_scope"`
		PathScope           []string                `json:"path_scope"`
		PredicateIDs        []string                `json:"predicate_ids"`
		Checks              []string                `json:"checks"`
		Prerequisites       []WorkerJobPrerequisite `json:"prerequisites"`
		UnresolvedRefs      []string                `json:"unresolved_refs"`
		ReservedIntegration string                  `json:"reserved_integration"`
	}{payload.Objective, payload.StoppingCondition, payload.ProjectScope, payload.PathScope, payload.PredicateIDs, payload.Checks, payload.Prerequisites, payload.UnresolvedRefs, payload.ReservedIntegration}
	raw, err := json.Marshal(obligation)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// workflowHeldJobAcceptPredicate excludes a held local job acceptance from
// workflow step-entry detection. Acceptance reset belongs to the correction
// history's obligation discharge rule instead.
const workflowHeldJobAcceptPredicate = `(COALESCE(json_extract(payload,'$.action_id'),'')<>'accept_worker_result' OR json_extract(payload,'$.worker_job') IS NULL OR COALESCE(json_extract(payload,'$.delivery_state'),'')<>'')`

func workflowCorrectionVerdictState(ctx context.Context, q queryer, workID string, definition WorkflowDefinition, currentStep, subject string) (*workflowCorrectionVerdictPrerequisites, error) {
	if workflowCorrectionReturnTarget(definition, currentStep) == "" {
		// The step evaluates nothing and serves no verdict step: no
		// verdict correction route opens from it. Every evaluator step of
		// every registered family resolves a route, and every late-verdict
		// step serves one that does (D4), so this read is the one shape
		// gate — including the terminal implementation shape whose
		// request_correction the work-kind era carried (CD-0201 D1).
		return nil, nil
	}
	contractVersion, err := activeWorkflowContractVersion(ctx, q, workID, subject)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, wrapFailure(KindUnavailable, subject, "cannot read the active workflow contract", true, "retry once the workflow contract is readable", err)
	}
	verdicts, err := latestWorkflowVerdicts(ctx, q, workID, contractVersion)
	if err != nil {
		return nil, err
	}
	nonOK := make([]workflowVerdictRecordedPayload, 0, len(verdicts))
	for _, verdict := range verdicts {
		if verdict.VerdictKind != "ok" || verdict.IncomparableWithApproved {
			nonOK = append(nonOK, verdict)
		}
	}
	if len(nonOK) == 0 {
		return nil, nil
	}
	var verdictSeq int64
	if err := q.QueryRowContext(ctx, `SELECT COALESCE(MAX(verdict_seq),0) FROM workflow_predicate_verdicts WHERE work_id=?`, workID).Scan(&verdictSeq); err != nil {
		return nil, wrapFailure(KindUnavailable, subject, "cannot read the latest workflow verdict sequence", true, "retry once the workflow verdict projection is readable", err)
	}
	acceptedDispatchSeq, accepted, acceptedErr := workflowAcceptedWorkerDelivery(ctx, q, workID, verdictSeq, subject)
	if acceptedErr != nil {
		return nil, acceptedErr
	}
	if !accepted {
		// Direct artifact actions carry production without a worker attempt.
		// The same frontier that establishes freshness supplies this ground;
		// an older artifact cannot bypass a later unaccepted review, and
		// production after the verdict cannot justify its correction.
		acceptedDispatchSeq, acceptedErr = workflowArtifactProductionFrontier(ctx, q, workID, definition, workflowCorrectionReturnTarget(definition, currentStep), acceptedDispatchSeq, verdictSeq, subject)
		if acceptedErr != nil {
			return nil, acceptedErr
		}
		accepted = acceptedDispatchSeq > 0
	}
	return &workflowCorrectionVerdictPrerequisites{
		nonOK: nonOK, contractVersion: contractVersion, verdictSeq: verdictSeq,
		acceptedDispatchSeq: acceptedDispatchSeq, accepted: accepted,
	}, nil
}

// workflowCorrectionVerdictPrerequisites is the verdict-side prerequisite
// state behind a correction request: the active contract's latest non-ok
// verdicts, the verdict sequence, and whether the worker delivery behind that
// sequence was accepted. The accepted-completed-delivery route (CD-0143 D1)
// and the checkpoint failed-review route read one state, so both surfaces
// answer from one derivation.
type workflowCorrectionVerdictPrerequisites struct {
	nonOK               []workflowVerdictRecordedPayload
	contractVersion     int64
	verdictSeq          int64
	acceptedDispatchSeq int64
	accepted            bool
}

// workflowVerdictCorrectionFromState closes the accepted-completed-delivery
// route (CD-0143 D1) on the derived verdict state: the route admits only a
// correction whose every current non-ok verdict postdates the accepted
// delivery and any prior correction.
func workflowVerdictCorrectionFromState(ctx context.Context, q queryer, workID string, definition WorkflowDefinition, currentStep, subject string, state *workflowCorrectionVerdictPrerequisites) (*WorkflowCorrectionContext, error) {
	if !state.accepted {
		return nil, nil
	}
	verdicts := state.nonOK
	seq := state.verdictSeq
	acceptedDispatchSeq := state.acceptedDispatchSeq
	contractVersion := state.contractVersion
	lastHealthySeq, healthyErr := workflowCorrectionHealthyBaseline(ctx, q, workID, contractVersion, seq)
	if healthyErr != nil {
		return nil, healthyErr
	}
	var latestCorrectionSeq int64
	if err := q.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq),0) FROM domain_events WHERE subject_type=? AND subject_id=? AND kind=? AND json_extract(payload,'$.action_id')='request_correction' AND seq>? AND seq<?`, string(SubjectWorkItem), workID, WorkflowActionCompleted, lastHealthySeq, seq).Scan(&latestCorrectionSeq); err != nil {
		return nil, wrapFailure(KindUnavailable, subject, "cannot inspect prior correction requests", true, "retry once the workflow correction projection is readable", err)
	}
	if latestCorrectionSeq >= seq {
		return nil, nil
	}
	verdictSequences, sequenceErr := workflowLatestVerdictSequences(ctx, q, workID, contractVersion, verdicts)
	if sequenceErr != nil {
		return nil, sequenceErr
	}
	if acceptedDispatchSeq <= latestCorrectionSeq {
		return nil, nil
	}
	for _, verdict := range verdicts {
		if verdictSequences[verdict.PredicateID] <= latestCorrectionSeq || verdictSequences[verdict.PredicateID] <= acceptedDispatchSeq {
			return nil, nil
		}
	}
	predicates := make([]string, 0, len(verdicts))
	evidence := make([]string, 0)
	for _, verdict := range verdicts {
		predicates = append(predicates, verdict.PredicateID)
		for _, ref := range verdict.EvaluationEvidence {
			if !slices.Contains(evidence, ref) {
				evidence = append(evidence, ref)
			}
		}
	}
	return &WorkflowCorrectionContext{
		Disposition:  "verification",
		PredicateIDs: nonNilStrings(predicates), EvidenceRefs: nonNilStrings(evidence),
		Diagnosis: "latest verification verdict is not healthy", Strategy: workflowCorrectionRouteStrategy(definition, currentStep),
	}, nil
}

func workflowLatestVerdictSequences(ctx context.Context, q queryer, workID string, contractVersion int64, wanted []workflowVerdictRecordedPayload) (map[string]int64, error) {
	history, err := workflowContractPredicateHistory(ctx, q, workID, contractVersion)
	if err != nil {
		return nil, err
	}
	pins, err := workflowContractDefinitionPins(ctx, q, workID, contractVersion)
	if err != nil {
		return nil, err
	}
	wantedIDs := make(map[string]bool, len(wanted))
	for _, verdict := range wanted {
		wantedIDs[verdict.PredicateID] = true
	}
	sequences := make(map[string]int64, len(wanted))
	seen := make(map[string]bool)
	foundWanted := 0
	rows, err := q.QueryContext(ctx, `SELECT verdict_seq,payload FROM workflow_predicate_verdicts WHERE work_id=? ORDER BY verdict_seq DESC`, workID)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "workflow_correction", "cannot read workflow verdict sequences", true, "retry once the workflow verdict projection is readable", err)
	}
	defer rows.Close()
	for rows.Next() {
		var seq int64
		var raw []byte
		if err := rows.Scan(&seq, &raw); err != nil {
			return nil, wrapFailure(KindUnavailable, "workflow_correction", "cannot scan workflow verdict sequence", true, "retry once the workflow verdict projection is readable", err)
		}
		var verdict workflowVerdictRecordedPayload
		if err := json.Unmarshal(raw, &verdict); err != nil {
			return nil, newFailure(KindInvariantViolation, "workflow_correction", "workflow verdict payload is malformed", false, "restore a verified backup into a new path with concord restore, then swap")
		}
		if verdict.ContractVersion <= 0 || verdict.ContractVersion > contractVersion || seen[verdict.PredicateID] {
			continue
		}
		if verdict.ContractVersion < contractVersion && (!workflowPredicateHistoryCompatible(history, verdict.ContractVersion, contractVersion, verdict.PredicateID, verdict) || !workflowContractDefinitionPinsCompatible(pins, verdict.ContractVersion, contractVersion)) {
			continue
		}
		seen[verdict.PredicateID] = true
		if wantedIDs[verdict.PredicateID] {
			sequences[verdict.PredicateID] = seq
			foundWanted++
		}
		if foundWanted == len(wantedIDs) {
			break
		}
	}
	if err := rows.Err(); err != nil {
		return nil, wrapFailure(KindUnavailable, "workflow_correction", "cannot scan workflow verdict sequences", true, "retry once the workflow verdict projection is readable", err)
	}
	return sequences, nil
}

// workflowRefinementStepID returns the step declaring checkpoint_refine.
func workflowRefinementStepID(definition WorkflowDefinition) string {
	for i := range definition.StepGraph.Steps {
		if stepDeclaresAction(definition, definition.StepGraph.Steps[i].ID, "checkpoint_refine") {
			return definition.StepGraph.Steps[i].ID
		}
	}
	return ""
}

// workflowDeliveryGateCorrectionContextForRegistry resolves the same gate
// context with the caller's authoritative registry folding the admission
// state's historical progress. A nil registry selects the builtin registry.
func workflowDeliveryGateCorrectionContextForRegistry(ctx context.Context, q queryer, workID string, definition WorkflowDefinition, currentStep, subject string, registry DefinitionRegistry) (*WorkflowCorrectionContext, error) {
	if !workflowStepIsDeliveryGate(workflowStep(definition, currentStep)) {
		// The gate shape owns the route; its target comes from the route table.
		return nil, nil
	}
	// Pool-backed readers open a short transaction around the same admission
	// loader the delivery guard and work pin use.
	lawTx, isTx := q.(*sql.Tx)
	if !isTx {
		db, isDB := q.(*sql.DB)
		if !isDB {
			return nil, newFailure(KindUnavailable, subject, "workflow action admission folds in the caller's transaction", false, "run the admission fold inside the mutation transaction")
		}
		readTx, beginErr := beginReadTx(ctx, db)
		if beginErr != nil {
			return nil, wrapFailure(KindUnavailable, subject, "cannot open the read transaction", true, "retry once the store is readable", beginErr)
		}
		defer func() {
			_ = readTx.Rollback()
		}()
		lawTx = readTx
	}
	// The fold and its continuation read on the transaction's connection: on
	// the pool-backed queryer the continuation's queries would park behind
	// the read transaction this wrapper holds (the store connection
	// invariant).
	state, _, _, err := loadWorkflowAdmissionStateTxForRegistry(ctx, lawTx, workID, definition, currentStep, subject, registry)
	if err != nil {
		return nil, err
	}
	return workflowDeliveryGateCorrectionContextFolded(ctx, lawTx, workID, definition, currentStep, subject, state)
}

// workflowDeliveryGateCorrectionContextFolded is the gate's corrective
// return when the standing accepted Review no longer covers the work: a
// successor contract changed the candidate or its depended-on revisions, so
// the delivery assertion refuses and no dispatch lives at the gate. The
// declared route returns the instance to the producer step to obtain a fresh
// accepted Review of the current candidate. An absent or still-applicable
// Review opens no return.
func workflowDeliveryGateCorrectionContextFolded(ctx context.Context, q queryer, workID string, definition WorkflowDefinition, currentStep, subject string, state WorkflowAdmissionState) (*WorkflowCorrectionContext, error) {
	if state.Review == nil || state.Review.Accepted == nil || state.Review.Candidate == nil {
		return nil, nil
	}
	if state.Review.Accepted.Report.OutputCandidate == *state.Review.Candidate && state.Review.DependenciesCurrent {
		return nil, nil
	}
	predicates, err := workflowActiveContractPredicateIDs(ctx, q, workID, subject)
	if err != nil {
		return nil, err
	}
	if len(predicates) == 0 {
		return nil, nil
	}
	target := workflowCorrectionReturnTarget(definition, currentStep)
	if target == "" {
		return nil, nil
	}
	var seq int64
	if err := q.QueryRowContext(ctx, `SELECT seq FROM domain_events WHERE event_id=? AND subject_id=?`, state.Review.Accepted.ReportRef, workID).Scan(&seq); err != nil {
		return nil, err
	}
	return &WorkflowCorrectionContext{
		Disposition: "verification", PredicateIDs: predicates, EvidenceRefs: []string{state.Review.Accepted.ReportRef},
		SourceEventID: state.Review.Accepted.ReportRef, SourceEventSeq: seq,
		Diagnosis: "the accepted current Review no longer covers the current candidate or depended-on revisions",
		Strategy:  fmt.Sprintf("return to step %q and obtain a fresh accepted review of the current candidate", target),
	}, nil
}

// Correction prerequisite classes. The shared admission derivation names the
// one prerequisite a closed correction request is missing, so the pin, the
// preflight, the fold, the dispatch resolver, and the guards state one answer
// and name the same gap.
const (
	workflowCorrectionMissingNone               = ""
	workflowCorrectionMissingVerdict            = "verdict"
	workflowCorrectionMissingAcceptedDelivery   = "accepted_delivery"
	workflowCorrectionMissingFailureDisposition = "failure_disposition"
	workflowCorrectionMissingGateReview         = "gate_review"
	workflowCorrectionMissingFreshReview        = "fresh_review"
)

// workflowCorrectionRequestUnavailableFailure is the one refusal a closed
// correction request produces. The class decides the message, so a refusal
// names the actual missing prerequisite instead of a generic verdict claim.
func workflowCorrectionRequestUnavailableFailure(subject, missing string) *Failure {
	switch missing {
	case workflowCorrectionMissingVerdict:
		return newFailure(KindInvalidOperation, subject, "request_correction requires a current non-ok verification verdict under the active workflow contract", false, "record the current verification verdict or reread the work pin")
	case workflowCorrectionMissingFailureDisposition:
		return newFailure(KindInvalidOperation, subject, "request_correction requires the checkpoint failure record that dispositions the failed review attempt", false, "record the failed review attempt at the checkpoint with record_worker_failure")
	case workflowCorrectionMissingAcceptedDelivery:
		return newFailure(KindInvalidOperation, subject, "request_correction requires an accepted worker delivery behind the current non-ok verdict", false, "accept the completed worker result or reread the work pin")
	case workflowCorrectionMissingFreshReview:
		return newFailure(KindInvalidOperation, subject, "request_correction already consumed the accepted Review; a further correction requires a fresh accepted Review of the current candidate", false, "dispatch Review of the current candidate and accept its disposition")
	case workflowCorrectionMissingGateReview:
		return newFailure(KindInvalidOperation, subject, "request_correction at the delivery gate requires an outstanding post-rejection review", false, "reread the current work pin")
	default:
		return newFailure(KindInvalidOperation, subject, "request_correction requires a current non-ok verification verdict after worker delivery", false, "record the current verification verdict or reread the work pin")
	}
}

// workflowAttemptDispatchSequence reads the sequence of the worker dispatch
// behind one dispatched attempt: the freshness anchor the failed-review
// correction's current verdicts must postdate.
func workflowAttemptDispatchSequence(ctx context.Context, q queryer, workID, attemptID, subject string) (int64, error) {
	var seq int64
	if err := q.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq),0) FROM domain_events WHERE subject_type=? AND subject_id=? AND kind=? AND json_extract(payload,'$.attempt_id')=?`, string(SubjectWorkItem), workID, WorkerDispatched, attemptID).Scan(&seq); err != nil {
		return 0, wrapFailure(KindUnavailable, subject, "cannot inspect the failed attempt dispatch", true, "retry once the worker attempt projection is readable", err)
	}
	return seq, nil
}

// workflowCheckpointFailedReviewCorrection is the amended CD-0143 D1 and
// CD-0193 D1 admission. At a human_checkpoint step whose latest attempt a
// worker actually dispatched failed and whose exact hold-mode
// record_worker_failure dispositions that attempt, a current non-ok verdict
// admits the evidence-bearing corrective return the accepted-delivery route
// cannot. The failure disposition is not an accepted success: it opens only
// this typed return, and the counting follows the verification
// comparator (requests since the last comparable-healthy verdict, plus the
// pending request). The admitted pair must be current: the dispositioned
// attempt's dispatch must postdate the step recovery anchor — a later
// accepted delivery, step entry, or correction request superseded it — and
// every current non-ok verdict must postdate the attempt's dispatch, so a
// correction request that consumed the pair ended it and only a fresh verdict
// or a fresh dispositioned failed review reopens the return.
func workflowCheckpointFailedReviewCorrection(ctx context.Context, q queryer, workID string, definition WorkflowDefinition, currentStep, subject string, state *workflowCorrectionVerdictPrerequisites, excludeSeq int64) (*WorkflowCorrectionContext, string, error) {
	if step := workflowStep(definition, currentStep); step == nil || step.Kind != WorkflowStepHumanCheckpoint {
		return nil, workflowCorrectionMissingAcceptedDelivery, nil
	}
	attemptID, lifecycle, found, err := latestDispatchedAttemptAtStep(ctx, q, workID, currentStep, 0)
	if err != nil {
		return nil, workflowCorrectionMissingNone, err
	}
	if !found || lifecycle != "failed" {
		return nil, workflowCorrectionMissingAcceptedDelivery, nil
	}
	recorded, err := workerRecordedFailureCountTx(ctx, q, workID, attemptID)
	if err != nil {
		return nil, workflowCorrectionMissingNone, wrapFailure(KindUnavailable, subject, "cannot inspect the checkpoint failure record", true, "retry once the workflow projection is readable", err)
	}
	if recorded == 0 {
		return nil, workflowCorrectionMissingFailureDisposition, nil
	}
	anchor, anchorErr := workflowCorrectionAcceptanceBefore(ctx, q, workID, excludeSeq, subject)
	if anchorErr != nil {
		return nil, workflowCorrectionMissingNone, anchorErr
	}
	dispatchSeq, dispatchErr := workflowAttemptDispatchSequence(ctx, q, workID, attemptID, subject)
	if dispatchErr != nil {
		return nil, workflowCorrectionMissingNone, dispatchErr
	}
	if dispatchSeq <= anchor {
		return nil, workflowCorrectionMissingVerdict, nil
	}
	verdictSequences, sequenceErr := workflowLatestVerdictSequences(ctx, q, workID, state.contractVersion, state.nonOK)
	if sequenceErr != nil {
		return nil, workflowCorrectionMissingNone, sequenceErr
	}
	for _, verdict := range state.nonOK {
		if verdictSequences[verdict.PredicateID] <= dispatchSeq {
			return nil, workflowCorrectionMissingVerdict, nil
		}
	}
	target := workflowUnhealthyVerdictRouteTarget(definition, currentStep)
	if target == "" {
		return nil, workflowCorrectionMissingAcceptedDelivery, nil
	}
	predicates := make([]string, 0, len(state.nonOK))
	evidence := make([]string, 0)
	for _, verdict := range state.nonOK {
		predicates = append(predicates, verdict.PredicateID)
		for _, ref := range verdict.EvaluationEvidence {
			if !slices.Contains(evidence, ref) {
				evidence = append(evidence, ref)
			}
		}
	}
	return &WorkflowCorrectionContext{
		Disposition:  "failed",
		PredicateIDs: nonNilStrings(predicates), EvidenceRefs: nonNilStrings(evidence),
		Diagnosis: "the checkpoint review attempt failed and the latest verification verdict is not healthy", Strategy: workflowCorrectionRouteStrategy(definition, currentStep),
		FailedAttemptID: attemptID,
	}, workflowCorrectionMissingNone, nil
}

// workflowCorrectionRequestAdmissionForRegistry derives the correction request the
// current step admits and names the one missing prerequisite when it admits
// none. It is the single derivation behind the availability check, the work
// pin, the preflight, the fold, and the dispatch refusal, so every surface
// answers from one read. excludeSeq names the fold's own in-flight completion
// the admission must not count against itself; read surfaces pass zero. A
// CD-0166 delivery gate corrects through the post-rejection review return; a
// checkpoint with a dispositioned failed review corrects through the
// failed-review return; every other step corrects through the verification
// verdict.
// The caller's registry resolves the folded state's historical progress;
// a nil registry selects the builtin registry.
func workflowCorrectionRequestAdmissionForRegistry(ctx context.Context, q queryer, workID string, definition WorkflowDefinition, currentStep, subject string, excludeSeq int64, registry DefinitionRegistry) (*WorkflowCorrectionContext, string, error) {
	if workflowCorrectionReturnTarget(definition, currentStep) != "" {
		review, err := loadWorkflowReviewState(ctx, q, workID, excludeSeq)
		if err != nil {
			return nil, workflowCorrectionMissingNone, err
		}
		if review.Accepted != nil && review.Candidate != nil && review.Accepted.Report.OutputCandidate == *review.Candidate && review.DependenciesCurrent && review.Accepted.Report.Disposition == "correction_required" {
			predicates, err := workflowActiveContractPredicateIDs(ctx, q, workID, subject)
			if err != nil {
				return nil, workflowCorrectionMissingNone, err
			}
			var seq int64
			if err := q.QueryRowContext(ctx, `SELECT seq FROM domain_events WHERE event_id=? AND subject_id=?`, review.Accepted.ReportRef, workID).Scan(&seq); err != nil {
				return nil, workflowCorrectionMissingNone, err
			}
			// One accepted Review authorizes one correction (CD-0166).
			var consumed int
			if err := q.QueryRowContext(ctx, `SELECT count(*) FROM domain_events WHERE subject_type=? AND subject_id=? AND kind=? AND json_extract(payload,'$.action_id')='request_correction' AND seq>? AND seq<>?`, string(SubjectWorkItem), workID, WorkflowActionCompleted, seq, excludeSeq).Scan(&consumed); err != nil {
				return nil, workflowCorrectionMissingNone, err
			}
			if consumed != 0 {
				return nil, workflowCorrectionMissingFreshReview, nil
			}
			return &WorkflowCorrectionContext{Disposition: "verification", PredicateIDs: predicates, EvidenceRefs: []string{review.Accepted.ReportRef}, SourceEventID: review.Accepted.ReportRef, SourceEventSeq: seq, Diagnosis: "the accepted current Review requires correction", Strategy: "perform the declared corrective work and obtain applicable Review"}, workflowCorrectionMissingNone, nil
		}
	}
	if workflowStepIsDeliveryGate(workflowStep(definition, currentStep)) {
		context, err := workflowDeliveryGateCorrectionContextForRegistry(ctx, q, workID, definition, currentStep, subject, registry)
		if err != nil || context != nil {
			return context, workflowCorrectionMissingNone, err
		}
		return nil, workflowCorrectionMissingGateReview, nil
	}
	state, err := workflowCorrectionVerdictState(ctx, q, workID, definition, currentStep, subject)
	if err != nil {
		return nil, workflowCorrectionMissingNone, err
	}
	if state != nil {
		context, contextErr := workflowVerdictCorrectionFromState(ctx, q, workID, definition, currentStep, subject, state)
		if contextErr != nil {
			return nil, workflowCorrectionMissingNone, contextErr
		}
		if context != nil {
			return context, workflowCorrectionMissingNone, nil
		}
		return workflowCheckpointFailedReviewCorrection(ctx, q, workID, definition, currentStep, subject, state, excludeSeq)
	}
	// No non-ok verdict stands under the active contract. A step that
	// resolves no unhealthy_verdict route and serves no verdict step keeps
	// the shape-neutral default refusal; an evaluator or late-verdict step
	// names the absent verdict.
	if workflowCorrectionReturnTarget(definition, currentStep) == "" {
		return nil, workflowCorrectionMissingNone, nil
	}
	return nil, workflowCorrectionMissingVerdict, nil
}

// workflowCorrectionRequestAdmissionStateForRegistry resolves the same
// admission with the caller's authoritative registry. A nil registry selects
// the builtin registry.
func workflowCorrectionRequestAdmissionStateForRegistry(ctx context.Context, q queryer, workID string, definition WorkflowDefinition, currentStep, subject string, excludeSeq int64, registry DefinitionRegistry) (bool, string, error) {
	context, missing, err := workflowCorrectionRequestAdmissionForRegistry(ctx, q, workID, definition, currentStep, subject, excludeSeq, registry)
	return context != nil, missing, err
}

// workflowActiveContractPredicateIDs lists the active contract's predicate IDs
// in ordinal order, or an empty slice when the item has no active contract.
func workflowActiveContractPredicateIDs(ctx context.Context, q queryer, workID, subject string) ([]string, error) {
	contractVersion, err := activeWorkflowContractVersion(ctx, q, workID, subject)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	rows, err := q.QueryContext(ctx, `SELECT predicate_id FROM workflow_contract_predicates WHERE work_id=? AND contract_version=? ORDER BY ordinal`, workID, contractVersion)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, subject, "cannot read active workflow predicates", true, "retry once the workflow contract is readable", err)
	}
	defer rows.Close()
	predicates := make([]string, 0, 4)
	for rows.Next() {
		var predicateID string
		if err := rows.Scan(&predicateID); err != nil {
			return nil, wrapFailure(KindUnavailable, subject, "cannot scan active workflow predicate", true, "retry once the workflow contract is readable", err)
		}
		predicates = append(predicates, predicateID)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapFailure(KindUnavailable, subject, "cannot enumerate active workflow predicates", true, "retry once the workflow contract is readable", err)
	}
	return predicates, nil
}

// validateCorrectionRequestPayload refuses a correction payload the folded
// correction request cannot represent. context is the one admission derivation
// the caller already folded — the admission state's CorrectionRequestContext
// at the guard and preflight sites, the fold's own derived request — so this
// check binds predicates and evidence only and never re-enters the loader.
// A nil context refuses: the step admits no correction request to bind
// against.
func validateCorrectionRequestPayload(ctx context.Context, q queryer, workID string, payload json.RawMessage, subject string, context *WorkflowCorrectionContext) error {
	if context == nil {
		return newFailure(KindUnavailable, subject, "the current step admits no correction request", false, "reread_entities")
	}
	fields, err := workflowActionObject(payload)
	if err != nil {
		return err
	}
	diagnosis := workflowFieldStringDefault(fields, "diagnosis", "")
	strategy := workflowFieldStringDefault(fields, "strategy", "")
	predicates := workflowFieldStrings(fields, "predicate_ids")
	evidence := workflowFieldStrings(fields, "evidence_refs")
	if diagnosis == "" || strategy == "" || len(predicates) == 0 || len(evidence) == 0 {
		return newFailure(KindInvalidPayload, subject, "request_correction requires diagnosis, strategy, predicate IDs, and bound evidence", false, "supply the complete correction disposition")
	}
	for _, predicate := range predicates {
		if !slices.Contains(context.PredicateIDs, predicate) {
			return newFailure(KindInvalidPayload, subject, "request_correction names a predicate without a current non-ok verdict", false, "name only affected approved predicates")
		}
	}
	for _, ref := range evidence {
		bound, boundErr := workflowEvidenceReferenceBound(ctx, q, workID, ref, subject, 0)
		if boundErr != nil {
			return boundErr
		}
		if !bound {
			return newFailure(KindMissingEvidence, subject, "request_correction evidence is not durably bound: "+ref, false, "provide_evidence")
		}
	}
	return nil
}

// workflowCorrectionContext reads the latest open failure or rejection. A
// record closes per CD-0205 D4: a job-bound record stays open until an
// acceptance discharges its recorded obligation, and an unbound record (and a
// recorded correction request) closes on the next materialized dispatch.
func workflowCorrectionContext(ctx context.Context, q queryer, workID, stepID string) (*WorkflowCorrectionContext, error) {
	return workflowCorrectionContextForDispatch(ctx, q, workID, stepID, "")
}

// workflowCorrectionRecordState is one failure, rejection, or correction
// request record selected by the obligation history.
type workflowCorrectionRecordState struct {
	seq      int64
	sourceID string
	fields   workflowCorrectionCompletionFields
	// jobKey names the recorded revision the failed attempt was dispatched
	// under; empty when the attempt carried no job binding.
	jobKey string
}

// workflowCorrectionCompletionFields is the durable completion payload of one
// correction record.
type workflowCorrectionCompletionFields struct {
	ActionID             string   `json:"action_id"`
	StepID               string   `json:"step_id"`
	OpenFindingIDs       []string `json:"correction_open_finding_ids"`
	AttemptID            string   `json:"worker_attempt_id"`
	AttemptEpoch         int64    `json:"attempt_epoch"`
	CorrectionDiagnosis  string   `json:"correction_diagnosis"`
	CorrectionStrategy   string   `json:"correction_strategy"`
	CorrectionPredicates []string `json:"correction_predicate_ids"`
	CorrectionEvidence   []string `json:"correction_evidence_refs"`
	ResultEvidence       []string `json:"result_evidence_refs"`
}

// The consuming dispatch must have a worker attempt that materialized: a
// worker.dispatched event for the completion's worker_attempt_id. A dispatch
// intent folded without its host worker-dispatch call (an interruption between
// the workflow action and the dispatch) leaves an unbound correction record
// open with its original source and failed-attempt identity.
//
// Job-aware consumption (CD-0205 D4): a failure or rejection dispatched under
// a recorded worker job stays open until an accepted result discharges its
// recorded obligation — the same discharge rule workflowCorrectionWindowBoundary
// walks. Job identity alone consumes nothing: a newer dispatch under the same
// job_id with a rewritten obligation, or an unrelated job's dispatch, or an
// acceptance without a job disposition, leaves the record open with its
// original source and failed-attempt identity. A failure with no job binding
// keeps the historical behavior: any
// newer materialized dispatch closes it.
func workflowCorrectionContextForDispatch(ctx context.Context, q queryer, workID, stepID, dispatchAttemptID string) (*WorkflowCorrectionContext, error) {
	record, err := workflowCorrectionOpenRecord(ctx, q, workID, dispatchAttemptID, "workflow_correction")
	if err != nil {
		return nil, err
	}
	if record == nil {
		return nil, nil
	}
	fields := record.fields
	sourceEventID := record.sourceID
	if fields.ActionID == "request_correction" {
		return &WorkflowCorrectionContext{
			Disposition:  "verification",
			PredicateIDs: correctionReferenceStrings(fields.CorrectionPredicates), EvidenceRefs: correctionReferenceStrings(fields.CorrectionEvidence), Diagnosis: fields.CorrectionDiagnosis, Strategy: fields.CorrectionStrategy,
			SourceEventID: sourceEventID, SourceEventSeq: record.seq,
		}, nil
	}
	if fields.AttemptID == "" && fields.ActionID != "request_correction" {
		return nil, newFailure(KindInvariantViolation, "workflow_correction", "correction completion has no worker attempt", false, "restore a verified backup into a new path with concord restore, then swap")
	}
	if stepID != "" && fields.ActionID != "request_correction" {
		if fields.StepID != "" && fields.StepID != stepID {
			return nil, nil
		}
	}
	var failureKind, failureDetail string
	if fields.AttemptID != "" {
		if err := q.QueryRowContext(ctx, `SELECT COALESCE(failure_kind,''),COALESCE(failure_detail,'') FROM worker_attempts WHERE work_id=? AND attempt_id=?`, workID, fields.AttemptID).Scan(&failureKind, &failureDetail); err != nil && err != sql.ErrNoRows {
			return nil, wrapFailure(KindUnavailable, "workflow_correction", "cannot read worker attempt detail", true, "retry once the worker attempt projection is readable", err)
		}
	}
	if fields.ActionID == "record_worker_failure" && fields.CorrectionDiagnosis == "" {
		fields.CorrectionDiagnosis = failureDetail
		if fields.CorrectionDiagnosis == "" {
			fields.CorrectionDiagnosis = "worker attempt failed"
		}
	}
	if fields.ActionID == "record_worker_failure" && fields.CorrectionStrategy == "" {
		fields.CorrectionStrategy = "retry with a fresh fenced attempt"
	}
	return &WorkflowCorrectionContext{
		Disposition:  dispositionForCorrection(fields.ActionID),
		PredicateIDs: correctionReferenceStrings(fields.CorrectionPredicates), EvidenceRefs: correctionReferenceStrings(append(fields.CorrectionEvidence, fields.ResultEvidence...)),
		Diagnosis: fields.CorrectionDiagnosis, Strategy: fields.CorrectionStrategy, FailureKind: failureKind, FailureDetail: failureDetail,
		FailedAttemptID: fields.AttemptID, FailedAttemptEpoch: fields.AttemptEpoch,
		SourceEventID: sourceEventID, SourceEventSeq: record.seq,
	}, nil
}

// workflowCorrectionOpenRecord includes acceptance at the current watermark.
// Job-bound records count through that prefix until their obligations are
// satisfied. Unbound records count through their own sequence and close only
// after a materialized dispatch, excluding the packet's own attempt so it
// can still read the record it consumes.
func workflowCorrectionOpenRecord(ctx context.Context, q queryer, workID, dispatchAttemptID, subject string) (*workflowCorrectionRecordState, error) {
	history, err := workflowCorrectionWalkWorkerSatisfaction(ctx, q, workID, 0, subject)
	if err != nil {
		return nil, err
	}
	return history.openRecord(dispatchAttemptID), nil
}

// correctionReferenceStrings keeps only the entries a work pin correction may
// carry. Evidence locators are admissible at 1 to 2048 bytes on the tool
// surface, while the correction projection's predicate and evidence lists must
// satisfy the reference rule the closed pin schema states. Entries outside
// that rule stay in the durable completion payload and drop out of the bounded
// pin view instead of failing the whole mutation result.
func correctionReferenceStrings(values []string) []string {
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if !ValidReference(value) {
			continue
		}
		if _, duplicate := seen[value]; duplicate {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func dispositionForCorrection(actionID string) string {
	if actionID == "request_correction" {
		return "requested"
	}
	if actionID == "reject_worker_result" {
		return "rejected"
	}
	return "failed"
}

// workflowRejectableResult is one eligible completed worker result.
type workflowRejectableResult struct {
	AttemptID    string
	AttemptEpoch int64
	ExecutorRef  string
	DispatchSeq  int64
	completed    bool
}

// workflowRejectedWorkerResultAvailable reports whether the step holds a completed worker result
// still open to rejection at the current settled prefix.
func workflowRejectedWorkerResultAvailable(ctx context.Context, q queryer, workID string, definition WorkflowDefinition, currentStep, subject string) (bool, error) {
	results, err := workflowRejectableWorkerResults(ctx, q, workID, definition, currentStep, subject, 0)
	if err != nil {
		return false, err
	}
	return len(results) > 0, nil
}

// validateWorkerResultRejection admits only an exact attempt/epoch pair the reader lists at the same bound.
func validateWorkerResultRejection(ctx context.Context, q queryer, workID string, definition WorkflowDefinition, currentStep, subject, attemptID string, epoch, beforeSeq int64) (workflowRejectableResult, error) {
	results, err := workflowRejectableWorkerResults(ctx, q, workID, definition, currentStep, subject, beforeSeq)
	if err != nil {
		return workflowRejectableResult{}, err
	}
	for _, result := range results {
		if result.AttemptID == attemptID && result.AttemptEpoch == epoch {
			return result, nil
		}
	}
	return workflowRejectableResult{}, newFailure(KindIllegalLifecycleTransition, subject, "worker result rejection does not name an eligible completed result", false, "reread the work pin and reject one of its eligible results")
}

// workflowRejectableWorkerResults reads the completed worker results still open to rejection before
// beforeSeq (exclusive; 0 reads the current settled prefix), each joined through its dispatch's recorded authorization.
func workflowRejectableWorkerResults(ctx context.Context, q queryer, workID string, definition WorkflowDefinition, currentStep, subject string, beforeSeq int64) ([]workflowRejectableResult, error) {
	if currentStep == "" || !stepDeclaresAction(definition, currentStep, "dispatch_worker") {
		return nil, nil
	}
	latestStartSeq, latestStartEpoch, latestStartFound, err := latestWorkflowActionStartAt(ctx, q, workID, currentStep, beforeSeq)
	if err != nil {
		return nil, err
	}
	type resultEvent struct {
		seq     int64
		kind    string
		payload []byte
	}
	var events []resultEvent
	rows, err := q.QueryContext(ctx, `SELECT seq,kind,payload FROM domain_events WHERE subject_type=? AND subject_id=? AND (?=0 OR seq<?) AND kind IN (?,?,?,?,?,?) ORDER BY seq`, string(SubjectWorkItem), workID, beforeSeq, beforeSeq, WorkflowActionStarted, WorkflowActionCompleted, WorkflowContractSuperseded, WorkerDispatched, WorkerCompleted, WorkerFailed)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, subject, "cannot read the worker result history", true, "retry once the workflow event log is readable", err)
	}
	for rows.Next() {
		var event resultEvent
		if err := rows.Scan(&event.seq, &event.kind, &event.payload); err != nil {
			rows.Close()
			return nil, wrapFailure(KindUnavailable, subject, "cannot scan the worker result history", true, "retry once the workflow event log is readable", err)
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, wrapFailure(KindUnavailable, subject, "cannot scan the worker result history", true, "retry once the workflow event log is readable", err)
	}
	if err := rows.Close(); err != nil {
		return nil, wrapFailure(KindUnavailable, subject, "cannot close the worker result history", true, "retry once the workflow event log is readable", err)
	}
	var held []*workflowRejectableResult
	live := make(map[string]*workflowRejectableResult)
	boundary := int64(0)
	checkpoint := workflowStep(definition, currentStep).Kind == WorkflowStepHumanCheckpoint
	closeInterval := func(seq int64) {
		boundary = seq
		held = nil
		live = make(map[string]*workflowRejectableResult)
	}
	for _, event := range events {
		switch event.kind {
		case string(WorkflowContractSuperseded):
			closeInterval(event.seq)
		case string(WorkflowActionStarted):
			var p workflowActionStartedPayload
			if err := json.Unmarshal(event.payload, &p); err != nil {
				return nil, newFailure(KindInvariantViolation, subject, "workflow action start payload is malformed", false, "restore a verified backup into a new path with concord restore, then swap")
			}
			if p.ActionID != "dispatch_worker" || checkpoint {
				closeInterval(event.seq)
			}
		case string(WorkflowActionCompleted):
			var p workflowActionCompletedPayload
			if err := json.Unmarshal(event.payload, &p); err != nil {
				return nil, newFailure(KindInvariantViolation, subject, "workflow action completion payload is malformed", false, "restore a verified backup into a new path with concord restore, then swap")
			}
			if workflowCompletedActionAdvances(definition, p) || p.ActionID == "request_correction" {
				closeInterval(event.seq)
			} else if p.ActionID == "accept_worker_result" || p.ActionID == "accept_worker_evidence" || p.ActionID == "reject_worker_result" {
				delete(live, p.WorkerAttemptID)
			}
		case string(WorkerDispatched):
			var p WorkerDispatchedPayload
			if err := json.Unmarshal(event.payload, &p); err != nil {
				return nil, newFailure(KindInvariantViolation, subject, "worker dispatched payload is malformed", false, "restore a verified backup into a new path with concord restore, then swap")
			}
			candidate := &workflowRejectableResult{AttemptID: p.AttemptID, ExecutorRef: p.LaneActorRef, DispatchSeq: event.seq}
			var window WorkerDispatchWindow
			var windowErr error
			if p.LaneActorRef != "" {
				window, windowErr = findAuthorizedDispatchWindowBefore(ctx, q, workID, p.AttemptID, event.seq)
			}
			var failure *Failure
			if windowErr != nil && (!failureAs(windowErr, &failure) || failure.Kind != KindUnauthorizedDispatch) {
				return nil, windowErr
			}
			if windowErr == nil && p.LaneActorRef != "" {
				if window.StepID != currentStep || window.StartSeq < boundary || window.StartSeq >= event.seq {
					continue
				}
				candidate.AttemptEpoch = window.AttemptEpoch
			} else {
				// No recorded authorization or executor: the attempt
				// counts only under the step's latest start at this bound — no cross-epoch authority.
				if !latestStartFound || event.seq <= latestStartSeq {
					continue
				}
				candidate.AttemptEpoch = latestStartEpoch
				candidate.ExecutorRef = ""
			}
			held = append(held, candidate)
			live[p.AttemptID] = candidate
		case string(WorkerCompleted):
			var p WorkerCompletedPayload
			if err := json.Unmarshal(event.payload, &p); err != nil {
				return nil, newFailure(KindInvariantViolation, subject, "worker completion payload is malformed", false, "restore a verified backup into a new path with concord restore, then swap")
			}
			if candidate := live[p.AttemptID]; candidate != nil && p.ReadbackModel != "" {
				candidate.completed = true
			}
		case string(WorkerFailed):
			var p WorkerFailedPayload
			if err := json.Unmarshal(event.payload, &p); err != nil {
				return nil, newFailure(KindInvariantViolation, subject, "worker failure payload is malformed", false, "restore a verified backup into a new path with concord restore, then swap")
			}
			delete(live, p.AttemptID)
		}
	}
	var results []workflowRejectableResult
	for _, candidate := range held {
		if candidate.completed && live[candidate.AttemptID] == candidate {
			results = append(results, *candidate)
		}
	}
	return results, nil
}

// validateWorkerPacketCorrection refuses a packet that does not consume the
// current correction context. Dispatch admission stays with workflowAdmit.
func validateWorkerPacketCorrection(ctx context.Context, q queryer, workID, currentStep string, packetRaw json.RawMessage) error {
	var packet struct {
		AttemptID string `json:"attempt_id"`
		Inputs    struct {
			Correction *WorkflowCorrectionContext `json:"correction"`
		} `json:"inputs"`
	}
	if err := json.Unmarshal(packetRaw, &packet); err != nil {
		return newFailure(KindInvalidPayload, "workflow_action", "dispatch_worker worker_packet is malformed", false, "supply the lane packet bound to this work item and attempt")
	}
	correction, err := workflowCorrectionContextForDispatch(ctx, q, workID, currentStep, packet.AttemptID)
	if err != nil {
		return err
	}
	if correction == nil {
		if packet.Inputs.Correction != nil {
			return newFailure(KindInvalidPayload, "workflow_action", "worker packet carries correction context without a durable correction", false, "build the packet from the current work pin")
		}
		return nil
	}
	if packet.Inputs.Correction == nil || !sameWorkflowCorrection(packet.Inputs.Correction, correction) {
		return newFailure(KindInvalidPayload, "workflow_action", "worker packet does not consume the current correction context", false, "build a fresh packet from the current work pin")
	}
	return nil
}

func sameWorkflowCorrection(left, right *WorkflowCorrectionContext) bool {
	return left.SourceEventID == right.SourceEventID && left.SourceEventSeq == right.SourceEventSeq && left.FailedAttemptID == right.FailedAttemptID && left.FailedAttemptEpoch == right.FailedAttemptEpoch && left.Disposition == right.Disposition && left.Diagnosis == right.Diagnosis && left.Strategy == right.Strategy && left.FailureKind == right.FailureKind && left.FailureDetail == right.FailureDetail && sameCorrectionStrings(left.PredicateIDs, right.PredicateIDs) && sameCorrectionStrings(left.EvidenceRefs, right.EvidenceRefs)
}

func sameCorrectionStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

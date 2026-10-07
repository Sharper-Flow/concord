package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// workflowDeliveryAdmission is the comparable classification of the delivery
// epoch. Raw proof commands stay outside the abstract state for tooling checks.
type workflowDeliveryAdmission struct {
	Started           bool
	ProofRequired     bool
	ProofReady        bool
	ProofDisqualifier string
	ProofFailure      *Failure
	Failure           *Failure
	// Jobs facet (CD-0205 D3): on a job-capable pin whose work has recorded
	// worker jobs, a delivery assertion additionally requires every required
	// job's latest revision satisfied and qualifying core-owned worktree
	// verification evidence bound after the required job acceptances and the
	// phase start, covering every required Project. Local acceptances, their
	// count, and a binding that merely mentions one acceptance prove no
	// integration and no delivery. JobsUnsatisfiedKeys is the deterministic
	// "job|revision" join of the unsatisfied latest revisions and
	// JobsScopeKeys the deterministic join of the required Projects, so the
	// facet stays comparable.
	JobsRecorded        bool
	JobsUnsatisfiedKeys string
	JobsScopeKeys       string
	JobsIntegrated      bool
}

// Runs retain binding order: a malformed operation refuses when reached,
// while a qualifying earlier run settles the proof without reading past it.
// Scope is empty for the refine-proof runs and carries the verify lease's
// Project for the integration runs, so the tooling check can re-validate the
// same per-Project coverage the admission derived.
type workflowVerificationRun struct {
	Command      []string
	Scope        string
	Disqualifier string
	Failure      error
}

func loadWorkflowDeliveryAdmission(ctx context.Context, q queryer, workID string, definition WorkflowDefinition, currentStep string) (workflowDeliveryAdmission, []workflowVerificationRun, []workflowVerificationRun, error) {
	state := workflowDeliveryAdmission{ProofRequired: workflowRefineProofGateActive(definition, currentStep)}
	startStep := currentStep
	if workflowStepIsDeliveryGate(workflowStep(definition, currentStep)) {
		for _, edge := range definition.StepGraph.Edges {
			if edge.To == currentStep && edge.Kind == WorkflowEdgeForward {
				startStep = edge.From
				break
			}
		}
	}
	startSeq, _, found, err := latestWorkflowActionStart(ctx, q, workID, startStep)
	if err != nil {
		return state, nil, nil, err
	}
	state.Started = found
	integrationRuns, err := loadWorkflowDeliveryJobAdmission(ctx, q, workID, definition, startSeq, found, &state)
	if err != nil {
		return state, nil, nil, err
	}
	if !state.ProofRequired || !found {
		return state, nil, integrationRuns, nil
	}
	startedAt, err := workflowActionOccurredAt(ctx, q, workID, startSeq)
	if err != nil {
		var failure *Failure
		if !failureAs(err, &failure) || failure.Kind == KindUnavailable {
			return state, nil, nil, err
		}
		state.Failure = failure
		return state, nil, nil, nil
	}
	refs, err := workflowVerificationBindingsAfter(ctx, q, workID, startSeq, "workflow_action")
	if err != nil {
		return state, nil, nil, err
	}
	var runs []workflowVerificationRun
	for _, ref := range refs {
		command, _, why, err := loadWorkflowVerifyRun(ctx, q, workID, ref, startedAt, "workflow_action")
		if err != nil {
			var failure *Failure
			if !failureAs(err, &failure) || failure.Kind == KindUnavailable {
				return state, nil, nil, err
			}
		}
		runs = append(runs, workflowVerificationRun{Command: command, Disqualifier: why, Failure: err})
	}
	state.ProofReady, state.ProofDisqualifier, err = workflowVerificationProof(runs, nil)
	if err != nil {
		state.ProofFailure = workflowFailureOf(err)
	}
	return state, runs, integrationRuns, nil
}

func workflowVerificationProof(runs []workflowVerificationRun, tooling *ProjectToolingManifest) (bool, string, error) {
	if len(runs) == 0 {
		return false, "no verification evidence is bound in the current refine epoch", nil
	}
	first := ""
	for _, run := range runs {
		if run.Failure != nil {
			return false, "", run.Failure
		}
		why := run.Disqualifier
		if why == "" {
			if tooling == nil || ProjectToolingInvocationDeclared(tooling, run.Command) {
				return true, "", nil
			}
			why = "the bound verify run's command is not a tool the Project declares"
		}
		if first == "" {
			first = why
		}
	}
	return false, first, nil
}

func workflowRefineProofFailure(why string) *Failure {
	return newFailure(KindMissingEvidence, "workflow_action",
		"leaving refine requires a verification binding in the current refine epoch that names a green worktree_verify run for this work item: "+why,
		false, "run worktree_verify on the refined work, then bind_evidence with evidence_kind verification and the run's worktree_verify operation ref before record_delivery")
}

// loadWorkflowDeliveryJobAdmission loads the CD-0205 job facet of the delivery
// admission: the required jobs' latest revisions with their dispositions, and
// whether qualifying core-owned worktree verification evidence integrates the
// required job result population. Integration derives from verification
// evidence bindings whose immutable subject names a completed worktree.verify
// durable operation — successful declared tooling, unchanged tracked tree —
// whose lease runs under a required Project, bound after every required job's
// recorded acceptance and after the phase start, in log order. A binding that
// merely mentions an acceptance, the count of local acceptances, or timestamps
// alone prove nothing: the binding order and the reference scope are the
// derivation. On definitions that predate the worker-job lifecycle the facet
// stays empty and no delivery changes behavior or digest.
func loadWorkflowDeliveryJobAdmission(ctx context.Context, q queryer, workID string, definition WorkflowDefinition, startSeq int64, startFound bool, state *workflowDeliveryAdmission) ([]workflowVerificationRun, error) {
	if !workflowWorkerJobsActive(definition) {
		return nil, nil
	}
	rows, err := q.QueryContext(ctx, `SELECT j.job_id, j.revision, j.state, COALESCE(j.satisfied_result_ref,''), COALESCE(j.project_scope,'') FROM worker_job_revisions j WHERE j.work_id=? AND j.revision=(SELECT MAX(latest.revision) FROM worker_job_revisions latest WHERE latest.work_id=j.work_id AND latest.job_id=j.job_id) ORDER BY j.job_id`, workID)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "workflow_action", "cannot read the worker-job dispositions for delivery", true, "retry once the worker-job projection is readable", err)
	}
	defer func() { _ = rows.Close() }()
	var unsatisfied []string
	scopes := make([]string, 0, 4)
	requiredScopes := map[string]bool{}
	acceptRefs := make([]string, 0, 4)
	requiredRevisions := make([]string, 0, 4)
	for rows.Next() {
		var jobID, jobState, resultRef, scope string
		var revision int64
		if err := rows.Scan(&jobID, &revision, &jobState, &resultRef, &scope); err != nil {
			return nil, wrapFailure(KindUnavailable, "workflow_action", "cannot scan the worker-job dispositions for delivery", true, "retry once the worker-job projection is readable", err)
		}
		state.JobsRecorded = true
		requiredRevisions = append(requiredRevisions, workerJobKey(jobID, revision))
		if !requiredScopes[scope] {
			requiredScopes[scope] = true
			scopes = append(scopes, scope)
		}
		if jobState != "satisfied" {
			unsatisfied = append(unsatisfied, workerJobKey(jobID, revision))
		} else if resultRef != "" {
			acceptRefs = append(acceptRefs, resultRef)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, wrapFailure(KindUnavailable, "workflow_action", "cannot scan the worker-job dispositions for delivery", true, "retry once the worker-job projection is readable", err)
	}
	state.JobsUnsatisfiedKeys = strings.Join(unsatisfied, ",")
	state.JobsScopeKeys = strings.Join(scopes, ",")
	if !state.JobsRecorded {
		return nil, nil
	}
	// The integration epoch opens at the latest of the phase start, every
	// required job's recorded acceptance, and every required job's recorded
	// result population: the latest worker.completed event of an attempt
	// dispatched under each required latest revision. The verify run must
	// have executed and bound after those results, so it observed the
	// integrated whole — including the pending final job a combined
	// acceptance counts as satisfied, whose own completion is in the log
	// before the acceptance that asserts delivery. A satisfied revision
	// refuses redispatch, so on the standalone route the acceptance of the
	// result still dominates its own completion and the derivation keeps its
	// recorded shape. Order is the log's seq, not a timestamp; the
	// occurred_at clock only bounds the lease's acquire time, as the refine
	// proof already does.
	coverageSeq := startSeq
	anchorAt := time.Time{}
	if startFound {
		occurred, err := workflowActionOccurredAt(ctx, q, workID, startSeq)
		if err != nil {
			return nil, err
		}
		anchorAt = occurred
	}
	for _, ref := range acceptRefs {
		var seq int64
		var occurred string
		if err := q.QueryRowContext(ctx, `SELECT seq,occurred_at FROM domain_events WHERE subject_type='work_item' AND subject_id=? AND event_id=?`, workID, ref).Scan(&seq, &occurred); err != nil {
			if err == sql.ErrNoRows {
				return nil, newFailure(KindInvariantViolation, "workflow_action", "a satisfied worker job names an acceptance that the event log does not hold", false, "rebuild the worker-job projection from the event log")
			}
			return nil, wrapFailure(KindUnavailable, "workflow_action", "cannot read the required job acceptance for delivery", true, "retry once the event log is readable", err)
		}
		if seq > coverageSeq {
			coverageSeq = seq
		}
		if at, err := time.Parse(time.RFC3339Nano, occurred); err == nil && at.After(anchorAt) {
			anchorAt = at
		}
	}
	completions, err := q.QueryContext(ctx, `SELECT COALESCE(json_extract(payload,'$.worker_job.job_id'),''),COALESCE(json_extract(payload,'$.worker_job.revision'),0),seq,occurred_at FROM domain_events WHERE subject_type='work_item' AND subject_id=? AND kind=? AND seq>?`, workID, WorkerCompleted, startSeq)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "workflow_action", "cannot read the required job result population for delivery", true, "retry once the event log is readable", err)
	}
	defer func() { _ = completions.Close() }()
	required := map[string]bool{}
	for _, key := range requiredRevisions {
		required[key] = true
	}
	for completions.Next() {
		var jobID, occurred string
		var revision, seq int64
		if err := completions.Scan(&jobID, &revision, &seq, &occurred); err != nil {
			return nil, wrapFailure(KindUnavailable, "workflow_action", "cannot scan the required job result population for delivery", true, "retry once the event log is readable", err)
		}
		if jobID == "" || !required[workerJobKey(jobID, revision)] {
			continue
		}
		if seq > coverageSeq {
			coverageSeq = seq
		}
		if at, err := time.Parse(time.RFC3339Nano, occurred); err == nil && at.After(anchorAt) {
			anchorAt = at
		}
	}
	if err := completions.Err(); err != nil {
		return nil, wrapFailure(KindUnavailable, "workflow_action", "cannot enumerate the required job result population for delivery", true, "retry once the event log is readable", err)
	}
	refs, err := workflowVerificationBindingsAfter(ctx, q, workID, coverageSeq, "workflow_action")
	if err != nil {
		return nil, err
	}
	covered := map[string]bool{}
	runs := make([]workflowVerificationRun, 0, len(refs))
	for _, ref := range refs {
		command, project, why, err := loadWorkflowVerifyRun(ctx, q, workID, ref, anchorAt, "workflow_action")
		if err != nil {
			var failure *Failure
			if !failureAs(err, &failure) || failure.Kind == KindUnavailable {
				return nil, err
			}
			// A malformed projection fails closed: the run qualifies for no
			// Project until the projection is rebuilt.
		}
		runs = append(runs, workflowVerificationRun{Command: command, Scope: project, Disqualifier: why, Failure: err})
		if err == nil && why == "" {
			covered[project] = true
		}
	}
	state.JobsIntegrated = workflowIntegrationCovered(scopes, covered)
	return runs, nil
}

// workflowIntegrationCovered decides per-Project coverage: every required
// non-empty scope needs one qualifying verify run of exactly that Project; a
// work-scoped job (empty scope) is covered by any qualifying run.
func workflowIntegrationCovered(scopes []string, covered map[string]bool) bool {
	any := false
	for _, project := range covered {
		_ = project
		any = true
		break
	}
	for _, scope := range scopes {
		if scope == "" {
			if !any {
				return false
			}
			continue
		}
		if !covered[scope] {
			return false
		}
	}
	return true
}

// workflowIntegrationScopes splits the deterministic required-scope join.
func workflowIntegrationScopes(keys string) []string {
	if keys == "" {
		return nil
	}
	return strings.Split(keys, ",")
}

// workflowDeliveryJobsFailure is the shared refusal of a delivery assertion
// behind unsatisfied required jobs or without qualifying integration
// evidence. The accepting attempt's own dispatched revision counts as
// satisfied for the population check (CD-0205 D5): the delivery-asserting
// accept records that disposition in the same event that asserts delivery.
// That pending satisfaction never supplies integration — integration derives
// only from qualifying verification evidence, on both routes alike.
func workflowDeliveryJobsFailure(state workflowDeliveryAdmission, accepting *WorkerJobBinding) *Failure {
	unsatisfied := workerJobKeys(state.JobsUnsatisfiedKeys)
	if accepting != nil {
		remaining := make([]string, 0, len(unsatisfied))
		self := workerJobKey(accepting.JobID, accepting.Revision)
		for _, key := range unsatisfied {
			if key != self {
				remaining = append(remaining, key)
			}
		}
		unsatisfied = remaining
	}
	if len(unsatisfied) != 0 {
		return newFailure(KindMissingEvidence, "workflow_action",
			fmt.Sprintf("the delivery assertion requires every recorded worker job satisfied: %d required job revision(s) remain unsatisfied", len(unsatisfied)),
			false, "satisfy or re-record the unsatisfied worker jobs before asserting whole-work delivery")
	}
	if state.JobsRecorded && !state.JobsIntegrated {
		return newFailure(KindMissingEvidence, "workflow_action",
			"the delivery assertion requires qualifying core-owned worktree verification evidence bound after the recorded worker-job acceptances, covering every required Project",
			false, "run worktree_verify on the integrated work after the accepted job results, bind the run's verification evidence for each required Project, then record delivery")
	}
	return nil
}

// workerJobKeys splits the deterministic unsatisfied-keys join. Job ids and
// the key separator cannot contain a comma, so the split is unambiguous.
func workerJobKeys(keys string) []string {
	if keys == "" {
		return nil
	}
	return strings.Split(keys, ",")
}

func workflowAdmitDelivery(state WorkflowAdmissionState, decision WorkflowAdmissionDecision) WorkflowAdmissionDecision {
	return workflowAdmitDeliveryForAccept(state, decision, nil)
}

// workflowAdmitDeliveryForAccept is the one complete delivery admission
// (CD-0205 D3/D5): standalone record_delivery and the delivery-asserting
// accept run the identical derivation — the fenced start, the refine proof,
// and the worker-job facet with its integration evidence. Only the accepting
// parameter differs: the accepting attempt's own dispatched revision counts
// as satisfied for the population check, because one completion records that
// disposition and the delivery assertion together. Its pending satisfaction
// never supplies integration, so a combined acceptance behind missing
// integration evidence refuses exactly as record_delivery does.
func workflowAdmitDeliveryForAccept(state WorkflowAdmissionState, decision WorkflowAdmissionDecision, accepting *WorkerJobBinding) WorkflowAdmissionDecision {
	if state.Delivery.Failure != nil {
		decision.Failure = state.Delivery.Failure
		return decision
	}
	if !state.Delivery.Started {
		decision.Failure = newFailure(KindInvalidOperation, "workflow_action", "record_delivery requires the delivery step's fenced start action in this attempt", false, "start the delivery-bearing step, do its work, then record delivery")
		return decision
	}
	if state.Delivery.ProofRequired {
		if state.Delivery.ProofFailure != nil {
			decision.Failure = state.Delivery.ProofFailure
			return decision
		}
		if !state.Delivery.ProofReady {
			decision.DeliveryProofMissing = true
			decision.Failure = workflowRefineProofFailure(state.Delivery.ProofDisqualifier)
			return decision
		}
	}
	if jobs := workflowDeliveryJobsFailure(state.Delivery, accepting); jobs != nil {
		decision.Failure = jobs
		return decision
	}
	return decision
}

// workflowIntegrationToolingFailure re-validates the integration derivation
// against the request's declared tooling manifest, the same split the refine
// proof keeps: the comparable state decides over qualifying runs, and the
// guard requires the qualifying runs' commands to be tools the Project
// declares. Every required Project keeps a declared qualifying run or the
// delivery assertion refuses.
func workflowIntegrationToolingFailure(delivery workflowDeliveryAdmission, runs []workflowVerificationRun, tooling *ProjectToolingManifest) *Failure {
	if !delivery.JobsRecorded || !delivery.JobsIntegrated || tooling == nil {
		return nil
	}
	scopes := workflowIntegrationScopes(delivery.JobsScopeKeys)
	declared := map[string]bool{}
	for _, run := range runs {
		if run.Failure != nil || run.Disqualifier != "" {
			continue
		}
		if ProjectToolingInvocationDeclared(tooling, run.Command) {
			declared[run.Scope] = true
		}
	}
	if !workflowIntegrationCovered(scopes, declared) {
		return newFailure(KindMissingEvidence, "workflow_action",
			"the delivery assertion's integration evidence must run commands the Project declares, covering every required Project",
			false, "run worktree_verify with the Project's declared tooling on the integrated work, then bind its verification evidence")
	}
	return nil
}

// The external declaration adds diagnostic context, not another admission
// decision. The shared refusal is copied so the folded answer stays immutable.
func workflowExecutionAdmissionFailure(decision WorkflowAdmissionDecision, tooling *ProjectToolingManifest) *Failure {
	if !decision.DeliveryProofMissing {
		return decision.Failure
	}
	failure := *decision.Failure
	failure.Detail += " " + ProjectToolingDeclaredText(tooling)
	return &failure
}

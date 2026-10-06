package store

import (
	"context"
	"fmt"
	"strings"
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
	// job's latest revision satisfied and one durable evidence binding that
	// names a satisfied job's recorded acceptance. Local acceptances and
	// their count prove no integration and no delivery. JobsUnsatisfiedKeys
	// is the deterministic "job|revision" join of the unsatisfied latest
	// revisions, so the facet stays comparable.
	JobsRecorded        bool
	JobsUnsatisfiedKeys string
	JobsIntegrated      bool
}

// Runs retain binding order: a malformed operation refuses when reached,
// while a qualifying earlier run settles the proof without reading past it.
type workflowVerificationRun struct {
	Command      []string
	Disqualifier string
	Failure      error
}

func loadWorkflowDeliveryAdmission(ctx context.Context, q queryer, workID string, definition WorkflowDefinition, currentStep string) (workflowDeliveryAdmission, []workflowVerificationRun, error) {
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
		return state, nil, err
	}
	state.Started = found
	if err := loadWorkflowDeliveryJobAdmission(ctx, q, workID, definition, &state); err != nil {
		return state, nil, err
	}
	if !state.ProofRequired || !found {
		return state, nil, nil
	}
	startedAt, err := workflowActionOccurredAt(ctx, q, workID, startSeq)
	if err != nil {
		var failure *Failure
		if !failureAs(err, &failure) || failure.Kind == KindUnavailable {
			return state, nil, err
		}
		state.Failure = failure
		return state, nil, nil
	}
	refs, err := workflowVerificationBindingsAfter(ctx, q, workID, startSeq, "workflow_action")
	if err != nil {
		return state, nil, err
	}
	var runs []workflowVerificationRun
	for _, ref := range refs {
		command, why, err := loadWorkflowVerifyRun(ctx, q, workID, ref, startedAt, "workflow_action")
		if err != nil {
			var failure *Failure
			if !failureAs(err, &failure) || failure.Kind == KindUnavailable {
				return state, nil, err
			}
		}
		runs = append(runs, workflowVerificationRun{Command: command, Disqualifier: why, Failure: err})
	}
	state.ProofReady, state.ProofDisqualifier, err = workflowVerificationProof(runs, nil)
	if err != nil {
		state.ProofFailure = workflowFailureOf(err)
	}
	return state, runs, nil
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
// whether one durable evidence binding names a satisfied job's recorded
// acceptance — the integration binding that ties the delivered whole to its
// accepted job results. On definitions that predate the worker-job lifecycle
// the facet stays empty and no delivery changes behavior or digest.
func loadWorkflowDeliveryJobAdmission(ctx context.Context, q queryer, workID string, definition WorkflowDefinition, state *workflowDeliveryAdmission) error {
	if !workflowWorkerJobsActive(definition) {
		return nil
	}
	rows, err := q.QueryContext(ctx, `SELECT j.job_id, j.revision, j.state, COALESCE(j.satisfied_result_ref,'') FROM worker_job_revisions j WHERE j.work_id=? AND j.revision=(SELECT MAX(latest.revision) FROM worker_job_revisions latest WHERE latest.work_id=j.work_id AND latest.job_id=j.job_id) ORDER BY j.job_id`, workID)
	if err != nil {
		return wrapFailure(KindUnavailable, "workflow_action", "cannot read the worker-job dispositions for delivery", true, "retry once the worker-job projection is readable", err)
	}
	defer func() { _ = rows.Close() }()
	var accepted []string
	var unsatisfied []string
	for rows.Next() {
		var jobID, jobState, resultRef string
		var revision int64
		if err := rows.Scan(&jobID, &revision, &jobState, &resultRef); err != nil {
			return wrapFailure(KindUnavailable, "workflow_action", "cannot scan the worker-job dispositions for delivery", true, "retry once the worker-job projection is readable", err)
		}
		state.JobsRecorded = true
		if resultRef != "" {
			accepted = append(accepted, resultRef)
		}
		if jobState != "satisfied" {
			unsatisfied = append(unsatisfied, workerJobKey(jobID, revision))
		}
	}
	state.JobsUnsatisfiedKeys = strings.Join(unsatisfied, ",")
	if err := rows.Err(); err != nil {
		return wrapFailure(KindUnavailable, "workflow_action", "cannot scan the worker-job dispositions for delivery", true, "retry once the worker-job projection is readable", err)
	}
	if len(accepted) == 0 {
		return nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(accepted)), ",")
	args := make([]any, 0, len(accepted)+3)
	args = append(args, string(SubjectWorkItem), workID, WorkflowEvidenceBound)
	for _, ref := range accepted {
		args = append(args, ref)
	}
	var bound int
	if err := q.QueryRowContext(ctx, `SELECT count(*) FROM domain_events WHERE subject_type=? AND subject_id=? AND kind=? AND json_extract(payload,'$.immutable_subject_ref') IN (`+placeholders+`)`, args...).Scan(&bound); err != nil {
		return wrapFailure(KindUnavailable, "workflow_action", "cannot read the integration evidence bindings", true, "retry once the workflow evidence projection is readable", err)
	}
	state.JobsIntegrated = bound != 0
	return nil
}

// workflowDeliveryJobsFailure is the shared refusal of a delivery assertion
// behind unsatisfied required jobs or without an integration binding. The
// accepting attempt's own dispatched revision is passed as satisfied when the
// delivery-asserting accept itself records that disposition in the same event
// (CD-0205 D5): its binding is integrated into the delivered whole by the
// very completion that asserts delivery, so the route keeps one derivation
// over the post-event state.
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
	if state.JobsRecorded && !state.JobsIntegrated && accepting == nil {
		return newFailure(KindMissingEvidence, "workflow_action",
			"the delivery assertion requires integration evidence bound to the accepted worker-job results",
			false, "bind evidence whose immutable subject is the recorded acceptance that satisfied a required worker job, then record delivery")
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
	if jobs := workflowDeliveryJobsFailure(state.Delivery, nil); jobs != nil {
		decision.Failure = jobs
		return decision
	}
	return decision
}

// workflowAdmitDeliveryForAccept is the delivery-asserting accept's use of the
// same delivery admission (CD-0205 D5): identical to workflowAdmitDelivery
// except that the accepting attempt's own dispatched revision counts as
// satisfied, because the accept's completion records that disposition in the
// same event that asserts delivery.
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

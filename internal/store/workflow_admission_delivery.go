package store

import (
	"context"
	"fmt"
	"sort"
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
	// job's latest revision satisfied and qualifying core-owned worktree
	// verification evidence on the current candidate, covering every required
	// Project. Local acceptances, their count, and a binding that mentions an acceptance prove no
	// integration and no delivery. JobsUnsatisfiedKeys is the deterministic
	// "job|revision" join of the unsatisfied latest revisions and
	// JobsScopeKeys the deterministic join of the required Projects, so the
	// facet stays comparable.
	JobsRecorded        bool
	JobsUnsatisfiedKeys string
	JobsScopeKeys       string
	JobsIntegrated      bool
	// ChecksRepairReviewDebt reports an open unconfirmed checks repair
	// (CD-0205 D4): delivery refuses while the funded confirming review's
	// debt stands, whatever the required jobs' dispositions read.
	ChecksRepairReviewDebt  bool
	ChecksRepairPendingKeys string
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
	_, _, found, err := latestWorkflowActionStart(ctx, q, workID, startStep)
	if err != nil {
		return state, nil, nil, err
	}
	state.Started = found
	integrationRuns, err := loadWorkflowDeliveryJobAdmission(ctx, q, workID, definition, &state)
	if err != nil {
		return state, nil, nil, err
	}
	if !state.ProofRequired || !found {
		return state, nil, integrationRuns, nil
	}
	refs, err := workflowVerificationBindingsAfter(ctx, q, workID, 0, "workflow_action")
	if err != nil {
		return state, nil, nil, err
	}
	var runs []workflowVerificationRun
	for _, ref := range refs {
		command, _, why, err := workflowCandidateVerificationRun(ctx, q, workID, ref)
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
// required job result population. The producer's actual subject must equal the
// integrated candidate. Acceptance chronology cannot invalidate unchanged proof.
func loadWorkflowDeliveryJobAdmission(ctx context.Context, q queryer, workID string, definition WorkflowDefinition, state *workflowDeliveryAdmission) ([]workflowVerificationRun, error) {
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
	for rows.Next() {
		var jobID, jobState, resultRef, scope string
		var revision int64
		if err := rows.Scan(&jobID, &revision, &jobState, &resultRef, &scope); err != nil {
			return nil, wrapFailure(KindUnavailable, "workflow_action", "cannot scan the worker-job dispositions for delivery", true, "retry once the worker-job projection is readable", err)
		}
		state.JobsRecorded = true
		if !requiredScopes[scope] {
			requiredScopes[scope] = true
			scopes = append(scopes, scope)
		}
		if jobState != "satisfied" {
			unsatisfied = append(unsatisfied, workerJobKey(jobID, revision))
		}
	}
	if err := rows.Err(); err != nil {
		return nil, wrapFailure(KindUnavailable, "workflow_action", "cannot scan the worker-job dispositions for delivery", true, "retry once the worker-job projection is readable", err)
	}
	sort.Strings(unsatisfied)
	state.JobsUnsatisfiedKeys = strings.Join(unsatisfied, ",")
	state.JobsScopeKeys = strings.Join(scopes, ",")
	if !state.JobsRecorded {
		return nil, nil
	}
	// Re-recording a job cannot hide an accepted repair's review debt.
	// Pending repairs matter only when the combined route accepts them.
	watermark, err := workerChecksRepairWatermarkTx(ctx, q, workID)
	if err != nil {
		return nil, err
	}
	history, walkErr := workerChecksRepairWalkTx(ctx, q, workID, watermark)
	if walkErr != nil {
		return nil, walkErr
	}
	pendingRepairs := make([]string, 0)
	for key, repair := range history.checksRepairs {
		if repair.openUnconfirmed() {
			state.ChecksRepairReviewDebt = true
		} else if repair.acceptanceSeq == 0 && !repair.voided {
			pendingRepairs = append(pendingRepairs, key)
		}
	}
	sort.Strings(pendingRepairs)
	state.ChecksRepairPendingKeys = strings.Join(pendingRepairs, ",")
	refs, err := workflowVerificationBindingsAfter(ctx, q, workID, 0, "workflow_action")
	if err != nil {
		return nil, err
	}
	covered := map[string]bool{}
	runs := make([]workflowVerificationRun, 0, len(refs))
	for _, ref := range refs {
		command, project, why, err := workflowCandidateVerificationRun(ctx, q, workID, ref)
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

// Proof applicability follows the producer's subject, not its position relative
// to an acceptance or a stage start. An unchanged candidate needs no rerun.
func workflowCandidateVerificationRun(ctx context.Context, q queryer, workID, ref string) ([]string, string, string, error) {
	producer, err := readOracleVerifySubjectReceipt(ctx, q, workID, ref)
	if err != nil {
		return nil, "", "", err
	}
	if producer == nil {
		return nil, "", "no passing unchanged-tree producer receipt", nil
	}
	review, err := loadWorkflowReviewState(ctx, q, workID, 0)
	if err != nil {
		return nil, "", "", err
	}
	if review.Candidate == nil || producer.SubjectRef != "commit:"+review.Candidate.Commit || producer.ProjectID != review.Candidate.ProjectID {
		return producer.Command, producer.ProjectID, "verification does not cover the current integrated candidate", nil
	}
	return producer.Command, producer.ProjectID, "", nil
}

// workflowIntegrationCovered decides per-Project coverage: every required
// non-empty scope needs one qualifying verify run of exactly that Project; a
// work-scoped job (empty scope) is covered by any qualifying run.
func workflowIntegrationCovered(scopes []string, covered map[string]bool) bool {
	anyCovered := false
	for _, project := range covered {
		_ = project
		anyCovered = true
		break
	}
	for _, scope := range scopes {
		if scope == "" {
			if !anyCovered {
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

// workflowDeliveryJobsFailure is the shared refusal of a delivery assertion
// behind unsatisfied required jobs or without qualifying integration
// evidence. The accepting attempt's own dispatched revision counts as
// satisfied for the population check (CD-0205 D5): the delivery-asserting
// accept records that disposition in the same event that asserts delivery.
// That pending satisfaction never supplies integration — integration derives
// only from qualifying verification evidence, on both routes alike.
func workflowDeliveryJobsFailure(state workflowDeliveryAdmission, accepting *WorkerJobBinding) *Failure {
	unsatisfied := splitKeyJoin(state.JobsUnsatisfiedKeys)
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
			fmt.Sprintf("the delivery assertion requires every recorded worker job satisfied: %d required job revision(s) remain unsatisfied (%s)", len(unsatisfied), workflowRefExcerpt(strings.Join(unsatisfied, ", "))),
			false, "satisfy or re-record the unsatisfied worker jobs before asserting whole-work delivery")
	}
	repairDebt := state.ChecksRepairReviewDebt
	if accepting != nil {
		for _, key := range splitKeyJoin(state.ChecksRepairPendingKeys) {
			repairDebt = repairDebt || key == workerJobKey(accepting.JobID, accepting.Revision)
		}
	}
	if repairDebt {
		return newFailure(KindMissingEvidence, "workflow_action",
			"the delivery assertion refuses while an unconfirmed checks repair's confirming review debt stands",
			false, "let the funded confirming review end and accept its ship verdict before asserting whole-work delivery")
	}
	if state.JobsRecorded && !state.JobsIntegrated {
		return newFailure(KindMissingEvidence, "workflow_action",
			"the delivery assertion requires passing producer verification on the current integrated candidate, covering every required Project",
			false, "verify the integrated candidate and bind the passing producer receipt for each required Project before recording delivery")
	}
	return nil
}

// splitKeyJoin splits a deterministic comma join of worker-job keys or
// required scopes. Neither can contain a comma, so the split is unambiguous.
func splitKeyJoin(keys string) []string {
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
	scopes := splitKeyJoin(delivery.JobsScopeKeys)
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

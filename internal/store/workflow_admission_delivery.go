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

// acceptingAttempt projects the same candidate the acceptance fold will install.
// Standalone delivery leaves it empty and proves the already integrated candidate.
func loadWorkflowDeliveryAdmission(ctx context.Context, q queryer, workID string, definition WorkflowDefinition, currentStep, acceptingAttempt string) (workflowDeliveryAdmission, []workflowVerificationRun, []workflowVerificationRun, error) {
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
	integrationRuns, err := loadWorkflowDeliveryJobAdmissionForAccept(ctx, q, workID, definition, &state, acceptingAttempt)
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
		command, _, why, err := workflowCandidateVerificationRun(ctx, q, workID, ref, acceptingAttempt)
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

// loadWorkflowDeliveryJobAdmissionForAccept loads the CD-0205 job facet of the delivery
// admission: the required jobs' latest revisions with their dispositions, and
// whether qualifying core-owned worktree verification evidence integrates the
// required job result population. The producer's actual subject must equal the
// integrated candidate. Acceptance chronology cannot invalidate unchanged proof.
func loadWorkflowDeliveryJobAdmissionForAccept(ctx context.Context, q queryer, workID string, definition WorkflowDefinition, state *workflowDeliveryAdmission, acceptingAttempt string) ([]workflowVerificationRun, error) {
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
		command, project, why, err := workflowCandidateVerificationRun(ctx, q, workID, ref, acceptingAttempt)
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
func workflowCandidateVerificationRun(ctx context.Context, q queryer, workID, ref, acceptingAttempt string) ([]string, string, string, error) {
	producer, err := readOracleVerifySubjectReceipt(ctx, q, workID, ref)
	if err != nil {
		return nil, "", "", err
	}
	if producer == nil {
		return nil, "", "no passing unchanged-tree producer receipt", nil
	}
	// Only an actual producer needs singular Review dependencies. Inapplicable
	// delivery and refs without a producer must leave recovery readable.
	review, err := loadWorkflowReviewState(ctx, q, workID, 0)
	if err != nil {
		return nil, "", "", err
	}
	candidate := review.Candidate
	if acceptingAttempt != "" {
		auth, result, err := readReviewAttempt(ctx, q, workID, acceptingAttempt)
		if err != nil {
			return nil, "", "", err
		}
		pending, err := workflowAcceptedResultCandidate(ctx, q, workID, auth, result)
		if err != nil {
			return nil, "", "", err
		}
		if pending != nil {
			candidate = pending
		}
	}
	if candidate == nil || producer.SubjectRef != "commit:"+candidate.Commit || producer.ProjectID != candidate.ProjectID {
		return producer.Command, producer.ProjectID, "verification does not cover the current integrated candidate", nil
	}
	return producer.Command, producer.ProjectID, "", nil
}

// The fold and combined delivery admission must select the same candidate.
// Reading a pending result here projects its acceptance without recording it.
func workflowAcceptedResultCandidate(ctx context.Context, q queryer, workID string, auth workflowActionCompletedPayload, result WorkerCompletedPayload) (*WorkerReviewCandidate, error) {
	if result.Review != nil && auth.WorkerCapabilityClass == "review" {
		candidate := result.Review.OutputCandidate
		return &candidate, nil
	}
	if delivery := result.ObservedDelivery; delivery != nil && delivery.State == "clean" {
		project := ""
		if auth.WorkerJob != nil {
			job, err := readWorkerJobRevisionTx(ctx, q, workID, *auth.WorkerJob)
			if err != nil {
				return nil, err
			}
			project = job.ProjectScope
		}
		return &WorkerReviewCandidate{ProjectID: project, Commit: delivery.Commit, Tree: delivery.Tree}, nil
	}
	return nil, nil
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
			false, "accept the confirming Review's continue or ready_for_final_proof disposition before asserting whole-work delivery")
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

func workflowAdmitDelivery(definition WorkflowDefinition, state WorkflowAdmissionState, decision WorkflowAdmissionDecision) WorkflowAdmissionDecision {
	return workflowAdmitDeliveryForAccept(definition, state, decision, nil)
}

// workflowAdmitDeliveryForAccept is the one complete delivery admission
// (CD-0205 D3/D5): standalone record_delivery and the delivery-asserting
// accept run the identical derivation — the fenced start, the refine proof,
// and the worker-job facet with its integration evidence. The loader qualifies
// proof against the candidate the route produces. The accepting attempt's own
// dispatched revision counts as satisfied for the population check, because one completion records that
// disposition and the delivery assertion together. Its pending satisfaction
// never supplies integration, so a combined acceptance behind missing
// integration evidence refuses exactly as record_delivery does.
func workflowAdmitDeliveryForAccept(definition WorkflowDefinition, state WorkflowAdmissionState, decision WorkflowAdmissionDecision, accepting *WorkerJobBinding) WorkflowAdmissionDecision {
	if state.Review != nil && workflowDefinitionProducesDelivery(definition) && (state.Review.Candidate != nil || state.RefinementWorkflow) {
		if err := workflowAdmitReview(*state.Review, ""); err != nil {
			decision.Failure = workflowFailureOf(err)
			return decision
		}
	}
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

// A group consumes continuation once. Its remaining members do not need an
// intermediate Review, but a changed depended-on revision still invalidates it.
// Delivery and completion use the same rule without an assignment group.
func workflowAdmitReview(state WorkflowReviewState, group string) error {
	if admitted, found := state.Groups[group]; found && admitted.CorrectionRef != "" && state.Accepted != nil && state.Accepted.ReportRef == admitted.ReviewRef && state.DependenciesCurrent {
		return nil
	}
	if blocker := state.blocker(); blocker != "" {
		return reviewFailure(KindMissingEvidence, "open in-scope blocking finding "+blocker+" blocks execution progression", "obtain an evidenced closure of "+blocker+" and have Main accept that Review report")
	}
	if admitted, found := state.Groups[group]; group != "" && found {
		applicable := state.Accepted != nil && state.Candidate != nil && state.Accepted.Report.OutputCandidate == *state.Candidate
		if !applicable || admitted.ReviewRef == state.Accepted.ReportRef {
			if state.Accepted == nil || state.DependenciesCurrent {
				return nil
			}
		}
	}
	// First entry relies on preceding stage exits and their binding guards.
	// A missing Review is not evidence that execution has never run.
	if group != "" && !state.ExecutionProgressed && state.Accepted == nil {
		return nil
	}
	if group != "" && state.ContinueConsumed && state.GroupID == group && state.Accepted != nil && state.DependenciesCurrent {
		return nil
	}
	if state.Accepted == nil || state.Candidate == nil || state.Accepted.Report.OutputCandidate != *state.Candidate || !state.DependenciesCurrent {
		return reviewFailure(KindMissingEvidence, "execution progression requires an accepted Review on the current candidate and depended-on revisions", "obtain and accept a Review of the current integrated candidate; only valid preceding exits admit the first group")
	}
	disposition := state.Accepted.Report.Disposition
	if group != "" {
		if disposition != "continue" {
			return reviewFailure(KindMissingEvidence, "opening an ordinary assignment group requires accepted continue, not "+disposition, "obtain and accept continue, or follow the report's declared corrective/upstream obligation")
		}
		if state.ContinueConsumed && state.GroupID != group {
			return reviewFailure(KindOperationConflict, "this continue permission has already opened group "+state.GroupID, "finish that group, integrate its results, and obtain a new accepted continue")
		}
		return nil
	}
	if disposition != "ready_for_final_proof" {
		return reviewFailure(KindMissingEvidence, "leaving execution requires accepted ready_for_final_proof, not "+disposition, "obtain and accept ready_for_final_proof on the current candidate")
	}
	return nil
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

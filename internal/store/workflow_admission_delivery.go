package store

import (
	"context"
	"slices"
)

// Raw proof commands stay outside the comparable state for tooling checks.
type workflowDeliveryAdmission struct {
	Started           bool
	ProofRequired     bool
	ProofReady        bool
	ProofDisqualifier string
	ProofFailure      *Failure
	Failure           *Failure
}

// Runs retain binding order: a malformed operation refuses when reached,
// while a qualifying earlier run settles the proof without reading past it.
type workflowVerificationRun struct {
	Command      []string
	Disqualifier string
	Failure      error
}

// Tooling adds diagnostic context without changing the folded decision.
func workflowExecutionAdmissionFailure(decision WorkflowAdmissionDecision, tooling *ProjectToolingManifest) *Failure {
	if !decision.DeliveryProofMissing {
		return decision.Failure
	}
	failure := *decision.Failure
	failure.Detail += " " + ProjectToolingDeclaredText(tooling)
	return &failure
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
	_, _, found, err := latestWorkflowActionStart(ctx, q, workID, startStep)
	if err != nil {
		return state, nil, err
	}
	state.Started = found
	if !state.ProofRequired || !found {
		return state, nil, nil
	}
	refs, err := workflowCandidateVerificationRefs(ctx, q, workID)
	if err != nil {
		return state, nil, err
	}
	var runs []workflowVerificationRun
	for _, ref := range refs {
		command, _, why, err := workflowCandidateVerificationRun(ctx, q, workID, ref)
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
		return false, "no passing producer verification covers the current integrated candidate", nil
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
		"leaving refine requires a passing worktree_verify run on the current integrated candidate: "+why,
		false, "run worktree_verify on the current integrated candidate before record_delivery")
}

// Proof applicability follows the producer's subject, not its position relative
// to a Review or a stage start. An unchanged candidate needs no rerun.
func workflowCandidateVerificationRun(ctx context.Context, q queryer, workID, ref string) ([]string, string, string, error) {
	producer, err := readOracleVerifySubjectReceipt(ctx, q, workID, ref)
	if err != nil {
		return nil, "", "", err
	}
	if producer == nil {
		return nil, "", "no passing unchanged-tree producer receipt", nil
	}
	candidate, err := workflowVerificationCandidate(ctx, q, workID)
	if err != nil {
		return nil, "", "", err
	}
	if candidate == nil || producer.SubjectRef != "commit:"+candidate.Commit || producer.ProjectID != candidate.ProjectID {
		return producer.Command, producer.ProjectID, "verification does not cover the current integrated candidate", nil
	}
	return producer.Command, producer.ProjectID, "", nil
}

// Explicit bindings and qualified producer receipts share the same candidate
// admission. Reading a tool's own receipt creates no duplicate evidence row.
func workflowCandidateVerificationRefs(ctx context.Context, q queryer, workID string) ([]string, error) {
	refs, err := workflowVerificationBindingsAfter(ctx, q, workID, 0, "workflow_action")
	if err != nil {
		return nil, err
	}
	candidate, err := workflowVerificationCandidate(ctx, q, workID)
	if err != nil || candidate == nil {
		return refs, err
	}
	rows, err := q.QueryContext(ctx, oracleVerifyReceiptSQL+` AND json_extract(l.result_json,'$.subject_ref')=? AND l.project_id=? ORDER BY d.rowid`, workID, "commit:"+candidate.Commit, candidate.ProjectID)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "workflow_action", "cannot read the candidate's producer verification", true, "retry once the producer receipts are readable", err)
	}
	defer rows.Close()
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, wrapFailure(KindUnavailable, "workflow_action", "cannot scan the candidate's producer verification", true, "retry once the producer receipts are readable", err)
		}
		producer, err := decodeOracleVerifyReceipt(raw, workID)
		if err != nil {
			return nil, err
		}
		if !slices.Contains(refs, producer.OperationRef) {
			refs = append(refs, producer.OperationRef)
		}
	}
	return refs, rows.Err()
}

func workflowVerificationCandidate(ctx context.Context, q queryer, workID string) (*ReviewCandidate, error) {
	review, err := readWorkflowReviewFacts(ctx, q, workID)
	if err != nil {
		return nil, err
	}
	return review.Candidate, nil
}

func workflowAdmitDelivery(definition WorkflowDefinition, state WorkflowAdmissionState, decision WorkflowAdmissionDecision) WorkflowAdmissionDecision {
	if state.Review != nil && workflowDefinitionProducesDelivery(definition) && (state.Review.Candidate != nil || state.RefinementWorkflow || state.Review.Dependencies != nil && !state.Review.Dependencies.ArchitectureNotApplicable) {
		if err := workflowAdmitReview(*state.Review); err != nil {
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
	return decision
}

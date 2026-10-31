package store

import (
	"context"
)

type workflowDeliveryAdmission struct {
	Started           bool
	ProofRequired     bool
	ProofReady        bool
	ProofDisqualifier string
	ProofFailure      *Failure
	Failure           *Failure
}

func workflowDefinitionProducesDelivery(definition WorkflowDefinition) bool {
	for _, step := range definition.StepGraph.Steps {
		for _, action := range step.Actions {
			if action == "record_delivery" {
				return true
			}
		}
	}
	return false
}

type workflowVerificationRun struct {
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
		why, err := workflowCandidateVerificationDisqualifier(ctx, q, workID, ref)
		if err != nil {
			var failure *Failure
			if !failureAs(err, &failure) || failure.Kind == KindUnavailable {
				return state, nil, err
			}
		}
		runs = append(runs, workflowVerificationRun{Disqualifier: why, Failure: err})
	}
	state.ProofReady, state.ProofDisqualifier, err = workflowVerificationProof(runs)
	if err != nil {
		state.ProofFailure = workflowFailureOf(err)
	}
	return state, runs, nil
}

func workflowVerificationProof(runs []workflowVerificationRun) (bool, string, error) {
	if len(runs) == 0 {
		return false, "no passing verification report covers the recorded Review candidate", nil
	}
	first := ""
	for _, run := range runs {
		if run.Failure != nil {
			return false, "", run.Failure
		}
		why := run.Disqualifier
		if why == "" {
			return true, "", nil
		}
		if first == "" {
			first = why
		}
	}
	return false, first, nil
}

func workflowRefineProofFailure(why string) *Failure {
	return newFailure(KindMissingEvidence, "workflow_action",
		"leaving refine requires a passing verification report on the current integrated candidate: "+why,
		false, "bind a passing report on the current integrated candidate before record_delivery")
}

// Proof applicability follows the recorded commit, not stage chronology.
func workflowCandidateVerificationDisqualifier(ctx context.Context, q queryer, workID, ref string) (string, error) {
	reports, err := readVerificationReports(ctx, q, workID, ref)
	if err != nil {
		return "", err
	}
	candidate, err := workflowVerificationCandidate(ctx, q, workID)
	if err != nil {
		return "", err
	}
	for _, report := range reports {
		if reportCovers(report, candidate) {
			return "", nil
		}
	}
	return "verification does not cover the current integrated candidate", nil
}

func workflowCandidateVerificationRefs(ctx context.Context, q queryer, workID string) ([]string, error) {
	return workflowVerificationBindingsAfter(ctx, q, workID, 0, "workflow_action")
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

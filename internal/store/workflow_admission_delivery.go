package store

import "context"

// workflowDeliveryAdmission is the comparable classification of the delivery
// epoch. Raw proof commands stay outside the abstract state for tooling checks.
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

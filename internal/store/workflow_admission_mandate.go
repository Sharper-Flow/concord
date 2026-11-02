package store

import (
	"context"
	"fmt"
)

// workflowMandateAdmission holds the first unbound mandate and input law.
// Recovery is a folded route, not a second query from an admission consumer.
type workflowMandateAdmission struct {
	Present          bool
	LawID            string
	InputLawID       string
	BindingStep      string
	AddedByContract  bool
	CorrectionAction string
	Failure          error
}

func loadWorkflowMandateAdmission(ctx context.Context, q queryer, workID string, definition WorkflowDefinition, step, subject string, state WorkflowAdmissionState) (workflowMandateAdmission, error) {
	var result workflowMandateAdmission
	mandates, version, err := workflowSpecMandate(ctx, q, workID, subject)
	if err != nil {
		var failure *Failure
		if !failureAs(err, &failure) || failure.Kind == KindUnavailable {
			return result, err
		}
		result.Failure = err
		return result, nil
	}
	if len(mandates) == 0 {
		return result, nil
	}
	result.Present = true
	result.BindingStep = workflowEvidenceBindingStep(definition, step)
	for _, lawID := range mandates {
		bound, err := workflowEvidenceReferenceBound(ctx, q, workID, lawID, subject)
		if err != nil {
			return result, err
		}
		if bound {
			continue
		}
		added, err := workflowContractAddsLaw(ctx, q, workID, version, lawID)
		if err != nil {
			return result, err
		}
		if result.LawID == "" {
			result.LawID, result.AddedByContract = lawID, added
			if added {
				if state.CorrectionRequestRecovery {
					result.CorrectionAction = "request_correction"
				}
			}
		}
		if !added {
			result.InputLawID = lawID
			break
		}
	}
	return result, nil
}

func workflowAdmitMandate(definition WorkflowDefinition, step string, state workflowMandateAdmission, actionID, subject string, reviewOnly bool) *Failure {
	if actionID == "bind_evidence" {
		return nil
	}
	terminalGate := actionID == "record_review" || actionID == "record_verdict" || actionID == "confirm_premise" || actionID == "complete"
	if !terminalGate {
		mode, declared := workflowActionExecutionMode(definition, actionID)
		if !declared || mode != ActionAdvance || !stepDeclaresAction(definition, step, "bind_evidence") {
			return nil
		}
	}
	if state.Failure != nil {
		return workflowFailureOf(state.Failure)
	}
	if !state.Present {
		return nil
	}
	if state.BindingStep == "" {
		return newFailure(KindInvariantViolation, subject, "workflow spec mandate has no bind_evidence step", false, "repair the pinned workflow definition")
	}
	// A local Review disposition judges a candidate; it neither delivers nor
	// binds the law this contract will add. Existing input law still binds first.
	if reviewOnly {
		state.LawID, state.AddedByContract = state.InputLawID, false
	}
	if state.LawID == "" {
		return nil
	}
	detail := fmt.Sprintf("spec mandate law %q is not bound", state.LawID)
	recovery := fmt.Sprintf("run bind_evidence on step %q before %s", state.BindingStep, actionID)
	if state.AddedByContract {
		detail = fmt.Sprintf("spec mandate law %q is one of the contract's own law additions; bind it only when the branch adds that id", state.LawID)
		recovery += fmt.Sprintf(" only when the branch adds %q", state.LawID)
		switch state.CorrectionAction {
		case "request_correction":
			recovery += fmt.Sprintf("; otherwise run %s, then revise_intent with the complete contract, before %s", state.CorrectionAction, actionID)
		default:
			recovery += "; otherwise use revise_intent with the complete contract"
		}
	}
	kind := KindMissingEvidence
	if actionID == "complete" {
		kind = KindInvariantViolation
	}
	return newFailure(kind, subject, detail, false, recovery)
}

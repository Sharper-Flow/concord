package store

import (
	"encoding/json"
	"fmt"
)

// WorkflowApprovalPolicyConsequenceV1 is the one explicit approval-policy
// value. Absence on a definition selects the released legacy policy; any other
// value refuses at definition validation and at classification.
const WorkflowApprovalPolicyConsequenceV1 = "consequence_v1"

// WorkflowApprovalClass is the semantic approval class of one action instance.
type WorkflowApprovalClass string

const (
	// WorkflowApprovalClassLegacy marks a classification answered from a
	// definition's released approval metadata; legacy has no semantic class.
	WorkflowApprovalClassLegacy WorkflowApprovalClass = "legacy"
	// WorkflowApprovalClassDestructive marks an identity-abandoning or
	// irreversible effect, which takes precedence over every other class.
	WorkflowApprovalClassDestructive WorkflowApprovalClass = "destructive"
	// WorkflowApprovalClassDecision marks the contract and confirmation
	// actions whose consent follows the contract's declared decision domains.
	WorkflowApprovalClassDecision WorkflowApprovalClass = "decision"
	// WorkflowApprovalClassRoutine marks authenticated record-keeping and
	// coordination that never mints a consent challenge.
	WorkflowApprovalClassRoutine WorkflowApprovalClass = "routine"
)

// WorkflowApprovalConsent is the concrete consent requirement of one action
// instance. Legacy classifications echo their declared metadata verbatim,
// including conditional.
type WorkflowApprovalConsent string

const (
	WorkflowConsentNone        WorkflowApprovalConsent = "none"
	WorkflowConsentConditional WorkflowApprovalConsent = "conditional"
	WorkflowConsentRequired    WorkflowApprovalConsent = "required"
)

// WorkflowDestructiveConsequence names a typed irreversible effect an action
// instance performs. The closed set grows only through an approved slice.
type WorkflowDestructiveConsequence string

// WorkflowDestructiveCancel marks an instance whose effect abandons the work
// identity's acceptance obligation.
const WorkflowDestructiveCancel WorkflowDestructiveConsequence = "cancel"

// WorkflowApprovalFacts carries the typed instance facts classification
// consumes. It deliberately carries no work title, work kind, changed-file
// set, or product-truth flag: none of those may infer approval scope.
type WorkflowApprovalFacts struct {
	// ProposedDecisionDomains is the first contract's declared fact for
	// approve_contract. New policy requires it to be present: nil refuses.
	ProposedDecisionDomains []string
	// ActiveDecisionDomains is the stored contract's declared fact for
	// confirm_premise. New policy requires it to be present: nil refuses.
	ActiveDecisionDomains []string
	// OperatorRequired records the executor's WorkflowOperatorRequired
	// evaluation. Legacy consent honors it for the operator verdict actions;
	// consequence_v1 never substitutes an operator for classification.
	OperatorRequired bool
	// Destructive names a typed irreversible effect. Legacy policy knows no
	// such fact and refuses one; consequence_v1 gives it precedence.
	Destructive WorkflowDestructiveConsequence
	// SeriousLawChange records that the proposed contract or successor makes
	// a law change that is serious under P12 or made at crucial size
	// (CD-0230 D4). Legacy policy knows no such fact and refuses one.
	SeriousLawChange bool
}

// WorkflowApprovalPolicyResult is one classification: the policy that answered,
// the semantic class, the concrete consent requirement, and the reason. It is
// not authorization: callers keep every version, scope, evidence, retry,
// convergence, independence, and admission check they already run.
type WorkflowApprovalPolicyResult struct {
	Policy  string
	Class   WorkflowApprovalClass
	Consent WorkflowApprovalConsent
	Reason  string
}

// workflowOperatorVerdictActions is the closed legacy set of actions whose
// WorkflowOperatorRequired evaluation forces an approval.
var workflowOperatorVerdictActions = map[string]bool{
	"record_verdict":     true,
	"complete":           true,
	"request_correction": true,
	"confirm_premise":    true,
}

// ClassifyWorkflowApproval answers one action instance's approval class and
// consent from the definition's own discriminator, the action's declared
// metadata, and the typed facts. It is pure: no store, clock, caller identity,
// or effect. An action neither the definition declares nor the engine's
// recovery owner owns, an unknown policy value, an unknown destructive fact,
// or a new-policy contract instance without its declared decision-domain fact
// refuses.
func ClassifyWorkflowApproval(definition WorkflowDefinition, actionID string, facts WorkflowApprovalFacts) (WorkflowApprovalPolicyResult, error) {
	if actionID == "" {
		return WorkflowApprovalPolicyResult{}, newFailure(KindInvalidInput, "workflow_approval_policy", "approval classification requires an action", false, "classify a declared workflow action")
	}
	var declared *WorkflowActionDefinition
	for index := range definition.ActionDefinitions {
		if definition.ActionDefinitions[index].ID == actionID {
			declared = &definition.ActionDefinitions[index]
			break
		}
	}
	if declared == nil {
		// The pin does not own every action the engine does: recovery
		// discovery holds the canonical declaration for its own actions.
		if recovered, owned := workflowRecoveryActionDefinition(actionID); owned {
			declared = &recovered
		}
	}
	if declared == nil {
		return WorkflowApprovalPolicyResult{}, newFailure(KindInvalidInput, "workflow_approval_policy", fmt.Sprintf("action %q is not declared by definition %s", actionID, definition.Ref), false, "classify an action the verified definition declares")
	}
	if facts.Destructive != "" && facts.Destructive != WorkflowDestructiveCancel {
		return WorkflowApprovalPolicyResult{}, newFailure(KindInvalidInput, "workflow_approval_policy", fmt.Sprintf("destructive consequence %q is outside the closed set", string(facts.Destructive)), false, "declare a destructive consequence the policy knows")
	}
	switch definition.ApprovalPolicy {
	case "":
		return classifyLegacyWorkflowApproval(definition, declared, actionID, facts)
	case WorkflowApprovalPolicyConsequenceV1:
		return classifyConsequenceV1WorkflowApproval(definition, actionID, facts)
	default:
		return WorkflowApprovalPolicyResult{}, newFailure(KindInvalidInput, "workflow_approval_policy", fmt.Sprintf("definition %s declares unknown approval policy %q", definition.Ref, definition.ApprovalPolicy), false, "declare approval_policy absence or consequence_v1")
	}
}

// classifyLegacyWorkflowApproval answers from the definition's released
// metadata: the declared approval stands and the operator verdict actions
// upgrade on WorkflowOperatorRequired.
func classifyLegacyWorkflowApproval(definition WorkflowDefinition, declared *WorkflowActionDefinition, actionID string, facts WorkflowApprovalFacts) (WorkflowApprovalPolicyResult, error) {
	if facts.Destructive != "" || facts.SeriousLawChange {
		return WorkflowApprovalPolicyResult{}, newFailure(KindInvalidInput, "workflow_approval_policy", "the legacy policy has no destructive consequence or law-change fact", false, "classify legacy actions without consequence facts")
	}
	consent := WorkflowApprovalConsent(declared.Approval)
	switch consent {
	case WorkflowConsentNone, WorkflowConsentConditional, WorkflowConsentRequired:
	default:
		return WorkflowApprovalPolicyResult{}, newFailure(KindInvalidInput, "workflow_approval_policy", fmt.Sprintf("definition %s action %s declares unknown approval %q", definition.Ref, actionID, string(declared.Approval)), false, "declare none, conditional, or required")
	}
	reason := fmt.Sprintf("legacy policy: %s v%d declares approval %s for %s", definition.Ref, definition.Version, consent, actionID)
	if facts.OperatorRequired && workflowOperatorVerdictActions[actionID] {
		consent = WorkflowConsentRequired
		reason = fmt.Sprintf("legacy policy: WorkflowOperatorRequired on %s requires an operator approval", actionID)
	}
	return WorkflowApprovalPolicyResult{Policy: "", Class: WorkflowApprovalClassLegacy, Consent: consent, Reason: reason}, nil
}

// classifyConsequenceV1WorkflowApproval answers from the typed decision fact
// and consequence facts: destructive precedence first, then the contract and
// confirmation actions whose consent follows the declared decision domains,
// and routine for everything else. No executor override substitutes an
// operator, and no work attribute infers scope.
func classifyConsequenceV1WorkflowApproval(definition WorkflowDefinition, actionID string, facts WorkflowApprovalFacts) (WorkflowApprovalPolicyResult, error) {
	pin := fmt.Sprintf("%s v%d", definition.Ref, definition.Version)
	if facts.Destructive != "" {
		return WorkflowApprovalPolicyResult{Policy: WorkflowApprovalPolicyConsequenceV1, Class: WorkflowApprovalClassDestructive, Consent: WorkflowConsentRequired,
			Reason: fmt.Sprintf("consequence_v1: %s on %s performs the irreversible effect %q and requires an exact approval", actionID, pin, string(facts.Destructive))}, nil
	}
	switch actionID {
	case "approve_contract":
		return classifyConsequenceV1Decision(definition, actionID, "proposed", facts.ProposedDecisionDomains, facts.SeriousLawChange)
	case "confirm_premise":
		return classifyConsequenceV1Decision(definition, actionID, "active", facts.ActiveDecisionDomains, false)
	case "supersede_contract":
		if facts.SeriousLawChange {
			return WorkflowApprovalPolicyResult{Policy: WorkflowApprovalPolicyConsequenceV1, Class: WorkflowApprovalClassDecision, Consent: WorkflowConsentRequired,
				Reason: fmt.Sprintf("consequence_v1: %s on %s makes a serious law change and requires the operator's approval of its exact text (CD-0230 D4)", actionID, pin)}, nil
		}
		fallthrough
	default:
		// The declared legacy metadata is deliberately not consulted: the
		// new policy replaces it for every routine action.
		reason := fmt.Sprintf("consequence_v1: %s on %s is routine record-keeping with no consent challenge", actionID, pin)
		if facts.OperatorRequired {
			reason += "; the executor override grants no operator substitution"
		}
		return WorkflowApprovalPolicyResult{Policy: WorkflowApprovalPolicyConsequenceV1, Class: WorkflowApprovalClassRoutine, Consent: WorkflowConsentNone, Reason: reason}, nil
	}
}

// classifyConsequenceV1Decision classifies one contract or confirmation
// instance from its declared decision-domain fact and, for a contract, its
// law-change seriousness. Absence refuses: a new-policy contract instance
// must declare the fact, where [] records engineering and a nonempty
// canonical set records decision-bearing work.
func classifyConsequenceV1Decision(definition WorkflowDefinition, actionID, role string, domains []string, seriousLawChange bool) (WorkflowApprovalPolicyResult, error) {
	pin := fmt.Sprintf("%s v%d", definition.Ref, definition.Version)
	if domains == nil {
		return WorkflowApprovalPolicyResult{}, newFailure(KindInvalidInput, "workflow_approval_policy", fmt.Sprintf("consequence_v1 %s on %s requires the %s decision-domain fact; absence is a legacy shape", actionID, pin, role), false, "declare the contract's decision domains explicitly")
	}
	if err := validateWorkflowDecisionDomains(domains); err != nil {
		// The fold validator owns invalid_payload for event folding; at the
		// classifier's pure input boundary the same cause refuses as
		// invalid_input with its detail preserved.
		var cause *Failure
		detail := err.Error()
		if failureAs(err, &cause) && cause.Detail != "" {
			detail = cause.Detail
		}
		return WorkflowApprovalPolicyResult{}, newFailure(KindInvalidInput, "workflow_approval_policy", fmt.Sprintf("consequence_v1 %s on %s declares a malformed %s decision-domain set: %s", actionID, pin, role, detail), false, "declare the domains canonically sorted and unique over preference, product, and ui_ux")
	}
	if len(domains) == 0 && seriousLawChange {
		return WorkflowApprovalPolicyResult{Policy: WorkflowApprovalPolicyConsequenceV1, Class: WorkflowApprovalClassDecision, Consent: WorkflowConsentRequired,
			Reason: fmt.Sprintf("consequence_v1: %s on %s makes a serious law change and requires the operator's approval of its exact text (CD-0230 D4)", actionID, pin)}, nil
	}
	if len(domains) == 0 {
		return WorkflowApprovalPolicyResult{Policy: WorkflowApprovalPolicyConsequenceV1, Class: WorkflowApprovalClassDecision, Consent: WorkflowConsentNone,
			Reason: fmt.Sprintf("consequence_v1: %s on %s declares engineering work and records without an approval binding", actionID, pin)}, nil
	}
	return WorkflowApprovalPolicyResult{Policy: WorkflowApprovalPolicyConsequenceV1, Class: WorkflowApprovalClassDecision, Consent: WorkflowConsentRequired,
		Reason: fmt.Sprintf("consequence_v1: %s on %s declares decision domains %v and requires the human consent question", actionID, pin, domains)}, nil
}

// workflowApprovalPolicyRequiresDecisionFact reports whether the pinned
// approval policy requires a contract instance to declare its decision-domain
// fact: consequence_v1 requires it; legacy absence knows no such fact.
func workflowApprovalPolicyRequiresDecisionFact(definition WorkflowDefinition) bool {
	return definition.ApprovalPolicy == WorkflowApprovalPolicyConsequenceV1
}

// Legacy authoring keeps its released bytes. New-policy authoring requires a
// canonical array: decoding preserves the distinction between null and [].
func workflowSubmittedDecisionDomains(definition WorkflowDefinition, role string, fields map[string]json.RawMessage) ([]string, error) {
	if !workflowApprovalPolicyRequiresDecisionFact(definition) {
		return nil, nil
	}
	raw, present := fields["decision_domains"]
	var domains []string
	if present && json.Unmarshal(raw, &domains) != nil {
		return nil, newFailure(KindInvalidPayload, "workflow_approval_policy", fmt.Sprintf("consequence_v1 %s decision_domains is not a decodable string array", role), false, "declare the domains as a JSON array over preference, product, and ui_ux")
	}
	if domains == nil {
		return nil, newFailure(KindInvalidPayload, "workflow_approval_policy", fmt.Sprintf("consequence_v1 %s requires the submitted decision-domain fact; absence is a legacy shape", role), false, "declare the contract's decision domains explicitly")
	}
	if err := validateWorkflowDecisionDomains(domains); err != nil {
		return nil, err
	}
	return domains, nil
}

package store

import "testing"

// TestShippedStepsAdmitTheirApprovalRequiredActions holds the invariant that
// stranded workflow.ops_runbook at its cleanup step: a step may declare an
// approval-required action only when its kind can serve the operator question
// that approval consumes. workflowOperatorQuestionTx refuses the question on
// any step that is not a human checkpoint, so a step of another kind declaring
// such an action offers a route no caller can take.
//
// request_correction is the one exception. Its approval is never served by a
// step question: the boundary that applies it consumes the operator identity
// of the request, backed on the tool surface by a durable approval challenge,
// so the CD-0166 delivery gate can declare the evidence-bearing corrective
// return and stay reachable while its kind is internal_sqlite.
//
// The scope is the shipped set. Frozen prior versions stay byte-identical by
// law, and three of them violate this rule, so the registry constructor cannot
// carry the check without rejecting history it must keep.
func TestShippedStepsAdmitTheirApprovalRequiredActions(t *testing.T) {
	for _, definition := range BuiltinWorkflowDefinitions() {
		approvalRequired := map[string]bool{}
		for _, action := range definition.ActionDefinitions {
			if action.Approval == ActionApprovalRequired {
				approvalRequired[action.ID] = true
			}
		}
		for _, step := range definition.StepGraph.Steps {
			if step.Kind == WorkflowStepHumanCheckpoint {
				continue
			}
			for _, id := range step.Actions {
				if id == "request_correction" && workflowStepIsDeliveryGate(&step) {
					continue
				}
				if approvalRequired[id] {
					t.Errorf("%s v%d step %q has kind %q and declares approval-required action %q; the operator question that approval consumes is served only from a human checkpoint, so the action can never run", definition.Ref, definition.Version, step.ID, step.Kind, id)
				}
			}
		}
	}
}

// TestShippedNonTerminalStepsCanAdvance proves the consequence the invariant
// above protects. A non-terminal step whose every advancing action is refused
// by its kind is a dead end, and every work item that reaches it stops.
func TestShippedNonTerminalStepsCanAdvance(t *testing.T) {
	for _, definition := range BuiltinWorkflowDefinitions() {
		terminal := map[string]bool{}
		for _, id := range definition.StepGraph.TerminalSteps {
			terminal[id] = true
		}
		effect := map[string]WorkflowActionDefinition{}
		for _, action := range definition.ActionDefinitions {
			effect[action.ID] = action
		}
		for _, step := range definition.StepGraph.Steps {
			if terminal[step.ID] {
				continue
			}
			reachable := false
			for _, id := range step.Actions {
				action, ok := effect[id]
				if !ok || action.ExecutionMode != ActionAdvance {
					continue
				}
				if action.Approval == ActionApprovalRequired && step.Kind != WorkflowStepHumanCheckpoint {
					continue
				}
				reachable = true
				break
			}
			if !reachable {
				t.Errorf("%s v%d step %q declares no advancing action its kind %q admits; work that reaches this step cannot leave it", definition.Ref, definition.Version, step.ID, step.Kind)
			}
		}
	}
}

// workflowRequiredEvidenceOutsideReach returns, in declaration order, the
// evidence kinds a definition requires — across the definition, its steps,
// and its rigor rules — that no action reachable through forward and optional
// edges can produce. TestShippedDefinitionsCanProduceRequiredEvidence holds
// this set empty for every shipped definition;
// TestSyntheticDefinitionEvidenceGapIsDetected holds it non-empty for a
// synthetic one, so the check cannot pass by accident.
func workflowRequiredEvidenceOutsideReach(definition WorkflowDefinition) []EvidenceKind {
	producible := evidenceStrings(workflowReachableEvidenceKinds(definition))
	var missing []EvidenceKind
	for _, kind := range definition.RequiredEvidenceKinds {
		if !containsString(producible, string(kind)) {
			missing = append(missing, kind)
		}
	}
	for _, step := range definition.StepGraph.Steps {
		for _, kind := range step.RequiredEvidenceKinds {
			if !containsString(producible, string(kind)) {
				missing = append(missing, kind)
			}
		}
	}
	for _, rule := range definition.RigorRules {
		for _, kind := range rule.RequiredEvidenceKinds {
			if !containsString(producible, string(kind)) {
				missing = append(missing, kind)
			}
		}
	}
	return missing
}

// TestShippedDefinitionsCanProduceRequiredEvidence holds the evidence-side
// consequence of the same graph facts the invariants above protect: every
// evidence kind a current shipped definition requires — at the definition, on
// a step, or in a rigor rule — is a kind at least one action on a step
// reachable through forward and optional edges can produce. A requirement
// outside that set strands the gate that waits for it: no route through the
// workflow can ever bind the kind, so no contract naming it can be served.
// Frozen prior versions stay byte-identical by law, so the scope is the
// shipped set alone, like the invariants above.
func TestShippedDefinitionsCanProduceRequiredEvidence(t *testing.T) {
	for _, definition := range BuiltinWorkflowDefinitions() {
		if missing := workflowRequiredEvidenceOutsideReach(definition); len(missing) != 0 {
			producible := evidenceStrings(workflowReachableEvidenceKinds(definition))
			t.Errorf("%s v%d requires evidence kinds %v its reachable actions cannot produce; those actions produce %v", definition.Ref, definition.Version, missing, producible)
		}
	}
}

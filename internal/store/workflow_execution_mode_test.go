package store

import "testing"

func TestWorkflowActionPoliciesHaveTypedExecutionModes(t *testing.T) {
	for id, policy := range builtinActionPolicies {
		if !validActionExecutionMode(policy.ExecutionMode) {
			t.Errorf("action %s has invalid mode %q", id, policy.ExecutionMode)
		}
	}
}

func TestWorkflowExecutionModesPreserveCurrentSemantics(t *testing.T) {
	for id, want := range map[string]ActionExecutionMode{
		"approve_contract": ActionAdvance, "start_execution": ActionFenced, "checkpoint_execution": ActionCheckpoint,
		"bind_evidence": ActionHold, "confirm_premise": ActionAdvance,
		"complete": ActionHold, "checkpoint_context": ActionHold, "cross_context_boundary": ActionHold,
		"record_delivery": ActionAdvance, "checkpoint_refine": ActionCheckpoint, "record_review": ActionHold,
	} {
		policy, ok := builtinActionPolicies[id]
		if !ok || policy.ExecutionMode != want {
			t.Errorf("%s mode=%q found=%v want=%q", id, policy.ExecutionMode, ok, want)
		}
	}
	if _, ok := builtinActionPolicies["unknown_action"]; ok {
		t.Fatal("unknown action has a policy")
	}
}

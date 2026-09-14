package agent

import "testing"

// The Domain overlap gate exempts the recovery choices its own refusal names,
// because guarding them would refuse the only way out of the condition. An
// observation records no Product change, so a blocked work item must stay able
// to record why it is blocked.
func TestMutationIsOverlapRecovery(t *testing.T) {
	for _, tc := range []struct {
		name      string
		tool      string
		operation string
		raw       string
		want      bool
	}{
		{name: "resolve_overlap is the primary recovery", tool: "concord_work_relate", operation: "resolve_overlap", raw: `{}`, want: true},
		{name: "supersede is a named recovery", tool: "concord_work_relate", operation: "supersede", raw: `{}`, want: true},
		{name: "restore_superseded is a named recovery", tool: "concord_work_relate", operation: "restore_superseded", raw: `{}`, want: true},
		{name: "cancelling is the terminal_work recovery", tool: "concord_work_transition", operation: "lifecycle", raw: `{"target":"cancelled"}`, want: true},
		{name: "completing is the terminal_work recovery", tool: "concord_work_transition", operation: "lifecycle", raw: `{"target":"completed"}`, want: true},
		{name: "supersede_contract is the named contract-correction recovery", tool: "concord_work_transition", operation: "workflow_action", raw: `{"action_id":"supersede_contract"}`, want: true},
		{name: "another workflow action is not a recovery", tool: "concord_work_transition", operation: "workflow_action", raw: `{"action_id":"record_delivery"}`, want: false},
		{name: "a workflow action with no action_id is not a recovery", tool: "concord_work_transition", operation: "workflow_action", raw: `{}`, want: false},
		{name: "a statement observation asserts no Product change", tool: "concord_work_define", operation: "observation_record", raw: `{"statement":"the gate refused the dispatch"}`, want: true},
		{name: "an external observation binds evidence and owes the boundary", tool: "concord_work_define", operation: "observation_record", raw: `{"external":{"kind":"verification","observation_id":"xobs:0123456789abcdef"}}`, want: false},
		{name: "an observation with neither form is not exempt", tool: "concord_work_define", operation: "observation_record", raw: `{}`, want: false},
		{name: "starting work is not a recovery", tool: "concord_work_transition", operation: "lifecycle", raw: `{"target":"in_progress"}`, want: false},
		{name: "capture is not a recovery", tool: "concord_work_define", operation: "capture", raw: `{}`, want: false},
		{name: "revising intent is not a recovery", tool: "concord_work_define", operation: "revise_intent", raw: `{}`, want: false},
		{name: "linking is not a recovery", tool: "concord_work_relate", operation: "link", raw: `{}`, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := mutationIsOverlapRecovery(tc.tool, tc.operation, []byte(tc.raw)); got != tc.want {
				t.Fatalf("mutationIsOverlapRecovery(%q, %q, %s) = %v, want %v", tc.tool, tc.operation, tc.raw, got, tc.want)
			}
		})
	}
}

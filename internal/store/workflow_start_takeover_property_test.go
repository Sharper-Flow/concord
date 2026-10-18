package store

import (
	"errors"
	"testing"
)

// The takeover rule keys on the declared fenced execution mode, so its reach
// is every fenced start the registry has ever published, dispatch_worker
// among them: a fresh dispatch opens the next epoch over the held attempt as
// any fenced start does. The archived families' fenced starts sit behind the
// archived-family refusal, which answers before any takeover read.
func TestFencedStartTakeoverPropertyReachesEveryFencedStart(t *testing.T) {
	t.Parallel()
	registry := BuiltinWorkflowRegistry()
	want := map[string]bool{"dispatch_worker": true, "start_execution": true, "start_refine": true, "start_repair": true, "start_poc": true, "start_run": true, "rollback_run": true, "run_analysis": true, "start_action": true}
	got := map[string]bool{}
	sawDispatch := false
	refs := []string{}
	for ref := range liveWorkflowAuthoringFamilies {
		refs = append(refs, ref)
	}
	for ref := range archivedWorkflowFamilies {
		refs = append(refs, ref)
	}
	for _, ref := range refs {
		for version := int64(1); version <= 200; version++ {
			registered, ok := registry.Lookup(ref, version)
			if !ok {
				continue
			}
			for _, action := range registered.Definition.ActionDefinitions {
				if action.ID == "dispatch_worker" {
					sawDispatch = true
				}
				if !WorkflowActionOpensHeldAttempt(action) {
					continue
				}
				got[action.ID] = true
				if workflowFamilyArchived(ref) {
					decision := workflowAdmit(registered.Definition, WorkflowAdmissionState{}, action.ID)
					var failure *Failure
					if !errors.As(decision.Failure, &failure) || failure.Kind != KindInvalidOperation {
						t.Fatalf("archived %s@%d %s admission = %v, want the archived-family refusal", ref, version, action.ID, decision.Failure)
					}
				}
			}
		}
	}
	if !sawDispatch {
		t.Fatal("no registered definition declares dispatch_worker")
	}
	for id := range want {
		if !got[id] {
			t.Errorf("fenced start %s is outside the takeover rule", id)
		}
	}
	for id := range got {
		if !want[id] {
			t.Errorf("unexpected action %s opens a held attempt; extend the reviewed set deliberately", id)
		}
	}
}

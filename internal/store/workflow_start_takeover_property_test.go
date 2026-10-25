package store

import (
	"testing"
)

// The takeover rule keys on the declared fenced execution mode, so its reach
// is every fenced start the registry publishes, dispatch_worker among them: a
// fresh dispatch opens the next epoch over the held attempt as any fenced
// start does. The registry holds one current definition per live family
// (CD-0242 D14); an archived family registers no definition, so none of its
// starts is reachable (CD-0218 D1).
func TestFencedStartTakeoverPropertyReachesEveryFencedStart(t *testing.T) {
	t.Parallel()
	registry := BuiltinWorkflowRegistry()
	want := map[string]bool{"dispatch_worker": true, "start_execution": true}
	got := map[string]bool{}
	for ref := range archivedWorkflowFamilies {
		if registered, ok := registry.Current(ref); ok {
			t.Fatalf("archived %s registers %s %s, want no reachable definition", ref, registered.Definition.Ref, registered.Digest)
		}
	}
	for ref := range liveWorkflowAuthoringFamilies {
		registered, ok := registry.Current(ref)
		if !ok {
			t.Fatalf("live family %s has no current definition", ref)
		}
		for _, action := range registered.Definition.ActionDefinitions {
			if action.ID == "dispatch_worker" && (action.ExecutionMode != ActionFenced || action.RequiredCapability != WorkerDispatchCapability) {
				t.Fatalf("%s dispatch_worker mode %q capability %q, want fenced under the worker_dispatch capability the lane-attempt hold keys on", ref, action.ExecutionMode, action.RequiredCapability)
			}
			if WorkflowActionOpensHeldAttempt(action) {
				got[action.ID] = true
			}
		}
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

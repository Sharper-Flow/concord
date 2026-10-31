package store

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestWorkflowTakeoverReadsCurrentHeldEpoch(t *testing.T) {
	t.Parallel()
	const work = "current-workflow-takeover"
	f := seedWorkflowReturnRouteFixture(t, work, "workflow.implementation", "execution")
	s := f.store
	holder, err := WorkflowActorRef(f.owner)
	if err != nil {
		t.Fatal(err)
	}
	for epoch := int64(1); epoch <= 2; epoch++ {
		if err := runVerdictActionAs(t, s, work, "start_execution", json.RawMessage(`{}`), 0, f.owner); err != nil {
			t.Fatal(err)
		}
		takeover, err := workflowStartTakeover(t.Context(), s.db, WorkflowActionDefinition{ExecutionMode: ActionFenced}, work, "actor:another-session")
		if err != nil {
			t.Fatal(err)
		}
		if takeover == nil || takeover.HeldEpoch != epoch || takeover.HolderActorRef != holder {
			t.Fatalf("takeover=%+v, want epoch %d held by %s", takeover, epoch, holder)
		}
	}
	read := func(actor string) func() (any, error) {
		return func() (any, error) {
			return workflowStartTakeover(t.Context(), s.db, WorkflowActionDefinition{ExecutionMode: ActionFenced}, work, actor)
		}
	}
	if takeover, err := read(holder)(); err != nil || takeover.(*WorkflowStartTakeover) != nil {
		t.Fatalf("holder restart requires no takeover: takeover=%+v err=%v", takeover, err)
	}
	assertWorkerFactsLogBlind(t, s, map[string]func() (any, error){
		"holder": read(holder), "other-session": read("actor:another-session"),
	})
}

func assertWorkerFactsLogBlind(t *testing.T, s *Store, checks map[string]func() (any, error)) {
	t.Helper()
	want := map[string]any{}
	for name, read := range checks {
		value, err := read()
		if err != nil {
			t.Fatalf("%s baseline: %v", name, err)
		}
		want[name] = value
	}
	assertEventLogAbsent(t, s)
	for name, read := range checks {
		t.Run(name, func(t *testing.T) {
			got, err := read()
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want[name]) {
				t.Fatalf("current answer = %#v; baseline = %#v", got, want[name])
			}
		})
	}
}

package store

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestSessionTakeoverReadsCurrentVacateRequest(t *testing.T) {
	for _, state := range []string{"none", "before-start", "other-work", "other-session", "after-start", "vacate-then-restart"} {
		t.Run(state, func(t *testing.T) {
			t.Parallel()
			work := "current-session-takeover-" + state
			f := seedWorkflowReturnRouteFixture(t, work, "workflow.implementation", "execution")
			s := f.store
			holder, err := WorkflowActorRef(f.owner)
			if err != nil {
				t.Fatal(err)
			}
			vacate := func(workID, session string) {
				t.Helper()
				event := operationEvent("vacate:"+state, "work.session_vacated", SubjectWorkItem, workID, map[string]any{
					"work_id": workID, "project_id": "project-test", "session_ref": session,
					"source_directory": "/data/linked", "destination_directory": "/data/main",
				})
				event.PayloadVersion = 2
				if err := ApplyOperation(t.Context(), s, Operation{Events: []Event{event}}); err != nil {
					t.Fatal(err)
				}
			}
			start := func() {
				t.Helper()
				if err := runVerdictActionAs(t, s, work, "start_execution", json.RawMessage(`{}`), 0, f.owner); err != nil {
					t.Fatal(err)
				}
			}
			if state == "before-start" {
				vacate(work, f.owner.SessionRef)
			}
			start()
			switch state {
			case "other-work":
				other := work + "-other"
				seedWork(t, s, other)
				vacate(other, f.owner.SessionRef)
			case "other-session":
				vacate(work, "session/other")
			case "after-start", "vacate-then-restart":
				vacate(work, f.owner.SessionRef)
				if state == "vacate-then-restart" {
					start()
				}
			}
			read := func() (any, error) {
				return workflowStartTakeover(t.Context(), s.db, WorkflowActionDefinition{ExecutionMode: ActionFenced}, work, "actor:another-session")
			}
			got, err := read()
			if err != nil {
				t.Fatal(err)
			}
			takeover := got.(*WorkflowStartTakeover)
			if state == "after-start" {
				if takeover != nil {
					t.Fatalf("holder's pending vacate did not release the attempt: %+v", takeover)
				}
			} else if takeover == nil || takeover.HolderActorRef != holder {
				t.Fatalf("takeover=%+v, want the current holder %s", takeover, holder)
			}
			assertWorkerFactsLogBlind(t, s, map[string]func() (any, error){"G33-session-vacate": read})
		})
	}
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

package store

import (
	"encoding/json"
	"math"
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
				return workflowStartTakeover(t.Context(), s.db, WorkflowActionDefinition{}, work, "actor:another-session", "", "")
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

func TestLaneTakeoverReadsCurrentAttemptState(t *testing.T) {
	for _, state := range []string{"live", "completed-awaiting-decision", "failed"} {
		t.Run(state, func(t *testing.T) {
			t.Parallel()
			work := "current-lane-takeover-" + state
			f := seedChecksRepairFixture(t, work, "repair")
			s := f.store
			job := &WorkerJobBinding{JobID: "job:" + work}
			attempt := "attempt:" + work
			dispatchJobBoundAttempt(t, s, work, "repair", attempt, nil, f.owner, 0, "dispatch:"+work, job)
			switch state {
			case "completed-awaiting-decision":
				if err := completeJobBoundAttemptForTest(s, work, attempt, reviewGateLane(t, "implementation"), job); err != nil {
					t.Fatal(err)
				}
			case "failed":
				failed := attributionWorkerFailedEvent(work, attempt, "failed:"+work, preferredModelForLane(reviewGateLane(t, "implementation")), attributionHostObservation())
				if err := s2WorkerFailedLive(t, s, failed); err != nil {
					t.Fatal(err)
				}
			}
			startEvent, err := readWorkflowActionStartTx(t.Context(), s.db, work, "repair", true)
			if err != nil {
				t.Fatal(err)
			}
			var start workflowActionStartedPayload
			if err := json.Unmarshal(startEvent.Payload, &start); err != nil {
				t.Fatal(err)
			}
			read := func() (any, error) {
				return workflowLaneAttemptHeldFrom(t.Context(), s.db, work, "repair", startEvent.Seq, start, "actor:another-session")
			}
			got, err := read()
			if want := state != "failed"; err != nil || got != want {
				t.Fatalf("lane hold=%v err=%v, want %t", got, err, want)
			}
			assertWorkerFactsLogBlind(t, s, map[string]func() (any, error){
				"G33-lane-hold": read,
				"G23-fenced-writer": func() (any, error) {
					writer, live, since, err := workflowFencedWriter(t.Context(), s.db, work, start.ActorRef, math.MaxInt64)
					return struct {
						Writer string
						Live   bool
						Since  int64
					}{writer, live, since}, err
				},
			})
		})
	}
}

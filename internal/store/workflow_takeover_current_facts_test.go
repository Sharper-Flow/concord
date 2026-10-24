package store

import (
	"encoding/json"
	"math"
	"testing"
)

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
			startEvent, err := readWorkerAuthorizationStartTx(t.Context(), s.db, work, attempt)
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

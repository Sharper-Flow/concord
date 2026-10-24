package store

import (
	"fmt"
	"reflect"
	"testing"
)

func TestSessionExecutionReadersUseCurrentObligations(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	const work = "session-current-obligations"
	actor, _ := continuityTestWorkflow(t, s, work)
	actorRef, err := WorkflowActorRef(actor)
	if err != nil {
		t.Fatal(err)
	}
	// The original epoch stays open while two newer epochs fail. The newest
	// failure is visible, but neither failure closes the older start.
	for _, epoch := range []int64{2, 3} {
		for _, kind := range []string{WorkflowActionStarted, WorkflowActionFailed} {
			version := readWorkVersion(t, s, work)
			id := fmt.Sprintf("session-obligation-%d-%s", epoch, kind)
			fields := map[string]any{"work_id": work, "expected_version": version, "resulting_version": version + 1, "step_id": "proposal", "attempt_epoch": epoch, "actor_ref": actorRef}
			if kind == WorkflowActionStarted {
				fields["action_id"], fields["accepted_inputs_digest"], fields["idempotency_identity"] = "record_proposal", testManifestDigest, id
			} else {
				fields["failure_kind"], fields["recoverable"] = "timeout", false
			}
			if err := applyWorkflowTestOperation(t.Context(), s, Operation{Events: []Event{workflowEventWithActor(id, kind, work, actorRef, fields)}}); err != nil {
				t.Fatal(err)
			}
		}
	}
	checks := map[string]func() (any, error){
		"all-work-execution": func() (any, error) { return workExecutionStoppedTx(t.Context(), s.db, work, "") },
		"own-session-execution": func() (any, error) {
			return sessionOwnedExecutionStoppedTx(t.Context(), s.db, work, actor.SessionRef)
		},
		"foreign-session-execution": func() (any, error) {
			return sessionOwnedExecutionStoppedTx(t.Context(), s.db, work, "session/foreign")
		},
		"unresolved-failure": func() (any, error) {
			var snapshot ContinuitySnapshot
			err := s.Transact(t.Context(), func(transaction *Transaction) error {
				return continuityReadFailureTx(t.Context(), transaction.tx, work, &snapshot, true)
			})
			return snapshot.UnresolvedFailure, err
		},
	}
	want := map[string]any{
		"all-work-execution":        false,
		"own-session-execution":     false,
		"foreign-session-execution": true,
		"unresolved-failure":        &ContextFailure{Kind: "timeout", Recoverable: false, StepID: "proposal", AttemptEpoch: 3},
	}
	for name, read := range checks {
		got, err := read()
		if err != nil || !reflect.DeepEqual(got, want[name]) {
			t.Fatalf("%s=%+v err=%v, want %+v", name, got, err, want[name])
		}
	}
	assertWorkerFactsLogBlind(t, s, checks)
}

func TestWorkLivenessReadsCurrentProgress(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	const work = "liveness-current-progress"
	continuityTestWorkflow(t, s, work)
	want, err := deriveWorkLivenessQ(t.Context(), s.db, work)
	if err != nil || want.LastProgress == "" {
		t.Fatalf("liveness=%+v err=%v, want recorded progress", want, err)
	}
	var stamp string
	if err := s.db.QueryRow(`SELECT max(occurred_at) FROM domain_events WHERE subject_type='work_item' AND subject_id=? AND kind NOT IN ('work.message_sent','work.message_withdrawn','workflow.overlap_resolved')`, work).Scan(&stamp); err != nil {
		t.Fatal(err)
	}
	if want.LastProgress != stamp {
		t.Fatalf("last_progress=%q, want the exact effect timestamp maximum %q", want.LastProgress, stamp)
	}
	assertWorkerFactsLogBlind(t, s, map[string]func() (any, error){
		"existing-work": func() (any, error) { return deriveWorkLivenessQ(t.Context(), s.db, work) },
		"unknown-work":  func() (any, error) { return deriveWorkLivenessQ(t.Context(), s.db, "unknown-work") },
	})
}

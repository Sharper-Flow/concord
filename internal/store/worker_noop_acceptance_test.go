package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestAcceptWorkerResultRefusesNoOpLaneReport(t *testing.T) {
	const workID = "accept-noop-implementation"
	f := seedOracleRepairFixture(t, workID)
	s := f.store
	defer s.Close()
	job := recordOracleJobRevision(t, s, workID, f.owner, "job:noop", twoPathOracleFields(t, s, workID, nil))
	attempt := "attempt:" + workID
	subject := dispatchChangeAcceptanceAttempt(t, f, workID, attempt, job)
	completed := completeChangeAcceptanceAttempt(t, f, workID, attempt, subject)
	if completed.ObservedDelivery == nil || completed.ObservedDelivery.State != "clean" || completed.ObservedDelivery.Commit != subject {
		t.Fatalf("no-op fixture must complete clean at its dispatch subject: observation=%+v subject=%s", completed.ObservedDelivery, subject)
	}
	before := maxEventSeq(t, s, workID)
	err := acceptChangeTestAttempt(t, f, workID, attempt)
	if err == nil || !strings.Contains(err.Error(), "no changed paths") {
		t.Fatalf("no-op implementation acceptance error = %v, want the no changed paths refusal", err)
	}
	if after := maxEventSeq(t, s, workID); after != before {
		t.Fatalf("refused no-op acceptance changed the event log: before=%d after=%d", before, after)
	}
	if err := runVerdictActionAs(t, s, workID, "reject_worker_result", mustJSONValue(map[string]any{
		"attempt_id": attempt, "attempt_epoch": latestStepStartEpoch(t, s, workID, "repair"),
		"diagnosis": "the editing attempt returned without changing its dispatch subject", "strategy": "deliver the recorded job in a correction",
		"predicate_ids": []string{"predicate:return-route"}, "evidence_refs": []string{"evidence:unchanged-subject"},
	}), 0, f.acceptor); err != nil {
		t.Fatalf("no-op result lost its rejection route: %v", err)
	}
}

func dispatchChangeAcceptanceAttempt(t *testing.T, f oracleRepairFixture, workID, attempt string, job WorkerJobBinding) string {
	t.Helper()
	s := f.store
	lane := reviewGateLane(t, "implementation")
	packet := joinPacketFor(t, s, workID, "repair", attempt, lane.ID, lane.Version, lane.Digest)
	packet["inputs"].(map[string]any)["worker_job"] = recordedPacketJobForTest(t, s, workID, job, lane.ID)
	view, err := readWorkContextView(context.Background(), s.db, workID)
	if err != nil {
		t.Fatal(err)
	}
	packet["inputs"].(map[string]any)["work_context"] = view
	if pin := issue1013Pin(t, s, workID); pin.Correction != nil {
		packet["inputs"].(map[string]any)["correction"] = pin.Correction
	}
	if err := dispatchJobPacketForTest(t, s, workID, attempt, f.owner, "dispatch-"+attempt, packet); err != nil {
		t.Fatal(err)
	}
	recordJobBoundWorkerDispatch(t, s, workID, attempt, &job)
	return view.SubjectCommit
}

func completeChangeAcceptanceAttempt(t *testing.T, f oracleRepairFixture, workID, attempt, head string) WorkerCompletedPayload {
	t.Helper()
	s := f.store
	completion := oracleCompletionEvent(t, s, workID, attempt, "noop-completion", nil)
	prepared, err := PrepareWorkerTerminalDelivery(context.Background(), s, PrepareWorkerTerminalDeliveryRequest{
		WorkID: workID, AttemptID: attempt, EventID: completion.EventID, Runner: qualifiedSubjectRunner{head: head},
	})
	if err != nil {
		t.Fatal(err)
	}
	workerTerminalEventThroughSeamCtx(t, WithWorkerTerminalPreparation(context.Background(), prepared), s, completion)
	var raw []byte
	if err := s.db.QueryRow(`SELECT payload FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.attempt_id')=?`, workID, WorkerCompleted, attempt).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var completed WorkerCompletedPayload
	if err := json.Unmarshal(raw, &completed); err != nil {
		t.Fatal(err)
	}
	return completed
}

func acceptChangeTestAttempt(t *testing.T, f oracleRepairFixture, workID, attempt string) error {
	t.Helper()
	return runVerdictActionAs(t, f.store, workID, "accept_worker_result", mustJSONValue(map[string]any{
		"attempt_id": attempt, "attempt_epoch": latestStepStartEpoch(t, f.store, workID, "repair"),
	}), 0, f.acceptor)
}

func TestAcceptWorkerResultChangeObservationBoundaries(t *testing.T) {
	for _, historical := range []bool{false, true} {
		name := "changed-subject"
		if historical {
			name = "historical-unobserved"
		}
		t.Run(name, func(t *testing.T) {
			workID := "accept-change-" + name
			f := seedOracleRepairFixture(t, workID)
			defer f.store.Close()
			job := recordOracleJobRevision(t, f.store, workID, f.owner, "job:boundary", twoPathOracleFields(t, f.store, workID, nil))
			attempt := "attempt:" + workID
			dispatchChangeAcceptanceAttempt(t, f, workID, attempt, job)
			if historical {
				if err := completeJobBoundAttemptForTest(f.store, workID, attempt, reviewGateLane(t, "implementation"), &job); err != nil {
					t.Fatal(err)
				}
			} else {
				completeChangeAcceptanceAttempt(t, f, workID, attempt, strings.Repeat("2", 40))
			}
			if err := acceptChangeTestAttempt(t, f, workID, attempt); err != nil {
				t.Fatalf("accept %s: %v", name, err)
			}
		})
	}
}

func TestAcceptWorkerResultKeepsRetainedDraftCorrection(t *testing.T) {
	const workID = "accept-retained-draft"
	f := seedOracleRepairFixture(t, workID)
	defer f.store.Close()
	job := recordOracleJobRevision(t, f.store, workID, f.owner, "job:retained-draft", twoPathOracleFields(t, f.store, workID, nil))
	first := "attempt:" + workID + ":first"
	originalSubject := dispatchChangeAcceptanceAttempt(t, f, workID, first, job)
	s2WorkerFailedAttributedLive(t, f.store, workID, first, "the attempt committed its draft but failed before a judgeable report")
	advanceOracleFixtureSubject(t, f, workID, "2")
	job = recordOracleJobRevision(t, f.store, workID, f.owner, job.JobID, twoPathOracleFields(t, f.store, workID, nil))
	corrective := "attempt:" + workID + ":correction"
	subject := dispatchChangeAcceptanceAttempt(t, f, workID, corrective, job)
	if subject == originalSubject {
		t.Fatal("retained draft fixture did not advance beyond the original authoring subject")
	}
	completed := completeChangeAcceptanceAttempt(t, f, workID, corrective, subject)
	if completed.ObservedDelivery == nil || completed.ObservedDelivery.Commit != subject {
		t.Fatal("corrective completion must retain its own dispatch subject")
	}
	if err := acceptChangeTestAttempt(t, f, workID, corrective); err != nil {
		t.Fatalf("retained draft correction refused as a no-op: %v", err)
	}
}

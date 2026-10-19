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
	lane := reviewGateLane(t, "implementation")
	attempt := "attempt:" + workID
	packet := joinPacketFor(t, s, workID, "repair", attempt, lane.ID, lane.Version, lane.Digest)
	packet["inputs"].(map[string]any)["worker_job"] = recordedPacketJobForTest(t, s, workID, job)
	view, err := readWorkContextView(context.Background(), s.db, workID)
	if err != nil {
		t.Fatal(err)
	}
	packet["inputs"].(map[string]any)["work_context"] = view
	if err := dispatchJobPacketForTest(t, s, workID, attempt, f.owner, "noop-dispatch", packet); err != nil {
		t.Fatal(err)
	}
	recordJobBoundWorkerDispatch(t, s, workID, attempt, &job)
	completion := oracleCompletionEvent(t, s, workID, attempt, "noop-completion", nil)
	prepared, err := PrepareWorkerTerminalDelivery(context.Background(), s, PrepareWorkerTerminalDeliveryRequest{
		WorkID: workID, AttemptID: attempt, EventID: completion.EventID, Runner: qualifiedSubjectRunner{head: view.SubjectCommit},
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
	if completed.ObservedDelivery == nil || completed.ObservedDelivery.State != "clean" || completed.ObservedDelivery.Commit != view.SubjectCommit {
		t.Fatalf("no-op fixture must complete clean at its dispatch subject: observation=%+v subject=%s", completed.ObservedDelivery, view.SubjectCommit)
	}
	before := maxEventSeq(t, s, workID)
	err = runVerdictActionAs(t, s, workID, "accept_worker_result", mustJSONValue(map[string]any{"attempt_id": attempt, "attempt_epoch": latestStepStartEpoch(t, s, workID, "repair")}), 0, f.acceptor)
	if err == nil || !strings.Contains(err.Error(), "no changed paths") {
		t.Fatalf("no-op implementation acceptance error = %v, want the no changed paths refusal", err)
	}
	if after := maxEventSeq(t, s, workID); after != before {
		t.Fatalf("refused no-op acceptance changed the event log: before=%d after=%d", before, after)
	}
}

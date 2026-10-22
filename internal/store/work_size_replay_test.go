package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestWorkSizeReplayKeepsAnInheritedRecordedReviewBasis(t *testing.T) {
	s := openTemp(t)
	seedWork(t, s, "sized-predecessor")
	seedWork(t, s, "sized-successor")
	model := workSizedPayload{ModelVersion: 2, Size: WorkSizeCrucial, Inputs: workSizeInputsFixture(), Footprint: workSizeFootprintFixture(), ReviewBasis: []string{"accepted-review"}}
	insert := func(id, kind, subject string, version int, payload any) int64 {
		t.Helper()
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		result, err := s.db.Exec(`INSERT INTO domain_events(event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload) VALUES(?,?,'work_item',?,'operator','2026-10-19T00:00:00Z',?,?)`, id, kind, subject, version, string(raw))
		if err != nil {
			t.Fatal(err)
		}
		seq, err := result.LastInsertId()
		if err != nil {
			t.Fatal(err)
		}
		return seq
	}
	// A historical resize copied its source model's accumulated proof into
	// the successor's first record. Replay no longer has a lineage table.
	sourceSeq := insert("source-model", WorkSized, "sized-predecessor", 2, model)
	resizeSeq := insert("historical-resize", WorkResized, "sized-predecessor", 1, workResizedPayload{SuccessorWorkID: "sized-successor", SourceModelEventID: "source-model", SuccessorModelEventID: "successor-model", RootWorkID: "sized-predecessor", EvidenceSource: workResizeEvidenceSource{WorkID: "sized-predecessor", ThroughSeq: sourceSeq}})
	raw, _ := json.Marshal(model)
	event := Event{Seq: resizeSeq + 1, EventID: "successor-model", Kind: WorkSized, SubjectType: SubjectWorkItem, SubjectID: "sized-successor", Actor: "operator", OccurredAt: time.Unix(30, 0).UTC(), PayloadVersion: 2, Payload: raw}
	tx := lawChangeTx(t, s)
	defer func() { _ = tx.Rollback() }()
	if _, err := beginFold(context.Background(), tx); err != nil {
		t.Fatal(err)
	}
	// A live event cannot borrow a retired resize's basis.
	requireFailureKind(t, foldWorkSized(context.Background(), tx, event), KindInvariantViolation, "live inherited proof")
	if err := foldWorkSized(workflowReplayContext(context.Background()), tx, event); err != nil {
		t.Fatalf("historical inherited basis refused: %v", err)
	}
	decision, sized, err := readEffectiveWorkSize(context.Background(), tx, event.SubjectID)
	if err != nil || !sized || decision.Effective != WorkSizeCrucial {
		t.Fatalf("replayed size=%+v sized=%v err=%v", decision, sized, err)
	}
}

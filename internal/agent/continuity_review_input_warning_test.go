package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

func TestContinuityReviewInputWarningStaysOutsideProjection(t *testing.T) {
	for _, kind := range []store.FailureKind{store.KindGitUnreachable, store.KindTimeout} {
		t.Run(string(kind), func(t *testing.T) {
			snapshot := store.ContinuitySnapshot{WorkID: "work-warning"}
			before, err := json.Marshal(ContinuityPayload(snapshot))
			if err != nil {
				t.Fatal(err)
			}
			beforeSnapshot, err := json.Marshal(snapshot)
			if err != nil {
				t.Fatal(err)
			}
			snapshot.ReviewInputWarning = &store.Failure{Kind: kind, Detail: "cannot observe the claimed worktree's candidate tree", Err: errors.New("synthetic Git probe failure")}
			after, err := json.Marshal(ContinuityPayload(snapshot))
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("warning changed projection bytes: %v", err)
			}
			afterSnapshot, err := json.Marshal(snapshot)
			if err != nil || !bytes.Equal(beforeSnapshot, afterSnapshot) {
				t.Fatalf("warning changed snapshot JSON: %v", err)
			}
			meta := continuityResultMeta(snapshot, nil)
			envelope := applyMeta(Envelope{}, meta, nil)
			if len(envelope.Warnings) != 1 || envelope.Warnings[0].Kind != "review_input_candidate_tree:"+string(kind) {
				t.Fatalf("warning lost its phase or typed cause: %+v", envelope.Warnings)
			}
			if len(envelope.Warnings[0].Kind) > 64 {
				t.Fatal("warning exceeds the published notice kind bound")
			}
		})
	}
}

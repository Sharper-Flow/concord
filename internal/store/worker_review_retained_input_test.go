package store

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestRetainedReviewPacketRequiresExactDisclosure(t *testing.T) {
	j, job := retainedReviewFixture(t, "retained-packet-disclosure")
	baseline, err := readWorkflowReviewFacts(t.Context(), j.s.db, j.workID)
	if err != nil {
		t.Fatal(err)
	}
	head := retainedReviewEdit(t, j, job, "implement", "failed-editor")
	job = retainedReviewRevision(t, j, []string{"seed.txt"})
	// Force status to reconsider the stat cache without changing file content.
	mtime := time.Now().Add(-time.Hour)
	if err := os.Chtimes(filepath.Join(j.repo, "seed.txt"), mtime, mtime); err != nil {
		t.Fatal(err)
	}
	indexPath := filepath.Join(j.repo, ".git", "index")
	indexBefore, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	before := j.effects(t)
	snapshot, err := ReadWorkflowContinuity(t.Context(), j.s, ContinuityRequest{Work: j.workID})
	if err != nil || snapshot.ReviewInputWarning != nil || snapshot.ReviewInput == nil {
		t.Fatalf("continuity descriptor: %+v, warning=%v, err=%v", snapshot.ReviewInput, snapshot.ReviewInputWarning, err)
	}
	want := &WorkerReviewRetainedFailedEdit{AcceptedCandidate: *baseline.Candidate, ChangedPaths: []string{"seed.txt"}}
	if snapshot.ReviewInput.InputCandidate.Commit != head || !reflect.DeepEqual(snapshot.ReviewInput.RetainedFailedEdit, want) {
		t.Fatalf("disclosure = %+v; want H=%s, C/paths=%+v", snapshot.ReviewInput, head, want)
	}
	if j.effects(t) != before {
		t.Fatal("continuity read changed durable state")
	}
	if indexAfter, err := os.ReadFile(indexPath); err != nil || !bytes.Equal(indexBefore, indexAfter) {
		t.Fatalf("continuity refreshed the Git index: %v", err)
	}
	lane := reviewGateLane(t, "review")
	packet := joinPacketFor(t, j.s, j.workID, currentStep(t, j.s, j.workID), "fresh-review", lane.ID, lane.Digest)
	inputs := packet["inputs"].(map[string]any)
	inputs["worker_job"] = recordedPacketJobForTest(t, j.s, j.workID, job, lane.ID)
	correction, err := workflowCorrectionContextForDispatch(t.Context(), j.s.db, j.workID, currentStep(t, j.s, j.workID), "fresh-review")
	if err != nil {
		t.Fatal(err)
	}
	if correction != nil {
		inputs["correction"] = correction
	}
	prepared, err := PrepareWorkerDispatchFreshness(t.Context(), j.s, PrepareWorkerDispatchRequest{WorkID: j.workID, SessionWorktree: j.repo, DispatchPayload: mustJSONValue(map[string]any{"attempt_id": "fresh-review", "worker_packet": packet}), reviewPlanner: oracleFixtureReviewPlanner(t, oracleFixtureRuntime)})
	if err != nil || prepared.proof.retainedReview == nil {
		t.Fatalf("native proof: %v", err)
	}
	ctx := WithWorkerDispatchPreparation(t.Context(), prepared)
	for _, shape := range []string{"omitted", "project", "commit", "tree", "paths omitted", "path injected", "path changed", "valid"} {
		t.Run(shape, func(t *testing.T) {
			var input WorkerReviewInput
			if err := json.Unmarshal(mustJSONValue(snapshot.ReviewInput), &input); err != nil {
				t.Fatal(err)
			}
			switch shape {
			case "omitted":
				input.RetainedFailedEdit = nil
			case "project":
				input.RetainedFailedEdit.AcceptedCandidate.ProjectID = "project:forged"
			case "commit":
				input.RetainedFailedEdit.AcceptedCandidate.Commit = strings.Repeat("f", 40)
			case "tree":
				input.RetainedFailedEdit.AcceptedCandidate.Tree = strings.Repeat("f", 40)
			case "paths omitted":
				input.RetainedFailedEdit.ChangedPaths = nil
			case "path injected":
				input.RetainedFailedEdit.ChangedPaths = append(input.RetainedFailedEdit.ChangedPaths, "injected.txt")
			case "path changed":
				input.RetainedFailedEdit.ChangedPaths[0] = "forged.txt"
			}
			inputs["review_input"] = input
			_, err := dispatchJoinAttempt(ctx, t, j.s, j.workID, readWorkVersion(t, j.s, j.workID), j.fixture.owner, packet)
			if shape == "valid" {
				if err != nil {
					t.Fatalf("exact producer disclosure refused: %v", err)
				}
				return
			}
			if failureKind(err) != KindInvalidPayload || !strings.Contains(err.Error(), "review_input") {
				t.Fatalf("forged %s: %v", shape, err)
			}
			if j.effects(t) != before {
				t.Fatal("forged descriptor changed durable state")
			}
		})
	}
}

func TestRetainedReviewContinuityDoesNotDropMissingObservation(t *testing.T) {
	j, job := retainedReviewFixture(t, "retained-continuity-observation")
	retainedReviewEdit(t, j, job, "implement", "failed-editor")
	retainedReviewRevision(t, j, []string{"seed.txt"})
	if err := os.WriteFile(filepath.Join(j.repo, "seed.txt"), []byte("unqualified dirty change\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := j.effects(t)
	snapshot, err := ReadWorkflowContinuity(t.Context(), j.s, ContinuityRequest{Work: j.workID})
	if err != nil || snapshot.ReviewInputWarning == nil || snapshot.ReviewInput != nil {
		t.Fatalf("missing observation silently lost disclosure: input=%+v warning=%v err=%v", snapshot.ReviewInput, snapshot.ReviewInputWarning, err)
	}
	if j.effects(t) != before {
		t.Fatal("failed continuity enrichment changed durable state")
	}
}

package store

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestReviewContinuityCarriesRecordedWorkSize(t *testing.T) {
	for _, kind := range []string{"unsized", "small", "larger", "crucial", "override"} {
		t.Run(kind, func(t *testing.T) {
			f := seedWorkContextFixture(t, "review-size-"+kind)
			t.Cleanup(func() { _ = f.store.Close() })
			if kind != "unsized" {
				inputs := workSizeInputsFixture()
				if kind == "larger" {
					inputs.Declared.EstimatedProductionLines = 61
				}
				if kind == "crucial" {
					inputs.Declared.Security = true
				}
				size, err := DecideWorkSize(WorkSizeModelVersion, inputs, nil)
				if err != nil {
					t.Fatal(err)
				}
				if err := applyWorkEvent(t, f.store, workSizedEvent("review-size-record", f.workID, WorkSizeModelVersion, size, inputs), nil); err != nil {
					t.Fatal(err)
				}
				if kind == "override" {
					insertWorkSizeOverrideApproval(t, f.store, f.workID, 1)
					if err := runWorkSizeOverride(t, f.store, workSizeOverrideRequest(t, f.store, f.workID, "review-size-override", WorkSizeCrucial)); err != nil {
						t.Fatal(err)
					}
				}
			}
			reviewSubjectClaim(t, f.store, f.workID)
			snapshot, err := ReadWorkflowContinuity(t.Context(), f.store, ContinuityRequest{Work: f.workID})
			if err != nil || snapshot.ReviewInput == nil {
				t.Fatalf("review input = %+v, %v", snapshot.ReviewInput, err)
			}
			var got struct {
				Size *WorkSizeSummary `json:"size"`
			}
			if err := json.Unmarshal(mustJSONValue(snapshot.ReviewInput), &got); err != nil {
				t.Fatal(err)
			}
			want, err := readWorkSizeSummary(t.Context(), f.store.db, f.workID)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got.Size, want) {
				t.Fatalf("review size = %+v, want canonical recorded summary %+v", got.Size, want)
			}
			if kind != "unsized" {
				model, effective := WorkSize(kind), WorkSize(kind)
				if kind == "override" {
					model, effective = WorkSizeSmall, WorkSizeCrucial
				}
				if got.Size == nil || got.Size.Size != effective || got.Size.ModelSize != model || got.Size.ModelVersion != WorkSizeModelVersion || got.Size.RecordedAt == "" {
					t.Fatalf("%s lost its recorded size/provenance: %+v", kind, got.Size)
				}
				if kind == "override" {
					if got.Size.OperatorSize == nil || *got.Size.OperatorSize != WorkSizeCrucial {
						t.Fatalf("override lost its operator size: %+v", got.Size)
					}
				} else if got.Size.OperatorSize != nil {
					t.Fatalf("model-only work invented an operator size: %+v", got.Size)
				}
			}
			if kind == "unsized" && strings.Contains(string(mustJSONValue(snapshot.ReviewInput)), `"size"`) {
				t.Fatal("unsized Review input must omit size, not default or null it")
			}
		})
	}
}

func TestReviewDispatchRequiresExactReviewInput(t *testing.T) {
	for _, field := range []string{"missing", "project", "commit", "tree", "criterion", "architecture", "other lane", "valid", "size valid", "size missing", "size effective", "size model", "size operator", "size version", "size timestamp", "size stale", "unsized size"} {
		t.Run(field, func(t *testing.T) {
			f := seedWorkContextFixture(t, "review-input-"+strings.ReplaceAll(field, " ", "-"))
			t.Cleanup(func() { _ = f.store.Close() })
			if strings.HasPrefix(field, "size ") {
				if err := applyWorkEvent(t, f.store, workSizedEvent("review-size-record", f.workID, WorkSizeModelVersion, WorkSizeSmall, workSizeInputsFixture()), nil); err != nil {
					t.Fatal(err)
				}
			}
			repo, commit := reviewSubjectClaim(t, f.store, f.workID)
			var branch string
			if err := f.store.db.QueryRow(`SELECT branch FROM worktree_entries WHERE set_id=? AND state='active'`, WorktreeSetID(f.workID)).Scan(&branch); err != nil {
				t.Fatal(err)
			}
			gitRunStore(t, repo, "branch", "-m", branch)
			job := reviewSubjectReadyRevision(t, f)
			lane := "review"
			if field == "other lane" {
				lane = "implement"
			}
			digest := registeredLaneIdentity(t, lane)
			packet := joinPacketFor(t, f.store, f.workID, currentStep(t, f.store, f.workID), "attempt:review-input", lane, digest)
			inputs := packet["inputs"].(map[string]any)
			inputs["worker_job"] = recordedPacketJobForTest(t, f.store, f.workID, job, lane)
			deps, err := workflowReviewDependencies(t.Context(), f.store.db, f.workID)
			if err != nil {
				t.Fatal(err)
			}
			tree, err := readReviewCandidateTree(t.Context(), ExecGitRunner{}, repo, commit)
			if err != nil {
				t.Fatal(err)
			}
			descriptor := WorkerReviewInput{InputCandidate: WorkerReviewCandidate{ProjectID: "project", Commit: commit, Tree: tree}, Dependencies: deps}
			descriptor.Size, err = readWorkSizeSummary(t.Context(), f.store.db, f.workID)
			if err != nil {
				t.Fatal(err)
			}
			switch field {
			case "project":
				descriptor.InputCandidate.ProjectID = "another-project"
			case "commit":
				descriptor.InputCandidate.Commit = strings.Repeat("f", 40)
			case "tree":
				descriptor.InputCandidate.Tree = strings.Repeat("f", 40)
			case "criterion":
				descriptor.Dependencies.Criteria[0].ContentDigest = "sha256:" + strings.Repeat("f", 64)
			case "architecture":
				descriptor.Dependencies.ArchitectureDigest = "sha256:" + strings.Repeat("f", 64)
				descriptor.Dependencies.ArchitectureNotApplicable = false
			case "size missing":
				descriptor.Size = nil
			case "size effective":
				descriptor.Size.Size = WorkSizeCrucial
			case "size model":
				descriptor.Size.ModelSize = WorkSizeCrucial
			case "size operator":
				size := WorkSizeCrucial
				descriptor.Size.OperatorSize = &size
			case "size version":
				descriptor.Size.ModelVersion++
			case "size timestamp":
				descriptor.Size.RecordedAt = "2026-10-28T00:00:00Z"
			case "size stale":
				inputs := workSizeInputsFixture()
				inputs.Declared.EstimatedProductionLines = 61
				if err := applyWorkEvent(t, f.store, workSizedEvent("review-size-rerun", f.workID, WorkSizeModelVersion, WorkSizeLarger, inputs), nil); err != nil {
					t.Fatal(err)
				}
			case "unsized size":
				descriptor.Size = &WorkSizeSummary{Size: WorkSizeSmall, ModelSize: WorkSizeSmall, ModelVersion: WorkSizeModelVersion, RecordedAt: "2026-10-28T00:00:00Z"}
			}
			if field != "missing" {
				inputs["review_input"] = descriptor
			} else {
				delete(inputs, "review_input")
			}
			prepared, err := PrepareWorkerDispatchFreshness(t.Context(), f.store, PrepareWorkerDispatchRequest{WorkID: f.workID, SessionWorktree: repo, DispatchPayload: mustJSONValue(map[string]any{"attempt_id": "attempt:review-input", "worker_packet": packet}), reviewPlanner: oracleFixtureReviewPlanner(t, oracleFixtureRuntime)})
			if err != nil {
				t.Fatal(err)
			}
			_, err = dispatchJoinAttempt(WithWorkerDispatchPreparation(context.Background(), prepared), t, f.store, f.workID, readWorkVersion(t, f.store, f.workID), f.owner, packet)
			if (err == nil) != (field == "valid" || field == "size valid") {
				t.Fatalf("dispatch %s = %v", field, err)
			}
			if (strings.HasPrefix(field, "size ") && field != "size valid" || field == "unsized size") && !strings.Contains(err.Error(), "review_input") {
				t.Fatalf("size mismatch must fail existing Review input binding, got %v", err)
			}
		})
	}
}

func TestReviewContinuityPreservesRecoveryOnCandidateTreeFailure(t *testing.T) {
	f := seedWorkContextFixture(t, "review-input-tree-failure")
	t.Cleanup(func() { _ = f.store.Close() })
	repo, _ := reviewSubjectClaim(t, f.store, f.workID)
	snapshot, err := ReadWorkflowContinuity(t.Context(), f.store, ContinuityRequest{Work: f.workID})
	if err != nil || snapshot.ReviewInput == nil {
		t.Fatalf("reachable candidate: %+v, %v", snapshot.ReviewInput, err)
	}
	if err := os.Rename(repo+"/.git", repo+"/.git-unavailable"); err != nil {
		t.Fatal(err)
	}
	t.Run("continuity", func(t *testing.T) {
		snapshot, err := ReadWorkflowContinuity(t.Context(), f.store, ContinuityRequest{Work: f.workID})
		if err != nil {
			t.Fatalf("optional review input blocked continuity: %v", err)
		}
		var failure *Failure
		if !failureAs(snapshot.ReviewInputWarning, &failure) || failure.Kind != KindGitUnreachable {
			t.Fatalf("want typed candidate-tree Git warning, got %v", snapshot.ReviewInputWarning)
		}
		if !strings.Contains(failure.Detail, "candidate tree") || failure.Err == nil || !failure.RetrySafe || failure.RecoveryAction == "" {
			t.Fatalf("candidate-tree failure lost its cause or recovery: %+v", failure)
		}
		if snapshot.ReviewInput != nil {
			t.Fatal("unreachable candidate must not expose review input")
		}
	})
}

func TestReviewPacketClaimsRemainBoundAtAcceptance(t *testing.T) {
	for _, field := range []string{"valid", "project", "commit", "tree", "criterion", "architecture"} {
		t.Run(field, func(t *testing.T) {
			f := seedWorkContextFixture(t, "review-input-accept-"+field)
			t.Cleanup(func() { _ = f.store.Close() })
			repo, _ := reviewSubjectClaim(t, f.store, f.workID)
			var branch string
			if err := f.store.db.QueryRow(`SELECT branch FROM worktree_entries WHERE set_id=? AND state='active'`, WorktreeSetID(f.workID)).Scan(&branch); err != nil {
				t.Fatal(err)
			}
			gitRunStore(t, repo, "branch", "-m", branch)
			snapshot, err := ReadWorkflowContinuity(t.Context(), f.store, ContinuityRequest{Work: f.workID})
			if err != nil || snapshot.ReviewInput == nil {
				t.Fatalf("review input: %+v, %v", snapshot.ReviewInput, err)
			}
			input := snapshot.ReviewInput
			attempt, epoch := completeCurrentReviewForTest(t, f, repo, "ready_for_final_proof", func(report *WorkerReviewBlock) {
				report.InputCandidate, report.OutputCandidate, report.Dependencies = input.InputCandidate, input.InputCandidate, input.Dependencies
				switch field {
				case "project":
					report.InputCandidate.ProjectID = "project:other"
				case "commit":
					report.InputCandidate.Commit = strings.Repeat("f", 40)
				case "tree":
					report.InputCandidate.Tree = strings.Repeat("f", 40)
				case "criterion":
					report.Dependencies.Criteria[0].ContentDigest = "sha256:" + strings.Repeat("f", 64)
				case "architecture":
					report.Dependencies.ArchitectureNotApplicable = false
					report.Dependencies.ArchitectureDigest = "sha256:" + strings.Repeat("f", 64)
				}
			})
			j := &candidateJourney{fixture: f, s: f.store, workID: f.workID, repo: repo}
			err = j.action(t, "accept_worker_result", map[string]any{"attempt_id": attempt, "attempt_epoch": epoch}, f.owner)
			if (err == nil) != (field == "valid") {
				t.Fatalf("acceptance %s: %v", field, err)
			}
		})
	}
}

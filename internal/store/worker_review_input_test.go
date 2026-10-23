package store

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestReviewDispatchRequiresExactReviewInput(t *testing.T) {
	for _, field := range []string{"missing", "project", "commit", "tree", "criterion", "architecture", "other lane", "valid"} {
		t.Run(field, func(t *testing.T) {
			f := seedWorkContextFixture(t, "review-input-"+strings.ReplaceAll(field, " ", "-"))
			t.Cleanup(func() { _ = f.store.Close() })
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
			version, digest := registeredLaneIdentity(t, lane)
			packet := joinPacketFor(t, f.store, f.workID, currentStep(t, f.store, f.workID), "attempt:review-input", lane, version, digest)
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
			}
			if field != "missing" {
				inputs["review_input"] = descriptor
			} else {
				delete(inputs, "review_input")
			}
			prepared, err := PrepareWorkerDispatchFreshness(t.Context(), f.store, PrepareWorkerDispatchRequest{WorkID: f.workID, SessionWorktree: repo})
			if err != nil {
				t.Fatal(err)
			}
			_, err = dispatchJoinAttempt(WithWorkerDispatchPreparation(context.Background(), prepared), t, f.store, f.workID, readWorkVersion(t, f.store, f.workID), f.owner, packet)
			if (err == nil) != (field == "valid") {
				t.Fatalf("dispatch %s = %v", field, err)
			}
		})
	}
}

func TestReviewEvalDependencyDigestsMatchStore(t *testing.T) {
	for _, name := range []string{"bounded-review", "refuses-self-acceptance", "seeded-scope-violation", "seeded-evidence-gap", "seeded-validator-weakening"} {
		t.Run(name, func(t *testing.T) {
			raw, err := os.ReadFile("../../adapter/opencode/evals/packets/review-" + name + ".json")
			if err != nil {
				t.Fatal(err)
			}
			var packet struct {
				Inputs struct {
					ReviewInput *WorkerReviewInput `json:"review_input"`
					Predicates  []struct {
						ID      string          `json:"predicate_id"`
						Kind    string          `json:"outcome_kind"`
						Payload json.RawMessage `json:"outcome_payload"`
					} `json:"outcome_predicates"`
					Design *WorkflowDesignRecord `json:"design_record"`
				} `json:"inputs"`
			}
			if err := json.Unmarshal(raw, &packet); err != nil {
				t.Fatal(err)
			}
			if packet.Inputs.ReviewInput == nil {
				t.Fatal("review packet lacks review_input")
			}
			deps := packet.Inputs.ReviewInput.Dependencies
			if len(deps.Criteria) != len(packet.Inputs.Predicates) {
				t.Fatal("criterion population differs")
			}
			for i, p := range packet.Inputs.Predicates {
				if deps.Criteria[i].PredicateID != p.ID || deps.Criteria[i].ContentDigest != workflowCriterionDigest(p.Kind, p.Payload) {
					t.Fatalf("criterion digest differs: %+v", deps.Criteria[i])
				}
			}
			if packet.Inputs.Design == nil {
				if !deps.ArchitectureNotApplicable || deps.ArchitectureDigest != "" {
					t.Fatal("absent design must be not applicable")
				}
			} else {
				d := packet.Inputs.Design
				want := reviewDigest(map[string]any{"approach": d.Approach, "decisions": d.Decisions, "touched_refs": d.TouchedRefs})
				if deps.ArchitectureNotApplicable || deps.ArchitectureDigest != want {
					t.Fatal("design digest differs")
				}
			}
		})
	}
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

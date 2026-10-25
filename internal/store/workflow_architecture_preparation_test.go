package store

import (
	"slices"
	"strings"
	"testing"
)

func TestArchitecturePreparationAcceptsCriteriaBeforeDesign(t *testing.T) {
	t.Parallel()
	definition := mustBuiltinDefinition(t, "workflow.implementation").Definition
	criteria, architecture := -1, -1
	for i, step := range definition.StepGraph.Steps {
		if slices.Contains(step.Actions, "approve_contract") {
			criteria = i
		}
		if slices.Contains(step.Actions, "record_design") {
			architecture = i
		}
	}
	if criteria < 0 || architecture < 0 || criteria >= architecture {
		t.Fatalf("architecture preparation must accept criteria before recording design: criteria=%d, architecture=%d", criteria, architecture)
	}
}

func TestArchitecturePreparationRecordingDoesNotAdvance(t *testing.T) {
	t.Parallel()
	action, err := catalogAction("record_design")
	if err != nil {
		t.Fatal(err)
	}
	if action.ExecutionMode != ActionHold {
		t.Fatalf("record_design mode=%q, want hold until attributed architecture review is accepted", action.ExecutionMode)
	}
}

func TestArchitecturePreparationOwnsScopedReviewJobRoute(t *testing.T) {
	t.Parallel()
	definition := mustBuiltinDefinition(t, "workflow.implementation").Definition
	for _, step := range definition.StepGraph.Steps {
		if !slices.Contains(step.Actions, "record_design") {
			continue
		}
		for _, action := range []string{"record_worker_job", "dispatch_worker", "accept_worker_result"} {
			if !slices.Contains(step.Actions, action) {
				t.Errorf("architecture step %q cannot %s for its scoped Review", step.ID, action)
			}
		}
		return
	}
	t.Fatal("implementation has no architecture part")
}

func TestArchitecturePreparationDoesNotAddAnAbsentDesignPart(t *testing.T) {
	t.Parallel()
	composition := builtinWorkflowComposition(t, "workflow.implementation")
	composition.Parts = slices.DeleteFunc(slices.Clone(composition.Parts), func(use WorkflowPartUse) bool {
		return use.Ref == "part.design"
	})
	definition, err := CompileWorkflowComposition(composition)
	if err != nil {
		t.Fatalf("composition without the optional design part: %v", err)
	}
	for _, step := range definition.StepGraph.Steps {
		if slices.Contains(step.Actions, "record_design") {
			t.Fatalf("composition gained architecture step %q without a design part", step.ID)
		}
	}
}

func TestArchitecturePreparationDigestIsNotAnAdequacyAssessment(t *testing.T) {
	t.Parallel()
	review := reviewBlockFixture()
	review.Dependencies.ArchitectureNotApplicable = false
	review.Dependencies.ArchitectureDigest = "sha256:" + strings.Repeat("a", 64)
	if err := validateWorkerReviewBlock(review); !hasFailureKind(err, KindInvalidPayload) {
		t.Fatalf("architecture digest without an attributed adequacy assessment: got %v, want %s", err, KindInvalidPayload)
	}
}

func TestArchitecturePreparationPacketCannotClaimAnotherStage(t *testing.T) {
	for _, claimedStep := range []string{"repair", "refine"} {
		t.Run(claimedStep, func(t *testing.T) {
			f := seedWorkContextFixture(t, "architecture-step-"+claimedStep)
			t.Cleanup(func() { _ = f.store.Close() })
			repo, _ := reviewSubjectClaim(t, f.store, f.workID)
			var branch string
			if err := f.store.db.QueryRow(`SELECT branch FROM worktree_entries WHERE set_id=? AND state='active'`, WorktreeSetID(f.workID)).Scan(&branch); err != nil {
				t.Fatal(err)
			}
			gitRunStore(t, repo, "branch", "-m", branch)
			if step := currentStep(t, f.store, f.workID); step != "repair" {
				t.Fatalf("fixture step=%q, want repair", step)
			}
			job := reviewSubjectReadyRevision(t, f)
			packet := joinPacketFor(t, f.store, f.workID, claimedStep, "attempt:architecture-step", "review", registeredLaneIdentity(t, "review"))
			packet["inputs"].(map[string]any)["worker_job"] = recordedPacketJobForTest(t, f.store, f.workID, job, "review")
			prepared, err := PrepareWorkerDispatchFreshness(t.Context(), f.store, PrepareWorkerDispatchRequest{WorkID: f.workID, SessionWorktree: repo})
			if err != nil {
				t.Fatal(err)
			}
			before := readWorkVersion(t, f.store, f.workID)
			_, err = dispatchJoinAttempt(WithWorkerDispatchPreparation(t.Context(), prepared), t, f.store, f.workID, before, f.owner, packet)
			if claimedStep == "repair" {
				if err != nil {
					t.Fatalf("exact-stage Review dispatch: %v", err)
				}
				return
			}
			if !hasFailureKind(err, KindInvalidPayload) {
				t.Fatalf("Review packet claims %q while the admitted step is repair: got %v, want %s", claimedStep, err, KindInvalidPayload)
			}
			if got := readWorkVersion(t, f.store, f.workID); got != before {
				t.Fatalf("refused stage claim changed work version from %d to %d", before, got)
			}
			var authorized int
			if err := f.store.db.QueryRow(`SELECT COUNT(*) FROM worker_authorizations WHERE work_id=?`, f.workID).Scan(&authorized); err != nil {
				t.Fatal(err)
			}
			if authorized != 0 {
				t.Fatalf("refused stage claim left %d authorizations", authorized)
			}
		})
	}
}

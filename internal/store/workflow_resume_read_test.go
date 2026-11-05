package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// writeResumePreludeFixture records one intake prelude
// through the same write-seam-guarded prelude write the actions use.
func writeResumePreludeFixture(t *testing.T, s *Store, workID string, prelude workflowPreludeState) {
	t.Helper()
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	scope, err := beginWrite(ctx, tx)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeWorkflowPreludeTx(ctx, tx, workID, prelude); err != nil {
		t.Fatal(err)
	}
	if err := scope.close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestReadWorkflowResumeAnswersOpeningStepsBeforeIntake(t *testing.T) {
	t.Parallel()
	s, version := seedBootstrapCapturedWork(t, "resume-opening-fresh")
	resume, err := ReadWorkflowResume(context.Background(), s, "resume-opening-fresh")
	if err != nil {
		t.Fatal(err)
	}
	if resume.Type != "" || resume.Steps != nil || resume.NextSteps != nil {
		t.Fatalf("fresh item answers plan fields: %+v", resume)
	}
	if got := strings.Join(resume.OpeningSteps, ","); got != "intake,pick_type" {
		t.Fatalf("opening steps=%q, want intake,pick_type", got)
	}
	if resume.Version != version {
		t.Fatalf("version=%d, want the work version %d", resume.Version, version)
	}
}

func TestReadWorkflowResumeProceedSkipsClarifyAndEndingsLeaveNone(t *testing.T) {
	t.Parallel()
	s, _ := seedBootstrapCapturedWork(t, "resume-opening-proceed")
	writeResumePreludeFixture(t, s, "resume-opening-proceed", workflowPreludeState{Intake: workflowIntakeRecord{Outcome: WorkflowIntakeProceed}})
	resume, err := ReadWorkflowResume(context.Background(), s, "resume-opening-proceed")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(resume.OpeningSteps, ","); got != "pick_type" {
		t.Fatalf("opening steps=%q, want pick_type", got)
	}

	s2, _ := seedBootstrapCapturedWork(t, "resume-opening-decline")
	writeResumePreludeFixture(t, s2, "resume-opening-decline", workflowPreludeState{Intake: workflowIntakeRecord{Outcome: WorkflowIntakeDecline}})
	resume, err = ReadWorkflowResume(context.Background(), s2, "resume-opening-decline")
	if err != nil {
		t.Fatal(err)
	}
	if len(resume.OpeningSteps) != 0 {
		t.Fatalf("decline leaves opening steps %v, want none", resume.OpeningSteps)
	}
}

func TestReadWorkflowResumeReturnsStoredPlanWithDerivedNext(t *testing.T) {
	t.Parallel()
	s, _ := seedBootstrapCapturedWork(t, "resume-planned")
	seedWorkflowPlanFrontier(t, s, "resume-planned", WorkflowStepPlanChange, "implement")
	resume, err := ReadWorkflowResume(context.Background(), s, "resume-planned")
	if err != nil {
		t.Fatal(err)
	}
	if resume.Type != WorkflowStepPlanChange {
		t.Fatalf("type=%q, want change", resume.Type)
	}
	if len(resume.NextSteps) != 1 || resume.NextSteps[0] != "implement" {
		t.Fatalf("next steps=%v, want [implement]", resume.NextSteps)
	}
	if len(resume.Steps) == 0 {
		t.Fatal("planned read carries no steps")
	}
	// Steps are the stored plan exactly: the completed prefix keeps its
	// attempts, including the reviewed step's reviewer, result, commit, link.
	plan, err := readWorkflowPlanTx(context.Background(), s.db, "resume-planned")
	if err != nil || plan == nil {
		t.Fatalf("read plan: %v", plan)
	}
	got, err := json.Marshal(resume.Steps)
	if err != nil {
		t.Fatal(err)
	}
	want, err := json.Marshal(plan.Steps)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("resume steps are not the stored plan:\n got %s\nwant %s", got, want)
	}
	if resume.Version != readWorkVersion(t, s, "resume-planned") {
		t.Fatalf("version=%d, want %d", resume.Version, readWorkVersion(t, s, "resume-planned"))
	}
}

func TestReadWorkflowResumeCompletePlanHasNoNextStep(t *testing.T) {
	t.Parallel()
	s, _ := seedBootstrapCapturedWork(t, "resume-complete")
	seedWorkflowPlanFrontier(t, s, "resume-complete", WorkflowStepPlanChange, "implement")
	completeWorkflowPlanFixture(t, s, "resume-complete")
	resume, err := ReadWorkflowResume(context.Background(), s, "resume-complete")
	if err != nil {
		t.Fatal(err)
	}
	if len(resume.NextSteps) != 0 {
		t.Fatalf("complete plan answers next steps %v, want none", resume.NextSteps)
	}
}

// cancelResumeWorkFixture cancels one needed work item through the lifecycle
// fold, which closes its workflow instance in the same write.
func cancelResumeWorkFixture(t *testing.T, s *Store, workID string) {
	t.Helper()
	version := readWorkVersion(t, s, workID)
	payload, err := json.Marshal(map[string]any{"from": "needed", "to": "cancelled", "reason": "no longer wanted", "expected_version": version, "resulting_version": version + 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{
		{EventID: "resume-cancel-" + workID, Kind: "work.transitioned", SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "operator", OccurredAt: time.Unix(3, 0).UTC(), PayloadVersion: 1, Payload: payload},
	}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}}); err != nil {
		t.Fatal(err)
	}
}

// A terminal item still answers its stored plan, but nothing remains to do:
// neither an opening step nor a plan step is offered to a resuming session.
func TestReadWorkflowResumeTerminalItemOffersNoStep(t *testing.T) {
	t.Parallel()
	s, _ := seedBootstrapCapturedWork(t, "resume-cancelled-intake")
	cancelResumeWorkFixture(t, s, "resume-cancelled-intake")
	resume, err := ReadWorkflowResume(context.Background(), s, "resume-cancelled-intake")
	if err != nil {
		t.Fatal(err)
	}
	if resume.State != "cancelled" || len(resume.OpeningSteps) != 0 {
		t.Fatalf("cancelled intake item answers state=%q opening=%v, want cancelled and none", resume.State, resume.OpeningSteps)
	}

	s2, _ := seedBootstrapCapturedWork(t, "resume-cancelled-planned")
	seedWorkflowPlanFrontier(t, s2, "resume-cancelled-planned", WorkflowStepPlanChange, "implement")
	cancelResumeWorkFixture(t, s2, "resume-cancelled-planned")
	resume, err = ReadWorkflowResume(context.Background(), s2, "resume-cancelled-planned")
	if err != nil {
		t.Fatal(err)
	}
	if resume.State != "cancelled" || len(resume.Steps) == 0 || len(resume.NextSteps) != 0 {
		t.Fatalf("cancelled planned item answers state=%q steps=%d next=%v, want cancelled, its plan, and no next step", resume.State, len(resume.Steps), resume.NextSteps)
	}
}

func TestReadWorkflowResumeRefusesUnknownAndUnboundedWork(t *testing.T) {
	t.Parallel()
	s, _ := seedBootstrapCapturedWork(t, "resume-known")
	if _, err := ReadWorkflowResume(context.Background(), s, "resume-unknown"); err == nil {
		t.Fatal("unknown work item answered")
	} else if failure, ok := err.(*Failure); !ok || failure.Kind != KindProjectionNotFound {
		t.Fatalf("unknown work failure=%v, want projection_not_found", err)
	}
	if _, err := ReadWorkflowResume(context.Background(), s, "x"); err == nil {
		t.Fatal("out-of-bounds work ID answered")
	} else if failure, ok := err.(*Failure); !ok || failure.Kind != KindInvalidPayload {
		t.Fatalf("bounds failure=%v, want invalid_payload", err)
	}
}

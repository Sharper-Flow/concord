package store

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

func TestReadSessionResumeParity(t *testing.T) {
	for _, planned := range []bool{false, true} {
		t.Run(map[bool]string{false: "opening", true: "plan"}[planned], func(t *testing.T) {
			const id = "session-resume-parity"
			s, _ := seedBootstrapCapturedWork(t, id)
			if planned {
				seedWorkflowPlanFrontier(t, s, id, WorkflowStepPlanChange, "implement")
			}
			ctx := context.Background()
			want, err := ReadWorkflowResume(ctx, s, id)
			if err != nil {
				t.Fatal(err)
			}
			got, err := ReadSessionResume(ctx, s, id, "")
			if err != nil {
				t.Fatal(err)
			}
			if got.Resume == nil || !reflect.DeepEqual(*got.Resume, want) || got.SourceVersionWatermark != got.Resume.SourceVersionWatermark || got.WorkflowInstanceAbsent || got.WorkID != id {
				t.Fatalf("session=%+v; public resume=%+v", got, want)
			}
		})
	}
}

func TestReadSessionResumeAbsentAndBounds(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	const id = "session-imported"
	seedWork(t, s, id)
	got, err := ReadSessionResume(ctx, s, id, "")
	if err != nil {
		t.Fatal(err)
	}
	if !got.WorkflowInstanceAbsent || got.Resume != nil || got.WorkID != id {
		t.Fatalf("imported work=%+v", got)
	}
	if _, err := ReadWorkflowResume(ctx, s, id); failureKind(err) != KindProjectionNotFound {
		t.Fatalf("public absence=%v", err)
	}
	if _, err := ReadSessionResume(ctx, s, "session-unknown", ""); failureKind(err) != KindProjectionNotFound {
		t.Fatalf("unknown=%v", err)
	}
	for _, pair := range [][2]string{{"x", ""}, {id, "x"}, {id, strings.Repeat("p", 129)}} {
		if _, err := ReadSessionResume(ctx, s, pair[0], pair[1]); failureKind(err) != KindInvalidPayload {
			t.Fatalf("bounds %q: %v", pair, err)
		}
	}
}

func TestReadSessionResumeHandoff(t *testing.T) {
	f := setupProjectHandoffFixture(t)
	recorded := recordHandoff(t, f, f.sourceTree)
	read := func(project string) SessionResumeRead {
		t.Helper()
		got, err := ReadSessionResume(context.Background(), f.store, f.workID, project)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got.ProductIdentity, []string{"product-w"}) || got.Resume == nil || got.SourceVersionWatermark != got.Resume.SourceVersionWatermark {
			t.Fatalf("inconsistent read: %+v", got)
		}
		return got
	}
	for _, project := range []string{f.targetProject, f.sourceProject, ""} {
		got := read(project)
		if got.PendingProjectHandoff == nil || got.PendingProjectHandoff.HandoffID != recorded.HandoffID {
			t.Fatalf("project %q: %+v", project, got.PendingProjectHandoff)
		}
	}
	consumeHandoff(t, f, recorded.HandoffID)
	got := read(f.targetProject)
	if got.PendingProjectHandoff == nil || got.PendingProjectHandoff.State != ProjectHandoffConsumed {
		t.Fatalf("addressed consumed: %+v", got.PendingProjectHandoff)
	}
	if got := read(f.sourceProject); got.PendingProjectHandoff != nil {
		t.Fatalf("consumed fallback: %+v", got.PendingProjectHandoff)
	}
}

func TestReadSessionResumeSnapshot(t *testing.T) {
	ctx := context.Background()
	const id = "session-snapshot"
	s, _ := seedBootstrapCapturedWork(t, id)
	before, err := ReadSessionResume(ctx, s, id, "")
	if err != nil {
		t.Fatal(err)
	}
	writer, err := Open(ctx, s.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	tx, err := beginReadTx(ctx, s.db)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := storeRevision(ctx, tx); err != nil {
		t.Fatal(err)
	}
	writeResumePreludeFixture(t, writer, id, workflowPreludeState{Intake: workflowIntakeRecord{Outcome: WorkflowIntakeProceed}})
	seedWork(t, writer, "session-snapshot-other")
	got, err := readSessionResumeTx(ctx, tx, id, "")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, before) {
		t.Fatalf("snapshot changed: got %+v, before %+v", got, before)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	after, err := ReadSessionResume(ctx, s, id, "")
	if err != nil {
		t.Fatal(err)
	}
	if reflect.DeepEqual(after.Resume.OpeningSteps, before.Resume.OpeningSteps) || after.SourceVersionWatermark <= before.SourceVersionWatermark {
		t.Fatalf("write not observed: before %+v, after %+v", before, after)
	}
}

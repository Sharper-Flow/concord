package store

import (
	"encoding/json"
	"testing"

	"github.com/sharper-flow/concord/internal/store/internal/sqlitedb"
)

func TestPlanTakeoverAndRetirement(t *testing.T) {
	ctx := t.Context()
	db, err := sqlitedb.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`CREATE TABLE workflow_instances(work_id TEXT PRIMARY KEY,workflow_type TEXT,step_plan TEXT);
		CREATE TABLE workflow_step_epochs(work_id TEXT,step_id TEXT,attempt_epoch INTEGER,latest_start_seq INTEGER,latest_start_event_id TEXT,latest_start_payload TEXT);
		CREATE TABLE workflow_actors(actor_ref TEXT,session_ref TEXT);
		CREATE TABLE effect_receipts(subject_type TEXT,subject_id TEXT,kind TEXT,effect_seq INTEGER);
		INSERT INTO workflow_instances(work_id) VALUES('work');
		INSERT INTO workflow_actors VALUES('holder','session-a'),('other','session-b')`)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := WorkflowStepPlanTemplate(WorkflowStepPlanChange)
	if err != nil {
		t.Fatal(err)
	}
	writePlan := func() {
		t.Helper()
		raw, err := json.Marshal(plan)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`UPDATE workflow_instances SET workflow_type=?,step_plan=? WHERE work_id='work'`, plan.Type, string(raw)); err != nil {
			t.Fatal(err)
		}
	}
	start := func(step string, seq int, actor string) {
		t.Helper()
		payload, err := json.Marshal(workflowActionStartedPayload{StepID: step, AttemptEpoch: 3, ActorRef: actor})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO workflow_step_epochs VALUES('work',?,3,?,'start',?)`, step, seq, string(payload)); err != nil {
			t.Fatal(err)
		}
	}
	action := WorkflowActionDefinition{ExecutionMode: ActionFenced}
	if got, err := workflowStartTakeover(ctx, db, action, "work", "other"); err != nil || got != nil {
		t.Fatalf("intake takeover = %v, %v", got, err)
	}
	start(plan.Steps[0].ID, 10, "holder")
	if _, err := workflowStartTakeover(ctx, db, action, "work", "other"); err == nil {
		t.Fatal("intake accepted unexpected start")
	}
	if _, err := workExecutionStoppedTx(ctx, db, "work", "session-b"); err == nil {
		t.Fatal("intake retirement accepted another session's unexpected start")
	}
	writePlan()
	held, err := workflowStartTakeover(ctx, db, action, "work", "other")
	if err != nil || held == nil || held.HeldEpoch != 3 || held.HolderActorRef != "holder" {
		t.Fatalf("held takeover = %v, %v", held, err)
	}
	if err := guardStartTakeover(ctx, db, action, "work", "other", held); err != nil {
		t.Fatal(err)
	}
	wrong := *held
	wrong.HeldEpoch++
	if err := guardStartTakeover(ctx, db, action, "work", "other", &wrong); err == nil {
		t.Fatal("accepted changed epoch")
	}
	wrong = *held
	wrong.HolderActorRef = "other-holder"
	if err := guardStartTakeover(ctx, db, action, "work", "other", &wrong); err == nil {
		t.Fatal("accepted changed holder")
	}
	if got, err := workflowStartTakeover(ctx, db, action, "work", "holder"); err != nil || got != nil {
		t.Fatalf("holder takeover = %v, %v", got, err)
	}
	checkStopped := func(session string, want bool) {
		t.Helper()
		got, err := workExecutionStoppedTx(ctx, db, "work", session)
		if err != nil || got != want {
			t.Fatalf("stopped(%q) = %v, %v; want %v", session, got, err, want)
		}
	}
	checkStopped("session-a", false)
	checkStopped("session-b", true)
	plan.Steps[0].Attempts = []WorkflowPlanAttempt{{Note: "done", CompletedAt: "2026-01-01T00:00:00Z", CompletionSeq: 11}}
	writePlan()
	checkStopped("session-a", true)
	plan.Steps[0].Attempts[0].CompletionSeq = 9
	writePlan()
	checkStopped("session-a", false)
	plan.Steps[0].Attempts[0].CompletionSeq = 10
	writePlan()
	checkStopped("session-a", true)
	plan.Steps[0].Attempts[0].CompletionSeq = 11
	writePlan()
	if got, err := workflowStartTakeover(ctx, db, action, "work", "other"); err != nil || got != nil {
		t.Fatalf("completed takeover = %v, %v", got, err)
	}
	if err := guardStartTakeover(ctx, db, action, "work", "other", held); err == nil {
		t.Fatal("accepted stale takeover")
	}
	start(plan.Steps[1].ID, 12, "holder")
	held, err = workflowStartTakeover(ctx, db, action, "work", "other")
	if err != nil || held == nil {
		t.Fatalf("next frontier takeover = %v, %v", held, err)
	}
	checkStopped("session-a", false)
	if _, err := db.Exec(`UPDATE workflow_step_epochs SET latest_start_payload=json_set(latest_start_payload,'$.actor_ref','unknown') WHERE latest_start_seq=12`); err != nil {
		t.Fatal(err)
	}
	checkStopped("session-b", false)
	plan.Steps[0].Attempts[0].CompletionSeq = 13
	writePlan()
	if got, err := workflowStartTakeover(ctx, db, action, "work", "other"); err != nil || got != nil {
		t.Fatalf("later completion on another step = %v, %v", got, err)
	}
}

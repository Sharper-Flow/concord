package store

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestReadWorkPinUsesOneTransactionAndDeclaredStepActions(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	_, version := continuityTestWorkflow(t, s, "workpin-reader")

	pin, err := ReadWorkPin(context.Background(), s, "workpin-reader")
	if err != nil {
		t.Fatal(err)
	}
	if pin.WorkID != "workpin-reader" || pin.Title == "" || pin.LinearIssueKey != "" || pin.ProjectID != "project" || pin.ProjectDisplayName != "Core" || pin.Version != version || pin.WorkflowType != string(WorkflowStepPlanChange) || pin.Step != "plan" {
		t.Fatalf("pin=%+v, want work, title, project, no unconfirmed key, version, and step", pin)
	}
	if !strings.HasPrefix(pin.Watermark, "seq:") {
		t.Fatalf("watermark=%q, want sequence watermark", pin.Watermark)
	}
	if len(pin.DrivingSessions) != 1 || pin.DrivingSessions[0].SessionRef != "session:continuity" || pin.DrivingSessions[0].LastActionID != "workflow.actor_recorded" || pin.DrivingSessions[0].LastActedAt != "2026-08-11T00:00:00Z" {
		t.Fatalf("driving sessions=%v, want only the coordinator session", pin.DrivingSessions)
	}
	gotActions := make([]string, 0, len(pin.NextValidIntents))
	for _, intent := range pin.NextValidIntents {
		gotActions = append(gotActions, intent.ActionID)
		if intent.Tool != "concord_work_transition" || intent.Operation != "workflow_action" || intent.ExpectedVersion != pin.Version {
			t.Fatalf("intent=%+v, want workflow action at pin version", intent)
		}
	}
	wantActions := append([]string{"insert_step", "pick_type"}, WorkflowStepPlanTemplateActions(WorkflowStepPlanChange, "plan")...)
	if !reflect.DeepEqual(gotActions, wantActions) {
		t.Fatalf("intent actions=%v, want %v", gotActions, wantActions)
	}

	var transactionPin WorkPin
	var available bool
	if err := s.Transact(context.Background(), func(tx *Transaction) error {
		var err error
		transactionPin, available, err = ReadMutationWorkPinTx(context.Background(), tx, "workpin-reader", nil)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if !available || !reflect.DeepEqual(transactionPin, pin) {
		t.Fatalf("transaction pin=%+v available=%t, read pin=%+v", transactionPin, available, pin)
	}
}

func TestReadWorkPinIncludesRecordedLinearIssueKeyInTheSameTransaction(t *testing.T) {
	s := openTemp(t)
	continuityTestWorkflow(t, s, "workpin-linear")
	if err := s.Transact(context.Background(), func(tr *Transaction) error {
		_, err := RecordLinearIssueLinkTx(context.Background(), tr, LinearIssueLink{WorkID: "workpin-linear", RemoteIssueUUID: "remote-1", HumanKey: "CON-42", URL: "https://linear.app/example/issue/CON-42"})
		return err
	}); err != nil {
		t.Fatalf("RecordLinearIssueLink error = %v", err)
	}

	pin, err := ReadWorkPin(context.Background(), s, "workpin-linear")
	if err != nil {
		t.Fatal(err)
	}
	if pin.Title == "" || pin.LinearIssueKey != "CON-42" {
		t.Fatalf("pin=%+v, want title and recorded Linear key", pin)
	}
}

func TestReadWorkPinAllowsWorkWithoutPrimaryProject(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	ctx := context.Background()
	workID := "workpin-no-primary"
	seedWorkWithUrgency(t, s, workID, "standard", 10)
	actor := WorkflowActor{PrincipalRef: "principal:workpin", ClientRef: "client:workpin", AgentRef: "agent:workpin", SessionRef: "session:workpin", ActorClass: ActorAgent}
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := initializeWorkflowRawTx(ctx, tx, WorkflowInitializationRequest{WorkID: workID, Actor: actor, Now: time.Date(2026, 8, 11, 0, 0, 0, 0, time.UTC)}); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	seedWorkflowPlanFrontier(t, s, workID, WorkflowStepPlanChange, "plan")

	pin, err := ReadWorkPin(ctx, s, workID)
	if err != nil {
		t.Fatal(err)
	}
	if pin.WorkID != workID || pin.ProjectID != "" || pin.ProjectDisplayName != "" || pin.CancelledInstanceCloses != 0 {
		t.Fatalf("pin=%+v, want an empty project identity", pin)
	}
}

func TestReadWorkPinCountsCancelledInstanceLifecycleCloses(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	ctx := context.Background()
	cases := []struct {
		id        string
		lifecycle string
		instance  string
		project   string
	}{
		{id: "pin-count-match", lifecycle: "completed", instance: "cancelled", project: "project"},
		{id: "pin-count-completed", lifecycle: "completed", instance: "completed", project: "project"},
		{id: "pin-count-cancelled", lifecycle: "cancelled", instance: "cancelled", project: "project"},
		{id: "pin-count-running", lifecycle: "needed", instance: "running", project: "project"},
		{id: "pin-count-other", lifecycle: "completed", instance: "cancelled", project: "project-secondary"},
	}
	for i, item := range cases {
		continuityTestWorkflow(t, s, item.id)
		if i == 0 {
			if err := ApplyOperation(ctx, s, Operation{Events: []Event{
				projectCreatedEvent("project-secondary", "create-project-secondary"),
				operationEvent("product-project-secondary", "product_project.added", SubjectProduct, "product", map[string]any{"product_id": "product", "project_id": "project-secondary", "role": "secondary", "reason": "test", "expected_version": 2, "resulting_version": 3}),
			}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProject, "project-secondary"): 0, VersionRef(SubjectProduct, "product"): 2}}); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := s.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1)`); err != nil {
			t.Fatal(err)
		}
		if _, err := s.DatabaseForTesting().Exec(`UPDATE work_items SET lifecycle=? WHERE id=?`, item.lifecycle, item.id); err != nil {
			t.Fatalf("set lifecycle for %s: %v", item.id, err)
		}
		if _, err := s.DatabaseForTesting().Exec(`UPDATE workflow_instances SET instance_state=? WHERE work_id=?`, item.instance, item.id); err != nil {
			t.Fatalf("set instance for %s: %v", item.id, err)
		}
		if _, err := s.DatabaseForTesting().Exec(`DELETE FROM work_projects WHERE work_id=?`, item.id); err != nil {
			t.Fatal(err)
		}
		if _, err := s.DatabaseForTesting().Exec(`INSERT INTO work_projects(work_id,project_id,role) VALUES(?,?,'primary')`, item.id, item.project); err != nil {
			t.Fatalf("seed %s: %v", item.id, err)
		}
		if _, err := s.DatabaseForTesting().Exec(`DELETE FROM write_guard`); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1); INSERT INTO work_projects(work_id,project_id,role) VALUES('pin-count-other','project','secondary'); DELETE FROM write_guard`); err != nil {
		t.Fatal(err)
	}
	pin, err := ReadWorkPin(ctx, s, "pin-count-completed")
	if err != nil {
		t.Fatal(err)
	}
	if pin.CancelledInstanceCloses != 2 {
		t.Fatalf("cancelled instance closes=%d, want 2", pin.CancelledInstanceCloses)
	}
	other, err := ReadWorkPin(ctx, s, "pin-count-other")
	if err != nil {
		t.Fatal(err)
	}
	if other.ProjectID != "project-secondary" || other.CancelledInstanceCloses != 1 {
		t.Fatalf("other project pin=%s closes=%d, want project-secondary with 1", other.ProjectID, other.CancelledInstanceCloses)
	}
}

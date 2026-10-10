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
	if pin.WorkID != "workpin-reader" || pin.Title == "" || pin.LinearIssueKey != "" || pin.ProjectID != "project" || pin.ProjectDisplayName != "Core" || pin.Version != version || pin.Step != "proposal" {
		t.Fatalf("pin=%+v, want work, title, project, no unconfirmed key, version, and step", pin)
	}
	if !strings.HasPrefix(pin.Watermark, "seq:") {
		t.Fatalf("watermark=%q, want sequence watermark", pin.Watermark)
	}
	if len(pin.DrivingSessions) != 1 || pin.DrivingSessions[0].SessionRef != "session:continuity" || pin.DrivingSessions[0].LastActionID != "record_proposal" || pin.DrivingSessions[0].LastActedAt != "2026-08-07T12:00:00Z" {
		t.Fatalf("driving sessions=%v, want only the coordinator session", pin.DrivingSessions)
	}
	registered, err := BuiltinWorkflowDefinitionForRef(pin.WorkflowType)
	if err != nil {
		t.Fatal(err)
	}
	step := workflowStep(registered.Definition, pin.Step)
	if step == nil {
		t.Fatalf("step %q is not registered", pin.Step)
	}
	gotActions := make([]string, 0, len(pin.NextValidIntents))
	for _, intent := range pin.NextValidIntents {
		gotActions = append(gotActions, intent.ActionID)
		if intent.Tool != "concord_work_transition" || intent.Operation != "workflow_action" || intent.ExpectedVersion != pin.Version {
			t.Fatalf("intent=%+v, want workflow action at pin version", intent)
		}
	}
	if !reflect.DeepEqual(gotActions, step.Actions) {
		t.Fatalf("intent actions=%v, want %v", gotActions, step.Actions)
	}

	var transactionPin WorkPin
	if err := s.Transact(context.Background(), func(tx *Transaction) error {
		var err error
		transactionPin, err = ReadWorkPinTransactionTx(context.Background(), tx, "workpin-reader")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(transactionPin, pin) {
		t.Fatalf("transaction pin=%+v, read pin=%+v", transactionPin, pin)
	}
	listing, err := s.QueryQ3(context.Background(), Q3Request{Product: "product", WorkIDs: []string{"workpin-reader"}, Detail: "full", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(listing.Items) != 1 || listing.Items[0].WorkPin == nil || listing.Items[0].WorkPin.Version != pin.Version {
		t.Fatalf("full listing=%+v, want the same work pin", listing.Items)
	}
}

func TestReadWorkPinIncludesRecordedLinearIssueKeyInTheSameTransaction(t *testing.T) {
	s := openTemp(t)
	continuityTestWorkflow(t, s, "workpin-linear")
	if _, err := s.RecordLinearIssueLink(context.Background(), LinearIssueLink{WorkID: "workpin-linear", RemoteIssueUUID: "remote-1", HumanKey: "CON-42", URL: "https://linear.app/example/issue/CON-42"}); err != nil {
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
	registered, err := BuiltinWorkflowDefinitionForRef("workflow.implementation")
	if err != nil {
		t.Fatal(err)
	}
	actor := WorkflowActor{PrincipalRef: "principal:workpin", ClientRef: "client:workpin", AgentRef: "agent:workpin", SessionRef: "session:workpin", ActorClass: ActorAgent}
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := initializeWorkflowRawTx(ctx, tx, WorkflowInitializationRequest{WorkID: workID, Definition: registered, Actor: actor, Now: time.Date(2026, 8, 11, 0, 0, 0, 0, time.UTC)}); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var version int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM work_items WHERE id=?`, workID).Scan(&version); err != nil {
		t.Fatal(err)
	}
	actorRef, err := WorkflowActorRef(actor)
	if err != nil {
		t.Fatal(err)
	}
	start := workflowEventWithActor("workpin-no-primary-start", WorkflowActionStarted, workID, actorRef, map[string]any{
		"work_id": workID, "expected_version": version, "resulting_version": version + 1,
		"step_id": "proposal", "action_id": "record_proposal", "attempt_epoch": 1,
		"accepted_inputs_digest": "sha256:workpin-no-primary", "idempotency_identity": "workpin-no-primary:start",
		"actor_ref": actorRef, "execution_model": preferredModelForLane(BuiltinLaneDefinitions()[0]),
	})
	if err := applyWorkflowTestOperation(ctx, s, Operation{Events: []Event{start}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}}); err != nil {
		t.Fatal(err)
	}

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
		if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
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
		if _, err := s.DatabaseForTesting().Exec(`DELETE FROM fold_guard`); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1); INSERT INTO work_projects(work_id,project_id,role) VALUES('pin-count-other','project','secondary'); DELETE FROM fold_guard`); err != nil {
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

func TestReadWorkPinIncludesTheCurrentWorkerAttemptEpoch(t *testing.T) {
	t.Parallel()
	s, _, _, attemptID := seedWorkerAtExecution(t, "workpin-attempt")

	pin, err := ReadWorkPin(context.Background(), s, "workpin-attempt")
	if err != nil {
		t.Fatal(err)
	}
	if pin.Attempt == nil || pin.Attempt.ID != attemptID || pin.Attempt.Epoch != 1 || pin.Attempt.Lane == "" || pin.Attempt.State != "dispatched" {
		t.Fatalf("attempt=%+v, want the dispatched worker attempt at epoch 1", pin.Attempt)
	}
	snapshot, err := ReadWorkflowContinuity(context.Background(), s, ContinuityRequest{Work: "workpin-attempt", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.WorkPin == nil {
		t.Fatal("continuity result has no work pin")
	}
	for _, intent := range snapshot.WorkPin.NextValidIntents {
		if intent.ActionID != "dispatch_worker" {
			continue
		}
		if len(intent.RequiredFields) != 1 || intent.RequiredFields[0] != "lane_id" {
			t.Fatalf("continuity dispatch_worker required fields = %v, want [lane_id]", intent.RequiredFields)
		}
		return
	}
	t.Fatal("continuity result has no dispatch_worker intent")
}

func TestReadWorkPinCompletedIncludesVerifiedCriteria(t *testing.T) {
	t.Parallel()
	s, completion := seedCompletionGateCase(t, "workpin-completed", completionGateCase{
		requiredEvidence: []string{"verification", "review"},
		includeSpec:      true,
		includeVerdict:   true,
		includePremise:   true,
		verdictKind:      "ok",
	})
	// Before the workflow completes, the gate withholds the criteria: the
	// projection pairs approved predicates with verdicts only for a
	// completed workflow and a completed item.
	pending, err := ReadWorkPin(context.Background(), s, "workpin-completed")
	if err != nil {
		t.Fatal(err)
	}
	if len(pending.VerifiedCriteria) != 0 {
		t.Fatalf("uncompleted workflow carries verified criteria = %v", pending.VerifiedCriteria)
	}
	if err := CompleteWorkflow(context.Background(), s, completion); err != nil {
		t.Fatal(err)
	}
	pin, err := ReadWorkPin(context.Background(), s, "workpin-completed")
	if err != nil {
		t.Fatal(err)
	}
	if len(pin.VerifiedCriteria) != 1 {
		t.Fatalf("verified criteria=%+v, want one approved predicate", pin.VerifiedCriteria)
	}
	criterion := pin.VerifiedCriteria[0]
	if criterion.PredicateID != "predicate:primary" || criterion.OutcomeKind != "check" || criterion.VerdictKind != "ok" {
		t.Fatalf("verified criterion=%+v, want the approved check and ok verdict", criterion)
	}
}

package store

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

func TestReadWorkPinUsesOneTransactionAndDeclaredStepActions(t *testing.T) {
	s := openTemp(t)
	_, version := continuityTestWorkflow(t, s, "workpin-reader")

	pin, err := ReadWorkPin(context.Background(), s, "workpin-reader")
	if err != nil {
		t.Fatal(err)
	}
	if pin.WorkID != "workpin-reader" || pin.Version != version || pin.Step != "proposal" {
		t.Fatalf("pin=%+v, want work, version, and step", pin)
	}
	if !strings.HasPrefix(pin.Watermark, "seq:") {
		t.Fatalf("watermark=%q, want sequence watermark", pin.Watermark)
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

func TestReadWorkPinIncludesTheCurrentWorkerAttemptEpoch(t *testing.T) {
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

func TestReadWorkPinExposesOutstandingEvidenceRecovery(t *testing.T) {
	ctx := context.Background()
	fixture := newAcceptanceRecoveryFixture(ctx, t, wf04OutstandingKind)

	pin, err := ReadWorkPin(ctx, fixture.store, fixture.workID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(pin.OutstandingEvidenceKinds, []string{wf04OutstandingKind}) {
		t.Fatalf("outstanding evidence kinds=%v, want [%s]", pin.OutstandingEvidenceKinds, wf04OutstandingKind)
	}
	var recovery *WorkPinIntent
	for i := range pin.NextValidIntents {
		if pin.NextValidIntents[i].ActionID == "bind_evidence" {
			recovery = &pin.NextValidIntents[i]
		}
	}
	if recovery == nil {
		t.Fatalf("next valid intents=%v, want recovery bind_evidence", pin.NextValidIntents)
	}
	if recovery.ReasonCode == "declared_step_action" || recovery.ExpectedVersion != pin.Version {
		t.Fatalf("recovery intent=%+v, want distinct reason and pin version", *recovery)
	}

	if err := fixture.bind(ctx, "workpin-recover", pin.Version, wf04OutstandingKind, wf04RecoveryRef); err != nil {
		t.Fatal(err)
	}
	after, err := ReadWorkPin(ctx, fixture.store, fixture.workID)
	if err != nil {
		t.Fatal(err)
	}
	if len(after.OutstandingEvidenceKinds) != 0 {
		t.Fatalf("outstanding evidence kinds after binding=%v, want empty", after.OutstandingEvidenceKinds)
	}
	for _, intent := range after.NextValidIntents {
		if intent.ActionID == "bind_evidence" {
			t.Fatalf("satisfied recovery route remains in intents=%v", after.NextValidIntents)
		}
	}
}

func TestReadWorkPinDoesNotExposeEvidenceRecoveryForTerminalWork(t *testing.T) {
	ctx := context.Background()
	fixture := newAcceptanceRecoveryFixture(ctx, t, wf04OutstandingKind)
	setWorkflowInstanceState(t, fixture.store, fixture.workID, "completed")

	pin, err := ReadWorkPin(ctx, fixture.store, fixture.workID)
	if err != nil {
		t.Fatal(err)
	}
	for _, intent := range pin.NextValidIntents {
		if intent.ActionID == "bind_evidence" && intent.ReasonCode != "declared_step_action" {
			t.Fatalf("terminal pin exposes recovery intent=%+v", intent)
		}
	}
}

func TestWorkPinIntentsDoNotDuplicateDeclaredEvidenceBind(t *testing.T) {
	definition := WorkflowDefinition{
		StepGraph:         WorkflowStepGraph{Steps: []WorkflowStep{{ID: "verify", Actions: []string{"bind_evidence"}}}},
		ActionDefinitions: []WorkflowActionDefinition{currentActionDefinition("bind_evidence", true)},
	}

	intents := workPinIntents(definition, "verify", 9, false)
	if len(intents) != 1 || intents[0].ActionID != "bind_evidence" || intents[0].ReasonCode != "declared_step_action" {
		t.Fatalf("intents=%+v, want one declared bind_evidence intent", intents)
	}
}

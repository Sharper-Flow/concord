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
}

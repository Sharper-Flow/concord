package store

import (
	"context"
	"encoding/json"
	"testing"
)

// footprintBindingActorRef is the agent pinWorkflowForTest records.
func footprintBindingActorRef(t *testing.T, workID string) string {
	t.Helper()
	ref, err := WorkflowActorRef(WorkflowActor{PrincipalRef: "principal/agent", ClientRef: "client/concord", AgentRef: "agent/size", SessionRef: "session/" + workID, ActorClass: ActorAgent})
	if err != nil {
		t.Fatal(err)
	}
	return ref
}

// footprintBindingItem seeds a work item beside a Domain registry, records a
// small size when small, and pins workflow.implementation at the composition
// that size selects.
func footprintBindingItem(t *testing.T, workID string, small bool) (*Store, WorkflowDefinition) {
	t.Helper()
	s := seedLawChangeFixture(t, workID)
	if small {
		recordSmallWorkSizeForTest(t, s, workID)
	}
	registered, err := pinWorkflowForTest(t, s, workID, "workflow.implementation")
	if err != nil {
		t.Fatal(err)
	}
	if want := map[bool]WorkSize{true: WorkSizeSmall, false: ""}[small]; workflowCompositionSize(registered.Definition.Ref, registered.Definition.Version) != want {
		t.Fatalf("%s pinned v%d; want the %q composition", workID, registered.Definition.Version, want)
	}
	return s, registered.Definition
}

func footprintBindingApproval(t *testing.T, s *Store, definition WorkflowDefinition, workID string) ([]Event, error) {
	t.Helper()
	return factWriteConstructInitialContract(t, s, definition, workID, footprintBindingActorRef(t, workID), footprintBindingFields(t, s, 1), readWorkVersion(t, s, workID))
}

// footprintBindingFields is a contract that changes no law at version.
func footprintBindingFields(t *testing.T, s *Store, version int) map[string]json.RawMessage {
	t.Helper()
	fields := lawChangeConstructorFields(t, s, version, []string{}, []string{})
	for _, name := range []string{workflowLawChangeReasonField, workflowLawChangeBehaviorField, workflowLawChangeSecurityField} {
		delete(fields, name)
	}
	return fields
}

func appendFootprintBindingEvents(s *Store, workID string, expected int64, events []Event) error {
	return tryAppendWorkflowEventsWithRegistry(s, BuiltinWorkflowRegistry(), Operation{Events: events, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): expected}})
}

func readFootprintBindingForTest(t *testing.T, s *Store, workID string, contractVersion int64) *workflowFootprintBinding {
	t.Helper()
	binding, err := readWorkflowFootprintBindingTx(context.Background(), s.DatabaseForTesting(), workID, contractVersion)
	if err != nil {
		t.Fatal(err)
	}
	return binding
}

func setFootprintBinding(t *testing.T, event Event, binding any) Event {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if binding == nil {
		delete(payload, "footprint_binding")
	} else {
		payload["footprint_binding"] = binding
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	event.Payload = raw
	return event
}

// rerunSmallSizeForTest records a second model run of a small item; shape
// sets its footprint.
func rerunSmallSizeForTest(t *testing.T, s *Store, workID string, shape func(*WorkSizeFootprint)) string {
	t.Helper()
	event, size := workSizedEventForTest(t, s, workID, 20, func(footprint *WorkSizeFootprint) {
		footprint.ChecksComplete, footprint.ShardsRead = true, true
		shape(footprint)
	})
	if size != WorkSizeSmall {
		t.Fatalf("the rerun decided %s", size)
	}
	event.EventID = workID + "-size-rerun"
	if err := applyWorkEvent(t, s, event, nil); err != nil {
		t.Fatal(err)
	}
	return event.EventID
}

// A small-composition contract records the size assessment whose checks it
// owes, and the fold accepts only the binding the size record derives.
func TestSmallContractBindsItsSizeAssessment(t *testing.T) {
	t.Parallel()

	t.Run("approval and supersession bind the current assessment", func(t *testing.T) {
		t.Parallel()
		const workID = "binding-small"
		s, definition := footprintBindingItem(t, workID, true)
		events, err := footprintBindingApproval(t, s, definition, workID)
		if err != nil {
			t.Fatalf("approval: %v", err)
		}
		if err := appendFootprintBindingEvents(s, workID, readWorkVersion(t, s, workID), events); err != nil {
			t.Fatalf("fold: %v", err)
		}
		want := workflowFootprintBinding{ContractVersion: 1, AssessmentEventID: workID + "-size"}
		if got := readFootprintBindingForTest(t, s, workID, 1); got == nil || *got != want {
			t.Fatalf("contract 1 binding = %+v; want %+v", got, want)
		}

		rerun := rerunSmallSizeForTest(t, s, workID, func(*WorkSizeFootprint) {})
		fields := footprintBindingFields(t, s, 2)
		raw, err := json.Marshal(fields)
		if err != nil {
			t.Fatal(err)
		}
		expected := readWorkVersion(t, s, workID)
		successor, err := factWriteConstructSuccessorContract(t, s, definition, workID, footprintBindingActorRef(t, workID), raw, fields, expected)
		if err != nil {
			t.Fatalf("supersession: %v", err)
		}
		if err := appendFootprintBindingEvents(s, workID, expected, successor); err != nil {
			t.Fatalf("supersession fold: %v", err)
		}
		want = workflowFootprintBinding{ContractVersion: 2, AssessmentEventID: rerun}
		if got := readFootprintBindingForTest(t, s, workID, 2); got == nil || *got != want {
			t.Fatalf("contract 2 binding = %+v; want %+v", got, want)
		}
		if err := RebuildFromLog(context.Background(), s); err != nil {
			t.Fatalf("replay: %v", err)
		}
	})

	t.Run("the fold refuses a missing, foreign, or stale binding", func(t *testing.T) {
		t.Parallel()
		const workID = "binding-tamper"
		s, definition := footprintBindingItem(t, workID, true)
		events, err := footprintBindingApproval(t, s, definition, workID)
		if err != nil {
			t.Fatalf("approval: %v", err)
		}
		expected := readWorkVersion(t, s, workID)
		for label, binding := range map[string]any{
			"missing":       nil,
			"wrong version": workflowFootprintBinding{ContractVersion: 2, AssessmentEventID: workID + "-size"},
			"foreign":       workflowFootprintBinding{ContractVersion: 1, AssessmentEventID: "other-size"},
		} {
			requireFailureKind(t, appendFootprintBindingEvents(s, workID, expected, []Event{setFootprintBinding(t, events[0], binding)}), KindInvalidPayload, label)
		}
		// A rerun after construction makes the constructed binding stale.
		rerunSmallSizeForTest(t, s, workID, func(*WorkSizeFootprint) {})
		requireFailureKind(t, appendFootprintBindingEvents(s, workID, readWorkVersion(t, s, workID), events), KindInvalidPayload, "stale")
		if got := readFootprintBindingForTest(t, s, workID, 1); got != nil {
			t.Fatalf("a refused approval left binding %+v", got)
		}
	})

	t.Run("approval refuses an assessment whose checks no longer execute", func(t *testing.T) {
		t.Parallel()
		const workID = "binding-unexecutable"
		s, definition := footprintBindingItem(t, workID, true)
		rerunSmallSizeForTest(t, s, workID, func(footprint *WorkSizeFootprint) {
			footprint.Laws = []FootprintLaw{{LawID: "CD-0001", Checks: []FootprintCheck{{Kind: "scenario", Value: "scenario:unsupported"}}}}
		})
		_, err := footprintBindingApproval(t, s, definition, workID)
		requireFailureKind(t, err, KindInvalidOperation, "unexecutable checks")
	})

	t.Run("the full composition carries no binding", func(t *testing.T) {
		t.Parallel()
		const workID = "binding-full"
		s, definition := footprintBindingItem(t, workID, false)
		events, err := footprintBindingApproval(t, s, definition, workID)
		if err != nil {
			t.Fatalf("approval: %v", err)
		}
		expected := readWorkVersion(t, s, workID)
		forged := setFootprintBinding(t, events[0], workflowFootprintBinding{ContractVersion: 1, AssessmentEventID: workID + "-size"})
		requireFailureKind(t, appendFootprintBindingEvents(s, workID, expected, []Event{forged}), KindInvalidPayload, "binding on full")
		if err := appendFootprintBindingEvents(s, workID, expected, events); err != nil {
			t.Fatalf("fold: %v", err)
		}
		if got := readFootprintBindingForTest(t, s, workID, 1); got != nil {
			t.Fatalf("full contract binding = %+v; want none", got)
		}
	})
}

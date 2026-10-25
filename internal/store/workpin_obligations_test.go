package store

import (
	"context"
	"reflect"
	"sort"
	"testing"
)

// workPinObligationDeclarationIDs recomputes the root, step, and rigor
// obligation declarations straight from the definition so the test does not
// reuse the pin's own collector helper as its oracle.
func workPinObligationDeclarationIDs(t *testing.T, definition WorkflowDefinition) []string {
	t.Helper()
	want := map[string]struct{}{}
	for _, kind := range definition.RequiredEvidenceKinds {
		want[string(kind)] = struct{}{}
	}
	for _, step := range definition.StepGraph.Steps {
		for _, kind := range step.RequiredEvidenceKinds {
			want[string(kind)] = struct{}{}
		}
	}
	for _, rule := range definition.RigorRules {
		for _, kind := range rule.RequiredEvidenceKinds {
			want[string(kind)] = struct{}{}
		}
	}
	ids := make([]string, 0, len(want))
	for id := range want {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func workPinDeclaredObligationsAndIdentity(t *testing.T, s *Store, workID string, registered RegisteredDefinition) {
	t.Helper()
	pin, err := ReadWorkPin(context.Background(), s, workID)
	if err != nil {
		t.Fatal(err)
	}
	want := workPinObligationDeclarationIDs(t, registered.Definition)
	if pin.Obligations == nil || !reflect.DeepEqual(*pin.Obligations, want) {
		t.Fatalf("pin obligations=%v, want the sorted root/step/rigor declarations %v", pin.Obligations, want)
	}
	if !sort.StringsAreSorted(*pin.Obligations) {
		t.Fatalf("pin obligations=%v are not sorted", *pin.Obligations)
	}
	if pin.WorkflowDefinitionDigest == nil || *pin.WorkflowDefinitionDigest != registered.Digest {
		t.Fatalf("pin definition digest=%v, want the registry digest %q", pin.WorkflowDefinitionDigest, registered.Digest)
	}
	if err := BuiltinWorkflowRegistry().Verify(registered.Definition.Ref, *pin.WorkflowDefinitionDigest); err != nil {
		t.Fatalf("pin definition digest does not verify: %v", err)
	}
	// The continuity envelope — the pinned projection every session boot
	// and concord_work_trace.continuity read renders — must carry the same
	// obligation list and definition identity the pin read carries, because
	// both derive from the one ReadWorkPinTx path inside the same read
	// transaction the continuity fold owns.
	snapshot, err := ReadWorkflowContinuity(context.Background(), s, ContinuityRequest{Work: workID})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.WorkPin == nil {
		t.Fatal("the continuity envelope carries no work pin")
	}
	if !reflect.DeepEqual(snapshot.WorkPin.Obligations, pin.Obligations) {
		t.Fatalf("continuity envelope obligations=%v, pin obligations=%v", snapshot.WorkPin.Obligations, pin.Obligations)
	}
	if snapshot.WorkPin.WorkflowType != pin.WorkflowType || snapshot.WorkPin.WorkflowDefinitionDigest == nil || *snapshot.WorkPin.WorkflowDefinitionDigest != *pin.WorkflowDefinitionDigest {
		t.Fatalf("continuity envelope definition identity=(%s, %v), pin identity=(%s, %v)",
			snapshot.WorkPin.WorkflowType, snapshot.WorkPin.WorkflowDefinitionDigest,
			pin.WorkflowType, pin.WorkflowDefinitionDigest)
	}
}

func TestWorkPinPublishesDeclaredObligationsAndDefinitionIdentity(t *testing.T) {
	s := openTemp(t)
	continuityTestWorkflow(t, s, "workpin-obligations")
	registered, err := BuiltinWorkflowDefinitionForRef("workflow.implementation")
	if err != nil {
		t.Fatal(err)
	}
	workPinDeclaredObligationsAndIdentity(t, s, "workpin-obligations", registered)
}

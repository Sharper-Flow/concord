package store

import (
	"context"
	"reflect"
	"sort"
	"testing"
	"time"
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

func workPinDeclaredObligationsAndIdentity(t *testing.T, s *Store, workID string, registered RegisteredDefinition, version int64) {
	t.Helper()
	pin, err := ReadWorkPin(context.Background(), s, workID)
	if err != nil {
		t.Fatal(err)
	}
	want := workPinObligationDeclarationIDs(t, registered.Definition)
	if !reflect.DeepEqual(pin.Obligations, want) {
		t.Fatalf("pin obligations=%v, want the sorted root/step/rigor declarations %v", pin.Obligations, want)
	}
	if !sort.StringsAreSorted(pin.Obligations) {
		t.Fatalf("pin obligations=%v are not sorted", pin.Obligations)
	}
	if pin.WorkflowDefinitionVersion != version {
		t.Fatalf("pin definition version=%d, want the pinned instance version %d", pin.WorkflowDefinitionVersion, version)
	}
	if pin.WorkflowDefinitionDigest != registered.Digest {
		t.Fatalf("pin definition digest=%q, want the registry digest %q", pin.WorkflowDefinitionDigest, registered.Digest)
	}
	if err := BuiltinWorkflowRegistry().Verify(registered.Definition.Ref, version, pin.WorkflowDefinitionDigest); err != nil {
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
	if snapshot.WorkPin.WorkflowDefinitionVersion != pin.WorkflowDefinitionVersion || snapshot.WorkPin.WorkflowDefinitionDigest != pin.WorkflowDefinitionDigest {
		t.Fatalf("continuity envelope definition identity=(%d, %s), pin identity=(%d, %s)",
			snapshot.WorkPin.WorkflowDefinitionVersion, snapshot.WorkPin.WorkflowDefinitionDigest,
			pin.WorkflowDefinitionVersion, pin.WorkflowDefinitionDigest)
	}
}

func TestWorkPinPublishesDeclaredObligationsAndDefinitionIdentity(t *testing.T) {
	s := openTemp(t)
	continuityTestWorkflow(t, s, "workpin-obligations")
	var pinned int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT definition_version FROM workflow_instances WHERE work_id=?`, "workpin-obligations").Scan(&pinned); err != nil {
		t.Fatal(err)
	}
	registered, err := BuiltinWorkflowDefinitionForRef("workflow.implementation")
	if err != nil {
		t.Fatal(err)
	}
	workPinDeclaredObligationsAndIdentity(t, s, "workpin-obligations", registered, pinned)
}

func TestWorkPinPublishesDeclaredObligationsForHistoricalDefinition(t *testing.T) {
	registry := BuiltinWorkflowRegistry()
	// The current family version comes from the current definition set, not
	// from Lookup(ref, version) — that returns the requested historical
	// definition itself, so comparing against it always succeeds and the
	// fixture silently skips. A real historical pin is a version strictly
	// below the current family version for the same ref.
	currentVersions := map[string]int64{}
	for _, current := range BuiltinWorkflowDefinitions() {
		currentVersions[current.Ref] = current.Version
	}
	historical := WorkflowDefinition{}
	found := false
	for _, definition := range BuiltinWorkflowDefinitionsWithHistory() {
		current, ok := currentVersions[definition.Ref]
		if !ok || definition.Version >= current {
			continue
		}
		historical = definition
		found = true
		break
	}
	if !found {
		t.Skip("no registered workflow definition family carries a historical version")
	}
	registered, ok := registry.Lookup(historical.Ref, historical.Version)
	if !ok {
		t.Fatalf("historical definition %s version %d is not registered", historical.Ref, historical.Version)
	}
	// The historical pin is written through the same event-fold route a real
	// definition change takes — a WorkflowDefinitionSelected event applied by
	// the fold, replayed the way
	// TestReplayAdmitsRecordedDefinitionChangeBetweenContractApprovals
	// replays one — never a manual projection UPDATE. The instance starts on
	// the family's current definition with no contract and no started
	// action, which is exactly the state the definition-change fold admits.
	s := openTemp(t)
	const workID = "workpin-historical"
	seedWork(t, s, workID)
	current, err := BuiltinWorkflowDefinitionForRef(historical.Ref)
	if err != nil {
		t.Fatal(err)
	}
	actor := WorkflowActor{PrincipalRef: "principal:workpin-historical", ClientRef: "client:workpin-historical", AgentRef: "agent:workpin-historical", SessionRef: "session:workpin-historical", ActorClass: ActorAgent}
	actorRef, err := WorkflowActorRef(actor)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Transact(context.Background(), func(tx *Transaction) error {
		return initializeWorkflowRawTx(context.Background(), tx.tx, WorkflowInitializationRequest{WorkID: workID, Definition: current, Actor: actor, Now: time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)})
	}); err != nil {
		t.Fatal(err)
	}
	var version int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM work_items WHERE id=?`, workID).Scan(&version); err != nil {
		t.Fatal(err)
	}
	selected := workflowEventWithActor("workpin-historical-definition", WorkflowDefinitionSelected, workID, actorRef, map[string]any{
		"work_id": workID, "expected_version": version, "resulting_version": version + 1,
		"ref": historical.Ref, "version": historical.Version, "digest": registered.Digest, "work_kind": historical.WorkKind,
	})
	selected.PayloadVersion = 1
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{selected}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}}); err != nil {
		t.Fatalf("fold refused the recorded definition change: %v", err)
	}
	workPinDeclaredObligationsAndIdentity(t, s, workID, registered, historical.Version)
}

package store

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

// recordWorkSizeForTest records a complete size record whose model size is
// the one the inputs decide, with the footprint's checks as given.
func recordWorkSizeForTest(t *testing.T, s *Store, workID string, lines int, checks []FootprintCheck, sharded []string) WorkSize {
	t.Helper()
	return recordWorkSizeFootprintForTest(t, s, workID, lines, func(footprint *WorkSizeFootprint) {
		footprint.ChecksComplete, footprint.ShardsRead = true, true
		footprint.Laws = []FootprintLaw{{LawID: "CD-0001", Checks: checks}}
		footprint.ShardedPackages = sharded
	})
}

func recordWorkSizeFootprintForTest(t *testing.T, s *Store, workID string, lines int, shape func(*WorkSizeFootprint)) WorkSize {
	t.Helper()
	event, size := workSizedEventForTest(t, s, workID, lines, shape)
	if err := applyWorkEvent(t, s, event, nil); err != nil {
		t.Fatal(err)
	}
	return size
}

// workSizedEventForTest builds the current-version size record of a seeded
// work item whose footprint shape gives, sized by the model.
func workSizedEventForTest(t *testing.T, s *Store, workID string, lines int, shape func(*WorkSizeFootprint)) (Event, WorkSize) {
	t.Helper()
	inputs := workSizeInputsFixture()
	inputs.Declared.EstimatedProductionLines = lines
	if err := s.DatabaseForTesting().QueryRow(`SELECT kind FROM work_items WHERE id=?`, workID).Scan(&inputs.Kind); err != nil {
		t.Fatal(err)
	}
	size, err := DecideWorkSize(WorkSizeModelVersion, inputs, nil)
	if err != nil {
		t.Fatal(err)
	}
	footprint := workSizeFootprintFixture()
	shape(&footprint)
	event := workSizedEventWith(workID+"-size", workID, WorkSizeModelVersion, size, inputs, footprint)
	event.PayloadVersion = workSizedPayloadVersion
	return event, size
}

// recordSmallWorkSizeForTest records a small size whose checks all execute,
// the record a small composition pin rests on.
func recordSmallWorkSizeForTest(t *testing.T, s *Store, workID string) {
	t.Helper()
	if size := recordWorkSizeForTest(t, s, workID, 20, nil, nil); size != WorkSizeSmall {
		t.Fatalf("the small size fixture decided %s", size)
	}
}

func pinWorkflowForTest(t *testing.T, s *Store, workID, ref string) (RegisteredDefinition, error) {
	t.Helper()
	ctx := context.Background()
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	definition, err := selectWorkflowDefinitionTx(ctx, tx, ref, workID)
	if err != nil {
		return RegisteredDefinition{}, err
	}
	actor := WorkflowActor{PrincipalRef: "principal/agent", ClientRef: "client/concord", AgentRef: "agent/size", SessionRef: "session/" + workID, ActorClass: ActorAgent}
	if err := initializeWorkflowRawTx(ctx, tx, WorkflowInitializationRequest{WorkID: workID, Definition: definition, Actor: actor, Now: time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)}); err != nil {
		return RegisteredDefinition{}, err
	}
	return definition, tx.Commit()
}

func recordedSizeSelection(t *testing.T, s *Store, workID string) workflowSizeSelection {
	t.Helper()
	var raw string
	if err := s.DatabaseForTesting().QueryRow(`SELECT json_extract(payload,'$.size_selection') FROM domain_events WHERE subject_id=? AND kind=? ORDER BY seq DESC LIMIT 1`, workID, WorkflowDefinitionSelected).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var selection workflowSizeSelection
	if err := json.Unmarshal([]byte(raw), &selection); err != nil {
		t.Fatal(err)
	}
	return selection
}

// CD-0229 D5: small work pins the small composition only when every check
// it owes has an executing suite; otherwise it keeps the full composition,
// keeps its small size, and the pin names each check without a suite.
func TestWorkflowSelectionPinsTheCompositionTheRecordedSizeSelects(t *testing.T) {
	t.Parallel()
	const ref = "workflow.implementation"
	head, err := BuiltinWorkflowDefinitionForRef(ref)
	if err != nil {
		t.Fatal(err)
	}
	small, err := builtinWorkflowDefinitionForSize(ref, WorkSizeSmall)
	if err != nil {
		t.Fatal(err)
	}
	executable := []FootprintCheck{{Kind: "go_test", Value: "internal/store.TestBody"}, {Kind: "go_package", Value: "./internal/agent"}, {Kind: "validator", Value: "scripts/check-alpha.py"}}
	unexecutable := []FootprintCheck{{Kind: "adapter_test", Value: "adapter/opencode/a.test.ts"}, {Kind: "go_package", Value: "./internal/store"}, {Kind: "scenario", Value: ".concord/scenarios/a.json"}, {Kind: "unknown_kind", Value: "x"}}
	cases := []struct {
		name      string
		lines     int
		checks    []FootprintCheck
		sharded   []string
		want      int64
		size      WorkSize
		retention *workflowFullRetention
	}{
		{name: "small-executable", lines: 20, checks: executable, want: small.Definition.Version, size: WorkSizeSmall},
		{name: "small-unexecutable", lines: 20, checks: append(slices.Clone(executable), unexecutable...), sharded: []string{"internal/store"}, want: head.Definition.Version, size: WorkSizeSmall,
			retention: &workflowFullRetention{Reason: WorkflowRetainedUnexecutableChecks, Checks: []FootprintCheck{unexecutable[0], unexecutable[1], unexecutable[2], unexecutable[3]}}},
		{name: "larger", lines: 400, checks: executable, want: head.Definition.Version, size: WorkSizeLarger},
		{name: "crucial", lines: 500, checks: executable, want: head.Definition.Version, size: WorkSizeCrucial},
		{name: "unsized", want: head.Definition.Version},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			s := openTemp(t)
			workID := "select-" + testCase.name
			seedWork(t, s, workID)
			if testCase.lines != 0 {
				if size := recordWorkSizeForTest(t, s, workID, testCase.lines, testCase.checks, testCase.sharded); size != testCase.size {
					t.Fatalf("fixture decided %s, want %s", size, testCase.size)
				}
			}
			pinned, err := pinWorkflowForTest(t, s, workID, ref)
			if err != nil {
				t.Fatal(err)
			}
			if pinned.Definition.Version != testCase.want {
				t.Fatalf("pinned v%d, want v%d", pinned.Definition.Version, testCase.want)
			}
			got := recordedSizeSelection(t, s, workID)
			if got.Size != testCase.size || (got.Retention == nil) != (testCase.retention == nil) {
				t.Fatalf("selection = %+v, want size %q retention %+v", got, testCase.size, testCase.retention)
			}
			if testCase.retention != nil {
				slices.SortFunc(testCase.retention.Checks, func(a, b FootprintCheck) int {
					return cmp.Or(strings.Compare(a.Kind, b.Kind), strings.Compare(a.Value, b.Value))
				})
				if got.Retention.Reason != testCase.retention.Reason || !slices.Equal(got.Retention.Checks, testCase.retention.Checks) {
					t.Fatalf("retention = %+v, want %+v", got.Retention, testCase.retention)
				}
			}
			if decision, _, err := readEffectiveWorkSize(context.Background(), s.DatabaseForTesting(), workID); err != nil || decision.Effective != testCase.size && testCase.lines != 0 {
				t.Fatalf("the selection changed the size: %+v, %v", decision, err)
			}
		})
	}
	// A record written before the shard read proves no go_package check
	// runs unsharded; a resize copies it unchanged.
	t.Run("small-shards-unread", func(t *testing.T) {
		t.Parallel()
		s := openTemp(t)
		seedWork(t, s, "select-unread")
		recordWorkSizeFootprintForTest(t, s, "select-unread", 20, func(footprint *WorkSizeFootprint) {
			footprint.ChecksComplete = true
			footprint.Laws = []FootprintLaw{{LawID: "CD-0001", Checks: executable}}
		})
		pinned, err := pinWorkflowForTest(t, s, "select-unread", ref)
		if err != nil || pinned.Definition.Version != head.Definition.Version {
			t.Fatalf("an unread shard declaration pinned v%d, %v; want the full head", pinned.Definition.Version, err)
		}
		want := &workflowFullRetention{Reason: WorkflowRetainedUnexecutableChecks, Checks: []FootprintCheck{{Kind: "go_package", Value: "./internal/agent"}}}
		if got := recordedSizeSelection(t, s, "select-unread"); got.Size != WorkSizeSmall || !reflect.DeepEqual(got.Retention, want) {
			t.Fatalf("unread selection = %+v, want retention %+v", got, want)
		}
	})
	t.Run("small-incomplete-inventory", func(t *testing.T) {
		t.Parallel()
		s := openTemp(t)
		seedWork(t, s, "select-incomplete")
		recordWorkSizeFootprintForTest(t, s, "select-incomplete", 20, func(*WorkSizeFootprint) {})
		pinned, err := pinWorkflowForTest(t, s, "select-incomplete", ref)
		if err != nil || pinned.Definition.Version != head.Definition.Version {
			t.Fatalf("an incomplete inventory pinned v%d, %v; want the full head", pinned.Definition.Version, err)
		}
		if got := recordedSizeSelection(t, s, "select-incomplete"); got.Size != WorkSizeSmall || got.Retention == nil || got.Retention.Reason != WorkflowRetainedIncompleteInventory || got.Retention.Checks != nil {
			t.Fatalf("incomplete selection = %+v", got)
		}
	})
	for _, family := range []string{"workflow.break_fix", "workflow.research"} {
		t.Run(family, func(t *testing.T) {
			t.Parallel()
			s := openTemp(t)
			workID := "select-" + family
			seedWork(t, s, workID)
			recordSmallWorkSizeForTest(t, s, workID)
			familyHead, err := BuiltinWorkflowDefinitionForRef(family)
			if err != nil {
				t.Fatal(err)
			}
			pinned, err := pinWorkflowForTest(t, s, workID, family)
			if err != nil || pinned.Definition.Version != familyHead.Definition.Version {
				t.Fatalf("%s small pinned v%d, %v; want its head v%d", family, pinned.Definition.Version, err, familyHead.Definition.Version)
			}
			if got := recordedSizeSelection(t, s, workID); got.Size != WorkSizeSmall || got.Retention != nil {
				t.Fatalf("%s selection = %+v", family, got)
			}
		})
	}
}

// The fold re-derives the pin: a small composition without a recorded small
// size, a full pin for small work without a retention, and a retention that
// names a check outside the footprint all refuse.
func TestWorkflowPinFoldRefusesASelectionTheRecordedSizeDoesNotDerive(t *testing.T) {
	t.Parallel()
	small, err := builtinWorkflowDefinitionForSize("workflow.implementation", WorkSizeSmall)
	if err != nil {
		t.Fatal(err)
	}
	head, err := BuiltinWorkflowDefinitionForRef("workflow.implementation")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name       string
		sizeChecks []FootprintCheck
		lines      int
		definition RegisteredDefinition
		selection  *workflowSizeSelection
	}{
		{name: "small-pin-unsized", definition: small, selection: &workflowSizeSelection{}},
		{name: "small-pin-legacy-payload", lines: 20, definition: small},
		{name: "small-pin-larger", lines: 400, definition: small, selection: &workflowSizeSelection{Size: WorkSizeLarger}},
		{name: "full-pin-small-without-retention", lines: 20, definition: head, selection: &workflowSizeSelection{Size: WorkSizeSmall}},
		{name: "retention-outside-footprint", lines: 20, sizeChecks: []FootprintCheck{{Kind: "scenario", Value: "a"}}, definition: head,
			selection: &workflowSizeSelection{Size: WorkSizeSmall, Retention: &workflowFullRetention{Reason: WorkflowRetainedUnexecutableChecks, Checks: []FootprintCheck{{Kind: "scenario", Value: "b"}}}}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			s := openTemp(t)
			workID := "fold-" + testCase.name
			seedWork(t, s, workID)
			if testCase.lines != 0 {
				recordWorkSizeForTest(t, s, workID, testCase.lines, testCase.sizeChecks, nil)
			}
			err := applyPinForTest(t, s, workID, testCase.definition, testCase.selection)
			var failure *Failure
			if !errors.As(err, &failure) {
				t.Fatalf("the fold admitted %s: %v", testCase.name, err)
			}
			if count := countWorkflowInstances(t, s, workID); count != 0 {
				t.Fatalf("a refused pin left %d instances", count)
			}
		})
	}
	t.Run("derived", func(t *testing.T) {
		t.Parallel()
		s := openTemp(t)
		seedWork(t, s, "fold-derived")
		recordSmallWorkSizeForTest(t, s, "fold-derived")
		if err := applyPinForTest(t, s, "fold-derived", small, &workflowSizeSelection{Size: WorkSizeSmall}); err != nil {
			t.Fatalf("the fold refused the derived selection: %v", err)
		}
		if count := countWorkflowInstances(t, s, "fold-derived"); count != 1 {
			t.Fatalf("the derived pin left %d instances", count)
		}
	})
}

// applyPinForTest appends the actor and definition_selected events a pin
// writes, carrying the given size selection instead of the derived one. A
// nil selection writes payload version 1, the pin an earlier binary wrote.
func applyPinForTest(t *testing.T, s *Store, workID string, definition RegisteredDefinition, selection *workflowSizeSelection) error {
	t.Helper()
	actor := WorkflowActor{PrincipalRef: "principal/agent", ClientRef: "client/concord", AgentRef: "agent/size", SessionRef: "session/" + workID, ActorClass: ActorAgent}
	return applyPinAsForTest(t, s, workID, definition, actor, selection)
}

func applyPinAsForTest(t *testing.T, s *Store, workID string, definition RegisteredDefinition, actor WorkflowActor, selection *workflowSizeSelection) error {
	t.Helper()
	ctx := context.Background()
	actorRef, err := WorkflowActorRef(actor)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	var version int64
	if err := tx.QueryRowContext(ctx, `SELECT version FROM work_items WHERE id=?`, workID).Scan(&version); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)
	actorPayload, _ := json.Marshal(map[string]any{
		"work_id": workID, "expected_version": version, "resulting_version": version + 1,
		"actor_ref": actorRef, "principal_ref": actor.PrincipalRef, "client_ref": actor.ClientRef,
		"agent_ref": actor.AgentRef, "session_ref": actor.SessionRef, "actor_class": actor.ActorClass,
	})
	fields := map[string]any{
		"work_id": workID, "expected_version": version + 1, "resulting_version": version + 2,
		"ref": definition.Definition.Ref, "version": definition.Definition.Version,
		"digest": definition.Digest, "work_kind": definition.Definition.WorkKind,
	}
	pinVersion := 1
	if selection != nil {
		fields["size_selection"], pinVersion = selection, workflowDefinitionSelectedPayloadVersion
	}
	definitionPayload, _ := json.Marshal(fields)
	scope := newFoldScope(tx)
	if err := scope.bindRegistry(tx, BuiltinWorkflowRegistry()); err != nil {
		t.Fatal(err)
	}
	if _, err := applyWorkflowOperationTx(ctx, tx, Operation{
		Events: []Event{
			{EventID: workID + "-actor", Kind: WorkflowActorRecorded, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: actorRef, OccurredAt: now, PayloadVersion: 1, Payload: actorPayload},
			{EventID: workID + "-pin", Kind: WorkflowDefinitionSelected, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: actorRef, OccurredAt: now, PayloadVersion: pinVersion, Payload: definitionPayload},
		},
		ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version},
	}, scope); err != nil {
		return err
	}
	return tx.Commit()
}

func countWorkflowInstances(t *testing.T, s *Store, workID string) int {
	t.Helper()
	var count int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM workflow_instances WHERE work_id=?`, workID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

// Carry-forward stays in the pinned composition, in both directions: a full
// pin first sized small keeps full with the existing-instance reason, and a
// small pin never carries onto full.
func TestWorkflowCarryForwardStaysInThePinnedComposition(t *testing.T) {
	t.Parallel()
	const ref = "workflow.implementation"
	ctx := context.Background()
	head, err := BuiltinWorkflowDefinitionForRef(ref)
	if err != nil {
		t.Fatal(err)
	}
	small, err := builtinWorkflowDefinitionForSize(ref, WorkSizeSmall)
	if err != nil {
		t.Fatal(err)
	}
	released, ok := BuiltinWorkflowRegistry().Lookup(ref, small.Definition.Version-1)
	if !ok || workflowCompositionSize(ref, released.Definition.Version) != "" {
		t.Fatalf("v%d is not a released full composition", small.Definition.Version-1)
	}
	actor := WorkflowActor{PrincipalRef: "principal/agent", ClientRef: "client/concord", AgentRef: "agent/size", SessionRef: "session/carry", ActorClass: ActorAgent}
	now := time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)
	repin := func(s *Store, workID string, definition RegisteredDefinition) error {
		tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback() }()
		if err := repinWorkflowRawTx(ctx, tx, WorkflowRepinRequest{WorkID: workID, EventID: workID + "-repin-" + definition.Digest[7:15], Definition: definition, Actor: actor, Now: now}); err != nil {
			return err
		}
		return tx.Commit()
	}

	// A released full pin, sized small afterwards, carries forward onto full.
	s := openTemp(t)
	seedWork(t, s, "carry-full")
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := initializeWorkflowRawTx(ctx, tx, WorkflowInitializationRequest{WorkID: "carry-full", Definition: released, Actor: actor, Now: now}); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	recordSmallWorkSizeForTest(t, s, "carry-full")
	selected, err := selectWorkflowDefinitionTx(ctx, s.DatabaseForTesting(), ref, "carry-full")
	if err != nil || selected.Definition.Version != head.Definition.Version {
		t.Fatalf("a full pin sized small selected v%d, %v; want the full head", selected.Definition.Version, err)
	}
	if err := repin(s, "carry-full", small); err == nil {
		t.Fatal("a full pin carried forward onto the small composition")
	}
	if err := repin(s, "carry-full", head); err != nil {
		t.Fatal(err)
	}
	if got := recordedSizeSelection(t, s, "carry-full"); got.Size != WorkSizeSmall || got.Retention == nil || got.Retention.Reason != WorkflowRetainedExistingFullInstance {
		t.Fatalf("full carry-forward selection = %+v", got)
	}

	// A full head pin sized small never moves down onto the small composition.
	s = openTemp(t)
	seedWork(t, s, "carry-head")
	if _, err := pinWorkflowForTest(t, s, "carry-head", ref); err != nil {
		t.Fatal(err)
	}
	recordSmallWorkSizeForTest(t, s, "carry-head")
	if err := repin(s, "carry-head", small); err == nil {
		t.Fatal("a full head pin moved down onto the small composition")
	}

	// A small pin never carries onto the full head.
	s = openTemp(t)
	seedWork(t, s, "carry-small")
	recordSmallWorkSizeForTest(t, s, "carry-small")
	if pinned, err := pinWorkflowForTest(t, s, "carry-small", ref); err != nil || pinned.Definition.Version != small.Definition.Version {
		t.Fatalf("small capture pinned v%d, %v", pinned.Definition.Version, err)
	}
	if selected, err := selectWorkflowDefinitionTx(ctx, s.DatabaseForTesting(), ref, "carry-small"); err != nil || selected.Definition.Version != small.Definition.Version {
		t.Fatalf("a small pin selected v%d, %v; want it to stay small", selected.Definition.Version, err)
	}
	if err := repin(s, "carry-small", head); err == nil {
		t.Fatal("a small pin carried forward onto the full composition")
	}
}

// Every pin replays from the log: the size precedes the pin it selects, and
// replay holds a selection only to its shape, so a binary that executes more
// check kinds replays older logs. A live fold requires the exact derivation.
func TestWorkflowPinSelectionReplays(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openTemp(t)
	unexecutable := []FootprintCheck{{Kind: "scenario", Value: "a"}, {Kind: "scenario", Value: "b"}}
	for _, workID := range []string{"replay-small", "replay-retained", "replay-larger"} {
		seedWork(t, s, workID)
	}
	recordSmallWorkSizeForTest(t, s, "replay-small")
	recordWorkSizeForTest(t, s, "replay-retained", 20, unexecutable, nil)
	recordWorkSizeForTest(t, s, "replay-larger", 400, nil, nil)
	for _, workID := range []string{"replay-small", "replay-retained", "replay-larger"} {
		if _, err := pinWorkflowForTest(t, s, workID, "workflow.implementation"); err != nil {
			t.Fatal(err)
		}
	}
	if err := RebuildFromLog(ctx, s); err != nil {
		t.Fatalf("the sized pins do not replay: %v", err)
	}

	head, err := BuiltinWorkflowDefinitionForRef("workflow.implementation")
	if err != nil {
		t.Fatal(err)
	}
	subset := workflowDefinitionSelectedPayload{Ref: head.Definition.Ref, Version: head.Definition.Version, SizeSelection: &workflowSizeSelection{
		Size: WorkSizeSmall, Retention: &workflowFullRetention{Reason: WorkflowRetainedUnexecutableChecks, Checks: unexecutable[:1]},
	}}
	foreign := subset
	foreign.SizeSelection = &workflowSizeSelection{Size: WorkSizeSmall, Retention: &workflowFullRetention{Reason: WorkflowRetainedUnexecutableChecks, Checks: []FootprintCheck{{Kind: "scenario", Value: "c"}}}}
	db := s.DatabaseForTesting()
	if err := checkWorkflowPinSelectionTx(workflowReplayContext(ctx), db, "replay-retained", subset); err != nil {
		t.Fatalf("replay refused a retention of footprint checks: %v", err)
	}
	if err := checkWorkflowPinSelectionTx(ctx, db, "replay-retained", subset); err == nil {
		t.Fatal("a live fold admitted a retention other than the derived one")
	}
	if err := checkWorkflowPinSelectionTx(workflowReplayContext(ctx), db, "replay-retained", foreign); err == nil {
		t.Fatal("replay admitted a retention outside the footprint")
	}
	small, err := builtinWorkflowDefinitionForSize("workflow.implementation", WorkSizeSmall)
	if err != nil {
		t.Fatal(err)
	}
	// A binary that learns to execute a retained check derives small; the
	// replayed full pin still folds, and a live fold refuses it.
	executable := []FootprintCheck{{Kind: "go_test", Value: "internal/store.TestBody"}}
	seedWork(t, s, "replay-learned")
	recordWorkSizeForTest(t, s, "replay-learned", 20, executable, nil)
	learned := workflowDefinitionSelectedPayload{Ref: head.Definition.Ref, Version: head.Definition.Version, SizeSelection: &workflowSizeSelection{
		Size: WorkSizeSmall, Retention: &workflowFullRetention{Reason: WorkflowRetainedUnexecutableChecks, Checks: executable},
	}}
	if err := checkWorkflowPinSelectionTx(workflowReplayContext(ctx), db, "replay-learned", learned); err != nil {
		t.Fatalf("replay refused a retention this binary no longer derives: %v", err)
	}
	if err := checkWorkflowPinSelectionTx(ctx, db, "replay-learned", learned); err == nil {
		t.Fatal("a live fold admitted a retention this binary does not derive")
	}
	misfit := workflowDefinitionSelectedPayload{Ref: small.Definition.Ref, Version: small.Definition.Version, SizeSelection: &workflowSizeSelection{Size: WorkSizeLarger}}
	if err := checkWorkflowPinSelectionTx(workflowReplayContext(ctx), db, "replay-larger", misfit); err == nil {
		t.Fatal("replay admitted a small composition pin for larger work")
	}
}

// A resize successor pins through the selector at its new size: an operator
// override down to small takes the small composition when the carried
// footprint's checks all execute, and keeps the full one otherwise.
func TestWorkResizeSuccessorPinsAtItsNewSize(t *testing.T) {
	t.Parallel()
	small, err := builtinWorkflowDefinitionForSize("workflow.implementation", WorkSizeSmall)
	if err != nil {
		t.Fatal(err)
	}
	head, err := BuiltinWorkflowDefinitionForRef("workflow.implementation")
	if err != nil {
		t.Fatal(err)
	}
	// unread is a complete record from before the shard read: its resize
	// copy still knows no shard state, so its go_package check keeps full.
	for _, record := range []string{"complete", "incomplete", "unread"} {
		t.Run(record, func(t *testing.T) {
			t.Parallel()
			s := openTemp(t)
			workID := "resize-down"
			switch record {
			case "complete":
				seedOutsideRepairTestWork(t, s, workID)
				if size := recordWorkSizeForTest(t, s, workID, 400, nil, nil); size != WorkSizeLarger {
					t.Fatalf("fixture decided %s", size)
				}
			case "unread":
				seedOutsideRepairTestWork(t, s, workID)
				recordWorkSizeFootprintForTest(t, s, workID, 400, func(f *WorkSizeFootprint) {
					f.ChecksComplete, f.PackageChecks = true, []FootprintPackageCheck{{Package: "internal/store", Kind: "go_package", Value: "./internal/store"}}
				})
			default:
				pinnedLargerWork(t, s, workID)
			}
			result, err := overrideWorkSize(t, s, workID, "resize-down-small", WorkSizeSmall)
			if err != nil || result.SuccessorWorkID == "" {
				t.Fatalf("override = %+v, %v; want a successor", result, err)
			}
			_, version, _ := workflowInstancePin(t, s, result.SuccessorWorkID)
			selection := recordedSizeSelection(t, s, result.SuccessorWorkID)
			want := map[string]string{"incomplete": WorkflowRetainedIncompleteInventory, "unread": WorkflowRetainedUnexecutableChecks}[record]
			if want == "" {
				if version != small.Definition.Version || selection.Size != WorkSizeSmall || selection.Retention != nil {
					t.Fatalf("complete successor pinned v%d, selection %+v; want the small composition", version, selection)
				}
				return
			}
			if version != head.Definition.Version || selection.Size != WorkSizeSmall || selection.Retention == nil || selection.Retention.Reason != want {
				t.Fatalf("%s successor pinned v%d, selection %+v; want the full head retained for %s", record, version, selection, want)
			}
		})
	}
}

// The size record's shard state is checked when it folds: shards need a
// complete check set and a read, and name sorted go_package check packages.
func TestWorkSizedFoldRefusesAMalformedShardState(t *testing.T) {
	t.Parallel()
	pkg := []FootprintCheck{{Kind: "go_package", Value: "./internal/agent"}, {Kind: "go_package", Value: "./internal/store"}}
	cases := map[string]func(*WorkSizeFootprint){
		"shards read without checks": func(f *WorkSizeFootprint) { f.ShardsRead = true },
		"shards without a read": func(f *WorkSizeFootprint) {
			f.ChecksComplete, f.Laws, f.ShardedPackages = true, []FootprintLaw{{LawID: "CD-0001", Checks: pkg}}, []string{"internal/store"}
		},
		"unsorted shards": func(f *WorkSizeFootprint) {
			f.ChecksComplete, f.ShardsRead, f.Laws, f.ShardedPackages = true, true, []FootprintLaw{{LawID: "CD-0001", Checks: pkg}}, []string{"internal/store", "internal/agent"}
		},
		"shard of no go_package check": func(f *WorkSizeFootprint) {
			f.ChecksComplete, f.ShardsRead, f.Laws, f.ShardedPackages = true, true, []FootprintLaw{{LawID: "CD-0001", Checks: pkg}}, []string{"internal/other"}
		},
	}
	for name, shape := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			s := openTemp(t)
			seedWork(t, s, "shard-state")
			event, _ := workSizedEventForTest(t, s, "shard-state", 20, shape)
			assertFailureKind(t, applyWorkEvent(t, s, event, nil), KindInvariantViolation)
		})
	}
	t.Run("well-formed", func(t *testing.T) {
		t.Parallel()
		s := openTemp(t)
		seedWork(t, s, "shard-state")
		recordWorkSizeForTest(t, s, "shard-state", 20, pkg, []string{"internal/agent", "internal/store"})
	})
}

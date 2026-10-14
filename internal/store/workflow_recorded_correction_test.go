package store

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func recordCorrectionFixture(t *testing.T, fixture workflowReturnRouteFixture, workID, step string, payloadVersion int) Event {
	t.Helper()
	actor, err := WorkflowActorRef(fixture.owner)
	if err != nil {
		t.Fatal(err)
	}
	var version int64
	if err := fixture.store.DatabaseForTesting().QueryRow(`SELECT version FROM work_items WHERE id=?`, workID).Scan(&version); err != nil {
		t.Fatal(err)
	}
	event := workflowEventWithActor(fmt.Sprintf("%s:recorded-correction-v%d", workID, payloadVersion), WorkflowActionCompleted, workID, actor, map[string]any{
		"work_id": workID, "expected_version": version, "resulting_version": version + 1,
		"action_id": "request_correction", "step_id": step, "attempt_epoch": 1,
		"actor_ref": actor, "result_evidence_refs": []string{}, "changed_refs": []string{workID},
		"correction_diagnosis":     "The retained result requires correction.",
		"correction_strategy":      "Return to the recorded producer.",
		"correction_predicate_ids": []string{"predicate:return-route"},
		"correction_evidence_refs": []string{"evidence:return-route-verification"},
	})
	event.PayloadVersion = payloadVersion
	insertHistoricalEvent(t, fixture.store, event)
	return event
}

func TestRecordedCorrectionReturnsReleasedRefinement(t *testing.T) {
	for _, tc := range []struct {
		ref     string
		version int64
		target  string
	}{
		{"workflow.break_fix", 8, "repair"},
		{"workflow.implementation", 15, "execution"},
	} {
		t.Run(tc.ref, func(t *testing.T) {
			workID := fmt.Sprintf("recorded-correction-%d-%s", tc.version, tc.target)
			fixture := seedHistoricalWorkflowReturnRouteFixture(t, workID, tc.ref, tc.version, "refine")
			s := fixture.store
			defer s.Close()
			if got := workflowCorrectionReturnTarget(mustRecordedCorrectionPin(t, tc.ref, tc.version).Definition, "refine"); got != "" {
				t.Fatalf("live route unexpectedly admits refinement: %s", got)
			}
			liveBefore := histEventRows(t, s)
			if err := InspectWorkflowActionAdmission(context.Background(), s, WorkflowActionPreflightRequest{
				WorkID: workID, ActionID: "request_correction", Actor: fixture.owner,
				Payload: json.RawMessage(`{"diagnosis":"The result differs.","strategy":"Repeat production.","predicate_ids":["predicate:return-route"],"evidence_refs":["evidence:return-route-verification"]}`),
			}); err == nil {
				t.Fatal("historical replay support admitted a live refinement correction")
			}
			if !reflect.DeepEqual(liveBefore, histEventRows(t, s)) {
				t.Fatal("refused live correction appended an event")
			}
			recordCorrectionFixture(t, fixture, workID, "refine", 3)
			before := histEventRows(t, s)
			for range 2 {
				if err := RebuildFromLog(context.Background(), s); err != nil {
					t.Fatal(err)
				}
				if got := currentStep(t, s, workID); got != tc.target {
					t.Fatalf("recorded return = %q, want %q", got, tc.target)
				}
				if !reflect.DeepEqual(before, histEventRows(t, s)) {
					t.Fatal("rebuild changed retained event headers, order, or bytes")
				}
			}
			snapshot := filepath.Join(t.TempDir(), "snapshot.db")
			if _, err := Backup(context.Background(), s, snapshot); err != nil {
				t.Fatal(err)
			}
			if _, err := RestoreBackup(context.Background(), snapshot, filepath.Join(t.TempDir(), "restored.db")); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func mustRecordedCorrectionPin(t *testing.T, ref string, version int64) RegisteredDefinition {
	t.Helper()
	entry, ok := BuiltinWorkflowRegistry().Lookup(ref, version)
	if !ok {
		t.Fatalf("missing registered definition %s v%d", ref, version)
	}
	return entry
}

// The archived rule's admission requires an implementation or break-fix
// family and a preceding eligible producer. Runtime replay uses only table data.
func archivedCorrectionTargetForProof(definition WorkflowDefinition, currentStep string) string {
	if definition.WorkKind != WorkKindImplementation && definition.WorkKind != WorkKindBreakFix {
		return ""
	}
	return archivedCorrectionResolverForProof(definition, currentStep)
}

func archivedCorrectionResolverForProof(definition WorkflowDefinition, currentStep string) string {
	preferred, fallback := "", ""
	preferredAction := "start_execution"
	if definition.WorkKind == WorkKindBreakFix {
		preferredAction = "start_repair"
	}
	for _, candidate := range definition.StepGraph.Steps {
		if candidate.Kind != WorkflowStepExternalEffect || !workflowStepFollows(definition, candidate.ID, currentStep) {
			continue
		}
		if fallback == "" {
			fallback = candidate.ID
		}
		if containsString(candidate.Actions, preferredAction) {
			preferred = candidate.ID
		}
	}
	if preferred != "" {
		return preferred
	}
	return fallback
}

func TestRecordedCorrectionTableMatchesEveryReleasedPinAndStep(t *testing.T) {
	want := map[recordedCorrectionPin]recordedCorrectionRoute{}
	excludedArchitectureTargets := 0
	for _, definition := range BuiltinWorkflowDefinitionsWithHistory() {
		entry := mustRecordedCorrectionPin(t, definition.Ref, definition.Version)
		pin := recordedCorrectionPin{definition.Ref, definition.Version, entry.Digest}
		for _, step := range definition.StepGraph.Steps {
			old, current := archivedCorrectionTargetForProof(definition, step.ID), workflowCorrectionReturnTarget(definition, step.ID)
			if old != "" && current != "" && old != current {
				t.Fatalf("conflicting applicable rules at %v %s: %s != %s", pin, step.ID, old, current)
			}
			if definition.Ref == "workflow.architecture_spike" {
				raw := archivedCorrectionResolverForProof(definition, step.ID)
				if raw != "" && current != "" && raw != current {
					excludedArchitectureTargets++
					if old != "" {
						t.Fatal("raw architecture-spike target survived archived admission")
					}
				}
			}
			expected := current
			if old != "" {
				route := want[pin]
				if route.target != "" && route.target != old {
					t.Fatal("one pin has inconsistent archived producers")
				}
				route.target = old
				route.steps = append(route.steps, step.ID)
				want[pin] = route
				expected = old
			}
			got, err := workflowRecordedCorrectionReturnTarget(entry, step.ID)
			if err != nil || got != expected {
				t.Fatalf("%v %s: target=%q want=%q error=%v", pin, step.ID, got, expected, err)
			}
		}
	}
	for pin, route := range want {
		sort.Strings(route.steps)
		want[pin] = route
	}
	if !reflect.DeepEqual(want, recordedCorrectionRoutes) {
		t.Fatal("recorded table differs from admission-filtered archived rule over released pins")
	}
	if excludedArchitectureTargets == 0 {
		t.Fatal("proof did not exercise excluded raw architecture-spike targets")
	}
}

func TestRecordedCorrectionSharedPinSupportsBothPayloadEras(t *testing.T) {
	const workID = "recorded-correction-shared-pin"
	fixture := seedHistoricalWorkflowReturnRouteFixture(t, workID, "workflow.break_fix", 8, "verify")
	s := fixture.store
	defer s.Close()
	for _, payloadVersion := range []int{3, 4} {
		if payloadVersion == 4 {
			for _, sourceStep := range []string{"repair", "refine"} {
				var version int64
				if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM work_items WHERE id=?`, workID).Scan(&version); err != nil {
					t.Fatal(err)
				}
				entry := mustRecordedCorrectionPin(t, "workflow.break_fix", 8)
				action := ""
				for _, candidate := range workflowStep(entry.Definition, sourceStep).Actions {
					if mode, ok := workflowActionExecutionMode(entry.Definition, candidate); ok && mode == ActionAdvance {
						action = candidate
						break
					}
				}
				if action == "" {
					t.Fatalf("fixture has no advancing action at %s", sourceStep)
				}
				actor, _ := WorkflowActorRef(fixture.owner)
				event := workflowEventWithActor(workID+":fresh-production:"+sourceStep, WorkflowActionCompleted, workID, actor, map[string]any{
					"work_id": workID, "expected_version": version, "resulting_version": version + 1,
					"step_id": sourceStep, "action_id": action, "attempt_epoch": 1,
					"actor_ref": actor, "result_evidence_refs": []string{}, "changed_refs": []string{workID},
				})
				if err := applyWorkflowTestOperationDirect(context.Background(), s, Operation{Events: []Event{event}}); err != nil {
					t.Fatal(err)
				}
			}
		}
		recordCorrectionFixture(t, fixture, workID, "verify", payloadVersion)
		before := histEventRows(t, s)
		if err := RebuildFromLog(context.Background(), s); err != nil {
			t.Fatal(err)
		}
		if currentStep(t, s, workID) != "repair" || !reflect.DeepEqual(before, histEventRows(t, s)) {
			t.Fatal("same-pin correction changed its target or retained history")
		}
	}
}

func TestRecordedCorrectionRefusalsAreAttributedAndAtomic(t *testing.T) {
	for _, tc := range []struct {
		step    string
		version int
	}{
		{"missing-step", 3},
		{"refine", 5},
	} {
		t.Run(fmt.Sprintf("%s-v%d", tc.step, tc.version), func(t *testing.T) {
			const workID = "recorded-correction-refusal"
			fixture := seedHistoricalWorkflowReturnRouteFixture(t, workID, "workflow.break_fix", 8, "refine")
			s := fixture.store
			defer s.Close()
			event := recordCorrectionFixture(t, fixture, workID, tc.step, tc.version)
			before, events := projectionSnapshot(t, s), histEventRows(t, s)
			err := RebuildFromLog(context.Background(), s)
			var failure *Failure
			if err == nil || !failureAs(err, &failure) || failure.EventID != event.EventID || failure.EventKind != event.Kind || failure.PayloadVersion != tc.version || failure.SubjectID != workID || failure.Sequence == 0 || failure.Stage == "" {
				t.Fatalf("unattributed refusal: %v", err)
			}
			if before != projectionSnapshot(t, s) || !reflect.DeepEqual(events, histEventRows(t, s)) {
				t.Fatal("refused replay changed projections or retained bytes")
			}
		})
	}
	entry := mustRecordedCorrectionPin(t, "workflow.break_fix", 8)
	entry.Digest = "sha256:" + strings.Repeat("0", 64)
	if _, err := workflowRecordedCorrectionReturnTarget(entry, "verify"); err == nil {
		t.Fatal("unknown digest borrowed a declared correction route")
	}
	entry.Definition.Version = 1000000
	if _, err := workflowRecordedCorrectionReturnTarget(entry, "verify"); err == nil {
		t.Fatal("unknown version borrowed a declared correction route")
	}
}

func TestFoldCorrectionReturnTargetSelectsResolverAndRefusesEmptyRoute(t *testing.T) {
	entry := mustRecordedCorrectionPin(t, "workflow.break_fix", 8)
	replayCtx := workflowReplayContext(context.Background())

	// Live effects resolve only through the declared route reader.
	liveStep := ""
	for _, step := range entry.Definition.StepGraph.Steps {
		if workflowCorrectionReturnTarget(entry.Definition, step.ID) != "" {
			liveStep = step.ID
			break
		}
	}
	if liveStep == "" {
		t.Fatal("no step declares a live correction route; the live selection case is unreachable")
	}
	want := workflowCorrectionReturnTarget(entry.Definition, liveStep)
	got, err := workflowFoldCorrectionReturnTarget(context.Background(), entry, liveStep, liveStep)
	if err != nil || got != want {
		t.Fatalf("live fold return at %s = (%q, %v), want (%q, nil)", liveStep, got, err, want)
	}

	// A step the live reader cannot resolve refuses even where the recorded
	// table holds a route: live effects never consult the table.
	if liveRefine := workflowCorrectionReturnTarget(entry.Definition, "refine"); liveRefine != "" {
		t.Fatalf("refine unexpectedly declares a live route %q; the historical-only case is unreachable", liveRefine)
	}
	recorded, err := workflowRecordedCorrectionReturnTarget(entry, "refine")
	if err != nil || recorded == "" {
		t.Fatalf("recorded refine route = (%q, %v), want a recorded target", recorded, err)
	}
	_, err = workflowFoldCorrectionReturnTarget(context.Background(), entry, "refine", "refine")
	wantFoldCorrectionFailure(t, err, "correction has no declared recovery route to a producer step", "repair the pinned workflow definition")

	// Replay resolves the recorded target only after the source-step check.
	if got, err := workflowFoldCorrectionReturnTarget(replayCtx, entry, "refine", "refine"); err != nil || got != recorded {
		t.Fatalf("replay fold return at refine = (%q, %v), want (%q, nil)", got, err, recorded)
	}
	_, err = workflowFoldCorrectionReturnTarget(replayCtx, entry, "verify", "refine")
	wantFoldCorrectionFailure(t, err, "recorded correction source step differs from the pinned current step", "restore a consistent recorded workflow history")

	// A correction with no resolved recovery route refuses on both paths.
	if empty := workflowCorrectionReturnTarget(entry.Definition, "repair"); empty != "" {
		t.Fatalf("repair unexpectedly declares a live route %q; the empty-route case is unreachable", empty)
	}
	if unresolved, err := workflowRecordedCorrectionReturnTarget(entry, "repair"); err != nil || unresolved != "" {
		t.Fatalf("recorded repair route = (%q, %v), want no route", unresolved, err)
	}
	_, err = workflowFoldCorrectionReturnTarget(context.Background(), entry, "repair", "repair")
	wantFoldCorrectionFailure(t, err, "correction has no declared recovery route to a producer step", "repair the pinned workflow definition")
	_, err = workflowFoldCorrectionReturnTarget(replayCtx, entry, "repair", "repair")
	wantFoldCorrectionFailure(t, err, "correction has no declared recovery route to a producer step", "repair the pinned workflow definition")
}

func wantFoldCorrectionFailure(t *testing.T, err error, detail, recovery string) {
	t.Helper()
	var failure *Failure
	if err == nil || !failureAs(err, &failure) || failure.Kind != KindIllegalLifecycleTransition || failure.Op != "fold_event" || failure.Detail != detail || failure.RetrySafe || failure.RecoveryAction != recovery {
		t.Fatalf("fold correction refusal = %v, want %s %q with recovery %q", err, KindIllegalLifecycleTransition, detail, recovery)
	}
}

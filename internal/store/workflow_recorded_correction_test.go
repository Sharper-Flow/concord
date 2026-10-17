package store

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
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
			if err := WorkflowActionPreflightWithRegistry(context.Background(), s, BuiltinWorkflowRegistry(), WorkflowActionPreflightRequest{
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
// family, a pin at or below the family's frozen released registration cap,
// and a preceding eligible producer. The cap comes from
// workflowReleasedRecoveryRouteFamilies alone, so the proof and the frozen
// route quarantine encode the era boundary once. Runtime replay uses only
// table data.
func archivedCorrectionTargetForProof(definition WorkflowDefinition, currentStep string) string {
	if definition.WorkKind != WorkKindImplementation && definition.WorkKind != WorkKindBreakFix {
		return ""
	}
	family, ok := workflowReleasedRecoveryRouteFamilies[definition.Ref]
	if !ok || definition.Version > family.through {
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

// TestRecordedCorrectionFailureAttributionPinsUseDeclaredRoutes holds the
// four registered post-era failure-attribution pins: they have no archived
// row, and replay uses the declared routes at every definition step,
// including empty results.
func TestRecordedCorrectionFailureAttributionPinsUseDeclaredRoutes(t *testing.T) {
	for _, tc := range []struct {
		ref     string
		version int64
	}{
		{"workflow.break_fix", 28},
		{"workflow.break_fix", 29},
		{"workflow.implementation", 31},
		{"workflow.implementation", 32},
	} {
		t.Run(fmt.Sprintf("%s-v%d", tc.ref, tc.version), func(t *testing.T) {
			entry := mustRecordedCorrectionPin(t, tc.ref, tc.version)
			boundary, ok := workflowReleasedRecoveryRouteFamilies[tc.ref]
			if !ok {
				t.Fatalf("failure-attribution pin: no frozen released family for %s", tc.ref)
			}
			if tc.version <= boundary.through {
				t.Fatalf("failure-attribution pin %s v%d is not post-era above the frozen cap %d", tc.ref, tc.version, boundary.through)
			}
			pin := recordedCorrectionPin{tc.ref, tc.version, entry.Digest}
			if row, carried := recordedCorrectionRoutes[pin]; carried {
				t.Fatalf("%s v%d pin %s carries a historical table row (%q, %v)", tc.ref, tc.version, pin.digest, row.target, row.steps)
			}
			withoutRoute, withRoute := 0, 0
			for _, step := range entry.Definition.StepGraph.Steps {
				declared := workflowCorrectionReturnTarget(entry.Definition, step.ID)
				got, err := workflowRecordedCorrectionReturnTarget(entry, step.ID)
				if err != nil || got != declared {
					t.Fatalf("%s v%d recorded return at %s = (%q, %v), want the unchanged declared reader's %q", tc.ref, tc.version, step.ID, got, err, declared)
				}
				if declared == "" {
					withoutRoute++
				} else {
					withRoute++
				}
			}
			if withoutRoute == 0 || withRoute == 0 {
				t.Fatalf("%s v%d comparison is vacuous: %d steps resolved no route and %d resolved one", tc.ref, tc.version, withoutRoute, withRoute)
			}
		})
	}
}

// recordedCorrectionExpectedTableForProof derives the exact expected recorded
// correction table from the era-bound archived proof over every released pin
// and step. It consults only the archived admission, the declared route
// reader, and the registry — never the global recorded table.
func recordedCorrectionExpectedTableForProof() (map[recordedCorrectionPin]recordedCorrectionRoute, error) {
	want := map[recordedCorrectionPin]recordedCorrectionRoute{}
	var problems []string
	excludedArchitectureTargets := 0
	for _, definition := range BuiltinWorkflowDefinitionsWithHistory() {
		entry, ok := BuiltinWorkflowRegistry().Lookup(definition.Ref, definition.Version)
		if !ok {
			problems = append(problems, fmt.Sprintf("missing registered definition %s v%d", definition.Ref, definition.Version))
			continue
		}
		pin := recordedCorrectionPin{definition.Ref, definition.Version, entry.Digest}
		for _, step := range definition.StepGraph.Steps {
			old, current := archivedCorrectionTargetForProof(definition, step.ID), workflowCorrectionReturnTarget(definition, step.ID)
			if old != "" && current != "" && old != current {
				problems = append(problems, fmt.Sprintf("conflicting applicable rules at %s v%d %s: %s != %s", pin.ref, pin.version, step.ID, old, current))
			}
			if definition.Ref == "workflow.architecture_spike" {
				raw := archivedCorrectionResolverForProof(definition, step.ID)
				if raw != "" && current != "" && raw != current {
					excludedArchitectureTargets++
					if old != "" {
						problems = append(problems, "raw architecture-spike target survived archived admission")
					}
				}
			}
			if old == "" {
				continue
			}
			route := want[pin]
			if route.target != "" && route.target != old {
				problems = append(problems, fmt.Sprintf("one pin has inconsistent archived producers at %s v%d %s", pin.ref, pin.version, step.ID))
			}
			route.target = old
			route.steps = append(route.steps, step.ID)
			want[pin] = route
		}
	}
	for pin, route := range want {
		sort.Strings(route.steps)
		want[pin] = route
	}
	if excludedArchitectureTargets == 0 {
		problems = append(problems, "proof did not exercise excluded raw architecture-spike targets")
	}
	if len(problems) != 0 {
		return nil, fmt.Errorf("recorded correction table derivation failed: %s", strings.Join(problems, "; "))
	}
	return want, nil
}

// recordedCorrectionExactTableError is the one exact-map checker: nil when
// candidate equals want under reflect.DeepEqual, a named error otherwise.
func recordedCorrectionExactTableError(want, candidate map[recordedCorrectionPin]recordedCorrectionRoute) error {
	if reflect.DeepEqual(want, candidate) {
		return nil
	}
	return fmt.Errorf("candidate table differs from the derived %d-row expected table", len(want))
}

func TestRecordedCorrectionTableMatchesEveryReleasedPinAndStep(t *testing.T) {
	want, err := recordedCorrectionExpectedTableForProof()
	if err != nil {
		t.Fatal(err)
	}
	for _, definition := range BuiltinWorkflowDefinitionsWithHistory() {
		entry := mustRecordedCorrectionPin(t, definition.Ref, definition.Version)
		for _, step := range definition.StepGraph.Steps {
			expected := workflowCorrectionReturnTarget(definition, step.ID)
			if old := archivedCorrectionTargetForProof(definition, step.ID); old != "" {
				expected = old
			}
			got, err := workflowRecordedCorrectionReturnTarget(entry, step.ID)
			if err != nil || got != expected {
				t.Fatalf("%s v%d %s: target=%q want=%q error=%v", definition.Ref, definition.Version, step.ID, got, expected, err)
			}
		}
	}
	if err := recordedCorrectionExactTableError(want, recordedCorrectionRoutes); err != nil {
		t.Fatal(err)
	}
}

// TestRecordedCorrectionExactTableRejectsInEraRowDeletion is a mutation
// witness: the derived expected table carries the last in-era implementation
// and break-fix row, and a copy missing that row — selected from the frozen
// registration caps — fails the exact-map check.
func TestRecordedCorrectionExactTableRejectsInEraRowDeletion(t *testing.T) {
	want, err := recordedCorrectionExpectedTableForProof()
	if err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{"workflow.implementation", "workflow.break_fix"} {
		boundary, ok := workflowReleasedRecoveryRouteFamilies[ref]
		if !ok {
			t.Fatalf("mutation witness: no frozen released family for %s", ref)
		}
		entry := mustRecordedCorrectionPin(t, ref, boundary.through)
		pin := recordedCorrectionPin{ref, boundary.through, entry.Digest}
		if _, carried := want[pin]; !carried {
			t.Fatalf("derived expected table lacks its last in-era %s row v%d", ref, boundary.through)
		}
		candidate := maps.Clone(want)
		delete(candidate, pin)
		if err := recordedCorrectionExactTableError(want, candidate); err == nil {
			t.Fatalf("deleting the last in-era %s row v%d was not rejected", ref, boundary.through)
		}
	}
}

// TestRecordedCorrectionPostEraPinsNeedNoTableRow is a mutation witness: the
// first post-era pin and the latest registered successor of each correction
// family carry no derived row, and adding the archived row such a pin would
// once have had fails the exact-map check.
func TestRecordedCorrectionPostEraPinsNeedNoTableRow(t *testing.T) {
	want, err := recordedCorrectionExpectedTableForProof()
	if err != nil {
		t.Fatal(err)
	}
	latest := map[string]int64{}
	for _, definition := range BuiltinWorkflowDefinitionsWithHistory() {
		if definition.Version > latest[definition.Ref] {
			latest[definition.Ref] = definition.Version
		}
	}
	for _, ref := range []string{"workflow.implementation", "workflow.break_fix"} {
		boundary, ok := workflowReleasedRecoveryRouteFamilies[ref]
		if !ok {
			t.Fatalf("post-era witness: no frozen released family for %s", ref)
		}
		for _, version := range []int64{boundary.through + 1, latest[ref]} {
			if version <= boundary.through {
				t.Fatalf("post-era witness pin %s v%d is not post-era", ref, version)
			}
			entry := mustRecordedCorrectionPin(t, ref, version)
			pin := recordedCorrectionPin{ref, version, entry.Digest}
			if _, carried := want[pin]; carried {
				t.Fatalf("derived expected table carries a post-era row for %s v%d", ref, version)
			}
			added := recordedCorrectionRoute{}
			for _, step := range entry.Definition.StepGraph.Steps {
				if target := archivedCorrectionResolverForProof(entry.Definition, step.ID); target != "" {
					added.target = target
					added.steps = append(added.steps, step.ID)
				}
			}
			if added.target == "" {
				t.Fatalf("post-era pin %s v%d derives no archived row to add", ref, version)
			}
			candidate := maps.Clone(want)
			candidate[pin] = added
			if err := recordedCorrectionExactTableError(want, candidate); err == nil {
				t.Fatalf("adding a post-era row for %s v%d was not rejected", ref, version)
			}
		}
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
		{"refine", eventKindRegistry[WorkflowActionCompleted].CurrentVersion + 1},
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

// TestRecordedCorrectionPostEraPinsMatchTheDeclaredRoute holds the fixed
// post-era implementation and break-fix pins on their declared fallback:
// the archived oracle, the declared route reader, and the unchanged replay
// reader return one identical nonempty target at every compared step.
func TestRecordedCorrectionPostEraPinsMatchTheDeclaredRoute(t *testing.T) {
	postEraFamilies := []struct {
		ref   string
		last  int64
		steps []string
	}{
		{ref: "workflow.break_fix", last: 29, steps: []string{"complete", "delivery", "verify"}},
		{ref: "workflow.implementation", last: 32, steps: []string{"acceptance", "delivery", "release"}},
	}
	pins, compared, mismatches := 0, 0, 0
	for _, family := range postEraFamilies {
		boundary, ok := workflowReleasedRecoveryRouteFamilies[family.ref]
		if !ok {
			t.Fatalf("post-era witness: no frozen released family for %s", family.ref)
		}
		for version := boundary.through + 1; version <= family.last; version++ {
			pins++
			entry := mustRecordedCorrectionPin(t, family.ref, version)
			for _, step := range family.steps {
				archived := archivedCorrectionResolverForProof(entry.Definition, step)
				declared := workflowCorrectionReturnTarget(entry.Definition, step)
				replay, err := workflowRecordedCorrectionReturnTarget(entry, step)
				if archived != "" && archived == declared && err == nil && replay == declared {
					compared++
					t.Logf("post-era witness %s v%d digest %s step %s archived %q declared %q replay %q match", family.ref, version, entry.Digest, step, archived, declared, replay)
					continue
				}
				mismatches++
				t.Errorf("post-era witness %s v%d digest %s step %s archived %q declared %q replay %q err %v", family.ref, version, entry.Digest, step, archived, declared, replay, err)
			}
		}
	}
	if pins != 20 {
		t.Errorf("post-era witness selected %d pins, want 20", pins)
	}
	if compared != 60 {
		t.Errorf("post-era witness compared %d steps, want 60", compared)
	}
	t.Logf("post-era witness totals: pins %d, compared steps %d, mismatches %d", pins, compared, mismatches)
}

// TestRecordedCorrectionPostEraRefineIsRefusedByLiveWriter drives the real
// live writer route — runIssue933OperatorAction through
// applyWorkflowActionRawTx and its commit, with no replay context — at the
// refine step of the first post-era pin of each correction family, using the
// fixture's own owner and operator actors. Refine declares no correction
// route on those pins, so the request must return the one typed
// invalid-operation refusal the no-route shape produces and leave the
// retained event rows, the
// work version, the refine step, the pinned definition, and the projections
// exactly as they stood.
func TestRecordedCorrectionPostEraRefineIsRefusedByLiveWriter(t *testing.T) {
	for _, tc := range []struct {
		ref  string
		slug string
	}{
		{"workflow.implementation", "implementation"},
		{"workflow.break_fix", "break-fix"},
	} {
		boundary, ok := workflowReleasedRecoveryRouteFamilies[tc.ref]
		if !ok {
			t.Fatalf("post-era writer witness: no frozen released family for %s", tc.ref)
		}
		version := boundary.through + 1
		t.Run(fmt.Sprintf("%s-v%d", tc.ref, version), func(t *testing.T) {
			workID := fmt.Sprintf("recorded-correction-post-era-refine-%s", tc.slug)
			fixture := seedHistoricalWorkflowReturnRouteFixture(t, workID, tc.ref, version, "refine")
			s := fixture.store
			defer s.Close()
			entry := mustRecordedCorrectionPin(t, tc.ref, version)
			if got := workflowCorrectionReturnTarget(entry.Definition, "refine"); got != "" {
				t.Fatalf("refine unexpectedly declares a live correction route %q; a refusal there would not name the absent route", got)
			}
			events, projections := histEventRows(t, s), projectionSnapshot(t, s)
			var versionBefore, definitionVersionBefore int64
			var stepBefore string
			if err := s.DatabaseForTesting().QueryRow(`SELECT w.version,i.current_step,i.definition_version FROM work_items w JOIN workflow_instances i ON i.work_id=w.id WHERE w.id=?`, workID).Scan(&versionBefore, &stepBefore, &definitionVersionBefore); err != nil {
				t.Fatal(err)
			}
			if stepBefore != "refine" || definitionVersionBefore != version {
				t.Fatalf("fixture = %s@%d at %q, want the first post-era pin v%d at refine", tc.ref, definitionVersionBefore, stepBefore, version)
			}
			correction := json.RawMessage(`{"diagnosis":"the retained result requires correction","strategy":"return to the recorded producer step","predicate_ids":["predicate:return-route"],"evidence_refs":["evidence:return-route-verification"]}`)
			err := runIssue933OperatorAction(t, s, workID, "request_correction", correction, fixture.owner, fixture.operator)
			const wantDetail = "request_correction requires a current non-ok verification verdict after worker delivery"
			const wantRecovery = "record the current verification verdict or reread the work pin"
			var failure *Failure
			if err == nil || !failureAs(err, &failure) || failure.Kind != KindInvalidOperation || failure.Op != "workflow_action" || failure.Detail != wantDetail || failure.RetrySafe || failure.RecoveryAction != wantRecovery {
				t.Fatalf("post-era refine request_correction = %v, want %s %q with recovery %q", err, KindInvalidOperation, wantDetail, wantRecovery)
			}
			var versionAfter, definitionVersionAfter int64
			var stepAfter string
			if err := s.DatabaseForTesting().QueryRow(`SELECT w.version,i.current_step,i.definition_version FROM work_items w JOIN workflow_instances i ON i.work_id=w.id WHERE w.id=?`, workID).Scan(&versionAfter, &stepAfter, &definitionVersionAfter); err != nil {
				t.Fatal(err)
			}
			if versionAfter != versionBefore || stepAfter != stepBefore || definitionVersionAfter != definitionVersionBefore {
				t.Fatalf("refused correction moved state %d/%q@%d to %d/%q@%d", versionBefore, stepBefore, definitionVersionBefore, versionAfter, stepAfter, definitionVersionAfter)
			}
			if !reflect.DeepEqual(events, histEventRows(t, s)) {
				t.Fatal("refused correction appended or changed a retained event row")
			}
			if projections != projectionSnapshot(t, s) {
				t.Fatal("refused correction changed a projection")
			}
		})
	}
}

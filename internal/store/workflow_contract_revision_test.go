package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestPremiseRevisionPreservesPlanAndContractHistory(t *testing.T) {
	const id = "revision-preserves-plan"
	f := seedWorkflowReturnRouteFixture(t, id, WorkflowStepPlanResearch, "conclude")
	beforePlan, err := readWorkflowPlanTx(t.Context(), f.store.db, id)
	if err != nil {
		t.Fatal(err)
	}
	beforeContract, err := readWorkflowContractVersion(t.Context(), f.store.db, id, 1)
	if err != nil {
		t.Fatal(err)
	}
	successor := pContractSuccessor(t, f.store, id)
	if err := reviseContractRecord(t, f.store, id, f.owner, successor, readWorkVersion(t, f.store, id)); err != nil {
		t.Fatalf("ordinary premise revision: %v", err)
	}
	afterPlan, err := readWorkflowPlanTx(t.Context(), f.store.db, id)
	if err != nil || !reflect.DeepEqual(beforePlan, afterPlan) {
		t.Fatalf("revision changed plan attempts: before=%+v after=%+v err=%v", beforePlan, afterPlan, err)
	}
	active, err := activeWorkflowContractVersion(t.Context(), f.store.db, id, "test")
	if err != nil || active != 2 {
		t.Fatalf("active contract=%d err=%v, want 2", active, err)
	}
	previous, err := readWorkflowContractVersion(t.Context(), f.store.db, id, 1)
	if err != nil {
		t.Fatal(err)
	}
	previous.SupersededBy = beforeContract.SupersededBy
	if !reflect.DeepEqual(beforeContract, previous) {
		t.Fatalf("revision changed historical contract: before=%+v after=%+v", beforeContract, previous)
	}
	current, err := readWorkflowContractVersion(t.Context(), f.store.db, id, 2)
	if err != nil || current.Version != 2 {
		t.Fatalf("successor=%+v err=%v", current, err)
	}
}

func undeclaredActionRefusal(t *testing.T, err error) *Failure {
	t.Helper()
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindIllegalLifecycleTransition || failure.Detail != "action is not declared on a frontier step" || failure.RetrySafe || failure.EffectPossible {
		t.Fatalf("undeclared action refusal=%v, want a permanent illegal_lifecycle_transition with no effect", err)
	}
	return failure
}

func TestWorkflowActionPreflightRefusesUndeclaredActionAtCheckpoint(t *testing.T) {
	const id = "preflight-supersede-at-checkpoint"
	f := seedWorkflowReturnRouteFixture(t, id, WorkflowStepPlanResearch, "conclude")
	preflight := func(action string) error {
		_, err := WorkflowActionDefinitionFor(t.Context(), f.store, WorkflowActionPreflightRequest{WorkID: id, ExpectedVersion: readWorkVersion(t, f.store, id), StepID: "conclude", ActionID: action, Payload: json.RawMessage(`{}`), Actor: f.owner})
		return err
	}
	before := contractCorrectionEffectSnapshot(t, f.store, id)
	refusal := undeclaredActionRefusal(t, preflight("record_finding"))
	if contractCorrectionEffectSnapshot(t, f.store, id) != before {
		t.Fatal("preflight changed effects")
	}
	// The research plan's conclude step as the template contract declares it.
	want := []string{"conclude", "record_conclusion", "record_report", "link_successor", "confirm_premise", "record_review", "record_delivery", "complete_step", "complete", "bind_evidence"}
	recovery := refusal.RecoveryAction
	at := 0
	for _, name := range want {
		index := strings.Index(recovery[at:], name)
		if index < 0 {
			t.Fatalf("recovery %q omits %q after the preceding name", recovery, name)
		}
		at += index + len(name)
	}
	for _, phrase := range []string{"not its admission", "prerequisites", "actor"} {
		if !strings.Contains(recovery, phrase) {
			t.Errorf("recovery %q does not separate naming from admission: %q", recovery, phrase)
		}
	}
	for _, name := range []string{"record_finding", "investigate"} {
		if strings.Contains(recovery, name) {
			t.Errorf("recovery %q names %q, which the frontier does not declare", recovery, name)
		}
	}
	if again := undeclaredActionRefusal(t, preflight("record_finding")); again.RecoveryAction != recovery {
		t.Fatalf("one plan produced two recoveries: %q then %q", recovery, again.RecoveryAction)
	}
	if contractCorrectionEffectSnapshot(t, f.store, id) != before {
		t.Fatal("repeated preflight changed effects")
	}
	// A named action passes the preflight that refused the unnamed one.
	if err := preflight("record_conclusion"); err != nil {
		t.Fatalf("an action the frontier declares was refused: %v", err)
	}

	// An empty frontier names no step action.
	completeWorkflowPlanFixture(t, f.store, id)
	before = contractCorrectionEffectSnapshot(t, f.store, id)
	recovery = undeclaredActionRefusal(t, preflight("frame_research")).RecoveryAction
	if contractCorrectionEffectSnapshot(t, f.store, id) != before {
		t.Fatal("preflight against a complete plan changed effects")
	}
	for _, phrase := range []string{"every plan step is complete", "no frontier step declares an action"} {
		if !strings.Contains(recovery, phrase) {
			t.Errorf("complete-plan recovery %q omits %q", recovery, phrase)
		}
	}
	if strings.Contains(recovery, "(") {
		t.Errorf("complete-plan recovery %q lists a step action", recovery)
	}
}

// The remedy lists frontier steps in plan order, each with the actions it
// declares, inside the byte budget the agent boundary keeps whole, and an
// empty frontier names nothing.
func TestUndeclaredActionRemedyBoundsItsFrontierList(t *testing.T) {
	refuse := func(plan *WorkflowStepPlan) string {
		t.Helper()
		var failure *Failure
		if _, err := workflowPlanActionOwnerTx(t.Context(), nil, "work", "record_finding", "", plan); !errors.As(err, &failure) || failure.Kind != KindIllegalLifecycleTransition {
			t.Fatalf("undeclared action refusal=%v", err)
		}
		return failure.RecoveryAction
	}
	if recovery := refuse(nil); !strings.Contains(recovery, "has no plan") || !strings.Contains(recovery, "no frontier step declares an action") || strings.Contains(recovery, "(") {
		t.Fatalf("planless recovery %q names a step or misstates the plan", recovery)
	}

	complete := currentWorkflowPlanFixture(t, WorkflowStepPlanChange)
	for index := range complete.Steps {
		attempt := WorkflowPlanAttempt{Note: "done", CompletedAt: "2026-01-01T00:00:00Z"}
		// The final clean review closes only on a passing result.
		if complete.Steps[index].Reviewed {
			attempt.Reviewer, attempt.Result, attempt.Commit, attempt.Link = "concord-review", "pass", "commit:fixture", "https://example.com/review"
		}
		complete.Steps[index].Attempts = []WorkflowPlanAttempt{attempt}
	}
	if !WorkflowStepPlanComplete(complete) {
		t.Fatal("fixture plan is not complete")
	}
	if recovery := refuse(&complete); !strings.Contains(recovery, "every plan step is complete") || strings.Contains(recovery, "(") {
		t.Fatalf("complete-plan recovery %q names a step action", recovery)
	}

	// Every frontier width from one step to wider than one remedy holds,
	// including the width at which only the last step overflows.
	const maxSteps = 40
	// The agent boundary keeps this many bytes of a remedy whole.
	const boundaryBudget = 1000
	const declared = " (complete_step, record_review, record_delivery, link_successor, bind_evidence)"
	const note = "; frontier steps not listed: "
	omittedAny := false
	for steps := 1; steps <= maxSteps; steps++ {
		wide := WorkflowStepPlan{Type: WorkflowStepPlanChange, Steps: make([]WorkflowPlanStep, 0, steps)}
		ids := make([]string, 0, steps)
		for index := range steps {
			id := "step-" + fmt.Sprintf("%0*d", 60, index)
			ids = append(ids, id)
			wide.Steps = append(wide.Steps, WorkflowPlanStep{ID: id, After: []string{}})
		}
		if frontier := WorkflowStepPlanFrontier(wide); len(frontier) != steps {
			t.Fatalf("fixture frontier holds %d steps, want %d", len(frontier), steps)
		}
		recovery := refuse(&wide)
		if len(recovery) > boundaryBudget {
			t.Fatalf("%d steps: recovery of %d bytes exceeds the %d the boundary keeps whole", steps, len(recovery), boundaryBudget)
		}
		listed := 0
		for listed < steps && strings.Contains(recovery, ids[listed]+declared) {
			listed++
		}
		if listed < 1 || (listed < steps && strings.Contains(recovery, ids[listed])) {
			t.Fatalf("%d steps: recovery lists %d steps whole and cuts the next: %q", steps, listed, recovery)
		}
		at := strings.Index(recovery, note)
		if listed == steps {
			if at >= 0 {
				t.Fatalf("%d steps: recovery lists every step yet counts omissions: %q", steps, recovery)
			}
			continue
		}
		omittedAny = true
		var omitted int
		if at < 0 {
			t.Fatalf("%d steps: recovery does not count the steps it left out: %q", steps, recovery)
		}
		if _, err := fmt.Sscanf(recovery[at+len(note):], "%d", &omitted); err != nil || omitted != steps-listed {
			t.Fatalf("%d steps: recovery reports %d omitted steps (parse error %v), want %d: %q", steps, omitted, err, steps-listed, recovery)
		}
		if again := refuse(&wide); again != recovery {
			t.Fatalf("one plan produced two recoveries: %q then %q", recovery, again)
		}
	}
	if !omittedAny {
		t.Fatal("no fixture frontier was wider than one remedy holds")
	}
}

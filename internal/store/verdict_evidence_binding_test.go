package store

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
)

// seedLanelessResearchItem binds an artifact without a worker dispatch.
func seedLanelessResearchItem(t *testing.T, workID string) (*Store, WorkflowActor) {
	t.Helper()
	f := seedWorkflowReturnRouteFixtureWithPlan(t, workID, WorkflowStepPlanResearch, "investigate", []string{"artifact"}, nil)
	if err := runVerdictActionAs(t, f.store, workID, "bind_evidence", json.RawMessage(`{"evidence_kind":"artifact","immutable_subject_ref":"evidence:research-report"}`), 0, f.owner); err != nil {
		t.Fatalf("bind research artifact: %v", err)
	}
	moveWorkflowPlanFixtureFrontier(t, f.store, workID, "conclude")
	return f.store, f.owner
}

// The WorkPin exposes the bound evidence set with kinds at a step that
// declares record_verdict, and stays empty at a step that does not.
func TestWorkPinExposesBoundVerdictEvidenceAtVerdictSteps(t *testing.T) {
	t.Parallel()
	const workID = "issue974-pin-evidence"
	s, _ := seedLanelessResearchItem(t, workID)

	pin, err := ReadWorkPin(context.Background(), s, workID)
	if err != nil {
		t.Fatal(err)
	}
	if !stepDeclaresActionByName(t, s, workID, pin.Step, "record_verdict") {
		t.Fatalf("fixture step %q must declare record_verdict", pin.Step)
	}
	if len(pin.VerdictEvidence) == 0 {
		t.Fatal("verdict_evidence is empty at a record_verdict step")
	}
	found := map[string]string{}
	for _, entry := range pin.VerdictEvidence {
		if entry.EvidenceKind == "" || entry.ImmutableSubjectRef == "" {
			t.Fatalf("verdict_evidence entry %+v carries an empty kind or ref", entry)
		}
		found[entry.ImmutableSubjectRef] = entry.EvidenceKind
	}
	if kind, ok := found["evidence:research-report"]; !ok || kind != "artifact" {
		t.Fatalf("verdict_evidence %v omits the artifact-bound research report", pin.VerdictEvidence)
	}

	// A step that does not declare record_verdict exposes no set.
	const earlyWork = "issue974-pin-no-verdict-step"
	early := seedLanelessResearchItemAtFrame(t, earlyWork)
	earlyPin, err := ReadWorkPin(context.Background(), early, earlyWork)
	if err != nil {
		t.Fatal(err)
	}
	if stepDeclaresActionByName(t, early, earlyWork, earlyPin.Step, "record_verdict") {
		t.Fatalf("fixture step %q must not declare record_verdict", earlyPin.Step)
	}
	if len(earlyPin.VerdictEvidence) != 0 {
		t.Fatalf("verdict_evidence %v must stay empty at a non-verdict step", earlyPin.VerdictEvidence)
	}
}

// seedLanelessResearchItemAtFrame pins a research instance that has only
// framed its question: it sits on a step that does not declare
// record_verdict.
func seedLanelessResearchItemAtFrame(t *testing.T, workID string) *Store {
	t.Helper()
	return seedWorkflowReturnRouteFixtureWithPlan(t, workID, WorkflowStepPlanResearch, "frame", []string{}, nil).store
}

func stepDeclaresActionByName(t *testing.T, s *Store, workID, stepID, actionID string) bool {
	t.Helper()
	plan, err := readWorkflowPlanTx(t.Context(), s.db, workID)
	if err != nil || plan == nil {
		t.Fatalf("read workflow plan: %v", err)
	}
	return slices.Contains(WorkflowStepPlanTemplateActions(plan.Type, stepID), actionID)
}

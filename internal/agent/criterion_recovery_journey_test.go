package agent

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

func TestContractRevisionPreservesExecutionAndLegislativeSubjects(t *testing.T) {
	f := newSpineFixtureForFamily(t, "workflow.break_fix")
	if step := f.read(t).Pinned.Step; step != "implement" {
		t.Fatalf("producing step = %q, want implement", step)
	}
	f.review(t, "pass")
	before := f.read(t)
	if before.Pinned.Step != "review" {
		t.Fatalf("reviewed implementation left step %q, want review", before.Pinned.Step)
	}
	var rawPlan string
	if err := f.s.DatabaseForTesting().QueryRow(`SELECT step_plan FROM workflow_instances WHERE work_id=?`, f.work).Scan(&rawPlan); err != nil {
		t.Fatal(err)
	}
	var plan store.WorkflowStepPlan
	if err := json.Unmarshal([]byte(rawPlan), &plan); err != nil {
		t.Fatal(err)
	}
	reviewed := false
	for _, step := range plan.Steps {
		if step.ID == "implement" && len(step.Attempts) > 0 {
			attempt := step.Attempts[len(step.Attempts)-1]
			reviewed = attempt.Reviewer == "concord-review" && attempt.Result == "pass" && attempt.Commit == f.proofCommit && attempt.Link == "https://example.com/review/spine"
		}
	}
	if !reviewed {
		t.Fatal("implementation review tuple was not recorded before revision")
	}
	contract := before.Pinned.Contract
	if contract == nil {
		t.Fatal("approved contract missing")
	}
	// Workflow engine contract §4.1: unchanged valid proof needs no
	// replacement merely because a contract version was appended.
	const evidenceRows = `SELECT * FROM workflow_evidence_bindings WHERE work_id=?`
	proof := agentRowsSnapshot(t, f.s, evidenceRows, f.work)
	if proof == "[]" {
		t.Fatal("fixture bound no proof before the revision")
	}
	successor := map[string]any{
		"contract_version":     contract.Version + 1,
		"legislative_subjects": contract.LegislativeSubjects,
		"spec_mandate":         contract.SpecMandate,
		"law_modifies":         contract.LawModifies,
		"architecture_binding": workflowArchitectureBindingForSeededRegistry(t, f.s),
	}
	updated, _ := dispatchContractRevisionResult(t, f.s, f.service, f.env, f.work, before.Pinned.Pin.Version, successor, "journey-revise-unchanged")
	spineOK(t, updated)
	after := f.read(t)
	if after.Pinned.Step != before.Pinned.Step || after.Pinned.Contract == nil || after.Pinned.Contract.Version != contract.Version+1 ||
		!reflect.DeepEqual(after.Pinned.Contract.LegislativeSubjects, contract.LegislativeSubjects) || after.Pinned.Contract.Version != contract.Version+1 {
		t.Fatalf("revision lost execution position or contract authority: before=%+v after=%+v", before.Pinned, after.Pinned)
	}
	if agentRowsSnapshot(t, f.s, evidenceRows, f.work) != proof {
		t.Fatal("ordinary revision discarded or rewrote bound proof")
	}
	var revisedPlan string
	if err := f.s.DatabaseForTesting().QueryRow(`SELECT step_plan FROM workflow_instances WHERE work_id=?`, f.work).Scan(&revisedPlan); err != nil {
		t.Fatal(err)
	}
	if revisedPlan != rawPlan {
		t.Fatal("ordinary revision changed the reviewed execution plan")
	}
	spineOK(t, f.action(t, "record_delivery", map[string]any{"delivery_artifact": "artifact:spine-value", "delivery_state": "asserted"}))
	spineOK(t, f.call(t, "concord_work_transition", "workflow_action", map[string]any{
		"work_id": f.work, "expected_version": f.read(t).Pinned.Pin.Version, "action_id": "confirm_premise", "idempotency_key": "revision-journey-confirm",
	}))
	var confirmedVersion int64
	if err := f.s.DatabaseForTesting().QueryRow(`SELECT contract_version FROM workflow_premise_confirmations WHERE work_id=?`, f.work).Scan(&confirmedVersion); err != nil {
		t.Fatal(err)
	}
	if confirmedVersion != after.Pinned.Contract.Version || f.read(t).Pinned.Step != "review" {
		t.Fatalf("confirmation version = %d, want active version %d at review", confirmedVersion, after.Pinned.Contract.Version)
	}
	f.completeReview(t)
	spineOK(t, f.action(t, "complete", map[string]any{"impact_verdict": "non-breaking"}))
	done := f.read(t)
	if done.Pinned.Pin.Lifecycle != "completed" || done.Pinned.Contract == nil || done.Pinned.Contract.Version != confirmedVersion {
		t.Fatalf("completion lost lifecycle or revised contract: %+v", done.Pinned)
	}
}

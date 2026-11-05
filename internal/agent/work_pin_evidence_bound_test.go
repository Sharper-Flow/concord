package agent

import (
	"context"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/store"
)

// New bind_evidence payloads use the shared 2-to-128-byte reference rule.
func wpebLongLocator() string {
	return "https://github.com/Sharper-Flow/concord/actions/runs/15999480341/job/45125907882#step:8:910" +
		"----------------" + "------------------------------------------------------------------------" +
		"------------------------------------------------------------------------" +
		"------------------------------------------------------------------------" +
		"------------------------------------------------------------------------" +
		"------------------------------------------------------------------------"
}

func TestBindEvidenceRefusesLocatorPastReferenceBound(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, service, grant, _ := mutationDispatchFixture(t, []agentcontract.Capability{"work_define", "work_transition"})
	seedCurrentWorkflowDomainFixture(t, s)
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	long := wpebLongLocator()
	if len(long) <= 256 || len(long) > 2048 {
		t.Fatalf("fixture locator is %d bytes, outside the 257..2048 reproduction span", len(long))
	}
	workID := captureCompositionWork(t, ctx, s, service, env, "Work pin locator bound", "bug", "workflow.break_fix", "wpeb-capture")
	compositionAction(t, ctx, s, service, env, workID, "record_intake", map[string]any{"outcome": "proceed", "open_questions_found": false})
	compositionAction(t, ctx, s, service, env, workID, "pick_type", map[string]any{"type": "fix", "reason": "Repair the locator defect."})
	version := agentCompositionWorkVersion(t, s, workID)

	// CD-0198 D2: one typed record_diagnosis carries the three sections the
	// separate reproduction, alignment, and root-cause actions recorded.
	response, _ := ekbDispatch(t, s, service, env, workID, version, "record_diagnosis", map[string]any{
		"reproduction": "The bounded defect reproduction statement.",
		"searched":     "The bounded backlog search statement.",
		"outcome":      "none_found",
		"root_cause":   "The bounded root cause statement.",
	}, nil, "wpeb-diagnosis")
	if response.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("record_diagnosis: %+v", response.Error)
	}
	compositionAction(t, ctx, s, service, env, workID, "complete_step", map[string]any{"step_id": "diagnose", "note": "Diagnosis recorded."})
	compositionRecordPlan(t, ctx, s, service, env, workID)
	after := agentCompositionWorkVersion(t, s, workID)
	response, _ = ekbDispatch(t, s, service, env, workID, after, "approve_contract", workflowContractFieldsForSeededRegistry(t, s), nil, "wpeb-approve")
	if response.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("approve_contract: %+v", response.Error)
	}
	compositionCompletePlanStep(t, ctx, s, service, env, workID)
	after = agentCompositionWorkVersion(t, s, workID)
	bound, _ := ekbDispatch(t, s, service, env, workID, after, "bind_evidence", map[string]any{"evidence_kind": "artifact", "immutable_subject_ref": long, "producer_watermark": "request:fixture"}, nil, "wpeb-bind")
	if bound.Outcome == agentcontract.OutcomeOK || bound.Error == nil || bound.Error.Kind != "invalid_input" {
		t.Fatalf("bind_evidence accepted the %d-byte locator: %+v", len(long), bound.Error)
	}
	var boundEvents int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM workflow_evidence_bindings b JOIN effect_receipts r ON r.event_id=b.binding_id WHERE b.work_id=? AND r.kind=? AND json_extract(b.payload,'$.immutable_subject_ref')=?`, workID, store.WorkflowEvidenceBound, long).Scan(&boundEvents); err != nil {
		t.Fatal(err)
	}
	if boundEvents != 0 {
		t.Fatalf("long locator bound events=%d, want 0", boundEvents)
	}
}

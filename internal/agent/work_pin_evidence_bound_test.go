package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

// A work pin can still echo a historical locator up to 2048 bytes. New
// bind_evidence payloads use the shared 2-to-128-byte reference rule.
func wpebLongLocator() string {
	return "https://github.com/Sharper-Flow/concord/actions/runs/15999480341/job/45125907882#step:8:910" +
		"----------------" + "------------------------------------------------------------------------" +
		"------------------------------------------------------------------------" +
		"------------------------------------------------------------------------" +
		"------------------------------------------------------------------------" +
		"------------------------------------------------------------------------"
}

func TestWorkPinEvidenceAdmitsTheDeclaredLocatorBound(t *testing.T) {
	t.Parallel()
	long := wpebLongLocator()
	if len(long) <= 256 {
		t.Fatalf("fixture locator is %d bytes, the reproduction needs one over 256", len(long))
	}
	if len(long) > 2048 {
		t.Fatalf("fixture locator is %d bytes, past the bind-side bound this defect is about", len(long))
	}
	payload := `{"changed_refs":[{"entity_kind":"work_item","id":"work-1","version":3}],"next_valid_intents":[],"operation_id":"workflow-1","work_pins":[{"work_id":"work-1","title":"t","linear_issue_key":"CON-1240","project_id":"concord","project_display_name":"Concord","version":3,"lifecycle":"in_progress","cancelled_instance_closes":0,"workflow_type":"workflow.break_fix","step":"refine","attempt":null,"pending_operator_decision":null,"driving_sessions":[],"watermark":"seq:1","next_valid_intents":[],"verdict_evidence":[{"evidence_kind":"verification","immutable_subject_ref":"` + long + `"}],"workflow_definition_version":2,"workflow_definition_digest":"sha256:` + strings.Repeat("0", 64) + `","obligations":["verification"]}]}`
	if err := ValidateOperationPayload("concord_work_transition", "workflow_action", []byte(payload), true); err != nil {
		t.Fatalf("work pin echoing a 2048-bound locator refused: %v", err)
	}
}

func TestBindEvidenceRefusesLocatorPastReferenceBound(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, service, grant, privateKey := mutationDispatchFixture(t, []Capability{"work_define", "work_transition"})
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
	version := agentCompositionWorkVersion(t, s, workID)

	// CD-0198 D2: one typed record_diagnosis carries the three sections the
	// separate reproduction, alignment, and root-cause actions recorded.
	response, after := ekbDispatch(t, ctx, s, service, env, grant, privateKey, workID, version, "record_diagnosis", map[string]any{
		"reproduction": "The bounded defect reproduction statement.",
		"searched":     "The bounded backlog search statement.",
		"outcome":      "none_found",
		"root_cause":   "The bounded root cause statement.",
	}, nil, "wpeb-diagnosis")
	if response.Outcome != OutcomeOK {
		t.Fatalf("record_diagnosis: %+v", response.Error)
	}
	response, after = ekbDispatch(t, ctx, s, service, env, grant, privateKey, workID, after, "approve_contract", workflowContractFieldsFixture(), nil, "wpeb-approve")
	if response.Outcome != OutcomeOK {
		t.Fatalf("approve_contract: %+v", response.Error)
	}
	// CD-0198 D3: the qualifying checkpoint claims the derived start.
	response, after = ekbDispatch(t, ctx, s, service, env, grant, privateKey, workID, after, "checkpoint_repair", map[string]any{}, nil, "wpeb-start")
	if response.Outcome != OutcomeOK {
		t.Fatalf("checkpoint_repair: %+v", response.Error)
	}
	bound, _ := ekbDispatch(t, ctx, s, service, env, grant, privateKey, workID, after, "bind_evidence", map[string]any{"evidence_kind": "artifact", "immutable_subject_ref": long}, nil, "wpeb-bind")
	if bound.Outcome == OutcomeOK || bound.Error == nil || bound.Error.Kind != "invalid_input" {
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

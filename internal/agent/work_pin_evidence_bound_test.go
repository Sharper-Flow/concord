package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

// A bind-side locator is declared at 1 to 2048 bytes, and the work pin echoes
// bound evidence verbatim into verdict_evidence. The closed response schema
// must admit the same span: work_pin_evidence.immutable_subject_ref caps at
// 2048 so every value the bind side admits stays representable in the response
// (issue #1240). The echo-bounds audit found work_pin_evidence is the only
// response surface echoing a 2048-bound value; every other evidence echo pairs
// a 128-bound input (workflowList items, ValidReference, reference-typed
// action list items) with the 128-bound reference def, and
// workflow_outcome_check pairs its 256-bound input with its 256-bound echo.
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
	payload := `{"changed_refs":[{"entity_kind":"work_item","id":"work-1","version":3}],"next_valid_intents":[],"operation_id":"workflow-1","work_pins":[{"work_id":"work-1","title":"t","linear_issue_key":"CON-1240","project_id":"concord","project_display_name":"Concord","version":3,"lifecycle":"in_progress","workflow_type":"workflow.break_fix","step":"refine","attempt":null,"pending_operator_decision":null,"driving_sessions":[],"watermark":"seq:1","next_valid_intents":[],"verdict_evidence":[{"evidence_kind":"verification","immutable_subject_ref":"` + long + `"}]}]}`
	if err := ValidateOperationPayload("concord_work_transition", "workflow_action", []byte(payload), true); err != nil {
		t.Fatalf("work pin echoing a 2048-bound locator refused: %v", err)
	}
}

// wpebAssertPinEchoesTheLocator revalidates a workflow_action result against
// the closed schema and requires the work pin's verdict_evidence to carry the
// long locator verbatim. The pin reads verdict_evidence only on a step that
// declares record_verdict, so this holds for responses that leave the work on
// such a step.
func wpebAssertPinEchoesTheLocator(t *testing.T, response Envelope, long string) {
	t.Helper()
	if err := ValidateOperationPayload("concord_work_transition", "workflow_action", response.Result, true); err != nil {
		t.Fatalf("workflow_action result failed closed-schema validation: %v", err)
	}
	var result struct {
		WorkPins []struct {
			WorkID          string `json:"work_id"`
			VerdictEvidence []struct {
				EvidenceKind        string `json:"evidence_kind"`
				ImmutableSubjectRef string `json:"immutable_subject_ref"`
			} `json:"verdict_evidence"`
		} `json:"work_pins"`
	}
	if err := json.Unmarshal(response.Result, &result); err != nil {
		t.Fatalf("workflow_action result is not decodable: %v", err)
	}
	if len(result.WorkPins) != 1 {
		t.Fatalf("workflow_action result carries %d work pins, want 1: %s", len(result.WorkPins), response.Result)
	}
	for _, entry := range result.WorkPins[0].VerdictEvidence {
		if entry.ImmutableSubjectRef == long {
			return
		}
	}
	t.Fatalf("work pin verdict_evidence does not echo the %d-byte locator: %s", len(long), response.Result)
}

// A >256-byte locator bound on the break_fix repair step must survive the full
// mutation path: bind_evidence commits, and the record_delivery that lands on
// verify commits with a schema-valid response whose pin echoes the locator.
// Under a 256-capped echo surface that delivery died as malformed_response and
// rolled back, wedging the workflow at repair.
func TestRecordDeliveryCommitsWithABoundLocatorPastTheEchoBound(t *testing.T) {
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

	response, after := ekbDispatch(t, ctx, s, service, env, grant, privateKey, workID, version, "record_reproduction", map[string]any{}, nil, "wpeb-reproduce")
	if response.Outcome != OutcomeOK {
		t.Fatalf("record_reproduction: %+v", response.Error)
	}
	response, after = ekbDispatch(t, ctx, s, service, env, grant, privateKey, workID, after, "record_root_cause", map[string]any{}, nil, "wpeb-diagnose")
	if response.Outcome != OutcomeOK {
		t.Fatalf("record_root_cause: %+v", response.Error)
	}
	response, after = ekbDispatch(t, ctx, s, service, env, grant, privateKey, workID, after, "approve_contract", workflowContractFieldsFixture(), nil, "wpeb-approve")
	if response.Outcome != OutcomeOK {
		t.Fatalf("approve_contract: %+v", response.Error)
	}
	response, after = ekbDispatch(t, ctx, s, service, env, grant, privateKey, workID, after, "start_repair", map[string]any{}, nil, "wpeb-start")
	if response.Outcome != OutcomeOK {
		t.Fatalf("start_repair: %+v", response.Error)
	}
	bound, after := ekbDispatch(t, ctx, s, service, env, grant, privateKey, workID, after, "bind_evidence", map[string]any{"evidence_kind": "artifact", "immutable_subject_ref": long}, nil, "wpeb-bind")
	if bound.Outcome != OutcomeOK {
		t.Fatalf("bind_evidence with a %d-byte locator: %+v", len(long), bound.Error)
	}

	// record_delivery leaves repair through the mandatory refine pass, and the
	// delivery that lands on verify reads the bound evidence into
	// verdict_evidence. A 256-capped echo surface fails that exact response's
	// closed-schema validation and rolls the delivery back.
	delivered, after := ekbDispatch(t, ctx, s, service, env, grant, privateKey, workID, after, "record_delivery", map[string]any{}, nil, "wpeb-deliver")
	if delivered.Outcome != OutcomeOK {
		t.Fatalf("record_delivery after the long bind: %+v", delivered.Error)
	}
	started, after := ekbDispatch(t, ctx, s, service, env, grant, privateKey, workID, after, "start_refine", map[string]any{}, nil, "wpeb-start-refine")
	if started.Outcome != OutcomeOK {
		t.Fatalf("start_refine: %+v", started.Error)
	}
	verified, _ := ekbDispatch(t, ctx, s, service, env, grant, privateKey, workID, after, "record_delivery", map[string]any{}, nil, "wpeb-deliver-verify")
	if verified.Outcome != OutcomeOK {
		t.Fatalf("record_delivery onto verify: %+v", verified.Error)
	}
	wpebAssertPinEchoesTheLocator(t, verified, long)

	var step string
	if err := s.DatabaseForTesting().QueryRow(`SELECT current_step FROM workflow_instances WHERE work_id=?`, workID).Scan(&step); err != nil {
		t.Fatal(err)
	}
	if step != "verify" {
		t.Fatalf("record_delivery left the step at %q, want verify", step)
	}
	var boundEvents int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_type='work_item' AND subject_id=? AND kind=? AND json_extract(payload,'$.immutable_subject_ref')=?`, workID, store.WorkflowEvidenceBound, long).Scan(&boundEvents); err != nil {
		t.Fatal(err)
	}
	if boundEvents != 1 {
		t.Fatalf("long locator bound events=%d, want 1", boundEvents)
	}
}

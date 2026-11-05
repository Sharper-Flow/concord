package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
)

func TestExternalEvidenceRecordingAndBinding(t *testing.T) {
	t.Parallel()
	s, service, grant, _ := mutationDispatchFixture(t, []agentcontract.Capability{"work_define", "work_transition"})
	scope, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scope)
	work, version := ekbWorkAtExecute(t, context.Background(), s, service, env, "external-route")
	start, version := ekbDispatch(t, s, service, env, work, version, "start_execution", nil, nil, "external-start")
	if start.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("start: %+v", start.Error)
	}
	var plan string
	var contract string
	if err := s.DatabaseForTesting().QueryRow(`SELECT step_plan FROM workflow_instances WHERE work_id=?`, work).Scan(&plan); err != nil {
		t.Fatal(err)
	}
	const contractQuery = `SELECT json_array(contract_version,premise,consequence_class,route_conventions,approved_at,approved_by,spec_mandate,rigor_class,law_modifies,law_boundary_version,self_repair_json,decision_domains,changes_product_truth,legislative_subjects) FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL`
	if err := s.DatabaseForTesting().QueryRow(contractQuery, work).Scan(&contract); err != nil {
		t.Fatal(err)
	}
	locator := "https://github.com/example/project/actions/runs/123"
	evidence := []agentcontract.EvidenceRef{{Kind: "verification", Authority: "github", LocatorKind: "check_run", Locator: locator}}
	recorded, version := ekbDispatch(t, s, service, env, work, version, "checkpoint_execution", nil, evidence, "external-record")
	if recorded.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("checkpoint: %+v", recorded.Error)
	}
	var result struct {
		OperationID string `json:"operation_id"`
	}
	if err := json.Unmarshal(recorded.Result, &result); err != nil || result.OperationID == "" || recorded.RequestID == "" {
		t.Fatalf("recording result=%s request=%q err=%v", recorded.Result, recorded.RequestID, err)
	}
	counts := func() (int, int) {
		t.Helper()
		var operations, receipts int
		if err := s.DatabaseForTesting().QueryRow(`SELECT (SELECT count(*) FROM durable_operations WHERE work_id=?1), (SELECT count(*) FROM effect_receipts WHERE subject_id=?1)`, work).Scan(&operations, &receipts); err != nil {
			t.Fatal(err)
		}
		return operations, receipts
	}
	operations, receipts := counts()
	for _, tc := range []struct{ name, producer, op, watermark, subject, message string }{
		{"fabricated", "github", "https://github.com/example/project/actions/runs/123", "github-run:123", locator, "no durable operation record"},
		{"principal", "github", result.OperationID, recorded.RequestID, locator, "producer_id"},
		{"watermark", env.PrincipalRef, result.OperationID, "wrong-request", locator, "producer_watermark"},
		{"omitted-run", env.PrincipalRef, "", "github-run:123", locator, "producer_watermark"},
		{"locator", env.PrincipalRef, result.OperationID, recorded.RequestID, "https://github.com/example/project/actions/runs/456", "locator mismatch"},
	} {
		fields := map[string]any{"evidence_kind": "verification", "immutable_subject_ref": tc.subject, "producer_id": tc.producer, "producer_run_ref": tc.op, "producer_watermark": tc.watermark}
		if tc.name == "omitted-run" {
			delete(fields, "producer_run_ref")
		}
		refused, after := ekbDispatch(t, s, service, env, work, version, "bind_evidence", fields, nil, "external-refuse-"+tc.name)
		if refused.Outcome != agentcontract.OutcomeError || refused.Error == nil || !strings.Contains(refused.Error.Message, tc.message) || refused.Error.EffectState != "none" || after != version || len(ekbEvidenceBoundEvents(t, s, work)) != 0 {
			t.Fatalf("%s refusal=%+v version=%d want %d", tc.name, refused.Error, after, version)
		}
		if tc.name == "fabricated" || tc.name == "omitted-run" {
			detail, ok := refused.Error.Details["recovery_detail"].(string)
			if !ok {
				t.Fatalf("missing recovery_detail: %+v", refused.Error.Details)
			}
			for _, instruction := range []string{"external locator in evidence on an otherwise admitted workflow action", "operation_id as producer_run_ref", "request_id as producer_watermark", "recording principal as producer_id", "omit producer_id when that principal binds"} {
				if !strings.Contains(detail, instruction) {
					t.Errorf("recovery_detail %q omits %q", detail, instruction)
				}
			}
		}
		if gotOperations, gotReceipts := counts(); gotOperations != operations || gotReceipts != receipts {
			t.Fatalf("%s refusal changed operations %d -> %d or receipts %d -> %d", tc.name, operations, gotOperations, receipts, gotReceipts)
		}
	}
	// The recording response does not show the principal; an omitted
	// producer_id defaults to the binding caller's principal.
	fields := map[string]any{"evidence_kind": "verification", "immutable_subject_ref": locator, "producer_run_ref": result.OperationID, "producer_watermark": recorded.RequestID}
	bound, after := ekbDispatch(t, s, service, env, work, version, "bind_evidence", fields, nil, "external-bind")
	if bound.Outcome != agentcontract.OutcomeOK || after <= version || len(ekbEvidenceBoundEvents(t, s, work)) != 1 {
		t.Fatalf("bind: %+v version=%d", bound.Error, after)
	}
	var afterPlan string
	var afterContract string
	if err := s.DatabaseForTesting().QueryRow(`SELECT step_plan FROM workflow_instances WHERE work_id=?`, work).Scan(&afterPlan); err != nil {
		t.Fatal(err)
	}
	if err := s.DatabaseForTesting().QueryRow(contractQuery, work).Scan(&afterContract); err != nil {
		t.Fatal(err)
	}
	if plan != afterPlan || contract != afterContract {
		t.Fatalf("frontier or contract changed: contract %s -> %s", contract, afterContract)
	}
	var subject, producer, op, watermark string
	if err := s.DatabaseForTesting().QueryRow(`SELECT json_extract(payload,'$.immutable_subject_ref'),json_extract(payload,'$.producer_id'),json_extract(payload,'$.producer_run_ref'),json_extract(payload,'$.producer_watermark') FROM workflow_evidence_bindings WHERE work_id=?`, work).Scan(&subject, &producer, &op, &watermark); err != nil {
		t.Fatal(err)
	}
	if subject != locator || producer != env.PrincipalRef || op != result.OperationID || watermark != recorded.RequestID {
		t.Fatalf("bound unexpected attribution: %q %q %q %q", subject, producer, op, watermark)
	}
}

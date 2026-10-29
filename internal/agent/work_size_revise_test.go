package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/store"
)

func reviseSizeInput(workID string, version int64, key, sizeField string) map[string]any {
	input := map[string]any{"work_id": workID, "expected_version": version, "title": "Sized", "value_statement": "The model sizes captured work", "kind": "task", "reason": "the change touches authentication", "idempotency_key": key}
	var field map[string]any
	if err := json.Unmarshal([]byte("{"+sizeField+"}"), &field); err != nil {
		panic(err)
	}
	for name, value := range field {
		input[name] = value
	}
	return input
}

func dispatchRevise(t *testing.T, s *store.Store, service *Service, env CallEnvelope, input map[string]any) (agentcontract.Envelope, []byte) {
	t.Helper()
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	response, err := Dispatch(context.Background(), s, service, InvokeRequest{Tool: "concord_work_define", Operation: "revise_intent", Input: raw}, env)
	if err != nil {
		t.Fatal(err)
	}
	return response, raw
}

func readWorkSizeSummary(t *testing.T, s *store.Store, service *Service, env CallEnvelope, workID string) *store.WorkSizeSummary {
	t.Helper()
	env.RequestID = "size-read-" + workID
	read, err := Dispatch(context.Background(), s, service, InvokeRequest{Tool: "concord_work_browse", Operation: "scope", Input: json.RawMessage(`{"product_id":"product-1","work_id":"` + workID + `"}`)}, env)
	if err != nil || read.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("scope read = %+v, %v", read, err)
	}
	raw, _ := json.Marshal(read.Result)
	var readback struct {
		Work struct {
			Size *store.WorkSizeSummary `json:"size"`
		} `json:"work"`
	}
	if err := json.Unmarshal(raw, &readback); err != nil {
		t.Fatal(err)
	}
	return readback.Work.Size
}

func TestReviseIntentSizeRevisionsStayOnTheWork(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, service, grant, scopeVersion := indexedCaptureFixture(t)
	captured := dispatchCapture(t, s, service, grant, scopeVersion, "resize-capture", sizedCaptureInput("resize-capture", sizedCaptureInputs))
	if captured.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("sized capture = %+v", captured.Error)
	}
	workID := (*captured.ChangedRefs)[0].ID
	env := mutationEnvelope(grant, scopeVersion)

	// The model's crucial decision stays on the same work item.
	env.RequestID = "resize-rerun"
	rerun, _ := dispatchRevise(t, s, service, env, reviseSizeInput(workID, 4, "resize-rerun", `"size_inputs":{"paths":["README.md"],"security":true,"user_visible":false,"blast_radius":"package","estimated_production_lines":20}`))
	if rerun.Outcome != agentcontract.OutcomeOK || rerun.ChangedRefs == nil || len(*rerun.ChangedRefs) != 1 {
		t.Fatalf("rerun = %+v changed=%+v", rerun.Error, rerun.ChangedRefs)
	}
	refs := *rerun.ChangedRefs
	if refs[0].ID != workID || refs[0].Version != "5" || workVersion(t, s, workID) != 5 {
		t.Fatalf("rerun changed refs = %+v", refs)
	}
	var lifecycle string
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT lifecycle FROM work_items WHERE id=?`, workID).Scan(&lifecycle); err != nil || lifecycle != "needed" {
		t.Fatalf("work lifecycle = %q, %v", lifecycle, err)
	}
	if size := readWorkSizeSummary(t, s, service, env, workID); size == nil || size.Size != store.WorkSizeCrucial || size.ModelSize != store.WorkSizeCrucial {
		t.Fatalf("rerun size = %+v", size)
	}

	if countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM work_items`) != 1 || countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM relations`) != 0 || countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM effect_receipts WHERE kind IN ('work.resized','work.superseded','workflow.successor_linked')`) != 0 {
		t.Fatal("size revision created a successor, relation, or supersession")
	}
}

package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

// These tests hold concord_work_define revise_intent to the size revision
// route (CD-0229 D6): size_inputs reruns the model and operator_size
// records the operator's size behind a consumed approval, both in place.

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

func dispatchRevise(t *testing.T, s *store.Store, service *Service, env CallEnvelope, input map[string]any) (Envelope, []byte) {
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
	if err != nil || read.Outcome != OutcomeOK {
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
	if captured.Outcome != OutcomeOK {
		t.Fatalf("sized capture = %+v", captured.Error)
	}
	workID := (*captured.ChangedRefs)[0].ID
	env := mutationEnvelope(grant, scopeVersion)

	// Both size fields in one revision refuse before any effect.
	env.RequestID = "resize-both"
	both, _ := dispatchRevise(t, s, service, env, reviseSizeInput(workID, 4, "resize-both", `"operator_size":"small",`+sizedCaptureInputs))
	if both.Error == nil || both.Error.Kind != "invalid_input" || workVersion(t, s, workID) != 4 {
		t.Fatalf("operator_size with size_inputs = %+v", both.Error)
	}

	// The model's crucial decision stays on the same work item.
	env.RequestID = "resize-rerun"
	rerun, _ := dispatchRevise(t, s, service, env, reviseSizeInput(workID, 4, "resize-rerun", `"size_inputs":{"paths":["README.md"],"security":true,"user_visible":false,"blast_radius":"package","estimated_production_lines":20}`))
	if rerun.Outcome != OutcomeOK || rerun.ChangedRefs == nil || len(*rerun.ChangedRefs) != 1 {
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
	if size := readWorkSizeSummary(t, s, service, env, workID); size == nil || size.Size != store.WorkSizeCrucial || size.ModelSize != store.WorkSizeCrucial || size.OperatorSize != nil {
		t.Fatalf("rerun size = %+v", size)
	}

	// The operator's size needs a consumed approval; with it, the effective
	// size changes in place.
	input := reviseSizeInput(workID, 5, "resize-override", `"operator_size":"larger"`)
	env.RequestID = "resize-override"
	challenge, _ := dispatchRevise(t, s, service, env, input)
	if challenge.Error == nil || challenge.Error.Kind != "approval_required" {
		t.Fatalf("override without approval = %+v", challenge.Error)
	}
	challengeRef, _ := challenge.Error.Details["approval_ref"].(string)
	if challengeRef == "" || countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM work_size_overrides`) != 0 {
		t.Fatalf("challenge = %+v", challenge.Error.Details)
	}
	approvedInput := cloneWithApproval(t, input, challengeRef)
	approvedRaw, err := json.Marshal(approvedInput)
	if err != nil {
		t.Fatal(err)
	}
	scope := map[string]any{"product_id": "product-1", "product_ids": []string{"product-1"}, "project_ids": []string{"project-1"}, "work_ids": []string{workID}, "scope_version": scopeVersion}
	env.HostApproval = signedHostApproval(nil, challengeRef, mutationDigest("concord_work_define", "revise_intent", env, approvedRaw), scope, map[string]any{"work": int64(5)}, grant.SessionRef, grant.AgentRef, grant.Worktree, fixedTime(), "")
	override, _ := dispatchRevise(t, s, service, env, approvedInput)
	if override.Outcome != OutcomeOK || override.ChangedRefs == nil || len(*override.ChangedRefs) != 1 {
		t.Fatalf("approved override = %+v changed=%+v", override.Error, override.ChangedRefs)
	}
	if (*override.ChangedRefs)[0].ID != workID || (*override.ChangedRefs)[0].Version != "7" || workVersion(t, s, workID) != 7 {
		t.Fatalf("override changed refs = %+v", *override.ChangedRefs)
	}
	larger := store.WorkSizeLarger
	if size := readWorkSizeSummary(t, s, service, env, workID); size == nil || size.Size != larger || size.ModelSize != store.WorkSizeCrucial || size.OperatorSize == nil || *size.OperatorSize != larger {
		t.Fatalf("override size = %+v", size)
	}
	if countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM work_items`) != 1 || countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM relations`) != 0 || countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM domain_events WHERE kind IN ('work.resized','work.superseded','workflow.successor_linked')`) != 0 {
		t.Fatal("size revision created a successor, relation, or supersession")
	}
}

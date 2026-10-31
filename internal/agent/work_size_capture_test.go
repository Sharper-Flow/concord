package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/pm1fixture"
	"github.com/sharper-flow/concord/internal/store"
)

// These tests hold concord_work_define capture to the work-size route
// (CD-0229 D4): size_inputs rides the capture, the store decides the size,
// and every refusal leaves no work item.

const sizedCaptureInputs = `"size_inputs":{"paths":["README.md"],"security":false,"user_visible":false,"blast_radius":"package","estimated_production_lines":20}`

func TestSourceFreeResearchOmitsSizingWithoutDefault(t *testing.T) {
	t.Parallel()
	s, service, grant, scopeVersion := captureTaskFixture(t)
	response := dispatchCapture(t, s, service, grant, scopeVersion, "research-unsized", `{"title":"Research","value_statement":"Record findings without source changes","kind":"research","project_ids":["project-1"],"task":"Investigate only; do not edit production source","idempotency_key":"research-unsized"}`)
	if response.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("capture=%+v", response.Error)
	}
	id := (*response.ChangedRefs)[0].ID
	if n := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM work_size_assessments WHERE work_id='`+id+`'`); n != 0 {
		t.Fatalf("sizing rows=%d", n)
	}
	var ref string
	if err := s.DatabaseForTesting().QueryRowContext(context.Background(), `SELECT definition_ref FROM workflow_instances WHERE work_id=?`, id).Scan(&ref); err != nil {
		t.Fatal(err)
	}
	if ref != "workflow.research" {
		t.Fatalf("workflow=%s", ref)
	}
}

func TestSourceFreeResearchSuppliedSizingStillRefusesMalformedEstimates(t *testing.T) {
	t.Parallel()
	for _, estimate := range []string{"", `,"estimated_production_lines":0`, `,"estimated_production_lines":-1`, `,"estimated_production_lines":100001`} {
		t.Run(estimate, func(t *testing.T) {
			s, service, grant, scopeVersion := captureTaskFixture(t)
			input := fmt.Sprintf(`{"title":"Research","value_statement":"No source changes","kind":"research","project_ids":["project-1"],"idempotency_key":"invalid-research-size","size_inputs":{"behavior":"investigate","security":false,"user_visible":false,"blast_radius":"contained"%s}}`, estimate)
			response, err := dispatchDefectCapture(t, s, service, grant, scopeVersion, "invalid-research-size", input)
			assertInputCorrection(t, response, err, "$.size_inputs.estimated_production_lines")
			if n := workItemCount(t, s); n != 0 {
				t.Fatalf("refusal left %d work items", n)
			}
			if n := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM work_size_assessments`); n != 0 {
				t.Fatalf("refusal left %d size assessments", n)
			}
		})
	}
}

func sizedCaptureInput(key, sizeInputs string) string {
	return `{"title":"Sized","value_statement":"The model sizes captured work","kind":"task","project_ids":["project-1"],"idempotency_key":"` + key + `",` + sizeInputs + `}`
}

func indexedCaptureFixture(t *testing.T) (*store.Store, *Service, Authority, string) {
	t.Helper()
	s, service, grant, _ := captureTaskFixture(t)
	ctx := context.Background()
	home, err := pm1fixture.SeedCommittedProductDomain(ctx, s, "product-1", "project-1", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RebuildKnowledgeIndex(ctx, home); err != nil {
		t.Fatal(err)
	}
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	return s, service, grant, scopeVersion
}

func workItemCount(t *testing.T, s *store.Store) int {
	t.Helper()
	return countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM work_items`)
}

func TestCaptureWithSizeInputsRecordsTheModelSize(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, service, grant, scopeVersion := indexedCaptureFixture(t)
	response := dispatchCapture(t, s, service, grant, scopeVersion, "sized-capture", sizedCaptureInput("sized-capture", sizedCaptureInputs))
	if response.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("sized capture response=%+v", response.Error)
	}
	changed := (*response.ChangedRefs)[0]
	if version := countRows(t, s.DatabaseForTesting(), `SELECT version FROM work_items WHERE id='`+changed.ID+`'`); version != 4 {
		t.Fatalf("work version = %d, want 4: work.sized must not bump it", version)
	}
	var size, inputsJSON string
	if err := s.DatabaseForTesting().QueryRow(`SELECT size_class, inputs_json FROM work_size_assessments WHERE work_id=?`, changed.ID).Scan(&size, &inputsJSON); err != nil {
		t.Fatalf("size record: %v", err)
	}
	var inputs store.WorkSizeInputs
	if err := json.Unmarshal([]byte(inputsJSON), &inputs); err != nil {
		t.Fatal(err)
	}
	// README.md is unmapped, so model v1 sizes the work at least larger.
	if store.WorkSize(size) != store.WorkSizeLarger || inputs.Kind != "task" || inputs.RepositoryCount != 1 || inputs.PathCount != 1 || inputs.UnmappedPathCount != 1 {
		t.Fatalf("size record = %s %+v", size, inputs)
	}

	env := mutationEnvelope(grant, scopeVersion)
	env.RequestID = "sized-capture-scope-read"
	read, err := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_browse", Operation: "scope", Input: json.RawMessage(`{"product_id":"product-1","work_id":"` + changed.ID + `"}`)}, env)
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
	if readback.Work.Size == nil || readback.Work.Size.Size != store.WorkSizeLarger || readback.Work.Size.ModelVersion != store.WorkSizeModelVersion || readback.Work.Size.RecordedAt == "" {
		t.Fatalf("scope read size = %+v in %s", readback.Work.Size, raw)
	}

	unsized := dispatchCapture(t, s, service, grant, scopeVersion, "unsized-capture", `{"title":"Unsized","value_statement":"Unsized work stays unsized","kind":"task","project_ids":["project-1"],"idempotency_key":"unsized-capture"}`)
	if unsized.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("unsized capture response=%+v", unsized.Error)
	}
	if count := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM work_size_assessments WHERE work_id='`+(*unsized.ChangedRefs)[0].ID+`'`); count != 0 {
		t.Fatalf("a capture without size_inputs was sized %d times", count)
	}
}

func TestCaptureSizeRefusalsLeaveNoWork(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		indexed    bool
		sizeInputs string
		// schema marks a refusal the closed payload schema owns; every other
		// row must reach the core and refuse with a typed envelope of kind.
		schema bool
		kind   string
	}{
		{name: "proposed size", indexed: true, schema: true, sizeInputs: `"size_inputs":{"paths":["README.md"],"size":"small","security":false,"user_visible":false,"blast_radius":"package","estimated_production_lines":20}`},
		{name: "undeclared boolean", indexed: true, schema: true, sizeInputs: `"size_inputs":{"paths":["README.md"],"user_visible":false,"blast_radius":"package","estimated_production_lines":20}`},
		{name: "no path or behavior", indexed: true, sizeInputs: `"size_inputs":{"security":false,"user_visible":false,"blast_radius":"package","estimated_production_lines":20}`, kind: "invalid_input"},
		{name: "no knowledge home", sizeInputs: sizedCaptureInputs, kind: "unknown_scope"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var (
				s            *store.Store
				service      *Service
				grant        Authority
				scopeVersion string
			)
			if tc.indexed {
				s, service, grant, scopeVersion = indexedCaptureFixture(t)
			} else {
				s, service, grant, scopeVersion = captureTaskFixture(t)
			}
			response, err := dispatchDefectCapture(t, s, service, grant, scopeVersion, "size-refused", sizedCaptureInput("size-refused", tc.sizeInputs))
			switch {
			case tc.schema:
				assertInputCorrection(t, response, err, "$.size_inputs.")
			case err != nil:
				t.Fatalf("dispatch err=%v, want a typed %s envelope", err, tc.kind)
			case response.Outcome != agentcontract.OutcomeError || response.Error == nil || response.Error.Kind != tc.kind:
				t.Fatalf("response=%+v, want a %s refusal", response.Error, tc.kind)
			}
			if count := workItemCount(t, s); count != 0 {
				t.Fatalf("refused capture left %d work items", count)
			}
		})
	}
}

// A sized capture reads one Product's knowledge home, so Projects that belong
// to two Products refuse before any approval or effect.
func TestCaptureSizeInputsRefusesProjectsOfTwoProducts(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, service, grant := crossProductDispatchFixture(t, []agentcontract.Capability{"work_define", "cross_scope"})
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	before := workItemCount(t, s)
	response := dispatchCapture(t, s, service, grant, scopeVersion, "sized-cross", sizedCaptureInput("sized-cross", sizedCaptureInputs))
	if response.Outcome != agentcontract.OutcomeError || response.Error == nil || response.Error.Kind != "ambiguous_scope" {
		t.Fatalf("cross-product sized capture = %+v", response.Error)
	}
	if after := workItemCount(t, s); after != before {
		t.Fatalf("refused capture changed the work count from %d to %d", before, after)
	}
}

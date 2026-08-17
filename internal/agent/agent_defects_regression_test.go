package agent

import (
	"encoding/json"
	"testing"
)

// TestRegressionDefect1_RelationEnvelopeShapeIsFromToKind drives
// concord_work_trace.relations through Dispatch against a fixture whose target
// work item HAS at least one relation (work-blocked has a depends_on/blocks
// edge to work-prereq) and asserts the envelope outcome is OK and the result's
// edges[0] carries from/to/kind with the expected IDs.
//
// Regression for the pre-fix q8 marshalling of []store.RelationEdge directly
// into the result. The store struct uses source/target JSON tags; the public
// agent envelope (work_relation_graph.edges in
// contracts/agent-tool-surface-payloads.schema.json) requires from/to/kind
// with additionalProperties: false. The pre-fix code produced a result that
// failed the schema's required-field check on any non-empty edge list.
//
// Pre-fix failure mode: Dispatch returns an envelope whose result-validation
// step rejects the envelope ("missing required $.edges[0].from"). Verified
// by stashing the q8 translation and observing Dispatch fail; the fix
// translates each store.RelationEdge into the agent-facing shape before
// building the result map.
func TestRegressionDefect1_RelationEnvelopeShapeIsFromToKind(t *testing.T) {
	s, service, grant, _ := agentJobsPM1Fixture(t)

	env := agentJobsEnvelope(grant, "proj-web", "prod-alpha")
	resp := dispatchRead(t, s, service, InvokeRequest{
		Tool:      "concord_work_trace",
		Operation: "relations",
		Input:     json.RawMessage(`{"work_id":"work-blocked","relation_kinds":["blocks"],"direction":"incoming"}`),
	}, env)

	if resp.Outcome != OutcomeOK {
		t.Fatalf("Dispatch concord_work_trace.relations outcome=%s err=%+v", resp.Outcome, resp.Error)
	}
	var result map[string]any
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("unmarshal relations result: %v", err)
	}
	edges, ok := result["edges"].([]any)
	if !ok {
		t.Fatalf("edges missing or not a slice: %T", result["edges"])
	}
	if len(edges) == 0 {
		t.Fatalf("edges is empty for work-blocked; expected at least one blocks edge")
	}
	first, ok := edges[0].(map[string]any)
	if !ok {
		t.Fatalf("edges[0] is not an object: %T", edges[0])
	}
	// The envelope must use from/to/kind — the public spelling defined by the
	// payload schema. The store struct's source/target spelling is private.
	from, fromOK := first["from"].(string)
	to, toOK := first["to"].(string)
	kind, kindOK := first["kind"].(string)
	if !fromOK || !toOK || !kindOK {
		t.Fatalf("edges[0] missing from/to/kind: got %#v", first)
	}
	// direction=incoming for blocks kind: the stored row is
	// (work_id_from=work-prereq, work_id_to=work-blocked). The envelope
	// carries from=blocker, to=blocked per the work_relation_graph schema.
	if from != "work-prereq" || to != "work-blocked" || kind != "blocks" {
		t.Fatalf("edges[0] wrong ids: from=%q to=%q kind=%q", from, to, kind)
	}
	// The envelope schema forbids additional properties on edges[0]; in
	// particular the old store-source/store-target keys must not leak.
	if _, hasSource := first["source"]; hasSource {
		t.Fatalf("edges[0] leaks store-source key; from/to/kind are the envelope contract")
	}
	if _, hasTarget := first["target"]; hasTarget {
		t.Fatalf("edges[0] leaks store-target key; from/to/kind are the envelope contract")
	}
}

// TestRegressionDefect2_SupersededLifecycleInWorkSummaryValidates asserts that
// a work_summary-bearing result containing a superseded work item validates
// against the public envelope schema. Pre-fix, work_summary.properties.lifecycle
// resolved to a four-value enum (needed/in_progress/completed/cancelled), so
// any Product that had ever superseded a work item produced a result that
// failed validation with "enum mismatch at $.items[N].lifecycle". The fix
// widens the shared #/$defs/lifecycle enum to include 'superseded', while a
// narrower sibling def (#/$defs/lifecycle_transition_target) protects
// transition inputs.
//
// Pre-fix failure mode: ValidateOperationPayload against the work_summary
// result shape returns an enum-mismatch error whenever any item carries
// lifecycle=superseded.
func TestRegressionDefect2_SupersededLifecycleInWorkSummaryValidates(t *testing.T) {
	payload := []byte(`{"items":[{"id":"work-old","kind":"task","title":"Old","lifecycle":"superseded","version":3}],"next_cursor":null}`)
	// work_browse.list returns work_page which is a list of work_summary.
	if err := ValidateOperationPayload("concord_work_browse", "list", payload, true); err != nil {
		t.Fatalf("superseded lifecycle in work_summary must validate, got %v", err)
	}
}

// TestRegressionDefect2_TransitionTargetRejectsSuperseded asserts that the
// structural split between #/$defs/lifecycle (observed) and
// #/$defs/lifecycle_transition_target (four agent-requestable targets) is
// honored by the work_transition_lifecycle_input schema. Without this test,
// a future widening of #/$defs/lifecycle alone would silently permit an
// agent to request a direct transition to "superseded", violating
// AJ5-atomic-supersession's atomicity requirement.
//
// Pre-fix failure mode (after only widening lifecycle, without the new def):
// ValidateOperationPayload for concord_work_transition.lifecycle would accept
// target="superseded", violating the AJ5 invariant that supersession is
// atomic with its supersedes relation. The split restores the structural
// guarantee; the runtime guard in mutations.go remains as defense-in-depth.
func TestRegressionDefect2_TransitionTargetRejectsSuperseded(t *testing.T) {
	payload := []byte(`{"work_id":"work-x","expected_version":1,"target":"superseded","reason":"probe","idempotency_key":"probe-superseded"}`)
	err := ValidateOperationPayload("concord_work_transition", "lifecycle", payload, false)
	if err == nil {
		t.Fatalf("work_transition_lifecycle_input with target=superseded must be rejected by the schema; the structural guarantee forbids direct transitions")
	}
	// The remaining four targets must still validate.
	for _, target := range []string{"needed", "in_progress", "completed", "cancelled"} {
		ok := []byte(`{"work_id":"work-x","expected_version":1,"target":"` + target + `","reason":"probe","idempotency_key":"probe-` + target + `"}`)
		if err := ValidateOperationPayload("concord_work_transition", "lifecycle", ok, false); err != nil {
			t.Fatalf("work_transition_lifecycle_input with target=%s must validate, got %v", target, err)
		}
	}
}

// TestRegressionDefect2_LifecycleFilterAcceptsSuperseded asserts that the
// work_browse_list_input filter (a third reference to #/$defs/lifecycle) also
// accepts lifecycle=superseded, so a Product that has ever superseded work
// can still be filtered by that state. This guards against an accidental
// re-narrowing of the shared def that would re-break defect 2 only on the
// read-filter side.
func TestRegressionDefect2_LifecycleFilterAcceptsSuperseded(t *testing.T) {
	payload := []byte(`{"page":{"cursor":null,"limit":20},"lifecycle":"superseded"}`)
	if err := ValidateOperationPayload("concord_work_browse", "list", payload, false); err != nil {
		t.Fatalf("work_browse_list_input with lifecycle=superseded must validate, got %v", err)
	}
}

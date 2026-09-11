package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

// CD-0041 D7 names the consequential action classes the architecture preflight
// guards, and states that read-only inspection remains available. An operation
// outside those classes must stay reachable while an unresolved Domain overlap
// stands, or a blocked item cannot record why it is blocked.
//
// The external variant of observation_record is the deliberate exception:
// observation_law_revision_boundary_test.go establishes that it accepts an
// attributed merge or ship result, which D7 does name.

// dispatchUnderOverlap runs one mutation against the seeded overlap fixture and
// returns the refusal kind, or the empty string when the call was admitted.
func dispatchUnderOverlap(t *testing.T, tool, operation string, input map[string]any) string {
	t.Helper()
	s, service, _, _, env, _ := seedAgentOverlapFixtureWith(t, []Capability{"work_relate", "work_define"})
	if err := store.InspectWorkflowDomainOverlap(context.Background(), s, "work-1"); err == nil {
		t.Fatal("precondition: the fixture reports no unresolved overlap on work-1")
	}
	raw, _ := json.Marshal(input)
	response, err := Dispatch(context.Background(), s, service, InvokeRequest{Tool: tool, Operation: operation, Input: raw}, env)
	if err != nil {
		t.Fatal(err)
	}
	if response.Error == nil {
		return ""
	}
	return response.Error.Kind
}

// TestOverlapGuardAdmitsOperationsOutsideD7Classes proves the guard no longer
// refuses operations CD-0041 D7 never named. Each call may still fail a later
// gate; what it must not return is domain_overlap.
func TestOverlapGuardAdmitsOperationsOutsideD7Classes(t *testing.T) {
	for _, probe := range []struct {
		name      string
		tool      string
		operation string
		input     map[string]any
	}{
		{
			name:      "mid-execution observation",
			tool:      "concord_work_define",
			operation: "observation_record",
			input: map[string]any{
				"work_id":         "work-1",
				"idempotency_key": "overlap-scope-observation",
				"observation_id":  "obs:0123456789abcdef",
				"statement":       "the overlap gate refuses every transition on this item",
			},
		},
		{
			name:      "peer message",
			tool:      "concord_work_relate",
			operation: "message_send",
			input: map[string]any{
				"work_id":          "work-1",
				"body":             "this item is blocked behind an unresolved Domain overlap",
				"expected_version": 2,
				"idempotency_key":  "overlap-scope-message",
			},
		},
	} {
		t.Run(probe.name, func(t *testing.T) {
			if kind := dispatchUnderOverlap(t, probe.tool, probe.operation, probe.input); kind == "domain_overlap" {
				t.Fatalf("%s.%s refused with domain_overlap, but CD-0041 D7 names no class that covers it", probe.tool, probe.operation)
			}
		})
	}
}

// TestOverlapGuardStillRefusesNamedD7Classes is the control. It proves the
// admission above narrowed the guard rather than removed it.
func TestOverlapGuardStillRefusesNamedD7Classes(t *testing.T) {
	external := map[string]any{
		"work_id":         "work-1",
		"idempotency_key": "overlap-scope-external",
		"external": map[string]any{
			"kind":           "capture",
			"observation_id": "xobs:0123456789abcdef",
			"subject_kind":   "git_position",
			"subject_ref":    "git:refs/heads/main",
			"captured_at":    "2026-08-08T11:55:00Z",
			"observed_universe": map[string]any{
				"shape":                  "item",
				"applied_scope":          "refs/heads/main at origin",
				"coverage":               "complete",
				"total_kind":             "eq",
				"total_value":            1,
				"canonical_identity_key": "ref_name",
			},
		},
	}
	if kind := dispatchUnderOverlap(t, "concord_work_define", "observation_record", external); kind != "domain_overlap" {
		t.Fatalf("external observation refusal kind=%q, want domain_overlap: it accepts a merge or ship result, which CD-0041 D7 names", kind)
	}
}

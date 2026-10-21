package store_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

func TestRetryWallV2StepAtHistoricalEscalation(t *testing.T) {
	for _, step := range []string{"repair", "refine"} {
		for _, count := range []int64{0, 3} {
			decision := store.RetryWallV2AdmissionFixture(step, count)
			if !decision.ConvergenceRequired || decision.Admitted || decision.Failure == nil {
				t.Fatalf("diagnostic changed dispatch admission: %+v", decision)
			}
			_, encoded := invokeRefusal(t, decision.Failure)
			var wire struct {
				Error struct {
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := json.Unmarshal(encoded, &wire); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(wire.Error.Message, "step "+step) || !strings.Contains(wire.Error.Message, "historical") {
				t.Errorf("refusal hides current step or historical escalation at active count %d: %s", count, encoded)
			}
		}
	}
}

func TestRetryWallV2SupersessionPrerequisites(t *testing.T) {
	decision := store.RetryWallV2AdmissionFixture("repair", 3)
	if !decision.ConvergenceRequired || decision.Failure == nil {
		t.Fatalf("expected a convergence refusal: %+v", decision)
	}
	out, encoded := invokeRefusal(t, decision.Failure)
	remedy, _ := out.Error.Details["recovery_detail"].(string)
	for _, want := range []string{
		"supersede_contract", "one fresh fenced attempt", "does not renew the three-attempt budget",
		"audit_evidence", "contract_version", "host execution failure",
	} {
		if !strings.Contains(remedy, want) {
			t.Errorf("refusal omits supersession prerequisite or retained constraint %q: %s", want, encoded)
		}
	}
}

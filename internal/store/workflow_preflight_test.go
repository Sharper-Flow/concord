package store

import (
	"encoding/json"
	"strings"
	"testing"
)

// The required-field scan must report the first declared missing field, in the
// registry's declaration order. A map walk names whichever field the runtime
// yields first, so the same empty payload reports a different field per run
// and a caller cannot tell which field to correct first.
func TestMissingRequiredFieldNamesFirstDeclaredMissingField(t *testing.T) {
	d, err := BuiltinWorkflowDefinitionForRef("workflow.break_fix")
	if err != nil {
		t.Fatal(err)
	}
	// checkpoint_context declares eight required fields; active_unit is first.
	for i := 0; i < 100; i++ {
		failure := validateWorkflowActionPayload(d.Definition, "checkpoint_context", json.RawMessage(`{}`))
		if failure == nil {
			t.Fatal("REPRO: an empty checkpoint_context payload passed the required-field scan")
		}
		if !strings.Contains(failure.Error(), `"active_unit"`) {
			t.Fatalf("REPRO: iteration %d named a non-first declared field: %v", i, failure)
		}
	}

	// A payload that carries the first declared field must name the next one.
	failure := validateWorkflowActionPayload(d.Definition, "checkpoint_context", json.RawMessage(`{"active_unit":"registry scan"}`))
	if failure == nil || !strings.Contains(failure.Error(), `"hypothesis"`) {
		t.Fatalf("partial payload did not name the next declared missing field: %v", failure)
	}
}

package store

import (
	"encoding/json"
	"testing"
)

func TestHistoricalReproDeclaredSchema(t *testing.T) {
	if _, err := validateWorkflowPlanActionPayload("record_review", json.RawMessage(`{"review":{}}`)); err == nil {
		t.Error("REPRO: preflight accepted an object that violates its declared schema")
	}
}

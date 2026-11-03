package store

import (
	"encoding/json"
	"testing"
)

// The fixture compiles at the pinned baseline and at HEAD through the
// historicalWorkflowPayload adapter each revision provides.
func TestHistoricalReproDeclaredSchema(t *testing.T) {
	if err := historicalWorkflowPayload("record_review", json.RawMessage(`{"review":{}}`)); err == nil {
		t.Error("REPRO: preflight accepted an object that violates its declared schema")
	}
}

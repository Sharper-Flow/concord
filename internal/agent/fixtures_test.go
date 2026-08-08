package agent

import (
	"encoding/json"
	"os"
	"testing"
)

func TestGeneratedPayloadFixturesAreConsumedByGoValidator(t *testing.T) {
	data, err := os.ReadFile("../../contracts/agent-tool-surface.fixtures.json")
	if err != nil {
		t.Fatal(err)
	}
	var corpus struct {
		ManifestDigest string `json:"manifest_digest"`
		Fixtures       []struct {
			InputSchema        string            `json:"input_schema"`
			InputValid         json.RawMessage   `json:"input_valid"`
			InputInvalidCases  []json.RawMessage `json:"input_invalid_cases"`
			ResultSchema       string            `json:"result_schema"`
			ResultValid        json.RawMessage   `json:"result_valid"`
			ResultInvalidCases []json.RawMessage `json:"result_invalid_cases"`
		} `json:"fixtures"`
	}
	if err := json.Unmarshal(data, &corpus); err != nil {
		t.Fatal(err)
	}
	if corpus.ManifestDigest != ManifestDigest || len(corpus.Fixtures) != len(ContractOperations) {
		t.Fatalf("fixture corpus drift: digest=%s fixtures=%d", corpus.ManifestDigest, len(corpus.Fixtures))
	}
	for _, fixture := range corpus.Fixtures {
		if err := ValidatePayloadSchema(fixture.InputSchema, fixture.InputValid); err != nil {
			t.Errorf("valid input %s rejected: %v", fixture.InputSchema, err)
		}
		for _, invalid := range fixture.InputInvalidCases {
			if err := ValidatePayloadSchema(fixture.InputSchema, invalid); err == nil {
				t.Errorf("invalid input %s accepted", fixture.InputSchema)
			}
		}
		if err := ValidatePayloadSchema(fixture.ResultSchema, fixture.ResultValid); err != nil {
			t.Errorf("valid result %s rejected: %v", fixture.ResultSchema, err)
		}
		for _, invalid := range fixture.ResultInvalidCases {
			if err := ValidatePayloadSchema(fixture.ResultSchema, invalid); err == nil {
				t.Errorf("invalid result %s accepted", fixture.ResultSchema)
			}
		}
	}
}

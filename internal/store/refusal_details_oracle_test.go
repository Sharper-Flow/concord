package store_test

import (
	"encoding/json"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

func TestRefusalRemedyIsPreserved(t *testing.T) {
	const remedy = "provide the required verification receipt and repeat the request"
	out, encoded := invokeRefusal(t, &store.Failure{
		Kind: store.KindMissingEvidence, Detail: "verification receipt is missing",
		RecoveryAction: remedy,
	})
	if out.Error.Details["recovery_detail"] != remedy {
		t.Fatalf("refusal lost the authored remedy: %s", encoded)
	}
	var wire struct {
		Error struct {
			Details struct {
				Remedy string `json:"recovery_detail"`
			} `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatal(err)
	}
	if wire.Error.Details.Remedy != remedy {
		t.Fatalf("model-visible details lost a diagnostic: %s", encoded)
	}
}

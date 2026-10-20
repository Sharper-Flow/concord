package store_test

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

func TestRefusalRemedyAndAdmittedKindsCoexist(t *testing.T) {
	const remedy = "choose one of the admitted event kinds and repeat the read"
	admitted := []string{"work.created", "work.completed"}
	out, encoded := invokeRefusal(t, &store.Failure{
		Kind: store.KindMissingEvidence, Detail: "event kind is not admitted",
		RecoveryAction: remedy, AdmittedEventKinds: admitted,
	})
	if out.Error.Details["recovery_detail"] != remedy {
		t.Fatalf("admitted kinds replaced the authored remedy: %s", encoded)
	}
	var wire struct {
		Error struct {
			Details struct {
				Remedy string   `json:"recovery_detail"`
				Kinds  []string `json:"admitted_event_kinds"`
			} `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatal(err)
	}
	if wire.Error.Details.Remedy != remedy || !slices.Equal(wire.Error.Details.Kinds, admitted) {
		t.Fatalf("model-visible details lost a diagnostic: %s", encoded)
	}
}

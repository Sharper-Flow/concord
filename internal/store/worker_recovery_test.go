package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
)

func TestValidateRecoveryPacket(t *testing.T) {
	original := json.RawMessage(`{"work_id":"work-original","attempt_id":"attempt-original","inputs":{"task":"original task"}}`)
	canonical, err := canonicalJSON(original)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(canonical)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	for _, test := range []struct {
		name, packet, digest string
		valid                bool
	}{
		{"original", string(original), digest, true},
		{"reordered", ` { "inputs": { "task": "original task" }, "attempt_id": "attempt-original", "work_id": "work-original" } `, digest, true},
		{"task changed", `{"work_id":"work-original","attempt_id":"attempt-original","inputs":{"task":"replacement task"}}`, digest, false},
		{"work changed", `{"work_id":"another-work","attempt_id":"attempt-original","inputs":{"task":"original task"}}`, digest, false},
		{"attempt changed", `{"work_id":"work-original","attempt_id":"another-attempt","inputs":{"task":"original task"}}`, digest, false},
		{"missing digest", string(original), "", false},
		{"wrong digest", string(original), "sha256:" + hex.EncodeToString(make([]byte, 32)), false},
		{"invalid JSON", `{`, digest, false},
		{"nonobject", `[]`, digest, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateRecoveryPacket(json.RawMessage(test.packet), test.digest, "work-original", "attempt-original")
			if (err == nil) != test.valid {
				t.Fatalf("valid=%v error=%v", test.valid, err)
			}
		})
	}
}

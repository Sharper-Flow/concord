package store

import (
	"encoding/json"
	"testing"
)

// TestMultibyteStringBoundsKeepCoreAndPublicationAligned pins the repaired
// representation contract between core string bounds and the published
// schema. Core validation counts UTF-8 BYTES (validateWorkflowPayloadValue's
// len(text)); the published standard minLength/maxLength count UNICODE CODE
// POINTS, so they are DERIVED from the enforcing byte bounds — maxLength
// equals the byte maximum and minLength is ceil(byte minimum / 4) — and can
// never refuse a core-admitted string, while x-maxBytes/x-minBytes carry the
// enforcing byte bounds the byte-aware validators apply. Both divergence
// directions are asserted on real published fields with boundary fixtures:
// a two-byte one-code-point premise the core admits, a one-byte string under
// the byte floor the core refuses, a max-byte ASCII string at both limits,
// and a max-code-point all-two-byte string whose bytes double the maximum
// (CON-412).

func mustJSONRaw(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

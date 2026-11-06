package store

import (
	"encoding/json"
	"strings"
	"testing"
)

// recordVerdictParityResolve resolves one #/$defs reference, or returns the
// node itself when it is not a reference.
func recordVerdictParityResolve(defs map[string]any, node map[string]any) map[string]any {
	if node == nil {
		return nil
	}
	ref, ok := node["$ref"].(string)
	if !ok || !strings.HasPrefix(ref, "#/$defs/") {
		return node
	}
	if target, ok := defs[strings.TrimPrefix(ref, "#/$defs/")].(map[string]any); ok {
		return target
	}
	return node
}

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
// TestMultibyteStringBoundsKeepCoreAndPublicationAligned retired by CON-1710
// under CD-0243 D3: it asserted the premise field's published byte bounds,
// and the premise field is retired with migration 190.

func mustJSONRaw(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

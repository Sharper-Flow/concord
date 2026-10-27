package payloadschema_test

import (
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/payloadschema"
)

func TestValidateEnvelope(t *testing.T) {
	t.Parallel()
	core := `{
		"schema_version":"1.0",
		"manifest_digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"request_id":"envelope-schema",
		"origin":"core",
		"tool":"concord_work_transition",
		"operation":"lifecycle",
		"outcome":"error",
		"resolved_scope":null,
		"authority":"authoritative",
		"freshness":null,
		"source_version_watermark":[],
		"ordering_keys":[],
		"next_cursor":null,
		"omissions":[],
		"warnings":[],
		"evidence_refs":[],
		"replayed":false,
		"error":{"kind":"invalid_input","retry_safe":false,"recovery_action":{"kind":"reread_entities"},"effect_state":"none"}
	}`
	adapter := strings.NewReplacer(
		`"origin":"core"`, `"origin":"adapter"`,
		`"authority":"authoritative"`, `"authority":"unreachable"`,
		`"kind":"reread_entities"`, `"kind":"correct_request"`,
		`"effect_state":"none"`, `"effect_state":"none","adapter_reason":"invalid_tool_input"`,
	).Replace(core)
	for _, test := range []struct {
		name string
		raw  string
		ok   bool
		text string
	}{
		{name: "core", raw: core, ok: true},
		{name: "adapter", raw: adapter, ok: true},
		{name: "whitespace", raw: core + " \n\t", ok: true},
		{name: "large number", raw: strings.Replace(core, `"effect_state":"none"`, `"effect_state":"none","details":{"large":1e10000}`, 1), ok: true},
		{name: "duplicate member keeps last value", raw: `{"schema_version":"invalid",` + strings.TrimPrefix(core, "{"), ok: true},
		{name: "duplicate nested member keeps last value", raw: strings.Replace(core, `"kind":"invalid_input"`, `"kind":"unknown","kind":"invalid_input"`, 1), ok: true},
		{name: "unknown root member", raw: strings.Replace(core, `"replayed":false`, `"replayed":false,"unknown":true`, 1)},
		{name: "unknown nested member", raw: strings.Replace(core, `"effect_state":"none"`, `"effect_state":"none","unknown":true`, 1)},
		{name: "unknown variant", raw: strings.Replace(core, `"outcome":"error"`, `"outcome":"unknown"`, 1)},
		{name: "missing adapter reason", raw: strings.Replace(adapter, `,"adapter_reason":"invalid_tool_input"`, "", 1)},
		{name: "array", raw: `[]`},
		{name: "null", raw: `null`},
		{name: "malformed", raw: `{`},
		{name: "empty", raw: ""},
		{name: "trailing value", raw: core + ` true`, text: "trailing JSON"},
		{name: "malformed trailing data", raw: core + ` {`, text: "trailing JSON"},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := payloadschema.ValidateEnvelope([]byte(test.raw))
			if (err == nil) != test.ok {
				t.Fatalf("ValidateEnvelope() = %v; want valid=%t", err, test.ok)
			}
			if test.text != "" && (err == nil || err.Error() != test.text) {
				t.Fatalf("ValidateEnvelope() = %v; want %q", err, test.text)
			}
		})
	}
}

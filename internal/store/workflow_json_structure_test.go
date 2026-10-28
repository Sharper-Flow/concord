package store

import (
	"strings"
	"testing"
)

func TestWorkflowJSONStructureBoundsAndRefusals(t *testing.T) {
	nested := func(depth int) string {
		return strings.Repeat("[", depth) + strings.Repeat("]", depth)
	}
	members := func(n int) string {
		fields := make([]string, n)
		for i := range fields {
			fields[i] = `"f` + strings.Repeat("x", i) + `":0`
		}
		return "{" + strings.Join(fields, ",") + "}"
	}
	items := func(n int) string {
		return "[" + strings.TrimSuffix(strings.Repeat("0,", n), ",") + "]"
	}
	cases := []struct {
		name, input, refusal string
	}{
		{"scalar", `"x"`, ""},
		{"nested object and array", `{"a":[1,{"b":null}],"c":{"d":true}}`, ""},
		{"empty containers", `{"a":{},"b":[]}`, ""},
		{"value at the deepest admitted depth", nested(maxWorkflowJSONDepth + 1), ""},
		{"container past the depth bound", nested(maxWorkflowJSONDepth + 2), "duplicate keys, invalid nesting"},
		{"scalar past the depth bound", strings.Repeat("[", maxWorkflowJSONDepth+1) + "0" + strings.Repeat("]", maxWorkflowJSONDepth+1), "duplicate keys, invalid nesting"},
		{"object at the entry bound", members(maxWorkflowJSONContainerEntries), ""},
		{"object past the entry bound", members(maxWorkflowJSONContainerEntries + 1), "duplicate keys, invalid nesting"},
		{"array at the entry bound", items(maxWorkflowJSONContainerEntries), ""},
		{"array past the entry bound", items(maxWorkflowJSONContainerEntries + 1), "duplicate keys, invalid nesting"},
		{"duplicate key", `{"a":1,"a":2}`, "duplicate keys, invalid nesting"},
		{"nested duplicate key", `[{"a":{"b":1,"b":1}}]`, "duplicate keys, invalid nesting"},
		{"same key in sibling objects", `[{"a":1},{"a":2}]`, ""},
		{"object closed as an array", `{"a":1]`, "duplicate keys, invalid nesting"},
		{"array closed as an object", `[1}`, "duplicate keys, invalid nesting"},
		{"empty object closed as an array", `{]`, "duplicate keys, invalid nesting"},
		{"empty array closed as an object", `[}`, "duplicate keys, invalid nesting"},
		{"unclosed container", `{"a":[1`, "duplicate keys, invalid nesting"},
		{"empty input", ``, "duplicate keys, invalid nesting"},
		{"out-of-range number", `[1e400]`, "duplicate keys, invalid nesting"},
		{"trailing value", `{"a":1} {}`, "trailing data"},
	}
	for _, tc := range cases {
		err := validateWorkflowJSONStructure([]byte(tc.input))
		switch {
		case tc.refusal == "" && err != nil:
			t.Errorf("%s: unexpected refusal %v", tc.name, err)
		case tc.refusal != "" && (err == nil || !strings.Contains(err.Error(), tc.refusal)):
			t.Errorf("%s: got %v, want a refusal containing %q", tc.name, err, tc.refusal)
		}
	}
}

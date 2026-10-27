package payloadschema_test

import (
	"encoding/json"
	"testing"

	"github.com/sharper-flow/concord/internal/payloadschema"
)

func TestValidateUniqueJSON(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name    string
		input   string
		invalid bool
		message string
	}{
		{name: "nested", input: `{"a":{"b":1},"c":[{"a":2},{"a":3}]}`},
		{name: "empty containers", input: `{"a":{},"b":[]}`},
		{name: "large number", input: `{"n":1e10000}`},
		{name: "null", input: `null`},
		{name: "boolean", input: `false`},
		{name: "string", input: `"value"`},
		{name: "trailing whitespace", input: "{} \n\t"},
		{name: "duplicate", input: `{"a":1,"a":2}`, invalid: true, message: `duplicate JSON object field "a"`},
		{name: "escaped duplicate", input: `{"a":1,"\u0061":2}`, invalid: true, message: `duplicate JSON object field "a"`},
		{name: "nested duplicate", input: `{"x":{"a":1,"a":2}}`, invalid: true, message: `duplicate JSON object field "a"`},
		{name: "array duplicate", input: `[{"a":1,"a":2}]`, invalid: true, message: `duplicate JSON object field "a"`},
		{name: "trailing value", input: `{} []`, invalid: true, message: "trailing JSON"},
		{name: "malformed trailing data", input: `{} garbage`, invalid: true, message: "trailing JSON"},
		{name: "empty", invalid: true, message: "EOF"},
		{name: "unfinished object", input: `{`, invalid: true},
		{name: "unfinished array", input: `[1,`, invalid: true},
		{name: "mismatched delimiter", input: `{"a":]}`, invalid: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := payloadschema.ValidateUniqueJSON([]byte(test.input))
			if (err != nil) != test.invalid {
				t.Fatalf("ValidateUniqueJSON(%q) = %v, invalid=%v", test.input, err, test.invalid)
			}
			if test.message != "" && err.Error() != test.message {
				t.Fatalf("error = %q, want %q", err, test.message)
			}
		})
	}
}

func FuzzValidateUniqueJSON(f *testing.F) {
	f.Add("key", "value")
	f.Add("\x00\"", "line\nbreak")
	f.Add("é", `\u0061`)
	f.Fuzz(func(t *testing.T, key, value string) {
		encodedKey, err := json.Marshal(key)
		if err != nil {
			t.Fatal(err)
		}
		encodedValue, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		member := string(encodedKey) + ":" + string(encodedValue)
		object := "{" + member + "}"
		for _, input := range []string{object, "[" + object + "," + object + "]"} {
			if err := payloadschema.ValidateUniqueJSON([]byte(input)); err != nil {
				t.Fatalf("unique members rejected: %s: %v", input, err)
			}
		}
		if err := payloadschema.ValidateUniqueJSON([]byte("{" + member + "," + member + "}")); err == nil {
			t.Fatal("duplicate member accepted")
		}
	})
}

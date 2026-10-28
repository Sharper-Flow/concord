package main

import (
	"bytes"
	"testing"
)

func TestCiWaitIngress(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want string
	}{
		{"empty", "", "concord ci-wait: EOF\n"},
		{"malformed", `{`, "concord ci-wait: unexpected EOF\n"},
		{"unknown field", `{"unexpected":true}`, "concord ci-wait: json: unknown field \"unexpected\"\n"},
		{"unknown selector field", `{"selector":{"unexpected":true}}`, "concord ci-wait: json: unknown field \"unexpected\"\n"},
		{"trailing object", `{} {}`, "concord ci-wait: trailing JSON\n"},
		{"trailing malformed", `{} x`, "concord ci-wait: trailing JSON\n"},
		{"repo type", `{"repo":42}`, "concord ci-wait: json: cannot unmarshal number into Go struct field ciWaitRequest.repo of type string\n"},
		{"selector kind type", `{"selector":{"kind":42}}`, "concord ci-wait: json: cannot unmarshal number into Go struct field ciWaitSelector.selector.kind of type string\n"},
		{"budget type", `{"time_seconds_max":"zero"}`, "concord ci-wait: json: cannot unmarshal string into Go struct field ciWaitRequest.time_seconds_max of type int\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			if code := runCiWait([]byte(tc.body), &out, &errOut); code != 1 {
				t.Errorf("exit=%d want 1", code)
			}
			if out.Len() != 0 {
				t.Errorf("stdout=%q want empty", out.String())
			}
			if got := errOut.String(); got != tc.want {
				t.Errorf("stderr=%q want %q", got, tc.want)
			}
		})
	}
}

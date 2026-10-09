package main

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

func TestCLIInputRefusalExitIsPreEffect(t *testing.T) {
	for _, raw := range []string{`{}`, `{"call_envelope":{}}`, `{"call_envelope":false}`} {
		t.Run(raw, func(t *testing.T) {
			var out, errOut bytes.Buffer
			if code := runJSONCommand("invoke", nil, strings.NewReader(raw), &out, &errOut); code != 64 {
				t.Fatalf("input refusal exit = %d, want 64; stderr = %s", code, &errOut)
			}
			if out.Len() != 0 || !strings.Contains(errOut.String(), "required field call_envelope") {
				t.Fatalf("input refusal stdout = %q, stderr = %q", &out, &errOut)
			}
		})
	}
	var out, errOut bytes.Buffer
	called := false
	code := runStoreFreeJSONCommand("invoke", nil, strings.NewReader(`{}`), &out, &errOut, func([]byte, io.Writer, io.Writer) int { called = true; return 0 })
	if code != 64 || called {
		t.Fatalf("store-free input refusal exit = %d, handler called = %t", code, called)
	}
}

func TestInvokeDecodeRefusalExitIsPreEffect(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := runInvoke([]byte(`not-json`), nil, nil, &out, &errOut); code != 64 || out.Len() != 0 {
		t.Fatalf("decode refusal exit = %d, stdout = %q, stderr = %q", code, &out, &errOut)
	}
}

func TestValidateRequiredCommandFieldsRejectsMissingNestedField(t *testing.T) {
	err := validateRequiredCommandFields("predecessor-import", []byte(`{"snapshot_path":"snapshot.json","projects":[],"select_change_ids":[],"product":{}}`))
	if err == nil || !strings.Contains(err.Error(), "product.product_id") {
		t.Fatalf("missing nested field error = %v", err)
	}
}

func TestValidateRequiredCommandFieldsRejectsNonObjectParent(t *testing.T) {
	err := validateRequiredCommandFields("predecessor-import", []byte(`{"snapshot_path":"snapshot.json","projects":[],"select_change_ids":[],"product":"not-an-object"}`))
	if err == nil || !strings.Contains(err.Error(), "product must be an object") {
		t.Fatalf("non-object parent error = %v", err)
	}
}

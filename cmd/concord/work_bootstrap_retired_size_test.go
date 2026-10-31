package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestWorkBootstrapRejectsRetiredSizeInputs(t *testing.T) {
	var out, diagnostics bytes.Buffer
	raw := []byte(`{"product_id":"p","project_id":"p","title":"t","value_statement":"v","kind":"task","task":"t","idempotency_key":"k","size_inputs":{"behavior":"change","security":false,"user_visible":false,"blast_radius":"contained","estimated_production_lines":20}}`)
	if code := runWorkBootstrap(raw, nil, &out, &diagnostics); code != workBootstrapRefusalExit {
		t.Fatalf("exit = %d, diagnostics = %q", code, diagnostics.String())
	}
	if out.Len() != 0 || !strings.Contains(diagnostics.String(), "size_inputs") {
		t.Fatalf("stdout = %q, diagnostics = %q", out.String(), diagnostics.String())
	}
}

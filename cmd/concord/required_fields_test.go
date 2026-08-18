package main

import (
	"strings"
	"testing"
)

func TestValidateRequiredCommandFieldsRejectsMissingNestedField(t *testing.T) {
	err := validateRequiredCommandFields("grant", []byte(`{"assertion":{},"expires_at":"2026-08-18T00:00:00Z","max_uses":1}`))
	if err == nil || !strings.Contains(err.Error(), "assertion.client_ref") {
		t.Fatalf("missing nested field error = %v", err)
	}
}

func TestValidateRequiredCommandFieldsRejectsNonObjectParent(t *testing.T) {
	err := validateRequiredCommandFields("grant", []byte(`{"assertion":"not-an-object","expires_at":"2026-08-18T00:00:00Z","max_uses":1}`))
	if err == nil || !strings.Contains(err.Error(), "assertion must be an object") {
		t.Fatalf("non-object parent error = %v", err)
	}
}

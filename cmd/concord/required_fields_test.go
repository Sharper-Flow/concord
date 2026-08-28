package main

import (
	"strings"
	"testing"
)

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

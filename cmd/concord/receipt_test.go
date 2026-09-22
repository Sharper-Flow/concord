package main

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

// The receipt verb is the only render surface for the product-owned closure
// receipt (CD-0169). These tests cover the verb's own boundary: routing,
// required fields, the unknown-work refusal, and the degrade path that
// prints nothing for a work item outside the completed lifecycle. The bytes
// themselves are pinned by internal/receipt.
func TestReceiptRefusesAMissingWorkID(t *testing.T) {
	t.Setenv(dbOverrideEnv, filepath.Join(t.TempDir(), "concord.db"))
	var out, errOut bytes.Buffer
	if code := runWithInput([]string{"receipt"}, strings.NewReader(`{}`), &out, &errOut); code != 1 {
		t.Fatalf("receipt without work_id exited %d, want 1", code)
	}
	if !strings.Contains(errOut.String(), "work_id") {
		t.Fatalf("diagnostic = %q, want a work_id field refusal", errOut.String())
	}
	if out.Len() != 0 {
		t.Fatalf("receipt printed %q, want nothing", out.String())
	}
}

func TestReceiptRefusesAnUnknownWorkItem(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "concord.db")
	t.Setenv(dbOverrideEnv, dbPath)
	mustOpenStore(t, dbPath)
	body, err := json.Marshal(map[string]string{"work_id": "work-absent"})
	if err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if code := runWithInput([]string{"receipt"}, bytes.NewReader(body), &out, &errOut); code != 1 {
		t.Fatalf("receipt for an unknown work item exited %d, want 1", code)
	}
	if !strings.Contains(errOut.String(), "work item is not recorded") {
		t.Fatalf("diagnostic = %q, want the store projection refusal", errOut.String())
	}
}

func TestReceiptPrintsNothingForAWorkItemOutsideTheCompletedLifecycle(t *testing.T) {
	repo := initLocatorRepo(t)
	dbPath := filepath.Join(t.TempDir(), "concord.db")
	t.Setenv(dbOverrideEnv, dbPath)
	s := mustOpenStore(t, dbPath)
	seedLocatorAuthority(t, s, repo)
	origin, err := s.BootstrapWorktree(context.Background(), bootstrapRequest(), nil)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]string{"work_id": origin.WorkID})
	if err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if code := runWithInput([]string{"receipt"}, bytes.NewReader(body), &out, &errOut); code != 0 {
		t.Fatalf("receipt for a needed work item exited %d stderr=%q, want 0", code, errOut.String())
	}
	if strings.TrimSpace(out.String()) != "" {
		t.Fatalf("receipt printed %q, want silence for a non-completed item", out.String())
	}
}

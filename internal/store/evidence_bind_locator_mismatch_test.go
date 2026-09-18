package store

import (
	"encoding/json"
	"strings"
	"testing"
)

// seedCompletedProducerOperation inserts a completed durable operation whose
// evidence refs name one locator, so a test can bind against it.
func seedCompletedProducerOperation(t *testing.T, s *Store, workID, opID, requestID, principal, recordedLocator string) {
	t.Helper()
	db := s.DatabaseForTesting()
	if _, err := db.Exec(`INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	_, err := db.Exec(`INSERT INTO durable_operations(op_id,attempt_epoch,work_id,workflow_type_ref,workflow_type_version,step_id,step_kind,accepted_inputs_digest,accepted_scope_snapshot,principal_ref,request_id,observed_at,result_kind,evidence_refs,contract_digest) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		opID, 1, workID, "workflow.break_fix", 1, "repair", "external_effect", "sha256:"+strings.Repeat("c", 64), "", principal, requestID, "2026-09-17T00:00:00Z", "completed", `["`+recordedLocator+`"]`, "sha256:"+strings.Repeat("d", 64))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
}

// A caller who names a producer operation that completed without the bound
// locator has a locator mismatch, not an authority problem. The refusal used
// to say the binding "is not backed by the existing durable-operation evidence
// authority", which sent callers hunting for a permission they already held.
// It must name the producer operation and say the locator is absent from what
// that operation recorded.
func TestEvidenceBindRefusalNamesLocatorMismatchOverAuthority(t *testing.T) {
	t.Parallel()
	const workID = "evidence-locator-mismatch"
	s := seedMandateRecoveryItem(t, workID)
	const (
		opID          = "producer-op-1"
		requestID     = "request:producer-1"
		principal     = "principal/operator"
		recordedURL   = "https://github.com/Sharper-Flow/concord/actions/runs/111"
		boundWrongURL = "https://github.com/Sharper-Flow/concord/actions/runs/222"
	)
	seedCompletedProducerOperation(t, s, workID, opID, requestID, principal, recordedURL)

	payload := `{"evidence_kind":"verification","immutable_subject_ref":"` + boundWrongURL + `","producer_id":"` + principal + `","producer_run_ref":"` + opID + `","producer_watermark":"` + requestID + `"}`
	err := runVerdictAction(t, s, workID, "bind_evidence", json.RawMessage(payload), 0)
	if err == nil {
		t.Fatal("a binding naming a locator the producer never recorded was admitted")
	}
	got := err.Error()
	if !strings.Contains(got, "locator") {
		t.Fatalf("refusal names authority instead of the locator mismatch: %s", got)
	}
	if !strings.Contains(got, opID) {
		t.Fatalf("refusal does not name the producer operation: %s", got)
	}
	if !strings.Contains(got, boundWrongURL) {
		t.Fatalf("refusal does not name the unrecorded locator: %s", got)
	}
}

// When the named producer operation itself is missing, the refusal keeps
// naming the producer, because that is the thing the caller must fix.
func TestEvidenceBindRefusalNamesMissingProducerOperation(t *testing.T) {
	t.Parallel()
	const workID = "evidence-missing-producer"
	s := seedMandateRecoveryItem(t, workID)

	payload := `{"evidence_kind":"verification","evidence_ref":"evidence:orphan","producer_id":"principal/operator","producer_run_ref":"no-such-op","producer_watermark":"request:none"}`
	err := runVerdictAction(t, s, workID, "bind_evidence", json.RawMessage(payload), 0)
	if err == nil {
		t.Fatal("a binding naming no producer operation was admitted")
	}
	got := err.Error()
	if !strings.Contains(got, "no-such-op") {
		t.Fatalf("refusal does not name the missing producer operation: %s", got)
	}
}

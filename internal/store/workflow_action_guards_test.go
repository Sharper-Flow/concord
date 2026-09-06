package store

import (
	"encoding/json"
	"strings"
	"testing"
)

func seedMandateRecoveryItem(t *testing.T, workID string) *Store {
	t.Helper()
	s, _ := seedItemAtAcceptance(t, workID, false)
	db := s.DatabaseForTesting()
	if _, err := db.Exec(`INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE workflow_contracts SET spec_mandate='["spec:one"]' WHERE work_id=? AND superseded_by IS NULL`, workID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO workflow_contract_law_revisions(work_id,contract_version,law_id,content_hash) VALUES(?,1,'spec:one',?)`, workID, "sha256:"+strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	return s
}

func recoveryBindPayload() json.RawMessage {
	return json.RawMessage(`{"evidence_kind":"artifact","evidence_ref":"spec:one"}`)
}

func TestBindEvidenceAdmittedPastBindingStepWhileMandateUnbound(t *testing.T) {
	const workID = "mandate-recovery-admit"
	s := seedMandateRecoveryItem(t, workID)
	if err := runVerdictAction(t, s, workID, "bind_evidence", recoveryBindPayload(), 0); err != nil {
		t.Fatalf("late mandate binding refused: %v", err)
	}
}

func TestBindEvidenceRefusedPastBindingStepOnceMandateBound(t *testing.T) {
	const workID = "mandate-recovery-narrow"
	s := seedMandateRecoveryItem(t, workID)
	if err := runVerdictAction(t, s, workID, "bind_evidence", recoveryBindPayload(), 0); err != nil {
		t.Fatalf("initial mandate binding refused: %v", err)
	}
	err := runVerdictAction(t, s, workID, "bind_evidence", recoveryBindPayload(), 0)
	if err == nil {
		t.Fatal("late binding passed after the mandate was bound")
	}
	var failure *Failure
	if !failureAs(err, &failure) || failure.Kind != KindIllegalLifecycleTransition {
		t.Fatalf("late binding error = %v, want %s", err, KindIllegalLifecycleTransition)
	}
}

func TestRecordVerdictPassesAfterRecoveryBind(t *testing.T) {
	const workID = "mandate-recovery-verdict"
	s := seedMandateRecoveryItem(t, workID)
	if err := runVerdictAction(t, s, workID, "bind_evidence", recoveryBindPayload(), 0); err != nil {
		t.Fatalf("mandate recovery binding refused: %v", err)
	}
	if err := runVerdictActionAs(t, s, workID, "record_verdict", json.RawMessage(`{"contract_version":1,"predicate_id":"predicate:primary","evaluation_evidence":["spec:one"]}`), 0, verdictReviewer(t, workID)); err != nil {
		t.Fatalf("verdict after mandate recovery binding refused: %v", err)
	}
}

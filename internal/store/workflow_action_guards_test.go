package store

import (
	"context"
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

// A declared verification obligation stays recoverable after the same law's
// mandate reference is bound under another kind (#905): the obligation gate
// reads the exact (evidence kind, law reference) tuple, so an artifact
// mandate bind must not lock a verification or review obligation out of the
// late binding path.
func TestBindEvidenceObligationRecoverableAfterMandateBound(t *testing.T) {
	const workID = "obligation-recovery-admit"
	s := seedMandateRecoveryItem(t, workID)
	db := s.DatabaseForTesting()
	var contractVersion int64
	if err := db.QueryRow(`SELECT MAX(contract_version) FROM workflow_contracts WHERE work_id=?`, workID).Scan(&contractVersion); err != nil {
		t.Fatal(err)
	}
	seedTx, seedErr := db.BeginTx(context.Background(), nil)
	if seedErr != nil {
		t.Fatal(seedErr)
	}
	if _, err := seedTx.Exec(`INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := seedTx.Exec(`INSERT INTO domains(home_project_id,home_locator_id,product_id,domain_id,name,purpose,status,registry_content_hash,scanned_commit_oid) VALUES('project','workflow-law-locator','product','root','Root','Product law','current',?,'test')`, "sha256:"+strings.Repeat("d", 64)); err != nil {
		t.Fatal(err)
	}
	if _, err := seedTx.Exec(`INSERT INTO domain_registries(product_id,home_project_id,home_locator_id,product_key,root_domain_id,schema_version,content_hash,scanned_commit_oid) VALUES('product','project','workflow-law-locator','product','root','1.0',?,'0000000000000000000000000000000000000000')`, "sha256:"+strings.Repeat("d", 64)); err != nil {
		t.Fatal(err)
	}
	if _, err := seedTx.Exec(`INSERT INTO workflow_architecture_bindings(work_id,contract_version,product_id,domain_registry_content_hash,home_domain_id,projection_hash) VALUES(?,?, 'product',?,'root',?)`, workID, contractVersion, "sha256:"+strings.Repeat("d", 64), "sha256:"+strings.Repeat("c", 64)); err != nil {
		t.Fatal(err)
	}
	if _, err := seedTx.Exec(`INSERT INTO workflow_contract_verification_obligations(work_id,contract_version,law_id,obligation_id) VALUES(?,?,'spec:one','verification')`, workID, contractVersion); err != nil {
		t.Fatal(err)
	}
	if _, err := seedTx.Exec(`DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	if err := seedTx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := runVerdictAction(t, s, workID, "bind_evidence", recoveryBindPayload(), 0); err != nil {
		t.Fatalf("mandate binding refused: %v", err)
	}
	if err := runVerdictAction(t, s, workID, "bind_evidence", json.RawMessage(`{"evidence_kind":"verification","evidence_ref":"spec:one"}`), 0); err != nil {
		t.Fatalf("obligation recovery binding refused after the mandate was bound: %v", err)
	}
	// Once the obligation tuple is bound, the same bind refuses: recovery
	// does not reopen evidence binding without limit.
	err := runVerdictAction(t, s, workID, "bind_evidence", json.RawMessage(`{"evidence_kind":"verification","evidence_ref":"spec:one"}`), 0)
	if err == nil {
		t.Fatal("obligation recovery admitted a second identical bind")
	}
	var failure *Failure
	if !failureAs(err, &failure) || failure.Kind != KindIllegalLifecycleTransition {
		t.Fatalf("repeat obligation bind error = %v, want %s", err, KindIllegalLifecycleTransition)
	}
}

// An obligation tuple that is already satisfied does not reopen recovery:
// the guard admits only what the completion gate still demands.
func TestBindEvidenceObligationRefusedOnceSatisfied(t *testing.T) {
	const workID = "obligation-recovery-satisfied"
	s := seedMandateRecoveryItem(t, workID)
	db := s.DatabaseForTesting()
	var contractVersion int64
	if err := db.QueryRow(`SELECT MAX(contract_version) FROM workflow_contracts WHERE work_id=?`, workID).Scan(&contractVersion); err != nil {
		t.Fatal(err)
	}
	seedTx, seedErr := db.BeginTx(context.Background(), nil)
	if seedErr != nil {
		t.Fatal(seedErr)
	}
	if _, err := seedTx.Exec(`INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := seedTx.Exec(`INSERT INTO domains(home_project_id,home_locator_id,product_id,domain_id,name,purpose,status,registry_content_hash,scanned_commit_oid) VALUES('project','workflow-law-locator','product','root','Root','Product law','current',?,'test')`, "sha256:"+strings.Repeat("d", 64)); err != nil {
		t.Fatal(err)
	}
	if _, err := seedTx.Exec(`INSERT INTO domain_registries(product_id,home_project_id,home_locator_id,product_key,root_domain_id,schema_version,content_hash,scanned_commit_oid) VALUES('product','project','workflow-law-locator','product','root','1.0',?,'0000000000000000000000000000000000000000')`, "sha256:"+strings.Repeat("d", 64)); err != nil {
		t.Fatal(err)
	}
	if _, err := seedTx.Exec(`INSERT INTO workflow_architecture_bindings(work_id,contract_version,product_id,domain_registry_content_hash,home_domain_id,projection_hash) VALUES(?,?,'product',?,'root',?)`, workID, contractVersion, "sha256:"+strings.Repeat("d", 64), "sha256:"+strings.Repeat("c", 64)); err != nil {
		t.Fatal(err)
	}
	if _, err := seedTx.Exec(`INSERT INTO workflow_contract_verification_obligations(work_id,contract_version,law_id,obligation_id) VALUES(?,?,'spec:one','review')`, workID, contractVersion); err != nil {
		t.Fatal(err)
	}
	if _, err := seedTx.Exec(`INSERT INTO domain_events(event_id,subject_type,subject_id,kind,actor,occurred_at,payload_version,payload) VALUES('ob-satisfied-ev','work_item',?,'workflow.evidence_bound','operator','2026-09-07T00:00:00Z',1,?)`, workID, `{"evidence_kind":"review","immutable_subject_ref":"spec:one"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := seedTx.Exec(`DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	if err := seedTx.Commit(); err != nil {
		t.Fatal(err)
	}
	err := runVerdictAction(t, s, workID, "bind_evidence", json.RawMessage(`{"evidence_kind":"review","evidence_ref":"spec:one"}`), 0)
	if err == nil {
		t.Fatal("satisfied obligation reopened recovery")
	}
	var failure *Failure
	if !failureAs(err, &failure) || failure.Kind != KindIllegalLifecycleTransition {
		t.Fatalf("satisfied obligation bind error = %v, want %s", err, KindIllegalLifecycleTransition)
	}
}

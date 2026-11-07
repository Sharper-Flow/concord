package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

type acceptanceRecoveryFixture struct {
	store    *Store
	workID   string
	owner    WorkflowActor
	operator WorkflowActor
	repo     string
	receipt  string
}

func newAcceptanceRecoveryFixture(_ context.Context, t *testing.T) acceptanceRecoveryFixture {
	t.Helper()
	return qCurrentAcceptance(t, "work-alpha")
}

func (f acceptanceRecoveryFixture) version(ctx context.Context, t *testing.T) int64 {
	t.Helper()
	version, err := workflowCurrentVersion(ctx, f.store, f.workID)
	if err != nil {
		t.Fatal(err)
	}
	return version
}

func (f acceptanceRecoveryFixture) run(ctx context.Context, actionID, label string, version int64, payload json.RawMessage, evidenceRefs []string) error {
	ctx, err := f.store.PrepareWorkflowActionProof(ctx, actionID, f.workID, payload)
	if err != nil {
		return err
	}
	request := WorkflowActionExecutionRequest{
		WorkID: f.workID, ExpectedVersion: version, ActionID: actionID, Payload: payload, EvidenceRefs: evidenceRefs,
		Actor: f.owner, OperationID: f.workID + ":" + label, PrincipalRef: f.owner.PrincipalRef, Tool: "concord_work_transition",
		IdempotencyKey: f.workID + ":" + label, IdempotencyIdentity: f.workID + ":" + label, RequestID: f.workID + ":" + label,
		AcceptedInputsDigest: "sha256:" + strings.Repeat("a", 64), ContractDigest: testManifestDigest,
		Now: time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC),
	}
	tx, err := f.store.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := applyWorkflowActionRawTx(ctx, tx, newWriteScope(tx), request); err != nil {
		return err
	}
	if err := leaveWrite(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}

func TestOrdinaryEvidenceBindingEnforcesVersionAndPreservesAuthority(t *testing.T) {
	const id, ref = "ordinary-binding-version", "evidence:ordinary-binding"
	route := seedWorkflowReturnRouteFixture(t, id, WorkflowStepPlanChange, "implement")
	f := acceptanceRecoveryFixture{store: route.store, workID: id, owner: route.owner, operator: route.operator}
	seedWorkflowAuthority(t, f.store, "ordinary-binding-producer", id, "principal/ordinary", "request/ordinary", []string{ref})
	payload := mustJSONValue(map[string]any{"evidence_kind": "artifact", "immutable_subject_ref": ref, "producer_id": "principal/ordinary", "producer_run_ref": "ordinary-binding-producer", "producer_watermark": "request/ordinary"})
	before := contractCorrectionEffectSnapshot(t, f.store, id)
	version := f.version(t.Context(), t)
	if err := f.run(t.Context(), "bind_evidence", "stale", version-1, payload, []string{ref}); !hasFailureKind(err, KindVersionConflict) {
		t.Fatalf("stale binding=%v, want version conflict", err)
	}
	if after := contractCorrectionEffectSnapshot(t, f.store, id); after != before {
		t.Fatalf("refused binding changed state: %s -> %s", before, after)
	}
	if err := f.run(t.Context(), "bind_evidence", "bind", version, payload, []string{ref}); err != nil {
		t.Fatalf("ordinary binding: %v", err)
	}
	afterBinding := contractCorrectionEffectSnapshot(t, f.store, id)
	if err := f.run(t.Context(), "bind_evidence", "bind", f.version(t.Context(), t), payload, []string{ref}); !hasFailureKind(err, KindIdempotencyConflict) {
		t.Fatalf("binding identity reused at a different version=%v", err)
	}
	if after := contractCorrectionEffectSnapshot(t, f.store, id); after != afterBinding {
		t.Fatalf("conflicting replay changed state: %s -> %s", afterBinding, after)
	}
	var count int
	if err := f.store.db.QueryRow(`SELECT count(*) FROM workflow_evidence_bindings WHERE work_id=? AND json_extract(payload,'$.immutable_subject_ref')=? AND json_extract(payload,'$.producer_watermark')=?`, id, ref, "request/ordinary").Scan(&count); err != nil || count != 1 {
		t.Fatalf("bindings=%d err=%v", count, err)
	}
	active, err := activeWorkflowContractVersion(t.Context(), f.store.db, id, "test")
	if err != nil || active != 1 || workflowPlanFixtureFrontier(t, f.store, id) != "implement" {
		t.Fatalf("binding changed plan or contract: active=%d err=%v", active, err)
	}
}

func TestTerminalWorkflowRefusesEvidenceBindingWithoutEffects(t *testing.T) {
	const id, ref = "terminal-binding-refusal", "evidence:terminal-binding"
	route := seedWorkflowReturnRouteFixture(t, id, WorkflowStepPlanChange, "review")
	f := acceptanceRecoveryFixture{store: route.store, workID: id, owner: route.owner}
	seedWorkflowAuthority(t, f.store, "terminal-binding-producer", id, "principal/terminal", "request/terminal", []string{ref})
	if _, err := f.store.db.Exec(`INSERT INTO write_guard(active) VALUES(1); UPDATE workflow_instances SET instance_state='completed' WHERE work_id=?; DELETE FROM write_guard`, id); err != nil {
		t.Fatal(err)
	}
	payload := mustJSONValue(map[string]any{"evidence_kind": "artifact", "immutable_subject_ref": ref, "producer_id": "principal/terminal", "producer_run_ref": "terminal-binding-producer", "producer_watermark": "request/terminal"})
	before := contractCorrectionEffectSnapshot(t, f.store, id)
	if err := f.run(t.Context(), "bind_evidence", "terminal", f.version(t.Context(), t), payload, []string{ref}); err == nil {
		t.Fatal("terminal workflow accepted evidence binding")
	}
	if after := contractCorrectionEffectSnapshot(t, f.store, id); after != before {
		t.Fatalf("refused binding changed state: %s -> %s", before, after)
	}
}

package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

// CD-0040 D11 verification participation, end to end: a native-run record
// embeds the shared component, a verification event answering its observation
// folds into both projections, and the D9 consumption gate refuses
// workflow.evidence_bound(native_run) until — and only until — the record is
// verified.
func TestNativeRunVerificationParticipationAndEvidenceGate(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, t.TempDir()+"/concord.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	workID := seedNativeRunFixture(t, s)

	actor := WorkflowActor{PrincipalRef: "principal-1", ClientRef: "client-1", AgentRef: "agent-1", SessionRef: "session-1", ActorClass: ActorAgent}
	report := func(phase, status string, version int64) Event {
		event, buildErr := buildNativeRunEvent("op:"+phase, workID, actor, time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC), version, phase, "run-gate-1", "routing://prod", status, "https://evidence.invalid/runs/run-gate-1/"+phase, "sha256:"+repeatHex("a"), "2026-08-20T12:00:00Z")
		if buildErr != nil {
			t.Fatalf("build %s report: %v", phase, buildErr)
		}
		return event
	}

	if err := s.Transact(ctx, func(tx *Transaction) error {
		if err := enterFold(ctx, tx.tx); err != nil {
			return err
		}
		_, applyErr := applyWorkflowOperationTx(ctx, tx.tx, Operation{Events: []Event{report("start", "started", 2)}})
		if applyErr != nil {
			return applyErr
		}
		return leaveFold(ctx, tx.tx)
	}); err != nil {
		t.Fatal(err)
	}

	// The report reads unverified with its observation identity attached.
	reports := readNativeRuns(t, s, workID)
	if len(reports) != 1 {
		t.Fatalf("native reports=%d, want 1", len(reports))
	}
	if reports[0].VerificationState != string(VerificationUnverified) || !reports[0].Unverified {
		t.Fatalf("fresh native report read as %s", reports[0].VerificationState)
	}
	observationID := reports[0].ObservationID
	if observationID == "" {
		t.Fatal("native report carries no observation identity")
	}

	// The producer operation exists from the start, so the only variable in
	// both binds below is the verification state of the attributed record.
	if err := s.Transact(ctx, func(tx *Transaction) error {
		_, execErr := tx.tx.ExecContext(ctx, `INSERT INTO durable_operations(op_id,attempt_epoch,work_id,workflow_type_ref,workflow_type_version,step_id,step_kind,accepted_inputs_digest,accepted_scope_snapshot,principal_ref,request_id,observed_at,contract_digest,result_kind,evidence_refs) VALUES('op:start',1,?,'workflow.ops_runbook',1,'execute','external_effect','sha256:'+?,'{}','principal-1','req-1','2026-08-20T12:00:00Z','sha256:'+?,'completed',?)`, workID, repeatHex("b"), repeatHex("c"), workflowJSON([]string{observationID}))
		return execErr
	}); err != nil {
		t.Fatal(err)
	}

	// D9 gate: binding the unverified record as completion evidence fails
	// closed, naming exactly why.
	if err := s.Transact(ctx, func(tx *Transaction) error {
		if err := enterFold(ctx, tx.tx); err != nil {
			return err
		}
		_, bindErr := applyWorkflowOperationTx(ctx, tx.tx, Operation{Events: []Event{workflowTypedEvent("op:bind-unverified", WorkflowEvidenceBound, workID, "actor:1", time.Date(2026, 8, 20, 12, 1, 0, 0, time.UTC), 3, map[string]any{
			"evidence_kind": "native_run", "immutable_subject_ref": observationID,
			"producer_id": "principal-1", "producer_run_ref": "op:start", "producer_watermark": "req-1",
			"observed_at": "2026-08-20T12:00:30Z",
		})}})
		if bindErr != nil {
			return bindErr
		}
		return leaveFold(ctx, tx.tx)
	}); err == nil {
		t.Fatal("unverified native_run evidence satisfied a completion gate")
	} else {
		var failure *Failure
		if !errors.As(err, &failure) || (failure.Kind != KindStaleRequiresReview && failure.Kind != KindMissingEvidence) {
			t.Fatalf("unverified native_run evidence refused for the wrong reason: %v", err)
		}
	}

	// The verification event names the shared observation; the fold answers
	// both the generic record and the native-run rows bound to it.
	if err := s.Transact(ctx, func(tx *Transaction) error {
		return AppendExternalObservationVerificationTx(ctx, tx, workID, "principal-1", time.Date(2026, 8, 20, 12, 2, 0, 0, time.UTC), ExternalObservationVerification{
			ObservationID: observationID, VerificationMethod: VerifyTrustedClientReport,
			VerifiedAt: "2026-08-20T12:02:00Z", VerifyingAuthorityRef: "client:verifier-1", Result: VerificationMatched,
		})
	}); err != nil {
		t.Fatal(err)
	}

	reports = readNativeRuns(t, s, workID)
	if len(reports) != 1 || reports[0].VerificationState != string(VerificationVerified) || reports[0].Unverified {
		t.Fatalf("verified observation did not reach the native report: %+v", reports)
	}

	// The gate now admits the same binding.
	if err := s.Transact(ctx, func(tx *Transaction) error {
		if err := enterFold(ctx, tx.tx); err != nil {
			return err
		}
		_, bindErr := applyWorkflowOperationTx(ctx, tx.tx, Operation{Events: []Event{workflowTypedEvent("op:bind-verified", WorkflowEvidenceBound, workID, "actor:1", time.Date(2026, 8, 20, 12, 3, 0, 0, time.UTC), 3, map[string]any{
			"evidence_kind": "native_run", "immutable_subject_ref": observationID,
			"producer_id": "principal-1", "producer_run_ref": "op:start", "producer_watermark": "req-1",
			"observed_at": "2026-08-20T12:00:30Z",
		})}})
		if bindErr != nil {
			return bindErr
		}
		return leaveFold(ctx, tx.tx)
	}); err != nil {
		t.Fatalf("verified native_run evidence was still refused: %v", err)
	}
}

func readNativeRuns(t *testing.T, s *Store, workID string) []NativeRunReport {
	t.Helper()
	ctx := context.Background()
	var out []NativeRunReport
	if err := s.Transact(ctx, func(tx *Transaction) error {
		reports, readErr := readWorkflowNativeRunsTx(ctx, tx.tx, workID)
		out = reports
		return readErr
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

func repeatHex(char string) string {
	out := ""
	for i := 0; i < 64; i++ {
		out += char
	}
	return out
}

func seedNativeRunFixture(t *testing.T, s *Store) string {
	t.Helper()
	ctx := context.Background()
	if err := s.Transact(ctx, func(tx *Transaction) error {
		events := []Event{
			{EventID: "gate-product", Kind: "product.created", SubjectType: SubjectProduct, SubjectID: "product-1", Actor: "operator", OccurredAt: time.Now().UTC(), PayloadVersion: 1, Payload: []byte(`{"display_name":"Product","stage_maturity":"prototype","stage_audience_commitment":"operator_only"}`)},
			{EventID: "gate-project", Kind: "project.created", SubjectType: SubjectProject, SubjectID: "project-1", Actor: "operator", OccurredAt: time.Now().UTC(), PayloadVersion: 1, Payload: []byte(`{"display_name":"Project"}`)},
			{EventID: "gate-product-project", Kind: "product_project.added", SubjectType: SubjectProduct, SubjectID: "product-1", Actor: "operator", OccurredAt: time.Now().UTC(), PayloadVersion: 1, Payload: []byte(`{"product_id":"product-1","project_id":"project-1","role":"primary","reason":"fixture","expected_version":1,"resulting_version":2}`)},
			{EventID: "gate-work", Kind: "work.created", SubjectType: SubjectWorkItem, SubjectID: "work-gate", Actor: "operator", OccurredAt: time.Now().UTC(), PayloadVersion: 2, Payload: []byte(`{"work_kind":"task","title":"Gate","priority":1}`)},
			{EventID: "gate-work-membership", Kind: "work.memberships_replaced", SubjectType: SubjectWorkItem, SubjectID: "work-gate", Actor: "operator", OccurredAt: time.Now().UTC(), PayloadVersion: 1, Payload: []byte(`{"memberships":[{"project_id":"project-1","role":"primary"}],"expected_version":1,"resulting_version":2}`)},
		}
		_, applyErr := ApplyOperationTx(ctx, tx, Operation{Events: events})
		return applyErr
	}); err != nil {
		t.Fatal(err)
	}
	return "work-gate"
}

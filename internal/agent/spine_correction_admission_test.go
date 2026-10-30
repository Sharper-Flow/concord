package agent

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/store"
)

func TestSpineCurrentReviewCorrectionAdmission(t *testing.T) {
	f := newSpineExecutionFixture(t)
	f.verify(t)
	f.recordJob(t)
	f.completeWorker(t)
	f.proof = f.verify(t)
	f.recordJob(t)
	beforeReview := f.read(t)
	candidate := *beforeReview.Pinned.Pin.Review.Candidate
	const reviewAttempt = "attempt:spine-correction-review"
	lane, job, epoch := f.startWorker(t, "review", reviewAttempt)
	report := &store.WorkerReviewBlock{
		Disposition: "correction_required", InputCandidate: candidate, OutputCandidate: candidate,
		Dependencies: *beforeReview.Pinned.Pin.Review.Dependencies,
		Assessments:  []store.WorkerReviewAssessment{{PredicateID: "predicate:primary", Assessment: "unproved", ReceiptRefs: []string{}}},
		Findings:     []store.WorkerReviewFinding{}, RemainingObligations: []store.WorkerReviewObligation{},
	}
	f.finishWorker(t, lane, reviewAttempt, job, report)
	fields := map[string]any{
		"diagnosis":     "The current Review requires a checked implementation correction.",
		"strategy":      "Correct the value implementation and obtain applicable Review.",
		"predicate_ids": []string{"predicate:primary"}, "evidence_refs": []string{f.proof},
	}
	actor := store.WorkflowActor{PrincipalRef: f.env.PrincipalRef, ClientRef: f.env.ClientRef, AgentRef: f.env.AgentRef, SessionRef: f.env.SessionRef, ActorClass: store.ActorAgent}
	refuse := func(t *testing.T, action string, payload map[string]any) {
		t.Helper()
		before := f.read(t)
		effects := premiseConsentSnapshot(t, f.s)
		err := store.WorkflowActionPreflightWithRegistry(t.Context(), f.s, store.BuiltinWorkflowRegistry(), store.WorkflowActionPreflightRequest{
			WorkID: f.work, ExpectedVersion: before.Pinned.Pin.Version, ActionID: action, Payload: retryJSON(payload), Actor: actor, SessionWorktree: f.path,
		})
		var failure *store.Failure
		if !errors.As(err, &failure) || failure.RecoveryAction == "" {
			t.Fatalf("preflight did not name the required recovery: %v", err)
		}
		response := f.action(t, action, payload)
		after := f.read(t)
		if response.Outcome != agentcontract.OutcomeError || response.Error == nil || response.Error.Kind != mapFailureKind(failure.Kind) || response.Error.Message != failure.Detail || response.Error.EffectState != agentcontract.EffectNone {
			t.Fatalf("mutation disagrees with preflight: preflight=%v mutation=%+v", err, response.Error)
		}
		if !reflect.DeepEqual(effects, premiseConsentSnapshot(t, f.s)) || !reflect.DeepEqual(before.Pinned.Pin.Review, after.Pinned.Pin.Review) || !reflect.DeepEqual(before.Pinned.Contract, after.Pinned.Contract) {
			t.Fatal("refused correction or delivery changed workflow authority")
		}
	}
	t.Run("terminal arrival is not correction authority", func(t *testing.T) { refuse(t, "request_correction", fields) })
	spineOK(t, f.action(t, "accept_worker_result", map[string]any{"attempt_id": reviewAttempt, "attempt_epoch": epoch}))
	accepted := f.read(t)
	if accepted.Pinned.Step != "execution" || accepted.Pinned.Pin.Review.Accepted == nil || accepted.Pinned.Pin.Review.Accepted.Report.Disposition != "correction_required" {
		t.Fatalf("Main lost the current correction hold: %+v", accepted.Pinned)
	}
	reportRef := accepted.Pinned.Pin.Review.Accepted.ReportRef
	fields["evidence_refs"] = []string{reportRef}
	t.Run("accepted report still requires durable binding", func(t *testing.T) { refuse(t, "request_correction", fields) })
	spineOK(t, f.action(t, "bind_evidence", map[string]any{"evidence_kind": "review", "evidence_ref": reportRef}))
	for _, vector := range []struct {
		name, field string
		value       []string
	}{
		{"unapproved criterion", "predicate_ids", []string{"predicate:unapproved"}},
		{"unbound evidence", "evidence_refs", []string{"evidence:unbound-correction"}},
	} {
		t.Run(vector.name, func(t *testing.T) {
			invalid := make(map[string]any, len(fields))
			for key, value := range fields {
				invalid[key] = value
			}
			invalid[vector.field] = vector.value
			refuse(t, "request_correction", invalid)
		})
	}
	delivery := map[string]any{"delivery_artifact": "artifact:spine-corrected-value", "delivery_state": "asserted"}
	t.Run("correction hold is not delivery readiness", func(t *testing.T) { refuse(t, "record_delivery", delivery) })
	before := f.read(t)
	attempts := countRows(t, f.s.DatabaseForTesting(), `SELECT count(*) FROM worker_attempts`)
	input := map[string]any{"work_id": f.work, "expected_version": before.Pinned.Pin.Version, "action_id": "request_correction", "fields": fields, "idempotency_key": "spine-current-correction"}
	if err := store.WorkflowActionPreflightWithRegistry(t.Context(), f.s, store.BuiltinWorkflowRegistry(), store.WorkflowActionPreflightRequest{
		WorkID: f.work, ExpectedVersion: before.Pinned.Pin.Version, ActionID: "request_correction", Payload: retryJSON(fields), Actor: actor, SessionWorktree: f.path,
	}); err != nil {
		t.Fatalf("current correction preflight refused: %v", err)
	}
	spineOK(t, f.call(t, "concord_work_transition", "workflow_action", input))
	corrected := f.read(t)
	context := corrected.Pinned.Pin.Correction
	if corrected.Pinned.Step != "execution" || context == nil || context.SourceEventID == "" || !reflect.DeepEqual(context.PredicateIDs, []string{"predicate:primary"}) || !reflect.DeepEqual(context.EvidenceRefs, []string{reportRef}) {
		t.Fatalf("correction lost its current Review scope or declared target: %+v", corrected.Pinned)
	}
	if !reflect.DeepEqual(before.Pinned.Contract, corrected.Pinned.Contract) || !reflect.DeepEqual(before.Pinned.Pin.Review, corrected.Pinned.Pin.Review) || !reflect.DeepEqual(before.Pinned.Pin.VerdictEvidence, corrected.Pinned.Pin.VerdictEvidence) || countRows(t, f.s.DatabaseForTesting(), `SELECT count(*) FROM worker_attempts`) != attempts {
		t.Fatal("recording correction changed the contract, Review, proof, attempt population or approval state")
	}
	effects := premiseConsentSnapshot(t, f.s)
	replay := spineOK(t, f.call(t, "concord_work_transition", "workflow_action", input))
	if !replay.Replayed || !reflect.DeepEqual(effects, premiseConsentSnapshot(t, f.s)) {
		t.Fatal("correction replay had another effect")
	}
	// A recorded correction authorizes a real producer dispatch. The report
	// still cannot grant readiness; Main must accept applicable Review on C.
	f.recordJob(t)
	const correctiveAttempt = "attempt:spine-corrective-value"
	lane, job, epoch = f.startWorker(t, "implement", correctiveAttempt)
	if err := os.WriteFile(filepath.Join(f.path, "pkg/value.go"), []byte("package spine\n\nfunc Value() int { return 2 + 5 }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitRun(t, f.path, "add", "pkg/value.go")
	gitRun(t, f.path, "commit", "-qm", "checked corrective implementation")
	f.proof = f.verify(t)
	f.finishWorker(t, lane, correctiveAttempt, job, nil)
	spineOK(t, f.action(t, "accept_worker_result", map[string]any{"attempt_id": correctiveAttempt, "attempt_epoch": epoch}))
	t.Run("corrective intake still requires current ready Review", func(t *testing.T) { refuse(t, "record_delivery", delivery) })
	f.recordJob(t)
	f.review(t, "ready_for_final_proof", f.proof)
	t.Run("ready Review closes the correction prerequisite", func(t *testing.T) { refuse(t, "request_correction", fields) })
	spineOK(t, f.action(t, "bind_evidence", map[string]any{"evidence_kind": "verification", "evidence_ref": f.proof}))
	spineOK(t, f.action(t, "record_delivery", delivery))
	if current := f.read(t); current.Pinned.Step != "refine" || current.Pinned.Pin.Review.Candidate == nil || current.Pinned.Pin.Review.Candidate.Commit != gitRun(t, f.path, "rev-parse", "HEAD") {
		t.Fatalf("corrective execution did not exit on its accepted checked candidate: %+v", current.Pinned)
	}
}

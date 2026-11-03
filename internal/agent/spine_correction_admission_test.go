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
	f.implement(t)
	f.proof = f.verify(t)
	beforeReview := f.read(t)
	if beforeReview.Pinned.Pin.Review == nil || beforeReview.Pinned.Pin.Review.Dependencies == nil {
		t.Fatal("qualified candidate has no current Review dependency identities")
	}
	candidate := store.ReviewCandidate{ProjectID: "project-1", Commit: gitRun(t, f.path, "rev-parse", "HEAD"), Tree: gitRun(t, f.path, "rev-parse", "HEAD^{tree}")}
	report := &store.ReviewBlock{
		Disposition: "correction_required", InputCandidate: candidate, OutputCandidate: candidate,
		Dependencies: *beforeReview.Pinned.Pin.Review.Dependencies,
		Assessments:  []store.ReviewAssessment{{PredicateID: "predicate:primary", Assessment: "unproved", ReceiptRefs: []string{}}},
		Findings:     []store.ReviewFinding{}, RemainingObligations: []store.ReviewObligation{},
	}
	actor := store.WorkflowActor{PrincipalRef: f.env.PrincipalRef, ClientRef: f.env.ClientRef, AgentRef: f.env.AgentRef, SessionRef: f.env.SessionRef, ActorClass: store.ActorAgent}
	refuse := func(t *testing.T, action string, payload map[string]any) {
		t.Helper()
		before := f.read(t)
		effects := premiseConsentSnapshot(t, f.s)
		err := store.WorkflowActionPreflightWithRegistry(t.Context(), f.s, store.BuiltinWorkflowRegistry(), store.WorkflowActionPreflightRequest{
			WorkID: f.work, ExpectedVersion: before.Pinned.Pin.Version, ActionID: action, Payload: retryJSON(payload), Actor: actor,
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
	spineOK(t, f.action(t, "record_review", map[string]any{"review": report}))
	accepted := f.read(t)
	if accepted.Pinned.Step != "execution" || accepted.Pinned.Pin.Review.Accepted == nil || accepted.Pinned.Pin.Review.Accepted.Report.Disposition != "correction_required" {
		t.Fatalf("Main lost the current correction hold: %+v", accepted.Pinned)
	}
	reportRef := accepted.Pinned.Pin.Review.Accepted.ReportRef
	t.Run("accepted report durably binds correction evidence", func(t *testing.T) {
		var bindings int
		if err := f.s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM workflow_evidence_bindings WHERE work_id=? AND json_extract(payload,'$.evidence_kind')='review' AND json_extract(payload,'$.immutable_subject_ref')=?`, f.work, reportRef).Scan(&bindings); err != nil || bindings != 1 {
			t.Fatalf("accepted Review report bindings=%d, want one: %v", bindings, err)
		}
	})
	delivery := map[string]any{"delivery_artifact": "artifact:spine-corrected-value", "delivery_state": "asserted"}
	t.Run("correction hold is not delivery readiness", func(t *testing.T) { refuse(t, "record_delivery", delivery) })
	// Corrective bytes still need current Review before delivery.
	if err := os.WriteFile(filepath.Join(f.path, "pkg/value.go"), []byte("package spine\n\nfunc Value() int { return 2 + 5 }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitRun(t, f.path, "add", "pkg/value.go")
	gitRun(t, f.path, "commit", "-qm", "checked corrective implementation")
	f.proof = f.verify(t)
	t.Run("corrective intake still requires current ready Review", func(t *testing.T) { refuse(t, "record_delivery", delivery) })
	f.review(t, "ready_for_final_proof", f.proof)
	spineOK(t, f.action(t, "bind_evidence", map[string]any{"evidence_kind": "verification", "evidence_ref": f.proof}))
	if step := f.read(t).Pinned.Step; step != "execution" {
		t.Fatalf("corrective Review moved the producing step to %q", step)
	}
	spineOK(t, f.action(t, "record_delivery", delivery))
	if current := f.read(t); current.Pinned.Step != "refine" || current.Pinned.Pin.Review.Candidate == nil || current.Pinned.Pin.Review.Candidate.Commit != gitRun(t, f.path, "rev-parse", "HEAD") {
		t.Fatalf("corrective execution did not exit on its accepted checked candidate: %+v", current.Pinned)
	}
}

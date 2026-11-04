package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Exact identities are compared by the typed Review conformance test.
type admissionReviewView struct {
	Present, Candidate, CandidateMatches, Dependencies, Current bool
	Blocked, PublicationOutstanding                             bool
	Disposition                                                 string
}

func admissionReviewViewOf(state *WorkflowReviewState) admissionReviewView {
	if state == nil {
		return admissionReviewView{}
	}
	view := admissionReviewView{Present: true, Candidate: state.Candidate != nil, Dependencies: state.Dependencies != nil,
		Current: state.DependenciesCurrent, Blocked: state.blocker() != "", PublicationOutstanding: state.PublicationOutstanding}
	if state.Accepted != nil {
		view.Disposition = state.Accepted.Report.Disposition
		view.CandidateMatches = state.Candidate != nil && *state.Candidate == state.Accepted.Report.OutputCandidate
	}
	return view
}

func conformanceImplementChange(t *testing.T, j *candidateJourney, label string) {
	t.Helper()
	path := filepath.Join(j.repo, "internal", "store", "value.go")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("package fixture\nvar Value = "+itoa(len(label))+" // "+label+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	j.commit(t, "author@example.invalid", "produce bounded implementation")
	j.observe(t, label)
}

func TestWorkflowPlanAdmissionReviewAndMandate(t *testing.T) {
	const workID = "plan-admission-review-mandate"
	f := seedWorkflowReturnRouteFixtureWithPlan(t, workID, WorkflowStepPlanChange, "implement", []string{}, nil)
	j := pReturnClaim(t, f, workID)
	for _, label := range []string{"first-change", "second-change"} {
		conformanceImplementChange(t, j, label)
		state, err := loadWorkflowReviewState(t.Context(), f.store.db, workID)
		if err != nil {
			t.Fatal(err)
		}
		if state.Accepted != nil {
			t.Fatalf("ordinary implementation manufactured a Review: %+v", state.Accepted)
		}
		reviewErr := workflowAdmitReview(state)
		_ = planAdmissionFailure(t, reviewErr, KindMissingEvidence)
		if got := workflowPlanFixtureFrontier(t, f.store, workID); got != "implement" {
			t.Fatalf("ordinary implementation advanced to %q", got)
		}
	}
	if _, err := f.store.db.Exec(`INSERT INTO write_guard(active) VALUES(1); UPDATE workflow_contracts SET spec_mandate='["law:required"]' WHERE work_id=? AND superseded_by IS NULL; DELETE FROM write_guard`, workID); err != nil {
		t.Fatal(err)
	}
	report := currentReviewBlockForTest(t, f.store, workID, "ready_for_final_proof")
	payload := mustJSONValue(map[string]any{"review": report})
	version := readWorkVersion(t, f.store, workID)
	request := planAdmissionRequest(workID, "record_review", version, f.owner, payload)
	pin, err := ReadWorkPin(t.Context(), f.store, workID)
	if err != nil || !workPinContainsAction(pin.NextValidIntents, "record_review") || !workPinContainsAction(pin.NextValidIntents, "bind_evidence") {
		t.Fatalf("plan intents = %+v, err=%v", pin.NextValidIntents, err)
	}
	first := planAdmissionFailure(t, planAdmissionPreflight(t, f.store, request), KindMissingEvidence)
	second := planAdmissionFailure(t, planAdmissionDispatch(t, f.store, request), KindMissingEvidence)
	if first.Detail != second.Detail || !strings.Contains(first.Detail, "law:required") || readWorkVersion(t, f.store, workID) != version {
		t.Fatalf("mandate refusal or rollback: preflight=%v dispatch=%v", first, second)
	}
	bind := planAdmissionRequest(workID, "bind_evidence", version, f.owner, json.RawMessage(`{"evidence_kind":"artifact","immutable_subject_ref":"law:required"}`))
	if err := planAdmissionPreflight(t, f.store, bind); err != nil {
		t.Fatalf("binding preflight: %v", err)
	}
	if err := planAdmissionDispatch(t, f.store, bind); err != nil {
		t.Fatalf("binding dispatch: %v", err)
	}
	request = planAdmissionRequest(workID, "record_review", readWorkVersion(t, f.store, workID), f.owner, payload)
	if err := planAdmissionPreflight(t, f.store, request); err != nil {
		t.Fatalf("bound review preflight: %v", err)
	}
	if err := planAdmissionDispatch(t, f.store, request); err != nil {
		t.Fatalf("bound review dispatch: %v", err)
	}
	state, err := loadWorkflowReviewState(t.Context(), f.store.db, workID)
	if err != nil || state.Accepted == nil || state.Accepted.ReportDigest != reviewDigest(report) {
		t.Fatalf("recorded Review = %+v, err=%v", state.Accepted, err)
	}
	if got := workflowPlanFixtureFrontier(t, f.store, workID); got != "implement" {
		t.Fatalf("recording Review advanced to %q", got)
	}
}

func TestWorkflowPlanAdmissionVerdictContractAndEvidence(t *testing.T) {
	for _, tc := range []struct {
		name, predicate string
		removeContract  bool
		kind            FailureKind
	}{
		{"missing-contract", "predicate:return-route", true, KindInvariantViolation},
		{"unapproved-predicate", "predicate:unapproved", false, KindInvalidPayload},
		{"missing-candidate", "predicate:return-route", false, KindMissingEvidence},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workID := "plan-verdict-" + tc.name
			f := seedWorkflowReturnRouteFixtureWithPlan(t, workID, WorkflowStepPlanChange, "review", []string{}, nil)
			if tc.removeContract {
				if _, err := f.store.db.Exec(`INSERT INTO write_guard(active) VALUES(1); UPDATE workflow_contracts SET superseded_by=1 WHERE work_id=?; DELETE FROM write_guard`, workID); err != nil {
					t.Fatal(err)
				}
			}
			version := readWorkVersion(t, f.store, workID)
			payload := mustJSONValue(map[string]any{"verdicts": []map[string]any{{"predicate_id": tc.predicate, "receipt_refs": []string{"evidence:unbound"}}}})
			request := planAdmissionRequest(workID, "record_verdict", version, f.owner, payload)
			preflight := planAdmissionFailure(t, planAdmissionPreflight(t, f.store, request), tc.kind)
			dispatch := planAdmissionFailure(t, planAdmissionDispatch(t, f.store, request), tc.kind)
			if preflight.Detail != dispatch.Detail || readWorkVersion(t, f.store, workID) != version {
				t.Fatalf("verdict refusal or rollback: preflight=%v dispatch=%v", preflight, dispatch)
			}
		})
	}
}

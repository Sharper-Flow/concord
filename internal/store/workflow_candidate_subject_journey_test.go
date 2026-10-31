package store

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

// candidateReport is test input for bind_evidence, not an execution receipt.
type candidateReport struct {
	OperationRef string
	SubjectRef   string
	Report       VerificationReport
}

type candidateJourney struct {
	fixture        workContextFixture
	s              *Store
	workID         string
	repo           string
	acceptor       WorkflowActor
	reviewReceipts []string
}

func (j *candidateJourney) commit(t *testing.T, author, message string) string {
	t.Helper()
	gitRunStore(t, j.repo, "-c", "user.email="+author, "-c", "user.name="+author, "add", ".")
	gitRunStore(t, j.repo, "-c", "user.email="+author, "-c", "user.name="+author, "commit", "-m", message)
	return j.head(t)
}

func (j *candidateJourney) head(t *testing.T) string {
	t.Helper()
	return runTestGit(t, j.repo, "rev-parse", "HEAD")
}

// observeRun builds a report from test data. Concord does not observe Git when
// it binds or consumes the report.
func (j *candidateJourney) observeRun(t *testing.T, id string) candidateReport {
	t.Helper()
	commit := j.head(t)
	ref := "commit:" + commit
	return candidateReport{OperationRef: ref, SubjectRef: ref, Report: VerificationReport{Source: "local", Commit: commit, Result: "pass", RunRef: "run:" + id}}
}

func (j *candidateJourney) observe(t *testing.T, id string) string {
	t.Helper()
	return j.observeRun(t, id).Report.Commit
}

func (j *candidateJourney) bindVerification(t *testing.T, report candidateReport) {
	t.Helper()
	actor := j.acceptor
	if actor.PrincipalRef == "" {
		actor = j.fixture.owner
	}
	if err := j.action(t, "bind_evidence", map[string]any{"evidence_kind": "verification", "immutable_subject_ref": report.SubjectRef, "verification_report": report.Report}, actor); err != nil {
		t.Fatalf("bind verification report: %v", err)
	}
}

func (j *candidateJourney) action(t *testing.T, action string, payload any, actor WorkflowActor) error {
	t.Helper()
	if action == "record_review" || action == "record_verdict" || action == "confirm_premise" || action == "complete" {
		return j.terminalAction(t, action, mustJSONValue(payload), actor)
	}
	return runVerdictActionAs(t, j.s, j.workID, action, mustJSONValue(payload), 0, actor)
}

func (j *candidateJourney) terminalAction(t *testing.T, action string, payload json.RawMessage, actor WorkflowActor) error {
	t.Helper()
	ctx, err := prepareTerminalTestContext(t.Context(), j.s, j.workID, action)
	if err != nil {
		return err
	}
	return j.terminalActionContext(t, ctx, action, payload, actor)
}

func (j *candidateJourney) terminalActionContext(t *testing.T, ctx context.Context, action string, payload json.RawMessage, actor WorkflowActor) error {
	t.Helper()
	if action == "complete" && knowledgeSourceSetProofFrom(ctx) == nil {
		var err error
		ctx, err = j.s.EstablishKnowledgeSourceSetProof(ctx, j.workID)
		if err != nil {
			return err
		}
	}
	version := readWorkVersion(t, j.s, j.workID)
	id := fmt.Sprintf("%s-%s-%d", action, j.workID, version)
	return j.s.Transact(ctx, func(tx *Transaction) error {
		_, err := ApplyWorkflowActionTx(ctx, tx, BuiltinWorkflowRegistry(), WorkflowActionExecutionRequest{
			WorkID: j.workID, ExpectedVersion: version, ActionID: action, Payload: payload, Actor: actor,
			EvidenceRefs: actionEvidenceRefs(action, payload), AcceptedInputsDigest: nativeDigest([]byte(id)), IdempotencyIdentity: id,
			OperationID: id, PrincipalRef: actor.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: id,
			RequestID: "request:" + id, ContractDigest: testManifestDigest, Now: time.Now().UTC(),
		})
		return err
	})
}

func (j *candidateJourney) pinned(t *testing.T) string {
	t.Helper()
	state, err := readWorkflowReviewFacts(t.Context(), j.s.db, j.workID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Candidate == nil {
		return ""
	}
	return state.Candidate.Commit
}

func (j *candidateJourney) effects(t *testing.T) string {
	t.Helper()
	return outsideRepairDatabaseSnapshot(t, j.s)
}

func TestManagedCandidateSubjectReviewNeverRelabelsOldProof(t *testing.T) {
	f, repo, refs := pReceiptFixture(t, "candidate-review-old-report", 1, 1)
	report := currentReviewBlockForTest(t, f.store, f.workID, "ready_for_final_proof")
	report.OutputCandidate.Commit = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	report.InputCandidate = report.OutputCandidate
	report.Assessments = []ReviewAssessment{{PredicateID: "predicate:p0", Assessment: "supported", ReceiptRefs: refs[0]}}
	_ = repo
	if err := validateReviewAssessmentReceiptSubjects(t.Context(), f.store.db, f.workID, report); !hasFailureKind(err, KindStaleRequiresReview) {
		t.Fatalf("Review relabelled a report for another commit: %v", err)
	}
}

func TestManagedCandidateSubjectStaleProofNeverAuthorizesB(t *testing.T) {
	f, _, refs := pReceiptFixture(t, "candidate-stale-report", 1, 1)
	candidate := &ReviewCandidate{Commit: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}
	if err := workflowAcceptanceSubjectCurrent(t.Context(), f.store.db, f.workID, candidate, refs[0]); !hasFailureKind(err, KindStaleRequiresReview) {
		t.Fatalf("acceptance admitted a report for another candidate: %v", err)
	}
}

func TestManagedCandidateSubjectFreshIndependentEvidenceAcceptsB(t *testing.T) {
	f, _, refs := pReceiptFixture(t, "candidate-current-report", 1, 1)
	state, err := loadWorkflowReviewState(t.Context(), f.store.db, f.workID)
	if err != nil || state.Candidate == nil {
		t.Fatalf("recorded Review = %+v, %v", state, err)
	}
	if err := workflowAcceptanceSubjectCurrent(t.Context(), f.store.db, f.workID, state.Candidate, refs[0]); err != nil {
		t.Fatalf("matching passing report refused: %v", err)
	}
	if err := runVerdictActionAs(t, f.store, f.workID, "record_verdict", pReceiptBatch(refs, 0), 0, f.owner); err != nil {
		t.Fatalf("associate independently reviewed report: %v", err)
	}
	seedComparisonObservation(t, f.store, f.workID)
	if err := pConfirmReceiptPremise(t, f); err != nil {
		t.Fatalf("confirm the reviewed candidate and report: %v", err)
	}
	bindCompletionClosure(t, f.store, f.workID, f.owner)
	j := &candidateJourney{fixture: f, s: f.store, workID: f.workID}
	if err := j.terminalAction(t, "complete", json.RawMessage(`{"impact_verdict":"non-breaking"}`), f.owner); err != nil {
		t.Fatalf("complete the reviewed candidate: %v", err)
	}
}

func TestManagedCandidateSubjectCorrectiveReviewOnB(t *testing.T) {
	c := acceptDeliveryCandidateAt(t, "candidate-corrective-report", "workflow.break_fix", "verify", false)
	acceptCurrentReviewForTest(t, c.f, "correction_required")
	run := c.j.observeRun(t, "correction")
	c.j.bindVerification(t, run)
	const diagnosis = "The candidate needs a bounded corrective pass"
	const strategy = "Correct the reviewed candidate"
	if err := c.j.action(t, "request_correction", map[string]any{"diagnosis": diagnosis, "strategy": strategy, "predicate_ids": []string{"predicate:return-route"}, "evidence_refs": []string{run.OperationRef}}, c.f.owner); err != nil {
		t.Fatalf("request correction: %v", err)
	}
	if step := currentStep(t, c.f.store, c.f.workID); step != "repair" {
		t.Fatalf("step after correction = %q, want repair", step)
	}
	correction, err := readWorkflowCorrectionContext(t.Context(), c.f.store.db, c.f.workID)
	if err != nil || correction == nil || correction.Diagnosis != diagnosis || correction.Strategy != strategy || len(correction.EvidenceRefs) != 1 || correction.EvidenceRefs[0] != run.OperationRef {
		t.Fatalf("correction context = %+v, %v", correction, err)
	}
	acceptCurrentReviewForTest(t, c.f, "ready_for_final_proof")
	if remaining, err := readWorkflowCorrectionContext(t.Context(), c.f.store.db, c.f.workID); err != nil || remaining != nil {
		t.Fatalf("corrective Review left direction %+v, %v", remaining, err)
	}
}

func TestManagedCandidateSubjectLateReviewNeverReplacesNewerCandidate(t *testing.T) {
	a := ReviewCandidate{ProjectID: "project:one", Commit: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Tree: "cccccccccccccccccccccccccccccccccccccccc"}
	b := ReviewCandidate{ProjectID: a.ProjectID, Commit: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Tree: "dddddddddddddddddddddddddddddddddddddddd"}
	report := &ReviewBlock{InputCandidate: a, OutputCandidate: b}
	if err := validateReviewCandidates(report, &b); !hasFailureKind(err, KindStaleRequiresReview) {
		t.Fatalf("late corrective Review accepted a superseded input: %v", err)
	}
}

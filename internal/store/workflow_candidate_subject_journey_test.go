package store

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const candidateJourneyHarness = `package oracle
import "testing"
func TestValue(t *testing.T) { if Value() != 7 { t.Fatalf("Value() = %d, want 7", Value()) } }
`

type candidateJourney struct {
	fixture        workContextFixture
	s              *Store
	workID         string
	repo           string
	acceptor       WorkflowActor
	bundle         NativeOracleControlBundle
	launches       int
	reviewReceipts []string
}

func newCandidateJourney(t *testing.T, workID string) *candidateJourney {
	t.Helper()
	f := seedWorkContextFixtureWithDefinition(t, workID, legacyApprovalPolicyDefinitionForRef(t, "workflow.break_fix"))
	t.Cleanup(func() { f.store.Close() })
	j := &candidateJourney{fixture: f, s: f.store, workID: workID, repo: t.TempDir()}
	j.acceptor = WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/candidate-acceptor", SessionRef: "session/" + workID + "-acceptor", ActorClass: ActorAgent}
	for name, data := range map[string]string{
		"go.mod":            "module example.invalid/oracle\n\ngo 1.26.0\ntoolchain go1.26.7\n",
		"pkg/value.go":      "package oracle\nfunc Value() int { return 6 }\n",
		"pkg/value_test.go": candidateJourneyHarness,
		"pkg/oracle.json":   `{"kind":"go_top_level_v1","package_cwd":"pkg","test_files":["pkg/value_test.go"],"fixture_files":[],"cases":{"case:fixture":["TestValue"]}}`,
	} {
		path := filepath.Join(j.repo, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	gitRunStore(t, j.repo, "init", "-b", "candidate-branch")
	base := j.commit(t, "author@example.invalid", "pinned harness and defect")
	if _, err := j.s.db.Exec(`INSERT INTO write_guard(active) VALUES(1);INSERT INTO worktree_entries(set_id,project_id,claim_op_id,branch,base_sha,path,repository_id,state,verified_at,git_facts) VALUES(?,'project','candidate-claim','candidate-branch',?,?,?,'active','2026-10-09T00:00:00Z','{}');DELETE FROM write_guard`, WorktreeSetID(workID), base, j.repo, j.repo); err != nil {
		t.Fatal(err)
	}
	j.s.nativeOracleProgramLaunched = func() { j.launches++ }
	if got := j.observe(t, "base"); got != base {
		t.Fatalf("base observation qualified %q, want %q", got, base)
	}
	insertInvestigationGateObservation(t, j.s, workID, "obs:"+strings.Repeat("c", 16), []string{workContextTestRoot})
	raw := mustJSONValue(acceptanceOracleFieldsWithoutPreparationsForTest(t, j.s, workID))
	var oracle AcceptanceOracle
	if err := json.Unmarshal(raw, &oracle); err != nil {
		t.Fatal(err)
	}
	j.bundle = NativeOracleControlBundle{Owner: oracle.Owners[0], Cases: oracle.Cases, Control: oracle.Controls[0]}
	j.bundle.Control.Cwd = "pkg"
	j.bundle.Control.RecipeSource = WorkContextReadingSource{Kind: WorkContextSourceRepositoryFile, ProjectID: "project", Path: "pkg/oracle.json", CommitOID: base}
	j.bundle.Control.Argv = []string{"go", "test", "-count=1", "-run", "^(TestValue)$", "."}
	j.bundle.Control.RequiredEvidenceRole = OracleEvidenceRoleIndependentlyExecuted
	j.bundle.Control.ReadinessEvidenceRefs = nil
	return j
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

func (j *candidateJourney) request(id string, oracle *NativeOracleRequest) WorktreeVerifyRequest {
	r := WorktreeVerifyRequest{WorkID: j.workID, ProjectID: "project", LeaseID: "candidate-" + id, RequestID: "request-" + id, PrincipalRef: "principal/operator", Owner: SessionWorktreeOwner{ClientRef: "client/candidate", AgentRef: "agent/candidate-verify", SessionRef: "session/candidate-verify"}, Runner: ExecGitRunner{}, Oracle: oracle, Now: time.Now().UTC(), SupportedBudgetSeconds: 60}
	if oracle == nil {
		r.Command = []string{"git", "rev-parse", "--verify", "HEAD^{commit}"}
	}
	return r
}

func (j *candidateJourney) observe(t *testing.T, id string) string {
	t.Helper()
	result := j.observeRun(t, id)
	if subject := j.pinned(t); result.SubjectRef != "commit:"+subject {
		t.Fatalf("observe %s produced %q but the core pins %q", id, result.SubjectRef, subject)
	}
	return strings.TrimPrefix(result.SubjectRef, "commit:")
}

func (j *candidateJourney) observeRun(t *testing.T, id string) WorktreeVerifyResult {
	t.Helper()
	result, err := j.s.VerifyWorktree(t.Context(), j.request("observe-"+id, nil))
	if err != nil || result.SubjectRef == "" || result.ExitCode != 0 || result.TrackedFilesChanged {
		t.Fatalf("observe %s: %+v err=%v", id, result, err)
	}
	return result
}

func (j *candidateJourney) prepare(t *testing.T, id string) WorktreeVerifyResult {
	t.Helper()
	bundle := j.bundle
	result, err := j.s.VerifyWorktree(t.Context(), j.request("prepare-"+id, &NativeOracleRequest{Phase: "prepare", ExpectedContractVersion: 1, ControlBundle: &bundle}))
	if err != nil || result.Oracle == nil || result.Oracle.Qualification != "ready" {
		t.Fatalf("prepare %s: %+v err=%v", id, result.Oracle, err)
	}
	return result
}

func (j *candidateJourney) action(t *testing.T, action string, payload any, actor WorkflowActor) error {
	t.Helper()
	if WorkflowActionRequiresSubjectFreshness(action) {
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
	return j.terminalActionContext(t, ctx, action, payload, actor, nil)
}

func (j *candidateJourney) terminalActionContext(t *testing.T, ctx context.Context, action string, payload json.RawMessage, actor WorkflowActor, operator *WorkflowActor) error {
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
	choice := ""
	if action == "confirm_premise" {
		choice = "confirm"
	}
	return j.s.Transact(ctx, func(tx *Transaction) error {
		_, err := ApplyWorkflowActionTx(ctx, tx, BuiltinWorkflowRegistry(), WorkflowActionExecutionRequest{
			WorkID: j.workID, ExpectedVersion: version, ActionID: action, Payload: payload, Actor: actor, OperatorActor: operator, SelectedChoice: choice,
			EvidenceRefs: actionEvidenceRefs(action, payload), AcceptedInputsDigest: nativeDigest([]byte(id)), IdempotencyIdentity: id,
			OperationID: id, PrincipalRef: actor.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: id,
			RequestID: "request:" + id, ContractDigest: testManifestDigest, Now: time.Now().UTC(),
		})
		return err
	})
}

type candidateAtVerify struct {
	*candidateJourney
	a, b            string
	runA            WorktreeVerifyResult
	preparationBase WorktreeVerifyResult
	snapshotA       map[string]subjectRecord
	reviewA         *ReviewBlock
}

func journeyToRefineA(t *testing.T, workID string) *candidateAtVerify {
	t.Helper()
	j := &candidateAtVerify{candidateJourney: newCandidateJourney(t, workID)}
	if err := j.action(t, "checkpoint_repair", map[string]any{}, j.acceptor); err != nil {
		t.Fatalf("checkpoint_repair: %v", err)
	}
	base := j.head(t)
	j.preparationBase = j.prepare(t, "base")
	if err := os.WriteFile(filepath.Join(j.repo, "pkg/value.go"), []byte("package oracle\nfunc Value() int { return 7 }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	j.a = j.commit(t, "author@example.invalid", "return the approved value")
	if j.a == base {
		t.Fatal("the change did not move the candidate")
	}
	j.observeRun(t, "review-a")
	acceptCurrentReviewForTest(t, j.fixture, j.repo, "ready_for_final_proof")
	delivery := map[string]any{"delivery_artifact": "artifact:candidate-a", "delivery_state": "asserted"}
	j.bindVerification(t, j.observeRun(t, "repair-a"))
	if err := j.action(t, "record_delivery", delivery, j.acceptor); err != nil {
		t.Fatalf("repair delivery over A: %v", err)
	}
	if err := j.action(t, "checkpoint_refine", map[string]any{}, j.acceptor); err != nil {
		t.Fatalf("checkpoint_refine: %v", err)
	}
	return j
}

func journeyThroughA(t *testing.T, workID string, reviewRecorded bool) *candidateAtVerify {
	t.Helper()
	j := journeyToRefineA(t, workID)
	j.runA = j.observeRun(t, "refine-a")
	if j.runA.SubjectRef != "commit:"+j.a {
		t.Fatalf("refine run subject = %q, want A %q", j.runA.SubjectRef, j.a)
	}
	j.bindVerification(t, j.runA)
	if err := j.action(t, "bind_evidence", map[string]any{"evidence_kind": "artifact", "immutable_subject_ref": "commit:" + j.a}, j.acceptor); err != nil {
		t.Fatalf("bind candidate A artifact: %v", err)
	}
	for _, gate := range []string{"refine", "delivery"} {
		if err := j.action(t, "record_delivery", map[string]any{"delivery_artifact": "artifact:candidate-a", "delivery_state": "asserted"}, j.acceptor); err != nil {
			t.Fatalf("%s delivery over A: %v", gate, err)
		}
	}
	if got := currentStep(t, j.s, j.workID); got != "verify" {
		t.Fatalf("step after delivery over A = %q, want verify", got)
	}
	j.reviewA = currentReviewBlockForTest(t, j.s, j.workID, "", "ready_for_final_proof")
	j.reviewA.Assessments = []ReviewAssessment{{PredicateID: "predicate:work-context", Assessment: "supported", ReceiptRefs: []string{j.runA.OperationRef}}}
	if reviewRecorded {
		if err := recordReviewForTest(t, j.s, j.workID, j.fixture.owner, j.reviewA); err != nil {
			t.Fatalf("record A Review: %v", err)
		}
		if err := j.action(t, "record_verdict", criterionAssociationPayload(j.runA.OperationRef, "predicate:work-context"), j.fixture.owner); err != nil {
			t.Fatalf("record A verdict: %v", err)
		}
	}
	j.snapshotA = j.snapshotSubjectRecords(t, j.a)
	return j
}

func journeyToVerify(t *testing.T, workID string, reviewRecorded bool) *candidateAtVerify {
	t.Helper()
	j := journeyThroughA(t, workID, reviewRecorded)
	j.commitB(t)
	j.refuseStaleExecution(t, "base-on-b", j.preparationBase)
	return j
}

func (j *candidateAtVerify) commitB(t *testing.T) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(j.repo, "pkg/value.go"), []byte("package oracle\n\nfunc Value() int { return 7 }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	j.b = j.commit(t, "author@example.invalid", "changed candidate")
	if j.b == j.a || j.pinned(t) != j.a {
		t.Fatal("fixture must change HEAD without changing the qualified A pin")
	}
}

func (j *candidateAtVerify) observeB(t *testing.T) {
	t.Helper()
	if got := j.observe(t, "b"); got != j.b {
		t.Fatalf("B observation pinned %q, want %q", got, j.b)
	}
}

func (j *candidateAtVerify) refuse(t *testing.T, route string, run func() error) {
	t.Helper()
	before := j.effects(t)
	err := run()
	if !hasFailureKind(err, KindStaleRequiresReview) && !(route == "confirm_premise" && hasFailureKind(err, KindMissingEvidence)) {
		t.Errorf("%s refusal = %v, want stale_requires_review", route, err)
	}
	if after := j.effects(t); after != before {
		t.Errorf("%s changed durable state:\nbefore=%s\nafter=%s", route, before, after)
	}
}

func (j *candidateAtVerify) confirmPremise(t *testing.T) error {
	t.Helper()
	return j.terminalAction(t, "confirm_premise", json.RawMessage(`{"contract_version":1}`), j.fixture.owner)
}

func (j *candidateAtVerify) completeOrdinary(t *testing.T) error {
	t.Helper()
	return j.terminalAction(t, "complete", json.RawMessage(`{"impact_verdict":"non-breaking"}`), j.fixture.owner)
}

func (j *candidateAtVerify) staleAcceptance(t *testing.T) {
	t.Helper()
	j.refuse(t, "confirm_premise", func() error { return j.confirmPremise(t) })
	if currentStep(t, j.s, j.workID) == "complete" {
		j.refuse(t, "ordinary complete", func() error { return j.completeOrdinary(t) })
	}
}

func TestManagedCandidateSubjectStaleProofNeverAuthorizesB(t *testing.T) {
	j := journeyToVerify(t, "candidate-stale-accepted", true)
	j.observeB(t)
	j.staleAcceptance(t)
	j.replayProducer(t, "observe-refine-a", j.a, j.b)
	j.requireRecordsUnchanged(t, j.snapshotA)
}

func TestManagedCandidateSubjectUnobservedHeadNeverCompletes(t *testing.T) {
	j := journeyToVerify(t, "candidate-unobserved", true)
	j.staleAcceptance(t)
	if pinned := j.pinned(t); pinned != j.a {
		t.Errorf("acceptance calls moved the pin to %q without a B observation", pinned)
	}
	j.requireRecordsUnchanged(t, j.snapshotA)
}

func TestManagedCandidateSubjectCompleteRechecksObservedSubject(t *testing.T) {
	j := journeyThroughA(t, "candidate-complete-drift", true)
	if err := j.confirmPremise(t); err != nil {
		t.Fatalf("confirm A before subject movement: %v", err)
	}
	j.commitB(t)
	j.observeB(t)
	j.refuse(t, "ordinary complete after confirmation of A", func() error { return j.completeOrdinary(t) })
	j.requireRecordsUnchanged(t, j.snapshotA)
}

func TestManagedCandidateSubjectDelayedReviewRequiresObservedCandidate(t *testing.T) {
	for _, movement := range []string{"head_moved", "pin_removed"} {
		t.Run(movement, func(t *testing.T) {
			j := journeyThroughA(t, "candidate-delayed-review-"+movement, false)
			if movement == "head_moved" {
				j.commitB(t)
				j.observeB(t)
			} else {
				j.reclaimWorktree(t)
			}
			before := j.effects(t)
			if err := recordReviewForTest(t, j.s, j.workID, j.fixture.owner, j.reviewA); !hasFailureKind(err, KindStaleRequiresReview) {
				t.Fatalf("delayed A Review after %s: %v", movement, err)
			}
			if j.effects(t) != before {
				t.Fatal("refused delayed Review changed durable state")
			}
			j.requireRecordsUnchanged(t, j.snapshotA)
		})
	}
}

func TestManagedCandidateSubjectReviewNeverRelabelsOldProof(t *testing.T) {
	j := journeyToVerify(t, "candidate-review-old-proof", false)
	j.observeB(t)
	report := currentReviewBlockForTest(t, j.s, j.workID, "", "ready_for_final_proof")
	report.Assessments = j.reviewA.Assessments
	before := j.effects(t)
	if err := recordReviewForTest(t, j.s, j.workID, j.fixture.owner, report); err == nil {
		t.Fatal("B Review admitted A's criterion receipt")
	}
	if j.effects(t) != before {
		t.Fatal("refused receipt substitution changed durable state")
	}
	j.requireRecordsUnchanged(t, j.snapshotA)
}

func TestManagedCandidateSubjectLateReviewNeverReplacesNewerCandidate(t *testing.T) {
	j := journeyWithIntegratedB(t, "candidate-late-review")
	before := j.effects(t)
	if err := recordReviewForTest(t, j.s, j.workID, j.fixture.owner, j.reviewA); !hasFailureKind(err, KindStaleRequiresReview) {
		t.Fatalf("late A Review over recorded B = %v, want stale_requires_review", err)
	}
	if j.effects(t) != before {
		t.Fatal("refused late Review changed durable state")
	}
	j.requireAcceptedReview(t, j.b)
	j.requireRecordsUnchanged(t, j.snapshotA)
}

func TestManagedCandidateSubjectMissingPinNeverAccepts(t *testing.T) {
	for _, gate := range []string{"confirm_premise", "complete"} {
		t.Run(gate, func(t *testing.T) {
			j := journeyThroughA(t, "candidate-missing-pin-"+gate, true)
			if gate == "complete" {
				if err := j.confirmPremise(t); err != nil {
					t.Fatalf("confirm A before pin disappears: %v", err)
				}
			}
			j.reclaimWorktree(t)
			j.refuse(t, gate+" with no current pin", func() error {
				if gate == "confirm_premise" {
					return j.confirmPremise(t)
				}
				return j.completeOrdinary(t)
			})
			j.requireRecordsUnchanged(t, j.snapshotA)
		})
	}
}

func (j *candidateAtVerify) reclaimWorktree(t *testing.T) {
	t.Helper()
	if _, err := j.s.db.Exec(`UPDATE worktree_entries SET state='reclaimed' WHERE set_id=?`, WorktreeSetID(j.workID)); err != nil {
		t.Fatal(err)
	}
	if got := j.pinned(t); got != "" {
		t.Fatalf("pin after reclaim = %q, want empty", got)
	}
}

func (j *candidateJourney) requireAcceptedReview(t *testing.T, candidate string) {
	t.Helper()
	state, err := loadWorkflowReviewState(t.Context(), j.s.db, j.workID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Accepted == nil || state.Accepted.ReportRef == "" || state.Candidate == nil || state.Candidate.Commit != candidate || state.Accepted.Report.OutputCandidate != *state.Candidate {
		t.Fatalf("accepted Review = %+v candidate = %+v, want exact output %s", state.Accepted, state.Candidate, candidate)
	}
}

func journeyWithIntegratedB(t *testing.T, workID string) *candidateAtVerify {
	t.Helper()
	j := journeyThroughA(t, workID, true)
	j.b, _ = j.completeCorrectiveReview(t, "review.txt", "corrected candidate\n", "integrated-b")
	if j.b == j.a {
		t.Fatal("the corrective Review did not change A")
	}
	j.requireAcceptedReview(t, j.b)
	j.requireRecordsUnchanged(t, j.snapshotA)
	return j
}

func TestManagedCandidateSubjectFreshIndependentEvidenceAcceptsB(t *testing.T) {
	j := journeyWithIntegratedB(t, "candidate-fresh-b")
	preparation := j.prepare(t, "b")
	if preparation.Oracle.SubjectCommit != j.b {
		t.Fatalf("B preparation subject = %q, want %q", preparation.Oracle.SubjectCommit, j.b)
	}
	launches := j.launches
	oracle := j.executeRequest(preparation)
	executed, err := j.s.VerifyWorktree(t.Context(), j.request("execute-b", &oracle))
	if err != nil || executed.Oracle == nil || executed.Oracle.Qualification != "pass" || executed.Oracle.SubjectCommit != j.b || j.launches != launches+1 {
		t.Fatalf("execute native oracle on B: %+v err=%v launches=%d", executed.Oracle, err, j.launches-launches)
	}
	report := currentReviewBlockForTest(t, j.s, j.workID, "", "ready_for_final_proof")
	report.Assessments = []ReviewAssessment{{PredicateID: "predicate:work-context", Assessment: "supported", ReceiptRefs: []string{executed.OperationRef}}}
	if err := recordReviewForTest(t, j.s, j.workID, j.fixture.owner, report); err != nil {
		t.Fatalf("record B's receipt-bound Review: %v", err)
	}
	j.requireAcceptedReview(t, j.b)
	j.bindVerification(t, executed)
	if err := j.action(t, "record_verdict", criterionAssociationPayload(executed.OperationRef, "predicate:work-context"), j.fixture.owner); err != nil {
		t.Fatalf("associate B's native oracle receipt: %v", err)
	}
	if err := j.confirmPremise(t); err != nil {
		t.Fatalf("confirm premise over B: %v", err)
	}
	bindCompletionClosure(t, j.s, j.workID, j.fixture.owner)
	if err := j.completeOrdinary(t); err != nil {
		t.Fatalf("complete over B: %v", err)
	}
	var state string
	if err := j.s.db.QueryRow(`SELECT instance_state FROM workflow_instances WHERE work_id=?`, j.workID).Scan(&state); err != nil || state != "completed" {
		t.Fatalf("instance state = %q err=%v, want completed", state, err)
	}
	if got := j.pinned(t); got != j.b {
		t.Errorf("final pin = %q, want B %q", got, j.b)
	}
	j.requireRecordsUnchanged(t, j.snapshotA)
}

func TestManagedCandidateSubjectCorrectiveReviewOnB(t *testing.T) {
	j := journeyWithIntegratedB(t, "candidate-corrective-b")
	acceptCurrentReviewForTest(t, j.fixture, j.repo, "correction_required")
	j.requireAcceptedReview(t, j.b)
	run := j.observeRun(t, "correction-b")
	j.bindVerification(t, run)
	const diagnosis = "B needs a bounded corrective pass"
	const strategy = "correct B under its pinned oracle"
	if err := j.action(t, "request_correction", map[string]any{"diagnosis": diagnosis, "strategy": strategy, "predicate_ids": []string{"predicate:work-context"}, "evidence_refs": []string{run.OperationRef}}, j.fixture.owner); err != nil {
		t.Fatalf("request correction of B: %v", err)
	}
	if step := currentStep(t, j.s, j.workID); step != "repair" {
		t.Fatalf("step after correction = %q, want repair", step)
	}
	before, err := workflowCorrectionContext(t.Context(), j.s.db, j.workID, "repair")
	if err != nil || before == nil || before.Disposition != "verification" || before.Diagnosis != diagnosis || before.Strategy != strategy || len(before.PredicateIDs) != 1 || before.PredicateIDs[0] != "predicate:work-context" || len(before.EvidenceRefs) != 1 || before.EvidenceRefs[0] != run.OperationRef {
		t.Fatalf("correction context = %+v err=%v, want this request's direction and B evidence", before, err)
	}
	if got := j.pinned(t); got != j.b {
		t.Fatalf("request_correction moved B pin to %q", got)
	}
	acceptCurrentReviewForTest(t, j.fixture, j.repo, "ready_for_final_proof")
	if after, err := workflowCorrectionContext(t.Context(), j.s.db, j.workID, "repair"); err != nil || after != nil {
		t.Fatalf("recorded corrective Review left unfinished direction: %+v %v", after, err)
	}
	j.requireRecordsUnchanged(t, j.snapshotA)
}

func (j *candidateJourney) bindVerification(t *testing.T, run WorktreeVerifyResult) {
	t.Helper()
	if err := j.action(t, "bind_evidence", map[string]any{
		"evidence_kind": "verification", "immutable_subject_ref": run.OperationRef, "producer_id": "principal/operator",
		"producer_run_ref": run.OperationRef, "producer_watermark": "request-" + strings.TrimPrefix(run.LeaseID, "candidate-"),
	}, j.acceptor); err != nil {
		t.Fatalf("bind verification %s: %v", run.OperationRef, err)
	}
}

func (j *candidateJourney) completeCorrectiveReview(t *testing.T, path, content, observeID string) (string, WorktreeVerifyResult) {
	t.Helper()
	input := currentReviewBlockForTest(t, j.s, j.workID, "", "ready_for_final_proof").InputCandidate
	if err := os.MkdirAll(filepath.Join(j.repo, filepath.Dir(path)), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(j.repo, path), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	output := j.commit(t, "reviewer@example.invalid", "corrective review pass")
	run := j.observeRun(t, observeID)
	if run.SubjectRef != "commit:"+output {
		t.Fatalf("corrected candidate run = %q, want %q", run.SubjectRef, output)
	}
	report := currentReviewBlockForTest(t, j.s, j.workID, "", "ready_for_final_proof")
	report.InputCandidate = input
	if err := recordReviewForTest(t, j.s, j.workID, j.fixture.owner, report); err != nil {
		t.Fatalf("record corrective Review: %v", err)
	}
	return output, run
}

func (j *candidateJourney) pinned(t *testing.T) string {
	t.Helper()
	subject, err := readCurrentOracleSubject(t.Context(), j.s.db, j.workID)
	if err != nil {
		t.Fatal(err)
	}
	return subject
}

func (j *candidateJourney) effects(t *testing.T) string {
	t.Helper()
	return outsideRepairDatabaseSnapshot(t, j.s)
}

type subjectRecord struct{ id, kind, payload string }

// Current Review projections may advance. Effect identities and digests, and
// captured native producer bytes, must not change when the candidate does.
func (j *candidateJourney) snapshotSubjectRecords(t *testing.T, subject string) map[string]subjectRecord {
	t.Helper()
	records := map[string]subjectRecord{}
	rows, err := j.s.db.Query(`SELECT event_id,kind||'@'||payload_version,payload_digest FROM effect_receipts WHERE subject_type='work_item' AND subject_id=?`, j.workID)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var r subjectRecord
		if err := rows.Scan(&r.id, &r.kind, &r.payload); err != nil {
			t.Fatal(err)
		}
		records["event:"+r.id] = r
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	producers, err := j.s.db.Query(`SELECT lease_id,outcome,coalesce(result_json,'') FROM worktree_verify_leases WHERE work_id=?`, j.workID)
	if err != nil {
		t.Fatal(err)
	}
	defer producers.Close()
	for producers.Next() {
		var r subjectRecord
		if err := producers.Scan(&r.id, &r.kind, &r.payload); err != nil {
			t.Fatal(err)
		}
		records["lease:"+r.id] = r
	}
	if err := producers.Err(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(records["lease:candidate-observe-refine-a"].payload, "commit:"+subject) {
		t.Fatalf("snapshot holds no qualified producer for %s", subject)
	}
	return records
}

func (j *candidateJourney) requireRecordsUnchanged(t *testing.T, snapshot map[string]subjectRecord) {
	t.Helper()
	current := j.snapshotSubjectRecords(t, strings.TrimPrefix(subjectOfLease(t, snapshot["lease:candidate-observe-refine-a"]), "commit:"))
	for key, want := range snapshot {
		got, ok := current[key]
		if !ok {
			t.Errorf("retained record %s disappeared", key)
		} else if got != want {
			t.Errorf("retained record %s changed:\nbefore=%s %s\nafter=%s %s", key, want.kind, want.payload, got.kind, got.payload)
		}
	}
}

func subjectOfLease(t *testing.T, record subjectRecord) string {
	t.Helper()
	var result WorktreeVerifyResult
	if err := json.Unmarshal([]byte(record.payload), &result); err != nil {
		t.Fatal(err)
	}
	return result.SubjectRef
}

func (j *candidateJourney) executeRequest(preparation WorktreeVerifyResult) NativeOracleRequest {
	return NativeOracleRequest{Phase: "execute", PreparationRunRef: preparation.OperationRef}
}

func (j *candidateJourney) refuseStaleExecution(t *testing.T, id string, preparation WorktreeVerifyResult) {
	t.Helper()
	launches := j.launches
	oracle := j.executeRequest(preparation)
	_, err := j.s.VerifyWorktree(t.Context(), j.request("stale-execute-"+id, &oracle))
	if err == nil {
		t.Errorf("stale preparation %s authorized execution on the changed HEAD", id)
	}
	if j.launches != launches {
		t.Errorf("stale execution %s launched %d test programs", id, j.launches-launches)
	}
}

func (j *candidateJourney) replayProducer(t *testing.T, id, subject, pinned string) {
	t.Helper()
	launches := j.launches
	var before string
	if err := j.s.db.QueryRow(`SELECT result_json FROM worktree_verify_leases WHERE lease_id=?`, "candidate-"+id).Scan(&before); err != nil {
		t.Fatal(err)
	}
	replay, err := j.s.VerifyWorktree(t.Context(), j.request(id, nil))
	if err != nil || replay.SubjectRef != "commit:"+subject {
		t.Errorf("replay of %s = %q err=%v, want recorded subject %s", id, replay.SubjectRef, err, subject)
	}
	var after string
	if err := j.s.db.QueryRow(`SELECT result_json FROM worktree_verify_leases WHERE lease_id=?`, "candidate-"+id).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before || j.launches != launches || j.pinned(t) != pinned {
		t.Errorf("replay of %s changed its producer, launched %d programs, or moved the pin to %q", id, j.launches-launches, j.pinned(t))
	}
}

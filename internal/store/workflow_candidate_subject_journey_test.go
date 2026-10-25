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

// The managed candidate journey: an Implement assignment produces subject A
// and Main records A as its integrated candidate. The candidate moves only
// through a recorded integration of an assignment result, never through live
// HEAD or the native pin (CD-0242 D5, D7). A record about A stays true of A,
// byte for byte, and authorizes nothing about a changed subject (D7).

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
	// The journey pins the current break-fix definition, under which Main
	// confirms and completes with no operator substitution.
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
	base := j.commit(t, "worker@example.invalid", "pinned harness and defect")
	if _, err := j.s.db.Exec(`INSERT INTO fold_guard(active) VALUES(1);INSERT INTO worktree_entries(set_id,project_id,claim_op_id,branch,base_sha,path,repository_id,state,verified_at,git_facts) VALUES(?,'project','candidate-claim','candidate-branch',?,?,?,'active','2026-10-09T00:00:00Z','{}');DELETE FROM fold_guard`, WorktreeSetID(workID), base, j.repo, j.repo); err != nil {
		t.Fatal(err)
	}
	j.s.nativeOracleProgramLaunched = func() { j.launches++ }
	if got := j.observe(t, "base"); got != base {
		t.Fatalf("base observation qualified %q, want %q", got, base)
	}
	// The confirmation question needs a recorded investigation artifact that
	// names a current Domain of the Product.
	insertInvestigationGateObservation(t, j.s, workID, "obs:"+strings.Repeat("c", 16), []string{workContextTestRoot})
	raw, err := json.Marshal(acceptanceOracleFieldsWithoutPreparationsForTest(t, j.s, workID))
	if err != nil {
		t.Fatal(err)
	}
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

// commit records the working tree as one commit by the named Git author.
// The Git identity is synthetic and carries no workflow authority.
func (j *candidateJourney) commit(t *testing.T, author, message string) string {
	t.Helper()
	gitRunStore(t, j.repo, "-c", "user.email="+author, "-c", "user.name="+author, "add", ".")
	gitRunStore(t, j.repo, "-c", "user.email="+author, "-c", "user.name="+author, "commit", "-m", message)
	return j.head(t)
}

func (j *candidateJourney) head(t *testing.T) string {
	t.Helper()
	out, err := (ExecGitRunner{}).Run(context.Background(), j.repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

func (j *candidateJourney) request(id string, o *NativeOracleRequest) WorktreeVerifyRequest {
	r := WorktreeVerifyRequest{WorkID: j.workID, ProjectID: "project", LeaseID: "candidate-" + id, RequestID: "request-" + id, PrincipalRef: "principal/operator", Owner: SessionWorktreeOwner{ClientRef: "client/candidate", AgentRef: "agent/candidate-verify", SessionRef: "session/candidate-verify"}, Runner: ExecGitRunner{}, Oracle: o, Now: time.Now().UTC()}
	r.SupportedBudgetSeconds = 60
	if o == nil {
		r.Command = []string{"git", "rev-parse", "--verify", "HEAD^{commit}"}
	}
	return r
}

// observe qualifies the clean HEAD through the native verification owner and
// returns the subject the core now pins.
func (j *candidateJourney) observe(t *testing.T, id string) string {
	t.Helper()
	result := j.observeRun(t, id)
	if subject := j.pinned(t); result.SubjectRef != "commit:"+subject {
		t.Fatalf("observe %s produced %q but the core pins %q", id, result.SubjectRef, subject)
	}
	return strings.TrimPrefix(result.SubjectRef, "commit:")
}

func (j *candidateJourney) prepare(t *testing.T, id string) WorktreeVerifyResult {
	t.Helper()
	bundle := j.bundle
	result, err := j.s.VerifyWorktree(context.Background(), j.request("prepare-"+id, &NativeOracleRequest{Phase: "prepare", ExpectedContractVersion: 1, ControlBundle: &bundle}))
	if err != nil || result.Oracle == nil || result.Oracle.Qualification != "ready" {
		t.Fatalf("prepare %s: %+v err=%v", id, result.Oracle, err)
	}
	return result
}

// recordReadyJob records one ready worker-job revision through the
// record_worker_job action. Its oracle carries exactly the named preparation.
func (j *candidateJourney) recordReadyJob(t *testing.T, jobID string, preparation WorktreeVerifyResult) WorkerJobBinding {
	t.Helper()
	control := j.bundle.Control
	control.ReadinessEvidenceRefs = []string{preparation.OperationRef}
	oracle, err := json.Marshal(AcceptanceOracle{Owners: []OracleOwner{j.bundle.Owner}, Cases: j.bundle.Cases, Controls: []OracleControl{control}})
	if err != nil {
		t.Fatal(err)
	}
	fields := map[string]any{
		"job_id": jobID, "objective": "return the approved value from the pinned harness",
		"stopping_condition": "the pinned harness passes on the candidate",
		"path_scope":         []string{"pkg"}, "checks": []string{"go test ./pkg"},
		"reserved_integration": "integration evidence binds at the parent effect step",
		"ready":                true, "readiness_evidence": []string{preparation.OperationRef},
		"acceptance_oracle": json.RawMessage(oracle),
	}
	if err := recordWorkerJobActionForTest(t, j.s, j.workID, j.fixture.owner, fields); err != nil {
		t.Fatalf("record ready job %s: %v", jobID, err)
	}
	views, err := readWorkerJobRevisions(context.Background(), j.s.db, j.workID)
	if err != nil {
		t.Fatal(err)
	}
	var binding WorkerJobBinding
	for _, view := range views {
		if view.Binding.JobID == jobID && view.Binding.Revision > binding.Revision {
			binding = view.Binding
		}
	}
	if binding.Digest == "" {
		t.Fatalf("job %s is not recorded", jobID)
	}
	return binding
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
	ctx := context.Background()
	prepared, err := PrepareWorkflowAcceptanceFreshness(ctx, j.s, PrepareWorkerDispatchRequest{WorkID: j.workID, SessionWorktree: j.repo})
	if err != nil {
		return err
	}
	ctx = WithWorkerDispatchPreparation(ctx, prepared)
	return j.terminalActionContext(t, ctx, action, payload, actor, nil)
}

func (j *candidateJourney) terminalActionContext(t *testing.T, ctx context.Context, action string, payload json.RawMessage, actor WorkflowActor, operator *WorkflowActor) error {
	t.Helper()
	version := readWorkVersion(t, j.s, j.workID)
	id := fmt.Sprintf("%s-%s-%d", action, j.workID, version)
	choice := ""
	if action == "confirm_premise" {
		choice = "confirm"
	}
	return j.s.Transact(ctx, func(transaction *Transaction) error {
		_, err := ApplyWorkflowActionTx(ctx, transaction, BuiltinWorkflowRegistry(), WorkflowActionExecutionRequest{
			WorkID: j.workID, ExpectedVersion: version, ActionID: action, Payload: payload, Actor: actor, OperatorActor: operator, SelectedChoice: choice,
			EvidenceRefs: actionEvidenceRefs(action, payload), AcceptedInputsDigest: nativeDigest([]byte(id)), IdempotencyIdentity: id,
			OperationID: id, PrincipalRef: actor.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: id,
			RequestID: "request:" + id, ContractDigest: testManifestDigest, Now: time.Now().UTC(),
		})
		return err
	})
}

// candidateAtVerify is a journey parked at verify after A was produced,
// accepted, delivered, and reviewed, with an unrecorded B committed.
type candidateAtVerify struct {
	*candidateJourney
	a, b            string
	runA            WorktreeVerifyResult
	jobA            WorkerJobBinding
	preparationBase WorktreeVerifyResult
	snapshotA       map[string]subjectRecord
}

// journeyToVerify drives subject A through the managed route to verify and
// commits an unrecorded B. reviewAccepted selects an A review
// accepted with a healthy verdict, or an A review completed with no
// disposition; host drift need not create a rejection event.
func journeyToVerify(t *testing.T, workID string, reviewAccepted bool) *candidateAtVerify {
	t.Helper()
	j := journeyThroughA(t, workID, reviewAccepted)
	j.commitB(t)
	j.refuseStaleExecution(t, "attempt-candidate-a", j.jobA, j.preparationBase)
	return j
}

func journeyToRefineA(t *testing.T, workID string) *candidateAtVerify {
	t.Helper()
	j := &candidateAtVerify{candidateJourney: newCandidateJourney(t, workID)}
	base := j.head(t)

	// Subject A: a managed implement attempt produces the change.
	j.preparationBase = j.prepare(t, "base")
	j.jobA = j.recordReadyJob(t, "job:candidate", j.preparationBase)
	j.dispatchJob(t, "attempt-candidate-a", "implement", "implementation", j.jobA)
	if err := os.WriteFile(filepath.Join(j.repo, "pkg", "value.go"), []byte("package oracle\nfunc Value() int { return 7 }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	j.a = j.commit(t, "worker@example.invalid", "worker returns the approved value")
	if j.a == base {
		t.Fatal("the worker commit did not change the subject")
	}
	j.completeLane(t, "attempt-candidate-a", "implementation", &j.jobA, nil)
	if err := j.action(t, "accept_worker_result", map[string]any{"attempt_id": "attempt-candidate-a", "attempt_epoch": latestStepStartEpoch(t, j.s, j.workID, "repair")}, j.acceptor); err != nil {
		t.Fatalf("accept A: %v", err)
	}

	// Repair, refine, and delivery over A, each exit proved by a qualified
	// green run of A in its epoch.
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

func journeyThroughA(t *testing.T, workID string, reviewAccepted bool) *candidateAtVerify {
	t.Helper()
	j := journeyToRefineA(t, workID)
	delivery := map[string]any{"delivery_artifact": "artifact:candidate-a", "delivery_state": "asserted"}
	j.runA = j.observeRun(t, "refine-a")
	if j.runA.SubjectRef != "commit:"+j.a {
		t.Fatalf("refine run subject = %q, want A %q", j.runA.SubjectRef, j.a)
	}
	j.bindVerification(t, j.runA)
	for _, gate := range []string{"refine", "delivery"} {
		if err := j.action(t, "record_delivery", delivery, j.acceptor); err != nil {
			t.Fatalf("%s delivery over A: %v", gate, err)
		}
	}
	if got := currentStep(t, j.s, j.workID); got != "verify" {
		t.Fatalf("step after delivery over A = %q, want verify", got)
	}

	// Independent review of A by actors who authored nothing.
	j.reviewReceipts = []string{j.runA.OperationRef}
	j.dispatchReview(t, "attempt-review-a")
	j.completeLane(t, "attempt-review-a", "review", nil, nil)
	if reviewAccepted {
		if err := j.acceptEvidence(t, "attempt-review-a"); err != nil {
			t.Fatalf("accept A review: %v", err)
		}
		if err := j.action(t, "record_verdict", criterionAssociationPayload(j.runA.OperationRef, "predicate:work-context"), j.fixture.owner); err != nil {
			t.Fatalf("record A verdict: %v", err)
		}
	}
	j.snapshotA = j.snapshotSubjectRecords(t, j.a)
	return j
}

func (j *candidateAtVerify) commitB(t *testing.T) {
	t.Helper()
	// An unrecorded commit B moves HEAD in the same worktree. No assignment
	// produced it, so it is not Main's integrated candidate (CD-0242 D5). The
	// approved predicates and pinned harness stay unchanged.
	if err := os.WriteFile(filepath.Join(j.repo, "pkg", "value.go"), []byte("package oracle\n\n// Value returns the approved value.\nfunc Value() int { return 7 }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	j.b = j.commit(t, "unrecorded@example.invalid", "unrecorded commit")
	if j.b == j.a {
		t.Fatal("the unrecorded commit did not change the subject")
	}

	// Before any B observation the core still pins A, and A's execution
	// authority cannot launch a control on B.
	if pinned := j.pinned(t); pinned != j.a {
		t.Fatalf("core pin before B observation = %q, want A %q", pinned, j.a)
	}
}

// observeB qualifies B through the native verification owner. It moves the
// pin and relabels nothing about A.
func (j *candidateAtVerify) observeB(t *testing.T) {
	t.Helper()
	if got := j.observe(t, "b"); got != j.b {
		t.Fatalf("B observation pinned %q, want %q", got, j.b)
	}
}

// refuse runs one stale-proof route and requires a refusal with no durable
// effect.
func (j *candidateAtVerify) refuse(t *testing.T, route string, run func() error) {
	t.Helper()
	before := j.effects(t)
	if err := run(); err == nil {
		t.Errorf("%s over A proof admitted with HEAD at B", route)
	} else {
		var failure *Failure
		// After stale review and verdict refusal, the pending-review branch
		// has no verdict to confirm. That independent evidence refusal is valid.
		if !failureAs(err, &failure) || (failure.Kind != KindStaleRequiresReview && !(route == "confirm_premise" && failure.Kind == KindMissingEvidence)) {
			t.Errorf("%s refusal = %v, want stale_requires_review", route, err)
		}
		t.Logf("%s over A proof refused: %v", route, err)
	}
	if after := j.effects(t); after != before {
		t.Errorf("%s changed durable state:\nbefore=%s\nafter=%s", route, before, after)
	}
}

func (j *candidateAtVerify) confirmPremise(t *testing.T) error {
	t.Helper()
	return j.terminalAction(t, "confirm_premise", json.RawMessage(`{"contract_version":1}`), j.fixture.owner)
}

// completeOrdinary is the ordinary completion call: impact_verdict only, with
// no current_commit, evidence_commit, or asserted staleness.
func (j *candidateAtVerify) completeOrdinary(t *testing.T) error {
	t.Helper()
	return j.terminalAction(t, "complete", json.RawMessage(`{"impact_verdict":"non-breaking"}`), j.fixture.owner)
}

// staleAcceptance drives the declared acceptance routes on A proof alone:
// confirmation, then the ordinary completion call. Each must refuse with no
// durable effect. A route that admits is reported and the next route still
// runs, so one red does not hide another.
func (j *candidateAtVerify) staleAcceptance(t *testing.T) {
	t.Helper()
	j.refuse(t, "confirm_premise", func() error { return j.confirmPremise(t) })
	if currentStep(t, j.s, j.workID) == "complete" {
		j.refuse(t, "ordinary complete", func() error { return j.completeOrdinary(t) })
	}
}

// TestManagedCandidateSubjectStaleProofNeverAuthorizesB proves A's accepted
// review, A's verdict, and A's verification runs cannot accept B, and that
// replaying A's producer returns A unchanged (CD-0242 D7).
func TestManagedCandidateSubjectStaleProofNeverAuthorizesB(t *testing.T) {
	j := journeyToVerify(t, "candidate-stale-accepted", true)
	j.observeB(t)
	j.staleAcceptance(t)
	j.replayProducer(t, "observe-refine-a", j.a, j.b)
	j.requireRecordsUnchanged(t, j.snapshotA)
}

// TestManagedCandidateSubjectUnobservedHeadNeverCompletes drives the ordinary
// acceptance calls while HEAD is B and the core still pins A. The ordinary
// completion call omits every optional commit and staleness assertion.
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

// TestManagedCandidateSubjectReviewOfIntegratedCandidateSurvivesSubjectMovement
// proves Main's integrated candidate, not live HEAD or the native pin, is the
// Review subject (CD-0242 D5, D7): with A still integrated, Main accepts A's
// Review after HEAD moves to an unrecorded B or after the pin disappears, and
// associates A's receipts with the criteria of the A it integrated.
func TestManagedCandidateSubjectReviewOfIntegratedCandidateSurvivesSubjectMovement(t *testing.T) {
	t.Run("head_moved", func(t *testing.T) {
		j := journeyToVerify(t, "candidate-review-head-moved", false)
		j.observeB(t)
		if got := j.pinned(t); got != j.b {
			t.Fatalf("pin after B observation = %q, want B %q", got, j.b)
		}
		if err := j.acceptEvidence(t, "attempt-review-a"); err != nil {
			t.Fatalf("accept A's Review with HEAD at unrecorded B: %v", err)
		}
		j.requireAcceptedReview(t, "attempt-review-a", j.a)
		// Accepting A's Review grants no final proof over B.
		j.staleAcceptance(t)
		j.requireRecordsUnchanged(t, j.snapshotA)
	})
	t.Run("pin_removed", func(t *testing.T) {
		j := journeyThroughA(t, "candidate-review-pin-removed", false)
		j.reclaimWorktree(t)
		if err := j.acceptEvidence(t, "attempt-review-a"); err != nil {
			t.Fatalf("accept A's Review with no current pin: %v", err)
		}
		j.requireAcceptedReview(t, "attempt-review-a", j.a)
		if err := j.action(t, "record_verdict", criterionAssociationPayload(j.runA.OperationRef, "predicate:work-context"), j.fixture.owner); err != nil {
			t.Fatalf("associate A's receipt with no current pin: %v", err)
		}
		j.requireRecordsUnchanged(t, j.snapshotA)
	})
}

// TestManagedCandidateSubjectReviewDispatchRefusesUnrecordedHead proves an
// unrecorded B never becomes the Review input, even once observed and pinned:
// Review dispatch on B refuses with no durable effect (CD-0242 D5).
func TestManagedCandidateSubjectReviewDispatchRefusesUnrecordedHead(t *testing.T) {
	j := journeyToVerify(t, "candidate-review-unrecorded-b", false)
	j.observeB(t)
	job := reviewSubjectReadyRevision(t, j.fixture)
	before := j.effects(t)
	err := j.dispatchReviewJob(t, "attempt-review-b", job)
	var failure *Failure
	if !failureAs(err, &failure) || failure.Kind != KindStaleRequiresReview || !strings.Contains(failure.Detail, "Review input differs from Main's current integrated candidate") {
		t.Fatalf("Review dispatch on unrecorded B = %v, want the integrated-candidate refusal", err)
	}
	if after := j.effects(t); after != before {
		t.Fatalf("refused Review dispatch on B changed durable state:\nbefore=%s\nafter=%s", before, after)
	}
	j.requireRecordsUnchanged(t, j.snapshotA)
}

// TestManagedCandidateSubjectLateReviewNeverReplacesNewerCandidate proves "a
// late or duplicate member result never replaces a newer accepted candidate"
// (CD-0242 D5): a second Review of A commits a corrective change A2, Main
// accepts it, and the late first Review of A then refuses with no durable
// effect while A2 stays the integrated candidate.
func TestManagedCandidateSubjectLateReviewNeverReplacesNewerCandidate(t *testing.T) {
	j := journeyThroughA(t, "candidate-late-review", false)
	job := reviewSubjectReadyRevision(t, j.fixture)
	const corrective = "attempt-review-corrective"
	if err := j.dispatchReviewJob(t, corrective, job); err != nil {
		t.Fatalf("dispatch the corrective Review of A: %v", err)
	}
	if got := j.dispatchedSubject(t, corrective); got != j.a {
		t.Fatalf("corrective Review input = %q, want A %q", got, j.a)
	}
	a2, _ := j.completeCorrectiveReview(t, corrective, job, filepath.Join("internal", "store", job.JobID, "review.txt"), "corrective review note\n", "corrective-a2")
	if err := j.acceptEvidence(t, corrective); err != nil {
		t.Fatalf("accept the corrective Review that integrates A2: %v", err)
	}
	j.requireAcceptedReview(t, corrective, a2)

	before := j.effects(t)
	err := j.acceptEvidence(t, "attempt-review-a")
	var failure *Failure
	if !failureAs(err, &failure) || failure.Kind != KindStaleRequiresReview || !strings.Contains(failure.Detail, "a newer accepted candidate replaced this Review's input") {
		t.Fatalf("late acceptance of A's Review over integrated A2 = %v, want the newer-candidate refusal", err)
	}
	if after := j.effects(t); after != before {
		t.Fatalf("refused late Review acceptance changed durable state:\nbefore=%s\nafter=%s", before, after)
	}
	j.requireAcceptedReview(t, corrective, a2)
	j.requireRecordsUnchanged(t, j.snapshotA)
}

// TestManagedCandidateSubjectMissingPinNeverAccepts proves the final-proof
// routes refuse when the active worktree, and with it the pin, disappears.
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

// reclaimWorktree models the loss of the active worktree, not a new workflow
// route, and requires the pin to disappear with it.
func (j *candidateAtVerify) reclaimWorktree(t *testing.T) {
	t.Helper()
	if _, err := j.s.db.Exec(`UPDATE worktree_entries SET state='reclaimed' WHERE set_id=?`, WorktreeSetID(j.workID)); err != nil {
		t.Fatal(err)
	}
	if got := j.pinned(t); got != "" {
		t.Fatalf("pin after reclaim = %q, want empty", got)
	}
}

// requireAcceptedReview requires Main's accepted Review to be the named
// attempt and the integrated candidate to be the named commit.
func (j *candidateJourney) requireAcceptedReview(t *testing.T, attemptID, candidate string) {
	t.Helper()
	state, err := loadWorkflowReviewState(t.Context(), j.s.db, j.workID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Accepted == nil || state.Accepted.AttemptID != attemptID || state.Candidate == nil || state.Candidate.Commit != candidate {
		t.Fatalf("accepted Review = %+v candidate = %+v, want %s integrating %s", state.Accepted, state.Candidate, attemptID, candidate)
	}
}

// journeyWithIntegratedB has Main accept a corrective Review's output B.
// Merely observing or pinning B does not integrate it (CD-0242 D5).
func journeyWithIntegratedB(t *testing.T, workID string) *candidateAtVerify {
	t.Helper()
	j := journeyThroughA(t, workID, true)
	job := reviewSubjectReadyRevision(t, j.fixture)
	const attempt = "attempt-integrate-b"
	if err := j.dispatchReviewJob(t, attempt, job); err != nil {
		t.Fatalf("dispatch corrective Review of A: %v", err)
	}
	j.b, _ = j.completeCorrectiveReview(t, attempt, job, filepath.Join("internal", "store", job.JobID, "review.txt"), "corrected candidate\n", "integrated-b")
	if j.b == j.a {
		t.Fatal("the corrective Review did not change A")
	}
	if err := j.acceptEvidence(t, attempt); err != nil {
		t.Fatalf("Main integrates B: %v", err)
	}
	j.requireAcceptedReview(t, attempt, j.b)
	j.requireRecordsUnchanged(t, j.snapshotA)
	return j
}

// TestManagedCandidateSubjectFreshIndependentEvidenceAcceptsB executes the
// pinned oracle on Main's integrated B in a fresh Review of B. Only that
// producer receipt proves B's criterion; A's retained records stay unchanged.
func TestManagedCandidateSubjectFreshIndependentEvidenceAcceptsB(t *testing.T) {
	j := journeyWithIntegratedB(t, "candidate-fresh-b")
	preparationB := j.prepare(t, "b")
	if preparationB.Oracle.SubjectCommit != j.b {
		t.Fatalf("B preparation subject = %q, want B %q", preparationB.Oracle.SubjectCommit, j.b)
	}
	jobB := j.recordReadyJob(t, "job:candidate", preparationB)
	if jobB.Revision <= j.jobA.Revision {
		t.Fatalf("B job revision %d does not follow A revision %d", jobB.Revision, j.jobA.Revision)
	}
	const attempt = "attempt-review-b"
	if err := j.dispatchReviewJob(t, attempt, jobB); err != nil {
		t.Fatalf("dispatch Review of integrated B: %v", err)
	}
	if got := j.dispatchedSubject(t, attempt); got != j.b {
		t.Fatalf("B Review dispatch subject = %q, want B %q", got, j.b)
	}
	launches := j.launches
	oracle := j.executeRequest(t, attempt, jobB, preparationB)
	executed, err := j.s.VerifyWorktree(t.Context(), j.request("execute-b", &oracle))
	if err != nil || executed.Oracle == nil || executed.Oracle.Qualification != "pass" || executed.Oracle.SubjectCommit != j.b || j.launches != launches+1 {
		t.Fatalf("execute native oracle on B: %+v err=%v launches=%d", executed.Oracle, err, j.launches-launches)
	}
	control := j.bundle.Control
	exit := 0
	j.reviewReceipts = []string{executed.OperationRef}
	j.completeLane(t, attempt, "review", &jobB, &WorkerOracleReceipt{
		ControlIDs: []string{control.ControlID}, CaseIDs: control.CaseIDs, SubjectCommit: j.b,
		RecipeSource: WorkerOracleRecipeSource{Kind: WorkContextSourceRepositoryFile, ProjectID: control.RecipeSource.ProjectID, Path: control.RecipeSource.Path, CommitOID: control.RecipeSource.CommitOID},
		Result:       OracleReceiptResultPass, ExitCode: &exit, RunRef: executed.OperationRef, EvidenceRefs: []string{executed.OperationRef},
	})
	if err := j.acceptEvidence(t, attempt); err != nil {
		t.Fatalf("Main accepts B's receipt-bound Review: %v", err)
	}
	j.requireAcceptedReview(t, attempt, j.b)
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

// TestManagedCandidateSubjectCorrectiveDispatchOnB preserves correction
// context, the integrated subject, the exact job binding, and epoch ordering.
// Correction-count authority is not part of the dispatch (CD-0242 D6).
func TestManagedCandidateSubjectCorrectiveDispatchOnB(t *testing.T) {
	j := journeyWithIntegratedB(t, "candidate-corrective-b")
	acceptCurrentReviewForTest(t, j.fixture, j.repo, "correction_required")
	review, err := loadWorkflowReviewState(t.Context(), j.s.db, j.workID)
	if err != nil || review.Accepted == nil || review.Candidate == nil || review.Candidate.Commit != j.b || review.Accepted.Report.InputCandidate.Commit != j.b {
		t.Fatalf("correction Review over B: %+v err=%v", review, err)
	}
	run := j.observeRun(t, "correction-b")
	j.bindVerification(t, run)
	const diagnosis = "B needs a bounded corrective pass"
	const strategy = "correct B under its pinned oracle"
	correction := map[string]any{
		"diagnosis": diagnosis, "strategy": strategy,
		"predicate_ids": []string{"predicate:work-context"}, "evidence_refs": []string{run.OperationRef},
	}
	if err := j.action(t, "request_correction", correction, j.fixture.owner); err != nil {
		t.Fatalf("request correction of integrated B: %v", err)
	}
	if step := currentStep(t, j.s, j.workID); step != "repair" {
		t.Fatalf("step after correction = %q, want repair", step)
	}
	preparationB := j.prepare(t, "corrective-b")
	jobB := j.recordReadyJob(t, "job:candidate", preparationB)
	const attempt = "attempt-corrective-b"
	before, err := workflowCorrectionContextForDispatch(t.Context(), j.s.db, j.workID, "repair", attempt)
	if err != nil || before == nil {
		t.Fatalf("open correction before dispatch = %+v err=%v", before, err)
	}
	if before.Disposition != "verification" || before.Diagnosis != diagnosis || before.Strategy != strategy || len(before.PredicateIDs) != 1 || before.PredicateIDs[0] != "predicate:work-context" || len(before.EvidenceRefs) != 1 || before.EvidenceRefs[0] != run.OperationRef {
		t.Fatalf("correction context = %+v, want this request's diagnosis, strategy, predicate and B evidence", before)
	}
	j.dispatchJob(t, attempt, "implement", "implementation", jobB)
	if got := j.dispatchedSubject(t, attempt); got != j.b {
		t.Errorf("corrective dispatch subject = %q, want B %q", got, j.b)
	}
	var job WorkerJobBinding
	var raw string
	if err := j.s.db.QueryRow(`SELECT json_extract(payload,'$.worker_job') FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.action_id')='dispatch_worker' AND json_extract(payload,'$.worker_attempt_id')=?`, j.workID, WorkflowActionCompleted, attempt).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(raw), &job); err != nil || job != jobB {
		t.Errorf("corrective dispatch job = %+v err=%v, want %+v", job, err, jobB)
	}
	if j.dispatchEpoch(t, attempt) <= j.dispatchEpoch(t, "attempt-candidate-a") {
		t.Error("corrective attempt epoch does not follow the A attempt")
	}
	var state string
	if err := j.s.db.QueryRow(`SELECT lifecycle_state FROM worker_attempts WHERE work_id=? AND attempt_id=?`, j.workID, attempt).Scan(&state); err != nil || state != "dispatched" {
		t.Errorf("corrective attempt state = %q err=%v, want dispatched", state, err)
	}
	if got := j.pinned(t); got != j.b {
		t.Errorf("corrective dispatch pin = %q, want B %q", got, j.b)
	}
	j.requireRecordsUnchanged(t, j.snapshotA)
}

// dispatchJob dispatches one job-bound attempt on the named lane at the
// current step, carrying the open correction context when one exists.
func (j *candidateJourney) dispatchJob(t *testing.T, attemptID, laneID, capability string, job WorkerJobBinding) {
	t.Helper()
	laneDigest := registeredLaneIdentity(t, laneID)
	packet := joinPacketFor(t, j.s, j.workID, currentStep(t, j.s, j.workID), attemptID, laneID, laneDigest)
	packet["inputs"].(map[string]any)["worker_job"] = recordedPacketJobForTest(t, j.s, j.workID, job, "implement")
	correction, err := workflowCorrectionContextForDispatch(t.Context(), j.s.db, j.workID, currentStep(t, j.s, j.workID), attemptID)
	if err != nil {
		t.Fatal(err)
	}
	if correction != nil {
		packet["inputs"].(map[string]any)["correction"] = correction
	}
	if err := j.dispatchPacket(t, packet); err != nil {
		t.Fatalf("dispatch %s on %s: %v", attemptID, laneID, err)
	}
	bound := job
	j.laneDispatch(t, attemptID, capability, &bound)
}

// dispatchPacket authorizes one prepared packet through the dispatch_worker
// action from the claimed worktree.
func (j *candidateJourney) dispatchPacket(t *testing.T, packet map[string]any) error {
	t.Helper()
	prepared, err := PrepareWorkerDispatchFreshness(context.Background(), j.s, PrepareWorkerDispatchRequest{WorkID: j.workID, SessionWorktree: j.repo})
	if err != nil {
		return err
	}
	ctx := WithWorkerDispatchPreparation(context.Background(), prepared)
	_, err = dispatchJoinAttempt(ctx, t, j.s, j.workID, readWorkVersion(t, j.s, j.workID), j.fixture.owner, packet)
	return err
}

// dispatchedSubject is the raw subject the dispatch authorization recorded.
func (j *candidateJourney) dispatchedSubject(t *testing.T, attemptID string) string {
	t.Helper()
	var subject string
	if err := j.s.db.QueryRow(`SELECT coalesce(json_extract(payload,'$.worker_subject_commit'),'') FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.action_id')='dispatch_worker' AND json_extract(payload,'$.worker_attempt_id')=?`, j.workID, WorkflowActionCompleted, attemptID).Scan(&subject); err != nil {
		t.Fatal(err)
	}
	return subject
}

// observeRun is observe that returns the qualified verification run.
func (j *candidateJourney) observeRun(t *testing.T, id string) WorktreeVerifyResult {
	t.Helper()
	result, err := j.s.VerifyWorktree(context.Background(), j.request("observe-"+id, nil))
	if err != nil || result.SubjectRef == "" {
		t.Fatalf("observe %s: %+v err=%v", id, result, err)
	}
	return result
}

// bindVerification binds a native verification run as verification evidence
// through the bind_evidence action.
func (j *candidateJourney) bindVerification(t *testing.T, run WorktreeVerifyResult) {
	t.Helper()
	if err := j.action(t, "bind_evidence", map[string]any{
		"evidence_kind": "verification", "immutable_subject_ref": run.OperationRef, "producer_id": "principal/operator",
		"producer_run_ref": run.OperationRef, "producer_watermark": "request-" + strings.TrimPrefix(run.LeaseID, "candidate-"),
	}, j.acceptor); err != nil {
		t.Fatalf("bind verification %s: %v", run.OperationRef, err)
	}
}

// dispatchReview dispatches one review-lane attempt on a fresh ready job.
func (j *candidateJourney) dispatchReview(t *testing.T, attemptID string) {
	t.Helper()
	if err := j.dispatchReviewJob(t, attemptID, reviewSubjectReadyRevision(t, j.fixture)); err != nil {
		t.Fatalf("dispatch review %s: %v", attemptID, err)
	}
}

// dispatchReviewJob dispatches one review-lane attempt bound to the job at
// the current step through the dispatch_worker action and the lane-actor
// dispatch route.
func (j *candidateJourney) dispatchReviewJob(t *testing.T, attemptID string, job WorkerJobBinding) error {
	t.Helper()
	laneDigest := registeredLaneIdentity(t, "review")
	packet := joinPacketFor(t, j.s, j.workID, currentStep(t, j.s, j.workID), attemptID, "review", laneDigest)
	packet["inputs"].(map[string]any)["worker_job"] = recordedPacketJobForTest(t, j.s, j.workID, job, "review")
	if err := j.dispatchPacket(t, packet); err != nil {
		return err
	}
	j.laneDispatch(t, attemptID, "review", &job)
	return nil
}

// laneDispatch records the worker.dispatched event through the lane-actor
// route with the packet digest the dispatch_worker authorization recorded.
func (j *candidateJourney) laneDispatch(t *testing.T, attemptID, capability string, job *WorkerJobBinding) {
	t.Helper()
	lane := reviewGateLane(t, capability)
	var packetDigest string
	if err := j.s.db.QueryRow(`SELECT json_extract(payload,'$.worker_packet_digest') FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.action_id')='dispatch_worker' AND json_extract(payload,'$.worker_attempt_id')=?`, j.workID, WorkflowActionCompleted, attemptID).Scan(&packetDigest); err != nil {
		t.Fatalf("read dispatch digest %s: %v", attemptID, err)
	}
	dispatch := Event{EventID: "candidate-dispatched-" + attemptID, Kind: WorkerDispatched, SubjectType: SubjectWorkItem, SubjectID: j.workID, Actor: "worker:test", OccurredAt: time.Now().UTC(), PayloadVersion: CurrentEventPayloadVersion(WorkerDispatched), Payload: mustJSONValue(WorkerDispatchedPayload{
		AttemptID: attemptID, LaneID: lane.ID, LaneDigest: lane.Digest, CapabilityClass: lane.CapabilityClass, PacketDigest: packetDigest,
		ReadbackModel: preferredModelForLane(lane), PacketSchemaVersion: WorkerPacketSchemaVersion, ReportSchemaVersion: WorkerReportSchemaVersion, WorkerJob: job,
	})}
	if err := j.s.Transact(context.Background(), func(transaction *Transaction) error {
		prepared, err := PrepareLaneActorDispatch(context.Background(), transaction, dispatch, j.fixture.owner.PrincipalRef, j.fixture.owner.ClientRef)
		if err != nil {
			return err
		}
		_, err = AppendLaneActorDispatchTx(context.Background(), transaction, prepared)
		return err
	}); err != nil {
		t.Fatalf("lane dispatch %s: %v", attemptID, err)
	}
}

// completeLane appends the attempt's worker.completed report. A review lane
// reports a ready Review of its unchanged input, and a non-nil receipt rides
// the first evidence entry.
func (j *candidateJourney) completeLane(t *testing.T, attemptID, capability string, job *WorkerJobBinding, receipt *WorkerOracleReceipt) {
	t.Helper()
	if job == nil {
		var err error
		job, err = workflowDispatchedJobForAttempt(t.Context(), j.s.db, j.workID, attemptID)
		if err != nil {
			t.Fatal(err)
		}
	}
	var review *WorkerReviewBlock
	if capability == "review" {
		review = currentReviewBlockForTest(t, j.s, j.workID, attemptID, "ready_for_final_proof")
	}
	j.completeAttempt(t, attemptID, capability, *job, review, receipt)
}

// completeCorrectiveReview commits the dispatched Review's corrective change
// inside its job's path scope, verifies the corrected candidate, and returns
// the Review's terminal report naming that output with the run's receipt.
func (j *candidateJourney) completeCorrectiveReview(t *testing.T, attemptID string, job WorkerJobBinding, path, content, observeID string) (string, WorktreeVerifyResult) {
	t.Helper()
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
	report := currentReviewBlockForTest(t, j.s, j.workID, attemptID, "ready_for_final_proof")
	report.OutputCandidate = WorkerReviewCandidate{ProjectID: report.InputCandidate.ProjectID, Commit: output, Tree: reviewObservedTree(t, j.repo, output)}
	j.reviewReceipts = []string{run.OperationRef}
	j.completeReview(t, attemptID, job, report)
	return output, run
}

// completeReview appends the Review attempt's worker.completed report with
// the named block; the terminal producer observes its output candidate.
func (j *candidateJourney) completeReview(t *testing.T, attemptID string, job WorkerJobBinding, review *WorkerReviewBlock) {
	t.Helper()
	j.completeAttempt(t, attemptID, "review", job, review, nil)
}

func (j *candidateJourney) completeAttempt(t *testing.T, attemptID, capability string, job WorkerJobBinding, review *WorkerReviewBlock, receipt *WorkerOracleReceipt) {
	t.Helper()
	lane := reviewGateLane(t, capability)
	event := jobBoundCompletionEvent(j.workID, "candidate-completed-"+attemptID, attemptID, lane, time.Now().UTC(), &job, workerFixtureDirectory(t, j.s, j.workID))
	if review != nil {
		var payload WorkerCompletedPayload
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		if len(j.reviewReceipts) != 0 {
			review.Assessments = []WorkerReviewAssessment{{PredicateID: "predicate:work-context", Assessment: "supported", ReceiptRefs: j.reviewReceipts}}
		}
		payload.Review = review
		event.Payload = mustJSONValue(payload)
	}
	if receipt != nil {
		var payload WorkerCompletedPayload
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		payload.Evidence[0].OracleReceipt = receipt
		event.Payload = mustJSONValue(payload)
	}
	ctx := context.Background()
	prepared, err := PrepareWorkerTerminalDelivery(ctx, j.s, PrepareWorkerTerminalDeliveryRequest{
		WorkID: j.workID, AttemptID: attemptID, EventID: event.EventID, Runner: ExecGitRunner{},
	})
	if err != nil {
		t.Fatalf("prepare completion %s: %v", attemptID, err)
	}
	workerTerminalEventThroughSeamCtx(t, WithWorkerTerminalPreparation(ctx, prepared), j.s, event)
}

func (j *candidateJourney) dispatchEpoch(t *testing.T, attemptID string) int64 {
	t.Helper()
	var epoch int64
	if err := j.s.db.QueryRow(`SELECT json_extract(payload,'$.attempt_epoch') FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.action_id')='dispatch_worker' AND json_extract(payload,'$.worker_attempt_id')=?`, j.workID, WorkflowActionCompleted, attemptID).Scan(&epoch); err != nil {
		t.Fatalf("read dispatch epoch %s: %v", attemptID, err)
	}
	return epoch
}

func (j *candidateJourney) acceptEvidence(t *testing.T, attemptID string) error {
	t.Helper()
	return j.action(t, "accept_worker_result", map[string]any{"attempt_id": attemptID, "attempt_epoch": j.dispatchEpoch(t, attemptID)}, j.fixture.owner)
}

func (j *candidateJourney) pinned(t *testing.T) string {
	t.Helper()
	subject, err := readCurrentOracleSubject(context.Background(), j.s.db, j.workID)
	if err != nil {
		t.Fatal(err)
	}
	return subject
}

// effects is the durable state a refused route must leave unchanged.
func (j *candidateJourney) effects(t *testing.T) string {
	t.Helper()
	var jobs, subject string
	if err := j.s.db.QueryRow(`SELECT coalesce(group_concat(job_id||':'||revision||':'||coalesce(satisfied_result_ref,''),','),'') FROM worker_job_revisions WHERE work_id=?`, j.workID).Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	subject = j.pinned(t)
	return contractCorrectionEffectSnapshot(t, j.s, j.workID) + " jobs=" + jobs + " pinned=" + subject
}

// subjectRecord is one retained record whose bytes must never change.
type subjectRecord struct{ id, kind, payload string }

// snapshotSubjectRecords captures every worker report, dispatch binding, and
// native producer result the work holds now.
func (j *candidateJourney) snapshotSubjectRecords(t *testing.T, subject string) map[string]subjectRecord {
	t.Helper()
	records := map[string]subjectRecord{}
	rows, err := j.s.db.Query(`SELECT event_id,kind||'@'||payload_version,payload FROM domain_events WHERE subject_id=? AND (kind IN (?,?) OR (kind=? AND json_extract(payload,'$.action_id') IN ('dispatch_worker','accept_worker_result','accept_worker_evidence')))`, j.workID, WorkerCompleted, WorkerDispatched, WorkflowActionCompleted)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
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

// requireRecordsUnchanged compares a snapshot with the current records byte
// for byte.
func (j *candidateJourney) requireRecordsUnchanged(t *testing.T, snapshot map[string]subjectRecord) {
	t.Helper()
	current := j.snapshotSubjectRecords(t, strings.TrimPrefix(subjectOfLease(t, snapshot["lease:candidate-observe-refine-a"]), "commit:"))
	for key, want := range snapshot {
		got, ok := current[key]
		if !ok {
			t.Errorf("retained record %s disappeared", key)
			continue
		}
		if got != want {
			t.Errorf("retained record %s changed:\nbefore=%s %s\nafter=%s %s", key, want.kind, want.payload, got.kind, got.payload)
		}
	}
}

func subjectOfLease(t *testing.T, r subjectRecord) string {
	t.Helper()
	var result WorktreeVerifyResult
	if err := json.Unmarshal([]byte(r.payload), &result); err != nil {
		t.Fatal(err)
	}
	return result.SubjectRef
}

// executeRequest names the recorded dispatch authority of one job-bound
// attempt.
func (j *candidateJourney) executeRequest(t *testing.T, attemptID string, job WorkerJobBinding, preparation WorktreeVerifyResult) NativeOracleRequest {
	t.Helper()
	var digest string
	if err := j.s.db.QueryRow(`SELECT json_extract(payload,'$.worker_packet_digest') FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.action_id')='dispatch_worker' AND json_extract(payload,'$.worker_attempt_id')=?`, j.workID, WorkflowActionCompleted, attemptID).Scan(&digest); err != nil {
		t.Fatal(err)
	}
	bound := job
	return NativeOracleRequest{Phase: "execute", AttemptID: attemptID, AttemptEpoch: j.dispatchEpoch(t, attemptID), WorkerPacketDigest: digest, WorkerJobBinding: &bound, ControlID: j.bundle.Control.ControlID, PreparationRunRef: preparation.OperationRef}
}

// refuseStaleExecution proves an earlier subject's execution authority
// refuses before any test program launches.
func (j *candidateJourney) refuseStaleExecution(t *testing.T, attemptID string, job WorkerJobBinding, preparation WorktreeVerifyResult) {
	t.Helper()
	launches := j.launches
	o := j.executeRequest(t, attemptID, job, preparation)
	_, err := j.s.VerifyWorktree(context.Background(), j.request("stale-execute-"+attemptID, &o))
	if err == nil {
		t.Errorf("stale execution authority of %s launched on the current HEAD", attemptID)
	}
	if j.launches != launches {
		t.Errorf("stale execution of %s launched %d test programs", attemptID, j.launches-launches)
	}
	t.Logf("stale execution of %s refused: %s launches=%d", attemptID, failureKind(err), j.launches-launches)
}

// replayProducer replays one A producer: the recorded A result returns
// unchanged, no program launches, and the pin is not requalified.
func (j *candidateJourney) replayProducer(t *testing.T, id, a, pinned string) {
	t.Helper()
	launches := j.launches
	var before string
	if err := j.s.db.QueryRow(`SELECT result_json FROM worktree_verify_leases WHERE lease_id=?`, "candidate-"+id).Scan(&before); err != nil {
		t.Fatal(err)
	}
	replay, err := j.s.VerifyWorktree(context.Background(), j.request(id, nil))
	if err != nil || replay.SubjectRef != "commit:"+a {
		t.Errorf("replay of %s = %q err=%v, want the recorded A subject", id, replay.SubjectRef, err)
	}
	var after string
	if err := j.s.db.QueryRow(`SELECT result_json FROM worktree_verify_leases WHERE lease_id=?`, "candidate-"+id).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before || j.launches != launches || j.pinned(t) != pinned {
		t.Errorf("replay of %s changed its producer, launched %d programs, or moved the pin to %q", id, j.launches-launches, j.pinned(t))
	}
}

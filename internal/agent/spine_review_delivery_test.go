package agent

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

func (f *spineFixture) executeControl(t *testing.T, attempt string, epoch int64, job *store.WorkerJobBinding, preparation, key string) store.WorktreeVerifyResult {
	t.Helper()
	response := spineOK(t, f.call(t, "concord_work_transition", "worktree_verify", map[string]any{
		"work_id": f.work, "idempotency_key": key, "oracle": map[string]any{
			"phase": "execute", "attempt_id": attempt, "attempt_epoch": epoch, "worker_job_binding": job,
			"worker_packet_digest": f.packetDigest, "control_id": "control:spine-value", "preparation_run_ref": preparation,
		},
	}))
	var result store.WorktreeVerifyResult
	if err := json.Unmarshal(response.Result, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

// Git calls drive the host fixture's checkout, not a simulated permission grant.
// The paired real-Git capability-guard test in worker_subject.test.ts owns the
// two-edit/commit grant and terminal-boundary proof.
func TestSpineReviewRepairsCandidateThroughMainDelivery(t *testing.T) {
	f := newSpineExecutionFixture(t)
	f.verify(t)
	f.recordJob(t)
	lane, job, epoch := f.startWorker(t, "implement", "attempt:spine-broken")
	if err := os.WriteFile(filepath.Join(f.path, "pkg/value.go"), []byte("package spine\nfunc Value() int { return 8 }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitRun(t, f.path, "add", "pkg/value.go")
	gitRun(t, f.path, "commit", "-qm", "candidate needing repair")
	f.finishWorker(t, lane, "attempt:spine-broken", job, nil)
	spineOK(t, f.action(t, "accept_worker_result", map[string]any{"attempt_id": "attempt:spine-broken", "attempt_epoch": epoch}))
	b := store.WorkerReviewCandidate{ProjectID: "project-1", Commit: gitRun(t, f.path, "rev-parse", "HEAD"), Tree: gitRun(t, f.path, "rev-parse", "HEAD^{tree}")}
	spineOK(t, f.call(t, "concord_work_transition", "worktree_verify", map[string]any{"work_id": f.work, "command": []string{"git", "rev-parse", "--verify", "HEAD^{commit}"}, "idempotency_key": "spine-pin-b"}))
	f.recordJob(t)
	current := f.read(t)
	preparation := current.Pinned.Jobs[0].AcceptanceOracle.Controls[0].ReadinessEvidenceRefs[0]
	dependencies := *current.Pinned.Pin.Review.Dependencies
	const attempt = "attempt:spine-repair"
	acceptedBeforeRepair := current.Pinned.Pin.Review.Accepted
	lane, job, epoch = f.startWorker(t, "review", attempt)
	broken := f.executeControl(t, attempt, epoch, job, preparation, "spine-control-b")
	if broken.Oracle == nil || broken.Oracle.Qualification != "fail" || broken.Oracle.SubjectCommit != b.Commit || broken.ExitCode == 0 {
		t.Fatalf("B did not retain its real failure: %+v", broken)
	}
	for _, expression := range []string{"3 + 3", "3 + 4"} {
		if err := os.WriteFile(filepath.Join(f.path, "pkg/value.go"), []byte("package spine\nfunc Value() int { return "+expression+" }\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	gitRun(t, f.path, "add", "pkg/value.go")
	gitRun(t, f.path, "commit", "-qm", "repair checked value")
	c := store.WorkerReviewCandidate{ProjectID: "project-1", Commit: gitRun(t, f.path, "rev-parse", "HEAD"), Tree: gitRun(t, f.path, "rev-parse", "HEAD^{tree}")}
	checked := f.executeControl(t, attempt, epoch, job, preparation, "spine-control-c")
	if checked.Oracle == nil || checked.Oracle.Qualification != "pass" || checked.Oracle.SubjectCommit != c.Commit || checked.Oracle.InputManifestDigest == "" || checked.Oracle.BinaryDigest == "" || checked.Oracle.BuildEnvironmentDigest == "" || checked.Oracle.InputManifestDigest == broken.Oracle.InputManifestDigest {
		t.Fatalf("C lacks captured execution proof: subject=%q oracle=%+v", checked.SubjectRef, checked.Oracle)
	}
	if !reflect.DeepEqual(checked.Command, []string{"go", "test", "-count=1", "."}) {
		t.Fatalf("producer executed another command: %v", checked.Command)
	}
	f.proof = f.verify(t)
	report := &store.WorkerReviewBlock{Disposition: "ready_for_final_proof", InputCandidate: b, OutputCandidate: c, Dependencies: dependencies,
		Assessments: []store.WorkerReviewAssessment{{PredicateID: "predicate:primary", Assessment: "supported", ReceiptRefs: []string{checked.OperationRef, f.proof}}},
		Findings: []store.WorkerReviewFinding{{FindingID: "finding:value", Severity: "P1", Confidence: "high", Detail: "Value returned eight; the declared check now passes after repair.", InScope: true, Blocking: true, Status: "closed", EvidenceRefs: []string{broken.OperationRef}, ClosureRefs: []string{checked.OperationRef},
			Oracle: &store.WorkerOracleFinding{Classification: "delivery_blocker", OwnerID: "owner:spine-value", FailureFamily: "incorrect-value", PredicateIDs: []string{"predicate:primary"}, CaseIDs: []string{"case:spine-value"}, ControlIDs: []string{"control:spine-value"}, EvidenceRefs: []string{broken.OperationRef}}}},
		RemainingObligations: []store.WorkerReviewObligation{{ObligationID: "obligation:publication", OwnerRef: "owner:main", SubjectRef: "commit:" + c.Commit, InputDigest: dependencies.Criteria[0].ContentDigest, NeededProof: "Publish the canonical closure receipt after delivery."}}}
	f.finishWorker(t, lane, attempt, job, report)
	unaccepted := f.read(t).Pinned.Pin
	if unaccepted.Review.Candidate == nil || *unaccepted.Review.Candidate != b || !reflect.DeepEqual(unaccepted.Review.Accepted, acceptedBeforeRepair) || unaccepted.Lifecycle != "in_progress" {
		t.Fatalf("terminal arrival granted readiness: %+v", unaccepted.Review)
	}
	for _, intent := range unaccepted.NextValidIntents {
		if intent.ActionID == "record_delivery" {
			t.Fatal("the current projection offers delivery before Main accepts readiness")
		}
	}
	delivery := map[string]any{"delivery_artifact": "artifact:spine-value", "delivery_state": "asserted"}
	preflightErr := store.WorkflowActionPreflightWithRegistry(t.Context(), f.s, store.BuiltinWorkflowRegistry(), store.WorkflowActionPreflightRequest{
		WorkID: f.work, ExpectedVersion: unaccepted.Version, ActionID: "record_delivery", Payload: retryJSON(delivery), SessionWorktree: f.path,
		Actor: store.WorkflowActor{PrincipalRef: f.env.PrincipalRef, ClientRef: f.env.ClientRef, AgentRef: f.env.AgentRef, SessionRef: f.env.SessionRef, ActorClass: store.ActorAgent},
	})
	var preflightFailure *store.Failure
	if !errors.As(preflightErr, &preflightFailure) || preflightFailure.Kind != store.KindMissingEvidence || preflightFailure.RecoveryAction == "" {
		t.Fatalf("preflight lost the readiness requirement or its recovery: %v", preflightErr)
	}
	held := f.action(t, "record_delivery", delivery)
	afterHeld := f.read(t).Pinned.Pin
	if held.Outcome != OutcomeError || held.Error == nil || held.Error.Kind != "missing_evidence" || held.Error.Message != preflightFailure.Detail || held.Error.EffectState != EffectNone || afterHeld.Version != unaccepted.Version || afterHeld.Lifecycle != unaccepted.Lifecycle || !reflect.DeepEqual(afterHeld.Review, unaccepted.Review) {
		t.Fatalf("an unaccepted ready report advanced execution: %+v", held)
	}
	acceptInput := map[string]any{"work_id": f.work, "expected_version": unaccepted.Version, "action_id": "accept_worker_result", "idempotency_key": "spine-main-accept-repair", "fields": map[string]any{"attempt_id": attempt, "attempt_epoch": epoch}}
	spineOK(t, f.call(t, "concord_work_transition", "workflow_action", acceptInput))
	accepted := f.read(t).Pinned.Pin
	if accepted.Review.Candidate == nil || *accepted.Review.Candidate != c || accepted.Review.Accepted.Report.Disposition != "ready_for_final_proof" || len(accepted.Review.Findings) != 1 || accepted.Review.Findings[0].Status != "closed" {
		t.Fatalf("Main did not atomically accept C and its evidenced closure: %+v", accepted.Review)
	}
	replayed := spineOK(t, f.call(t, "concord_work_transition", "workflow_action", acceptInput))
	afterReplay := f.read(t).Pinned.Pin
	if !replayed.Replayed || afterReplay.Version != accepted.Version || afterReplay.Lifecycle != accepted.Lifecycle || !reflect.DeepEqual(afterReplay.Review, accepted.Review) {
		t.Fatal("replaying Main's exact acceptance had another effect")
	}
	conflict := f.action(t, "reject_worker_result", map[string]any{"attempt_id": attempt, "attempt_epoch": epoch, "diagnosis": "Conflicting disposition of the accepted repair.", "strategy": "Retain the accepted candidate.", "predicate_ids": []string{"predicate:primary"}, "evidence_refs": []string{checked.OperationRef}})
	afterConflict := f.read(t).Pinned.Pin
	if conflict.Outcome != OutcomeError || conflict.Error == nil || conflict.Error.EffectState != EffectNone || afterConflict.Version != accepted.Version || afterConflict.Lifecycle != accepted.Lifecycle || !reflect.DeepEqual(afterConflict.Review, accepted.Review) {
		t.Fatalf("a conflicting rejection changed the accepted repair: %+v", conflict)
	}
	spineOK(t, f.action(t, "bind_evidence", map[string]any{"evidence_kind": "verification", "evidence_ref": checked.OperationRef}))
	spineOK(t, f.action(t, "bind_evidence", map[string]any{"evidence_kind": "verification", "evidence_ref": f.proof}))
	f.advanceToAcceptance(t)
	spineOK(t, f.call(t, "concord_work_define", "observation_record", map[string]any{"work_id": f.work, "statement": "The declared value check passes on the repaired candidate; Main accepted its attributed Review and native producer proof.", "refs": []string{f.read(t).Pinned.Contract.ArchitectureBinding.HomeDomainID, checked.OperationRef}, "idempotency_key": "spine-premise-investigation"}))
	beforeMissingProof := f.read(t).Pinned.Pin
	missingProof := f.action(t, "complete", map[string]any{"impact_verdict": "non-breaking"})
	afterMissingProof := f.read(t).Pinned.Pin
	if missingProof.Outcome != OutcomeError || missingProof.Error == nil || missingProof.Error.Kind != "missing_evidence" || missingProof.Error.Message != "premise confirmation requires a recorded workflow verdict" || missingProof.Error.EffectState != EffectNone || afterMissingProof.Version != beforeMissingProof.Version || afterMissingProof.Lifecycle != beforeMissingProof.Lifecycle || !reflect.DeepEqual(afterMissingProof.Review, beforeMissingProof.Review) {
		t.Fatalf("completion before criterion proof: outcome=%s error=%+v", missingProof.Outcome, missingProof.Error)
	}
	unrelatedResponse := spineOK(t, f.call(t, "concord_work_transition", "worktree_verify", map[string]any{"work_id": f.work, "command": []string{"git", "status", "--porcelain"}, "idempotency_key": "spine-unrelated-green"}))
	var unrelated store.WorktreeVerifyResult
	if err := json.Unmarshal(unrelatedResponse.Result, &unrelated); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, predicate, ref, kind string }{
		{"failed B receipt", "predicate:primary", broken.OperationRef, "missing_evidence"},
		{"readiness is not execution", "predicate:primary", preparation, "missing_evidence"},
		{"forged receipt", "predicate:primary", "worktree_verify:forged", "missing_evidence"},
		{"unrelated green command", "predicate:primary", unrelated.OperationRef, "missing_evidence"},
		{"unapproved criterion", "predicate:unapproved", checked.OperationRef, "invalid_input"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := f.read(t).Pinned.Pin
			response := f.action(t, "record_verdict", map[string]any{"verdicts": []map[string]any{{"predicate_id": tc.predicate, "receipt_refs": []string{tc.ref}}}})
			after := f.read(t).Pinned.Pin
			if response.Outcome != OutcomeError || response.Error == nil || response.Error.Kind != tc.kind || response.Error.EffectState != EffectNone || before.Version != after.Version || before.Lifecycle != after.Lifecycle || !reflect.DeepEqual(before.Review, after.Review) {
				t.Fatalf("proof refusal changed current state or returned the wrong requirement: %+v", response)
			}
		})
	}
	// A command receipt has semantic coverage only because the exact accepted
	// assessment above cites it for this criterion on C. The unrelated passing
	// command in the negative table never receives that coverage.
	spineOK(t, f.action(t, "record_verdict", map[string]any{"verdicts": []map[string]any{{"predicate_id": "predicate:primary", "receipt_refs": []string{f.proof}}}}))
	spineOK(t, f.action(t, "record_verdict", map[string]any{"verdicts": []map[string]any{{"predicate_id": "predicate:primary", "receipt_refs": []string{checked.OperationRef}}}}))
	beforeConfirmation := f.read(t).Pinned.Pin
	for _, intent := range beforeConfirmation.NextValidIntents {
		if intent.ActionID == "complete" {
			t.Fatal("completion was offered before the premise-confirmation exit")
		}
	}
	missingConfirmation := f.action(t, "complete", map[string]any{"impact_verdict": "non-breaking"})
	afterConfirmationRefusal := f.read(t).Pinned.Pin
	if missingConfirmation.Outcome != OutcomeError || missingConfirmation.Error == nil || missingConfirmation.Error.Kind != "invalid_transition" || missingConfirmation.Error.Message != "workflow action is not declared on the current step" || missingConfirmation.Error.EffectState != EffectNone || afterConfirmationRefusal.Version != beforeConfirmation.Version || afterConfirmationRefusal.Lifecycle != beforeConfirmation.Lifecycle || !reflect.DeepEqual(afterConfirmationRefusal.Review, beforeConfirmation.Review) {
		t.Fatalf("completion before premise confirmation: outcome=%s error=%+v", missingConfirmation.Outcome, missingConfirmation.Error)
	}
	pin := f.read(t).Pinned.Pin
	spineOK(t, f.call(t, "concord_work_transition", "workflow_action", map[string]any{"work_id": f.work, "expected_version": pin.Version, "action_id": "confirm_premise", "selected_choice": "confirm", "idempotency_key": "spine-confirm"}))
	spineOK(t, f.action(t, "bind_evidence", map[string]any{"evidence_kind": "review", "evidence_ref": "sha256:" + strings.Repeat("a", 64)}))
	if err := os.WriteFile(filepath.Join(f.path, "pkg/value.go"), []byte("package spine\nfunc Value() int { return 8 }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitRun(t, f.path, "add", "pkg/value.go")
	gitRun(t, f.path, "commit", "-qm", "unreviewed candidate")
	if head := gitRun(t, f.path, "rev-parse", "HEAD"); head == c.Commit {
		t.Fatal("the unreviewed candidate did not change the subject")
	}
	beforeChangedSubject := f.read(t).Pinned.Pin
	changedSubject := f.action(t, "complete", map[string]any{"impact_verdict": "non-breaking"})
	afterChangedSubject := f.read(t).Pinned.Pin
	if changedSubject.Outcome != OutcomeError || changedSubject.Error == nil || changedSubject.Error.Kind != "stale_requires_review" || changedSubject.Error.EffectState != EffectNone || afterChangedSubject.Version != beforeChangedSubject.Version || afterChangedSubject.Lifecycle != beforeChangedSubject.Lifecycle || !reflect.DeepEqual(afterChangedSubject.Review, beforeChangedSubject.Review) {
		t.Fatalf("completion did not revalidate the final checkout against C: %+v", changedSubject.Error)
	}
	gitRun(t, f.path, "checkout", "--detach", c.Commit)
	spineOK(t, f.action(t, "complete", map[string]any{"impact_verdict": "non-breaking"}))
	done := f.read(t).Pinned.Pin
	if done.Lifecycle != "completed" || done.Review.Candidate == nil || *done.Review.Candidate != c || !done.Review.PublicationOutstanding || len(done.VerifiedCriteria) != 1 {
		t.Fatalf("terminal delivery lost its subject, proof or publication obligation: %+v", done)
	}
}

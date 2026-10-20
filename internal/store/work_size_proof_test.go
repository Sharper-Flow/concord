package store

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

// proofLog writes the recorded review history the proof reads: review
// attempts, their reports, acceptances, refine checkpoints, and contract
// changes. The proof derives from these rows alone.
type proofLog struct {
	t    *testing.T
	s    *Store
	work string
	n    int
}

func (l *proofLog) event(kind string, payload any) string {
	l.t.Helper()
	l.n++
	id := l.work + ":proof-" + jsonInt(int64(l.n))
	raw, err := json.Marshal(payload)
	if err != nil {
		l.t.Fatal(err)
	}
	if _, err := l.s.DatabaseForTesting().Exec(`INSERT INTO domain_events(event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload) VALUES(?,?,?,?,?,?,?,?)`,
		id, kind, string(SubjectWorkItem), l.work, "test", time.Unix(int64(l.n), 0).UTC().Format(time.RFC3339Nano), 1, string(raw)); err != nil {
		l.t.Fatal(err)
	}
	return id
}

// review records a completed attempt of the capability and its report.
func (l *proofLog) review(attemptID, capability string, review *WorkerReviewBlock) {
	l.t.Helper()
	if _, err := l.s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1); INSERT INTO worker_attempts(work_id,attempt_id,lane_id,lane_version,lane_digest,capability_class,readback_model,packet_schema_version,report_schema_version,lifecycle_state,dispatched_at,completed_at) VALUES(?,?,'review',1,?,?,'test-model','1.0','1.0','completed','2026-01-01T00:00:00Z','2026-01-01T00:01:00Z'); DELETE FROM fold_guard`,
		l.work, attemptID, "sha256:"+strings.Repeat("a", 64), capability); err != nil {
		l.t.Fatal(err)
	}
	l.event(WorkerCompleted, WorkerCompletedPayload{AttemptID: attemptID, Review: review})
}

func (l *proofLog) accept(action, attemptID string) string {
	return l.event(WorkflowActionCompleted, map[string]any{"action_id": action, "step_id": "refine", "worker_attempt_id": attemptID})
}

func (l *proofLog) refine() {
	l.event(WorkflowActionCompleted, map[string]any{"action_id": "checkpoint_refine", "step_id": "refine"})
}

// pass records one reviewed and accepted refine pass and returns the
// acceptance event ID.
func (l *proofLog) pass(attemptID, action string, review *WorkerReviewBlock) string {
	l.review(attemptID, "review", review)
	return l.accept(action, attemptID)
}

func oracleFinding(severity, classification string) WorkerReviewFinding {
	return WorkerReviewFinding{Severity: severity, Confidence: "high", Detail: "finding", Oracle: &WorkerOracleFinding{Classification: classification, OwnerID: "owner:store"}}
}

func shipReview(findings ...WorkerReviewFinding) *WorkerReviewBlock {
	return &WorkerReviewBlock{Verdict: "ship", Findings: findings}
}

func largerWorkSizeInputs() WorkSizeInputs {
	inputs := workSizeInputsFixture()
	inputs.Declared.UserVisible = true
	return inputs
}

// largerWork seeds a work item whose model record sizes it larger.
func largerWork(t *testing.T, s *Store, workID string) *proofLog {
	t.Helper()
	seedWork(t, s, workID)
	if err := applyWorkEvent(t, s, workSizedEvent(workID+"-sized-1", workID, WorkSizeModelVersion, WorkSizeLarger, largerWorkSizeInputs()), nil); err != nil {
		t.Fatal(err)
	}
	return &proofLog{t: t, s: s, work: workID, n: 0}
}

func workSizeProofsForTest(t *testing.T, s *Store, workID string) []string {
	t.Helper()
	proofs, err := deriveWorkSizeProofsTx(context.Background(), s.DatabaseForTesting(), workID, 0)
	if err != nil {
		t.Fatal(err)
	}
	return proofs
}

// A second post-refine review that still finds an in-scope P0-P2 finding,
// accepted through either route, proves a larger change bigger.
func TestWorkSizeProofFromASecondInScopeReview(t *testing.T) {
	for _, action := range []string{"accept_worker_evidence", "accept_worker_result"} {
		t.Run(action, func(t *testing.T) {
			s := openTemp(t)
			log := largerWork(t, s, "proved")
			log.pass("review-1", action, shipReview())
			log.refine()
			proof := log.pass("review-2", action, shipReview(oracleFinding("P2", OracleClassificationDeliveryBlocker)))
			if got := workSizeProofsForTest(t, s, "proved"); !reflect.DeepEqual(got, []string{proof}) {
				t.Fatalf("proofs = %v, want [%s]", got, proof)
			}
		})
	}
}

// None of these proves the change bigger.
func TestWorkSizeProofRefusesWhatDoesNotProve(t *testing.T) {
	inScope := shipReview(oracleFinding("P1", OracleClassificationDeliveryBlocker))
	cases := []struct {
		name  string
		build func(l *proofLog)
	}{
		{"first review", func(l *proofLog) { l.pass("review-1", "accept_worker_evidence", inScope) }},
		{"no refine between reviews", func(l *proofLog) {
			l.pass("review-1", "accept_worker_evidence", shipReview())
			l.pass("review-2", "accept_worker_evidence", inScope)
		}},
		{"P3 finding", func(l *proofLog) {
			l.pass("review-1", "accept_worker_evidence", shipReview())
			l.refine()
			l.pass("review-2", "accept_worker_evidence", shipReview(oracleFinding("P3", OracleClassificationDeliveryBlocker)))
		}},
		{"out-of-scope finding", func(l *proofLog) {
			l.pass("review-1", "accept_worker_evidence", shipReview())
			l.refine()
			l.pass("review-2", "accept_worker_evidence", shipReview(oracleFinding("P0", OracleClassificationFollowUp)))
		}},
		{"closed finding", func(l *proofLog) {
			l.pass("review-1", "accept_worker_evidence", shipReview())
			l.refine()
			finding := oracleFinding("P1", OracleClassificationDeliveryBlocker)
			finding.Oracle.ContinuesFindingID = "finding:1:0"
			review := shipReview(finding)
			review.ResolvedFindings = []WorkerOracleResolution{{FindingID: "finding:1:0", EvidenceRefs: []string{"run:1"}}}
			l.pass("review-2", "accept_worker_evidence", review)
		}},
		{"oracle-free finding in a ship report", func(l *proofLog) {
			l.pass("review-1", "accept_worker_evidence", shipReview())
			l.refine()
			l.pass("review-2", "accept_worker_evidence", shipReview(WorkerReviewFinding{Severity: "P1", Confidence: "high", Detail: "finding"}))
		}},
		{"implementation attempt", func(l *proofLog) {
			l.pass("review-1", "accept_worker_evidence", shipReview())
			l.refine()
			l.review("implement-2", "implementation", inScope)
			l.accept("accept_worker_result", "implement-2")
		}},
		{"contract changed between reviews", func(l *proofLog) {
			l.pass("review-1", "accept_worker_evidence", shipReview())
			l.event(WorkflowContractSuperseded, map[string]any{})
			l.refine()
			l.pass("review-2", "accept_worker_evidence", inScope)
		}},
		{"finding before the size decision", func(l *proofLog) {
			l.pass("review-1", "accept_worker_evidence", shipReview())
			l.refine()
			l.pass("review-2", "accept_worker_evidence", inScope)
			t := l.t
			proofs := workSizeProofsForTest(t, l.s, l.work)
			sized := operationEvent(l.work+"-sized-2", WorkSized, SubjectWorkItem, l.work, workSizedPayload{ModelVersion: WorkSizeModelVersion, Size: WorkSizeCrucial, Inputs: largerWorkSizeInputs(), Footprint: workSizeFootprintFixture(), ReviewBasis: proofs})
			if err := applyWorkEvent(t, l.s, sized, nil); err != nil {
				t.Fatal(err)
			}
			overrideWorkSizeLarger(t, l.s, l.work)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := openTemp(t)
			log := largerWork(t, s, "unproved")
			log.t = t
			tc.build(log)
			if got := workSizeProofsForTest(t, s, "unproved"); len(got) != 0 {
				t.Fatalf("proofs = %v, want none", got)
			}
		})
	}
}

// overrideWorkSizeLarger records the operator's larger size so the window
// after a crucial record reads a larger effective size again.
func overrideWorkSizeLarger(t *testing.T, s *Store, workID string) {
	t.Helper()
	insertWorkSizeOverrideApproval(t, s, workID, 1)
	if err := runWorkSizeOverride(t, s, workSizeOverrideRequest(t, s, workID, workID+"-override", WorkSizeLarger)); err != nil {
		t.Fatal(err)
	}
}

// An oracle-free P0-P2 finding in a no_ship report counts as in scope.
func TestWorkSizeProofCountsAnOracleFreeFindingInANoShipReport(t *testing.T) {
	s := openTemp(t)
	log := largerWork(t, s, "oracle-free")
	log.pass("review-1", "accept_worker_evidence", shipReview())
	log.refine()
	proof := log.pass("review-2", "accept_worker_evidence", &WorkerReviewBlock{Verdict: "no_ship", Findings: []WorkerReviewFinding{{Severity: "P2", Confidence: "low", Detail: "finding"}}})
	if got := workSizeProofsForTest(t, s, "oracle-free"); !reflect.DeepEqual(got, []string{proof}) {
		t.Fatalf("proofs = %v, want [%s]", got, proof)
	}
}

// Only a larger change can prove bigger.
func TestWorkSizeProofNeedsALargerEffectiveSize(t *testing.T) {
	s := openTemp(t)
	sizedWork(t, s, "small")
	log := &proofLog{t: t, s: s, work: "small"}
	log.pass("review-1", "accept_worker_evidence", shipReview())
	log.refine()
	log.pass("review-2", "accept_worker_evidence", shipReview(oracleFinding("P0", OracleClassificationDeliveryBlocker)))
	if got := workSizeProofsForTest(t, s, "small"); len(got) != 0 {
		t.Fatalf("proofs on small work = %v", got)
	}
}

// The next size record carries exactly the derived basis and decides
// crucial; it releases the proof window, and a later rerun after the fix
// still decides crucial from the recorded basis.
func TestWorkSizeRecordCarriesTheExactReviewBasis(t *testing.T) {
	s := openTemp(t)
	log := largerWork(t, s, "basis")
	log.pass("review-1", "accept_worker_evidence", shipReview())
	log.refine()
	proof := log.pass("review-2", "accept_worker_evidence", shipReview(oracleFinding("P1", OracleClassificationDeliveryBlocker)))
	record := func(id string, size WorkSize, basis []string) error {
		return applyWorkEvent(t, s, operationEvent(id, WorkSized, SubjectWorkItem, "basis", workSizedPayload{ModelVersion: WorkSizeModelVersion, Size: size, Inputs: largerWorkSizeInputs(), Footprint: workSizeFootprintFixture(), ReviewBasis: basis}), nil)
	}
	assertFailureKind(t, record("basis-omitted", WorkSizeLarger, nil), KindInvariantViolation)
	assertFailureKind(t, record("basis-invented", WorkSizeCrucial, []string{proof, "basis:proof-99"}), KindInvariantViolation)
	assertFailureKind(t, record("basis-wrong-size", WorkSizeLarger, []string{proof}), KindInvariantViolation)
	if err := record("basis-rerun", WorkSizeCrucial, []string{proof}); err != nil {
		t.Fatal(err)
	}
	if got := workSizeProofsForTest(t, s, "basis"); len(got) != 0 {
		t.Fatalf("a newer size record leaves proofs %v", got)
	}
	assertFailureKind(t, record("basis-forgotten", WorkSizeLarger, nil), KindInvariantViolation)
	if err := record("basis-after-fix", WorkSizeCrucial, []string{proof}); err != nil {
		t.Fatal(err)
	}
	if _, err := DecideWorkSize(1, largerWorkSizeInputs(), []string{proof}); err == nil {
		t.Fatal("model version 1 accepted a review basis")
	}
}

func workSizeStopDecisions(t *testing.T, s *Store, workID string) (WorkflowAdmissionState, map[string]WorkflowAdmissionDecision) {
	t.Helper()
	definition, err := BuiltinWorkflowDefinitionForRef("workflow.implementation")
	if err != nil {
		t.Fatal(err)
	}
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	state, _, _, err := loadWorkflowAdmissionStateTxForRegistry(context.Background(), tx, workID, definition.Definition, "proposal", "test", BuiltinWorkflowRegistry())
	_ = tx.Rollback()
	if err != nil {
		t.Fatal(err)
	}
	decisions := map[string]WorkflowAdmissionDecision{}
	for _, action := range definition.Definition.ActionDefinitions {
		decisions[action.ID] = workflowAdmit(definition.Definition, state, action.ID)
	}
	for _, action := range []string{"reject_worker_result", "record_worker_failure"} {
		decisions[action] = workflowAdmit(definition.Definition, state, action)
	}
	return state, decisions
}

func stoppedBySize(decision WorkflowAdmissionDecision) bool {
	return decision.Failure != nil && decision.Failure.Kind == KindStaleRequiresReview && decision.Failure.RecoveryAction == RecoveryWorkSizeRerun
}

// CD-0229 D6: a proof stops every workflow action but the pre-proof
// attempts' rejection and failure routes. An override that keeps the size
// keeps the stop; only a newer size record releases it.
func TestWorkSizeProofStopsTheWorkflowUntilARerun(t *testing.T) {
	s := openTemp(t)
	seedOutsideRepairTestWork(t, s, "stopped")
	if err := applyWorkEvent(t, s, workSizedEvent("stopped-sized-1", "stopped", WorkSizeModelVersion, WorkSizeLarger, largerWorkSizeInputs()), nil); err != nil {
		t.Fatal(err)
	}
	if state, decisions := workSizeStopDecisions(t, s, "stopped"); state.SizeRerunRequired || stoppedBySize(decisions["dispatch_worker"]) {
		t.Fatal("an unproved larger item is stopped")
	}
	log := &proofLog{t: t, s: s, work: "stopped"}
	log.pass("review-1", "accept_worker_evidence", shipReview())
	log.refine()
	proof := log.pass("review-2", "accept_worker_evidence", shipReview(oracleFinding("P0", OracleClassificationDeliveryBlocker)))

	assertStopped := func(when string) {
		t.Helper()
		state, decisions := workSizeStopDecisions(t, s, "stopped")
		if !state.SizeRerunRequired {
			t.Fatalf("%s: the stop is not set", when)
		}
		for action, decision := range decisions {
			exempt := action == "reject_worker_result" || action == "record_worker_failure"
			if stoppedBySize(decision) == exempt {
				t.Fatalf("%s: %s stopped=%v, want %v (%+v)", when, action, stoppedBySize(decision), !exempt, decision.Failure)
			}
		}
	}
	assertStopped("after the proof")
	overrideWorkSizeLarger(t, s, "stopped")
	assertStopped("after an override that keeps the size")

	rerun := operationEvent("stopped-sized-2", WorkSized, SubjectWorkItem, "stopped", workSizedPayload{ModelVersion: WorkSizeModelVersion, Size: WorkSizeCrucial, Inputs: largerWorkSizeInputs(), Footprint: workSizeFootprintFixture(), ReviewBasis: []string{proof}})
	if err := applyWorkEvent(t, s, rerun, nil); err != nil {
		t.Fatal(err)
	}
	state, decisions := workSizeStopDecisions(t, s, "stopped")
	if state.SizeRerunRequired {
		t.Fatal("a newer size record leaves the stop set")
	}
	for action, decision := range decisions {
		if stoppedBySize(decision) {
			t.Fatalf("after the rerun %s is still stopped", action)
		}
	}
}

// A replayed size record derives its basis from the log before it, as the
// live fold did; a review accepted after it cannot change that basis.
func TestWorkSizeReplayDerivesTheBasisFromTheLogBeforeTheRecord(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	log := largerWork(t, s, "replayed")
	inScope := shipReview(oracleFinding("P1", OracleClassificationDeliveryBlocker))
	log.pass("review-1", "accept_worker_evidence", shipReview())
	log.refine()
	proof := log.pass("review-2", "accept_worker_evidence", inScope)
	sized := operationEvent("replayed-sized-2", WorkSized, SubjectWorkItem, "replayed", workSizedPayload{ModelVersion: WorkSizeModelVersion, Size: WorkSizeCrucial, Inputs: largerWorkSizeInputs(), Footprint: workSizeFootprintFixture(), ReviewBasis: []string{proof}})
	if err := applyWorkEvent(t, s, sized, nil); err != nil {
		t.Fatal(err)
	}
	log.refine()
	log.pass("review-3", "accept_worker_evidence", inScope)

	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	scope, err := beginFold(ctx, tx)
	if err != nil {
		t.Fatal(err)
	}
	// Rebuild the projection as it stood before the record, then refold it.
	if _, err := tx.ExecContext(ctx, `DELETE FROM work_size_assessments WHERE event_id=?`, sized.EventID); err != nil {
		t.Fatal(err)
	}
	recorded := Event{EventID: sized.EventID}
	var occurredAt string
	if err := tx.QueryRowContext(ctx, `SELECT seq, kind, subject_type, subject_id, actor, occurred_at, payload_version, payload FROM domain_events WHERE event_id=?`, sized.EventID).
		Scan(&recorded.Seq, &recorded.Kind, &recorded.SubjectType, &recorded.SubjectID, &recorded.Actor, &occurredAt, &recorded.PayloadVersion, &recorded.Payload); err != nil {
		t.Fatal(err)
	}
	if recorded.OccurredAt, err = time.Parse(time.RFC3339Nano, occurredAt); err != nil {
		t.Fatal(err)
	}
	if err := foldRecordedEvent(workflowReplayContext(ctx), tx, recorded, scope); err != nil {
		t.Fatalf("replayed size record refused: %v", err)
	}
}

// While a review proof stands unrecorded, an operator size that changes the
// size waits for the rerun that records it: a resize would otherwise drop the
// proof from the lineage, and a later rerun could size the change down.
func TestWorkSizeOverrideWaitsForTheRerunAfterAProof(t *testing.T) {
	s := openTemp(t)
	log := largerWork(t, s, "proved-override")
	log.pass("review-1", "accept_worker_evidence", shipReview())
	log.refine()
	log.pass("review-2", "accept_worker_evidence", shipReview(oracleFinding("P1", OracleClassificationDeliveryBlocker)))
	insertWorkSizeOverrideApproval(t, s, "proved-override", 1)
	err := runWorkSizeOverride(t, s, workSizeOverrideRequest(t, s, "proved-override", "proved-override-override", WorkSizeSmall))
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindStaleRequiresReview || failure.RecoveryAction != RecoveryWorkSizeRerun {
		t.Fatalf("override over a standing proof = %v, want the rerun refusal", err)
	}
	if count := countRows(t, s, "work_size_overrides"); count != 0 {
		t.Fatalf("refused override wrote %d rows", count)
	}
}

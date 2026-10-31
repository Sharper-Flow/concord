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

func runDeliveryAdmissionAction(t *testing.T, s *Store, workID string, payload json.RawMessage, owner WorkflowActor) error {
	t.Helper()
	const action = "record_delivery"
	ctx, err := prepareTerminalTestContext(t.Context(), s, workID, action)
	if err != nil {
		return err
	}
	version := readWorkVersion(t, s, workID)
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := applyWorkflowActionRawTx(ctx, tx, newWriteScope(tx), BuiltinWorkflowRegistry(), WorkflowActionExecutionRequest{
		EvidenceRefs: actionEvidenceRefs(action, payload), WorkID: workID, ExpectedVersion: version, ActionID: action, Payload: payload, Actor: owner,
		AcceptedInputsDigest: "sha256:" + strings.Repeat("d", 64) + fmt.Sprint(version), IdempotencyIdentity: action + "-accept-delivery-" + workID + "-" + fmt.Sprint(version), OperationID: action + "-accept-delivery-" + workID + "-" + fmt.Sprint(version),
		PrincipalRef: owner.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: action + "-accept-delivery-" + workID + "-" + fmt.Sprint(version), RequestID: "request:accept-delivery-" + workID, ContractDigest: testManifestDigest, Now: time.Unix(9, 0).UTC(),
	}); err != nil {
		return err
	}
	return tx.Commit()
}

func workflowLatestDeliveryAssertionSeq(ctx context.Context, s *Store, workID string) (int64, error) {
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	return workflowLatestDeliveryAssertionSeqTx(ctx, tx, workID)
}

type acceptDeliveryCandidate struct {
	f          workContextFixture
	j          *candidateJourney
	definition RegisteredDefinition
	early      candidateReport
}

func acceptDeliveryCandidateAt(t *testing.T, workID, ref, step string, _ bool) acceptDeliveryCandidate {
	t.Helper()
	definition := mustBuiltinDefinition(t, ref)
	route := seedWorkflowReturnRouteFixtureWithDefinition(t, workID, definition, step, []string{"verification"}, []string{"review", "artifact"})
	f := workContextFixture{store: route.store, workID: workID, owner: route.owner, operator: route.operator}
	repo, _ := reviewSubjectClaim(t, f.store, workID)
	c := acceptDeliveryCandidate{f: f, j: &candidateJourney{fixture: f, s: f.store, workID: workID, repo: repo, acceptor: f.owner}, definition: definition}
	c.early = c.j.observeRun(t, "early")
	c.j.bindVerification(t, c.early)
	if step == "refine" {
		reviewGateStartStep(t, f.store, workID, "refine", "start_refine", f.owner)
	}
	qAcceptCriterionReview(t, f, c.early.OperationRef)
	return c
}

func acceptDeliveryBindFresh(t *testing.T, c acceptDeliveryCandidate, id string) {
	t.Helper()
	c.j.bindVerification(t, c.j.observeRun(t, id))
}

// Qualification is itself green proof without a tooling manifest. This fixture
// needs a manifest to distinguish candidate observation from a declared check.
func acceptDeliveryCorrectedCandidateAt(t *testing.T, workID, ref string) acceptDeliveryCandidate {
	t.Helper()
	c := acceptDeliveryCandidateAt(t, workID, ref, "refine", false)
	input := currentReviewBlockForTest(t, c.f.store, workID, "ready_for_final_proof").OutputCandidate
	path := filepath.Join(c.j.repo, "review.txt")
	if err := os.WriteFile(path, []byte("corrected candidate awaiting verification\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c.j.commit(t, "reviewer@example.invalid", "corrective candidate")
	report := currentReviewBlockForTest(t, c.f.store, workID, "ready_for_final_proof")
	report.InputCandidate = input
	if err := recordReviewForTest(t, c.f.store, workID, c.f.owner, report); err != nil {
		t.Fatal(err)
	}
	return c
}

// A retained Review can have no surviving producer receipt. Seed its projection:
// the live record_review route and its fold require a qualified subject,
// whose green observation would itself satisfy proof without a manifest.
func acceptDeliveryUnobservedCandidateAt(t *testing.T, workID, ref string) acceptDeliveryCandidate {
	t.Helper()
	definition := mustBuiltinDefinition(t, ref)
	route := seedWorkflowReturnRouteFixtureWithDefinition(t, workID, definition, "refine", []string{"verification"}, []string{"review", "artifact"})
	f := workContextFixture{store: route.store, workID: workID, owner: route.owner, operator: route.operator}
	repo, _ := reviewSubjectClaimObserved(t, f.store, workID, false)
	c := acceptDeliveryCandidate{f: f, j: &candidateJourney{fixture: f, s: f.store, workID: workID, repo: repo, acceptor: f.owner}, definition: definition}
	reviewGateStartStep(t, f.store, workID, "refine", "checkpoint_refine", f.owner)
	actor := mustActorRef(t, f.owner)
	report := currentReviewBlockForTest(t, f.store, workID, "ready_for_final_proof")
	state := WorkflowReviewState{Dependencies: &report.Dependencies}
	applyWorkflowAcceptedReview(&state, WorkflowAcceptedReview{ReportRef: "retained-review:" + workID, ReportDigest: reviewDigest(report), ActorRef: actor, Report: *report}, map[string]ReviewFinding{})
	if err := f.store.Transact(t.Context(), func(tx *Transaction) error {
		guard, err := beginWrite(t.Context(), tx.tx)
		if err != nil {
			return err
		}
		if err := writeWorkflowReviewFacts(t.Context(), tx.tx, workID, state); err != nil {
			_ = guard.close(t.Context())
			return err
		}
		return guard.close(t.Context())
	}); err != nil {
		t.Fatal(err)
	}
	return c
}

func acceptDeliveryFailedReport(t *testing.T, c acceptDeliveryCandidate) {
	t.Helper()
	report := c.j.observeRun(t, "failed")
	report.Report.Result = "fail"
	c.j.bindVerification(t, report)
}

var acceptDeliveryFamilies = []struct{ ref, workStep, nextStep string }{
	{"workflow.implementation", "execution", "acceptance"},
	{"workflow.break_fix", "repair", "verify"},
}

func TestDeliveryAdmissionRefusals(t *testing.T) {
	for _, family := range acceptDeliveryFamilies {
		t.Run(family.ref, func(t *testing.T) {
			for _, tc := range []struct {
				name                string
				unproved, unstarted bool
				seed                func(*testing.T, acceptDeliveryCandidate)
				kind                FailureKind
				detail              string
			}{
				{"no-verification-run", true, false, func(*testing.T, acceptDeliveryCandidate) {}, KindMissingEvidence, "no passing verification report covers the recorded Review candidate"},
				{"stale-run", true, false, func(t *testing.T, c acceptDeliveryCandidate) {
					c.j.bindVerification(t, c.early)
				}, KindMissingEvidence, "verification does not cover the current integrated candidate"},
				{"failed-run", true, false, func(t *testing.T, c acceptDeliveryCandidate) {
					acceptDeliveryFailedReport(t, c)
				}, KindMissingEvidence, "verification does not cover the current integrated candidate"},
				{"missing-step-start", false, true, func(t *testing.T, c acceptDeliveryCandidate) {
					setWorkflowStepForOperatorVerdictTest(t, c.f.store, c.f.workID, "refine")
					acceptDeliveryBindFresh(t, c, "unstarted")
				}, KindInvalidOperation, "requires the delivery step's fenced start action"},
				{"corrective-review", false, false, func(t *testing.T, c acceptDeliveryCandidate) {
					completeCurrentReviewForTest(t, c.f, "correction_required")
					acceptDeliveryBindFresh(t, c, "corrective")
				}, KindMissingEvidence, "Review has not reached ready_for_final_proof"},
			} {
				t.Run(tc.name, func(t *testing.T) {
					const workID = "delivery-refusal"
					step := "refine"
					if tc.unstarted {
						step = family.workStep
					}
					var c acceptDeliveryCandidate
					switch {
					case tc.unproved && tc.name != "stale-run":
						c = acceptDeliveryUnobservedCandidateAt(t, workID, family.ref)
					case tc.unproved:
						c = acceptDeliveryCorrectedCandidateAt(t, workID, family.ref)
					default:
						c = acceptDeliveryCandidateAt(t, workID, family.ref, step, false)
					}
					tc.seed(t, c)
					before := outsideRepairDatabaseSnapshot(t, c.f.store)
					err := runDeliveryAdmissionAction(t, c.f.store, workID, mustJSONValue(map[string]any{"delivery_artifact": "artifact:refine-exit", "delivery_state": "asserted"}), reviewGateAcceptor(workID))
					var failure *Failure
					if !failureAs(err, &failure) || failure.Kind != tc.kind || !strings.Contains(failure.Detail, tc.detail) {
						t.Fatalf("delivery refusal = %v, want %s naming %q", err, tc.kind, tc.detail)
					}
					reviewGateRequireStep(t, c.f.store, workID, "refine")
					if outsideRepairDatabaseSnapshot(t, c.f.store) != before {
						t.Fatal("refused delivery changed durable state")
					}
					if seq, err := workflowLatestDeliveryAssertionSeq(t.Context(), c.f.store, workID); err != nil || seq != 0 {
						t.Fatalf("refused delivery left assertion %d: %v", seq, err)
					}
				})
			}
		})
	}
}

func TestDeliveryReusesPreRefineReceipt(t *testing.T) {
	for _, family := range acceptDeliveryFamilies {
		t.Run(family.ref, func(t *testing.T) {
			c := acceptDeliveryCandidateAt(t, "delivery-reuse", family.ref, "refine", false)
			c.j.bindVerification(t, c.early)
			if err := runDeliveryAdmissionAction(t, c.f.store, c.f.workID, mustJSONValue(map[string]any{"delivery_artifact": "artifact:reused-proof", "delivery_state": "asserted"}), reviewGateAcceptor(c.f.workID)); err != nil {
				t.Fatal(err)
			}
			reviewGateRequireStep(t, c.f.store, c.f.workID, "delivery")
		})
	}
}

func TestDeliveryAssertionReaders(t *testing.T) {
	const workID = "delivery-assertion-readers"
	c := acceptDeliveryCandidateAt(t, workID, "workflow.implementation", "refine", false)
	s := c.f.store
	acceptDeliveryBindFresh(t, c, "readers")
	if err := runDeliveryAdmissionAction(t, s, workID, mustJSONValue(map[string]any{"delivery_artifact": "artifact:delivery", "delivery_state": "asserted"}), reviewGateAcceptor(workID)); err != nil {
		t.Fatal(err)
	}
	seq, err := workflowLatestDeliveryAssertionSeq(t.Context(), s, workID)
	if err != nil || seq == 0 {
		t.Fatalf("delivery assertion sequence = %d, %v", seq, err)
	}
	assertion, err := workflowDeliveryAssertionRead(t.Context(), s.db, workID)
	if err != nil || assertion == nil || assertion.Artifact != "artifact:delivery" {
		t.Fatalf("assertion read = %+v, %v", assertion, err)
	}
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	targetSeq, targetVersion, artifact, targetErr := workflowDeliveryAssertionEventTx(t.Context(), tx, workID, assertion.EventID)
	_ = tx.Rollback()
	if targetErr != nil || targetSeq != seq || targetVersion != assertion.TargetPayloadVersion || artifact != assertion.Artifact {
		t.Fatalf("correction target = %d/%d/%s, %v", targetSeq, targetVersion, artifact, targetErr)
	}
	parked := parkedDeliveryRead(t.Context(), s.db, c.definition.Definition, workID, "delivery", "running")
	if parked == nil || parked.StepID != "delivery" || parked.ResumeAction != "record_delivery" {
		t.Fatalf("parked delivery = %+v", parked)
	}
	var completions int
	if err := s.db.QueryRow(`SELECT count(*) FROM effect_receipts WHERE subject_id=? AND kind=? AND delivery_assertion=1`, workID, WorkflowActionCompleted).Scan(&completions); err != nil || completions != 1 {
		t.Fatalf("asserted delivery completions = %d, %v", completions, err)
	}
	reviewGateRequireStep(t, s, workID, "delivery")
}

// A report on a commit outside the accepted Review shows the candidate moved.
// The earlier Review and its report cannot then authorize delivery.
func TestDeliveryRefusesReviewAfterReportMovesCandidate(t *testing.T) {
	const workID = "delivery-moved-candidate"
	c := acceptDeliveryCandidateAt(t, workID, "workflow.implementation", "refine", false)
	if err := os.WriteFile(filepath.Join(c.j.repo, "moved.txt"), []byte("moved candidate\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c.j.commit(t, "implementer@example.invalid", "moved candidate")
	c.j.bindVerification(t, c.j.observeRun(t, "moved"))
	before := outsideRepairDatabaseSnapshot(t, c.f.store)
	err := runDeliveryAdmissionAction(t, c.f.store, workID, mustJSONValue(map[string]any{"delivery_artifact": "artifact:refine-exit", "delivery_state": "asserted"}), reviewGateAcceptor(workID))
	if !hasFailureKind(err, KindStaleRequiresReview) {
		t.Fatalf("delivery after a moved-candidate report = %v, want stale_requires_review", err)
	}
	if outsideRepairDatabaseSnapshot(t, c.f.store) != before {
		t.Fatal("refused delivery changed durable state")
	}
	reviewGateRequireStep(t, c.f.store, workID, "refine")
}

func TestDeliveryRefusesDistinctFailedReportAfterReview(t *testing.T) {
	const workID = "delivery-distinct-failed"
	c := acceptDeliveryCandidateAt(t, workID, "workflow.implementation", "refine", false)
	failed := c.j.observeRun(t, "distinct-failed")
	failed.Report.Result = "fail"
	failed.SubjectRef = "report:distinct-failed"
	c.j.bindVerification(t, failed)
	state, err := loadWorkflowReviewState(t.Context(), c.f.store.db, workID)
	if err != nil || state.DependenciesCurrent {
		t.Fatalf("failed current report left cited Review current: %+v, %v", state, err)
	}
	review := currentReviewBlockForTest(t, c.f.store, workID, "ready_for_final_proof")
	for _, criterion := range review.Dependencies.Criteria {
		review.Assessments = append(review.Assessments, ReviewAssessment{PredicateID: criterion.PredicateID, Assessment: "supported", ReceiptRefs: []string{c.early.OperationRef}})
	}
	if err := recordReviewForTest(t, c.f.store, workID, c.f.owner, review); err == nil {
		t.Fatal("new Review accepted a historical pass after a distinct failed report")
	}
	err = runDeliveryAdmissionAction(t, c.f.store, workID, mustJSONValue(map[string]any{"delivery_artifact": "artifact:refine-exit", "delivery_state": "asserted"}), reviewGateAcceptor(workID))
	if err == nil {
		t.Fatal("delivery accepted a historical pass after a distinct failed report")
	}
	reviewGateRequireStep(t, c.f.store, workID, "refine")
	recovered := c.j.observeRun(t, "recovered")
	recovered.SubjectRef = "report:recovered"
	c.j.bindVerification(t, recovered)
	if err := runDeliveryAdmissionAction(t, c.f.store, workID, mustJSONValue(map[string]any{"delivery_artifact": "artifact:refine-exit", "delivery_state": "asserted"}), reviewGateAcceptor(workID)); err != nil {
		t.Fatalf("passing rerun did not recover delivery: %v", err)
	}
}

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

// Tooling is resolved outside the action's transaction, as it is at runtime.
func runDeliveryAdmissionAction(t *testing.T, s *Store, workID, action string, payload json.RawMessage, tooling *ProjectToolingManifest, owner WorkflowActor) error {
	t.Helper()
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
		PrincipalRef: owner.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: action + "-accept-delivery-" + workID + "-" + fmt.Sprint(version), RequestID: "request:accept-delivery-" + workID, ContractDigest: testManifestDigest, Now: time.Unix(9, 0).UTC(), ProjectTooling: tooling,
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
	early      WorktreeVerifyResult
}

func acceptDeliveryCandidateAt(t *testing.T, workID, ref, step string, _ bool) acceptDeliveryCandidate {
	t.Helper()
	definition := mustBuiltinDefinition(t, ref)
	route := seedWorkflowReturnRouteFixtureWithDefinition(t, workID, definition, step, []string{"verification"}, []string{"review", "artifact"})
	f := workContextFixture{store: route.store, workID: workID, owner: route.owner, operator: route.operator}
	repo, _ := reviewSubjectClaim(t, f.store, workID)
	c := acceptDeliveryCandidate{f: f, j: &candidateJourney{fixture: f, s: f.store, workID: workID, repo: repo, acceptor: f.owner}, definition: definition}
	c.early = c.j.observeRun(t, "early")
	if step == "refine" {
		reviewGateStartStep(t, f.store, workID, "refine", "start_refine", f.owner)
	}
	qAcceptCriterionReview(t, f, repo, c.early.OperationRef)
	return c
}

func acceptDeliveryBindFresh(t *testing.T, c acceptDeliveryCandidate, id string) {
	t.Helper()
	c.j.bindVerification(t, c.j.observeRun(t, id))
}

// The new candidate has a qualified observation but no associated verification.
func acceptDeliveryUnprovedCandidateAt(t *testing.T, workID, ref string, _ bool) acceptDeliveryCandidate {
	t.Helper()
	c := acceptDeliveryCandidateAt(t, workID, ref, "refine", false)
	input := currentReviewBlockForTest(t, c.f.store, workID, "", "ready_for_final_proof").OutputCandidate
	path := filepath.Join(c.j.repo, "review.txt")
	if err := os.WriteFile(path, []byte("corrected candidate awaiting verification\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c.j.commit(t, "reviewer@example.invalid", "corrective candidate")
	qualifyReviewSubjectForTest(t, c.f.store, workID, c.j.repo)
	report := currentReviewBlockForTest(t, c.f.store, workID, "", "ready_for_final_proof")
	report.InputCandidate = input
	if err := recordReviewForTest(t, c.f.store, workID, c.f.owner, report); err != nil {
		t.Fatal(err)
	}
	return c
}

func acceptDeliveryUnbindableRun(t *testing.T, c acceptDeliveryCandidate, id string, command []string) {
	t.Helper()
	request := c.j.request("observe-"+id, nil)
	request.Command = command
	run, _ := c.f.store.VerifyWorktree(t.Context(), request)
	if err := runTrackedFileRestore(c.j.repo); err != nil {
		t.Fatal(err)
	}
	if run.OperationRef == "" {
		return
	}
	err := c.j.action(t, "bind_evidence", map[string]any{
		"evidence_kind": "verification", "immutable_subject_ref": run.OperationRef, "producer_id": "principal/operator",
		"producer_run_ref": run.OperationRef, "producer_watermark": "request-" + strings.TrimPrefix(run.LeaseID, "candidate-"),
	}, c.j.acceptor)
	if err == nil {
		t.Fatalf("bind of the %s run %s was admitted", id, run.OperationRef)
	}
}

func runTrackedFileRestore(repo string) error {
	return os.WriteFile(filepath.Join(repo, "seed.txt"), []byte("review subject seed\n"), 0o644)
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
				seed                func(*testing.T, acceptDeliveryCandidate) *ProjectToolingManifest
				kind                FailureKind
				detail              string
			}{
				{"no-verification-run", true, false, func(*testing.T, acceptDeliveryCandidate) *ProjectToolingManifest { return nil }, KindMissingEvidence, "no passing producer verification covers the current integrated candidate"},
				{"stale-run", true, false, func(t *testing.T, c acceptDeliveryCandidate) *ProjectToolingManifest {
					c.j.bindVerification(t, c.early)
					return nil
				}, KindMissingEvidence, "verification does not cover the current integrated candidate"},
				{"failed-run", true, false, func(t *testing.T, c acceptDeliveryCandidate) *ProjectToolingManifest {
					acceptDeliveryUnbindableRun(t, c, "failed", []string{"false"})
					return nil
				}, KindMissingEvidence, "no passing producer verification covers the current integrated candidate"},
				{"dirty-run", true, false, func(t *testing.T, c acceptDeliveryCandidate) *ProjectToolingManifest {
					acceptDeliveryUnbindableRun(t, c, "dirty", []string{"sh", "-c", "echo dirty >> seed.txt"})
					return nil
				}, KindMissingEvidence, "no passing producer verification covers the current integrated candidate"},
				{"undeclared-run", false, false, func(t *testing.T, c acceptDeliveryCandidate) *ProjectToolingManifest {
					acceptDeliveryBindFresh(t, c, "undeclared")
					return refineProofTooling(ProjectDeclaredTool{ID: "go-test-race", Invocation: "go test -race ./..."})
				}, KindMissingEvidence, "not a tool the Project declares"},
				{"missing-step-start", false, true, func(t *testing.T, c acceptDeliveryCandidate) *ProjectToolingManifest {
					setWorkflowStepForOperatorVerdictTest(t, c.f.store, c.f.workID, "refine")
					acceptDeliveryBindFresh(t, c, "unstarted")
					return nil
				}, KindInvalidOperation, "requires the delivery step's fenced start action"},
				{"corrective-review", false, false, func(t *testing.T, c acceptDeliveryCandidate) *ProjectToolingManifest {
					completeCurrentReviewForTest(t, c.f, c.j.repo, "correction_required")
					acceptDeliveryBindFresh(t, c, "corrective")
					return nil
				}, KindMissingEvidence, "correction_required"},
			} {
				t.Run(tc.name, func(t *testing.T) {
					const workID = "delivery-refusal"
					step := "refine"
					if tc.unstarted {
						step = family.workStep
					}
					var c acceptDeliveryCandidate
					if tc.unproved {
						c = acceptDeliveryUnprovedCandidateAt(t, workID, family.ref, false)
					} else {
						c = acceptDeliveryCandidateAt(t, workID, family.ref, step, false)
					}
					tooling := tc.seed(t, c)
					before := outsideRepairDatabaseSnapshot(t, c.f.store)
					err := runDeliveryAdmissionAction(t, c.f.store, workID, "record_delivery", mustJSONValue(map[string]any{"delivery_artifact": "artifact:refine-exit", "delivery_state": "asserted"}), tooling, reviewGateAcceptor(workID))
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
			if err := runDeliveryAdmissionAction(t, c.f.store, c.f.workID, "record_delivery", mustJSONValue(map[string]any{"delivery_artifact": "artifact:reused-proof", "delivery_state": "asserted"}), nil, reviewGateAcceptor(c.f.workID)); err != nil {
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
	if err := runDeliveryAdmissionAction(t, s, workID, "record_delivery", mustJSONValue(map[string]any{"delivery_artifact": "artifact:delivery", "delivery_state": "asserted"}), nil, reviewGateAcceptor(workID)); err != nil {
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
	if err := s.db.QueryRow(`SELECT count(*) FROM workflow_completed_exits WHERE work_id=? AND action_id='record_delivery'`, workID).Scan(&completions); err != nil || completions != 1 {
		t.Fatalf("asserted delivery completions = %d, %v", completions, err)
	}
	reviewGateRequireStep(t, s, workID, "delivery")
}

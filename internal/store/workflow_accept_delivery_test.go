package store

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// The CD-0198 D4 combined accept: an accept_worker_result at the refinement
// step carries the delivery fields and runs the unchanged record_delivery
// admission — fenced start, CD-0192 refine proof with default-ref tooling,
// CD-0166 D6 post-rejection gate — and on success records the acceptance,
// the asserted delivery event, and the refine to delivery-gate advance in
// one transaction. Any refusal leaves no acceptance, delivery, or advance.

// acceptDeliveryRunDigest derives the deterministic verify-run digest one
// refine accept seeds, so a retried accept reuses the run instead of
// colliding with the lease primary key.
func acceptDeliveryRunDigest(workID, attemptID string) string {
	sum := sha256.Sum256([]byte(workID + ":" + attemptID))
	return fmt.Sprintf("%x", sum[:])
}

// runDeliveryAdmissionAction applies one delivery-admission action with an
// explicitly resolved tooling manifest, the shape the runtime assembles
// outside the transaction (CD-0192).
func runDeliveryAdmissionAction(t *testing.T, s *Store, workID, action string, payload json.RawMessage, tooling *ProjectToolingManifest, owner WorkflowActor) error {
	t.Helper()
	version := readWorkVersion(t, s, workID)
	tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := applyWorkflowActionRawTx(context.Background(), tx, newWriteScope(tx), BuiltinWorkflowRegistry(), WorkflowActionExecutionRequest{
		EvidenceRefs: actionEvidenceRefs(action, payload), WorkID: workID, ExpectedVersion: version, ActionID: action, Payload: payload, Actor: owner,
		AcceptedInputsDigest: "sha256:" + strings.Repeat("d", 64) + fmt.Sprint(version), IdempotencyIdentity: action + "-accept-delivery-" + workID + "-" + fmt.Sprint(version), OperationID: action + "-accept-delivery-" + workID + "-" + fmt.Sprint(version),
		PrincipalRef: owner.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: action + "-accept-delivery-" + workID + "-" + fmt.Sprint(version), RequestID: "request:accept-delivery-" + workID, ContractDigest: testManifestDigest, Now: time.Unix(9, 0).UTC(),
		ProjectTooling: tooling,
	}); err != nil {
		return err
	}
	return tx.Commit()
}

// acceptRefineResult accepts a completed worker attempt at the refinement
// step. On the CD-0198 D4 versions the accept carries its delivery fields and
// the admission reads a green verify run, which this helper seeds first; on
// the job-capable versions the run must additionally integrate the recorded
// job acceptances, so the seed anchors after the latest acceptance. Earlier
// versions keep the plain advancing accept they shipped with.
func acceptRefineResult(t *testing.T, s *Store, workID, attemptID string, epoch int64, acceptor WorkflowActor) error {
	t.Helper()
	entry, err := VerifyWorkflowInstanceDefinition(context.Background(), s, BuiltinWorkflowRegistry(), workID)
	if err != nil {
		return err
	}
	step := currentStep(t, s, workID)
	payload := json.RawMessage(`{"attempt_id":"` + attemptID + `","attempt_epoch":` + fmt.Sprint(epoch) + `}`)
	if workflowAcceptDeliveryAdmissionActive(entry.Definition, step) {
		refineProofSeedGreenRun(t, s, workID, acceptDeliveryRunDigest(workID, attemptID))
		if workflowWorkerJobsActive(entry.Definition) {
			workerJobIntegrationGreenRun(t, s, workID, acceptDeliveryRunDigest(workID, attemptID))
		}
		payload = json.RawMessage(`{"attempt_id":"` + attemptID + `","attempt_epoch":` + fmt.Sprint(epoch) + `,"delivery_artifact":"artifact:accept-delivery-` + workID + `","delivery_state":"asserted"}`)
	}
	return runVerdictActionAs(t, s, workID, "accept_worker_result", payload, 0, acceptor)
}

// acceptDeliverySeedExitRun seeds the green run the admission reads whenever
// the refinement step has started; without a start no run exists to prove
// and the admission refuses on the missing start first.
func acceptDeliverySeedExitRun(t *testing.T, s *Store, workID, digest string) {
	t.Helper()
	if _, _, started, err := latestWorkflowActionStart(context.Background(), s.db, workID, "refine"); err != nil {
		t.Fatal(err)
	} else if !started {
		return
	}
	refineProofSeedGreenRun(t, s, workID, digest)
}

// deliveryEntryRoute names one delivery-assertion entry route the refusal
// suites parameterize over: the standalone record_delivery exit, and the
// combined accept that carries its artifact (CD-0198 D4).
type deliveryEntryRoute struct {
	name string
	// exit drives the route's exit at the refinement step with the tooling
	// manifest the caller resolved. seed reports whether the route must seed
	// the qualifying run itself; a case that seeds its own disqualifying run
	// passes false. attemptID and epoch name the completed attempt the
	// combined accept dispositions; the record_delivery route ignores them.
	exit func(t *testing.T, s *Store, workID, attemptID string, epoch int64, acceptor WorkflowActor, tooling *ProjectToolingManifest, seed bool) error
}

func deliveryEntryRoutes() []deliveryEntryRoute {
	return []deliveryEntryRoute{
		{name: "record_delivery", exit: func(t *testing.T, s *Store, workID, _ string, _ int64, acceptor WorkflowActor, tooling *ProjectToolingManifest, seed bool) error {
			if seed {
				acceptDeliverySeedExitRun(t, s, workID, acceptDeliveryRunDigest(workID, "record-delivery"))
			}
			payload := json.RawMessage(`{"delivery_artifact":"artifact:refine-exit-` + workID + `","delivery_state":"asserted"}`)
			return runDeliveryAdmissionAction(t, s, workID, "record_delivery", payload, tooling, acceptor)
		}},
		{name: "accept_worker_result", exit: func(t *testing.T, s *Store, workID, attemptID string, epoch int64, acceptor WorkflowActor, tooling *ProjectToolingManifest, seed bool) error {
			if seed {
				acceptDeliverySeedExitRun(t, s, workID, acceptDeliveryRunDigest(workID, attemptID))
			}
			payload := json.RawMessage(`{"attempt_id":"` + attemptID + `","attempt_epoch":` + fmt.Sprint(epoch) + `,"delivery_artifact":"artifact:refine-exit-` + workID + `","delivery_state":"asserted"}`)
			return runDeliveryAdmissionAction(t, s, workID, "accept_worker_result", payload, tooling, acceptor)
		}},
	}
}

// workflowLatestDeliveryAssertionSeq adapts the tx-scoped latest-assertion
// read to a whole-store read for the reader tests.
func workflowLatestDeliveryAssertionSeq(ctx context.Context, s *Store, workID string) (int64, error) {
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	return workflowLatestDeliveryAssertionSeqTx(ctx, tx, workID)
}

// acceptDeliveryFixture holds one seeded workflow at its pre-refine work step
// on a CD-0198 D4 definition version.
type acceptDeliveryFixture struct {
	fixture     workflowReturnRouteFixture
	definition  RegisteredDefinition
	verdictStep string
	startAction string
}

// acceptDeliveryFixtureFor seeds one work item pinned to the named family's
// current definition at the step the case drives from: the refinement step, or the
// pre-refine work step when the case crosses the work pass first.
func acceptDeliveryFixtureFor(t *testing.T, workID, definitionRef, targetStep, startAction string) acceptDeliveryFixture {
	t.Helper()
	registered, ok := BuiltinWorkflowRegistry().Current(definitionRef)
	if !ok {
		t.Fatalf("workflow definition %s is not registered", definitionRef)
	}
	return acceptDeliveryFixture{
		fixture:     seedWorkflowReturnRouteFixtureWithDefinition(t, workID, registered, targetStep, []string{"verification"}, []string{"verification", "review", "artifact"}),
		definition:  registered,
		verdictStep: startAction[len("start_"):],
		startAction: startAction,
	}
}

// acceptDeliveryCandidate is current work of one family over a reviewed
// candidate: a claimed Git worktree, a command receipt captured before the
// step's start, and an accepted ready_for_final_proof Review of that receipt.
// When pending is set, a second Review attempt stands completed and
// unaccepted: the attempt a combined accept dispositions.
type acceptDeliveryCandidate struct {
	f          workContextFixture
	j          *candidateJourney
	definition RegisteredDefinition
	early      WorktreeVerifyResult
	attempt    string
	epoch      int64
}

// acceptDeliveryCandidateAt seeds the candidate at the named step. At the
// refinement step the fenced start runs after the early receipt, so that
// receipt predates the refine start.
func acceptDeliveryCandidateAt(t *testing.T, workID, ref, step string, pending bool) acceptDeliveryCandidate {
	t.Helper()
	definition := mustBuiltinDefinition(t, ref)
	// No verification binding is seeded; the producer receipt is still applicable.
	route := seedWorkflowReturnRouteFixtureWithDefinition(t, workID, definition, step, []string{"verification"}, []string{"review", "artifact"})
	f := workContextFixture{store: route.store, workID: workID, owner: route.owner, operator: route.operator}
	repo, _ := reviewSubjectClaim(t, f.store, workID)
	var branch string
	if err := f.store.db.QueryRow(`SELECT branch FROM worktree_entries WHERE set_id=? AND state='active'`, WorktreeSetID(workID)).Scan(&branch); err != nil {
		t.Fatal(err)
	}
	gitRunStore(t, repo, "branch", "-m", branch)
	c := acceptDeliveryCandidate{f: f, j: &candidateJourney{fixture: f, s: f.store, workID: workID, repo: repo, acceptor: f.owner}, definition: definition}
	c.early = c.j.observeRun(t, "early")
	if step == "refine" {
		reviewGateStartStep(t, f.store, workID, "refine", "start_refine", f.owner)
	}
	qAcceptCriterionReview(t, f, repo, c.early.OperationRef)
	if pending {
		c.attempt = "attempt:" + workID + ":review"
		c.epoch = acceptDeliveryCompleteReview(t, c, c.attempt, "ready_for_final_proof")
	}
	return c
}

// acceptDeliveryCompleteReview dispatches one job-bound Review attempt at the
// current step and lands its attributed disposition, leaving the result
// unaccepted. It returns the attempt's authorized epoch.
func acceptDeliveryCompleteReview(t *testing.T, c acceptDeliveryCandidate, attempt, disposition string) int64 {
	t.Helper()
	s, workID := c.f.store, c.f.workID
	job := reviewSubjectReadyRevision(t, c.f)
	if err := descendantDispatch(t, c.j, "review", attempt, job, nil); err != nil {
		t.Fatalf("review dispatch %s: %v", attempt, err)
	}
	c.j.laneDispatch(t, attempt, "review", &job)
	report := currentReviewBlockForTest(t, s, workID, attempt, disposition)
	for _, criterion := range report.Dependencies.Criteria {
		report.Assessments = append(report.Assessments, WorkerReviewAssessment{PredicateID: criterion.PredicateID, Assessment: "supported", ReceiptRefs: []string{c.early.OperationRef}})
	}
	lane := reviewGateLane(t, "review")
	evidence := []WorkerReportEvidence{}
	for _, obligation := range lane.EvidenceObligations {
		if obligation != "severity" {
			evidence = append(evidence, WorkerReportEvidence{Obligation: obligation, Detail: "Reviewed the candidate and its captured command receipt."})
		}
	}
	event := Event{EventID: attempt + ":completed", Kind: WorkerCompleted, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "worker:test", OccurredAt: time.Now().UTC(), PayloadVersion: CurrentEventPayloadVersion(WorkerCompleted), Payload: mustJSONValue(WorkerCompletedPayload{
		AttemptID: attempt, ReadbackModel: preferredModelForLane(lane), ReportSchemaVersion: WorkerReportSchemaVersion, WorkerDirectory: c.j.repo,
		Evidence: evidence, EvidenceOrigin: WorkerEvidenceReported, WorkerJob: &job, Review: report,
	})}
	prepared, err := PrepareWorkerTerminalDelivery(t.Context(), s, PrepareWorkerTerminalDeliveryRequest{WorkID: workID, AttemptID: attempt, EventID: event.EventID})
	if err != nil {
		t.Fatal(err)
	}
	workerTerminalEventThroughSeamCtx(t, WithWorkerTerminalPreparation(context.Background(), prepared), s, event)
	return c.j.dispatchEpoch(t, attempt)
}

// acceptDeliveryBindFresh binds one fresh green receipt of the unchanged
// candidate, acquired after every start the fixture recorded.
func acceptDeliveryBindFresh(t *testing.T, c acceptDeliveryCandidate, id string) {
	t.Helper()
	c.j.bindVerification(t, c.j.observeRun(t, id))
}

// acceptDeliveryUnprovedCandidateAt lands a corrective Review of a new commit
// without running verification on it. The earlier candidate's proof remains.
func acceptDeliveryUnprovedCandidateAt(t *testing.T, workID, ref string, pending bool) acceptDeliveryCandidate {
	t.Helper()
	c := acceptDeliveryCandidateAt(t, workID, ref, "refine", false)
	job := reviewSubjectReadyRevision(t, c.f)
	attempt := "attempt:" + workID + ":unproved"
	if err := descendantDispatch(t, c.j, "review", attempt, job, nil); err != nil {
		t.Fatal(err)
	}
	c.j.laneDispatch(t, attempt, "review", &job)
	path := filepath.Join(c.j.repo, "internal", "store", job.JobID, "review.txt")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("corrected candidate awaiting verification\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	output := c.j.commit(t, "reviewer@example.invalid", "corrective candidate")
	report := currentReviewBlockForTest(t, c.f.store, workID, attempt, "ready_for_final_proof")
	report.OutputCandidate = WorkerReviewCandidate{ProjectID: report.InputCandidate.ProjectID, Commit: output, Tree: reviewObservedTree(t, c.j.repo, output)}
	c.j.completeReview(t, attempt, job, report)
	if pending {
		c.attempt, c.epoch = attempt, c.j.dispatchEpoch(t, attempt)
	} else if err := c.j.acceptEvidence(t, attempt); err != nil {
		t.Fatal(err)
	}
	return c
}

// acceptDeliveryUnbindableRun runs one verify command that yields no passing
// unchanged-tree receipt and proves the core refuses to bind it, so neither
// delivery route can ever read it as proof.
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
		t.Fatalf("bind of the %s run %s was admitted, want a refusal", id, run.OperationRef)
	}
}

// runTrackedFileRestore restores the claim's tracked seed file after a
// verify command that edited it.
func runTrackedFileRestore(repo string) error {
	return os.WriteFile(filepath.Join(repo, "seed.txt"), []byte("review subject seed\n"), 0o644)
}

func acceptDeliveryCombinedPayload(c acceptDeliveryCandidate, artifact string) json.RawMessage {
	return mustJSONValue(map[string]any{"attempt_id": c.attempt, "attempt_epoch": c.epoch, "delivery_artifact": artifact, "delivery_state": "asserted"})
}

// acceptDeliveryFamilies names the two current delivery-producing families
// with their pre-refine work step and the step past the delivery gate.
var acceptDeliveryFamilies = []struct {
	ref      string
	workStep string
	nextStep string
}{
	{"workflow.implementation", "execution", "acceptance"},
	{"workflow.break_fix", "repair", "verify"},
}

func TestCombinedAcceptDeliversAtomically(t *testing.T) {
	for _, family := range acceptDeliveryFamilies {
		t.Run(family.ref, func(t *testing.T) {
			const workID = "combined-accept-atomic"
			c := acceptDeliveryCandidateAt(t, workID, family.ref, "refine", true)
			s := c.f.store
			acceptor := reviewGateAcceptor(workID)
			acceptDeliveryBindFresh(t, c, "exit")
			if err := runDeliveryAdmissionAction(t, s, workID, "accept_worker_result", acceptDeliveryCombinedPayload(c, "artifact:accept-delivery-"+workID), nil, acceptor); err != nil {
				t.Fatalf("combined accept at refine: %v", err)
			}

			// One operation recorded the assertion: the accept completion
			// carries the delivery fields and no record_delivery call ran.
			var acceptCompletions int
			if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM worker_attempt_dispositions WHERE work_id=? AND action_id='accept_worker_result' AND attempt_id=? AND json_extract(payload,'$.delivery_artifact')='artifact:accept-delivery-`+workID+`' AND json_extract(payload,'$.delivery_state')='asserted'`, workID, c.attempt).Scan(&acceptCompletions); err != nil {
				t.Fatal(err)
			}
			if acceptCompletions != 1 {
				t.Fatalf("combined accept recorded %d asserted completions, want 1", acceptCompletions)
			}
			var refineDeliveries int
			if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM workflow_completed_exits WHERE work_id=? AND action_id='record_delivery' AND step_id IN ('refine','delivery')`, workID).Scan(&refineDeliveries); err != nil {
				t.Fatal(err)
			}
			if refineDeliveries != 0 {
				t.Fatalf("combined accept recorded %d record_delivery events at refine or the gate, want none", refineDeliveries)
			}
			if seq, err := workflowLatestDeliveryAssertionSeq(context.Background(), s, workID); err != nil {
				t.Fatal(err)
			} else if seq == 0 {
				t.Fatal("the combined accept recorded no delivery assertion")
			}

			// The gate stays record_delivery-only.
			gate := workflowStep(c.definition.Definition, "delivery")
			if !workflowStepIsDeliveryGate(gate) || gate.Actions[0] != "record_delivery" || slices.Contains(gate.Actions, "accept_worker_result") {
				t.Fatalf("%s gate actions = %v, want a record_delivery-only gate", family.ref, gate.Actions)
			}

			// CD-0198 D4: the same transaction advances refine to the gate,
			// and the gate's record_delivery crosses to the verdict step.
			reviewGateRequireStep(t, s, workID, "delivery")
			delivery := json.RawMessage(`{"delivery_artifact":"artifact:gate-cross-` + workID + `","delivery_state":"asserted"}`)
			if err := runVerdictActionAs(t, s, workID, "record_delivery", delivery, 0, acceptor); err != nil {
				t.Fatalf("gate crossing after the combined accept: %v", err)
			}
			reviewGateRequireStep(t, s, workID, family.nextStep)
		})
	}
}

// TestDeliveryAdmissionRefusalsMatchAcrossEntryRoutes proves the combined
// accept refuses what record_delivery refuses — a missing, stale, failed,
// dirty, or undeclared verification run — and that record_delivery refuses a
// missing fenced start and a rejected Review. Every refusal leaves no
// acceptance, delivery, or advance. The combined accept dispositions an
// attempt dispatched at the step, and that dispatch derives the step start
// (CD-0198 D3), so its missing-start and rejected-Review equivalents cannot
// arise.
func TestDeliveryAdmissionRefusalsMatchAcrossEntryRoutes(t *testing.T) {
	for _, family := range acceptDeliveryFamilies {
		t.Run(family.ref, func(t *testing.T) {
			cases := []struct {
				name       string
				routes     []string
				step       string
				unproved   bool
				seed       func(t *testing.T, c acceptDeliveryCandidate) *ProjectToolingManifest
				wantKind   FailureKind
				wantDetail string
			}{
				{name: "no-verification-run", unproved: true, seed: func(*testing.T, acceptDeliveryCandidate) *ProjectToolingManifest { return nil },
					wantKind: KindMissingEvidence, wantDetail: "no passing producer verification covers the current integrated candidate"},
				{name: "stale-run", unproved: true, seed: func(t *testing.T, c acceptDeliveryCandidate) *ProjectToolingManifest {
					c.j.bindVerification(t, c.early)
					return nil
				}, wantKind: KindMissingEvidence, wantDetail: "verification does not cover the current integrated candidate"},
				{name: "failed-run", unproved: true, seed: func(t *testing.T, c acceptDeliveryCandidate) *ProjectToolingManifest {
					acceptDeliveryUnbindableRun(t, c, "failed", []string{"false"})
					return nil
				}, wantKind: KindMissingEvidence, wantDetail: "no passing producer verification covers the current integrated candidate"},
				{name: "dirty-run", unproved: true, seed: func(t *testing.T, c acceptDeliveryCandidate) *ProjectToolingManifest {
					acceptDeliveryUnbindableRun(t, c, "dirty", []string{"sh", "-c", "echo dirty >> seed.txt"})
					return nil
				}, wantKind: KindMissingEvidence, wantDetail: "no passing producer verification covers the current integrated candidate"},
				{name: "undeclared-run", seed: func(t *testing.T, c acceptDeliveryCandidate) *ProjectToolingManifest {
					acceptDeliveryBindFresh(t, c, "undeclared")
					return refineProofTooling(ProjectDeclaredTool{ID: "go-test-race", Invocation: "go test -race ./..."})
				}, wantKind: KindMissingEvidence, wantDetail: "not a tool the Project declares"},
				{name: "missing-step-start", routes: []string{"record_delivery"}, step: "work", seed: func(t *testing.T, c acceptDeliveryCandidate) *ProjectToolingManifest {
					parkWorkflowInstanceAt(t, c.f.store, c.f.workID, "refine")
					acceptDeliveryBindFresh(t, c, "unstarted")
					return nil
				}, wantKind: KindInvalidOperation, wantDetail: "requires the delivery step's fenced start action"},
				{name: "rejected-review", routes: []string{"record_delivery"}, seed: func(t *testing.T, c acceptDeliveryCandidate) *ProjectToolingManifest {
					rejected := "attempt:" + c.f.workID + ":rejected"
					epoch := acceptDeliveryCompleteReview(t, c, rejected, "ready_for_final_proof")
					reviewGateRejectResult(t, c.f.store, c.f.workID, rejected, epoch, reviewGateAcceptor(c.f.workID))
					acceptDeliveryBindFresh(t, c, "after-rejection")
					return nil
				}, wantKind: KindMissingEvidence, wantDetail: "remain unsatisfied"},
			}
			for _, testCase := range cases {
				routes := testCase.routes
				if routes == nil {
					routes = []string{"record_delivery", "accept_worker_result"}
				}
				for _, route := range routes {
					t.Run(testCase.name+"/"+route, func(t *testing.T) {
						const workID = "accept-delivery-refusal"
						step := "refine"
						if testCase.step == "work" {
							step = family.workStep
						}
						// The record_delivery route has no pending attempt: an
						// unaccepted job would refuse on the job population
						// before the case's own disqualifier.
						var c acceptDeliveryCandidate
						if testCase.unproved {
							c = acceptDeliveryUnprovedCandidateAt(t, workID, family.ref, route == "accept_worker_result")
						} else {
							c = acceptDeliveryCandidateAt(t, workID, family.ref, step, route == "accept_worker_result")
						}
						s := c.f.store
						tooling := testCase.seed(t, c)
						payload := json.RawMessage(`{"delivery_artifact":"artifact:refine-exit-` + workID + `","delivery_state":"asserted"}`)
						if route == "accept_worker_result" {
							payload = acceptDeliveryCombinedPayload(c, "artifact:refine-exit-"+workID)
						}
						err := runDeliveryAdmissionAction(t, s, workID, route, payload, tooling, reviewGateAcceptor(workID))
						var failure *Failure
						if err == nil || !failureAs(err, &failure) || failure.Kind != testCase.wantKind || !strings.Contains(failure.Detail, testCase.wantDetail) {
							t.Fatalf("%s refusal = %v, want kind %s naming %q", route, err, testCase.wantKind, testCase.wantDetail)
						}

						// The refusal is atomic: the step holds, no acceptance
						// recorded, and no delivery assertion exists.
						reviewGateRequireStep(t, s, workID, "refine")
						if c.attempt != "" {
							var accepted int
							if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM worker_attempt_dispositions WHERE work_id=? AND action_id='accept_worker_result' AND attempt_id=?`, workID, c.attempt).Scan(&accepted); err != nil {
								t.Fatal(err)
							}
							if accepted != 0 {
								t.Fatalf("refused combined accept recorded %d acceptances", accepted)
							}
						}
						if seq, seqErr := workflowLatestDeliveryAssertionSeq(context.Background(), s, workID); seqErr != nil {
							t.Fatal(seqErr)
						} else if seq != 0 {
							t.Fatalf("%s refusal left a delivery assertion at seq %d", route, seq)
						}
					})
				}
			}
		})
	}
}

func TestDeliveryReusesPreRefineReceiptAcrossEntryRoutes(t *testing.T) {
	for _, family := range acceptDeliveryFamilies {
		for _, route := range []string{"record_delivery", "accept_worker_result"} {
			t.Run(family.ref+"/"+route, func(t *testing.T) {
				const workID = "accept-delivery-reuse"
				c := acceptDeliveryCandidateAt(t, workID, family.ref, "refine", route == "accept_worker_result")
				c.j.bindVerification(t, c.early)
				payload := mustJSONValue(map[string]any{"delivery_artifact": "artifact:reused-proof", "delivery_state": "asserted"})
				if route == "accept_worker_result" {
					payload = acceptDeliveryCombinedPayload(c, "artifact:reused-proof")
				}
				if err := runDeliveryAdmissionAction(t, c.f.store, workID, route, payload, nil, reviewGateAcceptor(workID)); err != nil {
					t.Fatalf("unchanged proof captured before refine refused: %v", err)
				}
				reviewGateRequireStep(t, c.f.store, workID, "delivery")
			})
		}
	}
}

// TestRefineExitAdmitsOnlyADeliveryAssertion proves the CD-0198 D4
// boundaries on the current definitions: a plain accept at refine records the
// job's local acceptance and leaves refine without a delivery assertion, and
// delivery fields on an accept at the work step refuse.
func TestRefineExitAdmitsOnlyADeliveryAssertion(t *testing.T) {
	for _, family := range acceptDeliveryFamilies {
		t.Run(family.ref, func(t *testing.T) {
			const workID = "refine-exit-plain"
			c := acceptDeliveryCandidateAt(t, workID, family.ref, "refine", true)
			s := c.f.store
			acceptDeliveryBindFresh(t, c, "plain")
			plain := mustJSONValue(map[string]any{"attempt_id": c.attempt, "attempt_epoch": c.epoch})
			if err := runDeliveryAdmissionAction(t, s, workID, "accept_worker_result", plain, nil, reviewGateAcceptor(workID)); err != nil {
				t.Fatalf("plain local accept at refine: %v", err)
			}
			reviewGateRequireStep(t, s, workID, "refine")
			if seq, err := workflowLatestDeliveryAssertionSeq(context.Background(), s, workID); err != nil {
				t.Fatal(err)
			} else if seq != 0 {
				t.Fatalf("plain accept at refine recorded a delivery assertion at seq %d", seq)
			}

			// Delivery fields on an accept at the work step refuse: the
			// combined route exists only at the refinement step.
			const workIDWork = "refine-exit-work-step"
			work := acceptDeliveryCandidateAt(t, workIDWork, family.ref, family.workStep, true)
			acceptDeliveryBindFresh(t, work, "work")
			err := runDeliveryAdmissionAction(t, work.f.store, workIDWork, "accept_worker_result", acceptDeliveryCombinedPayload(work, "artifact:misplaced"), nil, reviewGateAcceptor(workIDWork))
			var misplaced *Failure
			if err == nil || !failureAs(err, &misplaced) || misplaced.Kind != KindInvalidOperation || !strings.Contains(misplaced.Detail, "carries delivery fields only at the delivery-admitting refinement step") {
				t.Fatalf("accept with delivery fields at %s = %v, want the misplaced-fields refusal", family.workStep, err)
			}
			reviewGateRequireStep(t, work.f.store, workIDWork, family.workStep)
		})
	}
}

// TestCombinedDeliveryAssertionReaders proves the delivery readers recognize
// the combined assertion.
func TestCombinedDeliveryAssertionReaders(t *testing.T) {
	const workID = "combined-assertion-readers"
	c := acceptDeliveryCandidateAt(t, workID, "workflow.implementation", "refine", true)
	s := c.f.store
	acceptDeliveryBindFresh(t, c, "readers")
	if err := runDeliveryAdmissionAction(t, s, workID, "accept_worker_result", acceptDeliveryCombinedPayload(c, "artifact:accept-delivery-"+workID), nil, reviewGateAcceptor(workID)); err != nil {
		t.Fatalf("combined accept: %v", err)
	}

	var acceptSeq, acceptPayloadVersion int64
	var acceptEventID string
	if err := s.DatabaseForTesting().QueryRow(`SELECT d.seq,r.payload_version,d.event_id FROM worker_attempt_dispositions d JOIN effect_receipts r ON r.event_id=d.event_id AND r.effect_seq=d.seq WHERE d.work_id=? AND d.action_id='accept_worker_result' AND d.attempt_id=?`, workID, c.attempt).Scan(&acceptSeq, &acceptPayloadVersion, &acceptEventID); err != nil {
		t.Fatal(err)
	}

	// Every reader keys on the asserted delivery fields, not on action_id:
	// the latest-assertion read, the parked assertion read, and the
	// correct_delivery target identity all answer the combined event.
	if seq, err := workflowLatestDeliveryAssertionSeq(context.Background(), s, workID); err != nil {
		t.Fatal(err)
	} else if seq != acceptSeq {
		t.Fatalf("latest delivery assertion seq = %d, want the combined accept's %d", seq, acceptSeq)
	}
	assertion, err := workflowDeliveryAssertionRead(context.Background(), s.db, workID)
	if err != nil || assertion == nil {
		t.Fatalf("assertion read = (%+v, %v)", assertion, err)
	}
	if assertion.EventID != acceptEventID || int64(assertion.TargetPayloadVersion) != acceptPayloadVersion || assertion.Artifact != "artifact:accept-delivery-"+workID {
		t.Fatalf("assertion read = %+v, want the combined accept event", assertion)
	}
	tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	targetSeq, targetVersion, targetArtifact, targetErr := workflowDeliveryAssertionEventTx(context.Background(), tx, workID, acceptEventID)
	_ = tx.Rollback()
	if targetErr != nil {
		t.Fatalf("correct_delivery target read: %v", targetErr)
	}
	if targetSeq != acceptSeq || int64(targetVersion) != acceptPayloadVersion || targetArtifact != assertion.Artifact {
		t.Fatalf("correct_delivery target = (%d, %d, %s), want the combined assertion", targetSeq, targetVersion, targetArtifact)
	}

	// The parked read reports the gate and its record_delivery resume.
	parked := parkedDeliveryRead(context.Background(), s.DatabaseForTesting(), c.definition.Definition, workID, "delivery", "running")
	if parked == nil || parked.StepID != "delivery" || parked.ResumeAction != "record_delivery" {
		t.Fatalf("parked delivery read = %+v", parked)
	}

	var completions int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM worker_attempt_dispositions WHERE work_id=? AND action_id='accept_worker_result' AND json_extract(payload,'$.delivery_state')='asserted'`, workID).Scan(&completions); err != nil {
		t.Fatal(err)
	}
	if completions != 1 {
		t.Fatalf("log carries %d asserted combined accepts, want 1", completions)
	}

	// CD-0198 D4: the combined accept advanced refine to the delivery gate.
	reviewGateRequireStep(t, s, workID, "delivery")
}

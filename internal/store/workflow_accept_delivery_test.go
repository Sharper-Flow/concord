package store

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// The CD-0198 D4 combined accept: on workflow.implementation v21+ and
// workflow.break_fix v19+, an accept_worker_result at the refinement step
// carries the delivery fields and runs the unchanged record_delivery
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
	version := verdictItemVersion(t, s, workID)
	tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := applyWorkflowActionRawTx(context.Background(), tx, newFoldScope(tx), BuiltinWorkflowRegistry(), WorkflowActionExecutionRequest{
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
		if workflowWorkerJobsActive(entry.Definition) {
			workerJobIntegrationGreenRun(t, s, workID, acceptDeliveryRunDigest(workID, attemptID))
		} else {
			refineProofSeedGreenRun(t, s, workID, acceptDeliveryRunDigest(workID, attemptID))
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

// acceptDeliveryFixtureFor seeds one work item pinned to the named family
// version at the step the case drives from: the refinement step, or the
// pre-refine work step when the case crosses the work pass first.
func acceptDeliveryFixtureFor(t *testing.T, workID, definitionRef string, definitionVersion int64, targetStep, startAction string) acceptDeliveryFixture {
	t.Helper()
	registered, ok := BuiltinWorkflowRegistry().Lookup(definitionRef, definitionVersion)
	if !ok {
		t.Fatalf("workflow definition %s@%d is not registered", definitionRef, definitionVersion)
	}
	return acceptDeliveryFixture{
		fixture:     seedWorkflowReturnRouteFixtureWithDefinition(t, workID, registered, targetStep, []string{"verification"}, []string{"verification", "review", "artifact"}),
		definition:  registered,
		verdictStep: startAction[len("start_"):],
		startAction: startAction,
	}
}

// acceptDeliverySeedVerifyRun seeds one verify lease whose outcome, exit
// code, and tracked-file change the caller chooses, so the refusal suite can
// stand up every disqualifier the refine proof names.
func acceptDeliverySeedVerifyRun(t *testing.T, s *Store, workID, digest, outcome string, exitCode int64, tracked bool, acquired time.Time) string {
	t.Helper()
	leaseID := digest + ":worktree-verify:" + workID
	command := []string{"go", "vet", "./..."}
	resultJSON, err := json.Marshal(WorktreeVerifyResult{WorkID: workID, ProjectID: "project-1", Branch: "work/" + workID, Path: "/tmp/worktrees/" + workID, LeaseID: leaseID, Command: command, ExitCode: int(exitCode), TrackedFilesChanged: tracked})
	if err != nil {
		t.Fatal(err)
	}
	commandJSON, err := json.Marshal(command)
	if err != nil {
		t.Fatal(err)
	}
	acquiredText := acquired.UTC().Format(time.RFC3339Nano)
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO worktree_verify_leases(lease_id,work_id,project_id,path,state,client_ref,agent_ref,session_ref,principal_ref,command_json,acquired_at,released_at,exit_code,outcome,result_json)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		leaseID, workID, "project-1", "/tmp/worktrees/"+workID, "released", "client/concord-1", "agent/owner", "session/"+workID, "principal/operator",
		string(commandJSON), acquiredText, acquired.Add(time.Second).UTC().Format(time.RFC3339Nano), exitCode, outcome, string(resultJSON)); err != nil {
		t.Fatalf("seed the verify lease: %v", err)
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO durable_operations
			(op_id,attempt_epoch,work_id,workflow_type_ref,workflow_type_version,step_id,step_kind,
			 accepted_inputs_digest,accepted_scope_snapshot,principal_ref,request_id,observed_at,contract_digest,
			 result_kind,result_payload,evidence_refs,changed_refs,completed_at)
			VALUES(?,1,?,'worktree.verify',1,'','external_effect','sha256:`+digest+`','{}','principal/operator','request/verify','`+acquiredText+`','','completed',?,?, '[]', ?)`,
		worktreeVerifyOperationRef(leaseID), workID, string(resultJSON), workflowJSON([]string{worktreeVerifyOperationRef(leaseID)}), acquired.Add(time.Second).UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("seed the verify authority: %v", err)
	}
	return worktreeVerifyOperationRef(leaseID)
}

// acceptDeliveryRefineStart returns the wall-clock the refine step's fenced
// start recorded, the bound every verify acquire time is judged against.
func acceptDeliveryRefineStart(t *testing.T, s *Store, workID string) time.Time {
	t.Helper()
	var occurred string
	if err := s.DatabaseForTesting().QueryRow(`SELECT occurred_at FROM domain_events WHERE subject_type='work_item' AND subject_id=? AND kind=? AND json_extract(payload,'$.step_id')='refine' AND json_extract(payload,'$.action_id')='start_refine' ORDER BY seq DESC LIMIT 1`, workID, WorkflowActionStarted).Scan(&occurred); err != nil {
		t.Fatalf("read the refine start: %v", err)
	}
	startedAt, err := time.Parse(time.RFC3339Nano, occurred)
	if err != nil {
		t.Fatalf("parse the refine start: %v", err)
	}
	return startedAt
}

// reviewGateDriveRejectionAt drives the work step, the refinement rejection,
// and the repaired implement attempt on a CD-0198 D4 fixture, leaving the
// repaired result standing unaccepted at refine.
func reviewGateDriveRejectionAt(t *testing.T, f acceptDeliveryFixture, workID string, at *int64) int64 {
	t.Helper()
	s := f.fixture.store
	ownerRef, err := WorkflowActorRef(f.fixture.owner)
	if err != nil {
		t.Fatal(err)
	}
	acceptor := reviewGateAcceptor(workID)
	implement := reviewGateLane(t, "implementation")
	review := reviewGateLane(t, "review")

	repairEpoch := reviewGateStartStep(t, s, workID, f.verdictStep, f.startAction, f.fixture.owner)
	repairAttempt := "attempt:" + workID + ":repair"
	reviewGateRunAttempt(t, s, workID, repairAttempt, f.verdictStep, repairEpoch, implement, ownerRef, *at)
	*at += 2
	if err := reviewGateAcceptResult(t, s, workID, repairAttempt, repairEpoch, acceptor); err != nil {
		t.Fatalf("accept repair: %v", err)
	}

	refineEpoch := reviewGateStartStep(t, s, workID, "refine", "start_refine", f.fixture.owner)
	reviewAttempt := "attempt:" + workID + ":review-1"
	reviewGateRunAttempt(t, s, workID, reviewAttempt, "refine", refineEpoch, review, ownerRef, *at)
	*at += 2
	reviewGateRejectResult(t, s, workID, reviewAttempt, refineEpoch, acceptor)
	repairedAttempt := "attempt:" + workID + ":repair-1"
	reviewGateRunAttempt(t, s, workID, repairedAttempt, "refine", refineEpoch, implement, ownerRef, *at)
	*at += 2
	return refineEpoch
}

func TestCombinedAcceptDeliversAtomically(t *testing.T) {
	for _, family := range []struct {
		ref         string
		version     int64
		verdictStep string
		nextStep    string
	}{
		{"workflow.implementation", 21, "execution", "acceptance"},
		{"workflow.break_fix", 19, "repair", "verify"},
	} {
		t.Run(family.ref, func(t *testing.T) {
			const workID = "combined-accept-atomic"
			f := acceptDeliveryFixtureFor(t, workID, family.ref, family.version, "refine", "start_refine")
			s := f.fixture.store
			acceptor := reviewGateAcceptor(workID)
			attempt := "attempt:" + workID + ":refine"

			// The step start, the completed attempt, and the green run stand
			// before the combined accept; the accept records everything else.
			reviewGateStartStep(t, s, workID, "refine", "start_refine", f.fixture.owner)
			epoch := latestStepStartEpoch(t, s, workID, "refine")
			ownerRef, err := WorkflowActorRef(f.fixture.owner)
			if err != nil {
				t.Fatal(err)
			}
			reviewGateRunAttempt(t, s, workID, attempt, "refine", epoch, reviewGateLane(t, "implementation"), ownerRef, 100)
			if err := acceptRefineResult(t, s, workID, attempt, epoch, acceptor); err != nil {
				t.Fatalf("combined accept at refine: %v", err)
			}

			// One operation advanced the step and recorded the assertion: the
			// accept completion carries the delivery fields, the step sits on
			// the gate, and no record_delivery call ever ran.
			reviewGateRequireStep(t, s, workID, "delivery")
			var acceptCompletions int
			if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.action_id')='accept_worker_result' AND json_extract(payload,'$.worker_attempt_id')=? AND json_extract(payload,'$.delivery_artifact')='artifact:accept-delivery-`+workID+`' AND json_extract(payload,'$.delivery_state')='asserted'`, workID, WorkflowActionCompleted, attempt).Scan(&acceptCompletions); err != nil {
				t.Fatal(err)
			}
			if acceptCompletions != 1 {
				t.Fatalf("combined accept recorded %d asserted completions, want 1", acceptCompletions)
			}
			var refineDeliveries int
			if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.action_id')='record_delivery' AND json_extract(payload,'$.step_id') IN ('refine','delivery')`, workID, WorkflowActionCompleted).Scan(&refineDeliveries); err != nil {
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

			// The gate stays record_delivery-only: the asserted accept parked
			// the item there, the gate's exit is record_delivery, and it
			// crosses to the verdict step.
			definition, err := BuiltinWorkflowDefinitionForRef(family.ref)
			if err != nil {
				t.Fatal(err)
			}
			gate := workflowStep(definition.Definition, "delivery")
			if !workflowStepIsDeliveryGate(gate) || gate.Actions[0] != "record_delivery" || containsString(gate.Actions, "accept_worker_result") {
				t.Fatalf("%s gate actions = %v, want a record_delivery-only gate", family.ref, gate.Actions)
			}
			delivery := json.RawMessage(`{"delivery_artifact":"artifact:gate-cross-` + workID + `","delivery_state":"asserted"}`)
			if err := runVerdictActionAs(t, s, workID, "record_delivery", delivery, 0, acceptor); err != nil {
				t.Fatalf("gate crossing after the combined accept: %v", err)
			}
			reviewGateRequireStep(t, s, workID, family.nextStep)
		})
	}
}

// TestDeliveryAdmissionRefusalsMatchAcrossEntryRoutes proves the combined
// accept refuses exactly what record_delivery refuses: a missing, stale,
// failed, dirty, or undeclared verification run, a missing step start, and
// unresolved post-rejection review debt — and every refusal leaves no
// acceptance, delivery, or advance.
func TestDeliveryAdmissionRefusalsMatchAcrossEntryRoutes(t *testing.T) {
	for _, family := range []struct {
		ref         string
		version     int64
		verdictStep string
		startAction string
	}{
		{"workflow.implementation", 21, "execution", "start_execution"},
		{"workflow.break_fix", 19, "repair", "start_repair"},
	} {
		t.Run(family.ref, func(t *testing.T) {
			cases := []struct {
				name       string
				fixtureAt  string
				seed       func(t *testing.T, f acceptDeliveryFixture, workID string)
				seedRun    bool
				wantKind   string
				wantDetail string
				tooling    *ProjectToolingManifest
			}{
				{name: "no-verification-run", fixtureAt: "refine", seed: func(t *testing.T, f acceptDeliveryFixture, workID string) {
					reviewGateStartStep(t, f.fixture.store, workID, "refine", "start_refine", f.fixture.owner)
				}, wantKind: string(KindMissingEvidence), wantDetail: "no verification evidence is bound in the current refine epoch"},
				{name: "stale-run", fixtureAt: "refine", seed: func(t *testing.T, f acceptDeliveryFixture, workID string) {
					s := f.fixture.store
					reviewGateStartStep(t, s, workID, "refine", "start_refine", f.fixture.owner)
					startedAt := acceptDeliveryRefineStart(t, s, workID)
					ref := acceptDeliverySeedVerifyRun(t, s, workID, strings.Repeat("2", 64), "completed", 0, false, startedAt.Add(-time.Minute))
					refineProofBindVerification(t, s, workID, ref, ref)
				}, wantKind: string(KindMissingEvidence), wantDetail: "acquired at or before the current refine start"},
				{name: "failed-run", fixtureAt: "refine", seed: func(t *testing.T, f acceptDeliveryFixture, workID string) {
					s := f.fixture.store
					reviewGateStartStep(t, s, workID, "refine", "start_refine", f.fixture.owner)
					startedAt := acceptDeliveryRefineStart(t, s, workID)
					ref := acceptDeliverySeedVerifyRun(t, s, workID, strings.Repeat("3", 64), "aborted", 1, false, startedAt.Add(time.Second))
					refineProofBindVerification(t, s, workID, ref, ref)
				}, wantKind: string(KindMissingEvidence), wantDetail: "the bound verify lease recorded outcome aborted"},
				{name: "dirty-run", fixtureAt: "refine", seed: func(t *testing.T, f acceptDeliveryFixture, workID string) {
					s := f.fixture.store
					reviewGateStartStep(t, s, workID, "refine", "start_refine", f.fixture.owner)
					startedAt := acceptDeliveryRefineStart(t, s, workID)
					ref := acceptDeliverySeedVerifyRun(t, s, workID, strings.Repeat("4", 64), "completed", 0, true, startedAt.Add(time.Second))
					refineProofBindVerification(t, s, workID, ref, ref)
				}, wantKind: string(KindMissingEvidence), wantDetail: "the bound verify run changed tracked files"},
				{name: "undeclared-run", fixtureAt: "refine", seed: func(t *testing.T, f acceptDeliveryFixture, workID string) {
					s := f.fixture.store
					reviewGateStartStep(t, s, workID, "refine", "start_refine", f.fixture.owner)
					startedAt := acceptDeliveryRefineStart(t, s, workID)
					ref := acceptDeliverySeedVerifyRun(t, s, workID, strings.Repeat("5", 64), "completed", 0, false, startedAt.Add(time.Second))
					refineProofBindVerification(t, s, workID, ref, ref)
				}, wantKind: string(KindMissingEvidence), wantDetail: "not a tool the Project declares",
					tooling: refineProofTooling(ProjectDeclaredTool{ID: "go-test-race", Invocation: "go test -race ./..."})},
				{name: "missing-step-start", fixtureAt: "refine", seed: func(t *testing.T, f acceptDeliveryFixture, workID string) {
					// No start_refine ran: the admission refuses before the
					// attempt fold ever sees the epoch.
				}, wantKind: string(KindInvalidOperation), wantDetail: "requires the delivery step's fenced start action in this attempt"},
				{name: "unresolved-review-debt", fixtureAt: family.verdictStep, seedRun: true, seed: func(t *testing.T, f acceptDeliveryFixture, workID string) {
					reviewGateDriveRejectionAt(t, f, workID, new(int64))
				}, wantKind: string(KindInvalidOperation), wantDetail: "fresh accepted review"},
			}
			for _, testCase := range cases {
				for _, route := range deliveryEntryRoutes() {
					t.Run(testCase.name+"/"+route.name, func(t *testing.T) {
						const workID = "accept-delivery-refusal"
						f := acceptDeliveryFixtureFor(t, workID, family.ref, family.version, testCase.fixtureAt, family.startAction)
						s := f.fixture.store
						acceptor := reviewGateAcceptor(workID)
						testCase.seed(t, f, workID)
						attemptID := "attempt:" + workID + ":r1"
						epoch := latestStepStartEpoch(t, s, workID, "refine")
						if testCase.name == "missing-step-start" {
							epoch = 1
						}
						if route.name == "accept_worker_result" && testCase.fixtureAt == "refine" {
							// The combined accept dispositions a completed
							// attempt at the step, so the equivalence case
							// stands one up before the exit.
							ownerRef, ownerErr := WorkflowActorRef(f.fixture.owner)
							if ownerErr != nil {
								t.Fatal(ownerErr)
							}
							reviewGateRunAttempt(t, s, workID, attemptID, "refine", epoch, reviewGateLane(t, "implementation"), ownerRef, 100)
						}
						err := route.exit(t, s, workID, attemptID, epoch, acceptor, testCase.tooling, testCase.seedRun)
						var failure *Failure
						if err == nil || !failureAs(err, &failure) || string(failure.Kind) != testCase.wantKind || !strings.Contains(failure.Detail, testCase.wantDetail) {
							t.Fatalf("%s refusal = %v, want kind %s naming %q", route.name, err, testCase.wantKind, testCase.wantDetail)
						}

						// The refusal is atomic: the step holds, no acceptance
						// recorded, and no delivery assertion exists.
						reviewGateRequireStep(t, s, workID, "refine")
						var accepted int
						if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.action_id')='accept_worker_result' AND json_extract(payload,'$.worker_attempt_id')=?`, workID, WorkflowActionCompleted, attemptID).Scan(&accepted); err != nil {
							t.Fatal(err)
						}
						if accepted != 0 {
							t.Fatalf("refused combined accept recorded %d acceptances", accepted)
						}
						if seq, seqErr := workflowLatestDeliveryAssertionSeq(context.Background(), s, workID); seqErr != nil {
							t.Fatal(seqErr)
						} else if seq != 0 {
							t.Fatalf("%s refusal left a delivery assertion at seq %d", route.name, seq)
						}
					})
				}
			}
		})
	}
}

// TestRefineExitExclusivityOnDeliveryAcceptVersions proves the CD-0198 D4
// boundaries: a plain accept at the admitting refine step refuses, delivery
// fields on an accept anywhere else or on a prior version refuse, the gate
// keeps record_delivery as its only delivery route, and prior pins keep the
// plain advancing accept.
func TestRefineExitExclusivityOnDeliveryAcceptVersions(t *testing.T) {
	for _, family := range []struct {
		ref         string
		version     int64
		prior       int64
		verdictStep string
		startAction string
	}{
		{"workflow.implementation", 21, 20, "execution", "start_execution"},
		{"workflow.break_fix", 19, 18, "repair", "start_repair"},
	} {
		t.Run(family.ref, func(t *testing.T) {
			// A plain accept at the admitting refine step refuses with the
			// delivery-admission remedy, even with the green run bound: the
			// refine exit requires an admitted delivery assertion.
			const workID = "refine-exit-exclusivity"
			f := acceptDeliveryFixtureFor(t, workID, family.ref, family.version, "refine", "start_refine")
			s := f.fixture.store
			acceptor := reviewGateAcceptor(workID)
			reviewGateStartStep(t, s, workID, "refine", "start_refine", f.fixture.owner)
			epoch := latestStepStartEpoch(t, s, workID, "refine")
			attempt := "attempt:" + workID + ":plain"
			ownerRef, err := WorkflowActorRef(f.fixture.owner)
			if err != nil {
				t.Fatal(err)
			}
			reviewGateRunAttempt(t, s, workID, attempt, "refine", epoch, reviewGateLane(t, "implementation"), ownerRef, 100)
			refineProofSeedGreenRun(t, s, workID, strings.Repeat("9", 64))
			err = runVerdictActionAs(t, s, workID, "accept_worker_result", json.RawMessage(`{"attempt_id":"`+attempt+`","attempt_epoch":`+fmt.Sprint(epoch)+`}`), 0, acceptor)
			var remedy *Failure
			if err == nil || !failureAs(err, &remedy) || remedy.Kind != KindInvalidOperation || !strings.Contains(remedy.Detail, "the refine step exits only through an admitted delivery assertion") {
				t.Fatalf("plain accept at refine = %v, want the delivery-admission remedy", err)
			}
			reviewGateRequireStep(t, s, workID, "refine")

			// Delivery fields on an accept at the work step refuse: the
			// combined route exists only at the refinement step. The plain
			// accept at the work step still advances.
			const workIDWork = "refine-exit-exclusivity-work"
			work := acceptDeliveryFixtureFor(t, workIDWork, family.ref, family.version, family.verdictStep, family.startAction)
			workEpoch := reviewGateStartStep(t, work.fixture.store, workIDWork, family.verdictStep, family.startAction, work.fixture.owner)
			workAttempt := "attempt:" + workIDWork + ":work"
			workOwnerRef, ownerErr := WorkflowActorRef(work.fixture.owner)
			if ownerErr != nil {
				t.Fatal(ownerErr)
			}
			reviewGateRunAttempt(t, work.fixture.store, workIDWork, workAttempt, family.verdictStep, workEpoch, reviewGateLane(t, "implementation"), workOwnerRef, 120)
			err = runVerdictActionAs(t, work.fixture.store, workIDWork, "accept_worker_result", json.RawMessage(`{"attempt_id":"`+workAttempt+`","attempt_epoch":`+fmt.Sprint(workEpoch)+`,"delivery_artifact":"artifact:misplaced","delivery_state":"asserted"}`), 0, reviewGateAcceptor(workIDWork))
			var misplaced *Failure
			if err == nil || !failureAs(err, &misplaced) || misplaced.Kind != KindInvalidOperation || !strings.Contains(misplaced.Detail, "carries delivery fields only at the delivery-admitting refinement step") {
				t.Fatalf("accept with delivery fields at %s = %v, want the misplaced-fields refusal", family.verdictStep, err)
			}
			if err := reviewGateAcceptResult(t, work.fixture.store, workIDWork, workAttempt, workEpoch, reviewGateAcceptor(workIDWork)); err != nil {
				t.Fatalf("plain accept at %s: %v", family.verdictStep, err)
			}

			// Delivery fields on a prior version refuse, and the prior
			// version keeps the plain advancing accept at refine (pin
			// stability).
			const priorID = "refine-exit-exclusivity-prior"
			prior := acceptDeliveryFixtureFor(t, priorID, family.ref, family.prior, "refine", "start_refine")
			priorStart := reviewGateStartStep(t, prior.fixture.store, priorID, "refine", "start_refine", prior.fixture.owner)
			priorAttempt := "attempt:" + priorID + ":r1"
			priorOwnerRef, priorErr := WorkflowActorRef(prior.fixture.owner)
			if priorErr != nil {
				t.Fatal(priorErr)
			}
			reviewGateRunAttempt(t, prior.fixture.store, priorID, priorAttempt, "refine", priorStart, reviewGateLane(t, "implementation"), priorOwnerRef, 100)
			err = runVerdictActionAs(t, prior.fixture.store, priorID, "accept_worker_result", json.RawMessage(`{"attempt_id":"`+priorAttempt+`","attempt_epoch":`+fmt.Sprint(priorStart)+`,"delivery_artifact":"artifact:prior","delivery_state":"asserted"}`), 0, reviewGateAcceptor(priorID))
			var priorFailure *Failure
			if err == nil || !failureAs(err, &priorFailure) || priorFailure.Kind != KindInvalidPayload || !strings.Contains(priorFailure.Detail, "delivery_artifact") || !strings.Contains(priorFailure.Detail, "not declared") {
				t.Fatalf("combined accept on %s v%d = %v, want the undeclared-field refusal", family.ref, family.prior, err)
			}
			if err := reviewGateAcceptResult(t, prior.fixture.store, priorID, priorAttempt, priorStart, reviewGateAcceptor(priorID)); err != nil {
				t.Fatalf("plain accept at refine on v%d: %v", family.prior, err)
			}
			reviewGateRequireStep(t, prior.fixture.store, priorID, "delivery")
		})
	}
}

// TestCombinedDeliveryAssertionReadersAndRebuild proves the delivery readers
// recognize the combined assertion and that a rebuild from the log lands on
// the same state the live fold produced.
func TestCombinedDeliveryAssertionReadersAndRebuild(t *testing.T) {
	const workID = "combined-assertion-readers"
	f := acceptDeliveryFixtureFor(t, workID, "workflow.implementation", 21, "refine", "start_refine")
	s := f.fixture.store
	acceptor := reviewGateAcceptor(workID)
	reviewGateStartStep(t, s, workID, "refine", "start_refine", f.fixture.owner)
	epoch := latestStepStartEpoch(t, s, workID, "refine")
	attempt := "attempt:" + workID + ":r1"
	ownerRef, err := WorkflowActorRef(f.fixture.owner)
	if err != nil {
		t.Fatal(err)
	}
	reviewGateRunAttempt(t, s, workID, attempt, "refine", epoch, reviewGateLane(t, "implementation"), ownerRef, 100)
	if err := acceptRefineResult(t, s, workID, attempt, epoch, acceptor); err != nil {
		t.Fatalf("combined accept: %v", err)
	}
	reviewGateRequireStep(t, s, workID, "delivery")

	var acceptSeq, acceptPayloadVersion int64
	var acceptEventID string
	if err := s.DatabaseForTesting().QueryRow(`SELECT seq,payload_version,event_id FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.action_id')='accept_worker_result' AND json_extract(payload,'$.worker_attempt_id')=?`, workID, WorkflowActionCompleted, attempt).Scan(&acceptSeq, &acceptPayloadVersion, &acceptEventID); err != nil {
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

	// The parked read still reports the gate and its record_delivery resume.
	parked := parkedDeliveryRead(context.Background(), s, f.definition.Definition, workID, "delivery", "running")
	if parked == nil || parked.StepID != "delivery" || parked.ResumeAction != "record_delivery" {
		t.Fatalf("parked delivery read = %+v", parked)
	}

	// A rebuild from the log lands on the same state.
	if err := RebuildFromLog(context.Background(), s); err != nil {
		t.Fatalf("rebuild from log: %v", err)
	}
	reviewGateRequireStep(t, s, workID, "delivery")
	rebuilt, err := workflowDeliveryAssertionRead(context.Background(), s.db, workID)
	if err != nil || rebuilt == nil {
		t.Fatalf("rebuilt assertion read = (%+v, %v)", rebuilt, err)
	}
	if rebuilt.EventID != assertion.EventID || rebuilt.Seq != assertion.Seq || rebuilt.Artifact != assertion.Artifact {
		t.Fatalf("rebuilt assertion = %+v, want %+v", rebuilt, assertion)
	}
	var rebuiltCompletions int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.action_id')='accept_worker_result' AND json_extract(payload,'$.delivery_state')='asserted'`, workID, WorkflowActionCompleted).Scan(&rebuiltCompletions); err != nil {
		t.Fatal(err)
	}
	if rebuiltCompletions != 1 {
		t.Fatalf("rebuilt log carries %d asserted combined accepts, want 1", rebuiltCompletions)
	}
}

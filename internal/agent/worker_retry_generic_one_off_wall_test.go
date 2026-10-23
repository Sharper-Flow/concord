package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/store"
)

// seedGenericOneOffVerifyWorkflow initializes an implementation instance for
// work-1, pins it at the refine checkpoint under an approved contract, and
// seeds the active worktree claim the fenced dispatch requires, so the
// review-lane dispatch loop can run there. The retained v23 pin predates the
// worker-job dispatch gate and keeps callable record_worker_failure; the archived
// generic_one_off authoring left the live catalog (CD-0218 D1).
func seedGenericOneOffVerifyWorkflow(t *testing.T, s *store.Store, grant Authority) string {
	t.Helper()
	return seedGenericOneOffWorkflowAtStep(t, s, grant, "refine")
}

// seedGenericOneOffWorkflowAtStep seeds the same implementation v23 fixture
// pinned at the named step.
func seedGenericOneOffWorkflowAtStep(t *testing.T, s *store.Store, grant Authority, step string) string {
	t.Helper()
	seedCurrentWorkflowDomainFixture(t, s)
	lookup, ok := store.BuiltinWorkflowRegistry().Lookup("workflow.implementation", 23)
	if !ok {
		t.Fatal("workflow.implementation v23 is not registered")
	}
	owner := store.WorkflowActor{PrincipalRef: grant.PrincipalRef, ClientRef: grant.ClientRef, AgentRef: grant.AgentRef, SessionRef: grant.SessionRef, ActorClass: store.ActorAgent}
	if err := s.Transact(context.Background(), func(tx *store.Transaction) error {
		return store.InitializeWorkflowTx(context.Background(), tx, store.WorkflowInitializationRequest{WorkID: "work-1", Definition: lookup, Actor: owner, Now: fixedTime()})
	}); err != nil {
		t.Fatal(err)
	}
	ownerRef, err := store.WorkflowActorRef(owner)
	if err != nil {
		t.Fatal(err)
	}
	path := t.TempDir()
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1);
		UPDATE workflow_instances SET current_step='`+step+`' WHERE work_id='work-1';
		INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class) VALUES('work-1',1,'approved retry objective','internal_sqlite','[]','[]','now',?,'[]','[]',0,'prototype_internal');
		INSERT INTO workflow_contract_predicates(work_id,contract_version,predicate_id,ordinal,outcome_kind,outcome_payload) VALUES('work-1',1,'predicate:primary',0,'check','{"kind":"check","check_ref":"check:workflow","immutable_subject_ref":"commit:verify-review","expected_result":"pass"}');
		DELETE FROM fold_guard`, ownerRef); err != nil {
		t.Fatalf("seed generic_one_off verify projections: %v", err)
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1);
		INSERT INTO worktree_entries(set_id,project_id,claim_op_id,branch,base_sha,path,repository_id,state,verified_at) VALUES(?,?,?,?,?,?,?,'active',?);
		DELETE FROM fold_guard`, store.WorktreeSetID("work-1"), "project-1", "claim:verify-wall", "verify-wall", strings.Repeat("a", 40), path, "repo:verify-wall", fixedTime().Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("seed verify-wall worktree: %v", err)
	}
	var claimed int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM worktree_entries WHERE set_id=? AND state='active'`, store.WorktreeSetID("work-1")).Scan(&claimed); err != nil || claimed != 1 {
		t.Fatalf("verify-wall worktree claim count=%d err=%v", claimed, err)
	}
	// The review gate joins the accepted implementation delivery on this
	// claim, so the claim carries its real candidate, the qualified verify
	// subject, and the accepted prerequisite before any journey dispatch.
	seedOracleVerifySubjectForWork(t, s, "work-1")
	seedGenericOneOffAcceptedImplementation(t, s, grant, path, step)
	return path
}

// runGenericOneOffStoreAction applies one store-level workflow action so the
// fixture can open dispatch windows, disposition results, and supersede the
// contract without minting approval challenges on the mutation boundary.
func runGenericOneOffStoreAction(t *testing.T, s *store.Store, action string, fields map[string]any, actor store.WorkflowActor, sessionWorktree, suffix string) {
	t.Helper()
	version := workVersion(t, s, "work-1")
	operationID := "generic-verify-" + action + "-" + suffix + "-" + strconv.FormatInt(version, 10)
	raw, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	// Live dispatch assembly requires the sealed preparation the
	// authenticated boundary prepares, so this raw route prepares one the
	// same way before the transaction opens.
	preparation, preparationErr := store.PrepareWorkerDispatchFreshness(context.Background(), s, store.PrepareWorkerDispatchRequest{WorkID: "work-1", SessionWorktree: sessionWorktree})
	if preparationErr != nil {
		t.Fatalf("prepare dispatch freshness for %s (%s): %v", action, suffix, preparationErr)
	}
	ctx := store.WithWorkerDispatchPreparation(context.Background(), preparation)
	if err := s.Transact(ctx, func(tx *store.Transaction) error {
		_, err := store.ApplyWorkflowActionTx(ctx, tx, store.BuiltinWorkflowRegistry(), store.WorkflowActionExecutionRequest{
			WorkID: "work-1", ExpectedVersion: version, ActionID: action, Payload: raw, Actor: actor, SessionWorktree: sessionWorktree,
			AcceptedInputsDigest: "sha256:" + strings.Repeat("f", 64) + operationID, IdempotencyIdentity: operationID, OperationID: operationID,
			PrincipalRef: actor.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: operationID, RequestID: "request:" + operationID,
			ContractDigest: ManifestDigest, Now: fixedTime(),
		})
		return err
	}); err != nil {
		t.Fatalf("store action %s (%s): %v", action, suffix, err)
	}
}

// seedGenericOneOffAcceptedImplementation produces and accepts one
// implementation delivery at the refinement gate through the released pin's
// own shapes: no worker-job binding, the completed report through the live
// terminal seam with the core delivery observation, and the gate acceptance.
// The acceptance lands before the failure window under test, and the
// instance parks back at the step the journey runs on.
func seedGenericOneOffAcceptedImplementation(t *testing.T, s *store.Store, grant Authority, worktree, journeyStep string) {
	t.Helper()
	owner := store.WorkflowActor{PrincipalRef: grant.PrincipalRef, ClientRef: grant.ClientRef, AgentRef: grant.AgentRef, SessionRef: grant.SessionRef, ActorClass: store.ActorAgent}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1);
		UPDATE workflow_instances SET current_step='refine' WHERE work_id='work-1';
		DELETE FROM fold_guard`); err != nil {
		t.Fatalf("park at the refinement gate for the accepted prerequisite: %v", err)
	}
	defer func() {
		if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1);
			UPDATE workflow_instances SET current_step='` + journeyStep + `' WHERE work_id='work-1';
			DELETE FROM fold_guard`); err != nil {
			t.Fatalf("park back at the journey step: %v", err)
		}
	}()
	attemptID := "attempt:work-1:accepted-impl"
	lane := deliveryRecoveryLane(t, "implementation")
	packet := bindPacketToRecordedState(t, s, map[string]any{"schema_version": store.WorkerPacketSchemaVersion, "attempt_id": attemptID, "lane_id": lane.ID, "lane_version": lane.Version, "lane_digest": lane.Digest, "work_id": "work-1", "step_id": "refine", "inputs": map[string]any{"task": "deliver the accepted implementation prerequisite", "constraints": []string{"preserve the approved contract"}}})
	runGenericOneOffStoreAction(t, s, "dispatch_worker", map[string]any{"attempt_id": attemptID, "worker_packet": packet}, owner, worktree, "accepted-impl")
	var packetDigest string
	if err := s.DatabaseForTesting().QueryRow(`SELECT json_extract(payload,'$.worker_packet_digest') FROM domain_events WHERE subject_id='work-1' AND kind=? AND json_extract(payload,'$.action_id')='dispatch_worker' AND json_extract(payload,'$.worker_attempt_id')=? ORDER BY seq DESC LIMIT 1`, store.WorkflowActionCompleted, attemptID).Scan(&packetDigest); err != nil {
		t.Fatal(err)
	}
	laneDispatch := store.Event{EventID: "accepted-impl-dispatch-" + attemptID, Kind: store.WorkerDispatched, SubjectType: store.SubjectWorkItem, SubjectID: "work-1", Actor: "worker:test", OccurredAt: fixedTime(), PayloadVersion: 2, Payload: retryJSON(store.WorkerDispatchedPayload{AttemptID: attemptID, LaneID: lane.ID, LaneVersion: lane.Version, LaneDigest: lane.Digest, CapabilityClass: lane.CapabilityClass, PacketDigest: packetDigest, ReadbackModel: "openai/gpt-5.6-luna", PacketSchemaVersion: store.WorkerPacketSchemaVersion, ReportSchemaVersion: store.WorkerReportSchemaVersion})}
	if err := s.Transact(context.Background(), func(tx *store.Transaction) error {
		enriched, err := store.PrepareLaneActorDispatch(context.Background(), tx, laneDispatch, grant.PrincipalRef, grant.ClientRef)
		if err != nil {
			return err
		}
		_, err = store.AppendLaneActorDispatchTx(context.Background(), tx, enriched)
		return err
	}); err != nil {
		t.Fatalf("seed the accepted prerequisite lane dispatch: %v", err)
	}
	agentWorkerCompletedThroughSeam(t, s, attemptID, "accepted-impl-completed-"+attemptID, store.WorkerCompletedPayload{AttemptID: attemptID, ReadbackModel: "openai/gpt-5.6-luna", ReportSchemaVersion: store.WorkerReportSchemaVersion})
	var epoch int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT json_extract(payload,'$.attempt_epoch') FROM domain_events WHERE subject_id='work-1' AND kind=? AND json_extract(payload,'$.action_id')='dispatch_worker' AND json_extract(payload,'$.step_id')='refine' ORDER BY seq DESC LIMIT 1`, store.WorkflowActionStarted).Scan(&epoch); err != nil {
		t.Fatal(err)
	}
	// The refinement gate's exit proof: a green worktree_verify run bound as
	// verification evidence in the current refine epoch.
	verifyRef := agentSeedIntegrationVerifyRun(t, s, "work-1", "accepted-impl-verify")
	runGenericOneOffStoreAction(t, s, "bind_evidence", map[string]any{"evidence_kind": "verification", "evidence_ref": verifyRef}, owner, worktree, "accepted-impl")
	runGenericOneOffStoreAction(t, s, "accept_worker_result", map[string]any{"attempt_id": attemptID, "attempt_epoch": epoch, "delivery_artifact": "artifact:accepted-impl", "delivery_state": "asserted"}, owner, worktree, "accepted-impl")
}

// seedGenericOneOffVerifyWall records three terminal review failures without
// a correction disposition and returns the last attempt identity and epoch.
func seedGenericOneOffVerifyWall(t *testing.T, s *store.Store, grant Authority, worktree string) (string, int64) {
	t.Helper()
	owner := store.WorkflowActor{PrincipalRef: grant.PrincipalRef, ClientRef: grant.ClientRef, AgentRef: grant.AgentRef, SessionRef: grant.SessionRef, ActorClass: store.ActorAgent}
	var failedID string
	var failedEpoch int64
	for cycle := 1; cycle <= 3; cycle++ {
		attemptID := fmt.Sprintf("attempt:work-1:verify-%d", cycle)
		runGenericOneOffStoreAction(t, s, "dispatch_worker", map[string]any{
			"attempt_id":    attemptID,
			"worker_packet": verifyWallPacket(t, s, attemptID, nil),
		}, owner, worktree, "dispatch-"+strconv.Itoa(cycle))
		var epoch int64
		if err := s.DatabaseForTesting().QueryRow(`SELECT json_extract(payload,'$.attempt_epoch') FROM domain_events WHERE subject_id='work-1' AND kind=? AND json_extract(payload,'$.action_id')='dispatch_worker' ORDER BY seq DESC LIMIT 1`, store.WorkflowActionStarted).Scan(&epoch); err != nil {
			t.Fatal(err)
		}
		applyVerifyWallDispatchAndFailure(t, s, grant, attemptID)
		if cycle == 3 {
			failedID, failedEpoch = attemptID, epoch
		}
	}
	return failedID, failedEpoch
}

// verifyWallReviewLane returns the builtin lane whose capability class the
// verify checkpoint admits.
func verifyWallReviewLane(t *testing.T) store.LaneDefinition {
	t.Helper()
	for _, lane := range store.BuiltinLaneDefinitions() {
		if lane.CapabilityClass == "review" {
			return lane
		}
	}
	t.Fatal("no builtin lane carries the review capability class")
	return store.LaneDefinition{}
}

// verifyWallPacket builds the lane packet the verify checkpoint's dispatch
// consumes, on the review lane the step admits, carrying the live correction
// record when one stands: the fold refuses a packet that does not consume it.
func verifyWallPacket(t *testing.T, s *store.Store, attemptID string, correction *store.WorkflowCorrectionContext) map[string]any {
	t.Helper()
	return verifyWallPacketForStep(t, s, attemptID, correction, "refine")
}

// verifyWallPacketForStep builds the same review-lane packet pinned to the
// named step, whose id the fold's step match reads back from the packet.
func verifyWallPacketForStep(t *testing.T, s *store.Store, attemptID string, correction *store.WorkflowCorrectionContext, step string) map[string]any {
	t.Helper()
	lane := verifyWallReviewLane(t)
	inputs := map[string]any{"task": "review the bounded change", "constraints": []string{"preserve the approved contract"}}
	if correction != nil {
		inputs["correction"] = correction
	}
	return bindPacketToRecordedState(t, s, map[string]any{"schema_version": store.WorkerPacketSchemaVersion, "attempt_id": attemptID, "lane_id": lane.ID, "lane_version": lane.Version, "lane_digest": lane.Digest, "work_id": "work-1", "step_id": step, "inputs": inputs})
}

// applyVerifyWallDispatchAndFailure records the lane dispatch on the review
// lane and the failed report for one verify-wall attempt.
func applyVerifyWallDispatchAndFailure(t *testing.T, s *store.Store, grant Authority, attemptID string) {
	t.Helper()
	lane := verifyWallReviewLane(t)
	dispatch := store.Event{EventID: "verify-wall-dispatch-" + attemptID, Kind: store.WorkerDispatched, SubjectType: store.SubjectWorkItem, SubjectID: "work-1", Actor: "worker:test", OccurredAt: fixedTime(), PayloadVersion: 2, Payload: retryJSON(store.WorkerDispatchedPayload{AttemptID: attemptID, LaneID: lane.ID, LaneVersion: lane.Version, LaneDigest: lane.Digest, CapabilityClass: lane.CapabilityClass, PacketDigest: "sha256:" + strings.Repeat("c", 64), ReadbackModel: "openai/gpt-5.6-luna", PacketSchemaVersion: store.WorkerPacketSchemaVersion, ReportSchemaVersion: store.WorkerReportSchemaVersion})}
	failure := store.Event{EventID: "verify-wall-failed-" + attemptID, Kind: store.WorkerFailed, SubjectType: store.SubjectWorkItem, SubjectID: "work-1", Actor: "worker:test", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: retryJSON(store.WorkerFailedPayload{AttemptID: attemptID, ReadbackModel: "openai/gpt-5.6-luna", FailureKind: store.WorkerFailureWorkerError, Detail: "verify review failed"})}
	if err := s.Transact(context.Background(), func(tx *store.Transaction) error {
		enriched, err := store.PrepareLaneActorDispatch(context.Background(), tx, dispatch, grant.PrincipalRef, grant.ClientRef)
		if err != nil {
			return err
		}
		if _, err := store.AppendLaneActorDispatchTx(context.Background(), tx, enriched); err != nil {
			return err
		}
		_, err = store.ApplyOperationTx(context.Background(), tx, store.Operation{Events: []store.Event{failure}})
		return err
	}); err != nil {
		t.Fatalf("seed verify-wall dispatch and failure for %s: %v", attemptID, err)
	}
}

// verifyWallLawHome is the one Git law home the pm1 fixture projected for the
// wall work item's Product, so the law boundary resolves a mandated law
// through it alone.
const (
	verifyWallLawHomeProject = "project-1"
	verifyWallLawHomeLocator = "domain-locator-product-1"
	verifyWallLawID          = "spec:one"
	verifyWallLawSuccessorID = "spec:two"
)

// seedVerifyWallLawPins writes the accepted synthetic law the law-pinned
// contract mandates into the fixture's Git law projection, outside the log
// like the store's own law fixtures, and publishes the law document the bound
// locator names inside the home checkout. The mandated law must predate the
// supersession whose successor derives its revision pins from it.
func seedVerifyWallLawPins(t *testing.T, s *store.Store) {
	t.Helper()
	seedVerifyWallLawDocument(t, s, ".concord/docs/spec.md")
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1);
		INSERT INTO law_subjects(home_project_id,home_locator_id,law_id,kind,status,path,title,content_hash,scanned_commit_oid) VALUES('` + verifyWallLawHomeProject + `','` + verifyWallLawHomeLocator + `','` + verifyWallLawID + `','spec','accepted','.concord/docs/spec.md','Synthetic verify-wall law','sha256:` + strings.Repeat("b", 64) + `','test');
		INSERT INTO law_domain_homes(home_project_id,home_locator_id,law_id,product_id,domain_id,law_content_hash,scanned_commit_oid,product_wide_rationale) VALUES('` + verifyWallLawHomeProject + `','` + verifyWallLawHomeLocator + `','` + verifyWallLawID + `','product-1','root','sha256:` + strings.Repeat("b", 64) + `','test','');
		DELETE FROM fold_guard`); err != nil {
		t.Fatalf("seed the verify-wall law projection: %v", err)
	}
}

// staleVerifyWallLawProjection is the honest law rescan: the Git home
// supersedes the mandated law under an accepted successor, and the projection
// carries the supersession beside it. The active contract's pinned revision
// reads stale on the next boundary scan — the ordinary stale-law recovery's
// own admission ground, with no fixture flag and no forced epoch standing in.
func staleVerifyWallLawProjection(t *testing.T, s *store.Store) {
	t.Helper()
	seedVerifyWallLawDocument(t, s, ".concord/docs/spec-two.md")
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1);
		UPDATE law_subjects SET status='superseded' WHERE home_project_id='` + verifyWallLawHomeProject + `' AND home_locator_id='` + verifyWallLawHomeLocator + `' AND law_id='` + verifyWallLawID + `';
		INSERT INTO law_subjects(home_project_id,home_locator_id,law_id,kind,status,path,title,content_hash,scanned_commit_oid) VALUES('` + verifyWallLawHomeProject + `','` + verifyWallLawHomeLocator + `','` + verifyWallLawSuccessorID + `','spec','accepted','.concord/docs/spec-two.md','Synthetic verify-wall successor law','sha256:` + strings.Repeat("c", 64) + `','test');
		INSERT INTO law_relations(home_project_id,home_locator_id,source_law_id,kind,target_law_id,scanned_commit_oid) VALUES('` + verifyWallLawHomeProject + `','` + verifyWallLawHomeLocator + `','` + verifyWallLawSuccessorID + `','supersedes','` + verifyWallLawID + `','test');
		INSERT INTO law_domain_homes(home_project_id,home_locator_id,law_id,product_id,domain_id,law_content_hash,scanned_commit_oid,product_wide_rationale) VALUES('` + verifyWallLawHomeProject + `','` + verifyWallLawHomeLocator + `','` + verifyWallLawSuccessorID + `','product-1','root','sha256:` + strings.Repeat("c", 64) + `','test','');
		DELETE FROM fold_guard`); err != nil {
		t.Fatalf("rescan the verify-wall law projection: %v", err)
	}
}

// seedVerifyWallLawDocument publishes one bound law document inside the pm1
// fixture's home checkout, so the law context's confined locator stat opens
// it the way a real checkout would.
func seedVerifyWallLawDocument(t *testing.T, s *store.Store, path string) {
	t.Helper()
	var home string
	if err := s.DatabaseForTesting().QueryRow(`SELECT locator_value FROM project_locators WHERE project_id=? AND locator_id=? AND kind='canonical_path'`, verifyWallLawHomeProject, verifyWallLawHomeLocator).Scan(&home); err != nil {
		t.Fatal(err)
	}
	full := filepath.Join(home, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte("# Synthetic verify-wall law\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// supersedeVerifyWallContract runs the operator-approved contract supersession
// at store level. The
// operator actor is the distinct identity the fold's operator guard demands,
// and the store-level run mints nothing on the agent challenge table.
func supersedeVerifyWallContract(t *testing.T, s *store.Store, owner store.WorkflowActor, version, next int64, reason string) {
	t.Helper()
	supersedeVerifyWallContractWithPredecessors(t, s, owner, version, next, nil, reason)
}

// supersedeVerifyWallContractWithPredecessors runs the same store-level
// supersession with an explicit predecessor list: a duplicated active
// projection (CD-0172 D6) names the retired rows the successor consumes,
// where the derived route's singular reader refuses on duplicates.
func supersedeVerifyWallContractWithPredecessors(t *testing.T, s *store.Store, owner store.WorkflowActor, version, next int64, predecessors []int64, reason string) {
	t.Helper()
	supersedeVerifyWallContractWithPredecessorsAndLawPins(t, s, owner, version, next, predecessors, nil, reason)
}

// supersedeVerifyWallContractWithLawPins is the canonical law-retaining case
// of the same store-level supersession: the successor's spec_mandate carries
// the named law pins, so a later Git-law rescan can supersede one and the
// ordinary stale-law recovery owns the renewal. Every mandated law must stand
// accepted in the fixture's Git law projection when the successor derives its
// revision pins.
func supersedeVerifyWallContractWithLawPins(t *testing.T, s *store.Store, owner store.WorkflowActor, version, next int64, mandate []string, reason string) {
	t.Helper()
	supersedeVerifyWallContractWithPredecessorsAndLawPins(t, s, owner, version, next, nil, mandate, reason)
}

func supersedeVerifyWallContractWithPredecessorsAndLawPins(t *testing.T, s *store.Store, owner store.WorkflowActor, version, next int64, predecessors []int64, mandate []string, reason string) {
	t.Helper()
	approvalRef := strings.Repeat("a", 64)
	operator := store.WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "approval:" + approvalRef, SessionRef: "session/verify-wall-supersede", ActorClass: store.ActorOperator}
	fields := map[string]any{
		"contract_version": next,
	}
	if predecessors != nil {
		fields["predecessor_contract_versions"] = predecessors
	}
	fields["premise"] = fmt.Sprintf("corrected verify objective %d: %s", next, reason)
	fields["outcome_predicates"] = []map[string]any{{
		"predicate_id": "predicate:primary", "ordinal": 0, "outcome_kind": "check",
		"outcome_payload": map[string]any{"kind": "check", "check_ref": "check:workflow", "immutable_subject_ref": "commit:verify-review", "expected_result": "pass"},
	}}
	// The retained implementation family is Product-changing: the
	// successor carries the complete architecture binding.
	fields["architecture_binding"] = workflowArchitectureBindingFixture()
	fields["required_evidence"] = []string{"artifact"}
	fields["route_conventions"] = []string{}
	fields["spec_mandate"] = mandate
	if mandate == nil {
		fields["spec_mandate"] = []string{}
	}
	fields["law_modifies"] = []string{}
	fields["rigor_class"] = "prototype_internal"
	fields["supersede_reason"] = reason
	fields["audit_evidence"] = []string{"evidence:verify-wall-supersede"}
	raw, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	operationID := "verify-wall-supersede-" + strconv.FormatInt(next, 10)
	if err := s.Transact(context.Background(), func(tx *store.Transaction) error {
		_, err := store.ApplyWorkflowActionTx(context.Background(), tx, store.BuiltinWorkflowRegistry(), store.WorkflowActionExecutionRequest{
			WorkID: "work-1", ExpectedVersion: version, ActionID: "supersede_contract", Payload: raw, Actor: owner,
			OperatorActor: &operator, OperatorApprovalRef: approvalRef,
			ApprovalOperationDigest: "sha256:" + strings.Repeat("c", 64), ApprovalScopeJSON: `{}`, ApprovalVersionsJSON: fmt.Sprintf(`{"work":%d}`, version), ApprovalConsequence: "recovery",
			AcceptedInputsDigest: "sha256:" + strings.Repeat("f", 64) + operationID, IdempotencyIdentity: operationID, OperationID: operationID,
			PrincipalRef: owner.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: operationID, RequestID: "request:" + operationID,
			ContractDigest: ManifestDigest, Now: fixedTime(),
		})
		return err
	}); err != nil {
		t.Fatalf("supersede contract v%d: %v", next, err)
	}
	var active int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT contract_version FROM workflow_contracts WHERE work_id='work-1' AND superseded_by IS NULL`).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if active != next {
		t.Fatalf("active contract after supersession = %d, want %d", active, next)
	}
}

// verifyWallChallenges counts the approval challenges the mutation boundary
// minted so far: the durable proof a journey minted none.
func verifyWallChallenges(t *testing.T, s *store.Store) int {
	t.Helper()
	return countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM agent_approval_challenges`)
}

// verifyWallDispatchInput builds one dispatch_worker workflow action input
// for the mutation boundary at the verify checkpoint, bound to the live work
// version, the recorded lane packet, and the standing correction record.
func verifyWallDispatchInput(t *testing.T, s *store.Store, attemptID, key string, correction *store.WorkflowCorrectionContext) (map[string]any, []byte) {
	t.Helper()
	return verifyWallDispatchInputForStep(t, s, attemptID, key, correction, "refine")
}

// verifyWallDispatchInputForStep builds the same boundary input with the
// packet pinned to the named step.
func verifyWallDispatchInputForStep(t *testing.T, s *store.Store, attemptID, key string, correction *store.WorkflowCorrectionContext, step string) (map[string]any, []byte) {
	t.Helper()
	input := map[string]any{
		"work_id": "work-1", "expected_version": workVersion(t, s, "work-1"), "action_id": "dispatch_worker",
		"idempotency_key": key, "fields": map[string]any{"attempt_id": attemptID, "worker_packet": verifyWallPacketForStep(t, s, attemptID, correction, step)},
	}
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	return input, raw
}

// verifyWallDispatchCompletions counts the dispatch completions the fold
// recorded for one attempt.
func verifyWallDispatchCompletions(t *testing.T, s *store.Store, attemptID string) int {
	t.Helper()
	return countRows(t, s.DatabaseForTesting(), fmt.Sprintf(`SELECT count(*) FROM domain_events WHERE subject_id='work-1' AND kind='%s' AND json_extract(payload,'$.action_id')='dispatch_worker' AND json_extract(payload,'$.worker_attempt_id')='%s'`, store.WorkflowActionCompleted, attemptID))
}

func assertDispatchHasNoRetryBudget(t *testing.T, s *store.Store, attemptID string) {
	t.Helper()
	var raw []byte
	if err := s.DatabaseForTesting().QueryRow(`SELECT payload FROM domain_events WHERE subject_id='work-1' AND kind=? AND json_extract(payload,'$.action_id')='dispatch_worker' AND json_extract(payload,'$.worker_attempt_id')=?`, store.WorkflowActionCompleted, attemptID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	if _, ok := payload["retry_convergence"]; ok {
		t.Fatalf("dispatch %s emitted retry budget authority: %s", attemptID, raw)
	}
}

// verifyWallPinCorrection reads the standing correction record from the work
// pin, failing when none stands.
func verifyWallPinCorrection(t *testing.T, s *store.Store) *store.WorkflowCorrectionContext {
	t.Helper()
	pin, err := store.ReadWorkPin(context.Background(), s, "work-1")
	if err != nil {
		t.Fatal(err)
	}
	if pin.Correction == nil {
		t.Fatal("no correction record stands on the work pin")
	}
	return pin.Correction
}

// mustScopeVersion reads the fixture's project scope version.
func mustScopeVersion(t *testing.T, s *store.Store) string {
	t.Helper()
	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	return scopeVersion
}

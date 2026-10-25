package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// seedComparisonObservation records the investigation artifact the operator
// question gate requires: a current Domain ref and a different work item. When
// the fixture Product holds no Domain registry yet, one is folded in first.
func seedComparisonObservation(t *testing.T, s *Store, workID string) {
	t.Helper()
	var domainRef string
	if err := s.DatabaseForTesting().QueryRow(`SELECT d.domain_id FROM domains d JOIN product_projects pp ON pp.product_id=d.product_id JOIN work_projects wp ON wp.project_id=pp.project_id WHERE wp.work_id=? AND d.status='current' LIMIT 1`, workID).Scan(&domainRef); err != nil {
		seedIssue31DomainRegistry(t, s)
		domainRef = "root"
	}
	tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := enterFold(context.Background(), tx); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO work_items(id,kind,title,lifecycle,priority,version,created_at,updated_at) VALUES(?,?,?,?,?,1,?,?)`, workID+"-compared", "task", "Investigation comparison", "needed", 0, "2026-09-09T00:00:00Z", "2026-09-09T00:00:00Z"); err != nil {
		_ = leaveFold(context.Background(), tx)
		tx.Rollback()
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO work_projects(work_id,project_id,role) VALUES(?,?,'secondary')`, workID+"-compared", "project"); err != nil {
		_ = leaveFold(context.Background(), tx)
		tx.Rollback()
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO work_observations(observation_id,work_id,statement,refs,tags,recorded_at) VALUES(?,?,?,?,?,?)`, "obs:"+workID[len(workID)-12:]+"cmp0", workID, "investigation before question", `["`+domainRef+`","`+workID+`-compared"]`, `[]`, "2026-09-09T00:00:00Z"); err != nil {
		_ = leaveFold(context.Background(), tx)
		tx.Rollback()
		t.Fatal(err)
	}
	if err := leaveFold(context.Background(), tx); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}
func seedItemAtAcceptance(t *testing.T, workID string) (*Store, WorkflowActor, WorkflowActor) {
	t.Helper()
	return seedItemAtAcceptanceRequiring(t, workID, false, []string{"verification"})
}

// seedItemAtAcceptanceRequiring seeds the same item with a chosen
// required_evidence set, so a contract can demand a kind whose consumption
// gate resolves against a captured record.
func seedItemAtAcceptanceRequiring(t *testing.T, workID string, prebind bool, requiredEvidence []string) (*Store, WorkflowActor, WorkflowActor) {
	t.Helper()
	ctx := context.Background()
	s := openTemp(t)
	seedWork(t, s, workID)
	seedWorkflowLaw(t, s)

	owner := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/owner", SessionRef: "session/" + workID, ActorClass: ActorAgent}
	ownerRef, err := WorkflowActorRef(owner)
	if err != nil {
		t.Fatal(err)
	}
	lane := BuiltinLaneDefinitions()[0]
	setup := []Event{
		workflowEvent("owner-actor-"+workID, WorkflowActorRecorded, workID, map[string]any{"work_id": workID, "expected_version": 2, "resulting_version": 3, "actor_ref": ownerRef, "principal_ref": owner.PrincipalRef, "client_ref": owner.ClientRef, "agent_ref": owner.AgentRef, "session_ref": owner.SessionRef, "actor_class": "agent"}),
		workflowEvent("definition-"+workID, WorkflowDefinitionSelected, workID, map[string]any{"work_id": workID, "expected_version": 3, "resulting_version": 4, "ref": workflowWorkerFixtureRef, "version": 2, "digest": workflowFixtureDefinition(t, 2).Digest, "work_kind": workflowFixtureWorkKind}),
		workflowActionCompletedFixture("proposal-"+workID, workID, ownerRef, 4, "proposal", "record_proposal"),
		workflowActionCompletedFixture("discovery-"+workID, workID, ownerRef, 5, "discovery", "record_discovery"),
		workflowActionCompletedFixture("design-"+workID, workID, ownerRef, 6, "design", "record_design"),
		workflowEventWithActor("contract-"+workID, WorkflowContractApproved, workID, ownerRef, map[string]any{"work_id": workID, "expected_version": 7, "resulting_version": 8, "contract_version": 1, "premise": "deliver the checked change", "outcome_kind": "check", "outcome_payload": map[string]any{"kind": "check", "check_ref": "check:workflow", "immutable_subject_ref": "commit:" + workID, "expected_result": "pass"}, "required_evidence": requiredEvidence, "route_conventions": []string{}, "spec_mandate": []string{}, "rigor_class": "prototype_internal", "consequence_class": "internal_sqlite"}),
		workflowEventWithActor("start-"+workID, WorkflowActionStarted, workID, ownerRef, map[string]any{"work_id": workID, "expected_version": 8, "resulting_version": 9, "step_id": "execution", "action_id": "start_execution", "attempt_epoch": 1, "accepted_inputs_digest": "sha256:" + strings.Repeat("a", 64), "idempotency_identity": "start:" + workID, "actor_ref": ownerRef, "execution_model": preferredModelForLane(lane)}),
	}
	reviewer := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/reviewer", SessionRef: "session/" + workID + "-reviewer", ActorClass: ActorAgent}
	reviewerRef, err := WorkflowActorRef(reviewer)
	if err != nil {
		t.Fatal(err)
	}
	reviewActor := workflowEvent("reviewer-actor-"+workID, WorkflowActorRecorded, workID, map[string]any{"work_id": workID, "expected_version": 9, "resulting_version": 10, "actor_ref": reviewerRef, "principal_ref": reviewer.PrincipalRef, "client_ref": reviewer.ClientRef, "agent_ref": reviewer.AgentRef, "session_ref": reviewer.SessionRef, "actor_class": "agent"})
	setup = append(setup, reviewActor)
	if err := applyWorkflowTestOperation(ctx, s, Operation{Events: setup, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): 2}}); err != nil {
		t.Fatal(err)
	}
	attemptID := "attempt:" + workID
	recordWorkerFixtureDispatchAuthorization(t, s, workID, attemptID, "execution", ownerRef)
	dispatch := Event{EventID: "dispatch-" + workID, Kind: WorkerDispatched, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: ownerRef, OccurredAt: time.Unix(2, 0).UTC(), PayloadVersion: CurrentEventPayloadVersion(WorkerDispatched), Payload: mustJSONValue(WorkerDispatchedPayload{AttemptID: attemptID, LaneID: lane.ID, LaneDigest: lane.Digest, CapabilityClass: lane.CapabilityClass, ReadbackModel: preferredModelForLane(lane), PacketSchemaVersion: WorkerPacketSchemaVersion, ReportSchemaVersion: WorkerReportSchemaVersion, PacketDigest: "sha256:" + strings.Repeat("e", 64)})}
	var preparedEvents []Event
	if err := s.Transact(ctx, func(tx *Transaction) error {
		var err error
		preparedEvents, err = PrepareLaneActorDispatch(ctx, tx, dispatch, owner.PrincipalRef, owner.ClientRef)
		if err != nil {
			return err
		}
		_, err = AppendLaneActorDispatchTx(ctx, tx, preparedEvents)
		return err
	}); err != nil {
		t.Fatalf("lane actor dispatch: %v", err)
	}
	attemptEpoch := latestStepStartEpoch(t, s, workID, "execution")
	completed := Event{EventID: "completed-" + workID, Kind: WorkerCompleted, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: ownerRef, OccurredAt: time.Unix(3, 0).UTC(), PayloadVersion: CurrentEventPayloadVersion(WorkerCompleted), Payload: mustJSONValue(WorkerCompletedPayload{AttemptID: attemptID, ReadbackModel: preferredModelForLane(lane), ReportSchemaVersion: WorkerReportSchemaVersion, WorkerDirectory: workerFixtureDirectory(t, s, workID), EvidenceOrigin: WorkerEvidenceReported, Evidence: laneCoveringEvidence(lane)})}
	terminalCtx := prepareWorkerTerminalDelivery(t, s, workID, attemptID, completed.EventID)
	workerTerminalEventThroughSeamCtx(t, terminalCtx, s, completed)
	if prebind {
		if err := runVerdictAction(t, s, workID, "bind_evidence", json.RawMessage(`{"evidence_kind":"verification","immutable_subject_ref":"evidence:seeded-verification"}`)); err != nil {
			t.Fatalf("bind verification evidence: %v", err)
		}
		if err := runVerdictAction(t, s, workID, "bind_evidence", json.RawMessage(`{"evidence_kind":"review","immutable_subject_ref":"evidence:seeded-review"}`)); err != nil {
			t.Fatalf("bind review evidence: %v", err)
		}
	}
	if err := runVerdictAction(t, s, workID, "accept_worker_result", json.RawMessage(mustJSON(map[string]any{"attempt_id": attemptID, "attempt_epoch": attemptEpoch}))); err != nil {
		t.Fatalf("accept worker result: %v", err)
	}
	return s, owner, reviewer
}

// verdictReviewer reads the non-executing reviewer actor from the test's
// isolated store, so verdict actions can be run by an agent that executed nothing.
func verdictReviewer(t *testing.T, s *Store, workID string) WorkflowActor {
	t.Helper()
	var reviewer WorkflowActor
	if err := s.DatabaseForTesting().QueryRow(`SELECT principal_ref,client_ref,agent_ref,session_ref,actor_class FROM workflow_actors WHERE agent_ref=? AND session_ref=?`, "agent/reviewer", "session/"+workID+"-reviewer").Scan(&reviewer.PrincipalRef, &reviewer.ClientRef, &reviewer.AgentRef, &reviewer.SessionRef, &reviewer.ActorClass); err != nil {
		t.Fatalf("reviewer for %s: %v", workID, err)
	}
	return reviewer
}

// actionEvidenceRefs supplies the immutable subject refs that the
// evidence-authority fold requires for evidence-producing actions.
func actionEvidenceRefs(action string, payload json.RawMessage) []string {
	if action != "bind_evidence" && action != "record_report" {
		return nil
	}
	var fields struct {
		ImmutableSubjectRef string `json:"immutable_subject_ref"`
	}
	if err := json.Unmarshal(payload, &fields); err != nil || fields.ImmutableSubjectRef == "" {
		return nil
	}
	return []string{fields.ImmutableSubjectRef}
}

func runVerdictAction(t *testing.T, s *Store, workID, action string, payload json.RawMessage) error {
	return runVerdictActionAs(t, s, workID, action, payload, 0, WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/owner", SessionRef: "session/" + workID, ActorClass: ActorAgent})
}

// runVerdictActionAs runs one action under the supplied authenticated actor.
func runVerdictActionAs(t *testing.T, s *Store, workID, action string, payload json.RawMessage, version int64, owner WorkflowActor) error {
	t.Helper()
	ctx, err := prepareTerminalTestContext(context.Background(), s, workID, action)
	if err != nil {
		return err
	}
	if version == 0 {
		version = readWorkVersion(t, s, workID)
	}
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := applyWorkflowActionRawTx(ctx, tx, newFoldScope(tx), BuiltinWorkflowRegistry(), WorkflowActionExecutionRequest{
		EvidenceRefs: actionEvidenceRefs(action, payload), WorkID: workID, ExpectedVersion: version, ActionID: action, Payload: payload, Actor: owner,
		AcceptedInputsDigest: "sha256:" + strings.Repeat("b", 64) + fmt.Sprint(version), IdempotencyIdentity: action + "-" + workID + "-" + fmt.Sprint(version), OperationID: action + "-" + workID + "-" + fmt.Sprint(version),
		PrincipalRef: owner.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: action + "-" + workID + "-" + fmt.Sprint(version), RequestID: "request:" + action + "-" + workID + "-" + fmt.Sprint(version), ContractDigest: testManifestDigest, Now: time.Unix(9, 0).UTC(),
	}); err != nil {
		return err
	}
	return tx.Commit()
}

// seedVerifiedNativeRunCapture records one native-run report on the work
// through the real report and verification events, so the capture and its
// verified state rebuild from the event log, and returns the observation
// identity the consumption gate resolves.
func seedVerifiedNativeRunCapture(t *testing.T, s *Store, workID string) string {
	t.Helper()
	ctx := context.Background()
	actor := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/owner", SessionRef: "session/" + workID, ActorClass: ActorAgent}
	var version int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM work_items WHERE id=?`, workID).Scan(&version); err != nil {
		t.Fatal(err)
	}
	report, err := buildNativeRunEvent("native-run-"+workID, workID, actor, time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC), version, "start", "run-"+workID, "go test ./internal/store/", "started", "https://evidence.invalid/runs/"+workID, "sha256:"+strings.Repeat("c", 64), "2026-09-14T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := applyWorkflowOperationTx(ctx, tx, Operation{Events: []Event{report}}, newFoldScope(tx)); err != nil {
		t.Fatalf("record native-run report: %v", err)
	}
	var observationID string
	if err := tx.QueryRow(`SELECT observation_id FROM workflow_native_runs WHERE work_id=? AND run_id=? AND phase='start'`, workID, "run-"+workID).Scan(&observationID); err != nil {
		t.Fatalf("read the reported run's observation identity: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := s.Transact(ctx, func(tx *Transaction) error {
		return AppendExternalObservationVerificationTx(ctx, tx, workID, "principal/operator", time.Date(2026, 9, 14, 0, 1, 0, 0, time.UTC), ExternalObservationVerification{
			ObservationID: observationID, VerificationMethod: VerifyTrustedClientReport,
			VerifiedAt: "2026-09-14T00:01:00Z", VerifyingAuthorityRef: "client/concord-1", Result: VerificationMatched,
		})
	}); err != nil {
		t.Fatalf("verify the native-run capture: %v", err)
	}
	return observationID
}

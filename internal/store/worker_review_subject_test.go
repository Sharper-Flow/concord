package store

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"
)

// The review subject: a review-lane dispatch reviews Main's current
// integrated candidate, the qualified subject that equals the live clean HEAD
// of the claimed worktree. It reads no implementation delivery history, so a
// baseline Review needs no implementation result (CD-0242 D5), and a contract
// version change alone does not move it (CD-0242 D7).

// reviewSubjectClaim claims the work's worktree as one real git repository
// and qualifies its HEAD through the worktree-verify owner, so the shared
// work-context subject names a commit the claimed worktree actually holds.
func reviewSubjectClaim(t *testing.T, s *Store, workID string) (string, string) {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	var projectVersion int64
	if err := s.db.QueryRow(`SELECT version FROM projects WHERE id='project'`).Scan(&projectVersion); err != nil {
		t.Fatal(err)
	}
	if err := s.AddProjectLocator(ctx, "project", ProjectLocator{ID: "review-repo-" + workID, Kind: LocatorCanonicalPath, Value: root}, projectVersion); err != nil {
		t.Fatal(err)
	}
	git := newFakeWorktreeGit(root)
	claim := baseClaim(git)
	claim.WorkID, claim.ProjectID, claim.ExpectedVersion = workID, "project", readWorkVersion(t, s, workID)
	claim.OpID, claim.RequestID = "review-claim-"+workID, "review-claim-"+workID
	claimed, err := s.ClaimWorktree(ctx, claim)
	if err != nil {
		t.Fatal(err)
	}
	dir := claimed.Entry.Path
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...) //nolint:gosec // test-only git argv, fixed binary
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("seed claim repo %v: %v: %s", args, err, stderr.String())
		}
		return strings.TrimSpace(string(out))
	}
	run("init", "-b", "main")
	run("config", "user.email", "concord@example.invalid")
	run("config", "user.name", "Concord Review Subject Test")
	if err := os.WriteFile(dir+"/seed.txt", []byte("review subject seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "-A")
	run("commit", "--allow-empty", "-m", "review subject seed")
	head := run("rev-parse", "--verify", "HEAD^{commit}")
	git.worktrees[dir] = claimed.Entry.Branch
	git.worktreeRepos[dir] = dir
	git.branches[claimed.Entry.Branch] = head
	req := verifyRequest(git, "verify-"+workID, []string{"true"}, func(context.Context, string, []string, int) (int, []byte, bool, error) { return 0, nil, false, nil })
	req.WorkID, req.ProjectID = workID, "project"
	if _, err := s.VerifyWorktree(ctx, req); err != nil {
		t.Fatal(err)
	}
	subject, err := readCurrentOracleSubject(ctx, s.db, workID)
	if err != nil || subject != head {
		t.Fatalf("qualified subject = %q err = %v, want the claimed worktree HEAD %q", subject, err, head)
	}
	return dir, head
}

// reviewSubjectRecordOperator records the operator actor tuple the folded
// accept action cites, exactly as a recorded actor reference must exist
// before any folded action names it.
func reviewSubjectRecordOperator(t *testing.T, s *Store, workID string) {
	t.Helper()
	operator := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/operator", SessionRef: "session/" + workID + "-operator", ActorClass: ActorOperator}
	operatorRef, err := WorkflowActorRef(operator)
	if err != nil {
		t.Fatal(err)
	}
	var recorded int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM workflow_actors WHERE actor_ref=?`, operatorRef).Scan(&recorded); err != nil {
		t.Fatal(err)
	}
	if recorded != 0 {
		return
	}
	version := readWorkVersion(t, s, workID)
	payload := map[string]any{
		"work_id": workID, "expected_version": version, "resulting_version": version + 1,
		"actor_ref": operatorRef, "principal_ref": operator.PrincipalRef, "client_ref": operator.ClientRef,
		"agent_ref": operator.AgentRef, "session_ref": operator.SessionRef, "actor_class": string(operator.ActorClass),
	}
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{workflowEventWithActor(workID+":review-operator", WorkflowActorRecorded, workID, operatorRef, payload)}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}}); err != nil {
		t.Fatalf("record operator actor: %v", err)
	}
}

// reviewSubjectAccept records the accept action that binds one completed
// implementation attempt, exactly as the accept completion the fold records
// carries the binding the review join reads. An accept completion
// legitimately advances the delivery-gate step, so the recorded-history
// route lands the event directly: the join reads the event, not the step.
func reviewSubjectAccept(t *testing.T, s *Store, workID, attemptID string) {
	t.Helper()
	operator := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/operator", SessionRef: "session/" + workID + "-operator", ActorClass: ActorOperator}
	operatorRef, err := WorkflowActorRef(operator)
	if err != nil {
		t.Fatal(err)
	}
	version := readWorkVersion(t, s, workID)
	payload := map[string]any{
		"work_id": workID, "expected_version": version, "resulting_version": version + 1,
		"step_id": "refine", "action_id": "accept_worker_result", "attempt_epoch": 1,
		"result_evidence_refs": []string{}, "changed_refs": []string{workID}, "actor_ref": operatorRef,
		"worker_attempt_id": attemptID,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := enterFold(context.Background(), tx); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(context.Background(), `INSERT INTO domain_events(event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload) VALUES(?, 'workflow.action_completed', 'work_item', ?, ?, ?, 2, ?)`,
		workID+":review-accept:"+attemptID, workID, operatorRef, time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC).Format(time.RFC3339Nano), string(raw)); err != nil {
		t.Fatalf("record acceptance of %s: %v", attemptID, err)
	}
	if err := leaveFold(context.Background(), tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

// reviewSubjectSupersedeContract supersedes the active contract through the
// folded supersession event, so the work's active contract moves past the
// accepted delivery's dispatch.
func reviewSubjectSupersedeContract(t *testing.T, s *Store, workID string) {
	t.Helper()
	reviewSubjectSupersedeContractVersions(t, s, workID, 1, 2, "the pinned contract moved past the accepted delivery")
}

// reviewSubjectSupersedeContractVersions records one contract supersession
// between the named versions with the fixture's successor shape; sequences
// that force repeated basis supersessions supersede through it directly.
func reviewSubjectSupersedeContractVersions(t *testing.T, s *Store, workID string, from, to int64, reason string) {
	t.Helper()
	version := readWorkVersion(t, s, workID)
	ownerRef, err := WorkflowActorRef(WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/owner", SessionRef: "session/" + workID, ActorClass: ActorAgent})
	if err != nil {
		t.Fatal(err)
	}
	payload := map[string]any{
		"work_id": workID, "expected_version": version, "resulting_version": version + 1,
		"previous_contract_version": from, "new_contract_version": to,
		"supersede_reason": reason,
		"audit_evidence":   []string{"evidence:review-subject-supersede"},
		"successor_contract": map[string]any{
			"contract_version": to, "premise": "deliver the checked change under a later contract", "outcome_kind": "check",
			"outcome_predicates": []map[string]any{{"predicate_id": "predicate:review-subject", "ordinal": 0, "outcome_kind": "check", "outcome_payload": map[string]any{"kind": "check", "check_ref": "check:review-subject", "immutable_subject_ref": "commit:" + workID, "expected_result": "pass"}}},
			"required_evidence":  []string{"verification"}, "route_conventions": []string{}, "spec_mandate": []string{}, "law_modifies": []string{},
			"law_revisions": []WorkflowLawRevision{}, "law_boundary_version": 1, "rigor_class": "prototype_internal", "consequence_class": "internal_sqlite",
			"architecture_binding": WorkflowArchitectureBinding{
				DomainRegistryContentHash: "sha256:" + strings.Repeat("b", 64), HomeDomainID: workContextTestRoot, AffectedDomainIDs: []string{workContextTestRoot, workContextTestChild},
				DomainModifies: []string{}, DomainRelationModifies: []WorkflowDomainRelationModification{}, LawAdditions: []WorkflowLawAddition{}, VerificationObligations: []WorkflowVerificationObligation{},
			},
		},
	}
	event := workflowEventWithActor(fmt.Sprintf("review-supersede-%d-%s", to, workID), WorkflowContractSuperseded, workID, ownerRef, payload)
	event.PayloadVersion = 1
	event = withPinnedDecisionFact(t, pinnedWorkflowDefinitionForTest(t, s, workID), event)
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}}); err != nil {
		t.Fatalf("supersede contract v%d: %v", from, err)
	}
}

// reviewSubjectDispatch runs the live review-lane dispatch at the work's
// current step with the given session worktree and returns the refusal. The
// job-capable repair step dispatches only a selected ready revision, so the
// caller records one fresh revision per review dispatch.
func reviewSubjectDispatch(t *testing.T, s *Store, f workContextFixture, attemptID, sessionWorktree string) error {
	t.Helper()
	const stepID = "repair"
	laneVersion, laneDigest := registeredLaneIdentity(t, "review")
	job := reviewSubjectReadyRevision(t, f)
	packet := joinPacketFor(t, s, f.workID, stepID, attemptID, "review", laneVersion, laneDigest)
	packet["inputs"].(map[string]any)["worker_job"] = recordedPacketJobForTest(t, s, f.workID, job, "review")
	packet["inputs"].(map[string]any)["binding"].(map[string]any)["work_version"] = readWorkVersion(t, s, f.workID)
	packetBytes, err := json.Marshal(packet)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(map[string]any{"attempt_id": attemptID, "worker_packet": json.RawMessage(packetBytes)})
	if err != nil {
		t.Fatal(err)
	}
	ctx, err := reviewSubjectPrepareDispatch(s, f.workID, sessionWorktree)
	if err != nil {
		return err
	}
	_, err = invokeWorkflowActionForCD0059(ctx, t, s, WorkflowActionExecutionRequest{
		WorkID: f.workID, ExpectedVersion: readWorkVersion(t, s, f.workID), ActionID: "dispatch_worker", Payload: payload,
		SessionWorktree:      sessionWorktree,
		Actor:                f.owner,
		AcceptedInputsDigest: cd0059TestDigest(t, attemptID+"-inputs"), IdempotencyIdentity: attemptID + "-op",
		OperationID: "op-" + attemptID, PrincipalRef: f.owner.PrincipalRef, Tool: "concord_work_transition",
		IdempotencyKey: attemptID + "-key", RequestID: "req-" + attemptID, AcceptedScope: `{}`, ContractDigest: testManifestDigest,
	})
	return err
}

// reviewSubjectReadyRevision records one fresh ready revision of the work's
// job, so the next dispatch binds a ready revision as the job lifecycle
// requires after an earlier revision was satisfied.
func reviewSubjectReadyRevision(t *testing.T, f workContextFixture) WorkerJobBinding {
	t.Helper()
	job := WorkerJobBinding{JobID: "review-subject-job-" + f.workID + "-" + time.Now().Format("150405.000000000"), Revision: 1}
	recordWorkerJobRevisionForTest(t, f.store, f.workID, f.owner, &job)
	return job
}

func reviewSubjectWindow(t *testing.T, s *Store, workID, attemptID string) WorkerDispatchWindow {
	t.Helper()
	var window WorkerDispatchWindow
	if err := s.Transact(context.Background(), func(tx *Transaction) error {
		var windowErr error
		window, windowErr = FindAuthorizedDispatchWindowTx(context.Background(), tx.tx, workID, attemptID)
		return windowErr
	}); err != nil {
		t.Fatalf("read review window: %v", err)
	}
	return window
}

// reviewSubjectImplementComplete dispatches one implementation attempt on a
// fresh ready job revision and records its terminal event, preparing the
// core delivery observation through the current producer path.
func reviewSubjectImplementComplete(t *testing.T, f workContextFixture, attemptID, eventID string) {
	t.Helper()
	s := f.store
	lane := laneForID(t)
	job := reviewSubjectReadyRevision(t, f)
	view, viewErr := readWorkContextView(context.Background(), s.DatabaseForTesting(), f.workID)
	if viewErr != nil || view == nil {
		t.Fatalf("admitted view = %+v err = %v, want a present view", view, viewErr)
	}
	if err := dispatchWorkContextAttempt(t, f, workContextDispatchPacket(t, f, attemptID, job, view)); err != nil {
		t.Fatalf("implement dispatch %s refused: %v", attemptID, err)
	}
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{implementDispatchEvidenceEvent(f.workID, attemptID, lane, job)}}); err != nil {
		t.Fatal(err)
	}
	version := WorkerEvidenceEventPayloadVersion(WorkerCompleted)
	completion := Event{EventID: eventID, Kind: WorkerCompleted, SubjectType: SubjectWorkItem, SubjectID: f.workID, Actor: "worker:test", OccurredAt: time.Unix(2, 0).UTC(), PayloadVersion: version, Payload: mustJSONValue(WorkerCompletedPayload{
		AttemptID: attemptID, ReadbackModel: preferredModelForLane(lane), ReportSchemaVersion: WorkerReportSchemaVersion,
		EvidenceOrigin: WorkerEvidenceReported, Evidence: laneCoveringEvidence(lane), WorkerJob: &job,
	})}
	ctx := context.Background()
	prepared, prepErr := PrepareWorkerTerminalDelivery(ctx, s, PrepareWorkerTerminalDeliveryRequest{WorkID: f.workID, AttemptID: attemptID, EventID: eventID})
	if prepErr != nil {
		t.Fatalf("terminal delivery preparation refused: %v", prepErr)
	}
	ctx = WithWorkerTerminalPreparation(ctx, prepared)
	workerTerminalEventThroughSeamCtx(t, ctx, s, completion)
}

func TestReviewDispatchPinsCurrentQualifiedSubject(t *testing.T) {
	fixture := seedWorkContextFixtureWithDefinition(t, "review-subject-current", mustBuiltinDefinition(t, "workflow.break_fix"))
	defer fixture.store.Close()
	s := fixture.store
	claimDir, head := reviewSubjectClaim(t, s, fixture.workID)
	view, err := readWorkContextView(context.Background(), s.DatabaseForTesting(), fixture.workID)
	if err != nil || view == nil || view.SubjectCommit != head {
		t.Fatalf("admitted view subject = %q err = %v, want the qualified HEAD %q", viewSubject(view), err, head)
	}
	if err := reviewSubjectDispatch(t, s, fixture, "attempt-review-baseline", claimDir); err != nil {
		t.Fatalf("baseline review dispatch refused: %v", err)
	}
	if window := reviewSubjectWindow(t, s, fixture.workID, "attempt-review-baseline"); window.SubjectCommit != head {
		t.Fatalf("review window subject = %q, want the current qualified candidate %q", window.SubjectCommit, head)
	}
}

// reviewSubjectPrepareDispatch prepares the one sealed dispatch proof the
// way the authenticated mutation boundary does, against the real claimed
// worktree.
func reviewSubjectPrepareDispatch(s *Store, workID, sessionWorktree string) (context.Context, error) {
	preparation, err := PrepareWorkerDispatchFreshness(context.Background(), s, PrepareWorkerDispatchRequest{WorkID: workID, SessionWorktree: sessionWorktree})
	if err != nil {
		return nil, err
	}
	return WithWorkerDispatchPreparation(context.Background(), preparation), nil
}

func viewSubject(view *WorkContextView) string {
	if view == nil {
		return ""
	}
	return view.SubjectCommit
}

// rawLiveDispatch runs one live dispatch action with no sealed preparation,
// directly against the raw action assembly the authenticated boundary drives,
// so the assembly's own admission is the surface under test.
func rawLiveDispatch(t *testing.T, s *Store, f workContextFixture, stepID, attemptID, sessionWorktree string, job WorkerJobBinding) error {
	t.Helper()
	laneVersion, laneDigest := registeredLaneIdentity(t, "review")
	packet := joinPacketFor(t, s, f.workID, stepID, attemptID, "review", laneVersion, laneDigest)
	packet["inputs"].(map[string]any)["worker_job"] = recordedPacketJobForTest(t, s, f.workID, job, "review")
	packet["inputs"].(map[string]any)["binding"].(map[string]any)["work_version"] = readWorkVersion(t, s, f.workID)
	packetBytes, err := json.Marshal(packet)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(map[string]any{"attempt_id": attemptID, "worker_packet": json.RawMessage(packetBytes)})
	if err != nil {
		t.Fatal(err)
	}
	request := WorkflowActionExecutionRequest{
		WorkID: f.workID, ExpectedVersion: readWorkVersion(t, s, f.workID), ActionID: "dispatch_worker", Payload: payload,
		SessionWorktree: sessionWorktree, Actor: f.owner,
		AcceptedInputsDigest: cd0059TestDigest(t, attemptID+"-inputs"), IdempotencyIdentity: attemptID + "-op",
		OperationID: "op-" + attemptID, PrincipalRef: f.owner.PrincipalRef, Tool: "concord_work_transition",
		IdempotencyKey: attemptID + "-key", RequestID: "req-" + attemptID, AcceptedScope: `{}`, ContractDigest: testManifestDigest,
	}
	err = s.Transact(context.Background(), func(tx *Transaction) error {
		_, inner := ApplyWorkflowActionTx(context.Background(), tx, BuiltinWorkflowRegistry(), request)
		return inner
	})
	return err
}

// TestLiveDispatchRequiresSealedPreparation proves the live assembly has no
// nil-proof route: an ordinary subject-bearing dispatch, a review dispatch,
// and a combined rejection that carries a retry each refuse when no sealed
// preparation exists, with zero effects. Recorded-history replay never
// reaches this live assembly guard and keeps its decode authority.
func TestLiveDispatchRequiresSealedPreparation(t *testing.T) {
	t.Run("an ordinary subject-bearing dispatch refuses without a preparation", func(t *testing.T) {
		fixture := seedWorkContextFixture(t, "work-live-noproof-ordinary")
		defer fixture.store.Close()
		s := fixture.store
		reviewSubjectClaim(t, s, fixture.workID)
		job := seedReadyWorkerJob(t, fixture)
		view, viewErr := readWorkContextView(context.Background(), s.DatabaseForTesting(), fixture.workID)
		if viewErr != nil || view == nil || view.SubjectCommit == "" {
			t.Fatalf("admitted view = %+v err = %v, want a qualified subject", view, viewErr)
		}
		err := rawLiveDispatch(t, s, fixture, "repair", "attempt-live-noproof-ordinary", dispatchSessionWorktree(t, s, fixture.workID), job)
		if err == nil || !hasFailureKind(err, KindInvalidOperation) || !strings.Contains(err.Error(), "sealed") {
			t.Fatalf("unprepared dispatch error = %v, want %s refusing the dispatch without its sealed preparation", err, KindInvalidOperation)
		}
		if got := dispatchStartedCount(t, s, fixture.workID); got != 0 {
			t.Fatalf("dispatch_worker starts = %d, want 0 on the missing-preparation refusal", got)
		}
	})

	t.Run("a review dispatch refuses without a preparation", func(t *testing.T) {
		fixture := seedWorkContextFixture(t, "work-live-noproof-review")
		defer fixture.store.Close()
		s := fixture.store
		claimDir, _ := reviewSubjectClaim(t, s, fixture.workID)
		job := seedReadyWorkerJob(t, fixture)
		err := rawLiveDispatch(t, s, fixture, "repair", "attempt-live-noproof-review", claimDir, job)
		if err == nil || !hasFailureKind(err, KindInvalidOperation) || !strings.Contains(err.Error(), "sealed") {
			t.Fatalf("unprepared review error = %v, want %s refusing the review without its sealed preparation", err, KindInvalidOperation)
		}
		if got := dispatchStartedCount(t, s, fixture.workID); got != 0 {
			t.Fatalf("dispatch_worker starts = %d, want 0 on the missing-preparation refusal", got)
		}
	})

	t.Run("a combined rejection with retry refuses without a preparation", func(t *testing.T) {
		fixture := seedWorkContextFixture(t, "work-live-noproof-reject")
		defer fixture.store.Close()
		s := fixture.store
		claimDir, _ := reviewSubjectClaim(t, s, fixture.workID)
		lane := laneForID(t)
		job := reviewSubjectReadyRevision(t, fixture)
		view, viewErr := readWorkContextView(context.Background(), s.DatabaseForTesting(), fixture.workID)
		if viewErr != nil || view == nil {
			t.Fatalf("admitted view = %+v err = %v, want a present view", view, viewErr)
		}
		if err := dispatchWorkContextAttempt(t, fixture, workContextDispatchPacket(t, fixture, "attempt-live-noproof-x", job, view)); err != nil {
			t.Fatalf("implement dispatch refused: %v", err)
		}
		if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{implementDispatchEvidenceEvent(fixture.workID, "attempt-live-noproof-x", lane, job)}}); err != nil {
			t.Fatal(err)
		}
		completion := Event{EventID: "complete-live-noproof-x", Kind: WorkerCompleted, SubjectType: SubjectWorkItem, SubjectID: fixture.workID, Actor: "worker:test", OccurredAt: time.Unix(2, 0).UTC(), PayloadVersion: WorkerEvidenceEventPayloadVersion(WorkerCompleted), Payload: mustJSONValue(WorkerCompletedPayload{
			AttemptID: "attempt-live-noproof-x", ReadbackModel: preferredModelForLane(lane), ReportSchemaVersion: WorkerReportSchemaVersion,
			EvidenceOrigin: WorkerEvidenceReported, Evidence: laneCoveringEvidence(lane), WorkerJob: &job,
		})}
		prepared, prepErr := PrepareWorkerTerminalDelivery(context.Background(), s, PrepareWorkerTerminalDeliveryRequest{WorkID: fixture.workID, AttemptID: "attempt-live-noproof-x", EventID: completion.EventID})
		if prepErr != nil {
			t.Fatalf("terminal delivery preparation refused: %v", prepErr)
		}
		workerTerminalEventThroughSeamCtx(t, WithWorkerTerminalPreparation(context.Background(), prepared), s, completion)
		reviewSubjectRecordOperator(t, s, fixture.workID)
		operator := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/operator", SessionRef: "session/" + fixture.workID + "-operator", ActorClass: ActorOperator}
		version := readWorkVersion(t, s, fixture.workID)
		payload, marshalErr := json.Marshal(map[string]any{
			"attempt_id": "attempt-live-noproof-x", "attempt_epoch": int64(1),
			"diagnosis": "the delivered change missed the recorded check", "strategy": "reproduce the check and repair the change",
			"predicate_ids": []string{"predicate:work-context"}, "evidence_refs": []string{"evidence:live-noproof-reject"},
			"retry": map[string]any{"lane_id": "implement"},
		})
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		request := WorkflowActionExecutionRequest{
			WorkID: fixture.workID, ExpectedVersion: version, ActionID: "reject_worker_result", Payload: payload,
			SessionWorktree: claimDir, Actor: operator, AcceptedInputsDigest: cd0059TestDigest(t, "live-noproof-reject-inputs"),
			IdempotencyIdentity: "live-noproof-reject-op", OperationID: "op-live-noproof-reject", PrincipalRef: operator.PrincipalRef,
			Tool: "concord_work_transition", IdempotencyKey: "live-noproof-reject-key", RequestID: "req-live-noproof-reject",
			AcceptedScope: `{}`, ContractDigest: testManifestDigest,
		}
		err := s.Transact(context.Background(), func(tx *Transaction) error {
			_, inner := ApplyWorkflowActionTx(context.Background(), tx, BuiltinWorkflowRegistry(), request)
			return inner
		})
		if err == nil || !hasFailureKind(err, KindInvalidOperation) || !strings.Contains(err.Error(), "sealed") {
			t.Fatalf("unprepared combined rejection error = %v, want %s refusing the whole operation without its sealed preparation", err, KindInvalidOperation)
		}
		var rejections, retryAttempts int
		if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_type='work_item' AND subject_id=? AND kind=? AND json_extract(payload,'$.action_id')='reject_worker_result'`, fixture.workID, WorkflowActionCompleted).Scan(&rejections); err != nil {
			t.Fatal(err)
		}
		if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM worker_attempts WHERE work_id=? AND attempt_id LIKE '%:retry'`, fixture.workID).Scan(&retryAttempts); err != nil {
			t.Fatal(err)
		}
		if rejections != 0 || retryAttempts != 0 {
			t.Fatalf("the unprepared combined operation committed %d rejection(s) and %d retry attempt(s), want zero effects", rejections, retryAttempts)
		}
	})
}

// qualifiedSubjectRunner models a clean zero-delta observation at the qualified
// verify subject. Real repositories resolve their immutable tree through Git;
// synthetic repositories model the tree OID as the subject OID. Declared-reading
// proofs still use the real fixture repositories.
type qualifiedSubjectRunner struct{ head string }

func (r qualifiedSubjectRunner) Run(ctx context.Context, dir string, args ...string) ([]byte, error) {
	if len(args) > 0 && args[0] == "status" {
		return nil, nil
	}
	if len(args) > 0 && args[0] == "rev-parse" {
		if strings.HasSuffix(args[len(args)-1], "^{tree}") {
			if _, err := os.Stat(dir + "/.git"); err == nil {
				return ExecGitRunner{}.Run(ctx, dir, args...)
			} else if !os.IsNotExist(err) {
				return nil, err
			}
		}
		return []byte(r.head), nil
	}
	if slices.Contains(args, "--name-only") {
		return nil, nil
	}
	return ExecGitRunner{}.Run(ctx, dir, args...)
}

// seedAcceptedImplementationDelivery dispatches, completes, and accepts one
// implementation attempt at the work's current step: the accepted delivery
// with a clean core observation that a review dispatch on implementation
// work requires. The observation is prepared against a synthetic runner
// answering the work's qualified verify subject, exactly as the shared
// dispatch invoker does.
func seedAcceptedImplementationDelivery(t *testing.T, s *Store, workID string, owner WorkflowActor) {
	t.Helper()
	ctx := context.Background()
	var stepID string
	if err := s.DatabaseForTesting().QueryRow(`SELECT current_step FROM workflow_instances WHERE work_id=?`, workID).Scan(&stepID); err != nil {
		t.Fatal(err)
	}
	lane := laneForID(t)
	job := WorkerJobBinding{JobID: "job:accepted-delivery-" + workID, Revision: 1}
	recordWorkerJobRevisionForTest(t, s, workID, owner, &job)
	attemptID := "attempt:accepted-delivery:" + workID
	packet := joinPacketFor(t, s, workID, stepID, attemptID, "implement", lane.Version, lane.Digest)
	packet["inputs"].(map[string]any)["worker_job"] = recordedPacketJobForTest(t, s, workID, job, "implement")
	packet["inputs"].(map[string]any)["binding"].(map[string]any)["work_version"] = readWorkVersion(t, s, workID)
	if err := dispatchJobPacketForTest(t, s, workID, attemptID, owner, "accepted-delivery-"+workID, packet); err != nil {
		t.Fatalf("accepted-delivery dispatch refused: %v", err)
	}
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{implementDispatchEvidenceEvent(workID, attemptID, lane, job)}}); err != nil {
		t.Fatal(err)
	}
	subject, subjectErr := readCurrentOracleSubject(ctx, s.DatabaseForTesting(), workID)
	if subjectErr != nil || subject == "" {
		t.Fatalf("qualified subject = %q err = %v, want the fixture's verify subject", subject, subjectErr)
	}
	completion := Event{EventID: "complete-accepted-delivery-" + workID, Kind: WorkerCompleted, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "worker:test", OccurredAt: time.Unix(3, 0).UTC(), PayloadVersion: WorkerEvidenceEventPayloadVersion(WorkerCompleted), Payload: mustJSONValue(WorkerCompletedPayload{
		AttemptID: attemptID, ReadbackModel: preferredModelForLane(lane), ReportSchemaVersion: WorkerReportSchemaVersion,
		EvidenceOrigin: WorkerEvidenceReported, Evidence: laneCoveringEvidence(lane), WorkerJob: &job,
	})}
	prepared, prepErr := PrepareWorkerTerminalDelivery(ctx, s, PrepareWorkerTerminalDeliveryRequest{WorkID: workID, AttemptID: attemptID, EventID: completion.EventID, Runner: qualifiedSubjectRunner{head: subject}})
	if prepErr != nil {
		t.Fatalf("accepted-delivery observation refused: %v", prepErr)
	}
	workerTerminalEventThroughSeamCtx(t, WithWorkerTerminalPreparation(ctx, prepared), s, completion)
	reviewSubjectAccept(t, s, workID, attemptID)
}

// TestReviewDispatchAfterContractSupersessionPinsCurrentSubject proves a
// contract version change alone does not move the reviewed candidate
// (CD-0242 D7): the review under the active successor contract pins the same
// qualified subject. A superseded work item cannot review at all through the
// instance lifecycle gate.
func TestReviewDispatchAfterContractSupersessionPinsCurrentSubject(t *testing.T) {
	fixture := seedWorkContextFixtureWithDefinition(t, "review-subject-supersede-pins", mustBuiltinDefinition(t, "workflow.break_fix"))
	defer fixture.store.Close()
	s := fixture.store
	claimDir, head := reviewSubjectClaim(t, s, fixture.workID)
	reviewSubjectSupersedeContract(t, s, fixture.workID)
	if err := reviewSubjectDispatch(t, s, fixture, "attempt-supersede-r1", claimDir); err != nil {
		t.Fatalf("review refused under the successor contract: %v", err)
	}
	if window := reviewSubjectWindow(t, s, fixture.workID, "attempt-supersede-r1"); window.SubjectCommit != head {
		t.Fatalf("review window subject = %q, want the unchanged qualified candidate %q", window.SubjectCommit, head)
	}

	// A superseded work item cannot review at all: the instance lifecycle
	// gate refuses every workflow action — the review dispatch's own job
	// record here — before any review effect exists.
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1);
		UPDATE workflow_instances SET instance_state='superseded' WHERE work_id=?;
		DELETE FROM fold_guard`, fixture.workID); err != nil {
		t.Fatal(err)
	}
	fields := map[string]any{
		"job_id": "job:superseded-review", "objective": "the superseded work item owes no review",
		"stopping_condition": "the recorded checks pass with no unresolved reference",
		"ready":              true, "readiness_evidence": []string{"evidence:superseded-review"},
	}
	if oracleCapablePinForWork(t, s, fixture.workID) {
		fields["acceptance_oracle"] = acceptanceOracleFieldsForTest(t, s, fixture.workID)
	}
	err := recordWorkerJobActionForTest(t, s, fixture.workID, fixture.owner, fields)
	if err == nil || !hasFailureKind(err, KindInvalidOperation) || !strings.Contains(err.Error(), "terminal workflow instance is immutable") {
		t.Fatalf("superseded-instance action error = %v, want %s refusing the terminal instance", err, KindInvalidOperation)
	}
}

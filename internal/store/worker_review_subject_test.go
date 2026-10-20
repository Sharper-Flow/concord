package store

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// This isolates the accepted-delivery query's prefix, not native dispatch
// admission. The existing native journeys exercise its actual producers.
func TestReviewDeliveryAnchorQueryIsPrefixBound(t *testing.T) {
	t.Parallel()
	db, appendEvent := correctionHistoryFixture(t)
	if _, err := db.Exec(`ALTER TABLE domain_events ADD COLUMN payload_version INTEGER NOT NULL DEFAULT 1`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE worker_attempts(attempt_id TEXT,work_id TEXT,capability_class TEXT,lifecycle_state TEXT)`); err != nil {
		t.Fatal(err)
	}
	var accepts []int64
	for _, attempt := range []string{"earlier", "later"} {
		if _, err := db.Exec(`INSERT INTO worker_attempts VALUES(?,'work-history','implementation','completed')`, attempt); err != nil {
			t.Fatal(err)
		}
		appendEvent(WorkflowActionCompleted, map[string]any{"action_id": "dispatch_worker", "worker_attempt_id": attempt, "worker_worktree_identity": "claim"})
		appendEvent(WorkerCompleted, map[string]any{"attempt_id": attempt})
		accepts = append(accepts, appendEvent(WorkflowActionCompleted, map[string]any{"action_id": "accept_worker_result", "worker_attempt_id": attempt}))
	}
	for boundary, want := range map[int64]int{accepts[0]: 0, accepts[0] + 1: 1, accepts[1]: 1, accepts[1] + 1: 2, 0: 2} {
		anchors, err := readReviewDeliveryAnchorsBefore(context.Background(), db, "work-history", boundary)
		if err != nil || len(anchors) != want {
			t.Fatalf("anchors before %d = %+v, %v; want %d", boundary, anchors, err, want)
		}
		if want == 1 && anchors[0].attemptID != "earlier" {
			t.Fatalf("later accepted delivery displaced the earlier prefix: %+v", anchors)
		}
	}
}

// The review subject: a review-lane dispatch on implementation and break_fix
// work reviews one immutable subject, the clean delivery the core observed on
// the latest accepted implementation-lane completion, which must equal the
// shared qualified candidate, the packet subject, and the live clean HEAD of
// the claimed worktree. Failed, rejected, unaccepted, wrong-worktree, and
// superseded-contract completions cannot replace the accepted source, and an
// accepted completion without a core observation requires a fresh
// implementation attempt.

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
	version := readWorkVersion(t, s, workID)
	ownerRef, err := WorkflowActorRef(WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/owner", SessionRef: "session/" + workID, ActorClass: ActorAgent})
	if err != nil {
		t.Fatal(err)
	}
	payload := map[string]any{
		"work_id": workID, "expected_version": version, "resulting_version": version + 1,
		"previous_contract_version": 1, "new_contract_version": 2,
		"supersede_reason": "the pinned contract moved past the accepted delivery",
		"audit_evidence":   []string{"evidence:review-subject-supersede"},
		"successor_contract": map[string]any{
			"contract_version": 2, "premise": "deliver the checked change under the second contract", "outcome_kind": "check",
			"outcome_predicates": []map[string]any{{"predicate_id": "predicate:review-subject", "ordinal": 0, "outcome_kind": "check", "outcome_payload": map[string]any{"kind": "check", "check_ref": "check:review-subject", "immutable_subject_ref": "commit:" + workID, "expected_result": "pass"}}},
			"required_evidence":  []string{"verification"}, "route_conventions": []string{}, "spec_mandate": []string{}, "law_modifies": []string{},
			"law_revisions": []WorkflowLawRevision{}, "law_boundary_version": 1, "rigor_class": "prototype_internal", "consequence_class": "internal_sqlite",
			"architecture_binding": WorkflowArchitectureBinding{
				DomainRegistryContentHash: "sha256:" + strings.Repeat("b", 64), HomeDomainID: workContextTestRoot, AffectedDomainIDs: []string{workContextTestRoot, workContextTestChild},
				DomainModifies: []string{}, DomainRelationModifies: []WorkflowDomainRelationModification{}, LawAdditions: []WorkflowLawAddition{}, VerificationObligations: []WorkflowVerificationObligation{},
			},
		},
	}
	event := workflowEventWithActor("review-supersede-1-"+workID, WorkflowContractSuperseded, workID, ownerRef, payload)
	event.PayloadVersion = 1
	event = withPinnedDecisionFact(t, pinnedWorkflowDefinitionForTest(t, s, workID), event)
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}}); err != nil {
		t.Fatalf("supersede contract v1: %v", err)
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
	packet["inputs"].(map[string]any)["worker_job"] = recordedPacketJobForTest(t, s, f.workID, job)
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

// reviewSubjectSecondClaim records one more active claim entry for the work,
// on another project row, so a review can present a session worktree the
// accepted delivery never named.
func reviewSubjectSecondClaim(t *testing.T, s *Store, workID, path string) {
	t.Helper()
	if _, err := s.DatabaseForTesting().Exec(`
INSERT INTO fold_guard(active) VALUES(1);
INSERT INTO worktree_entries(set_id,project_id,claim_op_id,branch,base_sha,path,repository_id,state,verified_at,git_facts)
VALUES(?,?,?,?,?,?,?,'active',?,'{}');
DELETE FROM fold_guard;`, WorktreeSetID(workID), "project-2", "claim-2", "change/claim", strings.Repeat("b", 40), path, "repo-2", "2026-09-05T00:00:00Z"); err != nil {
		t.Fatalf("insert second claim: %v", err)
	}
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
// core delivery observation when the terminal version carries one.
func reviewSubjectImplementComplete(t *testing.T, f workContextFixture, attemptID, eventID string, observed bool) {
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
	if !observed {
		version = workerCompletedTerminalDeliveryVersion - 1
	}
	completion := Event{EventID: eventID, Kind: WorkerCompleted, SubjectType: SubjectWorkItem, SubjectID: f.workID, Actor: "worker:test", OccurredAt: time.Unix(2, 0).UTC(), PayloadVersion: version, Payload: mustJSONValue(WorkerCompletedPayload{
		AttemptID: attemptID, ReadbackModel: preferredModelForLane(lane), ReportSchemaVersion: WorkerReportSchemaVersion,
		EvidenceOrigin: WorkerEvidenceReported, Evidence: laneCoveringEvidence(lane), WorkerJob: &job,
	})}
	ctx := context.Background()
	if observed {
		prepared, prepErr := PrepareWorkerTerminalDelivery(ctx, s, PrepareWorkerTerminalDeliveryRequest{WorkID: f.workID, AttemptID: attemptID, EventID: eventID})
		if prepErr != nil {
			t.Fatalf("terminal delivery preparation refused: %v", prepErr)
		}
		ctx = WithWorkerTerminalPreparation(ctx, prepared)
	}
	workerTerminalEventThroughSeamCtx(t, ctx, s, completion)
}

func TestReviewDispatchAcceptedDeliverySubject(t *testing.T) {
	// The review-subject fixture predates the failure-attribution
	// requirement, and the rule it pins does not depend on the pin
	// (settlement V3 option a): the review subject is selected from accepted
	// implementation deliveries joined to worktree claims
	// (worker_review_anchor.go readReviewDeliveryAnchors :16-46,
	// cleanCommit :57-75), never from the workflow pin's version. The only
	// pin-coupled member on this path is the failed-terminal attribution
	// gate, and the fixture's failed-completion scenery was authored as a
	// plain released terminal on a claim with no dispatch_worker
	// authorization — no window exists for an attributed identity to derive
	// from. Pin the latest pre-attribution head so the scenery keeps the
	// optional absence it shipped with.
	fixture := seedWorkContextFixtureWithDefinition(t, "review-subject-accepted", attributionInactiveWorkflowHeadForTest(t, "workflow.break_fix"))
	defer fixture.store.Close()
	s := fixture.store
	claimDir, head := reviewSubjectClaim(t, s, fixture.workID)
	view, err := readWorkContextView(context.Background(), s.DatabaseForTesting(), fixture.workID)
	if err != nil || view == nil || view.SubjectCommit != head {
		t.Fatalf("admitted view subject = %q err = %v, want the qualified HEAD %q", viewSubject(view), err, head)
	}
	reviewSubjectImplementComplete(t, fixture, "attempt-review-accepted-y", "complete-review-accepted-y", true)
	reviewSubjectAccept(t, s, fixture.workID, "attempt-review-accepted-y")
	// The other worktree claim lives for the whole test: a later subtest
	// presents it as the review session, and the claim entry must stay
	// resolvable until then.
	otherClaim := reviewSubjectCloneClaim(t, claimDir)

	t.Run("the review pins the accepted delivery subject", func(t *testing.T) {
		if err := reviewSubjectDispatch(t, s, fixture, "attempt-review-r1", claimDir); err != nil {
			t.Fatalf("review dispatch refused the accepted delivery: %v", err)
		}
		if window := reviewSubjectWindow(t, s, fixture.workID, "attempt-review-r1"); window.SubjectCommit != head {
			t.Fatalf("review window subject = %q, want the accepted delivery %q", window.SubjectCommit, head)
		}
	})

	t.Run("a newer unaccepted or failed completion cannot replace the accepted source", func(t *testing.T) {
		reviewSubjectImplementComplete(t, fixture, "attempt-review-unaccepted-z", "complete-review-unaccepted-z", true)
		if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{workerDispatchEvent(fixture.workID, "attempt-review-failed-z2", laneForID(t), nil)}}); err != nil {
			t.Fatal(err)
		}
		if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{workerFailedContextFindingsEvent(fixture.workID, "failed-review-z2", "attempt-review-failed-z2", preferredModelForLane(laneForID(t)), WorkerFailureWorkerError, nil, WorkerEvidenceEventPayloadVersion(WorkerFailed))}}); err != nil {
			t.Fatal(err)
		}
		if err := reviewSubjectDispatch(t, s, fixture, "attempt-review-r2", claimDir); err != nil {
			t.Fatalf("review refused with a newer unaccepted and failed completion standing: %v", err)
		}
		if window := reviewSubjectWindow(t, s, fixture.workID, "attempt-review-r2"); window.SubjectCommit != head {
			t.Fatalf("review window subject = %q, want the accepted source %q, never the unaccepted or failed completion", window.SubjectCommit, head)
		}
	})

	t.Run("a newer accepted delivery from another claimed worktree cannot replace the source", func(t *testing.T) {
		reviewSubjectSecondClaim(t, s, fixture.workID, otherClaim)
		// The newer accepted delivery is produced from the other claim, so
		// it never enters this worktree's accepted source: the review from
		// this claim still pins the older accepted delivery.
		wrongTree := seedWrongTreeAcceptedDelivery(t, s, fixture, otherClaim)
		if wrongTree == "" {
			t.Fatal("the wrong-worktree accepted delivery was not recorded")
		}
		if err := reviewSubjectDispatch(t, s, fixture, "attempt-review-newer-wrong-tree", claimDir); err != nil {
			t.Fatalf("review refused with a newer wrong-worktree acceptance standing: %v", err)
		}
		if window := reviewSubjectWindow(t, s, fixture.workID, "attempt-review-newer-wrong-tree"); window.SubjectCommit != head {
			t.Fatalf("review window subject = %q, want the older accepted source %q, never the newer wrong-worktree delivery", window.SubjectCommit, head)
		}
	})

	t.Run("a wrong-worktree-only accepted delivery cannot host the review", func(t *testing.T) {
		fixture := seedWorkContextFixture(t, "review-subject-wrong-only")
		defer fixture.store.Close()
		s := fixture.store
		claimDir, _ := reviewSubjectClaim(t, s, fixture.workID)
		other := reviewSubjectCloneClaim(t, claimDir)
		reviewSubjectSecondClaim(t, s, fixture.workID, other)
		if seedWrongTreeAcceptedDelivery(t, s, fixture, other) == "" {
			t.Fatal("the wrong-worktree accepted delivery was not recorded")
		}
		err := reviewSubjectDispatch(t, s, fixture, "attempt-review-wrong-only", claimDir)
		if err == nil || !hasFailureKind(err, KindUnauthorizedDispatch) || !strings.Contains(err.Error(), "no accepted implementation delivery on this worktree claim") {
			t.Fatalf("wrong-project-only review error = %v, want %s refusing the review whose only accepted delivery belongs to another worktree", err, KindUnauthorizedDispatch)
		}
	})

}

func TestReviewDispatchMissingObservedDelivery(t *testing.T) {
	fixture := seedWorkContextFixture(t, "review-subject-unobserved")
	defer fixture.store.Close()
	s := fixture.store
	claimDir, _ := reviewSubjectClaim(t, s, fixture.workID)
	// A completion recorded at the payload version before the core observed
	// deliveries carries no observed_delivery: the retained history an older
	// accepted result presents to the review.
	reviewSubjectImplementComplete(t, fixture, "attempt-review-unobserved", "complete-review-unobserved", false)
	reviewSubjectAccept(t, s, fixture.workID, "attempt-review-unobserved")
	err := reviewSubjectDispatch(t, s, fixture, "attempt-review-unobserved-r", claimDir)
	if err == nil || !hasFailureKind(err, KindUnauthorizedDispatch) || !strings.Contains(err.Error(), "fresh implementation attempt") {
		t.Fatalf("unobserved review error = %v, want %s requiring a fresh implementation attempt", err, KindUnauthorizedDispatch)
	}

	t.Run("a fresh accepted implementation closes the gap", func(t *testing.T) {
		reviewSubjectImplementComplete(t, fixture, "attempt-review-fresh", "complete-review-fresh", true)
		reviewSubjectAccept(t, s, fixture.workID, "attempt-review-fresh")
		if err := reviewSubjectDispatch(t, s, fixture, "attempt-review-fresh-r", claimDir); err != nil {
			t.Fatalf("review refused a fresh accepted implementation delivery: %v", err)
		}
		if window := reviewSubjectWindow(t, s, fixture.workID, "attempt-review-fresh-r"); window.SubjectCommit == "" {
			t.Fatal("the review window recorded no subject")
		}
	})
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

// reviewSubjectCloneClaim clones the claimed repository, so another worktree
// holds the same clean HEAD the qualified subject names.
func reviewSubjectCloneClaim(t *testing.T, claimDir string) string {
	t.Helper()
	other := filepath.Join(t.TempDir(), "other-claim")
	cmd := exec.Command("git", "clone", "--quiet", claimDir, other) //nolint:gosec // test-only git argv, fixed binary
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("clone claimed repository: %v: %s", err, stderr.String())
	}
	return other
}

// seedWrongTreeAcceptedDelivery dispatches, completes, and accepts one
// implementation attempt from the given session worktree, so the work holds
// an accepted delivery whose dispatch window names that other claim.
func seedWrongTreeAcceptedDelivery(t *testing.T, s *Store, f workContextFixture, sessionWorktree string) string {
	t.Helper()
	lane := laneForID(t)
	job := reviewSubjectReadyRevision(t, f)
	view, viewErr := readWorkContextView(context.Background(), s.DatabaseForTesting(), f.workID)
	if viewErr != nil || view == nil {
		t.Fatalf("admitted view = %+v err = %v, want a present view", view, viewErr)
	}
	attemptID := "attempt-review-wrong-tree-" + time.Now().Format("150405.000000000")
	packetBytes, marshalErr := json.Marshal(workContextDispatchPacket(t, f, attemptID, job, view))
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	payload, marshalErr := json.Marshal(map[string]any{"attempt_id": attemptID, "worker_packet": json.RawMessage(packetBytes)})
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	if _, err := invokeWorkflowActionForCD0059(context.Background(), t, s, WorkflowActionExecutionRequest{
		WorkID: f.workID, ExpectedVersion: readWorkVersion(t, s, f.workID), ActionID: "dispatch_worker", Payload: payload,
		SessionWorktree: sessionWorktree, Actor: f.owner,
		AcceptedInputsDigest: cd0059TestDigest(t, attemptID+"-inputs"), IdempotencyIdentity: attemptID + "-op",
		OperationID: "op-" + attemptID, PrincipalRef: f.owner.PrincipalRef, Tool: "concord_work_transition",
		IdempotencyKey: attemptID + "-key", RequestID: "req-" + attemptID, AcceptedScope: `{}`, ContractDigest: testManifestDigest,
	}); err != nil {
		t.Fatalf("wrong-tree implement dispatch refused: %v", err)
	}
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{implementDispatchEvidenceEvent(f.workID, attemptID, lane, job)}}); err != nil {
		t.Fatal(err)
	}
	subject, subjectErr := readCurrentOracleSubject(context.Background(), s.DatabaseForTesting(), f.workID)
	if subjectErr != nil || subject == "" {
		t.Fatalf("qualified subject = %q err = %v, want the fixture's verify subject", subject, subjectErr)
	}
	// The completion reports the same other claim its dispatch authorization
	// recorded, so the terminal ingest validates the attempt against its own
	// Project claim instead of the fixture's first claimed worktree.
	completion := Event{EventID: "complete-" + attemptID, Kind: WorkerCompleted, SubjectType: SubjectWorkItem, SubjectID: f.workID, Actor: "worker:test", OccurredAt: time.Unix(3, 0).UTC(), PayloadVersion: WorkerEvidenceEventPayloadVersion(WorkerCompleted), Payload: mustJSONValue(WorkerCompletedPayload{
		AttemptID: attemptID, ReadbackModel: preferredModelForLane(lane), ReportSchemaVersion: WorkerReportSchemaVersion,
		EvidenceOrigin: WorkerEvidenceReported, Evidence: laneCoveringEvidence(lane), WorkerJob: &job, WorkerDirectory: sessionWorktree,
	})}
	prepared, prepErr := PrepareWorkerTerminalDelivery(context.Background(), s, PrepareWorkerTerminalDeliveryRequest{WorkID: f.workID, AttemptID: attemptID, EventID: completion.EventID, Runner: qualifiedSubjectRunner{head: subject}})
	if prepErr != nil {
		t.Fatalf("wrong-tree observation refused: %v", prepErr)
	}
	workerTerminalEventThroughSeamCtx(t, WithWorkerTerminalPreparation(context.Background(), prepared), s, completion)
	reviewSubjectAccept(t, s, f.workID, attemptID)
	return attemptID
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
	packet["inputs"].(map[string]any)["worker_job"] = recordedPacketJobForTest(t, s, f.workID, job)
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

// qualifiedSubjectRunner answers the delivery probe without touching the
// filesystem: HEAD is the work's qualified verify subject and the tree is
// clean. Every other git call — the declared-reading resolution's committed
// proofs — runs against the real fixture repositories. A preparation over a
// subject-less view probes nothing.
type qualifiedSubjectRunner struct{ head string }

func (r qualifiedSubjectRunner) Run(ctx context.Context, dir string, args ...string) ([]byte, error) {
	if len(args) > 0 && args[0] == "status" {
		return nil, nil
	}
	if len(args) > 0 && args[0] == "rev-parse" {
		return []byte(r.head), nil
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
	packet["inputs"].(map[string]any)["worker_job"] = recordedPacketJobForTest(t, s, workID, job)
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

// TestReviewDispatchSupersededContractStillPinsAcceptedSubject proves the
// settled review rule: an accepted clean delivery recorded under an earlier
// contract version of the same work item hosts the review under the active
// contract. The review pins that delivery, and a later rejected
// implementation result does not replace it. A cancelled or superseded work
// item cannot review at all through the instance lifecycle gate.
func TestReviewDispatchSupersededContractStillPinsAcceptedSubject(t *testing.T) {
	// Same pin-independent review-subject rule as the accepted-subject suite
	// above (worker_review_anchor.go :16-46, :57-75; settlement V3 option
	// a). The rejected-implementation scenery additionally must not open a
	// correction record the later review dispatch would have to consume, so
	// the fixture keeps the pre-attribution head its review rule was
	// authored against.
	fixture := seedWorkContextFixtureWithDefinition(t, "review-subject-supersede-pins", attributionInactiveWorkflowHeadForTest(t, "workflow.break_fix"))
	defer fixture.store.Close()
	s := fixture.store
	claimDir, head := reviewSubjectClaim(t, s, fixture.workID)
	reviewSubjectImplementComplete(t, fixture, "attempt-supersede-y", "complete-supersede-y", true)
	reviewSubjectAccept(t, s, fixture.workID, "attempt-supersede-y")
	reviewSubjectSupersedeContract(t, s, fixture.workID)
	if err := reviewSubjectDispatch(t, s, fixture, "attempt-supersede-r1", claimDir); err != nil {
		t.Fatalf("review refused the accepted delivery under the superseded contract: %v", err)
	}
	if window := reviewSubjectWindow(t, s, fixture.workID, "attempt-supersede-r1"); window.SubjectCommit != head {
		t.Fatalf("review window subject = %q, want the accepted delivery %q from the superseded contract", window.SubjectCommit, head)
	}

	// A later rejected implementation result cannot replace the accepted
	// source, and it does not let an older one through either.
	lane := laneForID(t)
	failedJob := reviewSubjectReadyRevision(t, fixture)
	failedView, viewErr := readWorkContextView(context.Background(), s.DatabaseForTesting(), fixture.workID)
	if viewErr != nil || failedView == nil {
		t.Fatalf("admitted view = %+v err = %v, want a present view", failedView, viewErr)
	}
	if err := dispatchWorkContextAttempt(t, fixture, workContextDispatchPacket(t, fixture, "attempt-supersede-z", failedJob, failedView)); err != nil {
		t.Fatalf("rejected implement dispatch refused: %v", err)
	}
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{implementDispatchEvidenceEvent(fixture.workID, "attempt-supersede-z", lane, failedJob)}}); err != nil {
		t.Fatal(err)
	}
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{workerFailedContextFindingsEvent(fixture.workID, "failed-supersede-z", "attempt-supersede-z", preferredModelForLane(lane), WorkerFailureWorkerError, nil, WorkerEvidenceEventPayloadVersion(WorkerFailed))}}); err != nil {
		t.Fatal(err)
	}
	if err := reviewSubjectDispatch(t, s, fixture, "attempt-supersede-r2", claimDir); err != nil {
		t.Fatalf("review refused with a later rejected implementation standing: %v", err)
	}
	if window := reviewSubjectWindow(t, s, fixture.workID, "attempt-supersede-r2"); window.SubjectCommit != head {
		t.Fatalf("review window subject = %q, want the accepted source %q, never the rejected implementation", window.SubjectCommit, head)
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

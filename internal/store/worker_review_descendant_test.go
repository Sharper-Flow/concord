package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func descendantReviewFixture(t *testing.T, id string) (*candidateJourney, string) {
	t.Helper()
	f := seedWorkContextFixture(t, id)
	t.Cleanup(func() { f.store.Close() })
	dir, head := reviewSubjectClaim(t, f.store, id)
	var branch string
	if err := f.store.db.QueryRow(`SELECT branch FROM worktree_entries WHERE set_id=? AND state='active'`, WorktreeSetID(id)).Scan(&branch); err != nil {
		t.Fatal(err)
	}
	gitRunStore(t, dir, "branch", "-m", branch)
	reviewSubjectImplementComplete(t, f, "anchor-a", "anchor-a-completed", true)
	reviewSubjectAccept(t, f.store, id, "anchor-a")
	j := &candidateJourney{fixture: f, s: f.store, workID: id, repo: dir}
	j.observeRun(t, "refine-a")
	return j, head
}

type reviewAncestryOutageRunner struct{}

func (reviewAncestryOutageRunner) Run(ctx context.Context, path string, args ...string) ([]byte, error) {
	if len(args) > 0 && args[0] == "--no-replace-objects" {
		return nil, errors.New("synthetic ancestry outage")
	}
	return (ExecGitRunner{}).Run(ctx, path, args...)
}

func TestReviewDescendantAncestryOutagePreservesEditingRecovery(t *testing.T) {
	j, _ := descendantReviewFixture(t, "descendant-ancestry-outage")
	descendantCommit(t, j, "b")
	for _, lane := range []string{"review", "implement"} {
		job := reviewSubjectReadyRevision(t, j.fixture)
		prepared, err := PrepareWorkerDispatchFreshness(context.Background(), j.s, PrepareWorkerDispatchRequest{WorkID: j.workID, SessionWorktree: j.repo, Runner: reviewAncestryOutageRunner{}})
		if err != nil {
			t.Fatalf("prepare %s: %v", lane, err)
		}
		before := j.effects(t)
		err = descendantDispatch(t, j, lane, "outage-"+lane, job, prepared)
		if lane == "implement" {
			if err != nil {
				t.Fatalf("editing recovery under ancestry outage: %v", err)
			}
			continue
		}
		var failure *Failure
		if !errors.As(err, &failure) || failure.Kind != KindGitUnreachable || !failure.RetrySafe {
			t.Fatalf("review under ancestry outage = %v, want retryable git_unreachable", err)
		}
		if after := j.effects(t); after != before {
			t.Fatal("ancestry outage changed durable state")
		}
	}
}

func descendantCommit(t *testing.T, j *candidateJourney, id string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(j.repo, "seed.txt"), []byte(id+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	head := j.commit(t, "coordinator@example.invalid", id)
	if run := j.observeRun(t, id); run.SubjectRef != "commit:"+head {
		t.Fatalf("qualified subject = %q, want %q", run.SubjectRef, head)
	}
	return head
}

func descendantDispatch(t *testing.T, j *candidateJourney, laneID, attempt string, job WorkerJobBinding, prepared *WorkerDispatchPreparation) error {
	t.Helper()
	version, digest := registeredLaneIdentity(t, laneID)
	packet := joinPacketFor(t, j.s, j.workID, currentStep(t, j.s, j.workID), attempt, laneID, version, digest)
	packet["inputs"].(map[string]any)["worker_job"] = recordedPacketJobForTest(t, j.s, j.workID, job, laneID)
	correction, err := workflowCorrectionContextForDispatch(context.Background(), j.s.db, j.workID, currentStep(t, j.s, j.workID), attempt)
	if err != nil {
		return err
	}
	if correction != nil {
		packet["inputs"].(map[string]any)["correction"] = correction
	}
	if prepared == nil {
		var err error
		prepared, err = PrepareWorkerDispatchFreshness(context.Background(), j.s, PrepareWorkerDispatchRequest{WorkID: j.workID, SessionWorktree: j.repo})
		if err != nil {
			return err
		}
	}
	ctx := WithWorkerDispatchPreparation(context.Background(), prepared)
	_, err = dispatchJoinAttempt(ctx, t, j.s, j.workID, readWorkVersion(t, j.s, j.workID), j.fixture.owner, packet)
	return err
}

func requireDescendantRefusal(t *testing.T, j *candidateJourney, attempt string, kind FailureKind) {
	t.Helper()
	job := reviewSubjectReadyRevision(t, j.fixture)
	before := j.effects(t)
	err := descendantDispatch(t, j, "review", attempt, job, nil)
	if failureKind(err) != kind {
		t.Fatalf("review refusal = %v, want %s", err, kind)
	}
	if after := j.effects(t); after != before {
		t.Fatalf("refused review changed durable state:\nbefore=%s\nafter=%s", before, after)
	}
}

func TestReviewDescendantNewerEditingDispatchRefuses(t *testing.T) {
	for _, state := range []string{"in_flight", "failed", "abandoned", "rejected", "unaccepted", "design"} {
		t.Run(state, func(t *testing.T) {
			j, _ := descendantReviewFixture(t, "descendant-editor-"+state)
			snapshot := j.snapshotSubjectRecords(t, j.pinned(t))
			descendantCommit(t, j, "b")
			laneID, capability := "implement", "implementation"
			if state == "design" {
				laneID, capability = "design", "design"
			}
			job := reviewSubjectReadyRevision(t, j.fixture)
			if err := descendantDispatch(t, j, laneID, "new-editor", job, nil); err != nil {
				t.Fatalf("editing authorization: %v", err)
			}
			if state != "in_flight" && state != "design" {
				j.laneDispatch(t, "new-editor", capability, &job)
				switch state {
				case "failed", "abandoned":
					kind := WorkerFailureWorkerError
					if state == "abandoned" {
						kind = WorkerFailureAbandoned
					}
					event := Event{
						EventID: "new-editor-failed", Kind: WorkerFailed, SubjectType: SubjectWorkItem, SubjectID: j.workID,
						Actor: "worker:test", OccurredAt: time.Unix(3, 0).UTC(), PayloadVersion: WorkerEvidenceEventPayloadVersion(WorkerFailed),
						Payload: mustJSONValue(WorkerFailedPayload{
							AttemptID: "new-editor", ReadbackModel: preferredModelForLane(reviewGateLane(t, capability)), FailureKind: kind, Detail: "bounded worker error",
							FailureAttribution: &WorkerFailureAttribution{Origin: WorkerFailureAttributionRecorded, Observation: attributionHostObservation()},
						}),
					}
					workerTerminalEventThroughSeam(t, j.s, event)
				default:
					j.completeLane(t, "new-editor", capability, &job, nil)
					if state == "rejected" {
						reviewer := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/rejector", SessionRef: "session/rejector", ActorClass: ActorAgent}
						if err := j.action(t, "reject_worker_result", map[string]any{
							"attempt_id": "new-editor", "attempt_epoch": j.dispatchEpoch(t, "new-editor"),
							"diagnosis": "the editing result does not satisfy the approved obligation", "strategy": "correct the same obligation", "predicate_ids": []string{"predicate:work-context"}, "evidence_refs": []string{"new-editor-completed"},
						}, reviewer); err != nil {
							t.Fatalf("reject newer editing result: %v", err)
						}
					}
				}
			}
			requireDescendantRefusal(t, j, "refused-review", KindUnauthorizedDispatch)
			j.requireRecordsUnchanged(t, snapshot)
		})
	}
}

func TestReviewDescendantNonDescendantRefuses(t *testing.T) {
	for _, shape := range []string{"sibling", "older_rejected"} {
		t.Run(shape, func(t *testing.T) {
			j, root := descendantReviewFixture(t, "descendant-nonancestor-"+shape)
			if shape == "older_rejected" {
				// The rejected result predates a later genuinely produced and
				// accepted anchor. Resetting HEAD cannot revive the rejected result.
				reviewSubjectImplementComplete(t, j.fixture, "old-rejected", "old-rejected-completed", true)
				rejector := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/old-rejector", SessionRef: "session/old-rejector", ActorClass: ActorAgent}
				if err := j.action(t, "reject_worker_result", map[string]any{
					"attempt_id": "old-rejected", "attempt_epoch": j.dispatchEpoch(t, "old-rejected"), "diagnosis": "the old result is insufficient", "strategy": "produce the approved change", "predicate_ids": []string{"predicate:work-context"}, "evidence_refs": []string{"old-rejected-completed"},
				}, rejector); err != nil {
					t.Fatal(err)
				}
			}
			descendantCommit(t, j, "anchor-new")
			job := reviewSubjectReadyRevision(t, j.fixture)
			if err := descendantDispatch(t, j, "implement", "anchor-new", job, nil); err != nil {
				t.Fatal(err)
			}
			j.laneDispatch(t, "anchor-new", "implementation", &job)
			j.completeLane(t, "anchor-new", "implementation", &job, nil)
			reviewSubjectAccept(t, j.s, j.workID, "anchor-new")
			gitRunStore(t, j.repo, "reset", "--hard", root)
			if shape == "sibling" {
				descendantCommit(t, j, "sibling")
			} else {
				j.observeRun(t, "reset-rejected")
			}
			requireDescendantRefusal(t, j, "nonancestor-review", KindUnauthorizedDispatch)
		})
	}
}

func TestReviewDescendantPreparedSubjectDriftRefuses(t *testing.T) {
	j, _ := descendantReviewFixture(t, "descendant-prepared-drift")
	descendantCommit(t, j, "b")
	job := reviewSubjectReadyRevision(t, j.fixture)
	prepared, err := PrepareWorkerDispatchFreshness(context.Background(), j.s, PrepareWorkerDispatchRequest{WorkID: j.workID, SessionWorktree: j.repo})
	if err != nil {
		t.Fatal(err)
	}
	descendantCommit(t, j, "c")
	before := j.effects(t)
	if err := descendantDispatch(t, j, "review", "drift-review", job, prepared); failureKind(err) != KindMissingEvidence {
		t.Fatalf("prepared subject drift = %v, want missing_evidence for the stale job preparation", err)
	}
	if after := j.effects(t); after != before {
		t.Fatal("prepared-subject refusal changed durable state")
	}
}

func TestReviewDescendantPostObservationMovementKeepsObservedSubject(t *testing.T) {
	j := journeyThroughA(t, "descendant-unobserved-prepared-drift", true)
	j.commitB(t)
	runB := j.observeRun(t, "b")
	j.bindVerification(t, runB)
	laneVersion, laneDigest := registeredLaneIdentity(t, "review")
	packet := joinPacketFor(t, j.s, j.workID, currentStep(t, j.s, j.workID), "observed-b-review", "review", laneVersion, laneDigest)
	prepared, err := PrepareWorkerDispatchFreshness(context.Background(), j.s, PrepareWorkerDispatchRequest{WorkID: j.workID, SessionWorktree: j.repo})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(j.repo, "README.md"), []byte("unobserved c\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := j.commit(t, "coordinator@example.invalid", "unobserved c after preparation")
	if c == j.b || j.pinned(t) != j.b {
		t.Fatal("fixture must move live HEAD without replacing the qualified B pin")
	}
	ctx := WithWorkerDispatchPreparation(context.Background(), prepared)
	if _, err := dispatchJoinAttempt(ctx, t, j.s, j.workID, readWorkVersion(t, j.s, j.workID), j.fixture.owner, packet); err != nil {
		t.Fatalf("dispatch the sealed B observation: %v", err)
	}
	if subject := j.dispatchedSubject(t, "observed-b-review"); subject != j.b || subject == c {
		t.Fatalf("review subject = %q, want observed B %q, never unobserved C %q", subject, j.b, c)
	}
	inputs := packet["inputs"].(map[string]any)
	for _, name := range []string{"subject_commit", "candidate_subject", "worker_subject_commit"} {
		if _, exists := inputs[name]; exists {
			t.Fatalf("dispatch supplied an independent subject field %q", name)
		}
	}
	before := j.effects(t)
	err = j.terminalAction(t, "record_verdict", mustJSONValue(map[string]any{"contract_version": 1, "verdicts": []map[string]any{{"predicate_id": "predicate:work-context", "verdict_kind": "ok", "evaluation_evidence": []string{runB.OperationRef}}}}), j.reviewer, nil)
	if failureKind(err) != KindStaleRequiresReview {
		t.Fatalf("next re-observing gate = %v, want stale_requires_review", err)
	}
	if after := j.effects(t); after != before {
		t.Errorf("refused re-observing gate changed durable state:\nbefore=%s\nafter=%s", before, after)
	}
	j.requireRecordsUnchanged(t, j.snapshotA)
}

func TestReviewDescendantLaterCorrectiveAnchor(t *testing.T) {
	for _, accepted := range []bool{false, true} {
		t.Run(map[bool]string{false: "rejected", true: "accepted"}[accepted], func(t *testing.T) {
			j, _ := descendantReviewFixture(t, map[bool]string{false: "descendant-corrective-rejected", true: "descendant-corrective-accepted"}[accepted])
			descendantCommit(t, j, "b")
			job := reviewSubjectReadyRevision(t, j.fixture)
			if err := descendantDispatch(t, j, "implement", "corrective", job, nil); err != nil {
				t.Fatal(err)
			}
			j.laneDispatch(t, "corrective", "implementation", &job)
			c := descendantCommit(t, j, "worker-correction")
			j.completeLane(t, "corrective", "implementation", &job, nil)
			if !accepted {
				rejector := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/corrective-rejector", SessionRef: "session/corrective-rejector", ActorClass: ActorAgent}
				if err := j.action(t, "reject_worker_result", map[string]any{
					"attempt_id": "corrective", "attempt_epoch": j.dispatchEpoch(t, "corrective"), "diagnosis": "the corrective result is insufficient", "strategy": "correct the same obligation", "predicate_ids": []string{"predicate:work-context"}, "evidence_refs": []string{"candidate-completed-corrective"},
				}, rejector); err != nil {
					t.Fatal(err)
				}
				requireDescendantRefusal(t, j, "rejected-corrective-review", KindUnauthorizedDispatch)
				return
			}
			reviewSubjectAccept(t, j.s, j.workID, "corrective")
			anchors, err := readReviewDeliveryAnchors(context.Background(), j.s.db, j.workID)
			if err != nil {
				t.Fatal(err)
			}
			anchor := selectReviewDeliveryAnchor(anchors, workerWorktreeIdentity(j.repo))
			if anchor == nil || anchor.attemptID != "corrective" {
				t.Fatalf("accepted anchor = %+v, want corrective", anchor)
			}
			job = reviewSubjectReadyRevision(t, j.fixture)
			if err := descendantDispatch(t, j, "review", "corrective-review", job, nil); err != nil {
				t.Fatalf("review of accepted correction: %v", err)
			}
			if got := j.dispatchedSubject(t, "corrective-review"); got != c {
				t.Fatalf("review subject = %q, want %q", got, c)
			}
		})
	}
}

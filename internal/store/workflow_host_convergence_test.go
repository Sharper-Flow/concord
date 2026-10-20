package store

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/hostlease"
)

// hostConvergenceFailureAt is the wall-clock instant the fixture's
// host-cancelled terminal records. The boundary stubs anchor to it.
var hostConvergenceFailureAt = time.Unix(90, 0).UTC()

// hostConvergenceFailureEvent builds one current-version worker.failed
// event carrying the recorded producer observation exactly as the
// authenticated CLI assembles it: origin and observation only, never an
// identity. The observation is the caller's; the anchor time is the
// fixture's.
func hostConvergenceFailureEvent(workID, attemptID, eventID string, observation *WorkerFailureObservation, at time.Time) Event {
	payload := WorkerFailedPayload{AttemptID: attemptID, ReadbackModel: preferredModelForLane(BuiltinLaneDefinitions()[0]), FailureKind: WorkerFailureWorkerError, Detail: "the attempt failed before a judgeable result"}
	payload.FailureAttribution = &WorkerFailureAttribution{Origin: WorkerFailureAttributionRecorded, Observation: observation}
	for _, source := range observation.Sources {
		if source.Host != nil && source.Host.Tag == WorkerFailureHostTagAbandonment {
			payload.FailureKind = WorkerFailureAbandoned
		}
	}
	return Event{EventID: eventID, Kind: WorkerFailed, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "worker:test", OccurredAt: at, PayloadVersion: WorkerEvidenceEventPayloadVersion(WorkerFailed), Payload: mustJSONValue(payload)}
}

// hostConvergenceSeedEscalated drives the escalated correction fixture with
// one change: the third terminal closes through the signed worker-evidence
// seam carrying observation, so the failure the wall reads is the recorded
// host execution failure CD-0239 D1 condition 2 admits.
func hostConvergenceSeedEscalated(t *testing.T, workID string, observation *WorkerFailureObservation) (*Store, WorkflowActor) {
	t.Helper()
	s, owner, attemptID, _ := seedOldDefinitionWorker(t, workID)
	claimRetryWorktree(t, s, workID)
	worker := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/worker", SessionRef: "session/" + workID, ActorClass: ActorAgent}
	workerEpoch := int64(1)
	for correctionAttempt := int64(1); correctionAttempt <= 3; correctionAttempt++ {
		version := readWorkVersion(t, s, workID)
		if correctionAttempt == 3 {
			event := hostConvergenceFailureEvent(workID, attemptID, "failed-host-"+workID, observation, hostConvergenceFailureAt)
			if err := s2WorkerFailedLive(t, s, event); err != nil {
				t.Fatalf("the attributed host terminal ingest refused: %v", err)
			}
		} else {
			failWorkerAttempt(t, s, workID, attemptID)
		}
		applyRecordWorkerFailureForTest(t, s, workID, owner, attemptID, workerEpoch, version, "host-convergence-record-"+workID+fmt.Sprint(correctionAttempt))
		pin := issue1013Pin(t, s, workID)
		issue1013StartRepair(t, s, workID, worker, pin.Version, workerEpoch+1)
		pin = issue1013Pin(t, s, workID)
		if correctionAttempt == 3 {
			return s, owner
		}
		attemptID = "attempt:" + workID + ":" + fmt.Sprint(correctionAttempt+1)
		payload := issue1013CorrectionDispatchPayload(t, s, workID, "repair", attemptID, pin.Correction)
		if _, err := invokeWorkflowActionForCD0059(context.Background(), t, s, WorkflowActionExecutionRequest{
			WorkID: workID, ExpectedVersion: pin.Version, ActionID: "dispatch_worker", Payload: payload, SessionWorktree: dispatchSessionWorktree(t, s, workID),
			Actor: worker, AcceptedInputsDigest: "sha256:" + strings.Repeat("d", 64), IdempotencyIdentity: "host-convergence-dispatch-" + attemptID, OperationID: "host-convergence-dispatch-" + attemptID,
			PrincipalRef: worker.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: "host-convergence-dispatch-" + attemptID, RequestID: "request:host-convergence-dispatch-" + attemptID, ContractDigest: testManifestDigest, Now: time.Unix(10+correctionAttempt, 0).UTC(),
		}); err != nil {
			t.Fatalf("dispatch correction attempt %d: %v", correctionAttempt+1, err)
		}
		issue1013RecordWorkerDispatch(t, s, workID, attemptID)
		workerEpoch += 2
	}
	t.Fatal("the escalated host failure fixture did not return")
	return nil, WorkflowActor{}
}

// hostConvergencePinOccupancy records the dispatching session's occupancy
// row over the claimed worktree the dispatch admission boundary accepts,
// with the live test process as the recorded host identity (CD-0178 D3).
func hostConvergencePinOccupancy(t *testing.T, s *Store, workID, sessionRef string) (int, uint64) {
	t.Helper()
	pid := os.Getpid()
	pidStart, err := hostlease.ProcessStart(pid)
	if err != nil {
		t.Fatalf("cannot read the test process start: %v", err)
	}
	if _, err := s.RecordWorktreeClaimLanding(context.Background(), WorktreeClaimLandingRequest{
		WorkID: workID, SessionRef: sessionRef, LandedDirectory: dispatchSessionWorktree(t, s, workID), HostPID: pid,
	}); err != nil {
		t.Fatalf("record the session occupancy: %v", err)
	}
	return pid, pidStart
}

// hostConvergenceLeaseContext carries the live host lease observation for
// the given process the way the CLI does before a transaction (CD-0179 D3).
func hostConvergenceLeaseContext(ctx context.Context, pid int) context.Context {
	start, _ := hostlease.ProcessStart(pid)
	return WithHostLeaseSet(ctx, HostLeaseSet{Leases: []hostlease.Lease{{PID: pid, PidStart: start}}})
}

// hostConvergencePinWallStart pins the hostLeaseWallStart seam to one
// answer. The stub ignores the pid: the fixture records the test process,
// and the wall-clock start is exactly the value under test.
func hostConvergencePinWallStart(t *testing.T, start time.Time) {
	t.Helper()
	previous := hostLeaseWallStart
	hostLeaseWallStart = func(int) (time.Time, error) { return start, nil }
	t.Cleanup(func() { hostLeaseWallStart = previous })
}

// hostConvergenceDispatch drives one escalated wall dispatch through the
// public request route on the supplied context. The caller pins the wall
// seam and refetches the pin.
func hostConvergenceDispatch(t *testing.T, s *Store, workID, attemptID string, owner WorkflowActor, pin WorkPin, ctx context.Context, identity string) error {
	t.Helper()
	owner.AgentRef = "agent/worker"
	payload := issue1013CorrectionDispatchPayload(t, s, workID, "repair", attemptID, pin.Correction)
	_, err := invokeWorkflowActionForCD0059(ctx, t, s, WorkflowActionExecutionRequest{
		WorkID: workID, ExpectedVersion: pin.Version, ActionID: "dispatch_worker", Payload: payload,
		SessionWorktree: dispatchSessionWorktree(t, s, workID), Actor: owner,
		AcceptedInputsDigest: "sha256:" + strings.Repeat("e", 64), IdempotencyIdentity: identity, OperationID: identity,
		PrincipalRef: owner.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: identity,
		RequestID: "request:" + identity, ContractDigest: testManifestDigest, Now: time.Unix(120, 0).UTC(),
	})
	return err
}

// hostConvergenceRecordedConvergence reads the convergence the admitted
// dispatch recorded beside its completion.
func hostConvergenceRecordedConvergence(t *testing.T, s *Store, workID, attemptID string) WorkflowRetryConvergence {
	t.Helper()
	var raw string
	if err := s.DatabaseForTesting().QueryRow(`SELECT json_extract(payload,'$.retry_convergence') FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.action_id')='dispatch_worker' AND json_extract(payload,'$.worker_attempt_id')=? ORDER BY seq DESC LIMIT 1`, workID, WorkflowActionCompleted, attemptID).Scan(&raw); err != nil {
		t.Fatalf("read the admitted dispatch convergence: %v", err)
	}
	var basis WorkflowRetryConvergence
	if json.Unmarshal([]byte(raw), &basis) != nil {
		t.Fatalf("recorded convergence %s is not a WorkflowRetryConvergence", raw)
	}
	return basis
}

// hostConvergenceReportPhaseObservation is the invalid-report shape the law
// names: the host adapter observed a report phase, not a host execution.
func hostConvergenceReportPhaseObservation() *WorkerFailureObservation {
	return &WorkerFailureObservation{
		Observer: WorkerFailureObserverHostAdapter,
		Phase:    WorkerFailurePhaseReportParse,
		Cause:    WorkerFailureCauseUnknown,
		Basis:    WorkerFailureBasisUnknown,
		Sources:  []WorkerFailureSource{{Kind: WorkerFailureSourceValidator, Locator: "validator-host-1", Digest: testDigestB, Message: "worker-complete refused: the report names undeclared evidence"}},
	}
}

// hostConvergenceAbandonmentObservation is the abandonment shape the law
// excludes: the host adapter observed the coordinator's abandonment request.
func hostConvergenceAbandonmentObservation() *WorkerFailureObservation {
	return &WorkerFailureObservation{
		Observer: WorkerFailureObserverHostAdapter,
		Phase:    WorkerFailurePhaseHostExecution,
		Cause:    WorkerFailureCauseUnknown,
		Basis:    WorkerFailureBasisUnknown,
		Sources: []WorkerFailureSource{{Kind: WorkerFailureSourceHostEvent, Locator: "evt-host-abandon-1", Digest: testDigestC,
			Host: &WorkerFailureHostObservation{Tag: WorkerFailureHostTagAbandonment, EventType: "session.status"}}},
	}
}

func TestHostChangedConvergenceAdmitsOneDispatchFromARestartedHost(t *testing.T) {
	t.Run("a restart just beyond the minute admits one dispatch", func(t *testing.T) {
		const workID = "host-convergence-admit"
		s, owner := hostConvergenceSeedEscalated(t, workID, workerFailureObservationFixture())
		defer s.Close()
		pid, pidStart := hostConvergencePinOccupancy(t, s, workID, owner.SessionRef)
		ctx := hostConvergenceLeaseContext(context.Background(), pid)
		hostConvergencePinWallStart(t, hostConvergenceFailureAt.Add(time.Minute+time.Second))

		attempt := "attempt:" + workID + ":4"
		pin := issue1013Pin(t, s, workID)
		dispatchVisible := false
		for _, intent := range pin.NextValidIntents {
			if intent.ActionID == "dispatch_worker" {
				dispatchVisible = intent.ReasonCode == "escalated_retry_requires_convergence"
			}
		}
		if !dispatchVisible {
			t.Fatal("the host-free work pin must retain the visible convergence-required dispatch")
		}
		if err := hostConvergenceDispatch(t, s, workID, attempt, owner, pin, ctx, "host-convergence-admit-4"); err != nil {
			t.Fatalf("dispatch from the restarted host: %v", err)
		}
		basis := hostConvergenceRecordedConvergence(t, s, workID, attempt)
		if basis.Basis != "host_changed" {
			t.Fatalf("recorded convergence = %+v, want the host_changed basis", basis)
		}
		var terminalSeq int64
		if err := s.db.QueryRow(`SELECT seq FROM domain_events WHERE event_id=?`, "failed-host-"+workID).Scan(&terminalSeq); err != nil {
			t.Fatal(err)
		}
		if basis.LatestRecordSeq != terminalSeq || basis.HostPID != pid || basis.HostPIDStart != pidStart {
			t.Fatalf("recorded basis = %+v; want terminal %d and process %d/%d", basis, terminalSeq, pid, pidStart)
		}

		// CD-0164: the basis opens no window. The recorded correction count
		// survives the admitted attempt.
		if correction := issue1013Pin(t, s, workID).Correction; correction == nil || correction.AttemptCount < 3 {
			t.Fatalf("correction after the admitted host_changed dispatch = %#v, want the count of at least 3 preserved", correction)
		}

		// Rebuild uses the recorded basis facts, not the current host.
		if err := RebuildFromLog(context.Background(), s); err != nil {
			t.Fatalf("rebuild with the recorded host_changed basis: %v", err)
		}
		if again := hostConvergenceRecordedConvergence(t, s, workID, attempt); again != basis {
			t.Fatalf("rebuild changed the recorded basis: %+v -> %+v", basis, again)
		}

		// One host_changed dispatch per worker-satisfaction window: the
		// fifth dispatch faces the wall again with the host inputs still valid.
		pin = issue1013Pin(t, s, workID)
		if err := hostConvergenceDispatch(t, s, workID, "attempt:"+workID+":5", owner, pin, ctx, "host-convergence-admit-5"); !hasFailureKind(err, KindMissingEvidence) {
			t.Fatalf("second host_changed dispatch in the same window = %v, want missing_evidence", err)
		}
	})

	t.Run("exactly one minute after the failure is not strictly more", func(t *testing.T) {
		const workID = "host-convergence-admit-exact-minute"
		s, owner := hostConvergenceSeedEscalated(t, workID, workerFailureObservationFixture())
		defer s.Close()
		hostConvergencePinOccupancy(t, s, workID, owner.SessionRef)
		ctx := hostConvergenceLeaseContext(context.Background(), os.Getpid())
		hostConvergencePinWallStart(t, hostConvergenceFailureAt.Add(time.Minute))
		pin := issue1013Pin(t, s, workID)
		before := pin.Version
		if err := hostConvergenceDispatch(t, s, workID, "attempt:"+workID+":4", owner, pin, ctx, "host-convergence-exact-minute"); !hasFailureKind(err, KindMissingEvidence) {
			t.Fatalf("dispatch at exactly one minute = %v, want missing_evidence", err)
		}
		if after := readWorkVersion(t, s, workID); after != before {
			t.Fatalf("refused dispatch changed the work version: %d -> %d", before, after)
		}
	})

	t.Run("an existing approach basis takes precedence", func(t *testing.T) {
		const workID = "host-convergence-approach-precedence"
		s, owner := hostConvergenceSeedEscalated(t, workID, workerFailureObservationFixture())
		defer s.Close()
		hostConvergencePinOccupancy(t, s, workID, owner.SessionRef)
		ctx := hostConvergenceLeaseContext(context.Background(), os.Getpid())
		hostConvergencePinWallStart(t, hostConvergenceFailureAt.Add(time.Minute+time.Second))
		if err := convergenceSupersedeBasis(t, s, workID, owner); err != nil {
			t.Fatalf("supersede the contract behind the escalation wall: %v", err)
		}
		attempt := "attempt:" + workID + ":4"
		pin := issue1013Pin(t, s, workID)
		if err := hostConvergenceDispatch(t, s, workID, attempt, owner, pin, ctx, "host-convergence-approach-4"); err != nil {
			t.Fatalf("dispatch behind the approach basis: %v", err)
		}
		if basis := hostConvergenceRecordedConvergence(t, s, workID, attempt); basis.Basis != "approach_changed" {
			t.Fatalf("recorded convergence = %+v, want the approach_changed basis to keep precedence", basis)
		}
	})
}

func TestHostChangedConvergenceNeverFollowsAWorkerAttributedFailure(t *testing.T) {
	for _, tc := range []struct {
		name        string
		observation *WorkerFailureObservation
	}{
		{"a worker-reported execution failure", attributionWorkerObservation()},
		{"a host adapter report phase", hostConvergenceReportPhaseObservation()},
		{"an abandonment-tagged host source", hostConvergenceAbandonmentObservation()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workID := "host-convergence-" + strings.ReplaceAll(tc.name, " ", "-")
			s, owner := hostConvergenceSeedEscalated(t, workID, tc.observation)
			defer s.Close()
			hostConvergencePinOccupancy(t, s, workID, owner.SessionRef)
			ctx := hostConvergenceLeaseContext(context.Background(), os.Getpid())
			// The host inputs are a fully valid restart; only the
			// attribution fails the derivation.
			hostConvergencePinWallStart(t, hostConvergenceFailureAt.Add(time.Minute+time.Second))
			pin := issue1013Pin(t, s, workID)
			before := pin.Version
			err := hostConvergenceDispatch(t, s, workID, "attempt:"+workID+":4", owner, pin, ctx, "host-convergence-exclusion-"+workID)
			if !hasFailureKind(err, KindMissingEvidence) {
				t.Fatalf("dispatch behind a %s = %v, want missing_evidence", tc.name, err)
			}
			if after := readWorkVersion(t, s, workID); after != before {
				t.Fatalf("refused dispatch changed the work version: %d -> %d", before, after)
			}
		})
	}
}

func TestHostChangedConvergenceNeedsARestartAfterEachFailure(t *testing.T) {
	t.Run("a missing lease set derives no basis", func(t *testing.T) {
		const workID = "host-convergence-missing-lease"
		s, owner := hostConvergenceSeedEscalated(t, workID, workerFailureObservationFixture())
		defer s.Close()
		hostConvergencePinOccupancy(t, s, workID, owner.SessionRef)
		hostConvergencePinWallStart(t, hostConvergenceFailureAt.Add(time.Minute+time.Second))
		pin := issue1013Pin(t, s, workID)
		if err := hostConvergenceDispatch(t, s, workID, "attempt:"+workID+":4", owner, pin, context.Background(), "host-convergence-missing-lease-4"); !hasFailureKind(err, KindMissingEvidence) {
			t.Fatalf("dispatch without a lease observation = %v, want missing_evidence", err)
		}
	})

	t.Run("a lease set without the recorded process derives no basis", func(t *testing.T) {
		const workID = "host-convergence-foreign-lease"
		s, owner := hostConvergenceSeedEscalated(t, workID, workerFailureObservationFixture())
		defer s.Close()
		pid, _ := hostConvergencePinOccupancy(t, s, workID, owner.SessionRef)
		ctx := hostConvergenceLeaseContext(context.Background(), pid+100000)
		hostConvergencePinWallStart(t, hostConvergenceFailureAt.Add(time.Minute+time.Second))
		pin := issue1013Pin(t, s, workID)
		if err := hostConvergenceDispatch(t, s, workID, "attempt:"+workID+":4", owner, pin, ctx, "host-convergence-foreign-lease-4"); !hasFailureKind(err, KindMissingEvidence) {
			t.Fatalf("dispatch behind a foreign lease set = %v, want missing_evidence", err)
		}
	})

	t.Run("a stale recorded process identity derives no basis", func(t *testing.T) {
		const workID = "host-convergence-dead-process"
		s, owner := hostConvergenceSeedEscalated(t, workID, workerFailureObservationFixture())
		defer s.Close()
		pid, start := hostConvergencePinOccupancy(t, s, workID, owner.SessionRef)
		if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1); UPDATE worktree_occupancy SET host_pid_start=host_pid_start+1 WHERE session_ref=?; DELETE FROM fold_guard`, owner.SessionRef); err != nil {
			t.Fatalf("corrupt the recorded process start: %v", err)
		}
		ctx := WithHostLeaseSet(context.Background(), HostLeaseSet{Leases: []hostlease.Lease{{PID: pid, PidStart: start + 1}}})
		hostConvergencePinWallStart(t, hostConvergenceFailureAt.Add(time.Minute+time.Second))
		pin := issue1013Pin(t, s, workID)
		if err := hostConvergenceDispatch(t, s, workID, "attempt:"+workID+":4", owner, pin, ctx, "host-convergence-dead-process-4"); !hasFailureKind(err, KindMissingEvidence) {
			t.Fatalf("dispatch with matching recorded and leased identity but a different live start = %v, want missing_evidence", err)
		}
	})

	t.Run("neither the same host nor another restart buys a second attempt", func(t *testing.T) {
		const workID = "host-convergence-second-attempt"
		s, owner := hostConvergenceSeedEscalated(t, workID, workerFailureObservationFixture())
		defer s.Close()
		hostConvergencePinOccupancy(t, s, workID, owner.SessionRef)
		ctx := hostConvergenceLeaseContext(context.Background(), os.Getpid())
		hostConvergencePinWallStart(t, hostConvergenceFailureAt.Add(time.Minute+time.Second))

		fourth := "attempt:" + workID + ":4"
		pin := issue1013Pin(t, s, workID)
		if err := hostConvergenceDispatch(t, s, workID, fourth, owner, pin, ctx, "host-convergence-second-4"); err != nil {
			t.Fatalf("the first restarted-host dispatch: %v", err)
		}
		issue1013RecordWorkerDispatch(t, s, workID, fourth)

		// The admitted attempt fails the same way and re-arms the wall on
		// the fourth counted attempt.
		var epoch int64
		if err := s.DatabaseForTesting().QueryRow(`SELECT json_extract(payload,'$.attempt_epoch') FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.action_id')='dispatch_worker' ORDER BY seq DESC LIMIT 1`, workID, WorkflowActionStarted).Scan(&epoch); err != nil {
			t.Fatal(err)
		}
		secondFailure := hostConvergenceFailureAt.Add(5 * time.Minute)
		event := hostConvergenceFailureEvent(workID, fourth, "failed-host-second-"+workID, workerFailureObservationFixture(), secondFailure)
		if err := s2WorkerFailedLive(t, s, event); err != nil {
			t.Fatalf("the second attributed host terminal ingest refused: %v", err)
		}
		applyRecordWorkerFailureForTest(t, s, workID, owner, fourth, epoch, readWorkVersion(t, s, workID), "host-convergence-second-record-4")

		// The spent host basis refuses even before another restart.
		pin = issue1013Pin(t, s, workID)
		if err := hostConvergenceDispatch(t, s, workID, "attempt:"+workID+":5", owner, pin, ctx, "host-convergence-second-5-stale"); !hasFailureKind(err, KindMissingEvidence) {
			t.Fatalf("dispatch after spending the host basis = %v, want missing_evidence", err)
		}

		// A second restart after the new failure is still one host_changed
		// dispatch per worker-satisfaction window short: the window only
		// a satisfying worker acceptance closes is spent.
		hostConvergencePinWallStart(t, secondFailure.Add(time.Minute+time.Second))
		pin = issue1013Pin(t, s, workID)
		if err := hostConvergenceDispatch(t, s, workID, "attempt:"+workID+":5", owner, pin, ctx, "host-convergence-second-5-restart"); !hasFailureKind(err, KindMissingEvidence) {
			t.Fatalf("dispatch behind a second restart = %v, want missing_evidence", err)
		}
	})
}

package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// Terminal frontier attribution. The dispatch_worker
// authorization records the admitted work-context view's source_event_frontier
// — explicit zero when the view is absent, never an absent field on a new
// authorization — and the live terminal ingest derives
// consumed_context_frontier from that recorded authorization alone, never
// from the current context, model text, or a caller-supplied value. A
// historical authorization without a recorded frontier keeps explicit
// absence on the terminal it produces.

// laneForID returns the registered implement lane definition.
func laneForID(t *testing.T) LaneDefinition {
	t.Helper()
	const id = "implement"
	for _, definition := range BuiltinLaneDefinitions() {
		if definition.ID == id {
			return definition
		}
	}
	t.Fatalf("no registered lane %q", id)
	return LaneDefinition{}
}

// workerTerminalEventThroughSeam appends one terminal worker evidence event
// through the live ingest seam, exactly as the authenticated CLI boundary
// lands it.
func workerTerminalEventThroughSeam(t *testing.T, s *Store, event Event) {
	t.Helper()
	workerTerminalEventThroughSeamCtx(t, context.Background(), s, event)
}

func workerTerminalEventThroughSeamCtx(t *testing.T, ctx context.Context, s *Store, event Event) {
	t.Helper()
	if event.Kind == WorkerCompleted {
		event = workerCompletionAtClaim(t, s, event)
		event = workerCompletionWithReportedSize(t, ctx, s, event)
	}
	if err := s.Transact(ctx, func(tx *Transaction) error {
		_, err := ApplyWorkerEvidenceOperationTx(ctx, tx, event)
		return err
	}); err != nil {
		t.Fatalf("terminal ingest refused %s: %v", event.EventID, err)
	}
}

// unobservedTreeForTest is the synthetic delivered tree a completion built
// for the direct fold route reports: that route seals no observation, so
// the tree binds nothing there.
var unobservedTreeForTest = strings.Repeat("e", 40)

// workerCompletionWithReportedSize gives a completion routed through the
// sealed-observation seam the production_size a compliant worker reports,
// bound to the sealed observed tree. A builder's synthetic unobserved tree
// is rebound to the sealed one; an authored tree stays as written.
func workerCompletionWithReportedSize(t *testing.T, ctx context.Context, s *Store, event Event) Event {
	t.Helper()
	// The retired production_size member records nothing (CD-0242 D6); the
	// seam keeps its call site and carries no member.
	return event
}

// withReportedSizeForTest gives a capped implementation job's completion the
// production_size a compliant worker reports: the job's recorded base and
// the named delivered tree. A completion that already carries the member,
// another lane, or an uncapped job passes unchanged, so binding tests keep
// their authored bytes.
func withReportedSizeForTest(t *testing.T, s *Store, event Event, tree string) Event {
	t.Helper()
	// The retired production_size member records nothing (CD-0242 D6).
	return event
}

func workerCompletionAtClaim(t *testing.T, s *Store, event Event) Event {
	t.Helper()
	var payload WorkerCompletedPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.WorkerDirectory == "" {
		payload.WorkerDirectory = workerFixtureDirectory(t, s, event.SubjectID)
	}
	event.Payload = mustJSONValue(payload)
	return event
}

// The corpus cannot state the hash of a runtime claim path. An omitted identity
// cannot express a legacy authorization; Go tests own that case.
func workerDispatchAuthorizationAtClaim(t *testing.T, s *Store, event Event) Event {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	payload["worker_worktree_identity"] = workerFixtureWorktreeIdentity(t, s, event.SubjectID)
	event.Payload = mustJSONValue(payload)
	t.Logf("materialized dispatch worktree identity for %s", event.EventID)
	return event
}

func workerFixtureWorktreeIdentity(t *testing.T, s *Store, workID string) string {
	t.Helper()
	canonical, err := canonicalWorkerWorktreePath(workerFixtureDirectory(t, s, workID))
	if err != nil {
		t.Fatal(err)
	}
	return workerWorktreeIdentity(canonical)
}

func workerFixtureDirectory(t *testing.T, s *Store, workID string) string {
	t.Helper()
	var directory string
	err := s.DatabaseForTesting().QueryRow(`SELECT path FROM worktree_entries WHERE set_id=? AND state='active' ORDER BY project_id LIMIT 1`, WorktreeSetID(workID)).Scan(&directory)
	if errors.Is(err, sql.ErrNoRows) {
		directory = t.TempDir()
		insertWorkerWorktreeEntry(t, s, workID, directory)
	} else if err != nil {
		t.Fatal(err)
	}
	return directory
}

// stubbedDeliveryRunner answers the delivery probe without touching the
// filesystem: the frontier subtests attribute the authorization frontier,
// and the real Git behavior is the CLI tests' subject. The changed-path
// probe answers with no names, the clean zero-delta observation.
type stubbedDeliveryRunner struct{}

func (stubbedDeliveryRunner) Run(_ context.Context, _ string, args ...string) ([]byte, error) {
	switch {
	case len(args) > 0 && args[0] == "status":
		return nil, nil
	case slices.Contains(args, "--name-only"):
		return nil, nil
	}
	return []byte(strings.Repeat("a", 40)), nil
}

// prepareWorkerTerminalDelivery prepares the delivery proof for one terminal
// event the way the authenticated CLI ingress does, and returns the context
// that carries it into the seam.
func prepareWorkerTerminalDelivery(t *testing.T, s *Store, workID, attemptID, eventID string) context.Context {
	t.Helper()
	preparation, err := PrepareWorkerTerminalDelivery(context.Background(), s, PrepareWorkerTerminalDeliveryRequest{WorkID: workID, AttemptID: attemptID, EventID: eventID, Runner: stubbedDeliveryRunner{}})
	if err != nil {
		t.Fatalf("terminal delivery preparation refused: %v", err)
	}
	return WithWorkerTerminalPreparation(context.Background(), preparation)
}

// seedGitClaimRepo materializes the claimed worktree as one real git
// repository with a committed seed file and records its active claim, so the
// terminal delivery observation probes a reachable worktree.
func seedGitClaimRepo(t *testing.T, s *Store, workID string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "claim-"+workID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...) //nolint:gosec // test-only git argv, fixed binary
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			t.Fatalf("seed claim repo %v: %v: %s", args, err, stderr.String())
		}
	}
	run("init", "-b", "main")
	run("config", "user.email", "concord@example.invalid")
	run("config", "user.name", "Concord Terminal Observation Test")
	if err := os.WriteFile(filepath.Join(dir, "seed.txt"), []byte("claimed worktree seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "-A")
	run("commit", "--allow-empty", "-m", "claimed worktree seed")
	insertWorkerWorktreeEntry(t, s, workID, dir)
}

// rawTerminalFrontier reads one event's consumed_context_frontier straight
// from the persisted payload bytes.
func rawTerminalFrontier(t *testing.T, s *Store, eventID string) (int64, bool) {
	t.Helper()
	var frontier sql.NullInt64
	if err := s.DatabaseForTesting().QueryRow(`SELECT json_extract(payload,'$.consumed_context_frontier') FROM domain_events WHERE event_id=?`, eventID).Scan(&frontier); err != nil {
		t.Fatalf("read terminal event %s: %v", eventID, err)
	}
	return frontier.Int64, frontier.Valid
}

// dispatchCompletionFrontier reads the recorded worker_context_frontier of
// the dispatch_worker completion bound to the attempt.
func dispatchCompletionFrontier(t *testing.T, s *Store, workID, attemptID string) (int64, bool) {
	t.Helper()
	var frontier sql.NullInt64
	if err := s.DatabaseForTesting().QueryRow(`SELECT json_extract(payload,'$.worker_context_frontier') FROM domain_events WHERE subject_type='work_item' AND subject_id=? AND kind=? AND json_extract(payload,'$.action_id')='dispatch_worker' AND json_extract(payload,'$.worker_attempt_id')=? ORDER BY seq DESC LIMIT 1`, workID, WorkflowActionCompleted, attemptID).Scan(&frontier); err != nil {
		t.Fatalf("read dispatch completion frontier: %v", err)
	}
	return frontier.Int64, frontier.Valid
}

// currentSourceFrontier reads the view frontier the tx-scoped reader computes.
func currentSourceFrontier(t *testing.T, s *Store, workID string) int64 {
	t.Helper()
	view, err := readWorkContextView(context.Background(), s.DatabaseForTesting(), workID)
	if err != nil {
		t.Fatalf("read work context view: %v", err)
	}
	if view == nil {
		return 0
	}
	return view.SourceEventFrontier
}

// implementDispatchEvidenceEvent builds the implement-lane dispatch evidence
// the authorized attempt promotes, carrying the job binding the
// authorization bound at the current dispatch payload version.
func implementDispatchEvidenceEvent(workID, attemptID string, lane LaneDefinition, job WorkerJobBinding) Event {
	payload := WorkerDispatchedPayload{
		AttemptID: attemptID, LaneID: lane.ID, LaneVersion: lane.Version, LaneDigest: lane.Digest,
		CapabilityClass: lane.CapabilityClass, ReadbackModel: preferredModelForLane(lane),
		PacketSchemaVersion: WorkerPacketSchemaVersion, ReportSchemaVersion: WorkerReportSchemaVersion,
		PacketDigest: "sha256:" + strings.Repeat("f", 64), WorkerJob: &job,
	}
	return Event{EventID: attemptID, Kind: WorkerDispatched, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "worker:test", OccurredAt: time.Unix(1, 0).UTC(), PayloadVersion: WorkerEvidenceEventPayloadVersion(WorkerDispatched), Payload: mustJSONValue(payload)}
}

func TestWorkerTerminalConsumedFrontier(t *testing.T) {
	t.Run("the terminal carries the dispatch-time frontier, never the current one", func(t *testing.T) {
		fixture := seedWorkContextFixture(t, "ctx-frontier-f")
		defer fixture.store.Close()
		s := fixture.store
		declareSampleWorkContext(t, fixture, "the first declaration anchors frontier F")
		job := seedReadyWorkerJob(t, fixture)
		view, viewErr := readWorkContextView(context.Background(), s.DatabaseForTesting(), fixture.workID)
		if viewErr != nil || view == nil {
			t.Fatalf("admitted view = %+v err = %v, want a present view", view, viewErr)
		}
		frontierF := view.SourceEventFrontier
		attemptID := "attempt-frontier-f"
		packet := workContextDispatchPacket(t, fixture, attemptID, job, view)
		if err := dispatchWorkContextAttempt(t, fixture, packet); err != nil {
			t.Fatalf("dispatch refused: %v", err)
		}
		gotF, present := dispatchCompletionFrontier(t, s, fixture.workID, attemptID)
		if !present {
			t.Fatal("the dispatch authorization recorded no worker_context_frontier; a new authorization must carry the admitted view's frontier explicitly")
		}
		if gotF != frontierF {
			t.Fatalf("dispatch authorization frontier = %d, want the admitted view frontier %d", gotF, frontierF)
		}
		declareSampleWorkContext(t, fixture, "the second declaration raises the current frontier G")
		frontierG := currentSourceFrontier(t, s, fixture.workID)
		if frontierG <= frontierF {
			t.Fatalf("fixture frontier did not advance: F=%d G=%d", frontierF, frontierG)
		}
		lane := laneForID(t)
		if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{implementDispatchEvidenceEvent(fixture.workID, attemptID, lane, job)}}); err != nil {
			t.Fatal(err)
		}
		evidence := make([]WorkerReportEvidence, 0, len(lane.EvidenceObligations))
		for _, obligation := range lane.EvidenceObligations {
			evidence = append(evidence, WorkerReportEvidence{Obligation: obligation, Detail: "discharged " + obligation})
		}
		boundJob := job
		completion := Event{EventID: "complete-frontier-f", Kind: WorkerCompleted, SubjectType: SubjectWorkItem, SubjectID: fixture.workID, Actor: "worker:test", OccurredAt: time.Unix(2, 0).UTC(), PayloadVersion: WorkerEvidenceEventPayloadVersion(WorkerCompleted), Payload: mustJSONValue(WorkerCompletedPayload{
			AttemptID: attemptID, ReadbackModel: preferredModelForLane(lane), ReportSchemaVersion: WorkerReportSchemaVersion,
			EvidenceOrigin: WorkerEvidenceReported, Evidence: evidence, WorkerJob: &boundJob,
		})}
		workerTerminalEventThroughSeamCtx(t, prepareWorkerTerminalDelivery(t, s, fixture.workID, attemptID, completion.EventID), s, completion)
		consumed, consumedPresent := rawTerminalFrontier(t, s, "complete-frontier-f")
		if !consumedPresent {
			t.Fatal("the terminal carries no consumed_context_frontier; the ingest must derive it from the recorded authorization")
		}
		if consumed != frontierF {
			t.Fatalf("terminal consumed_context_frontier = %d, want the dispatch-time frontier %d, never the current frontier %d", consumed, frontierF, frontierG)
		}
	})

	t.Run("an absent view records explicit zero and the terminal carries zero", func(t *testing.T) {
		s := openTemp(t)
		defer s.Close()
		workID := "work-frontier-zero"
		seedGitClaimRepo(t, s, workID)
		seed := seedDispatchFixture(t, s, workID)
		attemptID := "attempt-frontier-zero"
		packet := dispatchWorkerPacket(t, s, workID, "execution", attemptID)
		if err := dispatchBindingAttempt(t, s, seed, attemptID, packet); err != nil {
			t.Fatalf("dispatch without a context view refused: %v", err)
		}
		if view := currentSourceFrontier(t, s, workID); view != 0 {
			t.Fatalf("fixture view frontier = %d, want the absent-view zero", view)
		}
		gotZero, present := dispatchCompletionFrontier(t, s, workID, attemptID)
		if !present {
			t.Fatal("a new authorization over an absent view recorded no worker_context_frontier; absence must never masquerade as history on a new authorization")
		}
		if gotZero != 0 {
			t.Fatalf("dispatch authorization frontier = %d, want explicit zero for an absent view", gotZero)
		}
		lane := laneForID(t)
		if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{workerDispatchEvent(workID, attemptID, lane, nil)}}); err != nil {
			t.Fatal(err)
		}
		completion := workerCompletedContextFindingsEvent(workID, "complete-frontier-zero", attemptID, preferredModelForLane(lane), lane, nil, WorkerEvidenceEventPayloadVersion(WorkerCompleted), workerFixtureDirectory(t, s, workID))
		workerTerminalEventThroughSeamCtx(t, prepareWorkerTerminalDelivery(t, s, workID, attemptID, completion.EventID), s, completion)
		consumed, consumedPresent := rawTerminalFrontier(t, s, "complete-frontier-zero")
		if !consumedPresent {
			t.Fatal("the terminal over an absent view carries no consumed_context_frontier; the recorded zero authorization must derive an explicit zero")
		}
		if consumed != 0 {
			t.Fatalf("terminal consumed_context_frontier = %d, want 0", consumed)
		}
	})

	t.Run("a failed terminal without findings carries the same frontier", func(t *testing.T) {
		// The frontier joins the dispatch authorization alone, so this
		// subtest pins the latest head that predates the attribution
		// requirement and records the failed terminal without an observation.
		breakFix, ok := BuiltinWorkflowRegistry().Current("workflow.break_fix")
		if !ok {
			t.Fatal("workflow.break_fix v27 is not registered")
		}
		fixture := seedWorkContextFixtureWithDefinition(t, "ctx-frontier-fail", breakFix)
		defer fixture.store.Close()
		s := fixture.store
		declareSampleWorkContext(t, fixture, "the declaration anchors the failed frontier F")
		job := seedReadyWorkerJob(t, fixture)
		view, viewErr := readWorkContextView(context.Background(), s.DatabaseForTesting(), fixture.workID)
		if viewErr != nil || view == nil {
			t.Fatalf("admitted view = %+v err = %v, want a present view", view, viewErr)
		}
		frontierF := view.SourceEventFrontier
		attemptID := "attempt-frontier-fail"
		packet := workContextDispatchPacket(t, fixture, attemptID, job, view)
		if err := dispatchWorkContextAttempt(t, fixture, packet); err != nil {
			t.Fatalf("dispatch refused: %v", err)
		}
		if _, present := dispatchCompletionFrontier(t, s, fixture.workID, attemptID); !present {
			t.Fatal("the dispatch authorization recorded no worker_context_frontier")
		}
		declareSampleWorkContext(t, fixture, "the later declaration must not reach the failed terminal")
		lane := laneForID(t)
		if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{implementDispatchEvidenceEvent(fixture.workID, attemptID, lane, job)}}); err != nil {
			t.Fatal(err)
		}
		failure := workerFailedContextFindingsEvent(fixture.workID, "failed-frontier-f", attemptID, preferredModelForLane(lane), WorkerFailureWorkerError, nil, WorkerEvidenceEventPayloadVersion(WorkerFailed))
		workerTerminalEventThroughSeamCtx(t, prepareWorkerTerminalDelivery(t, s, fixture.workID, attemptID, failure.EventID), s, failure)
		consumed, consumedPresent := rawTerminalFrontier(t, s, "failed-frontier-f")
		if !consumedPresent {
			t.Fatal("the failed terminal carries no consumed_context_frontier; a failure without findings still owes its authorization frontier")
		}
		if consumed != frontierF {
			t.Fatalf("failed terminal consumed_context_frontier = %d, want the dispatch-time frontier %d", consumed, frontierF)
		}
	})

	t.Run("a historical authorization without a recorded frontier stays absent", func(t *testing.T) {
		fixture := seedWorkContextFixture(t, "ctx-frontier-historical")
		defer fixture.store.Close()
		s := fixture.store
		attemptID := "attempt-frontier-historical"
		insertWorkerWorktreeEntry(t, s, fixture.workID, t.TempDir())
		seedHistoricalDispatchWindowRaw(t, s, fixture, attemptID)
		lane := BuiltinLaneDefinitions()[0]
		if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{workerDispatchEvent(fixture.workID, attemptID, lane, nil)}}); err != nil {
			t.Fatal(err)
		}
		completion := workerCompletedContextFindingsEvent(fixture.workID, "complete-frontier-historical", attemptID, preferredModelForLane(lane), lane, nil, WorkerEvidenceEventPayloadVersion(WorkerCompleted), workerFixtureDirectory(t, s, fixture.workID))
		workerTerminalEventThroughSeam(t, s, completion)
		if _, present := dispatchCompletionFrontier(t, s, fixture.workID, attemptID); present {
			t.Fatal("a historical authorization carries a recorded frontier; raw history must stay absent")
		}
		consumed, consumedPresent := rawTerminalFrontier(t, s, "complete-frontier-historical")
		if consumedPresent {
			t.Fatalf("terminal over a historical authorization derived consumed_context_frontier %d; absence must stay explicit absence, never a fabricated zero", consumed)
		}
	})
}

// TestWorkerTerminalClaimStateDriftRefusesAppend proves the claim
// revalidation is durable-state sensitive without any filesystem work: a
// claim whose row flips state between the delivery probe and the append
// refuses the completion even though its path, branch, and claim operation
// are unchanged.
func TestWorkerTerminalClaimStateDriftRefusesAppend(t *testing.T) {
	s := openTemp(t)
	defer s.Close()
	workID := "work-claim-state-drift"
	seedGitClaimRepo(t, s, workID)
	seed := seedDispatchFixture(t, s, workID)
	attemptID := "attempt-claim-state-drift"
	packet := dispatchWorkerPacket(t, s, workID, "execution", attemptID)
	if err := dispatchBindingAttempt(t, s, seed, attemptID, packet); err != nil {
		t.Fatalf("dispatch refused: %v", err)
	}
	lane := laneForID(t)
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{workerDispatchEvent(workID, attemptID, lane, nil)}}); err != nil {
		t.Fatal(err)
	}
	completion := workerCompletedContextFindingsEvent(workID, "complete-claim-state-drift", attemptID, preferredModelForLane(lane), lane, nil, WorkerEvidenceEventPayloadVersion(WorkerCompleted), workerFixtureDirectory(t, s, workID))
	prepareCtx := prepareWorkerTerminalDelivery(t, s, workID, attemptID, completion.EventID)
	if _, err := s.DatabaseForTesting().Exec(`UPDATE worktree_entries SET state='reclaimed' WHERE set_id=? AND project_id='project-1'`, WorktreeSetID(workID)); err != nil {
		t.Fatal(err)
	}
	err := s.Transact(prepareCtx, func(tx *Transaction) error {
		_, txErr := ApplyWorkerEvidenceOperationTx(prepareCtx, tx, completion)
		return txErr
	})
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindProjectionConflict {
		t.Fatalf("claim state flip after the probe error = %v, want %s", err, KindProjectionConflict)
	}
	var terminals int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_type='work_item' AND subject_id=? AND kind IN (?,?) AND json_extract(payload,'$.attempt_id')=?`, workID, WorkerCompleted, WorkerFailed, attemptID).Scan(&terminals); err != nil {
		t.Fatal(err)
	}
	if terminals != 0 {
		t.Fatalf("the state-flip refusal still recorded %d terminal event(s)", terminals)
	}
}

// seedHistoricalDispatchWindowRaw inserts the dispatch authorization pair a
// binary before the frontier feature recorded: a dispatch_worker start and
// completion with the digest and attempt identity, and no
// worker_context_frontier field at all. The rows land through one fold-guard
// session exactly as recorded history reaches the store.
func seedHistoricalDispatchWindowRaw(t *testing.T, s *Store, fixture workContextFixture, attemptID string) {
	t.Helper()
	ctx := context.Background()
	actorRef, err := WorkflowActorRef(fixture.owner)
	if err != nil {
		t.Fatal(err)
	}
	stepID := "repair"
	expected := int64(2)
	canonical, err := canonicalWorkerWorktreePath(workerFixtureDirectory(t, s, fixture.workID))
	if err != nil {
		t.Fatal(err)
	}
	startPayload := mustJSONValue(map[string]any{
		"work_id": fixture.workID, "expected_version": expected, "resulting_version": expected + 1,
		"step_id": stepID, "action_id": "dispatch_worker", "attempt_epoch": 1,
		"accepted_inputs_digest": "sha256:" + strings.Repeat("a", 64),
		"idempotency_identity":   "historical-open:" + attemptID, "actor_ref": actorRef,
	})
	completionPayload := mustJSONValue(map[string]any{
		"work_id": fixture.workID, "expected_version": expected + 1, "resulting_version": expected + 2,
		"step_id": stepID, "action_id": "dispatch_worker", "attempt_epoch": 1,
		"result_evidence_refs": []string{}, "changed_refs": []string{fixture.workID}, "actor_ref": actorRef,
		"worker_attempt_id":           attemptID,
		"worker_packet_digest":        "sha256:" + strings.Repeat("c", 64),
		"worker_packet_predicate_ids": []string{},
		"worker_worktree_identity":    workerWorktreeIdentity(canonical),
	})
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := enterFold(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO domain_events(event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload) VALUES(?, 'workflow.action_started', 'work_item', ?, ?, ?, 1, ?)`, "historical-start-"+attemptID, fixture.workID, actorRef, time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), string(startPayload)); err != nil {
		t.Fatalf("seed historical start: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO domain_events(event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload) VALUES(?, 'workflow.action_completed', 'work_item', ?, ?, ?, 1, ?)`, "historical-completed-"+attemptID, fixture.workID, actorRef, time.Date(2026, 10, 1, 0, 1, 0, 0, time.UTC), string(completionPayload)); err != nil {
		t.Fatalf("seed historical completion: %v", err)
	}
	if err := leaveFold(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var frontiers int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE event_id=? AND json_extract(payload,'$.worker_context_frontier') IS NOT NULL`, "historical-completed-"+attemptID).Scan(&frontiers); err != nil {
		t.Fatal(err)
	}
	if frontiers != 0 {
		t.Fatal("the historical fixture carries a frontier; it must not")
	}
}

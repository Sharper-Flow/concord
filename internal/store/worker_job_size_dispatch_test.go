package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// sizeDispatchFixture seeds one work item on the family's current registered
// head with the qualified oracle subject the shared recording helpers need.
// The generic return-route helper pins the legacy approval policy, whose
// catalog does not declare the size members.
func sizeDispatchFixture(t *testing.T, workID string) workflowReturnRouteFixture {
	t.Helper()
	registered, err := BuiltinWorkflowDefinitionForRef("workflow.break_fix")
	if err != nil {
		t.Fatal(err)
	}
	fixture := seedWorkflowReturnRouteFixtureWithDefinition(t, workID, registered, "repair",
		[]string{"verification"}, []string{"verification", "review", "artifact"})
	bootstrapOracleFixtureSubject(t, workerJobSizeStore(fixture), workID)
	return fixture
}

// recordSizeDispatchAttempt records one implementation-lane dispatch of the
// named job — its dispatch_worker authorization, the worker.dispatched
// evidence through the authoritative lane-actor route, its completion, and
// the rejection that disposes the completed result — so the job sits in
// correction: an implementation attempt completed since its last
// satisfaction.
func recordSizeDispatchAttempt(t *testing.T, fixture workflowReturnRouteFixture, workID, attemptID string, job WorkerJobBinding) {
	t.Helper()
	s := workerJobSizeStore(fixture)
	recordSizeDispatchAuthorization(t, s, workID, attemptID, job)
	if err := appendJobBoundWorkerDispatch(t, s, workID, attemptID, &job); err != nil {
		t.Fatalf("record implementation dispatch %s: %v", attemptID, err)
	}
	if err := completeJobBoundAttemptForTest(s, workID, attemptID, reviewGateLane(t, "implementation"), &job); err != nil {
		t.Fatalf("complete implementation attempt %s: %v", attemptID, err)
	}
	version := readWorkVersion(t, s, workID)
	rejection := map[string]any{
		"work_id": workID, "expected_version": version, "resulting_version": version + 1,
		"action_id": "reject_worker_result", "step_id": "repair", "attempt_id": attemptID, "worker_attempt_id": attemptID,
		"diagnosis": "the delivered subject does not satisfy the approved predicate",
		"strategy":  "retry the bounded job from the recorded base",
	}
	raw, err := json.Marshal(rejection)
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
		workID+":size-rejection:"+attemptID, workID, "principal/operator", time.Unix(22, 0).UTC().Format(time.RFC3339Nano), string(raw)); err != nil {
		t.Fatalf("record the rejection of %s: %v", attemptID, err)
	}
	if err := leaveFold(context.Background(), tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

// recordSizeDispatchAuthorizationClass records the dispatch_worker
// completion of the named capability class, so a review or verification
// attempt can bind the same capped job the implementation lane binds.
func recordSizeDispatchAuthorizationClass(t *testing.T, s *Store, workID, attemptID string, job WorkerJobBinding, class string) {
	t.Helper()
	version := readWorkVersion(t, s, workID)
	// The authorization binds the active claimed worktree, the Project the
	// completion route resolves from the dispatch.
	var directory string
	if err := s.DatabaseForTesting().QueryRow(`SELECT path FROM worktree_entries WHERE set_id=? AND state='active' ORDER BY project_id LIMIT 1`, WorktreeSetID(workID)).Scan(&directory); err != nil {
		t.Fatalf("read the claimed worktree of %s: %v", workID, err)
	}
	authorization := map[string]any{
		"work_id": workID, "expected_version": version, "resulting_version": version + 1,
		"action_id": "dispatch_worker", "step_id": "repair", "attempt_epoch": 1, "worker_attempt_id": attemptID,
		"worker_capability_class":  class,
		"worker_job":               map[string]any{"job_id": job.JobID, "revision": job.Revision, "digest": job.Digest},
		"worker_worktree_identity": workerWorktreeIdentity(filepath.Clean(directory)),
	}
	raw, err := json.Marshal(authorization)
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
	if _, err := tx.ExecContext(context.Background(), `INSERT INTO domain_events(event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload) VALUES(?, 'workflow.action_started', 'work_item', ?, ?, ?, 2, ?), (?, 'workflow.action_completed', 'work_item', ?, ?, ?, 2, ?)`,
		workID+":size-started:"+attemptID, workID, "principal/operator", time.Unix(20, 0).UTC().Format(time.RFC3339Nano),
		mustJSONValue(map[string]any{"work_id": workID, "action_id": "dispatch_worker", "step_id": "repair", "attempt_epoch": 1}),
		workID+":size-authorization:"+attemptID, workID, "principal/operator", time.Unix(20, 0).UTC().Format(time.RFC3339Nano), string(raw)); err != nil {
		t.Fatalf("record the dispatch authorization of %s: %v", attemptID, err)
	}
	if err := leaveFold(context.Background(), tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

// recordSizeDispatchAuthorization records the dispatch_worker completion the
// worker.dispatched evidence must match, under the recorded-history route
// the review-subject fixture uses for folded accept completions.
func recordSizeDispatchAuthorization(t *testing.T, s *Store, workID, attemptID string, job WorkerJobBinding) {
	recordSizeDispatchAuthorizationClass(t, s, workID, attemptID, job, "implementation")
}

// recordEditingJobForTest records one ready editing job from the caller's
// fields and fills the binding from the recorded revision.
func recordEditingJobForTest(t *testing.T, fixture workflowReturnRouteFixture, workID string, fields map[string]any, job *WorkerJobBinding) {
	t.Helper()
	s := workerJobSizeStore(fixture)
	fields["job_id"] = job.JobID
	if err := recordWorkerJobActionForTest(t, s, workID, fixture.owner, fields); err != nil {
		t.Fatalf("record worker-job %s: %v", job.JobID, err)
	}
	views, err := readWorkerJobRevisions(context.Background(), s.db, workID)
	if err != nil {
		t.Fatal(err)
	}
	for _, view := range views {
		if view.Binding.JobID == job.JobID {
			*job = view.Binding
		}
	}
}

// sizeDispatchPacketJob builds the packet worker_job content a coordinator
// sends: the recorded revision's projected content with the caller's
// caller_exclusions overlay applied.
func sizeDispatchPacketJob(t *testing.T, s *Store, workID string, job WorkerJobBinding, exclusions []string) map[string]any {
	t.Helper()
	// The raw projection without exclusions: the caller's overlay decides,
	// so each case controls the member exactly.
	content := recordedPacketJobForTest(t, s, workID, job, "review")
	if exclusions != nil {
		content["caller_exclusions"] = exclusions
	}
	return content
}

// The recorded size members project onto every admitted lane's packet, and
// the dispatch admission holds the claimed worker_job to the exact projected
// content: no exclusions on a non-implementation lane, and no drift.
func TestWorkerJobSizePacketProjectsSizeContent(t *testing.T) {
	const workID = "worker-job-size-packet-projection"
	fixture := sizeDispatchFixture(t, workID)
	s := workerJobSizeStore(fixture)
	definition := mustBuiltinDefinition(t, "workflow.break_fix").Definition
	job := &WorkerJobBinding{JobID: "job:projected", Revision: 1}
	recordEditingJobForTest(t, fixture, workID, workerJobSizeEditingFields(t, workID, fixture), job)
	views, err := readWorkerJobRevisions(context.Background(), s.db, workID)
	if err != nil || len(views) != 1 {
		t.Fatalf("recorded views = %#v, error = %v", views, err)
	}
	view := views[0]
	if view.ProductionSizeMax == nil || view.MeasurementBasis == nil || view.DirectCallerAllowance == nil {
		t.Fatalf("the recorded view carries no size members: %#v", view)
	}
	projected := packetJobFromView(view)
	if projected.ProductionSizeMax == nil || projected.MeasurementBasis == nil || projected.DirectCallerAllowance == nil || projected.CallerExclusions != nil {
		t.Fatalf("the packet projection carries the wrong size content: %#v", projected)
	}
	t.Run("implementation lane admits the exact projection", func(t *testing.T) {
		lane := reviewGateLane(t, "implementation")
		packet := joinPacketFor(t, s, workID, "repair", "attempt-projected", lane.ID, lane.Version, lane.Digest)
		packet["inputs"].(map[string]any)["worker_job"] = sizeDispatchPacketJob(t, s, workID, *job, nil)
		if _, _, err := validateWorkerPacketJob(context.Background(), s.db, definition, workID, lane, mustJSONValue(packet)); err != nil {
			t.Fatalf("implementation packet refused: %v", err)
		}
	})
	t.Run("review lane admits the same content without exclusions", func(t *testing.T) {
		lane := reviewGateLane(t, "review")
		packet := joinPacketFor(t, s, workID, "repair", "attempt-projected-review", lane.ID, lane.Version, lane.Digest)
		packet["inputs"].(map[string]any)["worker_job"] = sizeDispatchPacketJob(t, s, workID, *job, nil)
		if _, _, err := validateWorkerPacketJob(context.Background(), s.db, definition, workID, lane, mustJSONValue(packet)); err != nil {
			t.Fatalf("review packet refused: %v", err)
		}
	})
	t.Run("review lane refuses caller_exclusions", func(t *testing.T) {
		lane := reviewGateLane(t, "review")
		packet := joinPacketFor(t, s, workID, "repair", "attempt-projected-review-excl", lane.ID, lane.Version, lane.Digest)
		packet["inputs"].(map[string]any)["worker_job"] = sizeDispatchPacketJob(t, s, workID, *job, []string{"internal/store/worker_jobs.go"})
		_, _, err := validateWorkerPacketJob(context.Background(), s.db, definition, workID, lane, mustJSONValue(packet))
		if err == nil || !strings.Contains(err.Error(), "caller_exclusions") {
			t.Fatalf("review exclusions error = %v, want the implementation-only refusal", err)
		}
	})
	t.Run("drifted size content refuses", func(t *testing.T) {
		lane := reviewGateLane(t, "implementation")
		packet := joinPacketFor(t, s, workID, "repair", "attempt-projected-drift", lane.ID, lane.Version, lane.Digest)
		content := sizeDispatchPacketJob(t, s, workID, *job, nil)
		content["production_size_max"] = 999999
		packet["inputs"].(map[string]any)["worker_job"] = content
		_, _, err := validateWorkerPacketJob(context.Background(), s.db, definition, workID, lane, mustJSONValue(packet))
		if err == nil || !strings.Contains(err.Error(), "content does not match the recorded revision") {
			t.Fatalf("drifted content error = %v, want the equality refusal", err)
		}
	})
	t.Run("read-only job projects no members", func(t *testing.T) {
		readOnly := &WorkerJobBinding{JobID: "job:projected-read-only", Revision: 1}
		recordEditingJobForTest(t, fixture, workID, workerJobSizeReadOnlyFields(t, workID, fixture), readOnly)
		lane := reviewGateLane(t, "implementation")
		packet := joinPacketFor(t, s, workID, "repair", "attempt-projected-readonly", lane.ID, lane.Version, lane.Digest)
		packet["inputs"].(map[string]any)["worker_job"] = sizeDispatchPacketJob(t, s, workID, *readOnly, nil)
		binding, _, err := validateWorkerPacketJob(context.Background(), s.db, definition, workID, lane, mustJSONValue(packet))
		if err != nil || binding == nil {
			t.Fatalf("read-only packet refused: %v", err)
		}
	})
}

// The caller exclusions are the measured paths of the work's other jobs that
// are in correction, computed by the core and bound to the implementation
// packet exactly: a missing, extra, or different set refuses.
func TestWorkerJobSizeCallerExclusionsBindThePacket(t *testing.T) {
	const workID = "worker-job-size-caller-exclusions"
	fixture := sizeDispatchFixture(t, workID)
	s := workerJobSizeStore(fixture)
	definition := mustBuiltinDefinition(t, "workflow.break_fix").Definition
	blocked := &WorkerJobBinding{JobID: "job:blocked", Revision: 1}
	recordEditingJobForTest(t, fixture, workID, workerJobSizeEditingFields(t, workID, fixture), blocked)
	recordSizeDispatchAttempt(t, fixture, workID, "attempt:blocked", *blocked)
	other := &WorkerJobBinding{JobID: "job:other", Revision: 1}
	recordEditingJobForTest(t, fixture, workID, workerJobSizeEditingFields(t, workID, fixture), other)
	lane := reviewGateLane(t, "implementation")
	exclusions := []string{"internal/store/worker_jobs.go"}
	t.Run("implementation packet carries the computed exclusions", func(t *testing.T) {
		packet := joinPacketFor(t, s, workID, "repair", "attempt-exclusions", lane.ID, lane.Version, lane.Digest)
		packet["inputs"].(map[string]any)["worker_job"] = sizeDispatchPacketJob(t, s, workID, *other, exclusions)
		if _, _, err := validateWorkerPacketJob(context.Background(), s.db, definition, workID, lane, mustJSONValue(packet)); err != nil {
			t.Fatalf("exclusions-bearing packet refused: %v", err)
		}
	})
	for _, refusal := range []struct {
		name      string
		exclusion []string
	}{
		{"missing exclusions", nil},
		{"foreign exclusions", []string{"adapter/opencode/packet.ts"}},
		{"extra exclusions", []string{"internal/store/worker_jobs.go", "adapter/opencode/packet.ts"}},
	} {
		t.Run(refusal.name+" refuse", func(t *testing.T) {
			packet := joinPacketFor(t, s, workID, "repair", "attempt-exclusions-"+strings.ReplaceAll(refusal.name, " ", "-"), lane.ID, lane.Version, lane.Digest)
			packet["inputs"].(map[string]any)["worker_job"] = sizeDispatchPacketJob(t, s, workID, *other, refusal.exclusion)
			_, _, err := validateWorkerPacketJob(context.Background(), s.db, definition, workID, lane, mustJSONValue(packet))
			if err == nil || !strings.Contains(err.Error(), "content does not match the recorded revision") {
				t.Fatalf("exclusions %q error = %v, want the equality refusal", refusal.name, err)
			}
		})
	}
	t.Run("continuity publishes the implementation exclusions", func(t *testing.T) {
		if got := continuityReadyJobForTest(t, s, workID, other.JobID).CallerExclusions; !slices.Equal(got, exclusions) {
			t.Fatalf("continuity ready job caller_exclusions = %q, want %q", got, exclusions)
		}
	})
	t.Run("review lane carries no exclusions", func(t *testing.T) {
		reviewLane := reviewGateLane(t, "review")
		packet := joinPacketFor(t, s, workID, "repair", "attempt-exclusions-review", reviewLane.ID, reviewLane.Version, reviewLane.Digest)
		packet["inputs"].(map[string]any)["worker_job"] = sizeDispatchPacketJob(t, s, workID, *other, nil)
		if _, _, err := validateWorkerPacketJob(context.Background(), s.db, definition, workID, reviewLane, mustJSONValue(packet)); err != nil {
			t.Fatalf("review packet without exclusions refused: %v", err)
		}
	})
}

// The retained v33 pin keeps its packet3 closure and admits an uncapped
// implementation job unchanged, while the size-capable heads anchor
// dispatch_worker at the packet5 closure.
func TestWorkerJobSizeHistoricalPacketUnchanged(t *testing.T) {
	t.Parallel()
	implementation := mustCompileWorkflowComposition(implementationCompositionV36())
	if got := dispatchPacketSchemaRef(t, implementation); got != "worker_packet5" {
		t.Fatalf("implementation size-capable dispatch packet schema ref = %q, want worker_packet5", got)
	}
	breakFix := mustCompileWorkflowComposition(breakFixCompositionV33())
	if got := dispatchPacketSchemaRef(t, breakFix); got != "worker_packet5" {
		t.Fatalf("break_fix size-capable dispatch packet schema ref = %q, want worker_packet5", got)
	}
	for _, definition := range []WorkflowDefinition{mustCompileWorkflowComposition(implementationCompositionV35()), mustCompileWorkflowComposition(breakFixCompositionV32())} {
		if got := dispatchPacketSchemaRef(t, definition); got != "worker_packet4" {
			t.Fatalf("checks-repair predecessor dispatch packet schema ref = %q, want retained worker_packet4", got)
		}
	}
	v33 := mustCompileWorkflowComposition(implementationCompositionV33())
	if got := dispatchPacketSchemaRef(t, v33); got != "worker_packet3" {
		t.Fatalf("implementation v33 dispatch packet schema ref = %q, want the retained worker_packet3", got)
	}
	const workID = "worker-job-size-historical-packet"
	fixture := seedHistoricalWorkflowReturnRouteFixture(t, workID, "workflow.implementation", 33, "execution")
	s := workerJobSizeStore(fixture)
	definition, lookupErr := BuiltinWorkflowRegistry().Lookup("workflow.implementation", 33)
	if !lookupErr {
		t.Fatal("workflow.implementation v33 is not registered")
	}
	job := &WorkerJobBinding{JobID: "job:historical", Revision: 1}
	historicalFields := map[string]any{
		"job_id": job.JobID, "objective": "bounded historical job", "stopping_condition": "the recorded checks pass",
		"path_scope": []string{"internal/store"}, "checks": []string{"go test ./internal/store/"},
		"ready": true, "readiness_evidence": []string{"evidence:coordinator-ready"},
	}
	if oracleCapablePinForWork(t, s, workID) {
		bootstrapOracleFixtureSubject(t, s, workID)
		historicalFields["acceptance_oracle"] = acceptanceOracleFieldsForTest(t, s, workID)
	}
	if err := recordWorkerJobActionForTest(t, s, workID, fixture.owner, historicalFields); err != nil {
		t.Fatalf("record historical job: %v", err)
	}
	views, err := readWorkerJobRevisions(context.Background(), s.db, workID)
	if err != nil {
		t.Fatal(err)
	}
	for _, view := range views {
		if view.Binding.JobID == job.JobID {
			*job = view.Binding
		}
	}
	lane := reviewGateLane(t, "implementation")
	packet := joinPacketFor(t, s, workID, "execution", "attempt-historical", lane.ID, lane.Version, lane.Digest)
	packet["inputs"].(map[string]any)["worker_job"] = sizeDispatchPacketJob(t, s, workID, *job, nil)
	if _, _, err := validateWorkerPacketJob(context.Background(), s.db, definition.Definition, workID, lane, mustJSONValue(packet)); err != nil {
		t.Fatalf("historical v33 implementation dispatch refused: %v", err)
	}
}

// rerecordJobRevisionForTest records the job's next revision with the same
// size content, seeding fresh native preparations against the current
// qualified subject, and updates the binding.
func rerecordJobRevisionForTest(t *testing.T, f workContextFixture, job *WorkerJobBinding, baseCommit string) {
	t.Helper()
	fields := workerJobSizeEditingFields(t, f.workID, workflowReturnRouteFixture{store: f.store, owner: f.owner})
	fields["job_id"] = job.JobID
	fields["measurement_basis"].(map[string]any)["base_commit"] = baseCommit
	recordEditingJobForTest(t, workflowReturnRouteFixture{store: f.store, owner: f.owner}, f.workID, fields, job)
}

// sizeJourneyFixture seeds the work-context fixture with one real claimed
// worktree, so a dispatch runs the sealed freshness route against a
// qualified subject.
func sizeJourneyFixture(t *testing.T, workID string) (workContextFixture, string, string) {
	t.Helper()
	f := seedWorkContextFixture(t, workID)
	t.Cleanup(func() { f.store.Close() })
	dir, head := reviewSubjectClaim(t, f.store, workID)
	var branch string
	if err := f.store.db.QueryRow(`SELECT branch FROM worktree_entries WHERE set_id=? AND state='active'`, WorktreeSetID(workID)).Scan(&branch); err != nil {
		t.Fatal(err)
	}
	gitRunStore(t, dir, "branch", "-m", branch)
	bootstrapOracleFixtureSubject(t, f.store, workID)
	return f, dir, head
}

// sizeJourneyDispatch prepares the dispatch freshness with the session
// worktree (and the dispatched job binding, so the correction base anchors
// the ancestry probe) and runs the dispatch action.
func sizeJourneyDispatch(t *testing.T, f workContextFixture, dir, attemptID string, job WorkerJobBinding, exclusions []string) error {
	t.Helper()
	lane := reviewGateLane(t, "implementation")
	step := currentStep(t, f.store, f.workID)
	packet := joinPacketFor(t, f.store, f.workID, step, attemptID, lane.ID, lane.Version, lane.Digest)
	packet["inputs"].(map[string]any)["worker_job"] = sizeDispatchPacketJob(t, f.store, f.workID, job, exclusions)
	packet["inputs"].(map[string]any)["binding"].(map[string]any)["work_version"] = readWorkVersion(t, f.store, f.workID)
	correction, correctionErr := workflowCorrectionContextForDispatch(context.Background(), f.store.db, f.workID, step, attemptID)
	if correctionErr != nil {
		return correctionErr
	}
	if correction != nil {
		packet["inputs"].(map[string]any)["correction"] = correction
	}
	request := PrepareWorkerDispatchRequest{WorkID: f.workID, SessionWorktree: dir, WorkerJob: &job}
	prepared, err := PrepareWorkerDispatchFreshness(context.Background(), f.store, request)
	if err != nil {
		return err
	}
	ctx := WithWorkerDispatchPreparation(context.Background(), prepared)
	_, err = dispatchJoinAttempt(ctx, t, f.store, f.workID, readWorkVersion(t, f.store, f.workID), f.owner, packet)
	return err
}

// An implementation dispatch of an uncapped job refuses on a size-capable
// pin; the recorded cap is what makes an editing dispatch admissible.
func TestWorkerJobSizeImplementationLaneRequiresCappedJob(t *testing.T) {
	const workID = "worker-job-size-uncapped-dispatch"
	f, dir, _ := sizeJourneyFixture(t, workID)
	job := &WorkerJobBinding{JobID: "job:uncapped-dispatch", Revision: 1}
	recordEditingJobForTest(t, workflowReturnRouteFixture{store: f.store, owner: f.owner}, workID, workerJobSizeReadOnlyFields(t, workID, workflowReturnRouteFixture{store: f.store, owner: f.owner}), job)
	err := sizeJourneyDispatch(t, f, dir, "attempt:uncapped", *job, nil)
	if err == nil || !strings.Contains(err.Error(), "requires a capped worker job") {
		t.Fatalf("uncapped implementation dispatch error = %v, want the capped-job refusal", err)
	}
}

// The first implementation dispatch measures from its recorded base: the
// admitted clean subject must equal base_commit, and a newer subject refuses
// until a fresh revision records the current foundation.
func TestWorkerJobSizeFirstDispatchBindsTheRecordedBase(t *testing.T) {
	const workID = "worker-job-size-first-dispatch-base"
	f, dir, head := sizeJourneyFixture(t, workID)
	job := &WorkerJobBinding{JobID: "job:first-dispatch", Revision: 1}
	fields := workerJobSizeEditingFields(t, workID, workflowReturnRouteFixture{store: f.store, owner: f.owner})
	fields["measurement_basis"].(map[string]any)["base_commit"] = head
	recordEditingJobForTest(t, workflowReturnRouteFixture{store: f.store, owner: f.owner}, workID, fields, job)
	j := &candidateJourney{fixture: f, s: f.store, workID: workID, repo: dir}
	newer := descendantCommit(t, j, "prerequisite")
	rerecordJobRevisionForTest(t, f, job, head)
	err := sizeJourneyDispatch(t, f, dir, "attempt:newer-subject", *job, nil)
	var failure *Failure
	if !errors.As(err, &failure) || !strings.Contains(failure.Detail, "does not equal the admitted clean subject") {
		t.Fatalf("newer-subject first dispatch error = %v, want the base-equality refusal", err)
	}
	if failure != nil && !strings.Contains(failure.RecoveryAction, "record a fresh revision with the current foundation") {
		t.Fatalf("newer-subject first dispatch recovery = %q, want the record_worker_job recovery", failure.RecoveryAction)
	}
	rerecordJobRevisionForTest(t, f, job, newer)
	if err := sizeJourneyDispatch(t, f, dir, "attempt:first", *job, nil); err != nil {
		t.Fatalf("first dispatch on the fresh recorded base refused: %v", err)
	}
}

// finishSizeDispatchAttempt records the worker evidence and completion of an
// admitted implementation dispatch, so the job leaves its live attempt and
// enters correction.
func finishSizeDispatchAttempt(t *testing.T, f workContextFixture, attemptID string, job WorkerJobBinding) {
	t.Helper()
	if err := appendJobBoundWorkerDispatch(t, f.store, f.workID, attemptID, &job); err != nil {
		t.Fatalf("record implementation dispatch %s: %v", attemptID, err)
	}
	if err := completeJobBoundAttemptForTest(f.store, f.workID, attemptID, reviewGateLane(t, "implementation"), &job); err != nil {
		t.Fatalf("complete implementation attempt %s: %v", attemptID, err)
	}
}

// A live attempt's draft is unaccepted work: once the job's first
// implementation dispatch binds a base, a revision on the draft refuses at
// record time, and a re-dispatch beside the live attempt keeps the base and
// admits the draft subject as its descendant.
func TestWorkerJobSizeLiveAttemptKeepsItsBase(t *testing.T) {
	const workID = "worker-job-size-live-attempt-base"
	f, dir, head := sizeJourneyFixture(t, workID)
	route := workflowReturnRouteFixture{store: f.store, owner: f.owner}
	job := &WorkerJobBinding{JobID: "job:live-attempt", Revision: 1}
	fields := workerJobSizeEditingFields(t, workID, route)
	fields["measurement_basis"].(map[string]any)["base_commit"] = head
	recordEditingJobForTest(t, route, workID, fields, job)
	if err := sizeJourneyDispatch(t, f, dir, "attempt:live", *job, nil); err != nil {
		t.Fatalf("first dispatch on the recorded base refused: %v", err)
	}
	j := &candidateJourney{fixture: f, s: f.store, workID: workID, repo: dir}
	draft := descendantCommit(t, j, "live-draft")
	hidden := workerJobSizeEditingFields(t, workID, route)
	hidden["job_id"] = job.JobID
	hidden["measurement_basis"].(map[string]any)["base_commit"] = draft
	if err := recordWorkerJobActionForTest(t, f.store, workID, f.owner, hidden); err == nil || !strings.Contains(err.Error(), "hides the job's original base "+head) {
		t.Fatalf("revision on the live draft error = %v, want the original-base refusal", err)
	}
	rerecordJobRevisionForTest(t, f, job, head)
	if err := sizeJourneyDispatch(t, f, dir, "attempt:beside-live", *job, nil); err != nil {
		t.Fatalf("re-dispatch beside the live attempt on its draft refused: %v", err)
	}
}

// A correction keeps its original base: the subject may be the base or any
// proved descendant, never a foreign commit, never dirty.
func TestWorkerJobSizeCorrectionAncestry(t *testing.T) {
	const workID = "worker-job-size-correction-ancestry"
	f, dir, head := sizeJourneyFixture(t, workID)
	job := &WorkerJobBinding{JobID: "job:correction", Revision: 1}
	fields := workerJobSizeEditingFields(t, workID, workflowReturnRouteFixture{store: f.store, owner: f.owner})
	fields["measurement_basis"].(map[string]any)["base_commit"] = head
	recordEditingJobForTest(t, workflowReturnRouteFixture{store: f.store, owner: f.owner}, workID, fields, job)
	recordSizeDispatchAttempt(t, workflowReturnRouteFixture{store: f.store, owner: f.owner}, workID, "attempt:rejected", *job)
	j := &candidateJourney{fixture: f, s: f.store, workID: workID, repo: dir}
	t.Run("descendant prepared against another base refuses", func(t *testing.T) {
		descendantCommit(t, j, "draft-a")
		rerecordJobRevisionForTest(t, f, job, head)
		step := currentStep(t, f.store, f.workID)
		lane := reviewGateLane(t, "implementation")
		packet := joinPacketFor(t, f.store, f.workID, step, "attempt:other-base", lane.ID, lane.Version, lane.Digest)
		packet["inputs"].(map[string]any)["worker_job"] = sizeDispatchPacketJob(t, f.store, f.workID, *job, nil)
		packet["inputs"].(map[string]any)["binding"].(map[string]any)["work_version"] = readWorkVersion(t, f.store, f.workID)
		correction, err := workflowCorrectionContextForDispatch(context.Background(), f.store.db, f.workID, step, "attempt:other-base")
		if err != nil {
			t.Fatal(err)
		}
		if correction != nil {
			packet["inputs"].(map[string]any)["correction"] = correction
		}
		prepared, err := PrepareWorkerDispatchFreshness(context.Background(), f.store, PrepareWorkerDispatchRequest{WorkID: f.workID, SessionWorktree: dir, WorkerJob: job})
		if err != nil {
			t.Fatal(err)
		}
		prepared.proof.correctionBase = strings.Repeat("d", 40)
		_, err = dispatchJoinAttempt(WithWorkerDispatchPreparation(context.Background(), prepared), t, f.store, f.workID, readWorkVersion(t, f.store, f.workID), f.owner, packet)
		if err == nil || !strings.Contains(err.Error(), "was prepared against base") {
			t.Fatalf("mismatched sealed base error = %v, want the re-prepare refusal", err)
		}
	})
	t.Run("subject at the base admits", func(t *testing.T) {
		gitRunStore(t, dir, "reset", "--hard", head)
		j.observeRun(t, "reset-base")
		rerecordJobRevisionForTest(t, f, job, head)
		if err := sizeJourneyDispatch(t, f, dir, "attempt:at-base", *job, nil); err != nil {
			t.Fatalf("correction dispatch at the base refused: %v", err)
		}
		finishSizeDispatchAttempt(t, f, "attempt:at-base", *job)
	})
	t.Run("draft over accepted disjoint work descending from the base admits", func(t *testing.T) {
		// Another job's accepted work lands on a disjoint path above the
		// base; the correction's draft follows it on the same line.
		if err := os.MkdirAll(filepath.Join(dir, "adapter", "opencode"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "adapter", "opencode", "accepted.ts"), []byte("accepted disjoint work\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		j.commit(t, "coordinator@example.invalid", "accepted-disjoint")
		descendantCommit(t, j, "draft-b")
		rerecordJobRevisionForTest(t, f, job, head)
		if err := sizeJourneyDispatch(t, f, dir, "attempt:draft", *job, nil); err != nil {
			t.Fatalf("correction dispatch on the draft refused: %v", err)
		}
	})
	t.Run("non-descendant subject refuses", func(t *testing.T) {
		foreignWork := workID + "-foreign"
		foreign, foreignDir, foreignHead := sizeJourneyFixture(t, foreignWork)
		foreignJob := &WorkerJobBinding{JobID: "job:correction-foreign", Revision: 1}
		foreignFields := workerJobSizeEditingFields(t, foreignWork, workflowReturnRouteFixture{store: foreign.store, owner: foreign.owner})
		foreignFields["measurement_basis"].(map[string]any)["base_commit"] = foreignHead
		recordEditingJobForTest(t, workflowReturnRouteFixture{store: foreign.store, owner: foreign.owner}, foreignWork, foreignFields, foreignJob)
		recordSizeDispatchAttempt(t, workflowReturnRouteFixture{store: foreign.store, owner: foreign.owner}, foreignWork, "attempt:rejected-foreign", *foreignJob)
		foreignJourney := &candidateJourney{fixture: foreign, s: foreign.store, workID: foreignWork, repo: foreignDir}
		gitRunStore(t, foreignDir, "checkout", "--orphan", "foreign-"+foreignWork)
		gitRunStore(t, foreignDir, "commit", "--allow-empty", "-m", "foreign root")
		gitRunStore(t, foreignDir, "checkout", "-B", "work/"+foreignWork)
		foreignJourney.observeRun(t, "foreign-subject")
		rerecordJobRevisionForTest(t, foreign, foreignJob, foreignHead)
		err := sizeJourneyDispatch(t, foreign, foreignDir, "attempt:foreign", *foreignJob, nil)
		if err == nil || !strings.Contains(err.Error(), "nor its descendant") {
			t.Fatalf("foreign-subject correction error = %v, want the descent refusal", err)
		}
	})
	t.Run("dirty worktree refuses", func(t *testing.T) {
		dirtyWork := workID + "-dirty"
		dirty, dirtyDir, dirtyHead := sizeJourneyFixture(t, dirtyWork)
		dirtyJob := &WorkerJobBinding{JobID: "job:correction-dirty", Revision: 1}
		dirtyFields := workerJobSizeEditingFields(t, dirtyWork, workflowReturnRouteFixture{store: dirty.store, owner: dirty.owner})
		dirtyFields["measurement_basis"].(map[string]any)["base_commit"] = dirtyHead
		recordEditingJobForTest(t, workflowReturnRouteFixture{store: dirty.store, owner: dirty.owner}, dirtyWork, dirtyFields, dirtyJob)
		recordSizeDispatchAttempt(t, workflowReturnRouteFixture{store: dirty.store, owner: dirty.owner}, dirtyWork, "attempt:rejected-dirty", *dirtyJob)
		if err := os.WriteFile(filepath.Join(dirtyDir, "seed.txt"), []byte("dirty\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		err := sizeJourneyDispatch(t, dirty, dirtyDir, "attempt:dirty", *dirtyJob, nil)
		if err == nil || !strings.Contains(err.Error(), "has no clean admitted subject") {
			t.Fatalf("dirty correction dispatch error = %v, want the clean-subject refusal", err)
		}
	})
}

// A correction revision keeps its original base: another base hides the
// unaccepted work behind a later foundation and refuses.
func TestWorkerJobSizeCorrectionKeepsTheOriginalBaseAtRecord(t *testing.T) {
	const workID = "worker-job-size-record-base-retention"
	fixture := sizeDispatchFixture(t, workID)
	job := &WorkerJobBinding{JobID: "job:retained-base", Revision: 1}
	fields := workerJobSizeEditingFields(t, workID, fixture)
	fields["measurement_basis"].(map[string]any)["base_commit"] = testJobSizeBaseCommit()
	recordEditingJobForTest(t, fixture, workID, fields, job)
	recordSizeDispatchAttempt(t, fixture, workID, "attempt:blocked", *job)
	fields = workerJobSizeEditingFields(t, workID, fixture)
	fields["job_id"] = job.JobID
	fields["measurement_basis"].(map[string]any)["base_commit"] = strings.Repeat("c", 40)
	fields["production_size_max"] = 400
	err := recordWorkerJobActionForTest(t, workerJobSizeStore(fixture), workID, fixture.owner, fields)
	if err == nil || !strings.Contains(err.Error(), "hides the job's original base") {
		t.Fatalf("moved correction base error = %v, want the original-base refusal", err)
	}
	fields["measurement_basis"].(map[string]any)["base_commit"] = testJobSizeBaseCommit()
	if err := recordWorkerJobActionForTest(t, workerJobSizeStore(fixture), workID, fixture.owner, fields); err != nil {
		t.Fatalf("correction revision on the original base refused: %v", err)
	}
}

// A fresh revision recorded before the first dispatch moves the foundation;
// the correction then keeps the base its first implementation dispatch
// bound, not the lowest revision's base.
func TestWorkerJobSizeCorrectionKeepsTheFirstDispatchedBase(t *testing.T) {
	const workID = "worker-job-size-record-dispatched-base"
	fixture := sizeDispatchFixture(t, workID)
	stale := strings.Repeat("c", 40)
	job := &WorkerJobBinding{JobID: "job:refreshed-base", Revision: 1}
	fields := workerJobSizeEditingFields(t, workID, fixture)
	fields["measurement_basis"].(map[string]any)["base_commit"] = stale
	recordEditingJobForTest(t, fixture, workID, fields, job)
	recordEditingJobForTest(t, fixture, workID, workerJobSizeEditingFields(t, workID, fixture), job)
	recordSizeDispatchAttempt(t, fixture, workID, "attempt:refreshed", *job)
	fields = workerJobSizeEditingFields(t, workID, fixture)
	fields["job_id"] = job.JobID
	fields["measurement_basis"].(map[string]any)["base_commit"] = stale
	err := recordWorkerJobActionForTest(t, workerJobSizeStore(fixture), workID, fixture.owner, fields)
	if err == nil || !strings.Contains(err.Error(), "hides the job's original base") {
		t.Fatalf("lowest-revision base error = %v, want the first-dispatched-base refusal", err)
	}
	fields["measurement_basis"].(map[string]any)["base_commit"] = testJobSizeBaseCommit()
	if err := recordWorkerJobActionForTest(t, workerJobSizeStore(fixture), workID, fixture.owner, fields); err != nil {
		t.Fatalf("correction revision on the first-dispatched base refused: %v", err)
	}
}

// The recovery the uncapped-dispatch refusal names stays reachable: a
// capped revision of a job whose earlier revision was read-only records,
// because an uncapped revision holds no cap to inherit.
func TestWorkerJobSizeCappedRevisionFollowsReadOnlyRevision(t *testing.T) {
	const workID = "worker-job-size-capped-after-read-only"
	fixture := sizeDispatchFixture(t, workID)
	job := &WorkerJobBinding{JobID: "job:capped-after-read-only", Revision: 1}
	recordEditingJobForTest(t, fixture, workID, workerJobSizeReadOnlyFields(t, workID, fixture), job)
	recordEditingJobForTest(t, fixture, workID, workerJobSizeEditingFields(t, workID, fixture), job)
	if job.Revision != 2 {
		t.Fatalf("capped revision after a read-only revision = %#v, want revision 2", job)
	}
}

// While one job is in correction, an implementation dispatch of another job
// whose path scope intersects the correction's measured paths refuses and
// names the correction job; disjoint scopes and non-implementation lanes
// dispatch.
func TestWorkerJobSizeMeasuredPathSerialization(t *testing.T) {
	const workID = "worker-job-size-serialization"
	f, dir, head := sizeJourneyFixture(t, workID)
	blocked := &WorkerJobBinding{JobID: "job:blocked-serial", Revision: 1}
	blockedFields := workerJobSizeEditingFields(t, workID, workflowReturnRouteFixture{store: f.store, owner: f.owner})
	blockedFields["measurement_basis"].(map[string]any)["base_commit"] = head
	recordEditingJobForTest(t, workflowReturnRouteFixture{store: f.store, owner: f.owner}, workID, blockedFields, blocked)
	recordSizeDispatchAttempt(t, workflowReturnRouteFixture{store: f.store, owner: f.owner}, workID, "attempt:blocked-serial", *blocked)
	overlapping := &WorkerJobBinding{JobID: "job:overlapping", Revision: 1}
	overlapFields := workerJobSizeEditingFields(t, workID, workflowReturnRouteFixture{store: f.store, owner: f.owner})
	overlapFields["measurement_basis"].(map[string]any)["base_commit"] = head
	recordEditingJobForTest(t, workflowReturnRouteFixture{store: f.store, owner: f.owner}, workID, overlapFields, overlapping)
	exclusions := []string{"internal/store/worker_jobs.go"}
	err := sizeJourneyDispatch(t, f, dir, "attempt:overlap", *overlapping, exclusions)
	var failure *Failure
	if !errors.As(err, &failure) || !strings.Contains(failure.Detail, "intersects correction job job:blocked-serial") {
		t.Fatalf("overlapping dispatch error = %v, want the serialization refusal", err)
	}
	if failure != nil && !strings.Contains(failure.RecoveryAction, "accept correction job job:blocked-serial's result or split it") {
		t.Fatalf("overlapping dispatch recovery = %q, want the accept-or-split recovery", failure.RecoveryAction)
	}
	disjoint := &WorkerJobBinding{JobID: "job:disjoint", Revision: 1}
	disjointFields := workerJobSizeEditingFields(t, workID, workflowReturnRouteFixture{store: f.store, owner: f.owner})
	disjointFields["path_scope"] = []string{"adapter/opencode"}
	disjointFields["measurement_basis"] = map[string]any{"base_commit": head, "paths": []map[string]any{{"path": "adapter/opencode/packet.ts", "class": "production"}}}
	recordEditingJobForTest(t, workflowReturnRouteFixture{store: f.store, owner: f.owner}, workID, disjointFields, disjoint)
	if err := sizeJourneyDispatch(t, f, dir, "attempt:disjoint", *disjoint, exclusions); err != nil {
		t.Fatalf("disjoint-scope dispatch beside a correction refused: %v", err)
	}
}

// The caller exclusions stay inside the packet's declared bound: when the
// work's open corrections measure more paths than the packet carries, the
// exclusion computation refuses with the accept-or-split recovery instead of
// emitting a packet the closed schema rejects.
func TestWorkerJobSizeCallerExclusionsRefuseAboveTheBound(t *testing.T) {
	const workID = "worker-job-size-exclusions-bound"
	fixture := sizeDispatchFixture(t, workID)
	for index, count := range []int{WorkerJobMeasuredPathsMax, 1} {
		job := &WorkerJobBinding{JobID: fmt.Sprintf("job:correction-%d", index), Revision: 1}
		fields := workerJobSizeEditingFields(t, workID, fixture)
		paths := []map[string]any{}
		for n := 0; n < count; n++ {
			paths = append(paths, map[string]any{"path": fmt.Sprintf("internal/store/job%d_file%d.go", index, n), "class": "production"})
		}
		fields["measurement_basis"].(map[string]any)["paths"] = paths
		recordEditingJobForTest(t, fixture, workID, fields, job)
		recordSizeDispatchAttempt(t, fixture, workID, fmt.Sprintf("attempt:correction-%d", index), *job)
	}
	_, err := workerJobCallerExclusionsTx(context.Background(), workerJobSizeStore(fixture).db, workID, "job:other")
	var failure *Failure
	if !errors.As(err, &failure) || !strings.Contains(failure.Detail, fmt.Sprintf("would carry %d paths", WorkerJobMeasuredPathsMax+1)) || !strings.Contains(failure.RecoveryAction, "accept or split an open correction") {
		t.Fatalf("over-bound exclusions error = %v, want the bound refusal", err)
	}
	// The continuity read stays available: it publishes no exclusions for a
	// job above the bound, and that job's dispatch refuses with the recovery.
	other := &WorkerJobBinding{JobID: "job:other", Revision: 1}
	otherFields := workerJobSizeEditingFields(t, workID, fixture)
	otherFields["path_scope"] = []string{"adapter/opencode"}
	otherFields["measurement_basis"].(map[string]any)["paths"] = []map[string]any{{"path": "adapter/opencode/packet.ts", "class": "production"}}
	recordEditingJobForTest(t, fixture, workID, otherFields, other)
	if got := continuityReadyJobForTest(t, workerJobSizeStore(fixture), workID, other.JobID).CallerExclusions; got != nil {
		t.Fatalf("continuity published over-bound caller_exclusions %q, want none", got)
	}
}

// continuityReadyJobForTest reads one ready job from the continuity snapshot.
func continuityReadyJobForTest(t *testing.T, s *Store, workID, jobID string) WorkerPacketJob {
	t.Helper()
	snapshot, err := ReadWorkflowContinuity(context.Background(), s, ContinuityRequest{Work: workID})
	if err != nil {
		t.Fatal(err)
	}
	for _, job := range snapshot.ReadyWorkerJobs {
		if job.JobID == jobID {
			return job
		}
	}
	t.Fatalf("continuity carries no ready job %s", jobID)
	return WorkerPacketJob{}
}

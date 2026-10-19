package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

// The host observes the job's measured delta beside the delivery state
// (CD-0225 D2): when the completed implementation attempt's dispatched job
// carries a measurement basis, the terminal preparation runs one numstat diff
// between the recorded base and the observed commit with the shared diff
// flags, the sealed observation carries the per-path counts, and the ingest
// revalidates the observed base against the recorded job inside the
// transaction. A dirty tree records no paths; a Git failure refuses the
// completion with zero effects, exactly as before.

// scriptedDeliveryRunner answers the delivery probe's commands from a script
// and records the numstat argv, so a test proves the observation runs the
// shared diff flags against the job's recorded base.
type scriptedDeliveryRunner struct {
	statusOutput  []byte
	statusErr     error
	headOutput    string
	headErr       error
	numstatOutput []byte
	numstatErr    error

	numstatCalls    int
	lastNumstatArgs []string
}

func newScriptedDeliveryRunner() *scriptedDeliveryRunner {
	return &scriptedDeliveryRunner{headOutput: strings.Repeat("7", 40)}
}

func (r *scriptedDeliveryRunner) Run(_ context.Context, _ string, args ...string) ([]byte, error) {
	switch {
	case len(args) > 0 && args[0] == "status":
		return r.statusOutput, r.statusErr
	case len(args) > 0 && args[0] == "rev-parse":
		return []byte(r.headOutput), r.headErr
	default:
		r.numstatCalls++
		r.lastNumstatArgs = append([]string(nil), args...)
		return r.numstatOutput, r.numstatErr
	}
}

func TestWorkerNumstatParsing(t *testing.T) {
	t.Parallel()
	t.Run("normal counts parse", func(t *testing.T) {
		t.Parallel()
		paths, err := parseWorkerNumstat([]byte("3\t1\tinternal/store/a.go\x00"))
		if err != nil || len(paths) != 1 {
			t.Fatalf("parse = %#v, %v, want one path", paths, err)
		}
		if paths[0] != (WorkerObservedPath{Path: "internal/store/a.go", Added: 3, Deleted: 1}) {
			t.Fatalf("path = %#v, want the counted delta", paths[0])
		}
	})
	t.Run("binary numstat marks the path", func(t *testing.T) {
		t.Parallel()
		paths, err := parseWorkerNumstat([]byte("-\t-\tpixel.png\x00"))
		if err != nil || len(paths) != 1 {
			t.Fatalf("parse = %#v, %v, want one path", paths, err)
		}
		if paths[0] != (WorkerObservedPath{Path: "pixel.png", Binary: true}) {
			t.Fatalf("path = %#v, want the binary mark with zero counts", paths[0])
		}
	})
	t.Run("a rename parses as a deletion plus an addition", func(t *testing.T) {
		t.Parallel()
		paths, err := parseWorkerNumstat([]byte("4\t0\told.go\x000\t4\tnew.go\x00"))
		if err != nil || len(paths) != 2 {
			t.Fatalf("parse = %#v, %v, want two paths", paths, err)
		}
		if paths[0].Path != "old.go" || paths[0].Added != 4 || paths[1].Path != "new.go" || paths[1].Deleted != 4 {
			t.Fatalf("paths = %#v, want the rename split into a deletion and an addition", paths)
		}
	})
	t.Run("z output keeps paths verbatim", func(t *testing.T) {
		t.Parallel()
		paths, err := parseWorkerNumstat([]byte("1\t0\ta b#cäfe.txt\x00"))
		if err != nil || len(paths) != 1 {
			t.Fatalf("parse = %#v, %v, want one path", paths, err)
		}
		if paths[0].Path != "a b#cäfe.txt" {
			t.Fatalf("path = %q, want the -z bytes verbatim", paths[0].Path)
		}
	})
	t.Run("empty numstat parses to no paths", func(t *testing.T) {
		t.Parallel()
		paths, err := parseWorkerNumstat(nil)
		if err != nil || paths != nil {
			t.Fatalf("parse = %#v, %v, want nil paths", paths, err)
		}
	})
	for _, malformed := range []struct {
		name string
		raw  string
	}{
		{"missing count column", "oops\x00"},
		{"negative added", "-3\t1\tp.go\x00"},
		{"negative deleted", "3\t-1\tp.go\x00"},
		{"empty path", "1\t0\t\x00"},
	} {
		t.Run(malformed.name+" refuses", func(t *testing.T) {
			t.Parallel()
			if _, err := parseWorkerNumstat([]byte(malformed.raw)); err == nil {
				t.Fatalf("malformed numstat %q parsed", malformed.raw)
			}
		})
	}
}

// sizeObservationFixture seeds the work-context fixture with one recorded job
// and a dispatched implementation attempt whose terminal can be prepared, so
// each case controls the runner's git answers.
func sizeObservationFixture(t *testing.T, workID, definitionRef string, definitionVersion int64) (workContextFixture, WorkerJobBinding) {
	t.Helper()
	var fixture workContextFixture
	if definitionRef == "" {
		fixture = seedWorkContextFixture(t, workID)
	} else {
		registered, ok := BuiltinWorkflowRegistry().Lookup(definitionRef, definitionVersion)
		if !ok {
			t.Fatalf("%s v%d is not registered", definitionRef, definitionVersion)
		}
		fixture = seedWorkContextFixtureWithDefinition(t, workID, registered)
	}
	t.Cleanup(func() { fixture.store.Close() })
	declareSampleWorkContext(t, fixture, "the declaration anchors the size observation fixture")
	job := seedReadyWorkerJob(t, fixture)
	view, viewErr := readWorkContextView(context.Background(), fixture.store.DatabaseForTesting(), workID)
	if viewErr != nil || view == nil {
		t.Fatalf("admitted view = %+v err = %v, want a present view", view, viewErr)
	}
	attemptID := "attempt-" + workID
	packet := workContextDispatchPacket(t, fixture, attemptID, job, view)
	if err := dispatchWorkContextAttempt(t, fixture, packet); err != nil {
		t.Fatalf("dispatch refused: %v", err)
	}
	lane := laneForID(t)
	if err := ApplyOperation(context.Background(), fixture.store, Operation{Events: []Event{implementDispatchEvidenceEvent(workID, attemptID, lane, job)}}); err != nil {
		t.Fatal(err)
	}
	return fixture, job
}

func sizeObservationBasisBase(t *testing.T, fixture workContextFixture, job WorkerJobBinding) string {
	t.Helper()
	content, err := readWorkerJobSizeContentTx(context.Background(), fixture.store.db, fixture.workID)
	if err != nil {
		t.Fatal(err)
	}
	entry, recorded := content[job]
	if !recorded || entry.basis == nil {
		t.Fatalf("job %#v carries no recorded measurement basis", job)
	}
	return entry.basis.BaseCommit
}

func rawObservedDelivery(t *testing.T, db *sql.DB, eventID string) WorkerObservedDelivery {
	t.Helper()
	var raw []byte
	if err := db.QueryRow(`SELECT payload FROM domain_events WHERE event_id=?`, eventID).Scan(&raw); err != nil {
		t.Fatalf("read terminal event %s: %v", eventID, err)
	}
	var payload WorkerCompletedPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.ObservedDelivery == nil {
		t.Fatalf("terminal event %s carries no observed delivery", eventID)
	}
	return *payload.ObservedDelivery
}

func readDispatchedJobForTest(t *testing.T, s *Store, workID, attemptID string) *WorkerJobBinding {
	t.Helper()
	job, err := workflowDispatchedJobForAttempt(context.Background(), s.DatabaseForTesting(), workID, attemptID)
	if err != nil {
		t.Fatal(err)
	}
	return job
}

// sizeObservationCompletion builds the completed report event the terminal
// preparation consumes for the fixture's dispatched attempt.
func sizeObservationCompletion(t *testing.T, fixture workContextFixture) Event {
	t.Helper()
	lane := laneForID(t)
	attemptID := "attempt-" + fixture.workID
	job := readDispatchedJobForTest(t, fixture.store, fixture.workID, attemptID)
	return Event{EventID: "complete-" + fixture.workID, Kind: WorkerCompleted, SubjectType: SubjectWorkItem, SubjectID: fixture.workID, Actor: "worker:test", OccurredAt: time.Unix(2, 0).UTC(), PayloadVersion: WorkerEvidenceEventPayloadVersion(WorkerCompleted), Payload: mustJSONValue(WorkerCompletedPayload{
		AttemptID: attemptID, ReadbackModel: preferredModelForLane(lane), ReportSchemaVersion: WorkerReportSchemaVersion,
		EvidenceOrigin: WorkerEvidenceLegacyUnavailable, WorkerJob: job, WorkerDirectory: workerFixtureDirectory(t, fixture.store, fixture.workID),
	})}
}

func countTerminalsForTest(t *testing.T, s *Store, workID string) int {
	t.Helper()
	var terminals int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id=? AND kind IN (?,?)`, workID, WorkerCompleted, WorkerFailed).Scan(&terminals); err != nil {
		t.Fatal(err)
	}
	return terminals
}

func TestWorkerTerminalObservationCarriesJobSizeObservation(t *testing.T) {
	t.Run("a basis records the base and the parsed paths", func(t *testing.T) {
		fixture, job := sizeObservationFixture(t, "size-observe-basis", "", 0)
		s := fixture.store
		base := sizeObservationBasisBase(t, fixture, job)
		runner := newScriptedDeliveryRunner()
		runner.numstatOutput = []byte("3\t1\tinternal/store/worker_jobs.go\x00-\t-\ticon.png\x00")
		attemptID := "attempt-" + fixture.workID
		completion := sizeObservationCompletion(t, fixture)
		preparation, err := PrepareWorkerTerminalDelivery(context.Background(), s, PrepareWorkerTerminalDeliveryRequest{WorkID: fixture.workID, AttemptID: attemptID, EventID: completion.EventID, Runner: runner})
		if err != nil {
			t.Fatalf("terminal delivery preparation refused: %v", err)
		}
		observed := preparation.proof.observation
		if observed == nil || observed.Base != base {
			t.Fatalf("observed delivery = %#v, want base %s", observed, base)
		}
		if !slices.Equal(observed.Paths, []WorkerObservedPath{{Path: "internal/store/worker_jobs.go", Added: 3, Deleted: 1}, {Path: "icon.png", Binary: true}}) {
			t.Fatalf("observed paths = %#v, want the parsed numstat", observed.Paths)
		}
		if runner.numstatCalls != 1 {
			t.Fatalf("numstat ran %d times, want exactly one probe", runner.numstatCalls)
		}
		wantArgs := append(workerDiffSharedDiffArgs(), "--numstat", "-z", base, observed.Commit)
		if !slices.Equal(runner.lastNumstatArgs, wantArgs) {
			t.Fatalf("numstat argv = %q, want the shared flags against the recorded base: %q", runner.lastNumstatArgs, wantArgs)
		}
		workerTerminalEventThroughSeamCtx(t, WithWorkerTerminalPreparation(context.Background(), preparation), s, completion)
		stored := rawObservedDelivery(t, s.DatabaseForTesting(), completion.EventID)
		if stored.Base != base || !slices.Equal(stored.Paths, observed.Paths) {
			t.Fatalf("stored observed delivery = %#v, want the sealed base and paths", stored)
		}
	})
	t.Run("no basis records neither member", func(t *testing.T) {
		fixture, _ := sizeObservationFixture(t, "size-observe-plain", "workflow.break_fix", 27)
		s := fixture.store
		runner := newScriptedDeliveryRunner()
		attemptID := "attempt-" + fixture.workID
		completion := sizeObservationCompletion(t, fixture)
		preparation, err := PrepareWorkerTerminalDelivery(context.Background(), s, PrepareWorkerTerminalDeliveryRequest{WorkID: fixture.workID, AttemptID: attemptID, EventID: completion.EventID, Runner: runner})
		if err != nil {
			t.Fatalf("terminal delivery preparation refused: %v", err)
		}
		observed := preparation.proof.observation
		if observed == nil || observed.Base != "" || observed.Paths != nil {
			t.Fatalf("observed delivery = %#v, want no size members on a basis-free job", observed)
		}
		if runner.numstatCalls != 0 {
			t.Fatalf("numstat ran %d times, want none without a measurement basis", runner.numstatCalls)
		}
	})
	t.Run("a dirty observation records no paths and probes no numstat", func(t *testing.T) {
		fixture, job := sizeObservationFixture(t, "size-observe-dirty", "", 0)
		s := fixture.store
		base := sizeObservationBasisBase(t, fixture, job)
		runner := newScriptedDeliveryRunner()
		runner.statusOutput = []byte(" M seed.txt\x00")
		attemptID := "attempt-" + fixture.workID
		completion := sizeObservationCompletion(t, fixture)
		preparation, err := PrepareWorkerTerminalDelivery(context.Background(), s, PrepareWorkerTerminalDeliveryRequest{WorkID: fixture.workID, AttemptID: attemptID, EventID: completion.EventID, Runner: runner})
		if err != nil {
			t.Fatalf("terminal delivery preparation refused: %v", err)
		}
		observed := preparation.proof.observation
		if observed == nil || observed.State != "dirty" || observed.Base != base || observed.Paths != nil {
			t.Fatalf("observed delivery = %#v, want a dirty observation of base %s with no paths", observed, base)
		}
		if runner.numstatCalls != 0 {
			t.Fatalf("numstat ran %d times on a dirty tree, want none", runner.numstatCalls)
		}
		// A dirty capped completion stays a recorded report: it cannot
		// establish a size, and acceptance owns that refusal.
		workerTerminalEventThroughSeamCtx(t, WithWorkerTerminalPreparation(context.Background(), preparation), s, completion)
		stored := rawObservedDelivery(t, s.DatabaseForTesting(), completion.EventID)
		if stored.State != "dirty" || stored.Base != base || stored.Paths != nil {
			t.Fatalf("stored observed delivery = %#v, want the dirty completion recorded with its base and no paths", stored)
		}
	})
	t.Run("a numstat failure refuses the completion with zero effects", func(t *testing.T) {
		fixture, _ := sizeObservationFixture(t, "size-observe-failure", "", 0)
		s := fixture.store
		runner := newScriptedDeliveryRunner()
		runner.numstatErr = errors.New("git numstat exploded")
		attemptID := "attempt-" + fixture.workID
		completion := sizeObservationCompletion(t, fixture)
		_, err := PrepareWorkerTerminalDelivery(context.Background(), s, PrepareWorkerTerminalDeliveryRequest{WorkID: fixture.workID, AttemptID: attemptID, EventID: completion.EventID, Runner: runner})
		var failure *Failure
		if !errors.As(err, &failure) || failure.Kind != KindGitUnreachable {
			t.Fatalf("numstat failure error = %v, want %s", err, KindGitUnreachable)
		}
		if terminals := countTerminalsForTest(t, s, fixture.workID); terminals != 0 {
			t.Fatalf("the numstat refusal recorded %d terminal event(s)", terminals)
		}
	})
	t.Run("the ingest revalidates the observed base against the recorded job", func(t *testing.T) {
		fixture, _ := sizeObservationFixture(t, "size-observe-mismatch", "", 0)
		s := fixture.store
		runner := newScriptedDeliveryRunner()
		attemptID := "attempt-" + fixture.workID
		completion := sizeObservationCompletion(t, fixture)
		preparation, err := PrepareWorkerTerminalDelivery(context.Background(), s, PrepareWorkerTerminalDeliveryRequest{WorkID: fixture.workID, AttemptID: attemptID, EventID: completion.EventID, Runner: runner})
		if err != nil {
			t.Fatalf("terminal delivery preparation refused: %v", err)
		}
		preparation.proof.observation.Base = strings.Repeat("e", 40)
		err = s.Transact(WithWorkerTerminalPreparation(context.Background(), preparation), func(tx *Transaction) error {
			_, txErr := ApplyWorkerEvidenceOperationTx(WithWorkerTerminalPreparation(context.Background(), preparation), tx, completion)
			return txErr
		})
		if err == nil || !strings.Contains(err.Error(), "not the dispatched job's recorded base_commit") {
			t.Fatalf("tampered observed base error = %v, want the recorded-base revalidation refusal", err)
		}
		if terminals := countTerminalsForTest(t, s, fixture.workID); terminals != 0 {
			t.Fatalf("the base-mismatch refusal recorded %d terminal event(s)", terminals)
		}
	})
}

// Historical payloads replay with no base or counts, never invented ones:
// the size members of observed_delivery are reserved for the size payload
// version, and bytes that carry them below the boundary are fabricated
// content no store ever recorded.
func TestWorkerCompletedObservedSizeVersionBoundary(t *testing.T) {
	seedDispatchedAttempt := func(t *testing.T, s *Store, workID, attemptID string) {
		t.Helper()
		authorizeWorkerFixtureDispatch(t, s, workID, attemptID)
		lane := BuiltinLaneDefinitions()[0]
		if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{workerDispatchEvent(workID, attemptID, lane, nil)}}); err != nil {
			t.Fatal(err)
		}
	}
	completionAt := func(version int, payload WorkerCompletedPayload) Event {
		return Event{EventID: "complete-" + payload.AttemptID, Kind: WorkerCompleted, SubjectType: SubjectWorkItem, SubjectID: "work-size-version", Actor: "worker:test", OccurredAt: time.Unix(2, 0).UTC(), PayloadVersion: version, Payload: mustJSONValue(payload)}
	}
	base := func(attemptID string, directory string) WorkerCompletedPayload {
		return WorkerCompletedPayload{AttemptID: attemptID, ReadbackModel: "openai/gpt-5.6-luna", ReportSchemaVersion: WorkerReportSchemaVersion, EvidenceOrigin: WorkerEvidenceLegacyUnavailable, WorkerDirectory: directory}
	}
	t.Run("a pre-size payload replays with no base or counts", func(t *testing.T) {
		s := openTemp(t)
		defer s.Close()
		seedDispatchedAttempt(t, s, "work-size-version", "attempt-size-plain")
		legacy := completionAt(workerCompletedJobSizeVersion-1, base("attempt-size-plain", workerFixtureDirectory(t, s, "work-size-version")))
		if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{legacy}}); err != nil {
			t.Fatalf("pre-size completion refused: %v", err)
		}
		var raw []byte
		if err := s.DatabaseForTesting().QueryRow(`SELECT payload FROM domain_events WHERE event_id=?`, legacy.EventID).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var stored WorkerCompletedPayload
		if err := json.Unmarshal(raw, &stored); err != nil {
			t.Fatal(err)
		}
		if stored.ObservedDelivery != nil {
			t.Fatalf("stored observed delivery = %#v, want explicit absence of the size members on a pre-size payload", stored.ObservedDelivery)
		}
	})
	t.Run("observed base or paths below the boundary refuse", func(t *testing.T) {
		s := openTemp(t)
		defer s.Close()
		seedDispatchedAttempt(t, s, "work-size-version", "attempt-size-base")
		payload := base("attempt-size-base", workerFixtureDirectory(t, s, "work-size-version"))
		payload.ObservedDelivery = &WorkerObservedDelivery{Commit: strings.Repeat("3", 40), State: "clean", Base: strings.Repeat("a", 40)}
		err := ApplyOperation(context.Background(), s, Operation{Events: []Event{completionAt(workerCompletedJobSizeVersion-1, payload)}})
		if err == nil || !strings.Contains(err.Error(), "reserved for payload version >= 8") {
			t.Fatalf("observed base below the boundary error = %v, want the fabricated-bytes refusal", err)
		}
	})
}

// recordObservedCorrectionAttempt records one more implementation attempt of
// a job in correction whose completion carries the sealed observed paths, so
// the measured-paths union reads them back from the completed events.
func recordObservedCorrectionAttempt(t *testing.T, s *Store, workID, attemptID string, job WorkerJobBinding, observed []WorkerObservedPath) {
	t.Helper()
	recordSizeDispatchAuthorization(t, s, workID, attemptID, job)
	if err := appendJobBoundWorkerDispatch(t, s, workID, attemptID, &job); err != nil {
		t.Fatalf("record implementation dispatch %s: %v", attemptID, err)
	}
	lane := reviewGateLane(t, "implementation")
	completed := jobBoundCompletionEvent(workID, "job-window-completed-"+attemptID, attemptID, lane, time.Unix(18, 0).UTC(), &job, workerFixtureDirectory(t, s, workID))
	var payload WorkerCompletedPayload
	if err := json.Unmarshal(completed.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	paths := make([]WorkerObservedPath, len(observed))
	copy(paths, observed)
	payload.ObservedDelivery = &WorkerObservedDelivery{Commit: strings.Repeat("9", 40), State: "clean", Base: strings.Repeat("a", 40), Paths: paths}
	payload.ProductionSize = &WorkerProductionSize{Base: strings.Repeat("a", 40), Tree: strings.Repeat("9", 40), Count: 0}
	completed.Payload = mustJSONValue(payload)
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{completed}}); err != nil {
		t.Fatalf("complete observed attempt %s: %v", attemptID, err)
	}
}

// The correction's measured paths are its inventory plus the sealed observed
// paths of its earlier implementation attempts (CD-0225 D1), read once for
// all jobs with no per-job event scans.
func TestWorkerJobCorrectionMeasuredPathsUnion(t *testing.T) {
	const workID = "worker-job-size-measured-union"
	fixture := sizeDispatchFixture(t, workID)
	s := workerJobSizeStore(fixture)
	job := &WorkerJobBinding{JobID: "job:union", Revision: 1}
	recordEditingJobForTest(t, fixture, workID, workerJobSizeEditingFields(t, workID, fixture), job)
	recordSizeDispatchAttempt(t, fixture, workID, "attempt:union-one", *job)
	recordObservedCorrectionAttempt(t, s, workID, "attempt:union-two", *job, []WorkerObservedPath{
		{Path: "internal/store/worker_jobs.go", Added: 5, Deleted: 2},
		{Path: "internal/store/new_file.go", Added: 40, Deleted: 0},
	})
	other := &WorkerJobBinding{JobID: "job:union-other", Revision: 1}
	recordEditingJobForTest(t, fixture, workID, workerJobSizeEditingFields(t, workID, fixture), other)
	recordObservedCorrectionAttempt(t, s, workID, "attempt:union-other", *other, []WorkerObservedPath{
		{Path: "internal/store/other_job.go", Added: 3, Deleted: 0},
	})
	measured, err := workerJobCorrectionMeasuredTx(context.Background(), s.db, workID)
	if err != nil {
		t.Fatal(err)
	}
	paths := measured[job.JobID]
	if !slices.Contains(paths, "internal/store/worker_jobs.go") || !slices.Contains(paths, "internal/store/new_file.go") {
		t.Fatalf("measured paths = %q, want the inventory unioned with the sealed observed paths", paths)
	}
	if slices.Contains(paths, "internal/store/other_job.go") {
		t.Fatalf("measured paths = %q, want another job's observed paths excluded", paths)
	}
	seen := map[string]bool{}
	for _, path := range paths {
		if seen[path] {
			t.Fatalf("measured paths repeat %s: %q", path, paths)
		}
		seen[path] = true
	}
}

// A revision recorded before the job's first implementation dispatch at
// another base refuses a later dispatch: the job keeps the base its first
// dispatch bound, whatever earlier revisions recorded.
// The live dispatch route refuses the superseded revision at readiness
// first; the size admission keeps the same rule as defense in depth, and
// this test proves both layers.
func TestWorkerJobSizeRepeatDispatchRefusesStaleRevisionBase(t *testing.T) {
	const workID = "worker-job-size-repeat-stale-base"
	f, dir, head := sizeJourneyFixture(t, workID)
	route := workflowReturnRouteFixture{store: f.store, owner: f.owner}
	job := &WorkerJobBinding{JobID: "job:stale-rev", Revision: 1}
	staleFields := workerJobSizeEditingFields(t, workID, route)
	staleFields["measurement_basis"].(map[string]any)["base_commit"] = head
	recordEditingJobForTest(t, route, workID, staleFields, job)
	staleBinding := *job
	j := &candidateJourney{fixture: f, s: f.store, workID: workID, repo: dir}
	// Advance the foundation and record the revision the first dispatch
	// admits; the job's original base binds from that dispatch.
	current := descendantCommit(t, j, "advanced-foundation")
	rerecordJobRevisionForTest(t, f, job, current)
	if err := sizeJourneyDispatch(t, f, dir, "attempt:first", *job, nil); err != nil {
		t.Fatalf("first dispatch on its recorded base refused: %v", err)
	}
	// The live dispatch route refuses the stale revision before the size
	// admission: it is no longer the job's latest recorded revision.
	err := sizeJourneyDispatch(t, f, dir, "attempt:stale-live", staleBinding, nil)
	if err == nil || !strings.Contains(err.Error(), "not ready") {
		t.Fatalf("stale-revision live dispatch error = %v, want the readiness refusal", err)
	}
	// The size admission keeps the same rule: a stale revision's recorded
	// base is not the job's original base, whatever recorded it first.
	definition := mustBuiltinDefinition(t, "workflow.break_fix").Definition
	lane := reviewGateLane(t, "implementation")
	tx, txErr := f.store.DatabaseForTesting().BeginTx(context.Background(), nil)
	if txErr != nil {
		t.Fatal(txErr)
	}
	defer tx.Rollback()
	err = admitWorkerJobSizeDispatchTx(context.Background(), tx, workID, definition, lane, staleBinding, current)
	if err == nil || !strings.Contains(err.Error(), "not the job's original base "+current) {
		t.Fatalf("stale-revision size admission error = %v, want the original-base refusal", err)
	}
}

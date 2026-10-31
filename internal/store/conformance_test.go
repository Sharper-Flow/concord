package store

import (
	"bufio"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

const (
	conformanceWorkerEnv           = "TEST_CONCORD_CONFORMANCE_WORKER"
	conformanceLongEnv             = "CONCORD_CONFORMANCE_LONG"
	conformanceAcceptanceRunnerEnv = "CONCORD_ACCEPTANCE_RUNNER"
	githubActionsEnv               = "GITHUB_ACTIONS"
	acceptanceRunnerSignalExpected = "1"
)

// populationAuthorityReason names the closed set of reasons a population
// authority may resolve to. Reasons are stable strings: tests and downstream
// readers match on them.
const (
	populationAuthorityReasonDiagnosticEntryPoint      = "diagnostic_entry_point"
	populationAuthorityReasonRequiredCheckSignalAbsent = "required_check_signal_absent"
	populationAuthorityReasonRequiredCheck             = "required_check"
)

// durable_mixed is the separately named scenario that mixes the surviving
// public read owners with consequential durable commits (CD-0050 D2, CD-0207
// D1) on one temporary database.
const (
	durableMixedScenario            = "durable_mixed"
	durableMixedIterations          = 8
	durableMixedReadsPerIteration   = 3
	durableMixedReadBacksPerWorker  = 1
	durableMixedDurableOpsPerWorker = 2
	durableMixedProbeOp             = "conformance-durable-mixed-probe"
	durableMixedProbeProduct        = "conformance-durable-mixed-probe-product"
	durableMixedResearchOwner       = "conformance-durable-mixed-owner"
	durableMixedResearchPackID      = "conformance-durable-mixed-pack"
	mixedSampleProfileRead          = "mixed_read"
	mixedSampleProfileDurable       = "mixed_durable"
)

type conformanceRunnerProfile string

const (
	runnerProfileDiagnostic         conformanceRunnerProfile = "diagnostic"
	runnerProfileIsolatedAcceptance conformanceRunnerProfile = "isolated_acceptance"
)

// populationAuthority identifies the invocation provenance that binds the
// zero-escaped-busy policy (CD-0045 D2, CD-0046).
type populationAuthority string

const (
	populationAuthorityDiagnostic populationAuthority = "diagnostic"
	populationAuthorityAccepted   populationAuthority = "accepted"
)

// resolvePopulationAuthority identifies required-check correctness evidence.
//
// The mechanism here establishes the *provenance of the invocation*: a CI
// workflow that sets the required-check signal. It does not measure host
// isolation. A busy CI runner still passes the check; a quiet laptop with the
// signal exported would too. Nothing in this package may describe the signal
// as establishing isolation. See CD-0046.
//
// Rules, in order:
//   - profile is not the acceptance entry point → diagnostic, reason
//     `diagnostic_entry_point`.
//   - the acceptance-runner signal is not `"1"` → diagnostic, reason
//     `required_check_signal_absent`.
//   - otherwise → accepted, reason `required_check`.
//
// The signal is passed in rather than read from the environment so tests can
// drive the function without spawning child processes or mutating the global
// environment.
func resolvePopulationAuthority(profile conformanceRunnerProfile, acceptanceRunnerSignal string) (populationAuthority, string) {
	if profile != runnerProfileIsolatedAcceptance {
		return populationAuthorityDiagnostic, populationAuthorityReasonDiagnosticEntryPoint
	}
	if acceptanceRunnerSignal != acceptanceRunnerSignalExpected {
		return populationAuthorityDiagnostic, populationAuthorityReasonRequiredCheckSignalAbsent
	}
	return populationAuthorityAccepted, populationAuthorityReasonRequiredCheck
}

// resolveCIRunnerTripwire reports whether a run missing the acceptance-runner
// signal must fail visibly because it is operating under GitHub Actions.
// Local invocations (no `GITHUB_ACTIONS`) remain diagnostic and continue.
//
// A run is tripwired only when all three are true:
//   - the profile is the acceptance entry point;
//   - `GITHUB_ACTIONS == "true"`;
//   - the acceptance-runner signal is absent.
//
// The function is pure and takes all signals in so tests can drive every
// combination.
func resolveCIRunnerTripwire(profile conformanceRunnerProfile, githubActionsSignal, acceptanceRunnerSignal string) bool {
	return profile == runnerProfileIsolatedAcceptance &&
		githubActionsSignal == "true" &&
		acceptanceRunnerSignal != acceptanceRunnerSignalExpected
}

// readLoadAverageOneMinute returns the host's 1-minute load average on Linux,
// read from /proc/loadavg. It returns ok=false silently if the file is
// unreadable or malformed (e.g. macOS, Windows, or restricted CI sandboxes).
// The value is recorded as provenance only — it must never gate, classify, or
// influence any verdict. A later reader must not wire it into the threshold.
func readLoadAverageOneMinute() (load float64, ok bool) {
	data, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0, false
	}
	fields := strings.Fields(string(data))
	if len(fields) < 1 {
		return 0, false
	}
	value, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0, false
	}
	return value, true
}

// acceptanceRunnerSignal returns the invocation signal consumed by TestMain.
// Only an exact "1" grants accepted authority, so unset,
// empty, and any other value all resolve to diagnostic.
func acceptanceRunnerSignal() string {
	return conformanceConfiguration.acceptanceRunner
}

// githubActionsSignal reads the GITHUB_ACTIONS environment variable at the
// harness boundary, keeping environment access out of the pure predicates.
func githubActionsSignal() string {
	return os.Getenv(githubActionsEnv)
}

type WorkerOutcome string

const (
	outcomeAccepted            WorkerOutcome = "accepted"
	outcomeVersionConflict     WorkerOutcome = "version_conflict"
	outcomeDuplicate           WorkerOutcome = "duplicate"
	outcomeStaleAttempt        WorkerOutcome = "stale_attempt"
	outcomeIdempotencyConflict WorkerOutcome = "idempotency_conflict"
	outcomeUnsupportedPayload  WorkerOutcome = "unsupported_payload"
	outcomeBusyEscaped         WorkerOutcome = "busy_escaped"
	outcomeInvariantViolation  WorkerOutcome = "invariant_violation"
	outcomeLost                WorkerOutcome = "lost"
	outcomeError               WorkerOutcome = "error"
)

// WorkerSample preserves each operation's outcome within a worker population.
type WorkerSample struct {
	Outcome     WorkerOutcome `json:"outcome"`
	FailureKind FailureKind   `json:"failure_kind,omitempty"`
	Profile     string        `json:"profile,omitempty"`
}

// WorkerResult is the bounded line-delimited result emitted by one child.
type WorkerResult struct {
	Worker       int            `json:"worker"`
	PID          int            `json:"pid"`
	DBIdentity   string         `json:"db_identity"`
	EventIDs     []string       `json:"event_ids,omitempty"`
	OperationIDs []string       `json:"operation_ids,omitempty"`
	Outcome      WorkerOutcome  `json:"outcome"`
	FailureKind  FailureKind    `json:"failure_kind,omitempty"`
	Attempts     int            `json:"attempts,omitempty"`
	Profile      string         `json:"profile,omitempty"`
	Samples      []WorkerSample `json:"samples,omitempty"`
	// DurableCommits counts the durable transactions this child committed
	// through the durable owner (CD-0207 D1). It is set only by the
	// durable_mixed scenario.
	DurableCommits int64 `json:"durable_commits,omitempty"`
}

// conformanceMixedPopulation counts the durable_mixed operations and commits.
type conformanceMixedPopulation struct {
	Workers        int   `json:"workers"`
	ReadSamples    int64 `json:"read_samples"`
	DurableSamples int64 `json:"durable_samples"`
	Accepted       int64 `json:"accepted"`
	BusyEscaped    int64 `json:"busy_escaped"`
	Errors         int64 `json:"errors"`
	DurableCommits int64 `json:"durable_commits"`
}

// ConformanceReport records correctness and invocation provenance without paths.
type ConformanceReport struct {
	Workers                   int                              `json:"workers"`
	Attempts                  int                              `json:"attempts"`
	Counts                    map[WorkerOutcome]int            `json:"counts"`
	Lost                      int                              `json:"lost"`
	UnexpectedDupes           int                              `json:"unexpected_duplicates"`
	InvariantViolations       int                              `json:"invariant_violations"`
	BusyEscaped               int                              `json:"busy_escaped"`
	CorrectnessPassed         bool                             `json:"correctness_passed"`
	RaceInstrumented          bool                             `json:"race_instrumented"`
	RunnerProfile             conformanceRunnerProfile         `json:"runner_profile"`
	AcceptancePopulation      bool                             `json:"acceptance_population"`
	PopulationAuthority       populationAuthority              `json:"population_authority"`
	PopulationAuthorityReason string                           `json:"population_authority_reason"`
	Scenarios                 map[string]map[WorkerOutcome]int `json:"scenarios"`
	PayloadProfiles           map[string]map[int]WorkerOutcome `json:"payload_profiles,omitempty"`
	// MixedPopulation carries the durable_mixed sample population. The
	// separate write population omits it.
	MixedPopulation *conformanceMixedPopulation `json:"mixed_population,omitempty"`
}

func TestConformanceWorker(t *testing.T) {
	t.Parallel()
	if os.Getenv(conformanceWorkerEnv) != "1" {
		return
	}
	path := os.Getenv("TEST_CONCORD_CONFORMANCE_DB")
	worker := mustIntEnv("TEST_CONCORD_CONFORMANCE_WORKER_ID")
	scenario := os.Getenv("TEST_CONCORD_CONFORMANCE_SCENARIO")
	s, err := Open(context.Background(), path)
	if err != nil {
		emitWorker(WorkerResult{Worker: worker, PID: os.Getpid(), Outcome: outcomeError, FailureKind: failureKind(err), DBIdentity: dbIdentity(path)})
		return
	}
	defer s.Close()
	enc := json.NewEncoder(os.Stdout)
	_ = enc.Encode(map[string]any{"ready": true, "worker": worker, "pid": os.Getpid()})
	scanner := bufio.NewScanner(os.Stdin)
	if !scanner.Scan() || strings.TrimSpace(scanner.Text()) != "RUN" {
		return
	}
	result := runWorkerScenario(context.Background(), s, worker, scenario)
	result.PID = os.Getpid()
	result.DBIdentity = dbIdentity(path)
	_ = enc.Encode(result)
}

// Serial: ten child processes need the whole machine to meet their deadlines.
func TestTenProcessConformance(t *testing.T) {
	if os.Getenv(conformanceWorkerEnv) == "1" {
		return
	}
	runTenProcessConformance(t, runnerProfileDiagnostic, conformanceConfiguration.long)
}

// TestTenProcessAcceptanceConformance is the isolated acceptance-workflow entry
// point. The generic test above cannot elevate itself through environment input.
func TestTenProcessAcceptanceConformance(t *testing.T) {
	t.Parallel()
	if os.Getenv(conformanceWorkerEnv) == "1" {
		return
	}
	if !conformanceConfiguration.long {
		t.Skip("acceptance conformance runs only in long mode")
	}
	// Missing invocation provenance must not silently downgrade required proof.
	if resolveCIRunnerTripwire(runnerProfileIsolatedAcceptance, githubActionsSignal(), acceptanceRunnerSignal()) {
		t.Fatalf("acceptance entry point ran under GitHub Actions without %s=1; required check signal is absent", conformanceAcceptanceRunnerEnv)
	}
	runTenProcessConformance(t, runnerProfileIsolatedAcceptance, true)
}

func runTenProcessConformance(t *testing.T, runnerProfile conformanceRunnerProfile, long bool) {
	t.Helper()
	timeout := 180 * time.Second
	if long {
		timeout = 8 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	root := t.TempDir()
	path := filepath.Join(root, "conformance.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	report := newConformanceReport(runnerProfile)
	all := make([]WorkerResult, 0, 200)

	run := func(name string) []WorkerResult {
		results, runErr := runConformanceWorkers(ctx, path, name)
		if runErr != nil {
			t.Fatalf("scenario %s: %v", name, runErr)
		}
		assertTenWorkers(t, path, results)
		addScenario(&report, name, results)
		all = append(all, expandWorkerResults(results)...)
		return results
	}

	results := run("distinct")
	if countOutcome(results, outcomeAccepted) != 10 {
		t.Fatalf("distinct writes = %+v, want ten accepted", report.Scenarios["distinct"])
	}
	results = run("read_write")
	if countOutcome(results, outcomeAccepted) != 10 {
		t.Fatalf("concurrent reads + writes = %+v, want ten accepted", report.Scenarios["read_write"])
	}
	results = run("same")
	if countOutcome(results, outcomeAccepted) != 1 || countOutcome(results, outcomeVersionConflict) != 9 {
		t.Fatalf("same-entity race = %+v, want 1 accepted/9 conflicts", report.Scenarios["same"])
	}
	results = run("lifecycle_relations")
	if countOutcome(results, outcomeAccepted) != 10 {
		t.Fatalf("lifecycle/relation operations = %+v, want ten accepted", report.Scenarios["lifecycle_relations"])
	}
	if err := assertLifecycleRelationEffects(ctx, s); err != nil {
		t.Fatal(err)
	}

	claim := testClaim("conformance-fence", "parent-claim")
	if _, err := ClaimStepAuthorized(ctx, s, claim, nil); err != nil {
		t.Fatal(err)
	}
	results = run("fence")
	if countOutcome(results, outcomeAccepted) != 10 {
		t.Fatalf("fence claims = %+v, want ten accepted epochs", report.Scenarios["fence"])
	}
	if got := maxAttemptEpoch(t, s, "conformance-fence"); got != 11 {
		t.Fatalf("fence claims ended at epoch %d, want 11", got)
	}

	staleClaim := testClaim("conformance-stale", "stale-parent")
	if _, err := ClaimStepAuthorized(ctx, s, staleClaim, nil); err != nil {
		t.Fatal(err)
	}
	secondClaim := testClaim("conformance-stale", "stale-second-claim")
	secondClaim.PrincipalRef = "operator"
	secondClaim.RequestID = "stale-second-claim-request"
	if _, err := ClaimStepAuthorized(ctx, s, secondClaim, nil); err != nil {
		t.Fatal(err)
	}
	results = run("stale_completion")
	if countOutcome(results, outcomeStaleAttempt) != 10 {
		t.Fatalf("stale completions = %+v, want ten stale attempts", report.Scenarios["stale_completion"])
	}

	claim = testClaim("conformance-idempotent", "parent-idempotent-claim")
	if _, err := ClaimStepAuthorized(ctx, s, claim, nil); err != nil {
		t.Fatal(err)
	}
	results = run("idempotent")
	if countOutcome(results, outcomeAccepted) != 1 || countOutcome(results, outcomeDuplicate) != 9 {
		t.Fatalf("idempotent retries = %+v, want 1 accepted/9 replayed", report.Scenarios["idempotent"])
	}

	claim = testClaim("conformance-idempotency-conflict", "parent-conflict-claim")
	if _, err := ClaimStepAuthorized(ctx, s, claim, nil); err != nil {
		t.Fatal(err)
	}
	results = run("idempotency_conflict")
	if countOutcome(results, outcomeAccepted) != 1 || countOutcome(results, outcomeIdempotencyConflict) != 9 {
		t.Fatalf("idempotency conflicts = %+v, want 1 accepted/9 conflicts", report.Scenarios["idempotency_conflict"])
	}

	results = run("step_read")
	if maxAttemptEpoch(t, s, "conformance-fence") != 11 {
		t.Fatal("Step reads advanced the fence epoch")
	}
	if countOutcome(results, outcomeAccepted) != 10 {
		t.Fatalf("Step reads = %+v, want ten successful reads", report.Scenarios["step_read"])
	}

	// Ten children mix public reads with durable claim and completion commits.
	if err := prepareDurableMixedFixtures(t, ctx, s); err != nil {
		t.Fatal(err)
	}
	mixedResults, err := runConformanceWorkers(ctx, path, durableMixedScenario)
	if err != nil {
		t.Fatalf("durable_mixed scenario: %v", err)
	}
	assertTenWorkers(t, path, mixedResults)
	addScenario(&report, durableMixedScenario, mixedResults)
	if countOutcome(mixedResults, outcomeAccepted) != 10 {
		t.Fatalf("durable_mixed workers = %+v, want ten accepted", report.Scenarios[durableMixedScenario])
	}
	if err := verifyDurableMixedEffects(t, ctx, s, report.Workers); err != nil {
		t.Fatal(err)
	}
	mixedPopulation := summarizeMixedPopulation(mixedResults)
	if err := validateMixedPopulation(mixedPopulation, report.Workers); err != nil {
		t.Fatalf("durable_mixed population: %v", err)
	}
	report.MixedPopulation = &mixedPopulation
	all = append(all, expandWorkerResults(mixedResults)...)

	if err := ApplyOperation(ctx, s, conformanceCreationOperation("duplicate-conformance", "duplicate-membership")); err != nil {
		t.Fatal(err)
	}
	results = run("duplicate")
	if countOutcome(results, outcomeDuplicate) != 10 {
		t.Fatalf("post-commit retries = %+v, want ten duplicate classifications", report.Scenarios["duplicate"])
	}

	results = run("payload_compatibility")
	addPayloadProfiles(&report, results)
	if countOutcome(results, outcomeAccepted) != 2 || countOutcome(results, outcomeUnsupportedPayload) != 8 {
		t.Fatalf("payload compatibility = %+v, want two current accepted and eight non-current refused; results=%+v", report.Scenarios["payload_compatibility"], results)
	}
	for _, result := range results {
		if result.Profile != "current" {
			count, err := countEffectReceipts(s, fmt.Sprintf("compat-%d-work", result.Worker))
			if err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Fatalf("non-current payload worker %d (%s) wrote an effect receipt", result.Worker, result.Profile)
			}
		}
	}

	if err := killBeforeCommitAndRetry(ctx, path, s); err != nil {
		t.Fatal(err)
	}
	addScenario(&report, "precommit_sigkill", []WorkerResult{{Outcome: outcomeAccepted, Worker: 99, PID: os.Getpid(), Attempts: 1}})
	if err := validateMembershipInvariants(ctx, s.DatabaseForTesting()); err != nil {
		t.Fatal(err)
	}

	backupPath, manifest, backupResults := runConcurrentBackup(t, ctx, path)
	if _, err := VerifyBackup(ctx, backupPath); err != nil {
		t.Fatal(err)
	}
	if err := verifyRestoredBackup(t, ctx, backupPath, manifest); err != nil {
		t.Fatal(err)
	}
	assertTenWorkers(t, path, backupResults)
	addScenario(&report, "concurrent_online_backup_restore", backupResults)
	all = append(all, expandWorkerResults(backupResults)...)

	for _, result := range all {
		report.Counts[result.Outcome]++
		report.Attempts++
		if result.Outcome == outcomeLost {
			report.Lost++
		}
		if result.Outcome == outcomeInvariantViolation {
			report.InvariantViolations++
		}
		if result.Outcome == outcomeBusyEscaped {
			report.BusyEscaped++
		}
	}
	report.UnexpectedDupes = report.Scenarios["idempotent"][outcomeDuplicate] - 9
	if report.UnexpectedDupes < 0 {
		report.UnexpectedDupes = 0
	}
	report.CorrectnessPassed = conformanceCorrectnessPassed(report.PopulationAuthority, report.Lost, report.InvariantViolations, report.BusyEscaped)
	if !report.CorrectnessPassed {
		t.Fatalf("conformance correctness gate failed: %+v", report)
	}
	if report.BusyEscaped > 0 && !busyEscapesBindCorrectness(report.PopulationAuthority) {
		t.Logf("busy escapes=%d on diagnostic population; CD-0045 D2 binds zero-escape to the accepted population", report.BusyEscaped)
	}
	t.Log("ConformanceReport " + mustJSON(report))

	if long {
		runWritePopulation(t, ctx, root, runnerProfile)
	}
}

func newConformanceReport(profile conformanceRunnerProfile) ConformanceReport {
	authority, reason := resolvePopulationAuthority(profile, acceptanceRunnerSignal())
	report := ConformanceReport{
		Workers:                   10,
		Counts:                    map[WorkerOutcome]int{},
		Scenarios:                 map[string]map[WorkerOutcome]int{},
		RunnerProfile:             profile,
		AcceptancePopulation:      authority == populationAuthorityAccepted,
		PopulationAuthority:       authority,
		PopulationAuthorityReason: reason,
		RaceInstrumented:          conformanceRaceInstrumented,
	}
	return report
}

// busyEscapesBindCorrectness reports whether a busy escape fails the
// correctness gate on this population. CD-0045 D2 binds zero escaped
// SQLITE_BUSY to the accepted agent-facing population. Lost writes and
// invariant violations stay unconditionally fatal.
func busyEscapesBindCorrectness(authority populationAuthority) bool {
	return authority == populationAuthorityAccepted
}

// conformanceCorrectnessPassed is the base-population correctness gate:
// absolute correctness terms plus the admission promise where CD-0045 D2
// binds it. The authority is the resolved population authority, not the
// profile literal, matching how the report derives AcceptancePopulation.
func conformanceCorrectnessPassed(authority populationAuthority, lost, invariantViolations, busyEscaped int) bool {
	if lost != 0 || invariantViolations != 0 {
		return false
	}
	if busyEscapesBindCorrectness(authority) && busyEscaped != 0 {
		return false
	}
	return true
}

func runWritePopulation(t *testing.T, ctx context.Context, root string, profile conformanceRunnerProfile) {
	t.Helper()
	path := filepath.Join(root, "writes.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	results, err := runConformanceWorkers(ctx, path, "load")
	if err != nil {
		t.Fatal(err)
	}
	assertTenWorkers(t, path, results)
	if err := validateWritePopulation(results); err != nil {
		t.Fatal(err)
	}
	if err := verifyWriteEffects(ctx, s, 10, 100); err != nil {
		t.Fatal(err)
	}
	report := newConformanceReport(profile)
	addScenario(&report, "writes", results)
	report.Counts = report.Scenarios["writes"]
	report.Attempts = len(expandWorkerResults(results))
	report.CorrectnessPassed = true
	t.Log("ConformanceReport writes " + mustJSON(report))
}

func validateWritePopulation(results []WorkerResult) error {
	if len(results) != 10 {
		return fmt.Errorf("write population has %d workers, want 10", len(results))
	}
	seen := map[int]bool{}
	for _, result := range results {
		if result.Worker < 0 || result.Worker >= 10 || seen[result.Worker] {
			return fmt.Errorf("unexpected or duplicate write worker %d", result.Worker)
		}
		seen[result.Worker] = true
		if result.Outcome != outcomeAccepted || result.Attempts != 100 || len(result.Samples) != 100 {
			return fmt.Errorf("write worker %d: outcome=%s attempts=%d samples=%d, want accepted/100/100", result.Worker, result.Outcome, result.Attempts, len(result.Samples))
		}
		for attempt, sample := range result.Samples {
			if sample.Outcome != outcomeAccepted {
				return fmt.Errorf("write worker %d attempt %d: %s", result.Worker, attempt, sample.Outcome)
			}
		}
	}
	return nil
}

func verifyWriteEffects(ctx context.Context, s *Store, workers, attempts int) error {
	for worker := 0; worker < workers; worker++ {
		for attempt := 0; attempt < attempts; attempt++ {
			id := fmt.Sprintf("production-%d-%d", worker, attempt)
			op := conformanceCreationOperation(id, id+"-membership")
			for _, event := range op.Events {
				if count, err := countEffectReceipts(s, event.EventID); err != nil || count != 1 {
					return fmt.Errorf("write %s effect %s count=%d, want 1: %v", id, event.EventID, count, err)
				}
			}
			var count int
			if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT count(*) FROM products p JOIN product_projects pp ON pp.product_id=p.id JOIN projects j ON j.id=pp.project_id WHERE p.id=? AND p.version=2 AND j.id=? AND j.version=1 AND pp.role='primary'`, id, id+"-project").Scan(&count); err != nil || count != 1 {
				return fmt.Errorf("write %s projection count=%d, want 1: %v", id, count, err)
			}
			op.ExpectedVersions = nil
			if err := ApplyOperation(ctx, s, op); failureKind(err) != KindDuplicateEvent {
				return fmt.Errorf("write %s replay = %v, want duplicate event", id, err)
			}
		}
	}
	var count int
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT count(*) FROM effect_receipts`).Scan(&count); err != nil || count != workers*attempts*3 {
		return fmt.Errorf("write effect count=%d, want %d: %v", count, workers*attempts*3, err)
	}
	for _, table := range []string{"products", "projects", "product_projects"} {
		if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT count(*) FROM `+table).Scan(&count); err != nil || count != workers*attempts {
			return fmt.Errorf("write %s count=%d, want %d: %v", table, count, workers*attempts, err)
		}
	}
	return validateMembershipInvariants(ctx, s.DatabaseForTesting())
}

func runWorkerScenario(ctx context.Context, s *Store, worker int, scenario string) WorkerResult {
	if scenario == "load" || scenario == "backup_load" {
		return runLoadScenario(ctx, s, worker, scenario)
	}
	result := WorkerResult{Worker: worker, Outcome: outcomeError, Attempts: 1}
	var err error
	switch scenario {
	case "distinct", "read_write":
		id := fmt.Sprintf("conformance-%s-%d", scenario, worker)
		op := conformanceCreationOperation(id, id+"-membership")
		var applied ApplyOperationResult
		applied, err = ApplyOperationWithResult(ctx, s, op)
		result.EventIDs = applied.EventIDs
		if err == nil {
			_, err = s.ProjectsForProduct(ctx, id)
		}
	case "same":
		op := conformanceCreationOperation("same-conformance", fmt.Sprintf("same-membership-%d", worker))
		var applied ApplyOperationResult
		applied, err = ApplyOperationWithResult(ctx, s, op)
		result.EventIDs = applied.EventIDs
	case "lifecycle_relations":
		op := lifecycleRelationOperation(worker)
		var applied ApplyOperationResult
		applied, err = ApplyOperationWithResult(ctx, s, op)
		result.EventIDs = applied.EventIDs
	case "fence":
		claim := testClaim("conformance-fence", fmt.Sprintf("worker-claim-%d", worker))
		claim.PrincipalRef = fmt.Sprintf("agent-%d", worker)
		claim.RequestID = fmt.Sprintf("fence-request-%d", worker)
		var fence FenceResult
		fence, err = claimStepAuthorizedWithRegistry(ctx, s, claim, false, nil, nil)
		result.OperationIDs = []string{fence.OpID}
	case "stale_completion":
		req := completionRequest("conformance-stale", 1, fmt.Sprintf("stale-key-%d", worker), `{"stale":true}`)
		var fence FenceResult
		fence, err = completeStepWithRegistry(ctx, s, req, false, nil)
		result.OperationIDs = []string{fence.OpID}
	case "idempotent":
		req := completionRequest("conformance-idempotent", 1, "same-result", `{"accepted":true}`)
		var fence FenceResult
		fence, err = completeStepWithRegistry(ctx, s, req, false, nil)
		result.OperationIDs = []string{fence.OpID}
		if fence.Replayed {
			result.Outcome = outcomeDuplicate
		}
	case "idempotency_conflict":
		req := completionRequest("conformance-idempotency-conflict", 1, "same-conflict", fmt.Sprintf(`{"worker":%d}`, worker))
		var fence FenceResult
		fence, err = completeStepWithRegistry(ctx, s, req, false, nil)
		result.OperationIDs = []string{fence.OpID}
	case "step_read":
		_, err = Step(ctx, s, "conformance-fence")
	case "duplicate":
		retry := conformanceCreationOperation("duplicate-conformance", "duplicate-membership")
		retry.ExpectedVersions = nil
		_, err = ApplyOperationWithResult(ctx, s, retry)
	case "payload_compatibility":
		current := workCreatedCurrentVersion()
		version := current
		result.Profile = "current"
		switch {
		case worker < 3:
			version = 1
			result.Profile = "legacy_v1"
		case worker >= 3 && worker < 5:
			version = 2
			result.Profile = "legacy_v2"
		case worker >= 7:
			version = current + 1
			result.Profile = "newer"
		}
		op := compatibilityOperation(worker, version)
		var applied ApplyOperationResult
		applied, err = ApplyOperationWithResult(ctx, s, op)
		result.EventIDs = applied.EventIDs
	case "kill":
		op := conformanceCreationOperation("kill-conformance", "kill-membership")
		_, err = applyOperationWithResult(ctx, s, op, func() error {
			_, _ = fmt.Fprintln(os.Stdout, "AT_COMMIT_GATE")
			_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
			return nil
		})
	case durableMixedScenario:
		return runDurableMixedScenario(ctx, s, worker)
	default:
		err = errors.New("unknown conformance scenario")
	}
	if err == nil {
		if result.Outcome != outcomeDuplicate {
			result.Outcome = outcomeAccepted
		}
		return result
	}
	result.FailureKind = failureKind(err)
	result.Outcome = classifyOutcome(result.FailureKind, err, result.Outcome)
	return result
}

func runLoadScenario(ctx context.Context, s *Store, worker int, scenario string) WorkerResult {
	attempts := 100
	if scenario == "backup_load" {
		attempts = 5
	}
	result := WorkerResult{Worker: worker, Outcome: outcomeAccepted, Attempts: attempts}
	result.Samples = make([]WorkerSample, 0, attempts)
	for attempt := 0; attempt < attempts; attempt++ {
		if err := contextErr(ctx); err != nil {
			result.Outcome = outcomeError
			result.FailureKind = failureKind(err)
			break
		}
		id := fmt.Sprintf("production-%d-%d", worker, attempt)
		_, err := ApplyOperationWithResult(ctx, s, conformanceCreationOperation(id, id+"-membership"))
		sample := WorkerSample{Outcome: classifyOutcome(failureKind(err), err, outcomeAccepted), FailureKind: failureKind(err)}
		result.Samples = append(result.Samples, sample)
		if scenario == "backup_load" {
			// Spread the writes across the concurrent online backup interval.
			time.Sleep(time.Millisecond)
		}
	}
	return result
}

// durableMixedWorkerPlan builds one worker's durable operation identities for
// the durable_mixed scenario. Op IDs and idempotency keys are unique per
// worker, so the plan carries no same-entity contention; the `same` scenario
// owns that race. The claim and the completion use distinct idempotency keys.
func durableMixedWorkerPlan(worker int) (ClaimRequest, CompleteRequest) {
	opID := fmt.Sprintf("conformance-durable-mixed-%d", worker)
	claim := testClaim(opID, fmt.Sprintf("mixed-claim-%d", worker))
	completion := completionRequest(opID, 1, fmt.Sprintf("mixed-complete-%d", worker), fmt.Sprintf(`{"worker":%d}`, worker))
	return claim, completion
}

// prepareDurableMixedFixtures seeds the read targets the durable_mixed
// children use: a claimed probe operation for the fence read, a probe product
// for the membership read, and a research pack for the canonical research
// read. Everything lands in the temporary conformance database.
func prepareDurableMixedFixtures(t *testing.T, ctx context.Context, s *Store) error {
	t.Helper()
	if _, err := ApplyOperationWithResult(ctx, s, conformanceCreationOperation(durableMixedProbeProduct, durableMixedProbeProduct+"-membership")); err != nil {
		return fmt.Errorf("durable_mixed probe product: %w", err)
	}
	if _, err := ClaimStepAuthorized(ctx, s, testClaim(durableMixedProbeOp, "mixed-probe-claim"), nil); err != nil {
		return fmt.Errorf("durable_mixed probe claim: %w", err)
	}
	seedResearchWork(t, s, durableMixedResearchOwner)
	if _, err := createResearchPack(ctx, s, CreateResearchPackRequest{
		PackID:      durableMixedResearchPackID,
		OwnerWorkID: durableMixedResearchOwner,
		Revision:    ResearchRevisionInput{Question: "conformance", ScopeIn: json.RawMessage(`{}`), ScopeOut: json.RawMessage(`{}`), DoneWhen: json.RawMessage(`{}`), Method: "source"},
	}); err != nil {
		return fmt.Errorf("durable_mixed research pack: %w", err)
	}
	return nil
}

// runDurableMixedScenario performs one worker's durable_mixed plan: every
// iteration reads through the surviving public read owners, and the first and
// last iterations add the worker's durable claim and completion. The commit
// path is the durable owner's (CD-0050 D2, CD-0207 D1), so a durable commit
// that returns success is durable and waits for no WAL reader. Every
// operation appends one WorkerSample under its named profile.
func runDurableMixedScenario(ctx context.Context, s *Store, worker int) WorkerResult {
	result := WorkerResult{Worker: worker, Outcome: outcomeAccepted, Attempts: 1}
	claim, completion := durableMixedWorkerPlan(worker)
	invariant := func() WorkerResult {
		result.Outcome = outcomeInvariantViolation
		result.FailureKind = KindInvariantViolation
		return result
	}
	for iteration := 0; iteration < durableMixedIterations; iteration++ {
		_, err := Step(ctx, s, durableMixedProbeOp)
		if !appendMixedSample(&result, mixedSampleProfileRead, err) {
			return result
		}
		_, err = s.ProjectsForProduct(ctx, durableMixedProbeProduct)
		if !appendMixedSample(&result, mixedSampleProfileRead, err) {
			return result
		}
		_, err = s.ReadResearchPack(ctx, ResearchReadRequest{PackID: durableMixedResearchPackID, Limit: 100})
		if !appendMixedSample(&result, mixedSampleProfileRead, err) {
			return result
		}
		switch iteration {
		case 0:
			fence, err := ClaimStepAuthorized(ctx, s, claim, nil)
			if !appendMixedSample(&result, mixedSampleProfileDurable, err) {
				return result
			}
			if fence.AttemptEpoch != 1 || fence.Replayed {
				return invariant()
			}
		case durableMixedIterations - 1:
			fence, err := CompleteStep(ctx, s, completion)
			if !appendMixedSample(&result, mixedSampleProfileDurable, err) {
				return result
			}
			if fence.Replayed || fence.ResultKind != ResultCompleted {
				return invariant()
			}
			readback, err := Step(ctx, s, completion.OpID)
			if !appendMixedSample(&result, mixedSampleProfileRead, err) {
				return result
			}
			if readback.AttemptEpoch != completion.AttemptEpoch || readback.ResultKind != ResultCompleted || readback.ResultPayload != completion.ResultPayload {
				return invariant()
			}
		}
	}
	if got := s.DurableCommits(); got != durableMixedDurableOpsPerWorker {
		return invariant()
	}
	result.DurableCommits = int64(s.DurableCommits())
	return result
}

// appendMixedSample records one mixed operation sample. It returns false when
// err is non-nil and classifies the result outcome from that failure.
func appendMixedSample(result *WorkerResult, profile string, err error) bool {
	sample := WorkerSample{
		Outcome:     classifyOutcome(failureKind(err), err, outcomeAccepted),
		FailureKind: failureKind(err),
		Profile:     profile,
	}
	result.Samples = append(result.Samples, sample)
	if err != nil {
		result.FailureKind = sample.FailureKind
		result.Outcome = sample.Outcome
		return false
	}
	return true
}

// verifyDurableMixedEffects proves from the parent handle that each worker's
// durable commits landed and their effects are visible to a fresh read: the
// completion is the newest attempt with its payload, and the probe research
// pack still reads current.
func verifyDurableMixedEffects(t *testing.T, ctx context.Context, s *Store, workers int) error {
	t.Helper()
	for worker := 0; worker < workers; worker++ {
		claim, completion := durableMixedWorkerPlan(worker)
		fence, err := Step(ctx, s, claim.OpID)
		if err != nil {
			return fmt.Errorf("durable_mixed worker %d effect read: %w", worker, err)
		}
		if fence.AttemptEpoch != completion.AttemptEpoch || fence.ResultKind != ResultCompleted || fence.ResultPayload != completion.ResultPayload {
			return fmt.Errorf("durable_mixed worker %d effect = epoch %d kind %q payload %q, want epoch %d kind %q payload %q", worker, fence.AttemptEpoch, fence.ResultKind, fence.ResultPayload, completion.AttemptEpoch, ResultCompleted, completion.ResultPayload)
		}
		if got := maxAttemptEpoch(t, s, claim.OpID); got != completion.AttemptEpoch {
			return fmt.Errorf("durable_mixed worker %d newest attempt epoch = %d, want %d", worker, got, completion.AttemptEpoch)
		}
	}
	pack, err := s.ReadResearchPack(ctx, ResearchReadRequest{PackID: durableMixedResearchPackID, Limit: 100})
	if err != nil {
		return fmt.Errorf("durable_mixed probe pack read: %w", err)
	}
	if pack.OwnerWorkID != durableMixedResearchOwner || pack.CurrentRevision != 1 {
		return fmt.Errorf("durable_mixed probe pack = owner %q revision %d, want owner %q revision 1", pack.OwnerWorkID, pack.CurrentRevision, durableMixedResearchOwner)
	}
	return nil
}

func durableMixedExpectedReadSamples(workers int) int64 {
	return int64(workers) * (durableMixedIterations*durableMixedReadsPerIteration + durableMixedReadBacksPerWorker)
}

func durableMixedExpectedDurableSamples(workers int) int64 {
	return int64(workers) * durableMixedDurableOpsPerWorker
}

// summarizeMixedPopulation derives the separately named mixed population from
// the durable_mixed samples alone. Samples carrying any other profile are not
// part of this population.
func summarizeMixedPopulation(results []WorkerResult) conformanceMixedPopulation {
	population := conformanceMixedPopulation{Workers: len(results)}
	for _, result := range results {
		population.DurableCommits += result.DurableCommits
		for _, sample := range result.Samples {
			switch sample.Profile {
			case mixedSampleProfileRead:
				population.ReadSamples++
			case mixedSampleProfileDurable:
				population.DurableSamples++
			default:
				continue
			}
			switch sample.Outcome {
			case outcomeAccepted:
				population.Accepted++
			case outcomeBusyEscaped:
				population.BusyEscaped++
			case outcomeError:
				population.Errors++
			}
		}
	}
	return population
}

// validateMixedPopulation checks the structural invariants of the mixed
// population: every worker contributed its planned operations and commits.
// Outcome classification is not structural: busy escapes and errors flow
// through the existing base gate semantics (CD-0045 D2 binds zero-escape to
// the accepted population only).
func validateMixedPopulation(population conformanceMixedPopulation, workers int) error {
	if population.Workers != workers {
		return fmt.Errorf("workers = %d, want %d", population.Workers, workers)
	}
	if want := durableMixedExpectedReadSamples(workers); population.ReadSamples != want {
		return fmt.Errorf("read samples = %d, want %d", population.ReadSamples, want)
	}
	if want := durableMixedExpectedDurableSamples(workers); population.DurableSamples != want {
		return fmt.Errorf("durable samples = %d, want %d", population.DurableSamples, want)
	}
	if want := int64(workers) * durableMixedDurableOpsPerWorker; population.DurableCommits != want {
		return fmt.Errorf("durable commits = %d, want %d", population.DurableCommits, want)
	}
	if population.ReadSamples == 0 || population.DurableSamples == 0 {
		return fmt.Errorf("empty mixed sample population: %+v", population)
	}
	return nil
}

func classifyOutcome(kind FailureKind, err error, prior WorkerOutcome) WorkerOutcome {
	if err == nil {
		return prior
	}
	switch kind {
	case KindVersionConflict:
		return outcomeVersionConflict
	case KindDuplicateEvent:
		return outcomeDuplicate
	case KindStaleAttempt:
		return outcomeStaleAttempt
	case KindIdempotencyConflict:
		return outcomeIdempotencyConflict
	case KindUnsupportedPayloadVersion:
		return outcomeUnsupportedPayload
	}
	if strings.Contains(strings.ToLower(err.Error()), "busy") {
		return outcomeBusyEscaped
	}
	return outcomeError
}

func completionRequest(opID string, epoch int64, key, payload string) CompleteRequest {
	return CompleteRequest{OpID: opID, AttemptEpoch: epoch, ResultKind: ResultCompleted, ResultPayload: payload, PrincipalRef: "agent-shared", Tool: "execute", IdempotencyKey: key, RequestID: key + "-request", ObservedAt: time.Now().UTC(), ResultEventIDs: []string{"effect-" + key}}
}

func lifecycleRelationOperation(worker int) Operation {
	prefix := fmt.Sprintf("lifecycle-%d", worker)
	product, project, first, second := prefix+"-product", prefix+"-project", prefix+"-first", prefix+"-second"
	return Operation{Events: []Event{
		productCreatedEvent(product, prefix+"-product-created"),
		projectCreatedEvent(project, prefix+"-project-created"),
		membershipEvent(prefix+"-product-project", "product_project.added", SubjectProduct, product, map[string]any{"product_id": product, "project_id": project, "role": "primary", "reason": "conformance", "expected_version": 1, "resulting_version": 2}),
		workCreatedEvent(first, prefix+"-first-created"),
		workCreatedEvent(second, prefix+"-second-created"),
		membershipEvent(prefix+"-first-project", "work_project.added", SubjectWorkItem, first, map[string]any{"work_id": first, "project_id": project, "role": "primary", "reason": "conformance", "expected_version": 1, "resulting_version": 2}),
		membershipEvent(prefix+"-second-project", "work_project.added", SubjectWorkItem, second, map[string]any{"work_id": second, "project_id": project, "role": "secondary", "reason": "conformance", "expected_version": 1, "resulting_version": 2}),
		relationAddedEvent(prefix+"-relation", "blocks", first, second, 2, 3),
		workTransitionEvent(prefix+"-complete", first, "needed", "completed", 3, 4),
	}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProduct, product): 0, VersionRef(SubjectProject, project): 0, VersionRef(SubjectWorkItem, first): 0, VersionRef(SubjectWorkItem, second): 0}}
}

func compatibilityOperation(worker, payloadVersion int) Operation {
	id := fmt.Sprintf("compat-%d", worker)
	work := workCreatedEvent(id+"-work", id+"-work")
	work.PayloadVersion = payloadVersion
	if payloadVersion == 1 {
		work.Payload = []byte(fmt.Sprintf(`{"kind":"task","title":"%s","priority":10}`, id+"-work"))
	}
	return Operation{Events: []Event{
		productCreatedEvent(id+"-product", id+"-product"),
		projectCreatedEvent(id+"-project", id+"-project"),
		membershipEvent(id+"-product-project", "product_project.added", SubjectProduct, id+"-product", map[string]any{"product_id": id + "-product", "project_id": id + "-project", "role": "primary", "reason": "compatibility", "expected_version": 1, "resulting_version": 2}),
		work,
		membershipEvent(id+"-work-project", "work_project.added", SubjectWorkItem, id+"-work", map[string]any{"work_id": id + "-work", "project_id": id + "-project", "role": "primary", "reason": "compatibility", "expected_version": 1, "resulting_version": 2}),
	}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProduct, id+"-product"): 0, VersionRef(SubjectProject, id+"-project"): 0, VersionRef(SubjectWorkItem, id+"-work"): 0}}
}

func assertLifecycleRelationEffects(ctx context.Context, s *Store) error {
	for worker := 0; worker < 10; worker++ {
		prefix := fmt.Sprintf("lifecycle-%d", worker)
		var events int
		if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT count(*) FROM effect_receipts WHERE event_id LIKE ?`, prefix+"-%").Scan(&events); err != nil {
			return err
		}
		if events != 9 {
			return fmt.Errorf("lifecycle worker %d emitted %d events, want 9", worker, events)
		}
		var lifecycle string
		var version int64
		if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT lifecycle,version FROM work_items WHERE id=?`, prefix+"-first").Scan(&lifecycle, &version); err != nil {
			return err
		}
		if lifecycle != "completed" || version != 4 {
			return fmt.Errorf("lifecycle worker %d first projection = %s/v%d, want completed/v4", worker, lifecycle, version)
		}
		if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT count(*) FROM relations WHERE work_id_from=? AND work_id_to=? AND kind='blocks'`, prefix+"-first", prefix+"-second").Scan(&events); err != nil {
			return err
		}
		if events != 1 {
			return fmt.Errorf("lifecycle worker %d relation count = %d, want 1", worker, events)
		}
		if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT count(*) FROM product_projects WHERE product_id=? AND role='primary'`, prefix+"-product").Scan(&events); err != nil {
			return err
		}
		if events != 1 {
			return fmt.Errorf("lifecycle worker %d Product primary count = %d, want 1", worker, events)
		}
	}
	var orphan, cycles, duplicatePrimary int
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT count(*) FROM relations r LEFT JOIN work_items f ON f.id=r.work_id_from LEFT JOIN work_items t ON t.id=r.work_id_to WHERE f.id IS NULL OR t.id IS NULL`).Scan(&orphan); err != nil {
		return err
	}
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `WITH RECURSIVE reach(start,node) AS (SELECT work_id_from,work_id_to FROM relations UNION SELECT reach.start,r.work_id_to FROM reach JOIN relations r ON r.work_id_from=reach.node) SELECT count(*) FROM reach WHERE start=node`).Scan(&cycles); err != nil {
		return err
	}
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT (SELECT count(*) FROM (SELECT product_id FROM product_projects WHERE role='primary' GROUP BY product_id HAVING count(*)>1)) + (SELECT count(*) FROM (SELECT work_id FROM work_projects WHERE role='primary' GROUP BY work_id HAVING count(*)>1))`).Scan(&duplicatePrimary); err != nil {
		return err
	}
	if orphan != 0 || cycles != 0 || duplicatePrimary != 0 {
		return fmt.Errorf("lifecycle/relation invariants orphan=%d cycles=%d duplicate_primary=%d", orphan, cycles, duplicatePrimary)
	}
	return nil
}

type childResult struct {
	ready  bool
	result WorkerResult
	err    error
}

func runConformanceWorkers(ctx context.Context, path, scenario string) ([]WorkerResult, error) {
	return runConformanceWorkersWithHook(ctx, path, scenario, nil)
}

func runConformanceWorkersWithHook(ctx context.Context, path, scenario string, beforeRun func()) ([]WorkerResult, error) {
	childCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	type child struct {
		cmd   *exec.Cmd
		stdin io.WriteCloser
		lines chan childResult
	}
	children := make([]child, 0, 10)
	for worker := 0; worker < 10; worker++ {
		cmd := exec.CommandContext(childCtx, os.Args[0], "-test.run=^TestConformanceWorker$", "-test.v=false")
		cmd.Env = append(os.Environ(), conformanceWorkerEnv+"=1", "TEST_CONCORD_CONFORMANCE_DB="+path, fmt.Sprintf("TEST_CONCORD_CONFORMANCE_WORKER_ID=%d", worker), "TEST_CONCORD_CONFORMANCE_SCENARIO="+scenario)
		stdin, err := cmd.StdinPipe()
		if err != nil {
			return nil, err
		}
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			return nil, err
		}
		if err := cmd.Start(); err != nil {
			return nil, err
		}
		lines := make(chan childResult, 2)
		go readChildLines(stdout, lines)
		children = append(children, child{cmd: cmd, stdin: stdin, lines: lines})
	}
	for _, child := range children {
		select {
		case message := <-child.lines:
			if !message.ready {
				return nil, fmt.Errorf("worker did not become ready: %v", message.err)
			}
		case <-childCtx.Done():
			return nil, childCtx.Err()
		}
	}
	if beforeRun != nil {
		beforeRun()
	}
	for _, child := range children {
		if _, err := fmt.Fprintln(child.stdin, "RUN"); err != nil {
			return nil, err
		}
		_ = child.stdin.Close()
	}
	results := make([]WorkerResult, 0, 10)
	for _, child := range children {
		select {
		case message := <-child.lines:
			if message.err != nil {
				return nil, message.err
			}
			results = append(results, message.result)
		case <-childCtx.Done():
			return nil, childCtx.Err()
		}
	}
	for _, child := range children {
		if err := child.cmd.Wait(); err != nil {
			return nil, err
		}
	}
	sort.Slice(results, func(i, j int) bool { return results[i].Worker < results[j].Worker })
	return results, nil
}

func readChildLines(stdout io.Reader, output chan<- childResult) {
	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() {
		output <- childResult{err: errors.New("worker closed stdout before READY")}
		return
	}
	var ready struct {
		Ready bool `json:"ready"`
	}
	if err := json.Unmarshal(scanner.Bytes(), &ready); err != nil || !ready.Ready {
		output <- childResult{err: errors.New("malformed READY line")}
		return
	}
	output <- childResult{ready: true}
	if !scanner.Scan() {
		output <- childResult{err: errors.New("worker closed stdout before result")}
		return
	}
	var result WorkerResult
	if err := json.Unmarshal(scanner.Bytes(), &result); err != nil {
		output <- childResult{err: err}
		return
	}
	output <- childResult{result: result}
}

func failureKind(err error) FailureKind {
	var f *Failure
	if errors.As(err, &f) {
		return f.Kind
	}
	return ""
}

func conformanceCreationOperation(id, membershipID string) Operation {
	return Operation{Events: []Event{productCreatedEvent(id, id+"-product"), projectCreatedEvent(id+"-project", id+"-project-created"), membershipEvent(membershipID, "product_project.added", SubjectProduct, id, map[string]any{"product_id": id, "project_id": id + "-project", "role": "primary", "reason": "conformance", "expected_version": 1, "resulting_version": 2})}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProduct, id): 0, VersionRef(SubjectProject, id+"-project"): 0}}
}

func killBeforeCommitAndRetry(ctx context.Context, path string, s *Store) error {
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestConformanceWorker$", "-test.v=false")
	cmd.Env = append(os.Environ(), conformanceWorkerEnv+"=1", "TEST_CONCORD_CONFORMANCE_DB="+path, "TEST_CONCORD_CONFORMANCE_WORKER_ID=99", "TEST_CONCORD_CONFORMANCE_SCENARIO=kill")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() {
		return errors.New("kill worker closed stdout before READY")
	}
	if _, err := fmt.Fprintln(stdin, "RUN"); err != nil {
		return err
	}
	if !scanner.Scan() || strings.TrimSpace(scanner.Text()) != "AT_COMMIT_GATE" {
		return errors.New("kill worker did not reach real precommit gate")
	}
	if err := cmd.Process.Kill(); err != nil {
		return err
	}
	_ = stdin.Close()
	if err := cmd.Wait(); err == nil {
		return errors.New("kill worker unexpectedly committed")
	}
	if got, err := countEffectReceipts(s, "kill-membership"); err != nil {
		return err
	} else if got != 0 {
		return fmt.Errorf("killed transaction left %d membership events", got)
	}
	if _, err := ApplyOperationWithResult(ctx, s, conformanceCreationOperation("kill-conformance", "kill-membership")); err != nil {
		return fmt.Errorf("retry after killed commit gate: %w", err)
	}
	return nil
}

func runConcurrentBackup(t *testing.T, ctx context.Context, path string) (string, BackupManifest, []WorkerResult) {
	t.Helper()
	backupPath := filepath.Join(t.TempDir(), "concurrent-snapshot.db")
	type backupResult struct {
		manifest BackupManifest
		err      error
	}
	result := make(chan backupResult, 1)
	workersDone := make(chan struct{})
	backupStarted := make(chan struct{})
	backupCtx := context.WithValue(ctx, backupStartedContextKey{}, func() { close(backupStarted) })
	go func() {
		<-workersDone
		source, err := Open(backupCtx, path)
		if err != nil {
			result <- backupResult{err: err}
			return
		}
		manifest, err := Backup(backupCtx, source, backupPath)
		_ = source.Close()
		result <- backupResult{manifest: manifest, err: err}
	}()
	// The worker hook releases all ten ready processes immediately before RUN;
	// backup and their commits therefore share this live database interval.
	workers, err := runConformanceWorkersWithHook(ctx, path, "backup_load", func() {
		close(workersDone)
		select {
		case <-backupStarted:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	assertTenWorkers(t, path, workers)
	backup := <-result
	if backup.err != nil {
		t.Fatal(backup.err)
	}
	return backupPath, backup.manifest, workers
}

func verifyRestoredBackup(t *testing.T, ctx context.Context, backupPath string, manifest BackupManifest) error {
	t.Helper()
	restoredPath := filepath.Join(t.TempDir(), "restored.db")
	verified, err := RestoreBackup(ctx, backupPath, restoredPath)
	if err != nil {
		return err
	}
	if verified.SourceSnapshot.SchemaVersion != manifest.SchemaVersion || verified.RestoredDatabase.EventWatermark != manifest.EventWatermark {
		return fmt.Errorf("restored manifest = %+v, want schema=%d watermark=%d", verified, manifest.SchemaVersion, manifest.EventWatermark)
	}
	if got, err := storeRevisionAtPath(ctx, restoredPath); err != nil || got != manifest.EventWatermark {
		return fmt.Errorf("restored watermark = %d, want %d (err=%v)", got, manifest.EventWatermark, err)
	}
	restoredStore, err := Open(ctx, restoredPath)
	if err != nil {
		return err
	}
	defer restoredStore.Close()
	integrity, quick, foreign, err := verifySQLiteTriple(ctx, restoredStore.DatabaseForTesting())
	if err != nil {
		return err
	}
	if integrity != "ok" || quick != "ok" || len(foreign) != 0 {
		return fmt.Errorf("restored SQLite triple verification failed: integrity=%q quick=%q foreign=%v", integrity, quick, foreign)
	}
	if err := validateMembershipInvariants(ctx, restoredStore.DatabaseForTesting()); err != nil {
		return err
	}
	return nil
}

func storeRevisionAtPath(ctx context.Context, path string) (int64, error) {
	db, err := sqlOpenReadOnly(ctx, path)
	if err != nil {
		return 0, err
	}
	defer db.Close()
	return storeRevision(ctx, db)
}

func sqlOpenReadOnly(_ context.Context, path string) (*sql.DB, error) {
	// Keep verification on the same driver settings as VerifyBackup without
	// opening a Store that could run migrations on an untrusted candidate.
	return sql.Open(driverName, readOnlyDataSource(path))
}

func assertTenWorkers(t *testing.T, path string, results []WorkerResult) {
	t.Helper()
	if len(results) != 10 || uniquePIDs(results) != 10 {
		t.Fatalf("worker matrix used %d results and %d distinct PIDs, want 10/10", len(results), uniquePIDs(results))
	}
	want := dbIdentity(path)
	for _, result := range results {
		if result.DBIdentity != want {
			t.Fatalf("worker %d did not use canonical database identity", result.Worker)
		}
	}
}

func dbIdentity(path string) string {
	sum := sha256.Sum256([]byte(path))
	return hex.EncodeToString(sum[:])
}

func countEffectReceipts(s *Store, eventID string) (int, error) {
	var count int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM effect_receipts WHERE event_id=?`, eventID).Scan(&count); err != nil {
		return 0, fmt.Errorf("read effect receipt %s: %w", eventID, err)
	}
	return count, nil
}

func maxAttemptEpoch(t *testing.T, s *Store, opID string) int64 {
	t.Helper()
	var epoch int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT COALESCE(MAX(attempt_epoch),0) FROM durable_operations WHERE op_id=?`, opID).Scan(&epoch); err != nil {
		t.Fatal(err)
	}
	return epoch
}

func emitWorker(result WorkerResult) { _ = json.NewEncoder(os.Stdout).Encode(result) }
func mustIntEnv(name string) int {
	var value int
	_, _ = fmt.Sscanf(os.Getenv(name), "%d", &value)
	return value
}
func countOutcome(results []WorkerResult, outcome WorkerOutcome) int {
	count := 0
	for _, result := range results {
		if result.Outcome == outcome {
			count++
		}
	}
	return count
}
func uniquePIDs(results []WorkerResult) int {
	pids := map[int]struct{}{}
	for _, result := range results {
		pids[result.PID] = struct{}{}
	}
	return len(pids)
}

func addScenario(report *ConformanceReport, name string, results []WorkerResult) {
	counts := map[WorkerOutcome]int{}
	for _, result := range expandWorkerResults(results) {
		counts[result.Outcome]++
	}
	report.Scenarios[name] = counts
}

func addPayloadProfiles(report *ConformanceReport, results []WorkerResult) {
	if report.PayloadProfiles == nil {
		report.PayloadProfiles = map[string]map[int]WorkerOutcome{}
	}
	for _, result := range results {
		if report.PayloadProfiles[result.Profile] == nil {
			report.PayloadProfiles[result.Profile] = map[int]WorkerOutcome{}
		}
		report.PayloadProfiles[result.Profile][result.PID] = result.Outcome
	}
}

func expandWorkerResults(results []WorkerResult) []WorkerResult {
	expanded := make([]WorkerResult, 0, len(results))
	for _, result := range results {
		if len(result.Samples) == 0 {
			expanded = append(expanded, result)
			continue
		}
		for _, sample := range result.Samples {
			expanded = append(expanded, WorkerResult{Worker: result.Worker, PID: result.PID, DBIdentity: result.DBIdentity, Outcome: sample.Outcome, FailureKind: sample.FailureKind, Attempts: 1, Profile: sample.Profile})
		}
	}
	return expanded
}

func mustJSON(value any) string { data, _ := json.Marshal(value); return string(data) }

// workCreatedCurrentVersion is the registered current work.created payload
// version; the payload profiles derive from it.
func workCreatedCurrentVersion() int {
	registration, ok := registeredEventKind("work.created")
	if !ok {
		panic("work.created is not registered")
	}
	return registration.CurrentVersion
}

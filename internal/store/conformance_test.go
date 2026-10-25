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
	conformanceAttemptsEnv         = "CONCORD_CONFORMANCE_ATTEMPTS"
	conformanceUnpacedEnv          = "CONCORD_CONFORMANCE_UNPACED"
	conformanceAcceptanceRunnerEnv = "CONCORD_ACCEPTANCE_RUNNER"
	githubActionsEnv               = "GITHUB_ACTIONS"
	acceptanceRunnerSignalExpected = "1"
	conformanceP99TargetMS         = int64(100)
)

// populationAuthorityReason names the closed set of reasons a population
// authority may resolve to. Reasons are stable strings: tests and downstream
// readers match on them.
const (
	populationAuthorityReasonDiagnosticEntryPoint      = "diagnostic_entry_point"
	populationAuthorityReasonRequiredCheckSignalAbsent = "required_check_signal_absent"
	populationAuthorityReasonRequiredCheck             = "required_check"
)

// productionLikePaceInterval paces each long-profile worker at a constant rate
// calibrated to the measured production envelope CD-0011 records (below 0.1
// writes/second system-wide). 100 ms per worker is 10 writes/second per worker
// and 100 writes/second system-wide: 1000x the measured envelope with the
// interval equal to the P99 target, so lock-hold regressions still trip the
// accepted gate. CONCORD_CONFORMANCE_UNPACED=1 restores max-rate spin as
// diagnostic-only stress evidence; the acceptance profile refuses it.
const productionLikePaceInterval = 100 * time.Millisecond

// durable_mixed is the separately named scenario that mixes the surviving
// public read owners with consequential durable commits (CD-0050 D2, CD-0207
// D1) on one temporary database. Its samples form the separately named mixed
// population; they never feed the production-like latency denominator or the
// falsifier verdict (CD-0046 owns that provenance).
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

// populationAuthority classifies a run as eligible to emit an accepted
// falsifier verdict (`accepted`) or as bound to remain diagnostic
// (`diagnostic`). Only `accepted` runs may report `passed` or `fired`; every
// other run, including ones that exceed the threshold, must remain
// `inconclusive`.
type populationAuthority string

const (
	populationAuthorityDiagnostic populationAuthority = "diagnostic"
	populationAuthorityAccepted   populationAuthority = "accepted"
)

// resolvePopulationAuthority decides whether a run is allowed to emit an
// accepted verdict.
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
// Local invocations (no `GITHUB_ACTIONS`) report `inconclusive` and continue.
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

type sustainedThresholdStatus string

const (
	thresholdInconclusive sustainedThresholdStatus = "inconclusive"
	thresholdMet          sustainedThresholdStatus = "met"
	thresholdExceeded     sustainedThresholdStatus = "exceeded"
)

type falsifierStatus string

const (
	falsifierInconclusive falsifierStatus = "inconclusive"
	falsifierPassed       falsifierStatus = "passed"
	falsifierFired        falsifierStatus = "fired"
)

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

// WorkerSample is one operation's exact observer sample. Long profiles carry
// one sample per short write rather than hiding 100 attempts behind one worker
// process result.
type WorkerSample struct {
	Outcome          WorkerOutcome `json:"outcome"`
	FailureKind      FailureKind   `json:"failure_kind,omitempty"`
	BeginWaitMS      int64         `json:"begin_wait_ms"`
	CommitDurationMS int64         `json:"commit_duration_ms"`
	WallDurationMS   int64         `json:"wall_duration_ms"`
	Profile          string        `json:"profile,omitempty"`
}

// WorkerResult is the bounded line-delimited result emitted by one child.
type WorkerResult struct {
	Worker       int           `json:"worker"`
	PID          int           `json:"pid"`
	DBIdentity   string        `json:"db_identity"`
	EventIDs     []string      `json:"event_ids,omitempty"`
	OperationIDs []string      `json:"operation_ids,omitempty"`
	Outcome      WorkerOutcome `json:"outcome"`
	FailureKind  FailureKind   `json:"failure_kind,omitempty"`
	BeginWaitMS  int64         `json:"begin_wait_ms"`
	// QueueDurationMS remains as a compatibility alias for old evidence. It is
	// always the exact begin-wait sample, never a zero placeholder.
	QueueDurationMS  int64          `json:"queue_duration_ms"`
	WallDurationMS   int64          `json:"wall_duration_ms"`
	CommitDurationMS int64          `json:"commit_duration_ms"`
	Attempts         int            `json:"attempts,omitempty"`
	Profile          string         `json:"profile,omitempty"`
	Samples          []WorkerSample `json:"samples,omitempty"`
	// DurableCommits counts the durable transactions this child committed
	// through the durable owner (CD-0207 D1). It is set only by the
	// durable_mixed scenario.
	DurableCommits int64 `json:"durable_commits,omitempty"`
}

type latencySummary struct {
	Population int64 `json:"population"`
	P50MS      int64 `json:"p50_ms"`
	P99MS      int64 `json:"p99_ms"`
	MaxMS      int64 `json:"max_ms"`
}

type conformancePopulations struct {
	AllAttempts      int64 `json:"all_attempts"`
	AcceptedWrites   int64 `json:"accepted_writes"`
	BeginWaitSamples int64 `json:"begin_wait_samples"`
	CommitSamples    int64 `json:"commit_samples"`
	RaceInstrumented bool  `json:"race_instrumented"`
	ProductionLike   bool  `json:"production_like"`
}

// conformanceMixedPopulation is the separately named sample population for
// the durable_mixed scenario. It counts the public read samples and the
// durable commit samples on their own latency summaries. It is informational:
// it carries no threshold, no falsifier verdict, and no authority of its own,
// and its samples never redefine the accepted latency denominator (CD-0046).
type conformanceMixedPopulation struct {
	Workers                 int            `json:"workers"`
	ReadSamples             int64          `json:"read_samples"`
	DurableSamples          int64          `json:"durable_samples"`
	Accepted                int64          `json:"accepted"`
	BusyEscaped             int64          `json:"busy_escaped"`
	Errors                  int64          `json:"errors"`
	DurableCommits          int64          `json:"durable_commits"`
	ReadWallLatency         latencySummary `json:"read_wall_latency"`
	DurableWallLatency      latencySummary `json:"durable_wall_latency"`
	DurableBeginWaitLatency latencySummary `json:"durable_begin_wait_latency"`
	DurableCommitLatency    latencySummary `json:"durable_commit_latency"`
}

// ConformanceReport keeps correctness ahead of latency and names every timing
// population explicitly. Paths are intentionally excluded from public output.
type ConformanceReport struct {
	Workers                int                      `json:"workers"`
	Attempts               int                      `json:"attempts"`
	Counts                 map[WorkerOutcome]int    `json:"counts"`
	Lost                   int                      `json:"lost"`
	UnexpectedDupes        int                      `json:"unexpected_duplicates"`
	InvariantViolations    int                      `json:"invariant_violations"`
	BusyEscaped            int                      `json:"busy_escaped"`
	CorrectnessPassed      bool                     `json:"correctness_passed"`
	P99TargetMS            int64                    `json:"p99_target_ms"`
	ProductionLike         bool                     `json:"production_like"`
	ProductionLikeAttempts int                      `json:"production_like_attempts"`
	ProductionLikeP99MS    int64                    `json:"production_like_p99_ms"`
	ControlCommitP99MS     int64                    `json:"control_commit_p99_ms"`
	VerdictQuantity        string                   `json:"verdict_quantity"`
	PaceIntervalMS         int64                    `json:"pace_interval_ms"`
	RunnerProfile          conformanceRunnerProfile `json:"runner_profile"`
	AcceptancePopulation   bool                     `json:"acceptance_population"`
	// PopulationAuthority and PopulationAuthorityReason describe who may take
	// the falsifier verdict seriously. They are derived from the resolved
	// authority, not the profile literal, so a reader of any report can tell
	// why a verdict was or was not accepted. See CD-0046.
	PopulationAuthority       populationAuthority      `json:"population_authority"`
	PopulationAuthorityReason string                   `json:"population_authority_reason"`
	ThresholdStatus           sustainedThresholdStatus `json:"threshold_status"`
	FalsifierStatus           falsifierStatus          `json:"falsifier_status"`
	Populations               conformancePopulations   `json:"populations"`
	WallLatency               latencySummary           `json:"wall_latency"`
	BeginWaitLatency          latencySummary           `json:"begin_wait_latency"`
	CommitLatency             latencySummary           `json:"commit_latency"`
	AcceptedWallLatency       latencySummary           `json:"accepted_wall_latency"`
	AcceptedBeginLatency      latencySummary           `json:"accepted_begin_latency"`
	AcceptedCommitLatency     latencySummary           `json:"accepted_commit_latency"`
	// LoadAverageOneMinute records the host's 1-minute load average as
	// provenance. It is read best-effort from /proc/loadavg and omitted when
	// unreadable. It is never a gate, threshold, or verdict ingredient.
	LoadAverageOneMinute *float64 `json:"load_average_one_minute,omitempty"`
	// Latency fields are retained as a concise compatibility view of wall time.
	Latency         latencySummary                   `json:"latency"`
	AcceptedLatency latencySummary                   `json:"accepted_latency"`
	Scenarios       map[string]map[WorkerOutcome]int `json:"scenarios"`
	PayloadProfiles map[string]map[int]WorkerOutcome `json:"payload_profiles,omitempty"`
	// MixedPopulation carries the durable_mixed sample population. The
	// production-like rounds omit it.
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
	// CI tripwire: a CI run missing the required-check signal must fail
	// visibly. Without this, a workflow that drops the env var would silently
	// downgrade the required check to advisory and could still emit `passed`
	// or `fired`. Local runs (no GITHUB_ACTIONS) are allowed to remain
	// `inconclusive` and continue.
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

	// durable_mixed (CD-0050 D2, CD-0207 D1): ten children mix the surviving
	// public read owners with durable claim and completion commits on the
	// same temporary database. The mixed samples form their own named
	// population; the ordinary write latency populations stay untouched.
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
	// Mixed correctness outcomes join the base accounting, so the existing
	// gate semantics (CD-0045 D2) keep applying to them. Their timing stays
	// out of the base latency populations.
	for _, sample := range expandWorkerResults(mixedResults) {
		report.Counts[sample.Outcome]++
		report.Attempts++
		if sample.Outcome == outcomeLost {
			report.Lost++
		}
		if sample.Outcome == outcomeInvariantViolation {
			report.InvariantViolations++
		}
		if sample.Outcome == outcomeBusyEscaped {
			report.BusyEscaped++
		}
	}

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

	// CD-0195 D3: the workload covers the operation class that held the
	// write lock. Ten workers claim and reclaim worktrees whose native git
	// mutations run slower than a write; a write transaction that spanned
	// the native work would escape the concurrent writers' busy timeout.
	results = run("worktree_claim_reclaim")
	if countOutcome(results, outcomeAccepted) != 10 {
		t.Fatalf("worktree claim/reclaim = %+v, want ten accepted", report.Scenarios["worktree_claim_reclaim"])
	}
	if countOutcome(results, outcomeBusyEscaped) != 0 {
		t.Fatalf("worktree claim/reclaim escaped SQLITE_BUSY on the conformance population: %+v (CD-0195 D3)", report.Scenarios["worktree_claim_reclaim"])
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
	populateTiming(&report, all, false)
	if report.Populations.BeginWaitSamples == 0 || report.Populations.CommitSamples == 0 || report.BeginWaitLatency.MaxMS == 0 || report.CommitLatency.MaxMS == 0 {
		t.Fatalf("timing observer produced empty queue/commit samples: %+v", report.Populations)
	}
	report.CorrectnessPassed = conformanceCorrectnessPassed(report.PopulationAuthority, report.Lost, report.InvariantViolations, report.BusyEscaped)
	if !report.CorrectnessPassed {
		t.Fatalf("conformance correctness gate failed: %+v", report)
	}
	if report.BusyEscaped > 0 && !busyEscapesBindCorrectness(report.PopulationAuthority) {
		t.Logf("busy escapes=%d tolerated on %s population (CD-0045 D2 binds zero-escape to the accepted population; D3 keeps this run inconclusive); begin-wait P99=%dms", report.BusyEscaped, report.PopulationAuthority, report.BeginWaitLatency.P99MS)
	}
	t.Log("ConformanceReport " + mustJSON(report))

	if long {
		runLongProfiles(t, ctx, root, runnerProfile, report.CommitLatency.P99MS)
	}
}

func newConformanceReport(profile conformanceRunnerProfile) ConformanceReport {
	authority, reason := resolvePopulationAuthority(profile, acceptanceRunnerSignal())
	report := ConformanceReport{
		Workers:                   10,
		Counts:                    map[WorkerOutcome]int{},
		Scenarios:                 map[string]map[WorkerOutcome]int{},
		P99TargetMS:               conformanceP99TargetMS,
		RunnerProfile:             profile,
		AcceptancePopulation:      authority == populationAuthorityAccepted,
		PopulationAuthority:       authority,
		PopulationAuthorityReason: reason,
		ThresholdStatus:           thresholdInconclusive,
		FalsifierStatus:           falsifierInconclusive,
		Populations:               conformancePopulations{RaceInstrumented: conformanceRaceInstrumented},
	}
	if load, ok := readLoadAverageOneMinute(); ok {
		report.LoadAverageOneMinute = &load
	}
	return report
}

// busyEscapesBindCorrectness reports whether a busy escape fails the
// correctness gate on this population. CD-0045 D2 requires zero escaped
// SQLITE_BUSY on the accepted agent-facing population; D3 keeps every other
// population inconclusive regardless of its numbers. A diagnostic population
// on a contended host can exceed busy_timeout without any admission defect -
// the lock holder there may be an online backup pass, whose duration host
// load stretches and which writer admission does not bound - so the escape is
// reported, logged, and left non-binding rather than converted into a
// load-coupled CI failure (issue #309). Lost writes and invariant violations
// stay unconditionally fatal; only the admission-promise term is scoped.
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

func validateLoadPacing(profile conformanceRunnerProfile, unpaced bool) error {
	if unpaced && profile == runnerProfileIsolatedAcceptance {
		return fmt.Errorf("%s max-rate spin is diagnostic-only and cannot produce an accepted falsifier verdict", conformanceUnpacedEnv)
	}
	return nil
}

func loadPaceInterval() time.Duration {
	if conformanceConfiguration.unpaced {
		return 0
	}
	return productionLikePaceInterval
}

// classifySustainedFalsifier decides what verdict the rounds produced. An
// accepted verdict is reachable only when the resolved authority is accepted:
// a diagnostic authority, even with the threshold exceeded, returns
// `inconclusive`. A paced commit-duration exceedance also requires an
// above-target commit-duration P99 in the unpaced control round. Correctness
// precedence is preserved: a failed correctness population still forces
// `inconclusive` and fails the run independently.
func classifySustainedFalsifier(authority populationAuthority, aboveTarget, rounds int, correctnessPassed bool, controlCommitP99MS int64) (sustainedThresholdStatus, falsifierStatus) {
	if rounds < 1 || aboveTarget < 0 || aboveTarget > rounds {
		return thresholdInconclusive, falsifierInconclusive
	}
	required := rounds/2 + 1
	threshold := thresholdInconclusive
	if aboveTarget >= required {
		threshold = thresholdExceeded
	} else if rounds-aboveTarget >= required {
		threshold = thresholdMet
	}
	if !correctnessPassed || authority != populationAuthorityAccepted {
		return threshold, falsifierInconclusive
	}
	switch threshold {
	case thresholdExceeded:
		if controlCommitP99MS <= conformanceP99TargetMS {
			return threshold, falsifierInconclusive
		}
		return threshold, falsifierFired
	case thresholdMet:
		return threshold, falsifierPassed
	default:
		return threshold, falsifierInconclusive
	}
}

func runLongProfiles(t *testing.T, ctx context.Context, root string, runnerProfile conformanceRunnerProfile, controlCommitP99MS int64) {
	t.Helper()
	if err := validateLoadPacing(runnerProfile, conformanceConfiguration.unpaced); err != nil {
		t.Fatal(err)
	}
	const rounds = 3
	reports := make([]ConformanceReport, 0, rounds)
	for round := 0; round < rounds; round++ {
		path := filepath.Join(root, fmt.Sprintf("production-like-%d.db", round))
		s, err := Open(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		results, err := runConformanceWorkersWithHook(ctx, path, "load", nil)
		_ = s.Close()
		if err != nil {
			t.Fatalf("long round %d: %v", round+1, err)
		}
		assertTenWorkers(t, path, results)
		report := newConformanceReport(runnerProfile)
		report.ProductionLike = true
		report.VerdictQuantity = "commit_duration_p99"
		report.ControlCommitP99MS = controlCommitP99MS
		report.Populations.ProductionLike = true
		report.Scenarios["production_like_writes"] = map[WorkerOutcome]int{}
		samples := expandWorkerResults(results)
		for _, sample := range samples {
			report.Counts[sample.Outcome]++
			report.Scenarios["production_like_writes"][sample.Outcome]++
			report.Attempts++
		}
		report.ProductionLikeAttempts = len(samples)
		report.PaceIntervalMS = durationMS(loadPaceInterval())
		populateTiming(&report, samples, true)
		report.Lost = report.Counts[outcomeLost]
		report.UnexpectedDupes = report.Counts[outcomeDuplicate]
		report.InvariantViolations = report.Counts[outcomeInvariantViolation]
		report.BusyEscaped = report.Counts[outcomeBusyEscaped]
		report.CorrectnessPassed = report.Attempts == 1000 && report.Populations.AcceptedWrites == 1000 && report.Populations.AllAttempts == 1000 && report.WallLatency.Population == 1000 && report.BeginWaitLatency.Population == 1000 && report.CommitLatency.Population == 1000 && report.AcceptedWallLatency.Population == 1000 && report.AcceptedBeginLatency.Population == 1000 && report.AcceptedCommitLatency.Population == 1000 && report.Counts[outcomeAccepted] == 1000 && report.Lost == 0 && report.UnexpectedDupes == 0 && report.InvariantViolations == 0 && (!busyEscapesBindCorrectness(report.PopulationAuthority) || report.BusyEscaped == 0) && report.Counts[outcomeError] == 0
		if report.BusyEscaped > 0 && !busyEscapesBindCorrectness(report.PopulationAuthority) {
			t.Logf("long round %d: busy escapes=%d tolerated on %s population (CD-0045 D2 binds zero-escape to the accepted population)", round+1, report.BusyEscaped, report.PopulationAuthority)
		}
		reports = append(reports, report)
	}
	above := roundsAboveCommitTarget(reports)
	correctnessPassed := true
	for _, report := range reports {
		if !report.CorrectnessPassed {
			correctnessPassed = false
		}
	}
	authority, _ := resolvePopulationAuthority(runnerProfile, acceptanceRunnerSignal())
	threshold, status := classifySustainedFalsifier(authority, above, len(reports), correctnessPassed, controlCommitP99MS)
	for round, report := range reports {
		report.ThresholdStatus = threshold
		report.FalsifierStatus = status
		t.Logf("ConformanceReport long_round=%d %s", round+1, mustJSON(report))
	}
	for round, report := range reports {
		if !report.CorrectnessPassed {
			t.Fatalf("long production-like round %d correctness gate failed: attempts=%d accepted=%d counts=%+v populations=%+v", round+1, report.Attempts, report.Counts[outcomeAccepted], report.Counts, report.Populations)
		}
	}
	if status == falsifierFired {
		t.Fatal("falsifier_status=fired: sustained production-like commit-duration P99 exceeded target on the isolated acceptance population")
	} else if threshold == thresholdExceeded {
		if correctnessPassed && authority == populationAuthorityAccepted && controlCommitP99MS <= conformanceP99TargetMS {
			t.Logf("falsifier=inconclusive: paced commit-duration overshoot with clean unpaced control (host scheduling); paced_worst_p99_ms=%d control_p99_ms=%d", worstCommitP99(reports), controlCommitP99MS)
		} else {
			t.Logf("threshold_status=exceeded: verdict_quantity=commit-duration P99 runner_profile=%s is diagnostic; accepted falsifier remains inconclusive", runnerProfile)
		}
	}
}

func worstCommitP99(reports []ConformanceReport) int64 {
	var worst int64
	for _, report := range reports {
		if report.CommitLatency.P99MS > worst {
			worst = report.CommitLatency.P99MS
		}
	}
	return worst
}

func roundsAboveCommitTarget(reports []ConformanceReport) int {
	above := 0
	for _, report := range reports {
		if report.CommitLatency.P99MS > report.P99TargetMS {
			above++
		}
	}
	return above
}

func runWorkerScenario(ctx context.Context, s *Store, worker int, scenario string) WorkerResult {
	if scenario == "load" || scenario == "backup_load" {
		return runLoadScenario(ctx, s, worker, scenario)
	}
	result := WorkerResult{Worker: worker, Outcome: outcomeError, Attempts: 1}
	started := time.Now()
	observer := &operationObserver{}
	var err error
	switch scenario {
	case "distinct", "read_write":
		id := fmt.Sprintf("conformance-%s-%d", scenario, worker)
		op := conformanceCreationOperation(id, id+"-membership")
		var applied ApplyOperationResult
		applied, err = applyOperationObserved(ctx, s, op, nil, observer)
		result.EventIDs = applied.EventIDs
		if err == nil {
			_, err = s.ProjectsForProduct(ctx, id)
		}
	case "same":
		op := conformanceCreationOperation("same-conformance", fmt.Sprintf("same-membership-%d", worker))
		var applied ApplyOperationResult
		applied, err = applyOperationObserved(ctx, s, op, nil, observer)
		result.EventIDs = applied.EventIDs
	case "lifecycle_relations":
		op := lifecycleRelationOperation(worker)
		var applied ApplyOperationResult
		applied, err = applyOperationObserved(ctx, s, op, nil, observer)
		result.EventIDs = applied.EventIDs
	case "fence":
		claim := testClaim("conformance-fence", fmt.Sprintf("worker-claim-%d", worker))
		claim.PrincipalRef = fmt.Sprintf("agent-%d", worker)
		claim.RequestID = fmt.Sprintf("fence-request-%d", worker)
		var fence FenceResult
		fence, err = claimStepObservedAuthorized(ctx, s, claim, false, observer, nil)
		result.OperationIDs = []string{fence.OpID}
	case "stale_completion":
		req := completionRequest("conformance-stale", 1, fmt.Sprintf("stale-key-%d", worker), `{"stale":true}`)
		var fence FenceResult
		fence, err = completeStep(ctx, s, req, false, observer)
		result.OperationIDs = []string{fence.OpID}
	case "idempotent":
		req := completionRequest("conformance-idempotent", 1, "same-result", `{"accepted":true}`)
		var fence FenceResult
		fence, err = completeStep(ctx, s, req, false, observer)
		result.OperationIDs = []string{fence.OpID}
		if fence.Replayed {
			result.Outcome = outcomeDuplicate
		}
	case "idempotency_conflict":
		req := completionRequest("conformance-idempotency-conflict", 1, "same-conflict", fmt.Sprintf(`{"worker":%d}`, worker))
		var fence FenceResult
		fence, err = completeStep(ctx, s, req, false, observer)
		result.OperationIDs = []string{fence.OpID}
	case "step_read":
		_, err = Step(ctx, s, "conformance-fence")
	case "duplicate":
		retry := conformanceCreationOperation("duplicate-conformance", "duplicate-membership")
		retry.ExpectedVersions = nil
		_, err = applyOperationObserved(ctx, s, retry, nil, observer)
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
		applied, err = applyOperationObserved(ctx, s, op, nil, observer)
		result.EventIDs = applied.EventIDs
	case "kill":
		op := conformanceCreationOperation("kill-conformance", "kill-membership")
		_, err = applyOperationObserved(ctx, s, op, func() error {
			_, _ = fmt.Fprintln(os.Stdout, "AT_COMMIT_GATE")
			_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
			return nil
		}, observer)
	case "worktree_claim_reclaim":
		if err := runWorktreeClaimReclaimScenario(ctx, s, worker); err != nil {
			result.FailureKind = failureKind(err)
			result.Outcome = classifyOutcome(result.FailureKind, err, result.Outcome)
			result.WallDurationMS = durationMS(time.Since(started))
			return result
		}
	case durableMixedScenario:
		return runDurableMixedScenario(ctx, s, worker)
	default:
		err = errors.New("unknown conformance scenario")
	}
	result.WallDurationMS = durationMS(time.Since(started))
	result.BeginWaitMS = durationMS(observer.beginWait)
	result.QueueDurationMS = result.BeginWaitMS
	result.CommitDurationMS = durationMS(observer.commitDuration)
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
	attempts := conformanceConfiguration.attempts
	if attempts < 1 {
		attempts = 100
	}
	result := WorkerResult{Worker: worker, Outcome: outcomeAccepted, Attempts: attempts, Profile: "production_like"}
	result.Samples = make([]WorkerSample, 0, attempts)
	pace := time.Duration(0)
	if scenario == "load" {
		pace = loadPaceInterval()
	}
	for attempt := 0; attempt < attempts; attempt++ {
		if err := contextErr(ctx); err != nil {
			result.Outcome = outcomeError
			result.FailureKind = failureKind(err)
			break
		}
		started := time.Now()
		observer := &operationObserver{}
		id := fmt.Sprintf("production-%d-%d", worker, attempt)
		_, err := applyOperationObserved(ctx, s, conformanceCreationOperation(id, id+"-membership"), nil, observer)
		sample := WorkerSample{Outcome: classifyOutcome(failureKind(err), err, outcomeAccepted), FailureKind: failureKind(err), BeginWaitMS: durationMS(observer.beginWait), CommitDurationMS: durationMS(observer.commitDuration), WallDurationMS: durationMS(time.Since(started)), Profile: "production_like"}
		result.Samples = append(result.Samples, sample)
		if scenario == "backup_load" {
			time.Sleep(time.Millisecond)
		}
		if pace > 0 {
			if remaining := pace - time.Since(started); remaining > 0 {
				timer := time.NewTimer(remaining)
				select {
				case <-ctx.Done():
					timer.Stop()
					result.Outcome = outcomeError
					result.FailureKind = failureKind(ctx.Err())
					return result
				case <-timer.C:
				}
			}
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
		started := time.Now()
		_, err := Step(ctx, s, durableMixedProbeOp)
		if !appendMixedSample(&result, mixedSampleProfileRead, started, nil, err) {
			return result
		}
		started = time.Now()
		_, err = s.ProjectsForProduct(ctx, durableMixedProbeProduct)
		if !appendMixedSample(&result, mixedSampleProfileRead, started, nil, err) {
			return result
		}
		started = time.Now()
		_, err = s.ReadResearchPack(ctx, ResearchReadRequest{PackID: durableMixedResearchPackID, Limit: 100})
		if !appendMixedSample(&result, mixedSampleProfileRead, started, nil, err) {
			return result
		}
		switch iteration {
		case 0:
			observer := &operationObserver{}
			started := time.Now()
			fence, err := claimStepObservedAuthorized(ctx, s, claim, true, observer, nil)
			if !appendMixedSample(&result, mixedSampleProfileDurable, started, observer, err) {
				return result
			}
			if fence.AttemptEpoch != 1 || fence.Replayed {
				return invariant()
			}
		case durableMixedIterations - 1:
			observer := &operationObserver{}
			started := time.Now()
			fence, err := completeStep(ctx, s, completion, true, observer)
			if !appendMixedSample(&result, mixedSampleProfileDurable, started, observer, err) {
				return result
			}
			if fence.Replayed || fence.ResultKind != ResultCompleted {
				return invariant()
			}
			started = time.Now()
			readback, err := Step(ctx, s, completion.OpID)
			if !appendMixedSample(&result, mixedSampleProfileRead, started, nil, err) {
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
func appendMixedSample(result *WorkerResult, profile string, started time.Time, observer *operationObserver, err error) bool {
	begin, commit := mixedObserverDurations(observer)
	sample := WorkerSample{
		Outcome:          classifyOutcome(failureKind(err), err, outcomeAccepted),
		FailureKind:      failureKind(err),
		BeginWaitMS:      durationMS(begin),
		CommitDurationMS: durationMS(commit),
		WallDurationMS:   durationMS(time.Since(started)),
		Profile:          profile,
	}
	result.Samples = append(result.Samples, sample)
	if err != nil {
		result.FailureKind = sample.FailureKind
		result.Outcome = sample.Outcome
		return false
	}
	return true
}

func mixedObserverDurations(observer *operationObserver) (begin, commit time.Duration) {
	if observer == nil {
		return 0, 0
	}
	return observer.beginWait, observer.commitDuration
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
	var readWalls, durableWalls, durableBegins, durableCommits []int64
	for _, result := range results {
		population.DurableCommits += result.DurableCommits
		for _, sample := range result.Samples {
			switch sample.Profile {
			case mixedSampleProfileRead:
				population.ReadSamples++
				readWalls = append(readWalls, sample.WallDurationMS)
			case mixedSampleProfileDurable:
				population.DurableSamples++
				durableWalls = append(durableWalls, sample.WallDurationMS)
				durableBegins = append(durableBegins, sample.BeginWaitMS)
				durableCommits = append(durableCommits, sample.CommitDurationMS)
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
	population.ReadWallLatency = summarizeValues(readWalls)
	population.DurableWallLatency = summarizeValues(durableWalls)
	population.DurableBeginWaitLatency = summarizeValues(durableBegins)
	population.DurableCommitLatency = summarizeValues(durableCommits)
	return population
}

// validateMixedPopulation checks the structural invariants of the mixed
// population: every worker contributed its planned samples, every latency
// summary covers its own sample population, and no sample class is empty.
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
	if population.ReadWallLatency.Population != population.ReadSamples {
		return fmt.Errorf("read latency population = %d, want %d", population.ReadWallLatency.Population, population.ReadSamples)
	}
	for _, summary := range []struct {
		name  string
		value latencySummary
	}{
		{"durable wall", population.DurableWallLatency},
		{"durable begin-wait", population.DurableBeginWaitLatency},
		{"durable commit", population.DurableCommitLatency},
	} {
		if summary.value.Population != population.DurableSamples {
			return fmt.Errorf("%s latency population = %d, want %d", summary.name, summary.value.Population, population.DurableSamples)
		}
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
		if scenario == "backup_load" {
			cmd.Env = append(cmd.Env, conformanceAttemptsEnv+"=5")
		}
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
			expanded = append(expanded, WorkerResult{Worker: result.Worker, PID: result.PID, DBIdentity: result.DBIdentity, Outcome: sample.Outcome, FailureKind: sample.FailureKind, BeginWaitMS: sample.BeginWaitMS, QueueDurationMS: sample.BeginWaitMS, CommitDurationMS: sample.CommitDurationMS, WallDurationMS: sample.WallDurationMS, Attempts: 1, Profile: sample.Profile})
		}
	}
	return expanded
}

func populateTiming(report *ConformanceReport, results []WorkerResult, productionLike bool) {
	report.Populations.AllAttempts = int64(len(results))
	report.Populations.ProductionLike = productionLike
	report.Populations.RaceInstrumented = conformanceRaceInstrumented
	accepted := make([]WorkerResult, 0, len(results))
	walls, begins, commits := make([]int64, 0, len(results)), make([]int64, 0, len(results)), make([]int64, 0, len(results))
	acceptedWalls, acceptedBegins, acceptedCommits := make([]int64, 0, len(results)), make([]int64, 0, len(results)), make([]int64, 0, len(results))
	for _, result := range results {
		walls = append(walls, result.WallDurationMS)
		begins = append(begins, result.BeginWaitMS)
		commits = append(commits, result.CommitDurationMS)
		if result.Outcome == outcomeAccepted {
			accepted = append(accepted, result)
			acceptedWalls = append(acceptedWalls, result.WallDurationMS)
			acceptedBegins = append(acceptedBegins, result.BeginWaitMS)
			acceptedCommits = append(acceptedCommits, result.CommitDurationMS)
		}
	}
	report.Populations.AcceptedWrites = int64(len(accepted))
	report.Populations.BeginWaitSamples = int64(len(begins))
	report.Populations.CommitSamples = int64(len(commits))
	report.WallLatency = summarizeValues(walls)
	report.BeginWaitLatency = summarizeValues(begins)
	report.CommitLatency = summarizeValues(commits)
	report.AcceptedWallLatency = summarizeValues(acceptedWalls)
	report.AcceptedBeginLatency = summarizeValues(acceptedBegins)
	report.AcceptedCommitLatency = summarizeValues(acceptedCommits)
	report.Latency = report.WallLatency
	report.AcceptedLatency = report.AcceptedWallLatency
	if productionLike {
		report.ProductionLikeAttempts = len(results)
		report.ProductionLikeP99MS = report.WallLatency.P99MS
	}
}

func summarizeValues(values []int64) latencySummary {
	if len(values) == 0 {
		return latencySummary{}
	}
	sorted := append([]int64(nil), values...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	percentile := func(n int) int64 {
		index := (99*n+99)/100 - 1
		if index < 0 {
			index = 0
		}
		if index >= n {
			index = n - 1
		}
		return sorted[index]
	}
	return latencySummary{Population: int64(len(sorted)), P50MS: sorted[(len(sorted)-1)/2], P99MS: percentile(len(sorted)), MaxMS: sorted[len(sorted)-1]}
}

func durationMS(value time.Duration) int64 {
	if value <= 0 {
		return 0
	}
	return int64((value + time.Millisecond - 1) / time.Millisecond)
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

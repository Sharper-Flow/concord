package store

import (
	"context"
	"strings"
	"testing"
)

func TestResolvePopulationAuthority(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name          string
		profile       conformanceRunnerProfile
		signal        string
		wantAuthority populationAuthority
		wantReason    string
	}{
		{
			name:          "diagnostic entry point with signal present",
			profile:       runnerProfileDiagnostic,
			signal:        "1",
			wantAuthority: populationAuthorityDiagnostic,
			wantReason:    populationAuthorityReasonDiagnosticEntryPoint,
		},
		{
			name:          "diagnostic entry point without signal",
			profile:       runnerProfileDiagnostic,
			signal:        "",
			wantAuthority: populationAuthorityDiagnostic,
			wantReason:    populationAuthorityReasonDiagnosticEntryPoint,
		},
		{
			name:          "unknown entry point fails closed",
			profile:       conformanceRunnerProfile("unknown"),
			signal:        "1",
			wantAuthority: populationAuthorityDiagnostic,
			wantReason:    populationAuthorityReasonDiagnosticEntryPoint,
		},
		{
			name:          "acceptance entry point with signal absent",
			profile:       runnerProfileIsolatedAcceptance,
			signal:        "",
			wantAuthority: populationAuthorityDiagnostic,
			wantReason:    populationAuthorityReasonRequiredCheckSignalAbsent,
		},
		{
			name:          "acceptance entry point with non-1 signal",
			profile:       runnerProfileIsolatedAcceptance,
			signal:        "true",
			wantAuthority: populationAuthorityDiagnostic,
			wantReason:    populationAuthorityReasonRequiredCheckSignalAbsent,
		},
		{
			name:          "acceptance entry point with signal present",
			profile:       runnerProfileIsolatedAcceptance,
			signal:        "1",
			wantAuthority: populationAuthorityAccepted,
			wantReason:    populationAuthorityReasonRequiredCheck,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gotAuthority, gotReason := resolvePopulationAuthority(tt.profile, tt.signal)
			if gotAuthority != tt.wantAuthority || gotReason != tt.wantReason {
				t.Fatalf("resolvePopulationAuthority(%q, %q) = (%q, %q), want (%q, %q)", tt.profile, tt.signal, gotAuthority, gotReason, tt.wantAuthority, tt.wantReason)
			}
		})
	}
}

func TestResolveCIRunnerTripwire(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name          string
		profile       conformanceRunnerProfile
		githubActions string
		signal        string
		wantTripwire  bool
	}{
		{
			name:          "diagnostic entry point never tripwires",
			profile:       runnerProfileDiagnostic,
			githubActions: "true",
			signal:        "",
			wantTripwire:  false,
		},
		{
			name:          "acceptance with signal present never tripwires",
			profile:       runnerProfileIsolatedAcceptance,
			githubActions: "true",
			signal:        "1",
			wantTripwire:  false,
		},
		{
			name:          "acceptance without signal outside GitHub Actions never tripwires",
			profile:       runnerProfileIsolatedAcceptance,
			githubActions: "",
			signal:        "",
			wantTripwire:  false,
		},
		{
			name:          "acceptance without signal under GitHub Actions tripwires",
			profile:       runnerProfileIsolatedAcceptance,
			githubActions: "true",
			signal:        "",
			wantTripwire:  true,
		},
		{
			name:          "acceptance with non-1 signal under GitHub Actions tripwires",
			profile:       runnerProfileIsolatedAcceptance,
			githubActions: "true",
			signal:        "true",
			wantTripwire:  true,
		},
		{
			name:          "acceptance without signal under GitHub Actions=false never tripwires",
			profile:       runnerProfileIsolatedAcceptance,
			githubActions: "false",
			signal:        "",
			wantTripwire:  false,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := resolveCIRunnerTripwire(tt.profile, tt.githubActions, tt.signal)
			if got != tt.wantTripwire {
				t.Fatalf("resolveCIRunnerTripwire(%q, %q, %q) = %t, want %t", tt.profile, tt.githubActions, tt.signal, got, tt.wantTripwire)
			}
		})
	}
}

func TestAcceptanceEntryPointLocalInvocationStaysDiagnostic(t *testing.T) {
	t.Parallel()

	profile := runnerProfileIsolatedAcceptance
	signal := ""
	githubActions := ""

	if resolveCIRunnerTripwire(profile, githubActions, signal) {
		t.Fatal("tripwire fired for local invocation; local runs must not fail visibly")
	}
	authority, reason := resolvePopulationAuthority(profile, signal)
	if authority != populationAuthorityDiagnostic || reason != populationAuthorityReasonRequiredCheckSignalAbsent {
		t.Fatalf("local invocation resolved to %q/%q, want diagnostic/required_check_signal_absent", authority, reason)
	}
}

func TestConformanceCorrectnessGateScopesBusyEscapesToAcceptedPopulation(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		authority populationAuthority
		busy      int
		want      bool
	}{
		{"accepted population escapes", populationAuthorityAccepted, 1, false},
		{"accepted population clean", populationAuthorityAccepted, 0, true},
		{"diagnostic population escapes", populationAuthorityDiagnostic, 3, true},
	}
	for _, tc := range cases {
		if got := conformanceCorrectnessPassed(tc.authority, 0, 0, tc.busy); got != tc.want {
			t.Fatalf("%s: conformanceCorrectnessPassed(%s, busy=%d) = %v, want %v", tc.name, tc.authority, tc.busy, got, tc.want)
		}
	}
}

func TestConformanceCorrectnessGateKeepsAbsoluteTermsUnconditional(t *testing.T) {
	t.Parallel()

	for _, authority := range []populationAuthority{populationAuthorityAccepted, populationAuthorityDiagnostic} {
		if conformanceCorrectnessPassed(authority, 1, 0, 0) {
			t.Fatalf("%s: a lost write passed the correctness gate", authority)
		}
		if conformanceCorrectnessPassed(authority, 0, 1, 0) {
			t.Fatalf("%s: an invariant violation passed the correctness gate", authority)
		}
	}
}

func TestBusyEscapesBindCorrectnessFollowsResolvedAuthority(t *testing.T) {
	t.Parallel()

	// The acceptance profile without the required-check signal resolves to a
	// non-accepted authority (CD-0046), and must not bind the zero-escape term.
	local, _ := resolvePopulationAuthority(runnerProfileIsolatedAcceptance, "")
	if busyEscapesBindCorrectness(local) {
		t.Fatalf("acceptance profile without the runner signal resolved to %q and bound busy escapes", local)
	}
	required, _ := resolvePopulationAuthority(runnerProfileIsolatedAcceptance, "1")
	if !busyEscapesBindCorrectness(required) {
		t.Fatalf("acceptance profile with the runner signal resolved to %q and did not bind busy escapes", required)
	}
}

func TestWritePopulationRequiresExactlyTenWorkersAndThousandAcceptedWrites(t *testing.T) {
	t.Parallel()
	population := func() []WorkerResult {
		results := make([]WorkerResult, 10)
		for worker := range results {
			results[worker] = WorkerResult{Worker: worker, Outcome: outcomeAccepted, Attempts: 100, Samples: make([]WorkerSample, 100)}
			for attempt := range results[worker].Samples {
				results[worker].Samples[attempt].Outcome = outcomeAccepted
			}
		}
		return results
	}
	if err := validateWritePopulation(population()); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		mutate func([]WorkerResult) []WorkerResult
	}{
		{"missing worker", func(r []WorkerResult) []WorkerResult { return r[:9] }},
		{"duplicate worker", func(r []WorkerResult) []WorkerResult { r[1].Worker = 0; return r }},
		{"unknown worker", func(r []WorkerResult) []WorkerResult { r[1].Worker = 10; return r }},
		{"missing attempt", func(r []WorkerResult) []WorkerResult { r[0].Attempts--; return r }},
		{"missing sample", func(r []WorkerResult) []WorkerResult { r[0].Samples = r[0].Samples[:99]; return r }},
		{"extra sample", func(r []WorkerResult) []WorkerResult {
			r[0].Samples = append(r[0].Samples, WorkerSample{Outcome: outcomeAccepted})
			return r
		}},
		{"worker failure", func(r []WorkerResult) []WorkerResult { r[0].Outcome = outcomeError; return r }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateWritePopulation(tc.mutate(population())); err == nil {
				t.Fatal("invalid write population passed")
			}
		})
	}
	for _, outcome := range []WorkerOutcome{outcomeLost, outcomeDuplicate, outcomeInvariantViolation, outcomeBusyEscaped, outcomeError, outcomeVersionConflict} {
		t.Run(string(outcome), func(t *testing.T) {
			results := population()
			results[0].Samples[0].Outcome = outcome
			if err := validateWritePopulation(results); err == nil {
				t.Fatalf("%s sample passed", outcome)
			}
		})
	}
}

func TestWriteEffectsAndReplaysInProcess(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	result := runLoadScenario(ctx, s, 0, "backup_load")
	if result.Outcome != outcomeAccepted || len(result.Samples) != 5 || countOutcome(expandWorkerResults([]WorkerResult{result}), outcomeAccepted) != 5 {
		t.Fatalf("five-write worker = %+v", result)
	}
	for replay := 0; replay < 2; replay++ {
		if err := verifyWriteEffects(ctx, s, 1, 5); err != nil {
			t.Fatal(err)
		}
	}
	terminalCleanupSQL(t, s, `DELETE FROM effect_receipts WHERE event_id='production-0-0-membership'`)
	if err := verifyWriteEffects(ctx, s, 1, 5); err == nil || !strings.Contains(err.Error(), "count=0, want 1") {
		t.Fatalf("missing effect = %v, want missing receipt failure", err)
	}
}

func TestWriteEffectsRejectMissingProjection(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	runLoadScenario(ctx, s, 0, "backup_load")
	terminalCleanupSQL(t, s, `DELETE FROM product_projects WHERE product_id='production-0-0'`)
	if err := verifyWriteEffects(ctx, s, 1, 5); err == nil || !strings.Contains(err.Error(), "projection count=0") {
		t.Fatalf("missing projection = %v, want projection failure", err)
	}
}

func TestWriteEffectsRejectExtraEffects(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	runLoadScenario(ctx, s, 0, "backup_load")
	if err := ApplyOperation(ctx, s, conformanceCreationOperation("extra", "extra-membership")); err != nil {
		t.Fatal(err)
	}
	if err := verifyWriteEffects(ctx, s, 1, 5); err == nil || !strings.Contains(err.Error(), "effect count=18, want 15") {
		t.Fatalf("extra effects = %v, want excess receipt failure", err)
	}
}

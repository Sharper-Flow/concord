package store

import "testing"

func TestConformanceFalsifierFiresOnSustainedCommitDuration(t *testing.T) {
	t.Parallel()

	reports := []ConformanceReport{
		conformanceRoundReport(150, 50, 50),
		conformanceRoundReport(120, 50, 50),
		conformanceRoundReport(80, 50, 50),
	}
	authority, _ := resolvePopulationAuthority(runnerProfileIsolatedAcceptance, "1")
	above := roundsAboveCommitTarget(reports)
	threshold, status := classifySustainedFalsifier(authority, above, len(reports), true)

	if threshold != thresholdExceeded || status != falsifierFired {
		t.Fatalf("commit-duration P99 rounds above target = %d, classifySustainedFalsifier = %q/%q, want 2/exceeded/fired", above, threshold, status)
	}
}

func TestConformanceSchedulerOvershootIsInconclusive(t *testing.T) {
	t.Parallel()

	reports := []ConformanceReport{
		conformanceRoundReport(50, 150, 150),
		conformanceRoundReport(60, 120, 120),
		conformanceRoundReport(70, 110, 110),
	}
	authority, _ := resolvePopulationAuthority(runnerProfileIsolatedAcceptance, "1")
	above := roundsAboveCommitTarget(reports)
	threshold, status := classifySustainedFalsifier(authority, above, len(reports), true)

	if threshold != thresholdMet || status != falsifierPassed {
		t.Fatalf("scheduler-overshoot rounds above commit target = %d, classifySustainedFalsifier = %q/%q, want 0/met/passed", above, threshold, status)
	}
}

func conformanceRoundReport(commitP99, wallP99, beginWaitP99 int64) ConformanceReport {
	return ConformanceReport{
		CorrectnessPassed:   true,
		P99TargetMS:         100,
		ProductionLikeP99MS: wallP99,
		WallLatency:         latencySummary{P99MS: wallP99},
		BeginWaitLatency:    latencySummary{P99MS: beginWaitP99},
		CommitLatency:       latencySummary{P99MS: commitP99},
	}
}

func TestClassifySustainedFalsifierRequiresAcceptancePopulation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		profile       conformanceRunnerProfile
		signal        string
		aboveTarget   int
		rounds        int
		correctness   bool
		wantThreshold sustainedThresholdStatus
		wantFalsifier falsifierStatus
	}{
		{
			name:          "development crossing is diagnostic",
			profile:       runnerProfileDiagnostic,
			signal:        "1",
			aboveTarget:   2,
			rounds:        3,
			correctness:   true,
			wantThreshold: thresholdExceeded,
			wantFalsifier: falsifierInconclusive,
		},
		{
			name:          "unknown profile crossing fails closed",
			profile:       conformanceRunnerProfile("unknown"),
			signal:        "1",
			aboveTarget:   3,
			rounds:        3,
			correctness:   true,
			wantThreshold: thresholdExceeded,
			wantFalsifier: falsifierInconclusive,
		},
		{
			name:          "acceptance crossing fires",
			profile:       runnerProfileIsolatedAcceptance,
			signal:        "1",
			aboveTarget:   2,
			rounds:        3,
			correctness:   true,
			wantThreshold: thresholdExceeded,
			wantFalsifier: falsifierFired,
		},
		{
			name:          "acceptance target met passes",
			profile:       runnerProfileIsolatedAcceptance,
			signal:        "1",
			aboveTarget:   1,
			rounds:        3,
			correctness:   true,
			wantThreshold: thresholdMet,
			wantFalsifier: falsifierPassed,
		},
		{
			name:          "correctness failure blocks accepted outcome",
			profile:       runnerProfileIsolatedAcceptance,
			signal:        "1",
			aboveTarget:   3,
			rounds:        3,
			correctness:   false,
			wantThreshold: thresholdExceeded,
			wantFalsifier: falsifierInconclusive,
		},
		{
			name:          "empty population is inconclusive",
			profile:       runnerProfileIsolatedAcceptance,
			signal:        "1",
			aboveTarget:   0,
			rounds:        0,
			correctness:   true,
			wantThreshold: thresholdInconclusive,
			wantFalsifier: falsifierInconclusive,
		},
		{
			name:          "diagnostic authority stays inconclusive on threshold exceedance",
			profile:       runnerProfileDiagnostic,
			signal:        "1",
			aboveTarget:   3,
			rounds:        3,
			correctness:   true,
			wantThreshold: thresholdExceeded,
			wantFalsifier: falsifierInconclusive,
		},
		{
			name:          "acceptance without required signal stays inconclusive",
			profile:       runnerProfileIsolatedAcceptance,
			signal:        "",
			aboveTarget:   3,
			rounds:        3,
			correctness:   true,
			wantThreshold: thresholdExceeded,
			wantFalsifier: falsifierInconclusive,
		},
		{
			name:          "acceptance with non-1 signal stays inconclusive",
			profile:       runnerProfileIsolatedAcceptance,
			signal:        "true",
			aboveTarget:   3,
			rounds:        3,
			correctness:   true,
			wantThreshold: thresholdExceeded,
			wantFalsifier: falsifierInconclusive,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			authority, _ := resolvePopulationAuthority(tt.profile, tt.signal)
			threshold, falsifier := classifySustainedFalsifier(authority, tt.aboveTarget, tt.rounds, tt.correctness)
			if threshold != tt.wantThreshold || falsifier != tt.wantFalsifier {
				t.Fatalf("classifySustainedFalsifier(%q, %d, %d, %t) = %q/%q, want %q/%q", tt.profile, tt.aboveTarget, tt.rounds, tt.correctness, threshold, falsifier, tt.wantThreshold, tt.wantFalsifier)
			}
		})
	}
}

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

func TestValidateLoadPacingFailsAcceptanceUnpaced(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name    string
		profile conformanceRunnerProfile
		unpaced bool
		wantErr bool
	}{
		{name: "acceptance paced", profile: runnerProfileIsolatedAcceptance, unpaced: false, wantErr: false},
		{name: "acceptance unpaced fails closed", profile: runnerProfileIsolatedAcceptance, unpaced: true, wantErr: true},
		{name: "diagnostic unpaced stays diagnostic", profile: runnerProfileDiagnostic, unpaced: true, wantErr: false},
		{name: "unknown profile unpaced", profile: conformanceRunnerProfile("unknown"), unpaced: true, wantErr: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := validateLoadPacing(tt.profile, tt.unpaced)
			if (err != nil) != tt.wantErr {
				t.Fatalf("validateLoadPacing(%q, %t) error = %v, wantErr %v", tt.profile, tt.unpaced, err, tt.wantErr)
			}
		})
	}
}

// TestAcceptanceEntryPointLocalInvocationStaysInconclusive asserts the
// acceptance criterion that a local invocation of the acceptance entry point
// must report `inconclusive` regardless of the numbers observed. The local
// invocation is captured by: profile=acceptance, signal absent, GITHUB_ACTIONS
// unset. Under those inputs the tripwire does not fire, the resolver returns
// diagnostic, and the falsifier verdict stays inconclusive even when the
// threshold is exceeded.
func TestAcceptanceEntryPointLocalInvocationStaysInconclusive(t *testing.T) {
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
	threshold, status := classifySustainedFalsifier(authority, 3, 3, true)
	if threshold != thresholdExceeded || status != falsifierInconclusive {
		t.Fatalf("classifySustainedFalsifier with diagnostic authority on threshold-exceeded inputs returned %q/%q, want exceeded/inconclusive", threshold, status)
	}
}

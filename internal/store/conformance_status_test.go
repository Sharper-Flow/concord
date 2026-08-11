package store

import "testing"

func TestClassifySustainedFalsifierRequiresAcceptancePopulation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		profile       conformanceRunnerProfile
		aboveTarget   int
		rounds        int
		correctness   bool
		wantThreshold sustainedThresholdStatus
		wantFalsifier falsifierStatus
	}{
		{
			name:          "development crossing is diagnostic",
			profile:       runnerProfileDiagnostic,
			aboveTarget:   2,
			rounds:        3,
			correctness:   true,
			wantThreshold: thresholdExceeded,
			wantFalsifier: falsifierInconclusive,
		},
		{
			name:          "unknown profile crossing fails closed",
			profile:       conformanceRunnerProfile("unknown"),
			aboveTarget:   3,
			rounds:        3,
			correctness:   true,
			wantThreshold: thresholdExceeded,
			wantFalsifier: falsifierInconclusive,
		},
		{
			name:          "acceptance crossing fires",
			profile:       runnerProfileIsolatedAcceptance,
			aboveTarget:   2,
			rounds:        3,
			correctness:   true,
			wantThreshold: thresholdExceeded,
			wantFalsifier: falsifierFired,
		},
		{
			name:          "acceptance target met passes",
			profile:       runnerProfileIsolatedAcceptance,
			aboveTarget:   1,
			rounds:        3,
			correctness:   true,
			wantThreshold: thresholdMet,
			wantFalsifier: falsifierPassed,
		},
		{
			name:          "correctness failure blocks accepted outcome",
			profile:       runnerProfileIsolatedAcceptance,
			aboveTarget:   3,
			rounds:        3,
			correctness:   false,
			wantThreshold: thresholdExceeded,
			wantFalsifier: falsifierInconclusive,
		},
		{
			name:          "empty population is inconclusive",
			profile:       runnerProfileIsolatedAcceptance,
			aboveTarget:   0,
			rounds:        0,
			correctness:   true,
			wantThreshold: thresholdInconclusive,
			wantFalsifier: falsifierInconclusive,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			threshold, falsifier := classifySustainedFalsifier(tt.profile, tt.aboveTarget, tt.rounds, tt.correctness)
			if threshold != tt.wantThreshold || falsifier != tt.wantFalsifier {
				t.Fatalf("classifySustainedFalsifier(%q, %d, %d, %t) = %q/%q, want %q/%q", tt.profile, tt.aboveTarget, tt.rounds, tt.correctness, threshold, falsifier, tt.wantThreshold, tt.wantFalsifier)
			}
		})
	}
}

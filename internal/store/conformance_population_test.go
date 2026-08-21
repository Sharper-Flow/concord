package store

import (
	"fmt"
	"testing"
	"time"
)

// representativeP99WithinTarget records read-path latency provenance and only
// enforces the target for the accepted conformance population. Diagnostic runs
// still expose their measurements without making host load an authoritative
// failure signal.
func representativeP99WithinTarget(t *testing.T, name string, p99, target time.Duration, population string, samples int) bool {
	t.Helper()
	authority, reason := resolvePopulationAuthority(runnerProfileIsolatedAcceptance, acceptanceRunnerSignal())
	load := "unavailable"
	if value, ok := readLoadAverageOneMinute(); ok {
		load = fmt.Sprintf("%.2f", value)
	}
	t.Logf(
		"%s P99=%s target=%s population=%s samples=%d population_authority=%s population_authority_reason=%s load_average_one_minute=%s",
		name, p99, target, population, samples, authority, reason, load,
	)
	return authority != populationAuthorityAccepted || p99 <= target
}

func TestRepresentativeP99PopulationGate(t *testing.T) {
	const (
		p99        = 101 * time.Millisecond
		target     = 100 * time.Millisecond
		population = "synthetic representative read"
	)

	t.Setenv(conformanceAcceptanceRunnerEnv, "")
	if !representativeP99WithinTarget(t, "read-path diagnostic", p99, target, population, 100) {
		t.Fatal("diagnostic read-path measurement must not fail the build")
	}

	t.Setenv(conformanceAcceptanceRunnerEnv, acceptanceRunnerSignalExpected)
	if representativeP99WithinTarget(t, "read-path accepted", p99, target, population, 100) {
		t.Fatal("accepted read-path measurement above target must fail the build")
	}
}

package store

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/gittest"
	"github.com/sharper-flow/concord/internal/testenv"
)

// CD-0046's invocation signals configure the test harness, not production code.
// Consume these two declared controls before the universal environment scrub.
// Child roles and fixture paths use TEST_CONCORD_* and need no scrub exemption.
var conformanceConfiguration struct {
	long             bool
	acceptanceRunner string
}

func TestMain(m *testing.M) {
	conformanceConfiguration.long = os.Getenv(conformanceLongEnv) == "1"
	conformanceConfiguration.acceptanceRunner = os.Getenv(conformanceAcceptanceRunnerEnv)
	dir := testenv.ScrubEnv()
	if err := gittest.ScrubRepositoryEnvironment(); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(testenv.Cleanup(dir, 1))
	}
	gittest.DisableBackgroundMaintenance()
	testDatabaseTemplate.dir = dir
	code := m.Run()
	os.Exit(testenv.Cleanup(dir, code))
}

func TestConformanceControlsAreConsumed(t *testing.T) {
	for _, key := range []string{conformanceLongEnv, conformanceAcceptanceRunnerEnv} {
		if _, present := os.LookupEnv(key); present {
			t.Errorf("harness control %s reached the test environment", key)
		}
	}
	if os.Getenv("TEST_CONCORD_EXPECT_CONFIG") == "1" {
		if !conformanceConfiguration.long || acceptanceRunnerSignal() != "1" {
			t.Fatalf("invocation configuration lost: %+v", conformanceConfiguration)
		}
	}
}

func TestConformanceConfigurationSurvivesScrub(t *testing.T) {
	// The child must self-terminate if this parent dies abnormally: its own
	// test timeout bounds it past the parent, the process context within it.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0],
		"-test.run=^TestConformanceControlsAreConsumed$", "-test.timeout=10s")
	command.Env = append(os.Environ(), "TEST_CONCORD_EXPECT_CONFIG=1",
		conformanceLongEnv+"=1", conformanceAcceptanceRunnerEnv+"=1")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("configuration child: %v\n%s", err, output)
	}
}

package store

import (
	"fmt"
	"os"
	"os/exec"
	"testing"

	"github.com/sharper-flow/concord/internal/gittest"
	"github.com/sharper-flow/concord/internal/testenv"
)

// CD-0046's invocation signals configure the test harness, not production code.
// Consume these four declared controls before the universal environment scrub.
// Child roles and fixture paths use TEST_CONCORD_* and need no scrub exemption.
var conformanceConfiguration struct {
	long, unpaced    bool
	attempts         int
	acceptanceRunner string
}

func TestMain(m *testing.M) {
	conformanceConfiguration.long = os.Getenv(conformanceLongEnv) == "1"
	conformanceConfiguration.unpaced = os.Getenv(conformanceUnpacedEnv) == "1"
	conformanceConfiguration.acceptanceRunner = os.Getenv(conformanceAcceptanceRunnerEnv)
	_, _ = fmt.Sscanf(os.Getenv(conformanceAttemptsEnv), "%d", &conformanceConfiguration.attempts)
	dir := testenv.ScrubEnv()
	gittest.DisableBackgroundMaintenance()
	testDatabaseTemplate.dir = dir
	code := m.Run()
	os.Exit(testenv.Cleanup(dir, code))
}

func TestConformanceControlsAreConsumed(t *testing.T) {
	for _, key := range []string{conformanceLongEnv, conformanceUnpacedEnv, conformanceAttemptsEnv, conformanceAcceptanceRunnerEnv} {
		if _, present := os.LookupEnv(key); present {
			t.Errorf("harness control %s reached the test environment", key)
		}
	}
	if os.Getenv("TEST_CONCORD_EXPECT_CONFIG") == "1" {
		if !conformanceConfiguration.long || !conformanceConfiguration.unpaced || conformanceConfiguration.attempts != 3 || acceptanceRunnerSignal() != "1" {
			t.Fatalf("invocation configuration lost: %+v", conformanceConfiguration)
		}
	}
}

func TestConformanceConfigurationSurvivesScrub(t *testing.T) {
	command := exec.Command(os.Args[0], "-test.run=^TestConformanceControlsAreConsumed$")
	command.Env = append(os.Environ(), "TEST_CONCORD_EXPECT_CONFIG=1",
		conformanceLongEnv+"=1", conformanceUnpacedEnv+"=1",
		conformanceAttemptsEnv+"=3", conformanceAcceptanceRunnerEnv+"=1")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("configuration child: %v\n%s", err, output)
	}
}

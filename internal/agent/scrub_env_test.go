package agent

import (
	"fmt"
	"os"
	"testing"

	"github.com/sharper-flow/concord/internal/gittest"
	"github.com/sharper-flow/concord/internal/testenv"
)

func TestMain(m *testing.M) {
	dir := testenv.ScrubEnv()
	if err := gittest.ScrubRepositoryEnvironment(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(testenv.Cleanup(dir, 1))
	}
	gittest.DisableBackgroundMaintenance()
	code := m.Run()
	os.Exit(testenv.Cleanup(dir, code))
}

func TestGitFixtureStartupIsolation(t *testing.T) {
	gittest.TestStartupIsolation(t, func(t *testing.T) {
		seedFixtureRepo(t, t.TempDir(), "fixture\n", "fixture")
	})
}

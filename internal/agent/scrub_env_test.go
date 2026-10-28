package agent

import (
	"os"
	"testing"

	"github.com/sharper-flow/concord/internal/gittest"
	"github.com/sharper-flow/concord/internal/store"
	"github.com/sharper-flow/concord/internal/testenv"
)

func TestMain(m *testing.M) {
	if code, handled := store.RunNativeIsolationSupervisor(); handled {
		os.Exit(code)
	}
	dir := testenv.ScrubEnv()
	gittest.DisableBackgroundMaintenance()
	code := m.Run()
	os.Exit(testenv.Cleanup(dir, code))
}

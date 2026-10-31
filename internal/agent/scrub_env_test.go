package agent

import (
	"os"
	"testing"

	"github.com/sharper-flow/concord/internal/gittest"
	"github.com/sharper-flow/concord/internal/testenv"
)

func TestMain(m *testing.M) {
	dir := testenv.ScrubEnv()
	gittest.DisableBackgroundMaintenance()
	code := m.Run()
	os.Exit(testenv.Cleanup(dir, code))
}

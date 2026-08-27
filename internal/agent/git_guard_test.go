package agent

import (
	"os"
	"testing"

	"github.com/sharper-flow/concord/internal/gittest"
)

func TestMain(m *testing.M) {
	gittest.DisableBackgroundMaintenance()
	os.Exit(m.Run())
}

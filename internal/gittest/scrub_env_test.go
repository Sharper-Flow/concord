package gittest

import (
	"fmt"
	"os"
	"testing"

	"github.com/sharper-flow/concord/internal/testenv"
)

func TestMain(m *testing.M) {
	dir := testenv.ScrubEnv()
	if err := ScrubRepositoryEnvironment(); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(testenv.Cleanup(dir, 1))
	}
	code := m.Run()
	os.Exit(testenv.Cleanup(dir, code))
}

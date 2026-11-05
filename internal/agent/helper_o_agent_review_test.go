package agent

import (
	"os"
	"testing"

	"github.com/sharper-flow/concord/internal/gittest"
)

func fixtureClaimSubject(t *testing.T, path string) {
	t.Helper()
	command, err := gittest.Command(path, "rev-parse", "--verify", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := command.Run(); err == nil {
		return
	}
	gitRun(t, path, "init", "--quiet", "--initial-branch=main")
	gitRun(t, path, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--quiet", "--allow-empty", "-m", "fixture subject")
}

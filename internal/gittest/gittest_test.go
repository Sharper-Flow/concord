package gittest

import (
	"os/exec"
	"strings"
	"testing"
)

// TestConfigReachesGit proves the environment guard actually reaches git: a
// fixture repository created inside the guard reports gc.auto as disabled.
// A guard that silently fails to apply would leave the #542 race open with no
// signal, so the mechanism carries its own probe.
func TestConfigReachesGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available")
	}
	DisableBackgroundMaintenance()

	repo := t.TempDir()
	init := exec.Command("git", "init", "--quiet", repo)
	if out, err := init.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	output, err := exec.Command("git", "-C", repo, "config", "gc.auto").Output()
	if err != nil {
		t.Fatalf("git config gc.auto: %v", err)
	}
	if got := strings.TrimSpace(string(output)); got != "0" {
		t.Fatalf("gc.auto=%q, want 0; the guard did not reach git", got)
	}
}

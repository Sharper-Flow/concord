package testenv_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/testenv"
)

func TestScrubEnv(t *testing.T) {
	for _, key := range []string{"CONCORD_DB_PATH", "CONCORD_SELECTED_PRODUCT_ID", "CONCORD_TEST_SUBPROCESS", "CONCORD_FUTURE_INPUT"} {
		t.Setenv(key, "poison")
	}
	for _, key := range []string{"HOME", "XDG_DATA_HOME", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "GOPATH", "GOMODCACHE", "GOCACHE", "GOENV", "TEST_TELEMETRY_DIR", "GOTELEMETRY", "GOTELEMETRYDIR"} {
		t.Setenv(key, os.Getenv(key))
	}
	before, err := exec.Command("go", "env", "GOPATH", "GOMODCACHE", "GOCACHE", "GOENV").Output()
	if err != nil {
		t.Fatal(err)
	}
	root := testenv.ScrubEnv()
	t.Cleanup(func() {
		if code := testenv.Cleanup(root, 0); code != 0 {
			t.Errorf("cleanup returned %d", code)
		}
	})
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, "CONCORD_") {
			t.Errorf("session variable survived: %s", strings.SplitN(entry, "=", 2)[0])
		}
	}
	home := filepath.Join(root, "home")
	for _, key := range []string{"HOME", "XDG_DATA_HOME", "XDG_CONFIG_HOME", "XDG_STATE_HOME"} {
		if got := os.Getenv(key); got != home {
			t.Errorf("%s = %q, want the private home %q", key, got, home)
		}
	}
	entries, err := os.ReadDir(home)
	if err != nil || len(entries) != 0 {
		t.Fatalf("private home must start empty: entries=%v err=%v", entries, err)
	}
	after, err := exec.Command("go", "env", "GOPATH", "GOMODCACHE", "GOCACHE", "GOENV").Output()
	if err != nil || string(after) != string(before) {
		t.Fatalf("child Go paths changed: before=%s after=%s err=%v", before, after, err)
	}
	t.Setenv("CONCORD_DB_PATH", filepath.Join(root, "fixture.db"))
	if os.Getenv("CONCORD_DB_PATH") == "" {
		t.Fatal("explicit test input was not retained")
	}
}

func TestCleanupAndFreshHome(t *testing.T) {
	for _, key := range []string{"HOME", "XDG_DATA_HOME", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "TEST_TELEMETRY_DIR", "GOTELEMETRY", "GOTELEMETRYDIR"} {
		t.Setenv(key, os.Getenv(key))
	}
	first := testenv.ScrubEnv()
	second := testenv.ScrubEnv()
	if first == second {
		t.Fatal("run roots collided")
	}
	if code := testenv.Cleanup(first, 7); code != 7 {
		t.Fatalf("test failure changed: %d", code)
	}
	if code := testenv.Cleanup(second, 0); code != 0 {
		t.Fatalf("cleanup failed: %d", code)
	}
	for _, root := range []string{first, second} {
		for _, dir := range []string{root, filepath.Join(root, "home"), filepath.Join(root, "go-telemetry")} {
			if _, err := os.Stat(dir); !os.IsNotExist(err) {
				t.Errorf("run root state still exists: %v", err)
			}
		}
	}
}

// A fresh test process must leave no run root behind: each child reserves its
// root under a parent-owned TMPDIR, and TestMain's Cleanup must remove it
// before exit. One hundred children cover repeated reservation paths. Normal
// exit is the cleanup contract; a killed child may leave its root behind.
func TestCleanupRemovesFreshProcessRunRoots(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for child := 0; child < 100; child++ {
		childTmp := t.TempDir()
		env := make([]string, 0, len(os.Environ())+1)
		for _, entry := range os.Environ() {
			if !strings.HasPrefix(entry, "TMPDIR=") {
				env = append(env, entry)
			}
		}
		env = append(env, "TMPDIR="+childTmp)
		command := exec.Command(executable, "-test.run=^TestScrubGuard/valid$", "-test.count=1", "-test.timeout=60s")
		command.Env = env
		if out, err := command.CombinedOutput(); err != nil {
			t.Fatalf("child %d: %v: %s", child, err, out)
		}
		left, err := os.ReadDir(childTmp)
		if err != nil {
			t.Fatal(err)
		}
		if len(left) != 0 {
			t.Fatalf("child %d left run roots behind: %v", child, left)
		}
	}
}

// A TestMain whose environment probe fails after reservation must remove the
// run root it already created and fail loudly: an empty parent-owned PATH
// leaves the go command unresolvable, and the parent-owned TMPDIR must hold
// no leftover root once the child exits.
func TestScrubEnvCleansRunRootWhenProbeFails(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	binDir := t.TempDir()
	runDir := t.TempDir()
	env := make([]string, 0, len(os.Environ())+2)
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "PATH=") && !strings.HasPrefix(entry, "TMPDIR=") {
			env = append(env, entry)
		}
	}
	env = append(env, "PATH="+binDir, "TMPDIR="+runDir)
	command := exec.Command(executable, "-test.run=^TestScrubGuard/valid$", "-test.count=1", "-test.timeout=60s")
	command.Env = env
	out, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("child succeeded without a resolvable go command: %s", out)
	}
	if !strings.Contains(string(out), "testenv: isolate run root:") {
		t.Fatalf("child diagnostic missing: %s", out)
	}
	left, err := os.ReadDir(runDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 0 {
		t.Fatalf("child left run roots behind: %v", left)
	}
}

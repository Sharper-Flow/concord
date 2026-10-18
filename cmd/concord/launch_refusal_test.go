package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// launchRefusalChildArg is the fixed argument the argv[0] test passes its
// re-executed child. TestMain routes a child invoked with it to
// runLaunchRefusalChild, which reports the ordinary route result for the
// arguments the parent supplies.
const launchRefusalChildArg = "launch-refusal-child-argv0"

// runLaunchRefusalChild runs the route function on the parent's arguments and
// exits with the result, so the parent observes the same route decision a real
// process would make.
func runLaunchRefusalChild() int {
	var out, errOut bytes.Buffer
	code := runWithInput(os.Args[2:], strings.NewReader(""), &out, &errOut)
	_, _ = os.Stdout.WriteString(out.String())
	_, _ = os.Stderr.WriteString(errOut.String())
	return code
}

func TestRemovedLaunchEntriesRefuseWithoutExecuting(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"bare zl", []string{"zl"}},
		{"zl help", []string{"zl", "--help"}},
		{"zl short help", []string{"zl", "-h"}},
		{"zl with work and prompt", []string{"zl", "work-1", "--", "prompt words"}},
		{"zl resume-last", []string{"zl", "--resume-last"}},
		{"resume-last alone", []string{"--resume-last"}},
		{"session boot", []string{"session"}},
		{"session help", []string{"session", "--help"}},
		{"session short help", []string{"session", "-h"}},
		{"launcher help", []string{"launcher", "--help"}},
		{"launcher short help", []string{"launcher", "-h"}},
		{"operator outside repair", []string{"outside-repair"}},
		{"outside repair help", []string{"outside-repair", "--help"}},
		{"outside repair short help", []string{"outside-repair", "-h"}},
		{"project canonical path", []string{"project-canonical-path"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			database := filepath.Join(t.TempDir(), "authority", "concord.db")
			t.Setenv(dbOverrideEnv, database)
			stdin := &countingStdin{}
			var out, errOut bytes.Buffer
			if code := runWithInput(tc.args, stdin, &out, &errOut); code != 2 {
				t.Fatalf("%v exit=%d, want 2; stdout=%q stderr=%q", tc.args, code, out.String(), errOut.String())
			}
			if out.Len() != 0 {
				t.Fatalf("%v stdout=%q, want empty", tc.args, out.String())
			}
			if !strings.Contains(errOut.String(), "unsupported arguments") || !strings.Contains(errOut.String(), "Usage:") {
				t.Fatalf("%v stderr=%q, want the unsupported-usage refusal plus usage", tc.args, errOut.String())
			}
			if stdin.reads != 0 {
				t.Fatalf("%v read stdin %d times, want 0", tc.args, stdin.reads)
			}
			if _, err := os.Stat(filepath.Dir(database)); !os.IsNotExist(err) {
				t.Fatalf("%v opened authority storage under %s: %v", tc.args, filepath.Dir(database), err)
			}
		})
	}
	var help bytes.Buffer
	writeUsage(&help)
	for _, advertised := range []string{
		"  concord zl ",
		"  concord session ",
		"--resume-last",
		"concord outside-repair < JSON stdin",
		"concord project-canonical-path < JSON stdin",
	} {
		if strings.Contains(help.String(), advertised) {
			t.Fatalf("help still advertises the removed launch entry %q", advertised)
		}
	}
}

func TestRemovedLaunchArgvZeroDoesNotForward(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	database := filepath.Join(t.TempDir(), "authority", "concord.db")
	cmd := exec.Command(executable) //nolint:gosec // the test binary re-executes itself with fixed arguments.
	cmd.Args = []string{"zl", launchRefusalChildArg, "session"}
	cmd.Env = append(os.Environ(), dbOverrideEnv+"="+database)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	runErr := cmd.Run()
	code := 0
	if runErr != nil {
		var exit *exec.ExitError
		if !errors.As(runErr, &exit) {
			t.Fatalf("child run: %v stderr=%q", runErr, errOut.String())
		}
		code = exit.ExitCode()
	}
	if code != 2 {
		t.Fatalf("child exit=%d, want 2; stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	if !strings.Contains(errOut.String(), "unsupported arguments") {
		t.Fatalf("child stderr=%q, want the unsupported-usage refusal", errOut.String())
	}
	if _, statErr := os.Stat(filepath.Dir(database)); !os.IsNotExist(statErr) {
		t.Fatalf("child opened authority storage under %s: %v", filepath.Dir(database), statErr)
	}
}

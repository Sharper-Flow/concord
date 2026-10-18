package main

// Shared fixture for tests that exercise the real registry probe against a
// fake host installed on PATH. The fake host's probe branch emulates the
// directory dependence of the real host: it answers `debug config` with an
// agent map read from the invocation directory, and it counts its probes.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeHostScript is the fake host binary the tests install on PATH.
const fakeHostScript = `#!/bin/sh
record="$CONCORD_FAKE_HOST_RECORD"
if [ "$1" = "debug" ] && [ "$2" = "config" ]; then
	pwd > "$record/probe-cwd"
	n=$(cat "$record/probe-count" 2>/dev/null || echo 0)
	echo $((n+1)) > "$record/probe-count"
	if [ -f opencode.registry.json ]; then
		cat opencode.registry.json
	else
		printf '{}'
	fi
	exit 0
fi
printf '%s\n' "$#" "$1" "$2" "$3" > "$record/host-argv"
pwd > "$record/host-cwd"
exit 0
`

// installFakeHost puts the fake host binary first on PATH and points its
// record directory at recordDir.
func installFakeHost(t *testing.T, recordDir string) {
	t.Helper()
	installFakeCommand(t, recordDir, fakeHostScript)
}

func installFakeCommand(t *testing.T, recordDir, script string) {
	t.Helper()
	binDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(binDir, "opencode"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CONCORD_FAKE_HOST_RECORD", recordDir)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// hostRecord reads one record the fake host wrote. A missing record fails
// the test with the cause: the fake host never ran that branch.
func hostRecord(t *testing.T, recordDir, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(recordDir, name))
	if err != nil {
		t.Fatalf("fake host record %s: %v", name, err)
	}
	return strings.TrimSpace(string(data))
}

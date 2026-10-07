// Package testenv isolates test binaries from session variables and user state.
package testenv

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// ScrubEnv must run before m.Run in each package's TestMain. It removes all
// CONCORD_* variables, including unknown future inputs, and redirects user
// state to an empty temporary directory. Tests supply their own inputs afterward.
// Go's effective cache and module paths are pinned before HOME changes so child
// go commands can use the modules and toolchain already fetched by the host.
// The caller passes the returned directory and m.Run's code to Cleanup.
func ScrubEnv() string {
	output, err := exec.Command("go", "env", "-json", "GOPATH", "GOMODCACHE", "GOCACHE", "GOENV").Output()
	if err != nil {
		fail("resolve Go environment", err)
	}
	var goEnv map[string]string
	if err := json.Unmarshal(output, &goEnv); err != nil {
		fail("decode Go environment", err)
	}
	for key, value := range goEnv {
		if err := os.Setenv(key, value); err != nil {
			fail("pin "+key, err)
		}
	}
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "CONCORD_") {
			if err := os.Unsetenv(key); err != nil {
				fail("unset "+key, err)
			}
		}
	}
	root, err := os.MkdirTemp("", "concord-testenv-")
	if err != nil {
		fail("create temporary home", err)
	}
	for _, key := range []string{"HOME", "XDG_DATA_HOME", "XDG_CONFIG_HOME", "XDG_STATE_HOME"} {
		if err := os.Setenv(key, root); err != nil {
			_ = os.RemoveAll(root)
			fail("isolate "+key, err)
		}
	}
	return root
}

// Cleanup removes the isolated state after m.Run and preserves a test failure.
func Cleanup(root string, code int) int {
	if err := os.RemoveAll(root); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "testenv: cleanup: %v\n", err)
		if code == 0 {
			return 1
		}
	}
	return code
}

func fail(operation string, err error) {
	_, _ = fmt.Fprintf(os.Stderr, "testenv: %s: %v\n", operation, err)
	os.Exit(1)
}

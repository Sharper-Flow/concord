// Package testenv isolates test binaries from session variables and user state.
package testenv

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ScrubEnv must precede m.Run in TestMain. It removes all CONCORD_* variables,
// isolates HOME and user state, and sets telemetry mode to exact bytes "off".
// Go cache and module paths are pinned before HOME changes so child commands can
// reuse the host's toolchain and modules. Pass the returned root and exit code to Cleanup.
func ScrubEnv() string {
	root, err := os.MkdirTemp("", "concord-testenv-")
	if err != nil {
		fail("create run root", err)
	}
	if err := isolate(root); err != nil {
		_ = os.RemoveAll(root)
		fail("isolate run root", err)
	}
	return root
}

// isolate arms telemetry before any Go child and resolves Go paths before HOME changes.
func isolate(root string) error {
	home := filepath.Join(root, "home")
	telemetry := filepath.Join(root, "go-telemetry")
	for _, dir := range []string{home, telemetry} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			return err
		}
	}
	if err := os.WriteFile(filepath.Join(telemetry, "mode"), []byte("off"), 0o600); err != nil {
		return err
	}
	// TEST_TELEMETRY_DIR overrides any ambient value, and the "off" mode file
	// makes every child Go command skip counters and uploaders entirely.
	if err := os.Setenv("TEST_TELEMETRY_DIR", telemetry); err != nil {
		return err
	}
	output, err := exec.Command("go", "env", "-json", "GOPATH", "GOMODCACHE", "GOCACHE", "GOENV").Output()
	if err != nil {
		return fmt.Errorf("resolve Go environment: %w", err)
	}
	var goEnv map[string]string
	if err := json.Unmarshal(output, &goEnv); err != nil {
		return fmt.Errorf("decode Go environment: %w", err)
	}
	for key, value := range goEnv {
		if err := os.Setenv(key, value); err != nil {
			return fmt.Errorf("pin %s: %w", key, err)
		}
	}
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "CONCORD_") {
			if err := os.Unsetenv(key); err != nil {
				return fmt.Errorf("unset %s: %w", key, err)
			}
		}
	}
	for _, key := range []string{"HOME", "XDG_DATA_HOME", "XDG_CONFIG_HOME", "XDG_STATE_HOME"} {
		if err := os.Setenv(key, home); err != nil {
			return fmt.Errorf("isolate %s: %w", key, err)
		}
	}
	return nil
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

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

// ScrubEnv must run before m.Run in each package's TestMain. It removes all
// CONCORD_* variables, including unknown future inputs, and redirects user
// state to an empty private home. Go telemetry is quarantined in a private
// directory whose mode file holds the exact bytes "off", so child Go commands
// record no counters and start no uploaders. Go's effective cache and module
// paths are pinned before HOME changes so child go commands can use the
// modules and toolchain already fetched by the host. The returned run root
// owns the private home and telemetry directories; the caller passes it with
// m.Run's code to Cleanup.
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

// isolate builds the private state inside root and rewires the process
// environment. The telemetry directory is armed before the first Go child
// runs, so even the environment probe below records nothing, and the probe
// resolves Go's paths while the original HOME is still in place.
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

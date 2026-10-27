package gittest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRepositoryEnvironmentKeysRejectsInvalidDiscovery(t *testing.T) {
	for _, output := range []string{"", "\n", "PATH", "GIT_DIR\nHOME", "GIT_bad", "GIT_DIR=value"} {
		if keys, err := repositoryEnvironmentKeys([]byte(output)); err == nil || keys != nil {
			t.Fatalf("invalid namespace %q accepted: %v, %v", output, keys, err)
		}
	}
	keys, err := repositoryEnvironmentKeys([]byte("GIT_DIR\nGIT_CONFIG_COUNT\nGIT_FUTURE_LOCAL_INPUT\n"))
	if err != nil || !keys["GIT_DIR"] || !keys["GIT_CONFIG_COUNT"] || !keys["GIT_FUTURE_LOCAL_INPUT"] || !keys["GIT_QUARANTINE_PATH"] {
		t.Fatalf("Git namespace was not preserved: %v, %v", keys, err)
	}
}

func TestScrubRepositoryEnvironmentClearsHookRedirectsBeforeDiscovery(t *testing.T) {
	// Preserve settings installed by other harness tests, including indexed
	// entries that this test does not overwrite but the scrub must remove.
	for _, entry := range os.Environ() {
		key, value, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "GIT_") {
			t.Setenv(key, value)
		}
	}
	fixture := t.TempDir()
	poisoned := map[string]string{
		"GIT_DIR":               filepath.Join(fixture, "not-a-repository"),
		"GIT_WORK_TREE":         fixture,
		"GIT_COMMON_DIR":        filepath.Join(fixture, "not-a-common-directory"),
		"GIT_INDEX_FILE":        filepath.Join(fixture, "index"),
		"GIT_QUARANTINE_PATH":   filepath.Join(fixture, "quarantine"),
		"GIT_CONFIG_COUNT":      "not-a-count",
		"GIT_CONFIG_KEY_0":      "core.worktree",
		"GIT_CONFIG_VALUE_0":    fixture,
		"GIT_CONFIG_KEY_999":    "core.bare",
		"GIT_CONFIG_VALUE_999":  "true",
		"GIT_CONFIG_PARAMETERS": "invalid configuration parameters",
	}
	for key, value := range poisoned {
		t.Setenv(key, value)
	}
	t.Setenv("GIT_TERMINAL_PROMPT", "0")
	t.Setenv("TEST_GIT_ENV_KEEP", "keep")
	if err := ScrubRepositoryEnvironment(); err != nil {
		t.Fatal(err)
	}
	for key := range poisoned {
		if _, found := os.LookupEnv(key); found {
			t.Errorf("hook redirect %s survived", key)
		}
	}
	if os.Getenv("GIT_TERMINAL_PROMPT") != "0" || os.Getenv("TEST_GIT_ENV_KEEP") != "keep" {
		t.Fatal("scrub changed transport or unrelated environment")
	}
}

package gittest

import (
	"os"
	"os/exec"
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

func TestScrubStopsOuterGlobalHook(t *testing.T) {
	for _, key := range []string{"GIT_CONFIG_GLOBAL", "GIT_CONFIG_SYSTEM", "GIT_TEMPLATE_DIR"} {
		t.Run(key, func(t *testing.T) {
			testScrubStopsOuterHook(t, key)
		})
	}
}

func testScrubStopsOuterHook(t *testing.T, key string) {
	outer := t.TempDir()
	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.invalid", "GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.invalid")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	git(outer, "init", "-q", "-b", "main")
	tracked := filepath.Join(outer, "tracked")
	if err := os.WriteFile(tracked, []byte("outer\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(outer, "add", "tracked")
	git(outer, "commit", "-q", "-m", "outer")
	hooks := filepath.Join(outer, "template", "hooks")
	if err := os.MkdirAll(hooks, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hooks, "pre-commit"), []byte("#!/bin/sh\nprintf changed > \"$OUTER_TRACKED\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(outer, "hook-config")
	if err := os.WriteFile(config, []byte("[core]\n\thooksPath = "+hooks+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	paths := []string{tracked, filepath.Join(outer, ".git", "HEAD"), filepath.Join(outer, ".git", "refs", "heads", "main"), filepath.Join(outer, ".git", "config"), filepath.Join(outer, ".git", "index"), config}
	before := make([]string, len(paths))
	for i, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		before[i] = string(data)
	}
	for _, entry := range os.Environ() {
		key, value, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "GIT_") {
			t.Setenv(key, value)
		}
	}
	t.Setenv("OUTER_TRACKED", tracked)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "0")
	value := config
	if key == "GIT_TEMPLATE_DIR" {
		value = filepath.Dir(hooks)
	}
	t.Setenv(key, value)
	if err := ScrubRepositoryEnvironment(); err != nil {
		t.Fatal(err)
	}
	fixture := t.TempDir()
	git(fixture, "init", "-q")
	if err := os.WriteFile(filepath.Join(fixture, "file"), []byte("fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(fixture, "add", "file")
	git(fixture, "commit", "-q", "-m", "fixture")
	for i, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != before[i] {
			t.Errorf("outer file changed: %s", path)
		}
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
		"GIT_CONFIG_GLOBAL":     filepath.Join(fixture, "global"),
		"GIT_CONFIG_SYSTEM":     filepath.Join(fixture, "system"),
		"GIT_TEMPLATE_DIR":      filepath.Join(fixture, "template"),
	}
	for key, value := range poisoned {
		t.Setenv(key, value)
	}
	t.Setenv("GIT_TERMINAL_PROMPT", "0")
	t.Setenv("GIT_EXEC_PATH", fixture)
	t.Setenv("TEST_GIT_ENV_KEEP", "keep")
	if err := ScrubRepositoryEnvironment(); err != nil {
		t.Fatal(err)
	}
	for key := range poisoned {
		if _, found := os.LookupEnv(key); found {
			t.Errorf("hook redirect %s survived", key)
		}
	}
	if os.Getenv("GIT_TERMINAL_PROMPT") != "0" || os.Getenv("TEST_GIT_ENV_KEEP") != "keep" || os.Getenv("GIT_EXEC_PATH") != fixture {
		t.Fatal("scrub changed transport or unrelated environment")
	}
}

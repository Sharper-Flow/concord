// Package gittest owns process-wide git configuration for test binaries that
// create fixture repositories, including their background-maintenance guard.
package gittest

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ScrubRepositoryEnvironment removes a launching hook's repository and injected
// configuration before fixtures run. External config and templates can install
// hooks in fixtures, so they are removed too. GIT_EXEC_PATH is retained because
// Git exports it to hooks and needs it to find its own subcommands.
// Call this before DisableBackgroundMaintenance so the harness owns Git settings.
func ScrubRepositoryEnvironment() error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "git", "rev-parse", "--local-env-vars")
	command.Dir = os.TempDir()
	command.Env = []string{}
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "GIT_") {
			command.Env = append(command.Env, entry)
		}
	}
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("discover Git repository environment: %w\n%s", err, output)
	}
	keys, err := repositoryEnvironmentKeys(output)
	if err != nil {
		return err
	}
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if keys[key] || strings.HasPrefix(key, "GIT_CONFIG_KEY_") || strings.HasPrefix(key, "GIT_CONFIG_VALUE_") {
			if err := os.Unsetenv(key); err != nil {
				return fmt.Errorf("clear Git repository variable %s: %w", key, err)
			}
		}
	}
	return nil
}

func repositoryEnvironmentKeys(output []byte) (map[string]bool, error) {
	names := strings.Fields(string(output))
	if len(names) == 0 {
		return nil, fmt.Errorf("git reported an empty repository environment namespace")
	}
	valid := regexp.MustCompile(`^GIT_[A-Z0-9_]+$`)
	keys := map[string]bool{
		"GIT_QUARANTINE_PATH": true,
		"GIT_CONFIG_GLOBAL":   true,
		"GIT_CONFIG_SYSTEM":   true,
		"GIT_TEMPLATE_DIR":    true,
	}
	for _, name := range names {
		if !valid.MatchString(name) {
			return nil, fmt.Errorf("git reported an invalid repository environment variable: %q", name)
		}
		keys[name] = true
	}
	return keys, nil
}

// DisableBackgroundMaintenance prevents detached Git maintenance from racing
// fixture cleanup. Environment configuration covers every Git invocation.
func DisableBackgroundMaintenance() {
	// Merge with an existing GIT_CONFIG_COUNT rather than replacing it, so a
	// caller that already declared environment config keeps it.
	base := 0
	if count, err := strconv.Atoi(os.Getenv("GIT_CONFIG_COUNT")); err == nil && count > 0 {
		base = count
	}
	entries := []struct{ key, value string }{
		{"gc.auto", "0"},
		{"gc.autoDetach", "false"},
		{"fetch.writeCommitGraph", "false"},
		{"maintenance.repo", "false"},
	}
	for index, entry := range entries {
		position := strconv.Itoa(base + index)
		_ = os.Setenv("GIT_CONFIG_KEY_"+position, entry.key)
		_ = os.Setenv("GIT_CONFIG_VALUE_"+position, entry.value)
	}
	_ = os.Setenv("GIT_CONFIG_COUNT", strconv.Itoa(base+len(entries)))
}

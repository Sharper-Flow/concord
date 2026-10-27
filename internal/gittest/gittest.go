// Package gittest owns process-wide git configuration for test binaries that
// create fixture repositories. It exists so the background-maintenance guard
// has one authority instead of fourteen per-fixture copies.
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
// configuration before fixtures run. Call it before DisableBackgroundMaintenance
// so the test harness, not the launching repository, owns child Git settings.
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
		return nil, fmt.Errorf("Git reported an empty repository environment namespace")
	}
	valid := regexp.MustCompile(`^GIT_[A-Z0-9_]+$`)
	keys := map[string]bool{"GIT_QUARANTINE_PATH": true}
	for _, name := range names {
		if !valid.MatchString(name) {
			return nil, fmt.Errorf("Git reported an invalid repository environment variable: %q", name)
		}
		keys[name] = true
	}
	return keys, nil
}

// DisableBackgroundMaintenance configures every git invocation made by the
// calling test binary to never start background work in a fixture repository.
//
// The race it closes: porcelain commands (commit, fetch, merge) can invoke
// `git gc --auto` at exit, and a detached gc still writing into
// `.git/objects/pack` while `t.TempDir` cleanup walks the tree fails with
// `unlinkat .../.git/objects/pack: directory not empty` — the test body has
// already passed.
//
// The configuration travels through git's environment variables rather than
// per-repo `git config` calls, so it covers every fixture, present and future,
// without each fixture knowing about it. Setting these keys is harmless on git
// versions that predate them: unknown or irrelevant keys are ignored.
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

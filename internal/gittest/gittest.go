// Package gittest owns process-wide git configuration for test binaries that
// create fixture repositories. It exists so the background-maintenance guard
// has one authority instead of fourteen per-fixture copies.
package gittest

import (
	"os"
	"strconv"
)

// DisableBackgroundMaintenance configures every git invocation made by the
// calling test binary to never start background work in a fixture repository.
//
// The race it closes: porcelain commands (commit, fetch, merge) can invoke
// `git gc --auto` at exit, and a detached gc still writing into
// `.git/objects/pack` while `t.TempDir` cleanup walks the tree fails with
// `unlinkat .../.git/objects/pack: directory not empty` — the test body has
// already passed (issue #542).
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

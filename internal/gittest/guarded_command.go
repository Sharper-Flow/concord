package gittest

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
)

// Command builds a Git command that runs in dir. It refuses an empty or
// relative dir: Git resolves such a directory against the process working
// directory, so the fixture could read and write an outer repository
// instead of its own.
func Command(dir string, args ...string) (*exec.Cmd, error) {
	if !filepath.IsAbs(dir) {
		return nil, fmt.Errorf("test git requires an absolute fixture directory: %q", dir)
	}
	command := exec.Command("git", args...) //nolint:gosec // git is a fixed program and callers pass code-owned argv values without a shell.
	command.Dir = dir
	return command, nil
}

// CommandContext builds a guarded Git command bound to ctx.
func CommandContext(ctx context.Context, dir string, args ...string) (*exec.Cmd, error) {
	if !filepath.IsAbs(dir) {
		return nil, fmt.Errorf("test git requires an absolute fixture directory: %q", dir)
	}
	command := exec.CommandContext(ctx, "git", args...) //nolint:gosec // git is a fixed program and callers pass code-owned argv values without a shell.
	command.Dir = dir
	return command, nil
}

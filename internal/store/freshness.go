package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// FreshnessRunner extends GitRunner with commands that run with the
// noninteractive environment the bounded origin refresh requires. A runner
// without it keeps every behavior that does not refresh the Git cache.
type FreshnessRunner interface {
	GitRunner
	RunNoninteractive(ctx context.Context, dir string, args ...string) ([]byte, error)
}

// noninteractiveFetchEnv returns the environment additions that refuse every
// credential prompt the HTTPS and SSH transports can open. Git reads a
// credential from the askpass program's stdout, so an empty-output program
// answers empty and authentication fails instead of blocking; OpenSSH
// SSH_ASKPASS_REQUIRE=force routes the SSH transport's password and
// passphrase prompts to the same program with no terminal fallback. The
// additions override no repository or host configuration: core.sshCommand
// and the caller's identity policy stay in force.
func noninteractiveFetchEnv() []string {
	return []string{
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ASKPASS=/bin/true",
		"SSH_ASKPASS=/bin/true",
		"SSH_ASKPASS_REQUIRE=force",
	}
}

var _ FreshnessRunner = ExecGitRunner{}

// RunNoninteractive runs one git command with terminal and askpass prompts
// refused, so a remote that wants credentials fails instead of blocking.
// The command runs under the runner's one bounded execution policy: its own
// process group, a SIGKILL of that group when the caller's context ends, and
// bounded remaining pipe drainage after cancellation, even
// when a descendant of git holds the output pipes open.
func (ExecGitRunner) RunNoninteractive(ctx context.Context, dir string, args ...string) ([]byte, error) {
	if dir == "" {
		return nil, errors.New("empty git directory")
	}
	command := append([]string{"-C", dir}, args...)
	cmd := exec.Command("git", command...) //nolint:gosec // git is fixed, argv values stay separate, and no shell is invoked.
	cmd.Env = append(os.Environ(), noninteractiveFetchEnv()...)
	stdout, stderr, err := runBoundedGitOutput(ctx, cmd)
	if err != nil {
		if trimmed := strings.TrimSpace(string(stderr)); trimmed != "" {
			return stdout, fmt.Errorf("%w: %s", err, trimmed)
		}
	}
	return stdout, err
}

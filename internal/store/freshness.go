package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// freshnessPreflightTimeout is the fixed deadline the shared Git preflight
// gives the complete freshness operation: one origin fetch and every local
// probe a sample runs around it. It is a variable so tests can shorten it
// instead of waiting out the real budget.
var freshnessPreflightTimeout = 10 * time.Second

// Branch freshness statuses. ok carries the sampled facts; unknown carries
// only its typed reason and never an authoritative count.
const (
	FreshnessStatusOK      = "ok"
	FreshnessStatusUnknown = "unknown"
)

// Branch freshness reasons. The set is closed: a deadline the preflight set
// reads as timeout, a failed fetch reads as fetch_failed, and every local
// probe failure reads as probe_failed.
const (
	FreshnessReasonTimeout     = "timeout"
	FreshnessReasonFetchFailed = "fetch_failed"
	FreshnessReasonProbeFailed = "probe_failed"
)

// BranchFreshness is the typed freshness a resume reports for the worktree it
// enters: how far the checked-out branch sits behind the origin default
// branch after one bounded refresh. The unknown status reports a fetch or
// probe failure with its typed reason and no count.
type BranchFreshness struct {
	Status      string `json:"status"`
	HeadSHA     string `json:"head_sha,omitempty"`
	DefaultRef  string `json:"default_ref,omitempty"`
	DefaultSHA  string `json:"default_sha,omitempty"`
	BehindCount *int64 `json:"behind_count,omitempty"`
	Reason      string `json:"reason,omitempty"`
}

func okBranchFreshness(headSHA, defaultRef, defaultSHA string, behindCount int64) BranchFreshness {
	count := behindCount
	return BranchFreshness{Status: FreshnessStatusOK, HeadSHA: headSHA, DefaultRef: defaultRef, DefaultSHA: defaultSHA, BehindCount: &count}
}

func unknownBranchFreshness(reason string) BranchFreshness {
	return BranchFreshness{Status: FreshnessStatusUnknown, Reason: reason}
}

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

// errFreshnessDeadline marks that a freshness failure was the fixed preflight
// deadline rather than a transport failure.
var errFreshnessDeadline = errors.New("freshness preflight deadline exceeded")

// freshnessDeadlineTripped reports that the freshness operation's own fixed
// deadline expired while the caller's context still lives. A caller
// cancellation is not the preflight deadline.
func freshnessDeadlineTripped(ctx, parent context.Context) bool {
	return ctx.Err() != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) && parent.Err() == nil
}

// fetchOriginDefaultBranch runs the one bounded refresh the shared Git
// preflight owns: the registered origin default branch, force-mirrored into
// its remote-tracking ref, with prompts refused. The fetch names the remote
// by its registered name and ignores configured ref mappings with an empty
// --refmap, so repository configuration cannot widen the refresh onto a
// local branch or any other ref. --no-recurse-submodules keeps the refresh
// from contacting submodule origins. The fetch updates only the selected
// remote-tracking ref and whatever objects and metadata the transfer needs,
// so it carries no durable
// effect to roll back. The caller owns the fixed deadline.
func fetchOriginDefaultBranch(ctx context.Context, runner GitRunner, repo, defaultRef string) error {
	branch := strings.TrimPrefix(defaultRef, "origin/")
	refspec := "+refs/heads/" + branch + ":refs/remotes/origin/" + branch
	fetch := []string{"fetch", "--no-tags", "--no-recurse-submodules", "--refmap=", "origin", refspec}
	var err error
	if noninteractive, ok := runner.(FreshnessRunner); ok {
		_, err = noninteractive.RunNoninteractive(ctx, repo, fetch...)
	} else {
		_, err = runner.Run(ctx, repo, fetch...)
	}
	return err
}

// creationPreflight is the fixed bounded window one fresh creation owes. The
// window opens with the origin default-branch refresh and stays open while
// the caller resolves its creation base, so the fetch and every local probe
// around it share one fixed deadline (CD-0088 D2). The caller closes the
// window once the base is resolved, before any durable or native creation
// effect; the refresh and the read-only probes are the only Git work the
// window covers.
type creationPreflight struct {
	ctx    context.Context
	cancel context.CancelFunc
}

// close ends the window. Calling it more than once is safe.
func (p creationPreflight) close() {
	if p.cancel != nil {
		p.cancel()
	}
}

// tripped reports that the window's fixed deadline expired while the caller's
// context still lives. A caller cancellation is not the preflight deadline.
func (p creationPreflight) tripped(parent context.Context) bool {
	return freshnessDeadlineTripped(p.ctx, parent)
}

// startCreationPreflight runs the shared Git preflight every fresh creation
// owes: one bounded, noninteractive fetch of the registered origin default
// branch, before the caller resolves its creation base. A caller-pinned ref
// or base SHA stays an exact pin; the fetch keeps the remote-tracking cache
// current and never moves a pin. The returned window carries the fixed
// deadline for the caller's base-resolution probes, and close ends it. The
// fetch runs outside every transaction (CD-0195 D2) and precedes the
// capture, so a refresh failure leaves no work item, claim, branch, or
// worktree behind.
func startCreationPreflight(ctx context.Context, runner GitRunner, repo string) (creationPreflight, error) {
	preflightCtx, cancel := context.WithTimeout(ctx, freshnessPreflightTimeout)
	window := creationPreflight{ctx: preflightCtx, cancel: cancel}
	defaultRef, err := bootstrapDefaultBranchRef(preflightCtx, runner, repo)
	if err == nil {
		err = fetchOriginDefaultBranch(preflightCtx, runner, repo, defaultRef)
	}
	if err == nil {
		return window, nil
	}
	deadline := freshnessDeadlineTripped(preflightCtx, ctx)
	cancel()
	if deadline {
		return creationPreflight{}, wrapFailure(KindGitUnreachable, "work_bootstrap", "origin default branch refresh exceeded its fixed deadline", true, "restore access to the origin remote and replay the same request", errFreshnessDeadline)
	}
	if _, ok := err.(*Failure); ok {
		return creationPreflight{}, err
	}
	return creationPreflight{}, wrapFailure(KindGitUnreachable, "work_bootstrap", "origin default branch refresh failed", true, "restore access to the origin remote and replay the same request", err)
}

// freshnessDeadlineRefusal is the typed refusal a preflight deadline trip
// returns wherever in the window it trips.
func freshnessDeadlineRefusal(operation string) *Failure {
	return wrapFailure(KindGitUnreachable, operation, "the bounded freshness preflight exceeded its fixed deadline", true, "restore access to the origin remote and replay the same request", errFreshnessDeadline)
}

// SampleWorktreeFreshness refreshes the registered origin default branch and
// samples how far the worktree's checked-out branch sits behind it. The
// sample never blocks its caller: every fetch or probe failure returns the
// unknown freshness with a typed reason and no count, and the fixed deadline
// covers the whole sample, the fetch and every local probe. One default-ref
// read drives the fetch and the sample, and the lag counts between the two
// sampled commit SHAs, so a ref that moves mid-sample cannot mix two
// snapshots. It records nothing, never rebases, and changes no worktree
// file, index entry, or local branch tip; the fetch only moves the shared
// remote-tracking cache.
func SampleWorktreeFreshness(ctx context.Context, entry WorktreeEntry, runner GitRunner) BranchFreshness {
	if entry.Path == "" {
		return unknownBranchFreshness(FreshnessReasonProbeFailed)
	}
	sampleCtx, cancel := context.WithTimeout(ctx, freshnessPreflightTimeout)
	defer cancel()
	unknown := func(reason string) BranchFreshness {
		if freshnessDeadlineTripped(sampleCtx, ctx) {
			return unknownBranchFreshness(FreshnessReasonTimeout)
		}
		return unknownBranchFreshness(reason)
	}
	if err := runnerProbe(sampleCtx, runner, entry.Path); err != nil {
		return unknown(FreshnessReasonProbeFailed)
	}
	defaultRef, err := bootstrapDefaultBranchRef(sampleCtx, runner, entry.Path)
	if err != nil {
		return unknown(FreshnessReasonProbeFailed)
	}
	if err := fetchOriginDefaultBranch(sampleCtx, runner, entry.Path, defaultRef); err != nil {
		return unknown(FreshnessReasonFetchFailed)
	}
	headSHA, err := resolveCommitSHARunner(sampleCtx, runner, entry.Path, "HEAD")
	if err != nil {
		return unknown(FreshnessReasonProbeFailed)
	}
	defaultSHA, err := resolveCommitSHARunner(sampleCtx, runner, entry.Path, "refs/remotes/"+defaultRef)
	if err != nil {
		return unknown(FreshnessReasonProbeFailed)
	}
	countOut, err := runner.Run(sampleCtx, entry.Path, "rev-list", "--count", headSHA+".."+defaultSHA)
	if err != nil {
		return unknown(FreshnessReasonProbeFailed)
	}
	behind, parseErr := strconv.ParseInt(strings.TrimSpace(string(countOut)), 10, 64)
	if parseErr != nil || behind < 0 {
		return unknown(FreshnessReasonProbeFailed)
	}
	return okBranchFreshness(headSHA, defaultRef, defaultSHA, behind)
}

// runnerProbe proves the runner can read the worktree before the sample
// reports on it, so a gone worktree degrades to the typed unknown instead of
// half a sample.
func runnerProbe(ctx context.Context, runner GitRunner, dir string) error {
	_, err := runner.Run(ctx, dir, "rev-parse", "--git-dir")
	return err
}

package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// integrationProbeTimeout is the one aggregate deadline the constructor
// owns; no command resets it.
const integrationProbeTimeout = 5 * time.Second

// integrationProbeRefusalReason is the typed transport refusal of the
// probe. A refusal is never an exit-1 success, never carries a partial
// result, and poisons the probe for every later command.
type integrationProbeRefusalReason string

func (r integrationProbeRefusalReason) Error() string {
	return "integration probe " + string(r) + " refusal"
}

const (
	integrationProbeRefusalCanceled integrationProbeRefusalReason = "canceled"
	integrationProbeRefusalDeadline integrationProbeRefusalReason = "deadline"
	integrationProbeRefusalOverflow integrationProbeRefusalReason = "overflow"
	integrationProbeRefusalStart    integrationProbeRefusalReason = "start"
	integrationProbeRefusalPipe     integrationProbeRefusalReason = "pipe"
)

var errIntegrationProbeExitStatus = errors.New("integration probe command exited nonzero")

// integrationProbeResult is one completed command. Every refusal leaves
// the zero result.
type integrationProbeResult struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
}

// integrationGitProbe owns the shared deadline and budget of the whole
// native preparation: remaining counts the combined output budget across
// both streams and every command under one lock, and terminal records the
// refusal that poisoned the probe.
type integrationGitProbe struct {
	ctx       context.Context
	cancel    context.CancelFunc
	mu        sync.Mutex
	remaining int
	overflow  bool
	terminal  error
}

func newIntegrationGitProbe(parent context.Context) *integrationGitProbe {
	ctx, cancel := context.WithTimeout(parent, integrationProbeTimeout)
	return &integrationGitProbe{ctx: ctx, cancel: cancel, remaining: maxGitOutput}
}

// refusal returns the terminal refusal of the probe, recording the context
// cause of a probe whose deadline expired or whose caller canceled it.
func (p *integrationGitProbe) refusal() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.terminal == nil && p.ctx.Err() != nil {
		reason := integrationProbeRefusalCanceled
		if errors.Is(p.ctx.Err(), context.DeadlineExceeded) {
			reason = integrationProbeRefusalDeadline
		}
		p.terminal = fmt.Errorf("%w: %w", reason, p.ctx.Err())
	}
	return p.terminal
}

// poison records err as the terminal refusal while the probe is still live.
func (p *integrationGitProbe) poison(err error) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.terminal == nil {
		p.terminal = err
	}
	return p.terminal
}

// environment is the fresh allowlist every probe command runs under:
// nothing is inherited except PATH, the system and global config and
// attribute sources collapse onto the empty device, and the replacement,
// lazy-fetch, prompt, and optional-lock controls stay pinned. Exposed for
// identity sealing.
func (p *integrationGitProbe) environment() []string {
	env := []string{
		"LC_ALL=C",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_ATTR_NOSYSTEM=1",
		"GIT_NO_REPLACE_OBJECTS=1",
		"GIT_NO_LAZY_FETCH=1",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_OPTIONAL_LOCKS=0",
	}
	if path, ok := os.LookupEnv("PATH"); ok {
		return append([]string{"PATH=" + path}, env...)
	}
	return env
}

// integrationProbeMergeConfig is the fixed built-in merge configuration
// every probe command shares: the built-in text driver for unspecified
// merge attributes, rename and directory-rename detection at their
// built-in defaults, no renormalization, and the merge conflict style.
var integrationProbeMergeConfig = []string{
	"merge.default=text",
	"merge.renames=true",
	"merge.directoryRenames=conflict",
	"merge.renormalize=false",
	"merge.conflictStyle=merge",
}

// mergeConfigArgs exposes the fixed merge configuration for identity
// sealing.
func (p *integrationGitProbe) mergeConfigArgs() []string {
	return append([]string(nil), integrationProbeMergeConfig...)
}

// integrationProbeStream is one output stream of the shared combined
// budget. The first byte past the remaining total poisons the whole probe
// with the overflow refusal and cancels it: output is refused, never
// truncated into acceptance. The per-stream buffer never exceeds the
// shared budget, so the unbounded buffer owner of git_runner.go is not
// reused.
type integrationProbeStream struct {
	probe    *integrationGitProbe
	data     bytes.Buffer
	complete bool
}

func (s *integrationProbeStream) Write(chunk []byte) (int, error) {
	s.probe.mu.Lock()
	defer s.probe.mu.Unlock()
	if s.probe.overflow {
		return len(chunk), nil
	}
	if len(chunk) > s.probe.remaining {
		s.probe.remaining = 0
		s.probe.overflow = true
		s.probe.terminal = integrationProbeRefusalOverflow
		s.probe.cancel()
		return len(chunk), nil
	}
	s.probe.remaining -= len(chunk)
	_, _ = s.data.Write(chunk)
	return len(chunk), nil
}

// ReadFrom copies one command pipe to EOF and records whether EOF arrived;
// a pipe that WaitDelay closes under a lingering descendant stays incomplete.
func (s *integrationProbeStream) ReadFrom(r io.Reader) (int64, error) {
	n, err := io.Copy(struct{ io.Writer }{s}, r)
	s.complete = err == nil
	return n, err
}

// output runs one internally derived command: the fixed cwd, optional
// stdin, environment additions over the fresh allowlist, and argv. The -c
// merge settings prefix overrides repository, global, and system config
// alike. Cancellation kills the child process group, WaitDelay bounds the
// pipes of descriptors that escape the child, and no shell runs.
func (p *integrationGitProbe) output(cwd string, stdin io.Reader, env []string, argv ...string) (integrationProbeResult, error) {
	if err := p.refusal(); err != nil {
		return integrationProbeResult{}, err
	}
	args := make([]string, 0, 2*len(integrationProbeMergeConfig)+len(argv))
	for _, setting := range integrationProbeMergeConfig {
		args = append(args, "-c", setting)
	}
	args = append(args, argv...)
	stdout, stderr := &integrationProbeStream{probe: p}, &integrationProbeStream{probe: p}
	// Constructed with only the fixed executable so the launch shape stays
	// literal; the already-separated argument vector appends unchanged.
	// The child context is value-free. os/exec stops watching it once the
	// direct child exits, so the aggregate probe context, the single
	// deadline owner, forwards to the group kill itself until Wait drains.
	childCtx, cancelChild := context.WithCancel(context.Background())
	defer cancelChild()
	cmd := exec.CommandContext(childCtx, "git")
	cmd.Args = append(cmd.Args, args...)
	cmd.Dir = cwd
	cmd.Env = append(p.environment(), env...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = boundedGitWaitDelay
	if err := cmd.Start(); err != nil {
		return integrationProbeResult{}, p.poison(fmt.Errorf("%w: %w", integrationProbeRefusalStart, err))
	}
	stopForward := context.AfterFunc(p.ctx, func() {
		_ = cmd.Cancel()
		cancelChild()
	})
	waitErr := cmd.Wait()
	stopForward()
	if err := p.refusal(); err != nil {
		return integrationProbeResult{}, err
	}
	if !stdout.complete || !stderr.complete {
		return integrationProbeResult{}, p.poison(fmt.Errorf("%w: output pipe closed before EOF", integrationProbeRefusalPipe))
	}
	if waitErr == nil {
		return integrationProbeResult{Stdout: stdout.data.Bytes(), Stderr: stderr.data.Bytes()}, nil
	}
	var exitErr *exec.ExitError
	if errors.As(waitErr, &exitErr) {
		return integrationProbeResult{Stdout: stdout.data.Bytes(), Stderr: stderr.data.Bytes(), ExitCode: exitErr.ExitCode()}, nil
	}
	return integrationProbeResult{}, p.poison(fmt.Errorf("%w: %w", integrationProbeRefusalPipe, waitErr))
}

// Run adapts the probe to the GitRunner seam for the version and
// default-ref helpers: exit zero or an error, never a nonzero result. The
// caller's context only refuses an already-dead call; the probe's own
// aggregate deadline still governs execution.
func (p *integrationGitProbe) Run(ctx context.Context, repo string, args ...string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return p.runZero(repo, nil, args...)
}

var _ GitRunner = (*integrationGitProbe)(nil)

func (p *integrationGitProbe) runZero(cwd string, env []string, argv ...string) ([]byte, error) {
	got, err := p.output(cwd, nil, env, argv...)
	if err != nil {
		return nil, err
	}
	if got.ExitCode != 0 {
		return nil, fmt.Errorf("%w: exit status %d", errIntegrationProbeExitStatus, got.ExitCode)
	}
	return got.Stdout, nil
}

// integrationObjectView owns one private object view over a source
// repository: dir is a fresh 0700 temporary bare repository created through
// os.MkdirTemp("", ...), and env carries the exact additions every view
// command runs under: the view's own private index and the original objects
// directory as one C-quoted GIT_ALTERNATE_OBJECT_DIRECTORIES entry, which
// Git never writes new objects to. View commands run through the probe's
// output with dir and env, so derived writes land in the view objects while
// source refs, config, index, objects, and worktree stay untouched.
type integrationObjectView struct {
	dir string
	env []string
}

// integrationObjectViewFailure is the typed store refusal for unsupported
// native attribute input.
func integrationObjectViewFailure(detail, recovery string) error {
	return newFailure(KindInvalidPayload, "worker_integration_git_runner", detail, false, recovery)
}

// integrationMergeAttrValues are the only merge attribute values the probe
// accepts: the bare spellings and the built-in drivers. Any other value
// names an external driver the source could execute.
var integrationMergeAttrValues = map[string]bool{
	"set":         true,
	"unset":       true,
	"unspecified": true,
	"text":        true,
	"binary":      true,
	"union":       true,
}

// newIntegrationObjectView derives the original objects directory and the
// source object format through the sanitized probe runner and initializes
// the private bare view with the derived format. It accepts no public
// caller path. A refused construction removes its own directory and joins
// a failed cleanup beside the construction cause instead of hiding it.
func (p *integrationGitProbe) newIntegrationObjectView(sourceRepo string) (*integrationObjectView, error) {
	objects, err := p.runZero(sourceRepo, nil, "rev-parse", "--git-path", "objects")
	if err != nil {
		return nil, err
	}
	sourceObjects := strings.TrimSpace(string(objects))
	if !filepath.IsAbs(sourceObjects) {
		sourceObjects = filepath.Join(sourceRepo, sourceObjects)
	}
	format, err := p.runZero(sourceRepo, nil, "rev-parse", "--show-object-format")
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "concord-integration-view-")
	if err != nil {
		return nil, fmt.Errorf("create integration object view: %w", err)
	}
	if _, err := p.runZero(dir, nil, "init", "-q", "--bare", "--template=", "--object-format="+strings.TrimSpace(string(format))); err != nil {
		if cleanup := os.RemoveAll(dir); cleanup != nil {
			return nil, errors.Join(err, cleanup)
		}
		return nil, err
	}
	return &integrationObjectView{
		dir: dir,
		env: []string{
			"GIT_INDEX_FILE=" + filepath.Join(dir, "index"),
			"GIT_ALTERNATE_OBJECT_DIRECTORIES=" + strconv.Quote(sourceObjects),
		},
	}, nil
}

// close removes only this view directory and reports removal errors.
func (v *integrationObjectView) close() error {
	return os.RemoveAll(v.dir)
}

// checkIntegrationMergeAttributes reads each supplied parent or merge-base
// commit into the view's private index with read-tree, enumerates the
// tracked paths with ls-files -z, and queries check-attr --cached -z
// --stdin merge for every path. It returns one map per ref, in ref order,
// from path to the exact merge value.
func (p *integrationGitProbe) checkIntegrationMergeAttributes(view *integrationObjectView, refs ...string) ([]map[string]string, error) {
	perRef := make([]map[string]string, 0, len(refs))
	for _, ref := range refs {
		if _, err := p.runZero(view.dir, view.env, "read-tree", ref); err != nil {
			return nil, err
		}
		listed, err := p.runZero(view.dir, view.env, "ls-files", "-z")
		if err != nil {
			return nil, err
		}
		paths, err := sliceDiffTokens(listed, "ls-files -z")
		if err != nil {
			return nil, err
		}
		got, err := p.output(view.dir, bytes.NewReader(listed), view.env, "check-attr", "--cached", "-z", "--stdin", "merge")
		if err != nil {
			return nil, err
		}
		if got.ExitCode != 0 {
			return nil, fmt.Errorf("%w: exit status %d", errIntegrationProbeExitStatus, got.ExitCode)
		}
		values, err := parseIntegrationMergeAttrs(paths, got.Stdout)
		if err != nil {
			return nil, err
		}
		perRef = append(perRef, values)
	}
	return perRef, nil
}

// parseIntegrationMergeAttrs requires exactly one complete NUL triple per
// enumerated path, in the enumerated order, naming the merge attribute and
// carrying only an accepted value. Any other output is unsupported native
// input and refuses through the typed store Failure.
func parseIntegrationMergeAttrs(paths []string, output []byte) (map[string]string, error) {
	triples, err := sliceDiffTokens(output, "check-attr -z merge")
	if err != nil {
		return nil, err
	}
	if len(triples) != 3*len(paths) {
		return nil, integrationObjectViewFailure(fmt.Sprintf("merge attribute output carries %d NUL fields for %d tracked paths", len(triples), len(paths)), "run check-attr --cached -z --stdin merge over the enumerated index paths")
	}
	values := make(map[string]string, len(paths))
	for i, path := range paths {
		name, attribute, value := triples[3*i], triples[3*i+1], triples[3*i+2]
		if name != path || attribute != "merge" {
			return nil, integrationObjectViewFailure(fmt.Sprintf("merge attribute triple %q %q does not match tracked path %q", name, attribute, path), "run check-attr --cached -z --stdin merge over the enumerated index paths")
		}
		if !integrationMergeAttrValues[value] {
			return nil, integrationObjectViewFailure(fmt.Sprintf("tracked path %q carries unsupported merge attribute value %q", name, value), "pin the merge attribute to a built-in value or remove the external driver")
		}
		values[name] = value
	}
	return values, nil
}

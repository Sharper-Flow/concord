package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/sharper-flow/concord/internal/store"
)

const (
	outsideRepairLaunchRefusalExit = 2
	outsideRepairAdHocBranchPrefix = "outside-repair"
	outsideRepairGitFetchDeadline  = 60 * time.Second
	outsideRepairAdHocDirName      = "opencode/worktree-adhoc/concord"
)

var outsideRepairAdHocPathPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

// Strip inherited selection and occupancy, not the installed read-capable
// adapter, client credentials, or host configuration. Environment stays private.
var outsideRepairManagedEnvPrefixes = []string{
	"CONCORD_SELECTED_", "CONCORD_SESSION_", "CONCORD_WORK_",
	"CONCORD_WORKTREE_", "CONCORD_LEASE_", "CONCORD_HOST_LEASE_",
	"CONCORD_LAST_WORK_ID=", "CONCORD_DB_PATH=", "CONCORD_PRODUCT_ID=", "CONCORD_PROJECT_ID=",
}

type outsideRepairLaunchInput struct {
	WorkID string `json:"work_id"`
	Agent  string `json:"agent"`
}

type outsideRepairLaunchOutput struct {
	SchemaVersion string   `json:"schema_version"`
	WorkID        string   `json:"work_id"`
	Agent         string   `json:"agent"`
	Branch        string   `json:"branch"`
	Path          string   `json:"path"`
	BaseSHA       string   `json:"base_sha"`
	Repository    string   `json:"repository"`
	DefaultRef    string   `json:"default_ref"`
	Argv          []string `json:"argv"`
}

type outsideRepairDeps struct {
	Git         store.FreshnessRunner
	HostCommand func(context.Context, string, []string) (hostCommandResolution, error)
	Exec        sessionRunnerFunc
	XDGHome     func() (string, error)
	Now         func() time.Time
	Terminal    func() (io.ReadWriteCloser, error)
}

func defaultOutsideRepairDeps() outsideRepairDeps {
	return outsideRepairDeps{
		Git: store.ExecGitRunner{}, HostCommand: outsideRepairHostCommand, Exec: runOpenCode,
		XDGHome: outsideRepairXDGHome, Now: time.Now, Terminal: outsideRepairTerminal,
	}
}

func outsideRepairHostCommand(ctx context.Context, dir string, env []string) (hostCommandResolution, error) {
	return resolveHostCommand(ctx, dir, func(ctx context.Context, argv []string, dir string) ([]byte, error) {
		return probeHostConfigWithEnv(ctx, argv, dir, env)
	})
}

func outsideRepairXDGHome() (string, error) {
	if value := os.Getenv("XDG_DATA_HOME"); value != "" {
		return value, nil
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", fmt.Errorf("outside repair: cannot resolve XDG_DATA_HOME and $HOME is unavailable")
	}
	return filepath.Join(home, ".local", "share"), nil
}

// JSON consumes stdin to EOF. The host must use the controlling terminal,
// not that exhausted pipe, and must not write its TUI into the JSON stream.
func outsideRepairTerminal() (io.ReadWriteCloser, error) {
	terminal, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("outside repair requires a controlling TTY: %w", err)
	}
	if !terminalStreams(terminal, terminal) {
		_ = terminal.Close()
		return nil, fmt.Errorf("outside repair requires an interactive TTY")
	}
	return terminal, nil
}

// This operator entry reads the hold and repository identity only. Git owns
// the ad-hoc branch; no Concord claim, workflow action, or receipt is recorded.
func runOutsideRepairCommand(raw []byte, s *store.Store, deps outsideRepairDeps, out, errOut io.Writer) (exitCode int) {
	refuse := func(err error) int {
		writeOperatorDiagnostic(errOut, "outside-repair", err.Error())
		return outsideRepairLaunchRefusalExit
	}
	var input outsideRepairLaunchInput
	if err := decodeObject(raw, &input); err != nil {
		return refuse(err)
	}
	if !outsideRepairAdHocPathPattern.MatchString(input.WorkID) {
		return refuse(fmt.Errorf("work_id is missing or out of bounds"))
	}
	if !outsideRepairAdHocPathPattern.MatchString(input.Agent) {
		return refuse(fmt.Errorf("agent is missing or out of bounds"))
	}
	if deps.Git == nil || deps.HostCommand == nil || deps.Exec == nil || deps.XDGHome == nil || deps.Now == nil || deps.Terminal == nil {
		return refuse(fmt.Errorf("outside repair launch dependencies are incomplete"))
	}
	ctx := context.Background()
	snapshot, err := store.ReadWorkflowContinuity(ctx, s, store.ContinuityRequest{Work: input.WorkID, Limit: 1})
	if err != nil {
		return refuse(err)
	}
	if snapshot.OutsideRepairDisposition == nil || snapshot.OutsideRepairDisposition.State != store.OutsideRepairStateActive {
		return refuse(fmt.Errorf("no outside-repair hold is active for work %s; record an operator-approved hold first", input.WorkID))
	}
	pin, err := store.ReadWorkPin(ctx, s, input.WorkID)
	if err != nil {
		return refuse(err)
	}
	projectID, err := outsideRepairPrimaryProjectID(ctx, s, input.WorkID)
	if err != nil {
		return refuse(err)
	}
	canonicalPath, err := s.ProjectCanonicalPath(ctx, projectID)
	if err != nil {
		return refuse(err)
	}
	if _, err := sessionDirectoryOnDisk(canonicalPath); err != nil {
		return refuse(err)
	}
	xdgHome, err := deps.XDGHome()
	if err != nil {
		return refuse(err)
	}
	if !filepath.IsAbs(xdgHome) || filepath.Clean(xdgHome) != xdgHome {
		return refuse(fmt.Errorf("XDG_DATA_HOME must be an absolute clean directory"))
	}
	branch := outsideRepairAdHocBranchPrefix + "-" + outsideRepairShortSlug(input.WorkID, deps.Now)
	parent := filepath.Join(xdgHome, outsideRepairAdHocDirName)
	path := filepath.Join(parent, branch)
	if err := outsideRepairValidateParent(parent); err != nil {
		return refuse(err)
	}
	if _, err := os.Lstat(path); err == nil {
		return refuse(fmt.Errorf("ad-hoc worktree path already exists: %s", path))
	} else if !os.IsNotExist(err) {
		return refuse(fmt.Errorf("cannot inspect ad-hoc worktree path: %w", err))
	}
	terminal, err := deps.Terminal()
	if err != nil {
		return refuse(err)
	}
	defer func() {
		if err := terminal.Close(); err != nil && exitCode == 0 {
			writeOperatorDiagnostic(errOut, "outside-repair", "cannot close host terminal: "+err.Error())
			exitCode = 1
		}
	}()

	// One bounded Git phase; cancel on every exit and before the TUI starts.
	gitCtx, cancel := context.WithTimeout(ctx, outsideRepairGitFetchDeadline)
	defer cancel()
	host, err := store.ResolveProjectHost(gitCtx, canonicalPath, canonicalPath, deps.Git)
	if err != nil {
		return refuse(err)
	}
	project, err := s.MatchResolvedProjectHost(gitCtx, host)
	if err != nil {
		return refuse(err)
	}
	if project.ProjectID != projectID || !host.MainWorktree || host.Canonical != canonicalPath || host.Host.WorktreePath != canonicalPath {
		return refuse(fmt.Errorf("canonical repository does not match the registered Project locator"))
	}
	defaultRef, err := store.DefaultBranchRef(gitCtx, canonicalPath)
	if err != nil {
		return refuse(err)
	}
	_, err = deps.Git.Run(gitCtx, canonicalPath, "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
	if err == nil {
		return refuse(fmt.Errorf("ad-hoc branch already exists: %s", branch))
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 {
		return refuse(fmt.Errorf("cannot inspect ad-hoc branch: %w", err))
	}
	if err := outsideRepairFetchDefault(gitCtx, deps.Git, canonicalPath, defaultRef); err != nil {
		return refuse(fmt.Errorf("cannot refresh the default remote ref %s: %w", defaultRef, err))
	}
	baseSHA, err := outsideRepairResolveCommit(gitCtx, deps.Git, canonicalPath, "refs/remotes/"+defaultRef)
	if err != nil {
		return refuse(err)
	}
	if err := os.MkdirAll(parent, 0o750); err != nil {
		return refuse(fmt.Errorf("cannot create ad-hoc worktree parent: %w", err))
	}
	if err := outsideRepairValidateParent(parent); err != nil {
		return refuse(err)
	}
	if _, err := deps.Git.Run(gitCtx, canonicalPath, "worktree", "add", "--quiet", "-b", branch, path, baseSHA); err != nil {
		return refuse(fmt.Errorf("git worktree add refused for branch %s at %s; no cleanup attempted: %w", branch, path, err))
	}
	status, err := deps.Git.Run(gitCtx, path, "status", "--porcelain")
	if err != nil {
		return refuse(fmt.Errorf("cannot read the new worktree status: %w", err))
	}
	if strings.TrimSpace(string(status)) != "" {
		return refuse(fmt.Errorf("ad-hoc worktree is dirty after creation; retained at %s", path))
	}
	cancel()
	env := outsideRepairBuildEnv()
	resolution, err := deps.HostCommand(ctx, path, env)
	if err != nil {
		return refuse(fmt.Errorf("ad-hoc worktree retained at %s: %w", path, err))
	}
	if err := verifyHostRegistersHandle(resolution, input.Agent); err != nil {
		return refuse(fmt.Errorf("ad-hoc worktree retained at %s: %w", path, err))
	}
	argv := append(append([]string(nil), resolution.Command...), "--agent", input.Agent, "--prompt", outsideRepairBuildPrompt(input.WorkID, pin))
	output := outsideRepairLaunchOutput{
		SchemaVersion: "1.0", WorkID: input.WorkID, Agent: input.Agent,
		Branch: branch, Path: path, BaseSHA: baseSHA, Repository: canonicalPath, DefaultRef: defaultRef, Argv: argv,
	}
	if code := writeJSON(out, output, errOut); code != 0 {
		return code
	}
	if err := deps.Exec(ctx, path, argv, env, terminal, terminal, terminal); err != nil {
		writeOperatorDiagnostic(errOut, "outside-repair", "host execution refused: "+err.Error())
		return 1
	}
	return 0
}

// Every existing ancestor must be a real directory outside Git. Rejecting
// symlinks prevents an XDG alias from redirecting into main or a claimed tree.
// Missing directories are created only after identity and collision checks.
func outsideRepairValidateParent(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return fmt.Errorf("ad-hoc worktree parent must be an absolute clean directory")
	}
	for probe := path; ; probe = filepath.Dir(probe) {
		// #nosec G703 -- probe is an ancestor of the absolute clean parent;
		// Lstat does not dereference its final component; all ancestors are checked.
		info, err := os.Lstat(probe)
		if err == nil {
			if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("ad-hoc worktree parent is not a real directory: %s", probe)
			}
			// #nosec G703 -- inspect only the fixed Git marker in that ancestor.
			if _, err := os.Lstat(filepath.Join(probe, ".git")); err == nil {
				return fmt.Errorf("ad-hoc worktree parent is inside a Git repository or worktree: %s", probe)
			} else if !os.IsNotExist(err) {
				return fmt.Errorf("cannot inspect parent repository boundary: %w", err)
			}
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("cannot inspect ad-hoc worktree parent: %w", err)
		}
		if probe == filepath.Dir(probe) {
			return nil
		}
	}
}

func outsideRepairPrimaryProjectID(ctx context.Context, s *store.Store, workID string) (string, error) {
	projects, err := s.ProjectsForWork(ctx, workID)
	if err != nil {
		return "", err
	}
	for _, project := range projects {
		if project.Role == "primary" {
			return project.ID, nil
		}
	}
	return "", fmt.Errorf("outside repair: work has no primary Project")
}

// Hash the identity as well as the invocation time: store references can
// contain colons and dots that are not safe Git branch components.
func outsideRepairShortSlug(workID string, now func() time.Time) string {
	digest := sha256.Sum256([]byte(workID + ":" + strconv.FormatInt(now().UnixNano(), 10)))
	return hex.EncodeToString(digest[:12])
}

func outsideRepairFetchDefault(ctx context.Context, runner store.FreshnessRunner, repo, defaultRef string) error {
	branch := strings.TrimPrefix(defaultRef, "origin/")
	// Mirror only the default tracking ref, not configured local ref mappings.
	args := []string{"fetch", "--quiet", "--no-tags", "--no-recurse-submodules", "--refmap=", "origin", "+refs/heads/" + branch + ":refs/remotes/origin/" + branch}
	_, err := runner.RunNoninteractive(ctx, repo, args...)
	return err
}

func outsideRepairResolveCommit(ctx context.Context, runner store.GitRunner, repo, ref string) (string, error) {
	out, err := runner.Run(ctx, repo, "rev-parse", "--verify", ref+"^{commit}")
	if err != nil {
		return "", fmt.Errorf("cannot resolve %s to a commit: %w", ref, err)
	}
	sha := strings.TrimSpace(string(out))
	decoded, err := hex.DecodeString(sha)
	if err != nil || len(decoded) != 20 || strings.ToLower(sha) != sha {
		return "", fmt.Errorf("resolved commit %q is not a 40-character lowercase hex SHA", sha)
	}
	return sha, nil
}

func outsideRepairBuildPrompt(workID string, pin store.WorkPin) string {
	reason := ""
	if pin.OutsideRepairDisposition != nil {
		reason = pin.OutsideRepairDisposition.Reason
	}
	return fmt.Sprintf("Outside repair session for work %s\nProject: %s\nTitle: %s\nLinear: %s\nHold reason: %s\n\n"+
		"Repair the bounded defect in this ad-hoc branch under host permissions and repository rules. "+
		"This session has no Concord workflow authority. Use read-only Concord tools for context only; do not perform Concord writes, managed dispatch, worktree claims, or workflow actions. "+
		"Use the pull request, required checks, and published release as ordinary repository evidence, never as fabricated Concord workflow evidence. "+
		"Later reconciliation belongs to an authorized coordinator with the required operator approval, not this repair agent. No managed session boot packet is supplied.",
		workID, pin.ProjectID, pin.Title, pin.LinearIssueKey, reason)
}

func outsideRepairBuildEnv() []string {
	env := os.Environ()
	out := make([]string, 0, len(env))
	for _, value := range env {
		if !outsideRepairManagedEnvName(value) {
			out = append(out, value)
		}
	}
	return out
}

func outsideRepairManagedEnvName(value string) bool {
	for _, prefix := range outsideRepairManagedEnvPrefixes {
		if strings.HasPrefix(value, prefix) {
			return true
		}
	}
	return false
}

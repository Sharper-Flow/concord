package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
)

// concord ci-wait runs one deterministic, bounded slice of a GitHub CI wait.
// CD-0160 gives it the responsibility the generated prompt could not carry:
// deadline enforcement, poll pacing, iteration counting, and terminal-state
// classification live here, not in model reasoning. One invocation advances
// the wait by at most ciWaitSliceSeconds and reports the observed state; the
// caller re-invokes until the report is terminal.
//
// The command is store-free: a CI wait touches GitHub through `gh` and a
// state file, never Concord authority, so it routes around the database open
// like predecessor-inventory. It stays read-only against GitHub: every gh
// invocation is a query subcommand.
//
// Segmentation exists because the host bash tool kills long-running commands
// (default 2 minutes, hard maximum 10 minutes), so a single blocking 30-minute
// command can never deliver a report. The deadline survives across invocations
// in a state file under the operator's state directory; the first invocation
// records started_at, later invocations resume the same deadline.

const (
	ciWaitSliceSeconds    = 100
	ciWaitPollSeconds     = 15
	ciWaitMaxIterations   = 120
	ciWaitDefaultBudget   = 1800
	ciWaitShutdownSlack   = 5 * time.Second
	ciWaitStateDir        = "concord"
	ciWaitStateFilePrefix = "ci-wait-"
	ciWaitStateMaxAge     = 24 * time.Hour
)

// ciWaitCommandTimeout bounds one gh invocation. It is a variable so the test
// binary can shrink it; the released binary keeps the 45-second bound.
var ciWaitCommandTimeout = 45 * time.Second

// ciWaitRequest is the JSON body the utility agent posts on stdin.
// TimeSecondsMax is a pointer so an explicit 0 (deadline immediately) stays
// distinct from an omitted field (the registry's 1800-second default).
type ciWaitRequest struct {
	Selector       *ciWaitSelector `json:"selector"`
	Repo           string          `json:"repo"`
	Mode           string          `json:"mode"`
	TimeSecondsMax *int            `json:"time_seconds_max"`
	StateFile      string          `json:"state_file"`
}

type ciWaitSelector struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

// ciWaitReport is the JSON object on stdout. Status is closed: pending means
// re-invoke; every other status is terminal for the whole wait.
type ciWaitReport struct {
	Status     string         `json:"status"`
	Reason     string         `json:"reason,omitempty"`
	SHA        string         `json:"sha,omitempty"`
	HeadSHA    string         `json:"head_sha,omitempty"`
	RunURL     string         `json:"run_url,omitempty"`
	MergeState string         `json:"merge_state,omitempty"`
	Checks     *ciWaitChecks  `json:"checks,omitempty"`
	Failures   []ciWaitDetail `json:"failures,omitempty"`
	Iterations int            `json:"iterations"`
	Elapsed    int            `json:"elapsed_seconds"`
	Deadline   int            `json:"deadline_seconds"`
	StateFile  string         `json:"state_file,omitempty"`
}

type ciWaitChecks struct {
	Total   int `json:"total"`
	Passing int `json:"passing"`
	Failing int `json:"failing"`
	Skipped int `json:"skipped"`
	Pending int `json:"pending"`
}

type ciWaitDetail struct {
	Name           string `json:"name"`
	URL            string `json:"url,omitempty"`
	Classification string `json:"classification"`
	FirstError     string `json:"first_error,omitempty"`
}

// ciWaitState is the durable slice of one wait: the deadline anchor plus the
// counters the CLI owns. The model never supplies these values.
type ciWaitState struct {
	SchemaVersion  int            `json:"schema_version"`
	Selector       ciWaitSelector `json:"selector"`
	Repo           string         `json:"repo"`
	BudgetSeconds  int            `json:"budget_seconds"`
	StartedAt      time.Time      `json:"started_at"`
	DeadlineAt     time.Time      `json:"deadline_at"`
	Iterations     int            `json:"iterations"`
	LastSHA        string         `json:"last_sha,omitempty"`
	HeadSHA        string         `json:"head_sha,omitempty"`
	LastMergeState string         `json:"last_merge_state,omitempty"`
	Mode           string         `json:"mode,omitempty"`
}

func runCiWait(raw []byte, out, errOut io.Writer) int {
	var request ciWaitRequest
	if err := decodeObject(raw, &request); err != nil {
		writeOperatorDiagnostic(errOut, "ci-wait", err.Error())
		return 1
	}

	state, stateFile, err := ciWaitLoadOrCreate(request)
	if err != nil {
		return ciWaitEmit(out, ciWaitReport{Status: "refused", Reason: err.Error()}, 1)
	}

	// The wall-time budget is a ceiling on the caller's declaration, never a
	// grant: a caller cannot extend a wait the registry bounds at 1800s.
	if !time.Now().Before(state.DeadlineAt) {
		return ciWaitFinishTimeout(state, stateFile, out)
	}

	remaining := time.Until(state.DeadlineAt)
	slice := time.Duration(ciWaitSliceSeconds) * time.Second
	if remaining-ciWaitShutdownSlack < slice {
		slice = remaining - ciWaitShutdownSlack
	}
	if slice < time.Second {
		return ciWaitFinishTimeout(state, stateFile, out)
	}
	sliceEnd := time.Now().Add(slice)

	// lastObservation carries the most recent poll's fields into the pending
	// report, so a caller that re-invokes sees what the last slice saw.
	var lastObservation ciWaitReport
	for {
		state.Iterations++
		report, terminal, terr := ciWaitPoll(state)
		if terr != nil {
			// A provider, auth, or transport failure is an explicit error.
			// It is never a success, and it keeps the state file so the wait
			// can resume after the caller retries.
			report.Status = "error"
			report.Reason = terr.Error()
			report.Iterations = state.Iterations
			report.Elapsed = int(time.Since(state.StartedAt).Seconds())
			report.Deadline = state.BudgetSeconds
			report.SHA = state.LastSHA
			report.HeadSHA = state.HeadSHA
			report.MergeState = state.LastMergeState
			report.StateFile = stateFile
			ciWaitSaveState(state, stateFile)
			return ciWaitEmit(out, report, 1)
		}
		if terminal {
			ciWaitRemoveState(stateFile)
			report.Iterations = state.Iterations
			report.Elapsed = int(time.Since(state.StartedAt).Seconds())
			report.Deadline = state.BudgetSeconds
			return ciWaitEmit(out, report, 0)
		}
		lastObservation = report
		if !time.Now().Add(ciWaitPollSeconds).Before(sliceEnd) {
			break
		}
		// Deterministic sleep inside the slice; the model issues no commands.
		time.Sleep(ciWaitPollSeconds)
	}

	pending := ciWaitReport{
		Status:     "pending",
		Reason:     lastObservation.Reason,
		SHA:        lastObservation.SHA,
		HeadSHA:    lastObservation.HeadSHA,
		RunURL:     lastObservation.RunURL,
		MergeState: lastObservation.MergeState,
		Checks:     lastObservation.Checks,
		Iterations: state.Iterations,
		Elapsed:    int(time.Since(state.StartedAt).Seconds()),
		Deadline:   state.BudgetSeconds,
		StateFile:  stateFile,
	}
	ciWaitSaveState(state, stateFile)
	return ciWaitEmit(out, pending, 0)
}

// ciWaitPoll reads the current state once and classifies it. The observation
// comes from gh; the classification derives from it and nothing else.
func ciWaitPoll(state *ciWaitState) (ciWaitReport, bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), ciWaitCommandTimeout)
	defer cancel()

	switch state.Selector.Kind {
	case "pr":
		return ciWaitPollPR(ctx, state)
	case "sha":
		return ciWaitPollSHA(ctx, state)
	case "run":
		return ciWaitPollRun(ctx, state)
	default:
		return ciWaitReport{}, false, fmt.Errorf("selector.kind must be one of pr, sha, or run")
	}
}

func ciWaitGH(ctx context.Context, args ...string) ([]byte, error) {
	//nolint:gosec // G204: subcommands are a fixed read-only query set, and every
	// variable arg is validated before this call: selector values are
	// digits-only or hex-40, and repo is a token the gh binary parses, never a
	// shell input.
	cmd := exec.CommandContext(ctx, "gh", args...)
	// The gh process becomes its own process group leader, so the kill below
	// reaches every child it spawned. Without this, a hung `gh` child keeps
	// the stdout pipe open and outlives the command timeout.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	// Cancel kills the whole process group, not just the direct child.
	cmd.Cancel = func() error {
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		return os.ErrProcessDone
	}
	cmd.WaitDelay = time.Second
	runErr := cmd.Run()
	if runErr != nil {
		detail := strings.TrimSpace(stderr.String())
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("gh did not answer within its command timeout")
		}
		if detail == "" {
			detail = runErr.Error()
		}
		return nil, fmt.Errorf("gh %s: %s", strings.Join(args[:min(2, len(args))], " "), detail)
	}
	return stdout.Bytes(), nil
}

func ciWaitDecode(body []byte, target any) error {
	if err := json.Unmarshal(body, target); err != nil {
		return fmt.Errorf("gh returned unreadable JSON: %s", err)
	}
	return nil
}

// --- PR selector ---

type ghPRCheck struct {
	Name   string `json:"name"`
	State  string `json:"state"`
	Link   string `json:"link"`
	Bucket string `json:"bucket"`
}

type ghPRHead struct {
	HeadRefOid       string `json:"headRefOid"`
	URL              string `json:"url"`
	State            string `json:"state"`
	MergedAt         string `json:"mergedAt"`
	MergeStateStatus string `json:"mergeStateStatus"`
}

func ciWaitPollPR(ctx context.Context, state *ciWaitState) (ciWaitReport, bool, error) {
	headBody, err := ciWaitGH(ctx, "pr", "view", state.Selector.Value, "--repo", state.Repo, "--json", "headRefOid,url,state,mergedAt,mergeStateStatus")
	if err != nil {
		return ciWaitReport{}, false, err
	}
	var head ghPRHead
	if err := ciWaitDecode(headBody, &head); err != nil {
		return ciWaitReport{}, false, err
	}
	state.LastMergeState = head.MergeStateStatus
	if head.HeadRefOid == "" {
		return ciWaitReport{}, false, fmt.Errorf("gh pr view returned no head SHA")
	}
	// Head-SHA policy: the wait watches one commit. A pushed correction means
	// the checks now observed belong to a different subject than the one the
	// caller asked about, so the wait stops with superseded instead of
	// reporting a verdict for a SHA nobody requested.
	if state.HeadSHA == "" {
		state.HeadSHA = head.HeadRefOid
		state.LastSHA = head.HeadRefOid
	} else if head.HeadRefOid != state.HeadSHA {
		report := ciWaitReport{
			Status:     "superseded",
			Reason:     "the pull request head changed during the wait",
			HeadSHA:    head.HeadRefOid,
			SHA:        state.HeadSHA,
			RunURL:     head.URL,
			MergeState: head.MergeStateStatus,
		}
		return report, true, nil
	}

	report := ciWaitReport{
		Status:     "pending",
		SHA:        state.LastSHA,
		HeadSHA:    state.HeadSHA,
		RunURL:     head.URL,
		MergeState: head.MergeStateStatus,
	}
	if head.MergedAt != "" {
		report.Status = "merged"
		report.Reason = "the pull request is merged"
		return report, true, nil
	}
	if strings.EqualFold(head.State, "CLOSED") {
		report.Status = "closed"
		report.Reason = "the pull request is closed without a merge"
		return report, true, nil
	}
	if state.Mode == "merge" {
		if ciWaitMergeable(head.MergeStateStatus) {
			report.Status = "success"
			report.Reason = "the pull request can merge"
			return report, true, nil
		}
		report.Reason = "the pull request is not mergeable yet"
		return report, false, nil
	}

	checksBody, err := ciWaitGH(ctx, "pr", "checks", state.Selector.Value,
		"--repo", state.Repo, "--json", "name,state,link,bucket")
	if err != nil {
		return ciWaitReport{}, false, err
	}
	var checks []ghPRCheck
	if err := ciWaitDecode(checksBody, &checks); err != nil {
		return ciWaitReport{}, false, err
	}

	counts := &ciWaitChecks{Total: len(checks)}
	var failures []ciWaitDetail
	for _, check := range checks {
		switch check.Bucket {
		case "pass":
			counts.Passing++
		case "fail":
			counts.Failing++
			failures = append(failures, ciWaitDetail{
				Name:           check.Name,
				URL:            check.Link,
				Classification: ciWaitClassifyName(check.Name),
			})
		case "skipping":
			counts.Skipped++
		default:
			counts.Pending++
		}
	}
	report.Checks = counts
	// An empty check set is never success: a PR with no checks yet stays
	// pending until the deadline, and the timeout report says so.
	if counts.Total == 0 {
		report.Reason = "no checks reported for this pull request yet"
		return report, false, nil
	}
	if counts.Pending == 0 {
		if counts.Failing > 0 {
			report.Status = "failure"
			report.Failures = failures
			report.RunURL = ciWaitFirstRunURL(checks)
			ciWaitCollectFailureExcerpts(ctx, state, report.Failures)
		} else if ciWaitMergeConflict(head.MergeStateStatus) {
			report.Reason = "the pull request checks passed but GitHub reports a merge conflict"
			return report, false, nil
		} else {
			report.Status = "success"
		}
		return report, true, nil
	}
	return report, false, nil
}

// ciWaitMergeConflict reports whether GitHub's merge state is direct evidence
// that the merge commit cannot be created. DIRTY is that evidence. The lazy
// UNKNOWN, BEHIND, BLOCKED, UNSTABLE, and DRAFT values, and an empty value,
// carry no verdict about check completeness, so in checks mode they never
// block a green check set (CD-0161).
func ciWaitMergeConflict(status string) bool {
	return strings.EqualFold(status, "DIRTY")
}

// ciWaitMergeable is the merge-mode success gate: GitHub asserts that the
// pull request can merge only for CLEAN or HAS_HOOKS. Every other value,
// including the lazy UNKNOWN, stays pending (CD-0160 D4, CD-0161).
func ciWaitMergeable(status string) bool {
	switch strings.ToUpper(status) {
	case "CLEAN", "HAS_HOOKS":
		return true
	default:
		return false
	}
}

// --- SHA selector ---

type ghRunSummary struct {
	DatabaseID int64  `json:"databaseId"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	URL        string `json:"url"`
}

func ciWaitPollSHA(ctx context.Context, state *ciWaitState) (ciWaitReport, bool, error) {
	body, err := ciWaitGH(ctx, "run", "list", "--repo", state.Repo,
		"--commit", state.Selector.Value, "--json", "databaseId,name,status,conclusion,url")
	if err != nil {
		return ciWaitReport{}, false, err
	}
	var runs []ghRunSummary
	if err := ciWaitDecode(body, &runs); err != nil {
		return ciWaitReport{}, false, err
	}
	state.LastSHA = strings.ToLower(state.Selector.Value)
	// A commit with no runs yet is pending, never success: the empty set does
	// not silently count as passing CI.
	if len(runs) == 0 {
		return ciWaitReport{
			Status: "pending",
			Reason: "no workflow runs observed for this commit yet",
			SHA:    state.LastSHA,
			Checks: &ciWaitChecks{},
		}, false, nil
	}
	counts := &ciWaitChecks{Total: len(runs)}
	var failures []ciWaitDetail
	for _, run := range runs {
		switch run.Status {
		case "completed":
			switch run.Conclusion {
			case "success":
				counts.Passing++
			case "skipped", "neutral":
				counts.Skipped++
			case "cancelled":
				counts.Failing++
				failures = append(failures, ciWaitDetail{
					Name: run.Name, URL: run.URL, Classification: "cancelled",
				})
			default:
				counts.Failing++
				failures = append(failures, ciWaitDetail{
					Name: run.Name, URL: run.URL,
					Classification: ciWaitClassifyName(run.Name),
				})
			}
		default:
			counts.Pending++
		}
	}
	report := ciWaitReport{
		Status: "pending",
		SHA:    state.LastSHA,
		Checks: counts,
		RunURL: runs[0].URL,
	}
	if counts.Pending == 0 {
		if counts.Failing > 0 {
			report.Status = "failure"
			report.Failures = failures
		} else {
			report.Status = "success"
		}
		return report, true, nil
	}
	return report, false, nil
}

// --- Run selector ---

type ghRunDetail struct {
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	URL        string `json:"url"`
	HeadSHA    string `json:"headSha"`
}

func ciWaitPollRun(ctx context.Context, state *ciWaitState) (ciWaitReport, bool, error) {
	body, err := ciWaitGH(ctx, "run", "view", state.Selector.Value,
		"--repo", state.Repo, "--json", "status,conclusion,url,headSha")
	if err != nil {
		return ciWaitReport{}, false, err
	}
	var run ghRunDetail
	if err := ciWaitDecode(body, &run); err != nil {
		return ciWaitReport{}, false, err
	}
	state.LastSHA = run.HeadSHA
	counts := &ciWaitChecks{Total: 1}
	report := ciWaitReport{
		Status: "pending",
		SHA:    run.HeadSHA,
		RunURL: run.URL,
		Checks: counts,
	}
	if run.Status != "completed" {
		counts.Pending = 1
		return report, false, nil
	}
	switch run.Conclusion {
	case "success":
		counts.Passing = 1
		report.Status = "success"
	case "skipped", "neutral":
		counts.Skipped = 1
		report.Status = "success"
		report.Reason = "the run concluded " + run.Conclusion
	case "cancelled":
		counts.Failing = 1
		report.Status = "failure"
		report.Failures = []ciWaitDetail{{
			Name: "run " + state.Selector.Value, URL: run.URL, Classification: "cancelled",
		}}
	default:
		counts.Failing = 1
		report.Status = "failure"
		report.Failures = []ciWaitDetail{{
			Name: "run " + state.Selector.Value, URL: run.URL,
			Classification: ciWaitClassifyName(""),
		}}
	}
	return report, true, nil
}

// ciWaitClassifyName derives a coarse classification from the check or run
// name alone. Specific markers win over generic ones, so a migration check
// named with the word "tests" still classifies as a build failure. It never
// invents an error line: FirstError stays empty unless a log read supplies
// one, and an empty field means "not collected".
func ciWaitClassifyName(name string) string {
	lower := strings.ToLower(name)
	switch {
	case strings.Contains(lower, "migration") || strings.Contains(lower, "build") || strings.Contains(lower, "compile"):
		return "build_failure"
	case strings.Contains(lower, "test") || strings.Contains(lower, "e2e") || strings.Contains(lower, "journey"):
		return "test_failure"
	case strings.Contains(lower, "lint") || strings.Contains(lower, "gate") || strings.Contains(lower, "check") || strings.Contains(lower, "validator") || strings.Contains(lower, "scan"):
		return "lint_or_validator"
	case strings.Contains(lower, "infra") || strings.Contains(lower, "deploy") || strings.Contains(lower, "bicep"):
		return "infrastructure"
	default:
		return "unknown"
	}
}

var ghRunURLPattern = regexp.MustCompile(`/actions/runs/(\d+)`)

// ciWaitCollectFailureExcerpts enriches each failing check with the first
// error line from its run's failed logs. It is best effort: a read that fails
// or times out leaves FirstError empty, which means "not collected", never a
// fabricated line. The log read runs inside the slice context, so a hung log
// fetch cannot push the wait past its deadline.
func ciWaitCollectFailureExcerpts(ctx context.Context, state *ciWaitState, failures []ciWaitDetail) {
	for i := range failures {
		runID := ghRunIDFromURL(failures[i].URL)
		if runID == "" {
			continue
		}
		// A fresh short context per read: one slow log fetch cannot consume
		// the whole slice budget of the others.
		readCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		logBody, err := ciWaitGH(readCtx, "run", "view", runID,
			"--repo", state.Repo, "--log-failed")
		cancel()
		if err != nil || len(logBody) == 0 {
			continue
		}
		failures[i].FirstError = ciWaitFirstErrorLine(string(logBody))
	}
}

// ghRunIDFromURL extracts the numeric run id from a check's job link.
func ghRunIDFromURL(link string) string {
	match := ghRunURLPattern.FindStringSubmatch(link)
	return match[1]
}

// ciWaitFirstErrorLine picks the first line from failed logs that reads as an
// error. Preference order: Go/test failure markers, then the first line that
// mentions an error, then the first non-empty line. All candidates come from
// the observed log; nothing is composed.
func ciWaitFirstErrorLine(log string) string {
	lines := strings.Split(log, "\n")
	failureMarkers := []string{"--- FAIL:", "FAIL\t", "Error:", "error:", "ERROR:", "##[error]"}
	for _, marker := range failureMarkers {
		for _, line := range lines {
			if strings.Contains(line, marker) {
				return ciWaitTrimLine(line)
			}
		}
	}
	for _, line := range lines {
		if strings.TrimSpace(line) != "" {
			return ciWaitTrimLine(line)
		}
	}
	return ""
}

func ciWaitTrimLine(line string) string {
	line = strings.TrimSpace(line)
	// gh log lines carry a "job\tstep\tmessage" prefix; keep the message part.
	if parts := strings.SplitN(line, "\t", 3); len(parts) == 3 {
		line = strings.TrimSpace(parts[2])
	}
	if len(line) > 300 {
		line = line[:297] + "..."
	}
	return line
}

// ciWaitFirstRunURL picks one run URL from the observed check links so the
// failure report names where the failure lives.
func ciWaitFirstRunURL(checks []ghPRCheck) string {
	for _, check := range checks {
		if check.Bucket == "fail" {
			if runID := ghRunIDFromURL(check.Link); runID != "" {
				return "https://github.com/actions/runs/" + runID
			}
			return check.Link
		}
	}
	return ""
}

// --- lifecycle helpers ---

func ciWaitEmit(out io.Writer, report ciWaitReport, exit int) int {
	encoded, err := json.Marshal(report)
	if err != nil {
		return 1
	}
	_, _ = out.Write(append(encoded, '\n'))
	return exit
}

// ciWaitStatePath resolves the state file location. The state file carries the
// deadline across segmented invocations, so the directory must be writable by
// the utility session but must not depend on the repository.
func ciWaitStatePath(requested string) (string, error) {
	if requested != "" {
		if filepath.IsAbs(requested) {
			return requested, nil
		}
		return "", fmt.Errorf("state_file must be an absolute path")
	}
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("cannot resolve a state directory: %w", err)
		}
		base = filepath.Join(home, ".local", "state")
	}
	dir := filepath.Join(base, ciWaitStateDir)
	if err := os.MkdirAll(dir, 0o750); err != nil { //nolint:gosec // G703: dir is composed of an operator-resolved state root (XDG_STATE_HOME or UserHomeDir) and a constant subdirectory name; the hello path never crosses a caller string.
		return "", fmt.Errorf("cannot create the state directory: %w", err)
	}
	return filepath.Join(dir, fmt.Sprintf("%s%d.json", ciWaitStateFilePrefix, os.Getpid())), nil
}

func ciWaitLoadOrCreate(request ciWaitRequest) (*ciWaitState, string, error) {
	if request.StateFile != "" {
		if state, ok := ciWaitReadState(request.StateFile); ok {
			if state.Mode == "" {
				state.Mode = "checks"
			}
			return state, request.StateFile, nil
		}
		// A named state file that exists but cannot be read back is a caller
		// error: silently starting a new wait here would reset the deadline.
		if _, statErr := os.Stat(request.StateFile); statErr == nil {
			return nil, "", fmt.Errorf("state_file exists but cannot be read as a ci-wait state file")
		}
	}
	if request.Selector == nil {
		return nil, "", fmt.Errorf("selector is required: one of pr, sha, or run with its value")
	}
	if request.Repo == "" {
		return nil, "", fmt.Errorf("repo is required as owner/name")
	}
	switch request.Selector.Kind {
	case "pr":
		if !ciWaitDigitsOnly(request.Selector.Value) {
			return nil, "", fmt.Errorf("a pr selector value must be a pull request number")
		}
	case "sha":
		if !ciWaitHex40(request.Selector.Value) {
			return nil, "", fmt.Errorf("a sha selector value must be a 40-character commit SHA")
		}
	case "run":
		if !ciWaitDigitsOnly(request.Selector.Value) {
			return nil, "", fmt.Errorf("a run selector value must be a run id")
		}
	default:
		return nil, "", fmt.Errorf("selector.kind must be one of pr, sha, or run")
	}
	mode := request.Mode
	if mode == "" {
		mode = "checks"
	}
	if mode != "checks" && mode != "merge" {
		return nil, "", fmt.Errorf("mode must be checks or merge")
	}
	if mode == "merge" && request.Selector.Kind != "pr" {
		return nil, "", fmt.Errorf("mode merge requires a pr selector")
	}

	budget := ciWaitDefaultBudget
	if request.TimeSecondsMax != nil {
		budget = *request.TimeSecondsMax
		if budget < 0 || budget > ciWaitDefaultBudget {
			return nil, "", fmt.Errorf("time_seconds_max must be between 0 and %d", ciWaitDefaultBudget)
		}
	}
	now := time.Now().UTC()
	state := &ciWaitState{
		SchemaVersion: 1,
		Selector:      *request.Selector,
		Repo:          request.Repo,
		BudgetSeconds: budget,
		StartedAt:     now,
		DeadlineAt:    now.Add(time.Duration(budget) * time.Second),
		Mode:          mode,
	}
	path, err := ciWaitStatePath(request.StateFile)
	if err != nil {
		return nil, "", err
	}
	return state, path, nil
}

func ciWaitReadState(path string) (*ciWaitState, bool) {
	raw, err := os.ReadFile(path) //nolint:gosec // G304: the path is this CLI's own state location — built by ciWaitStatePath under the operator's state directory, or an absolute caller path whose unreadable content answers false below.
	if err != nil {
		return nil, false
	}
	if err != nil {
		return nil, false
	}
	var state ciWaitState
	if err := json.Unmarshal(raw, &state); err != nil {
		return nil, false
	}
	if state.SchemaVersion != 1 || time.Since(state.DeadlineAt) > ciWaitStateMaxAge {
		return nil, false
	}
	return &state, true
}

func ciWaitSaveState(state *ciWaitState, path string) {
	if path == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		return
	}
	_ = os.WriteFile(path, encoded, 0o600)
}

func ciWaitRemoveState(path string) {
	if path == "" {
		return
	}
	_ = os.Remove(path)
}

func ciWaitDigitsOnly(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func ciWaitHex40(value string) bool {
	if len(value) != 40 {
		return false
	}
	for _, r := range value {
		ok := (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')
		if !ok {
			return false
		}
	}
	return true
}

// ciWaitFinishTimeout closes a wait whose deadline passed. The deadline comes
// from the state the CLI itself wrote, never from the caller. A pending check
// at the bound is a timeout, never a success.
func ciWaitFinishTimeout(state *ciWaitState, stateFile string, out io.Writer) int {
	ciWaitRemoveState(stateFile)
	report := ciWaitReport{
		Status:     "timeout",
		Reason:     "the wall-time deadline expired before CI reached a terminal state",
		Iterations: state.Iterations,
		Elapsed:    int(time.Since(state.StartedAt).Seconds()),
		Deadline:   state.BudgetSeconds,
		SHA:        state.LastSHA,
		HeadSHA:    state.HeadSHA,
		MergeState: state.LastMergeState,
	}
	return ciWaitEmit(out, report, 1)
}

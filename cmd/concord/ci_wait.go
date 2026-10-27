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
	"sort"
	"strconv"
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
// invocation is a query subcommand, a fixed REST endpoint read with GET, or
// the one constant GraphQL query document (CD-0232, CD-0233).
//
// Segmentation exists because the host bash tool kills long-running commands
// (default 2 minutes, hard maximum 10 minutes), so a single blocking 30-minute
// command can never deliver a report. The deadline survives across invocations
// in a state file under the operator's state directory; the first invocation
// records started_at, later invocations resume the same deadline.

const (
	ciWaitSliceSeconds      = 100
	ciWaitPollFastSeconds   = 15
	ciWaitPollMediumSeconds = 30
	ciWaitPollSlowSeconds   = 60
	ciWaitPollMediumAfter   = 60 * time.Second
	ciWaitPollSlowAfter     = 300 * time.Second
	ciWaitCommandRetryDelay = 2 * time.Second
	ciWaitPRRollupPageSize  = 100
	ciWaitDefaultBudget     = 1800
	ciWaitShutdownSlack     = 5 * time.Second
	ciWaitStateDir          = "concord"
	ciWaitStateFilePrefix   = "ci-wait-"
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
	// HeadSHA names the commit a pr wait judges, normally the one the caller
	// just pushed. Empty means the head the first poll observes.
	HeadSHA string `json:"head_sha"`
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
	Total   int           `json:"total"`
	Passing int           `json:"passing"`
	Failing int           `json:"failing"`
	Skipped int           `json:"skipped"`
	Pending int           `json:"pending"`
	Entries []ciWaitCheck `json:"entries,omitempty"`
}

type ciWaitCheck struct {
	Name           string `json:"name"`
	URL            string `json:"url,omitempty"`
	Bucket         string `json:"bucket"`
	Classification string `json:"-"`
	FirstError     string `json:"-"`
	DetailError    string `json:"-"`
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
	OwnerPID       int            `json:"owner_pid,omitempty"`
	// ExpectedHeadSHA is the commit the caller named. HeadSHA stays empty
	// until the pull request head reaches it.
	ExpectedHeadSHA string `json:"expected_head_sha,omitempty"`
	// MergeQueueBaseline is the newest merge-queue event id the wait's first
	// queue read observed. Nil means not read yet; "" means no event.
	MergeQueueBaseline *string `json:"merge_queue_baseline,omitempty"`
	// RequiredBase names the base branch RequiredContexts were read for. An
	// empty value means the required contexts are not read yet.
	RequiredBase     string   `json:"required_base,omitempty"`
	RequiredContexts []string `json:"required_contexts,omitempty"`
}

func runCiWait(raw []byte, out, errOut io.Writer) int {
	var request ciWaitRequest
	if err := decodeObject(raw, &request); err != nil {
		writeOperatorDiagnostic(errOut, "ci-wait", diagnosticText(err))
		return 1
	}

	state, stateFile, err := ciWaitLoadOrCreate(request)
	if err != nil {
		return ciWaitEmit(out, ciWaitReport{Status: "refused", Reason: err.Error()}, 1)
	}
	// Only pending can resume. The caller owns the wait across child slices;
	// persisting before polling also preserves the deadline if a child dies.
	resumable := false
	defer func() {
		if !resumable {
			ciWaitRemoveState(stateFile)
		}
	}()
	ciWaitSaveState(state, stateFile)

	// The wall-time budget is a ceiling on the caller's declaration, never a
	// grant: a caller cannot extend a wait the registry bounds at 1800s.
	if !time.Now().Before(state.DeadlineAt) {
		return ciWaitFinishTimeout(state, out)
	}

	remaining := time.Until(state.DeadlineAt)
	slice := time.Duration(ciWaitSliceSeconds) * time.Second
	if remaining-ciWaitShutdownSlack < slice {
		slice = remaining - ciWaitShutdownSlack
	}
	if slice < time.Second {
		return ciWaitFinishTimeout(state, out)
	}
	sliceEnd := time.Now().Add(slice)
	sliceContext, cancel := context.WithDeadline(context.Background(), sliceEnd)
	defer cancel()

	// lastObservation carries the most recent poll's fields into the pending
	// report, so a caller that re-invokes sees what the last slice saw.
	var lastObservation ciWaitReport
	for {
		state.Iterations++
		report, terminal, terr := ciWaitPoll(sliceContext, state)
		if terr != nil {
			var stalled *ciWaitStallError
			if errors.As(terr, &stalled) {
				pending := ciWaitReport{
					Status:     "pending",
					Reason:     stalled.Error(),
					Iterations: state.Iterations,
					Elapsed:    int(time.Since(state.StartedAt).Seconds()),
					Deadline:   state.BudgetSeconds,
					SHA:        state.LastSHA,
					HeadSHA:    state.HeadSHA,
					MergeState: state.LastMergeState,
					StateFile:  stateFile,
				}
				ciWaitSaveState(state, stateFile)
				resumable = true
				return ciWaitEmit(out, pending, 0)
			}
			// A provider, auth, or transport failure ends the wait with an
			// explicit error, never success or a resumable pending report.
			report.Status = "error"
			report.Reason = terr.Error()
			report.Iterations = state.Iterations
			report.Elapsed = int(time.Since(state.StartedAt).Seconds())
			report.Deadline = state.BudgetSeconds
			report.SHA = state.LastSHA
			report.HeadSHA = state.HeadSHA
			report.MergeState = state.LastMergeState
			return ciWaitEmit(out, report, 1)
		}
		if terminal {
			report.Iterations = state.Iterations
			report.Elapsed = int(time.Since(state.StartedAt).Seconds())
			report.Deadline = state.BudgetSeconds
			return ciWaitEmit(out, report, 0)
		}
		lastObservation = report
		interval := ciWaitPollInterval(time.Since(state.StartedAt))
		if !time.Now().Add(interval).Before(sliceEnd) {
			break
		}
		// Deterministic sleep inside the slice; the model issues no commands.
		time.Sleep(interval)
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
	resumable = true
	return ciWaitEmit(out, pending, 0)
}

// ciWaitPoll reads the current state once and classifies it. The observation
// comes from gh; the classification derives from it and nothing else.
//
// A resumed state file skips the request checks, yet its repo and selector
// value become gh arguments, API path segments, and a GraphQL variable, so
// every poll checks them again before any gh call.
func ciWaitPoll(ctx context.Context, state *ciWaitState) (ciWaitReport, bool, error) {
	if err := ciWaitSubjectError(state.Repo, state.Selector); err != nil {
		return ciWaitReport{}, false, fmt.Errorf("the state file names an invalid subject: %w", err)
	}
	switch state.Selector.Kind {
	case "pr":
		return ciWaitPollPR(ctx, state)
	case "sha":
		return ciWaitPollSHA(ctx, state)
	default:
		return ciWaitPollRun(ctx, state)
	}
}

var errCiWaitCommandTimeout = errors.New("gh command timeout")

type ciWaitStallError struct {
	command string
}

func (e *ciWaitStallError) Error() string {
	return fmt.Sprintf("gh command stalled after two timeout attempts: %s", e.command)
}

func ciWaitGH(ctx context.Context, args ...string) ([]byte, error) {
	for attempt := 0; attempt < 2; attempt++ {
		body, err := ciWaitGHOnce(ctx, args...)
		if err == nil {
			return body, nil
		}
		if !errors.Is(err, errCiWaitCommandTimeout) {
			return nil, err
		}
		if attempt == 1 {
			return nil, &ciWaitStallError{command: strings.Join(args[:min(2, len(args))], " ")}
		}
		timer := time.NewTimer(ciWaitCommandRetryDelay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return nil, &ciWaitStallError{command: strings.Join(args[:min(2, len(args))], " ")}
		case <-timer.C:
		}
	}
	return nil, &ciWaitStallError{command: strings.Join(args[:min(2, len(args))], " ")}
}

func ciWaitGHOnce(ctx context.Context, args ...string) ([]byte, error) {
	commandContext, cancel := context.WithTimeout(ctx, ciWaitCommandTimeout)
	defer cancel()
	//nolint:gosec // G204: subcommands are a fixed read-only query set, and every
	// variable arg is validated before this call: selector values are
	// digits-only or hex-40, repo is two plain path segments, a branch name is
	// escaped per path segment, and `gh api` reads only fixed GET endpoints
	// and one constant GraphQL query document. No arg is ever shell input.
	cmd := exec.CommandContext(commandContext, "gh", args...)
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
		if errors.Is(commandContext.Err(), context.DeadlineExceeded) {
			return nil, errCiWaitCommandTimeout
		}
		if detail == "" {
			detail = runErr.Error()
		}
		return nil, fmt.Errorf("gh %s: %s", strings.Join(args[:min(2, len(args))], " "), detail)
	}
	return stdout.Bytes(), nil
}

func ciWaitPollInterval(elapsed time.Duration) time.Duration {
	switch {
	case elapsed < ciWaitPollMediumAfter:
		return ciWaitPollFastSeconds * time.Second
	case elapsed < ciWaitPollSlowAfter:
		return ciWaitPollMediumSeconds * time.Second
	default:
		return ciWaitPollSlowSeconds * time.Second
	}
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
	HeadRefOid        string       `json:"headRefOid"`
	BaseRefName       string       `json:"baseRefName"`
	URL               string       `json:"url"`
	State             string       `json:"state"`
	MergedAt          string       `json:"mergedAt"`
	MergeStateStatus  string       `json:"mergeStateStatus"`
	StatusCheckRollup []ghPRRollup `json:"statusCheckRollup"`
}

type ghPRRollup struct {
	TypeName   string `json:"__typename"`
	Name       string `json:"name"`
	Context    string `json:"context"`
	State      string `json:"state"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	DetailsURL string `json:"detailsUrl"`
	TargetURL  string `json:"targetUrl"`
	Bucket     string `json:"bucket"`

	StartedAt    time.Time `json:"startedAt"`
	WorkflowName string    `json:"workflowName"`
}

func ciWaitPollPR(ctx context.Context, state *ciWaitState) (ciWaitReport, bool, error) {
	headBody, err := ciWaitGH(ctx, "pr", "view", state.Selector.Value, "--repo", state.Repo, "--json", "headRefOid,baseRefName,url,state,mergedAt,mergeStateStatus,statusCheckRollup")
	if err != nil {
		return ciWaitReport{}, false, err
	}
	var head ghPRHead
	if err := ciWaitDecode(headBody, &head); err != nil {
		return ciWaitReport{}, false, err
	}
	if head.HeadRefOid == "" {
		return ciWaitReport{}, false, fmt.Errorf("gh pr view returned no head SHA")
	}
	// A wait the caller named a commit for judges only that commit. Until
	// the pull request head reaches it, the observed head is the one the
	// push replaces, so its checks and merge state are no verdict. A pull
	// request merged or closed at another head never reaches the commit.
	if state.HeadSHA == "" && state.ExpectedHeadSHA != "" && head.HeadRefOid != state.ExpectedHeadSHA {
		report := ciWaitReport{
			Status:     "pending",
			Reason:     "the pull request head is not the expected head yet",
			SHA:        state.ExpectedHeadSHA,
			HeadSHA:    head.HeadRefOid,
			RunURL:     head.URL,
			MergeState: head.MergeStateStatus,
		}
		if head.MergedAt != "" || strings.EqualFold(head.State, "CLOSED") {
			report.Status = "superseded"
			report.Reason = "the pull request ended at a head other than the expected head"
			return report, true, nil
		}
		return report, false, nil
	}
	// Only the watched commit's merge state reaches a later stall, error, or
	// timeout report.
	state.LastMergeState = head.MergeStateStatus
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
	// CD-0161 D2: merge mode waits for the pull request to end, so a merged
	// or closed one ends the wait. Checks mode waits for the watched head's
	// checks, and a merge or close proves nothing about them.
	if state.Mode == "merge" && head.MergedAt != "" {
		report.Status = "merged"
		report.Reason = "the pull request is merged"
		return report, true, nil
	}
	if state.Mode == "merge" && strings.EqualFold(head.State, "CLOSED") {
		report.Status = "closed"
		report.Reason = "the pull request is closed without a merge"
		return report, true, nil
	}
	population, err := ciWaitPRPopulation(ctx, state, head)
	if err != nil {
		return report, false, err
	}
	report.Checks, _ = ciWaitCountPRChecks(population)
	// Queue removal retains precedence over head-check failures.
	if state.Mode == "merge" {
		removal, err := ciWaitMergeQueueRemoval(ctx, state)
		if err != nil {
			return ciWaitReport{}, false, err
		}
		if removal != nil {
			return ciWaitQueueRemovalReport(ctx, state, report, removal), true, nil
		}
	}
	runs, err := ciWaitWorkflowRuns(ctx, state, state.HeadSHA)
	if err != nil {
		return report, false, err
	}
	population = ciWaitWithoutWorkflowChecks(population, runs)
	if failed, terminal := ciWaitObservedFailure(state, report, population); terminal {
		return failed, true, nil
	}
	population, err = ciWaitObserveWorkflows(ctx, state, ciWaitNewestWorkflowRuns(runs), population)
	if err != nil {
		return report, false, err
	}
	if failed, terminal := ciWaitObservedFailure(state, report, population); terminal {
		return failed, true, nil
	}
	var missing []string
	if state.Mode != "merge" {
		required, err := ciWaitRequiredContexts(ctx, state, head.BaseRefName)
		if err != nil {
			return report, false, err
		}
		population, missing = ciWaitJoinRequired(population, required)
	}
	report.Checks, _ = ciWaitCountPRChecks(population)
	return ciWaitPRCheckReport(state, head, report, missing)
}

func ciWaitPRPopulation(ctx context.Context, state *ciWaitState, head ghPRHead) ([]ciWaitCheck, error) {
	var population []ciWaitCheck
	switch {
	case state.Mode != "merge" && len(head.StatusCheckRollup) == ciWaitPRRollupPageSize:
		checksBody, err := ciWaitGH(ctx, "pr", "checks", state.Selector.Value,
			"--repo", state.Repo, "--json", "name,state,link,bucket")
		if err != nil {
			return nil, err
		}
		var checks []ghPRCheck
		if err := ciWaitDecode(checksBody, &checks); err != nil {
			return nil, err
		}
		population = ciWaitPRCheckPopulation(checks, ciWaitNewestRollupPerIdentity(head.StatusCheckRollup))
	default:
		for _, entry := range ciWaitNewestRollupPerIdentity(head.StatusCheckRollup) {
			population = append(population, ciWaitRollupCheck(entry))
		}
	}
	return population, nil
}

func ciWaitCountPRChecks(population []ciWaitCheck) (*ciWaitChecks, []ciWaitDetail) {
	counts := &ciWaitChecks{Total: len(population), Entries: make([]ciWaitCheck, 0, len(population))}
	var failures []ciWaitDetail
	for _, check := range population {
		counts.Entries = append(counts.Entries, check)
		switch check.Bucket {
		case "pass":
			counts.Passing++
		case "fail":
			counts.Failing++
			classification := check.Classification
			if classification == "" {
				classification = ciWaitClassifyName(check.Name)
			}
			failures = append(failures, ciWaitDetail{
				Name:           check.Name,
				URL:            check.URL,
				Classification: classification,
				FirstError:     check.FirstError,
			})
		case "skipping":
			counts.Skipped++
		default:
			counts.Pending++
		}
	}
	return counts, failures
}

func ciWaitPRCheckReport(state *ciWaitState, head ghPRHead, report ciWaitReport, missing []string) (ciWaitReport, bool, error) {
	counts := report.Checks
	if state.Mode == "merge" {
		if ciWaitMergeable(head.MergeStateStatus) {
			report.Reason = "the pull request can merge but is not merged yet"
		} else {
			report.Reason = "the pull request is not mergeable yet"
		}
		return report, false, nil
	}
	if counts.Total == 0 {
		report.Reason = "no checks reported for this pull request yet"
		return report, false, nil
	}
	if len(missing) > 0 {
		report.Reason = "required checks not reported yet: " + strings.Join(missing, ", ")
		return report, false, nil
	}
	if counts.Pending > 0 {
		report.Reason = "the check and workflow population is not complete yet"
		return report, false, nil
	}
	if ciWaitMergeConflict(head.MergeStateStatus) {
		report.Reason = "the pull request checks passed but GitHub reports a merge conflict"
		return report, false, nil
	}
	report.Status = "success"
	return report, true, nil
}

// ciWaitPRCheckPopulation joins the gh pr checks snapshot with the pull
// request status rollup. A rollup entry replaces a same-name snapshot entry,
// because the rollup is the source that tells us whether GitHub registered the
// check and reached a terminal conclusion.
func ciWaitPRCheckPopulation(checks []ghPRCheck, rollup []ghPRRollup) []ciWaitCheck {
	population := make([]ciWaitCheck, 0, len(checks)+len(rollup))
	positions := make(map[string]int, len(checks)+len(rollup))
	for _, check := range checks {
		observed := ciWaitCheck{Name: check.Name, URL: check.Link, Bucket: check.Bucket}
		key := ciWaitCheckKey(observed.Name)
		if key == "" {
			key = fmt.Sprintf("snapshot-%d", len(population))
		}
		positions[key] = len(population)
		population = append(population, observed)
	}
	for _, entry := range rollup {
		observed := ciWaitRollupCheck(entry)
		key := ciWaitCheckKey(observed.Name)
		if position, ok := positions[key]; ok {
			population[position] = observed
			continue
		}
		positions[key] = len(population)
		population = append(population, observed)
	}
	return population
}

func ciWaitRollupCheck(entry ghPRRollup) ciWaitCheck {
	name := entry.Name
	if name == "" {
		name = entry.Context
	}
	if name == "" {
		name = "unnamed status check"
	}
	bucket, _ := ciWaitRollupBucket(entry)
	observed := ciWaitCheck{Name: name, URL: entry.DetailsURL, Bucket: bucket}
	if observed.URL == "" {
		observed.URL = entry.TargetURL
	}
	return observed
}

// ciWaitNewestRollupPerIdentity keeps the greatest StartedAt per check
// identity. A CheckRun's identity is (name, workflowName) because the rollup
// export omits the event field; a StatusContext's identity is its context, in
// a separate key namespace.
func ciWaitNewestRollupPerIdentity(rollup []ghPRRollup) []ghPRRollup {
	positions := make(map[[3]string]int, len(rollup))
	newest := make([]ghPRRollup, 0, len(rollup))
	for _, entry := range rollup {
		key := [3]string{"check", entry.Name, entry.WorkflowName}
		if entry.TypeName == "StatusContext" {
			key = [3]string{"context", entry.Context, ""}
		}
		if position, ok := positions[key]; ok {
			if entry.StartedAt.After(newest[position].StartedAt) {
				newest[position] = entry
			}
			continue
		}
		positions[key] = len(newest)
		newest = append(newest, entry)
	}
	return newest
}

func ciWaitCheckKey(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

func ciWaitRollupBucket(entry ghPRRollup) (string, bool) {
	if entry.Bucket != "" {
		bucket := strings.ToLower(entry.Bucket)
		return bucket, bucket != "pending" && bucket != "cancel"
	}
	conclusion := strings.ToUpper(strings.TrimSpace(entry.Conclusion))
	if conclusion != "" {
		if entry.Status != "" && !strings.EqualFold(entry.Status, "COMPLETED") {
			return "pending", false
		}
		switch conclusion {
		case "SUCCESS":
			return "pass", true
		case "SKIPPED", "NEUTRAL":
			return "skipping", true
		case "FAILURE", "CANCELLED", "TIMED_OUT", "ACTION_REQUIRED", "STARTUP_FAILURE", "STALE":
			return "fail", true
		default:
			return "pending", false
		}
	}
	// StatusContext entries expose a terminal state instead of a CheckRun
	// conclusion. Treat the known terminal states as their conclusion.
	switch strings.ToUpper(strings.TrimSpace(entry.State)) {
	case "SUCCESS":
		return "pass", true
	case "FAILURE", "ERROR":
		return "fail", true
	case "EXPECTED", "PENDING":
		return "pending", false
	}
	return "pending", false
}

// ciWaitMergeConflict reports whether GitHub's merge state is direct evidence
// that the merge commit cannot be created. DIRTY is that evidence. The lazy
// UNKNOWN, BEHIND, BLOCKED, UNSTABLE, and DRAFT values, and an empty value,
// carry no verdict about check completeness, so in checks mode they never
// block a green check set (CD-0161).
func ciWaitMergeConflict(status string) bool {
	return strings.EqualFold(status, "DIRTY")
}

// ciWaitMergeable reports whether GitHub's merge state says the pull request
// can merge: CLEAN, HAS_HOOKS, or UNSTABLE (mergeable with non-passing checks).
func ciWaitMergeable(status string) bool {
	switch strings.ToUpper(status) {
	case "CLEAN", "HAS_HOOKS", "UNSTABLE":
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
	CreatedAt  string `json:"createdAt"`
}

// ciWaitRunListFields is the field set every `gh run list` read requests.
const ciWaitRunListFields = "databaseId,name,status,conclusion,url,createdAt"

// ciWaitSortRuns puts a commit's runs in creation order, so the
// representative run and the failure order come from the provider's
// metadata, never from the order gh lists them in. A run that another run
// triggers is created after it, so the earliest run stands for the commit.
// gh reports createdAt in RFC 3339 UTC, which orders lexically; the run ID
// breaks a tie.
func ciWaitSortRuns(runs []ghRunSummary) {
	sort.SliceStable(runs, func(i, j int) bool {
		if runs[i].CreatedAt != runs[j].CreatedAt {
			return runs[i].CreatedAt < runs[j].CreatedAt
		}
		return runs[i].DatabaseID < runs[j].DatabaseID
	})
}

func ciWaitPollSHA(ctx context.Context, state *ciWaitState) (ciWaitReport, bool, error) {
	state.LastSHA = strings.ToLower(state.Selector.Value)
	runs, err := ciWaitWorkflowRuns(ctx, state, state.LastSHA)
	if err != nil {
		return ciWaitReport{}, false, err
	}
	runs = ciWaitNewestWorkflowRuns(runs)
	report := ciWaitReport{Status: "pending", SHA: state.LastSHA, Checks: &ciWaitChecks{}}
	if len(runs) == 0 {
		report.Reason = "no workflow runs observed for this commit yet"
		return report, false, nil
	}
	report.RunURL = runs[0].URL
	population, err := ciWaitObserveWorkflows(ctx, state, runs, nil)
	if err != nil {
		return report, false, err
	}
	if failed, terminal := ciWaitObservedFailure(state, report, population); terminal {
		return failed, true, nil
	}
	report.Checks, _ = ciWaitCountPRChecks(population)
	if report.Checks.Pending > 0 {
		report.Reason = "the workflow population is not complete yet"
		return report, false, nil
	}
	report.Status = "success"
	return report, true, nil
}

// ciWaitRunBucket classifies one workflow run: an unfinished run is pending,
// and every completed conclusion other than success, skipped, or neutral fails.
func ciWaitRunBucket(run ghRunSummary) string {
	if run.Status != "completed" {
		return "pending"
	}
	switch run.Conclusion {
	case "success":
		return "pass"
	case "skipped", "neutral":
		return "skipping"
	default:
		return "fail"
	}
}

func ciWaitRunFailure(run ghRunSummary) ciWaitDetail {
	classification := ciWaitClassifyName(run.Name)
	if run.Conclusion == "cancelled" {
		classification = "cancelled"
	}
	return ciWaitDetail{Name: run.Name, URL: run.URL, Classification: classification}
}

// --- Run selector ---

type ghRunDetail struct {
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	URL        string `json:"url"`
	HeadSHA    string `json:"headSha"`
	Attempt    int    `json:"attempt"`
}

func ciWaitPollRun(ctx context.Context, state *ciWaitState) (ciWaitReport, bool, error) {
	body, err := ciWaitGH(ctx, "run", "view", state.Selector.Value,
		"--repo", state.Repo, "--json", "status,conclusion,url,headSha,attempt")
	if err != nil {
		return ciWaitReport{}, false, err
	}
	var run ghRunDetail
	if err := ciWaitDecode(body, &run); err != nil {
		return ciWaitReport{}, false, err
	}
	if run.Attempt <= 0 || run.HeadSHA == "" {
		return ciWaitReport{}, false, fmt.Errorf("gh run view returned no qualified subject or attempt")
	}
	state.LastSHA = run.HeadSHA
	id, _ := strconv.ParseInt(state.Selector.Value, 10, 64)
	report := ciWaitReport{Status: "pending", SHA: run.HeadSHA, RunURL: run.URL}
	population, err := ciWaitObserveWorkflows(ctx, state, []ciWaitWorkflowRun{{
		ID: id, Attempt: run.Attempt, HeadSHA: run.HeadSHA, Name: "run " + state.Selector.Value,
		Status: run.Status, Conclusion: run.Conclusion, URL: run.URL,
	}}, nil)
	if err != nil {
		return report, false, err
	}
	if failed, terminal := ciWaitObservedFailure(state, report, population); terminal {
		return failed, true, nil
	}
	report.Checks, _ = ciWaitCountPRChecks(population)
	if report.Checks.Pending > 0 {
		report.Reason = "the workflow population is not complete yet"
		return report, false, nil
	}
	report.Status = "success"
	if run.Conclusion == "skipped" || run.Conclusion == "neutral" {
		report.Reason = "the run concluded " + run.Conclusion
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

// ghRunIDFromURL extracts the numeric run id from a check's job link. Not
// every check link carries one: GitHub Advanced Security reports CodeQL
// through a check-run link of the form /<owner>/<repo>/runs/<check-run-id>.
// The empty string means "this link names no run", which is what both callers
// test for.
func ghRunIDFromURL(link string) string {
	match := ghRunURLPattern.FindStringSubmatch(link)
	if match == nil {
		return ""
	}
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

func ciWaitFirstRunURLForPopulation(repo string, checks []ciWaitCheck) string {
	for _, check := range checks {
		if check.Bucket == "fail" {
			if runID := ghRunIDFromURL(check.URL); runID != "" && repo != "" {
				return "https://github.com/" + repo + "/actions/runs/" + runID
			}
			return check.URL
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
	path, err := ciWaitStatePath(request.StateFile)
	if err != nil {
		return nil, "", err
	}
	ciWaitReapState(filepath.Dir(path), path)
	if request.StateFile != "" {
		if state, ok := ciWaitReadState(request.StateFile); ok {
			if state.Mode == "" {
				state.Mode = "checks"
			}
			state.OwnerPID = os.Getppid()
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
	if err := ciWaitSubjectError(request.Repo, *request.Selector); err != nil {
		return nil, "", err
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
	if request.HeadSHA != "" && (request.Selector.Kind != "pr" || !ciWaitHex40(request.HeadSHA)) {
		return nil, "", fmt.Errorf("head_sha must be a 40-character commit SHA and requires a pr selector")
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
		OwnerPID:      os.Getppid(),
		// GitHub reports object ids in lowercase. A timeout before the pull
		// request reaches the expected head still names it.
		ExpectedHeadSHA: strings.ToLower(request.HeadSHA),
		LastSHA:         strings.ToLower(request.HeadSHA),
	}
	return state, path, nil
}

func ciWaitReadState(path string) (*ciWaitState, bool) {
	raw, err := os.ReadFile(path) //nolint:gosec // G304: the path is this CLI's own state location — built by ciWaitStatePath under the operator's state directory, or an absolute caller path whose unreadable content answers false below.
	if err != nil {
		return nil, false
	}
	var state ciWaitState
	if err := json.Unmarshal(raw, &state); err != nil {
		return nil, false
	}
	if state.SchemaVersion != 1 || state.DeadlineAt.IsZero() {
		return nil, false
	}
	return &state, true
}

// Reap only expired waits with a provably dead caller. Unknown legacy watch
// owners, unreadable files, symlinks, and live owners are not deletion evidence.
// The selected file is left for resume to report its original timeout.
func ciWaitReapState(dir, selected string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		name := entry.Name()
		if !entry.Type().IsRegular() || !strings.HasPrefix(name, ciWaitStateFilePrefix) || !strings.HasSuffix(name, ".json") {
			continue
		}
		path := filepath.Join(dir, name)
		if path == selected {
			continue
		}
		state, ok := ciWaitReadState(path)
		if !ok || time.Now().Before(state.DeadlineAt) {
			continue
		}
		owner := state.OwnerPID
		if owner == 0 {
			// The original verb recorded its PID in the filename only.
			owner, _ = strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(name, ciWaitStateFilePrefix), ".json"))
		}
		if owner > 0 && errors.Is(syscall.Kill(owner, 0), syscall.ESRCH) {
			ciWaitRemoveState(path)
		}
	}
}

func ciWaitSaveState(state *ciWaitState, path string) {
	if path == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil { //nolint:gosec // G703: CD-0160 D1 permits an operator-selected absolute state_file; ciWaitStatePath validates that boundary before this write. There is no sandbox-relative path here.
		return
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		return
	}
	_ = os.WriteFile(path, encoded, 0o600) //nolint:gosec // G703: path is the resolved state path or the operator-selected absolute state_file validated by ciWaitStatePath, as required by CD-0160 D1.
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

// ciWaitSubjectError checks the repository and selector a wait names. Both
// reach gh as arguments, so a value outside these shapes never runs.
func ciWaitSubjectError(repo string, selector ciWaitSelector) error {
	if !ciWaitRepoValid(repo) {
		return fmt.Errorf("repo is required as owner/name")
	}
	switch selector.Kind {
	case "pr":
		if !ciWaitDigitsOnly(selector.Value) {
			return fmt.Errorf("a pr selector value must be a pull request number")
		}
	case "sha":
		if !ciWaitHex40(selector.Value) {
			return fmt.Errorf("a sha selector value must be a 40-character commit SHA")
		}
	case "run":
		if !ciWaitDigitsOnly(selector.Value) {
			return fmt.Errorf("a run selector value must be a run id")
		}
	default:
		return fmt.Errorf("selector.kind must be one of pr, sha, or run")
	}
	return nil
}

// ciWaitRepoValid admits exactly two plain path segments. The repository
// names fixed GitHub API paths and GraphQL variables, so a segment can carry
// no separator, query, fragment, gh placeholder, or dot-only traversal.
func ciWaitRepoValid(repo string) bool {
	owner, name, ok := strings.Cut(repo, "/")
	return ok && ciWaitRepoSegment(owner) && ciWaitRepoSegment(name)
}

func ciWaitRepoSegment(segment string) bool {
	if segment == "" || segment == "." || segment == ".." {
		return false
	}
	for _, r := range segment {
		ok := (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r == '.' || r == '_' || r == '-'
		if !ok {
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
func ciWaitFinishTimeout(state *ciWaitState, out io.Writer) int {
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
	if state.HeadSHA == "" && state.ExpectedHeadSHA != "" {
		report.Reason = "the wall-time deadline expired before the pull request head reached the expected head"
	}
	return ciWaitEmit(out, report, 1)
}

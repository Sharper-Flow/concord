package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ghStubDir builds a directory of fake gh executables that answer each named
// scenario, plus a PATH override. The tests never touch the network.
func ghStubDir(t *testing.T, script string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func ghStubPath(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("XDG_STATE_HOME", t.TempDir())
}

func runCiWaitStdin(t *testing.T, body string) (int, ciWaitReport, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := runCiWait([]byte(body), &out, &errOut)
	var report ciWaitReport
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatalf("report is not JSON: %v; stdout=%q stderr=%q", err, out.String(), errOut.String())
	}
	return code, report, errOut.String()
}

func ciWaitBudget(seconds int) *int { return &seconds }

func ciWaitJSON(t *testing.T, v any) string {
	t.Helper()
	encoded, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

// --- refusal paths ---

func TestCiWaitMissingSelectorRefuses(t *testing.T) {
	ghStubPath(t, ghStubDir(t, "exit 97"))
	code, report, _ := runCiWaitStdin(t, `{"repo":"o/r"}`)
	if code != 1 || report.Status != "refused" || report.Reason == "" {
		t.Fatalf("want refused with reason, got code=%d report=%+v", code, report)
	}
}

func TestCiWaitUnknownSelectorKindRefuses(t *testing.T) {
	ghStubPath(t, ghStubDir(t, "exit 97"))
	code, report, _ := runCiWaitStdin(t, `{"selector":{"kind":"blob","value":"x"},"repo":"o/r"}`)
	if code != 1 || report.Status != "refused" {
		t.Fatalf("want refused, got code=%d report=%+v", code, report)
	}
}

func TestCiWaitPRSelectorValueMustBeNumeric(t *testing.T) {
	ghStubPath(t, ghStubDir(t, "exit 97"))
	code, report, _ := runCiWaitStdin(t, `{"selector":{"kind":"pr","value":"abc"},"repo":"o/r"}`)
	if code != 1 || report.Status != "refused" {
		t.Fatalf("want refused, got code=%d report=%+v", code, report)
	}
}

func TestCiWaitSHASelectorValueMustBeHex40(t *testing.T) {
	ghStubPath(t, ghStubDir(t, "exit 97"))
	code, report, _ := runCiWaitStdin(t, `{"selector":{"kind":"sha","value":"53718f"},"repo":"o/r"}`)
	if code != 1 || report.Status != "refused" {
		t.Fatalf("want refused, got code=%d report=%+v", code, report)
	}
}

// --- deadline enforcement ---

func TestCiWaitZeroBudgetTerminatesWithTimeout(t *testing.T) {
	// A hung gh must not prevent termination: the deadline check runs before
	// any gh invocation.
	ghStubPath(t, ghStubDir(t, "sleep 500"))
	code, report, _ := runCiWaitStdin(t, ciWaitJSON(t, ciWaitRequest{
		Selector: &ciWaitSelector{Kind: "pr", Value: "1"}, Repo: "o/r", TimeSecondsMax: ciWaitBudget(0),
	}))
	if code != 1 || report.Status != "timeout" {
		t.Fatalf("want timeout, got code=%d report=%+v", code, report)
	}
	if report.Elapsed > ciWaitDefaultBudget {
		t.Fatalf("elapsed exceeds the deadline: %d", report.Elapsed)
	}
}

func TestCiWaitHungCommandCannotPreventDeadline(t *testing.T) {
	// One slice against a gh that sleeps forever: the command context must
	// kill the whole process group, and the report must be an explicit error
	// (never success, never a hang). The command timeout is shrunk for the
	// test binary; the released bound stays 45 seconds.
	ghStubPath(t, ghStubDir(t, "sleep 500"))
	saved := ciWaitCommandTimeout
	ciWaitCommandTimeout = 2 * time.Second
	t.Cleanup(func() { ciWaitCommandTimeout = saved })
	start := time.Now()
	code, report, _ := runCiWaitStdin(t, ciWaitJSON(t, ciWaitRequest{
		Selector: &ciWaitSelector{Kind: "pr", Value: "1"}, Repo: "o/r", TimeSecondsMax: ciWaitBudget(1800),
	}))
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("a hung gh prevented termination (took %v)", elapsed)
	}
	if report.Status != "error" {
		t.Fatalf("want explicit error from hung gh, got %s", report.Status)
	}
	if code != 1 {
		t.Fatalf("error report must exit 1, got %d", code)
	}
	if !strings.Contains(report.Reason, "command timeout") {
		t.Fatalf("reason must name the command timeout: %q", report.Reason)
	}
}

func TestCiWaitDeadlineCarriedAcrossInvocations(t *testing.T) {
	ghStubPath(t, ghStubDir(t, `case "$*" in *"pr view"*) echo '{"headRefOid":"aabb","url":"u"}';; *"pr checks"*) echo '[{"name":"c","state":"PENDING","link":"l","bucket":"pending"}]';; esac`))
	_, first, _ := runCiWaitStdin(t, ciWaitJSON(t, ciWaitRequest{
		Selector: &ciWaitSelector{Kind: "pr", Value: "1"}, Repo: "o/r", TimeSecondsMax: ciWaitBudget(8),
	}))
	if first.Status != "pending" || first.StateFile == "" {
		t.Fatalf("want pending with state file, got %+v", first)
	}
	stateBytes, err := os.ReadFile(first.StateFile)
	if err != nil {
		t.Fatal(err)
	}
	var state ciWaitState
	if err := json.Unmarshal(stateBytes, &state); err != nil {
		t.Fatal(err)
	}
	if state.BudgetSeconds != 8 {
		t.Fatalf("state must carry the caller's bounded budget, got %d", state.BudgetSeconds)
	}
	// Wait past the carried deadline, then resume: the deadline comes from the
	// state file, not from a fresh budget.
	time.Sleep(8100 * time.Millisecond)
	_, second, _ := runCiWaitStdin(t, ciWaitJSON(t, ciWaitRequest{
		Selector: &ciWaitSelector{Kind: "pr", Value: "1"}, Repo: "o/r", TimeSecondsMax: ciWaitBudget(1800),
		StateFile: first.StateFile,
	}))
	if second.Status != "timeout" {
		t.Fatalf("resumed wait must honor the carried deadline, got %s", second.Status)
	}
	if _, err := os.Stat(first.StateFile); !os.IsNotExist(err) {
		t.Fatal("terminal wait must remove its state file")
	}
}

// --- selector routing ---

func TestCiWaitSelectorsQueryTheIntendedSubject(t *testing.T) {
	// The pre-repair prompt routed run ids through `gh run list --commit`,
	// which queries a SHA, not a run. Each selector must invoke the gh
	// subcommand that queries its own subject.
	sha := strings.Repeat("a", 40)
	capture := filepath.Join(t.TempDir(), "args")
	stub := `echo "$*" >> "` + capture + `"
case "$*" in
	*"pr view"*) echo '{"headRefOid":"aabb","url":"u"}';;
	*"pr checks"*) echo '[{"name":"c","state":"SUCCESS","link":"l","bucket":"pass"}]';;
	*"run list"*) echo '[{"databaseId":1,"name":"n","status":"completed","conclusion":"success","url":"u"}]';;
	*"run view"*) echo '{"status":"completed","conclusion":"success","url":"u","headSha":"s"}';;
esac`
	ghStubPath(t, ghStubDir(t, stub))
	cases := []struct {
		kind  string
		value string
		want  string
	}{
		{"pr", "5", "pr checks 5 --repo o/r"},
		{"sha", sha, "run list --repo o/r --commit " + sha},
		{"run", "42", "run view 42 --repo o/r"},
	}
	for _, tc := range cases {
		_, _, _ = runCiWaitStdin(t, ciWaitJSON(t, ciWaitRequest{
			Selector: &ciWaitSelector{Kind: tc.kind, Value: tc.value}, Repo: "o/r", TimeSecondsMax: ciWaitBudget(1800),
		}))
	}
	raw, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	for _, tc := range cases {
		found := false
		for _, line := range lines {
			if strings.Contains(line, tc.want) {
				found = true
			}
		}
		if !found {
			t.Fatalf("selector %s never queried %q; captured: %q", tc.kind, tc.want, string(raw))
		}
	}
}

// --- terminal classification ---

func prChecksFixture(total, passing, failing, skipped, pending int) string {
	checks := []map[string]string{}
	for i := 0; i < passing; i++ {
		checks = append(checks, map[string]string{"name": "pass", "state": "SUCCESS", "link": "https://github.com/o/r/actions/runs/10/job/1", "bucket": "pass"})
	}
	for i := 0; i < failing; i++ {
		checks = append(checks, map[string]string{"name": "test", "state": "FAILURE", "link": "https://github.com/o/r/actions/runs/10/job/2", "bucket": "fail"})
	}
	for i := 0; i < skipped; i++ {
		checks = append(checks, map[string]string{"name": "skip", "state": "SKIPPED", "link": "https://github.com/o/r/actions/runs/10/job/3", "bucket": "skipping"})
	}
	for i := 0; i < pending; i++ {
		checks = append(checks, map[string]string{"name": "run", "state": "IN_PROGRESS", "link": "l", "bucket": "pending"})
	}
	_ = total
	encoded, _ := json.Marshal(checks)
	return string(encoded)
}

func TestCiWaitTerminalOutcomes(t *testing.T) {
	cases := []struct {
		name       string
		checks     string
		logs       string
		wantStatus string
		wantPass   int
		wantFail   int
		wantSkip   int
		wantPend   int
	}{
		{"all pass", prChecksFixture(2, 2, 0, 0, 0), "", "success", 2, 0, 0, 0},
		{"failing present", prChecksFixture(3, 1, 1, 1, 0), "something\n--- FAIL: TestThing (0.1s)\n", "failure", 1, 1, 1, 0},
		{"all skipped is success", prChecksFixture(1, 0, 0, 1, 0), "", "success", 0, 0, 1, 0},
		{"mixed pending", prChecksFixture(3, 1, 0, 1, 1), "", "pending", 1, 0, 1, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ghStubPath(t, ghStubDir(t, `case "$*" in
				*"pr view"*) echo '{"headRefOid":"aabbccdd","url":"u"}';;
				*"pr checks"*) cat <<'EOC'
`+tc.checks+`
EOC
;;
				*"log-failed"*) cat <<'EOL'
`+tc.logs+`
EOL
;;
			esac`))
			budget := 1800
			if tc.wantStatus == "pending" {
				// A pending verdict ends the slice; keep the test short.
				budget = 8
			}
			code, report, _ := runCiWaitStdin(t, ciWaitJSON(t, ciWaitRequest{
				Selector: &ciWaitSelector{Kind: "pr", Value: "5"}, Repo: "o/r", TimeSecondsMax: ciWaitBudget(budget),
			}))
			if report.Status != tc.wantStatus {
				t.Fatalf("status=%s want %s (report=%+v)", report.Status, tc.wantStatus, report)
			}
			if report.Checks == nil {
				t.Fatal("report must carry observed counts")
			}
			if report.Checks.Passing != tc.wantPass || report.Checks.Failing != tc.wantFail || report.Checks.Skipped != tc.wantSkip || report.Checks.Pending != tc.wantPend {
				t.Fatalf("counts=%+v want pass=%d fail=%d skip=%d pend=%d", report.Checks, tc.wantPass, tc.wantFail, tc.wantSkip, tc.wantPend)
			}
			if tc.wantStatus == "success" && code != 0 {
				t.Fatalf("success must exit 0, got %d", code)
			}
			if tc.wantStatus == "failure" {
				if code != 0 {
					t.Fatalf("a failure verdict is a successful observation; want exit 0, got %d", code)
				}
				if len(report.Failures) == 0 || report.Failures[0].FirstError != "--- FAIL: TestThing (0.1s)" {
					t.Fatalf("failure detail must quote the observed error, got %+v", report.Failures)
				}
			}
		})
	}
}

func TestCiWaitEmptyCheckSetIsNeverSuccess(t *testing.T) {
	ghStubPath(t, ghStubDir(t, `case "$*" in
		*"pr view"*) echo '{"headRefOid":"aabb","url":"u"}';;
		*"pr checks"*) echo '[]';;
	esac`))
	_, report, _ := runCiWaitStdin(t, ciWaitJSON(t, ciWaitRequest{
		Selector: &ciWaitSelector{Kind: "pr", Value: "5"}, Repo: "o/r", TimeSecondsMax: ciWaitBudget(8),
	}))
	if report.Status != "pending" {
		t.Fatalf("an empty check set must stay pending, got %s", report.Status)
	}
	if report.Reason == "" {
		t.Fatal("pending with no checks must say why")
	}
}

func TestCiWaitPRMergeModeUsesGitHubMergeState(t *testing.T) {
	ghStubPath(t, ghStubDir(t, `case "$*" in
		*"pr view"*) echo '{"headRefOid":"aabb","url":"u","state":"OPEN","mergedAt":null,"mergeStateStatus":"CLEAN"}';;
		*"pr checks"*) exit 97;;
	esac`))
	_, report, _ := runCiWaitStdin(t, ciWaitJSON(t, ciWaitRequest{
		Selector: &ciWaitSelector{Kind: "pr", Value: "5"}, Repo: "o/r", Mode: "merge", TimeSecondsMax: ciWaitBudget(1800),
	}))
	if report.Status != "success" || report.MergeState != "CLEAN" {
		t.Fatalf("merge mode must succeed from CLEAN without reading checks, got %+v", report)
	}
}

func TestCiWaitPRChecksModeMergeStates(t *testing.T) {
	// CD-0161: in checks mode only DIRTY is evidence about the merge itself.
	// GitHub computes mergeability lazily and commonly answers UNKNOWN, so the
	// non-verdict states must not block a complete passing check set.
	cases := []struct {
		mergeState string
		wantStatus string
	}{
		{"UNKNOWN", "success"},
		{"BEHIND", "success"},
		{"BLOCKED", "success"},
		{"UNSTABLE", "success"},
		{"DRAFT", "success"},
		{"", "success"},
		{"CLEAN", "success"},
		{"DIRTY", "pending"},
	}
	for _, tc := range cases {
		t.Run("merge_state_"+tc.mergeState, func(t *testing.T) {
			ghStubPath(t, ghStubDir(t, `case "$*" in
				*"pr view"*) echo '{"headRefOid":"aabb","url":"u","state":"OPEN","mergedAt":null,"mergeStateStatus":"`+tc.mergeState+`"}';;
				*"pr checks"*) echo '[{"name":"c","state":"SUCCESS","link":"l","bucket":"pass"}]';;
			esac`))
			code, report, _ := runCiWaitStdin(t, ciWaitJSON(t, ciWaitRequest{
				Selector: &ciWaitSelector{Kind: "pr", Value: "5"}, Repo: "o/r", TimeSecondsMax: ciWaitBudget(8),
			}))
			if report.Status != tc.wantStatus {
				t.Fatalf("mergeState=%q: status=%s want %s (report=%+v)", tc.mergeState, report.Status, tc.wantStatus, report)
			}
			if report.MergeState != tc.mergeState {
				t.Fatalf("mergeState=%q: report must carry the observed merge state, got %q", tc.mergeState, report.MergeState)
			}
			if tc.wantStatus == "success" && code != 0 {
				t.Fatalf("mergeState=%q: success must exit 0, got %d", tc.mergeState, code)
			}
		})
	}
}

func TestCiWaitPRMergeModeStaysPendingOnUnknown(t *testing.T) {
	// Merge mode gates on GitHub's own mergeability assertion (CLEAN or
	// HAS_HOOKS). The lazy UNKNOWN means "not computed", so the wait keeps
	// polling instead of reporting success or failure.
	ghStubPath(t, ghStubDir(t, `case "$*" in
		*"pr view"*) echo '{"headRefOid":"aabb","url":"u","state":"OPEN","mergedAt":null,"mergeStateStatus":"UNKNOWN"}';;
		*"pr checks"*) exit 97;;
	esac`))
	_, report, _ := runCiWaitStdin(t, ciWaitJSON(t, ciWaitRequest{
		Selector: &ciWaitSelector{Kind: "pr", Value: "5"}, Repo: "o/r", Mode: "merge", TimeSecondsMax: ciWaitBudget(8),
	}))
	if report.Status != "pending" || report.MergeState != "UNKNOWN" {
		t.Fatalf("merge mode must keep UNKNOWN pending, got %+v", report)
	}
}

func TestCiWaitPRMergedAndClosedAreTerminal(t *testing.T) {
	for _, tc := range []struct {
		name      string
		state     string
		mergedAt  string
		wantState string
	}{
		{name: "merged", state: "CLOSED", mergedAt: "2026-09-20T00:00:00Z", wantState: "merged"},
		{name: "closed", state: "CLOSED", wantState: "closed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ghStubPath(t, ghStubDir(t, `case "$*" in
				*"pr view"*) echo '{"headRefOid":"aabb","url":"u","state":"`+tc.state+`","mergedAt":"`+tc.mergedAt+`","mergeStateStatus":"`+tc.name+`"}';;
				*"pr checks"*) exit 97;;
			esac`))
			_, report, _ := runCiWaitStdin(t, ciWaitJSON(t, ciWaitRequest{
				Selector: &ciWaitSelector{Kind: "pr", Value: "5"}, Repo: "o/r", TimeSecondsMax: ciWaitBudget(1800),
			}))
			if report.Status != tc.wantState {
				t.Fatalf("status=%s want %s, report=%+v", report.Status, tc.wantState, report)
			}
		})
	}
}

func TestCiWaitMergeModeRequiresPRSelector(t *testing.T) {
	ghStubPath(t, ghStubDir(t, "exit 97"))
	code, report, _ := runCiWaitStdin(t, ciWaitJSON(t, ciWaitRequest{
		Selector: &ciWaitSelector{Kind: "run", Value: "42"}, Repo: "o/r", Mode: "merge",
	}))
	if code != 1 || report.Status != "refused" {
		t.Fatalf("merge mode on a run selector must refuse, got code=%d report=%+v", code, report)
	}
}

func TestCiWaitHeadSHAChangeSupersedes(t *testing.T) {
	ghStubPath(t, ghStubDir(t, `case "$*" in
		*"pr view"*) echo '{"headRefOid":"newsha","url":"u"}';;
		*"pr checks"*) echo '[]';;
	esac`))
	statePath := filepath.Join(t.TempDir(), "state.json")
	state := ciWaitState{SchemaVersion: 1, Selector: ciWaitSelector{Kind: "pr", Value: "5"}, Repo: "o/r",
		BudgetSeconds: 1800, StartedAt: time.Now().UTC(), DeadlineAt: time.Now().UTC().Add(time.Hour), HeadSHA: "oldsha"}
	if err := os.WriteFile(statePath, []byte(ciWaitJSON(t, state)), 0o600); err != nil {
		t.Fatal(err)
	}
	_, report, _ := runCiWaitStdin(t, ciWaitJSON(t, ciWaitRequest{
		Selector: &ciWaitSelector{Kind: "pr", Value: "5"}, Repo: "o/r",
		StateFile: statePath,
	}))
	if report.Status != "superseded" {
		t.Fatalf("a changed head SHA must supersede the wait, got %s", report.Status)
	}
	if report.SHA != "oldsha" || report.HeadSHA != "newsha" {
		t.Fatalf("superseded report must name both SHAs, got %+v", report)
	}
}

func TestCiWaitRunSelectorConclusions(t *testing.T) {
	cases := []struct {
		conclusion string
		status     string
	}{
		{"success", "success"},
		{"skipped", "success"},
		{"failure", "failure"},
		{"cancelled", "failure"},
		{"startup_failure", "failure"},
	}
	for _, tc := range cases {
		t.Run(tc.conclusion, func(t *testing.T) {
			ghStubPath(t, ghStubDir(t, `echo '{"status":"completed","conclusion":"`+tc.conclusion+`","url":"u","headSha":"sha"}'`))
			_, report, _ := runCiWaitStdin(t, ciWaitJSON(t, ciWaitRequest{
				Selector: &ciWaitSelector{Kind: "run", Value: "42"}, Repo: "o/r", TimeSecondsMax: ciWaitBudget(1800),
			}))
			if report.Status != tc.status {
				t.Fatalf("conclusion %s: status=%s want %s", tc.conclusion, report.Status, tc.status)
			}
		})
	}
}

func TestCiWaitRunSelectorPendingStaysPending(t *testing.T) {
	ghStubPath(t, ghStubDir(t, `echo '{"status":"in_progress","conclusion":null,"url":"u","headSha":"sha"}'`))
	_, report, _ := runCiWaitStdin(t, ciWaitJSON(t, ciWaitRequest{
		Selector: &ciWaitSelector{Kind: "run", Value: "42"}, Repo: "o/r", TimeSecondsMax: ciWaitBudget(8),
	}))
	if report.Status != "pending" {
		t.Fatalf("an in-progress run must stay pending, got %s", report.Status)
	}
}

func TestCiWaitSHASelectorNoRunsIsPendingNeverSuccess(t *testing.T) {
	ghStubPath(t, ghStubDir(t, `echo '[]'`))
	_, report, _ := runCiWaitStdin(t, ciWaitJSON(t, ciWaitRequest{
		Selector: &ciWaitSelector{Kind: "sha", Value: strings.Repeat("a", 40)}, Repo: "o/r", TimeSecondsMax: ciWaitBudget(8),
	}))
	if report.Status != "pending" || report.Checks == nil || report.Checks.Total != 0 {
		t.Fatalf("no runs must be pending with an empty observed set, got %+v", report)
	}
}

func TestCiWaitSHASelectorCancelledRunIsFailure(t *testing.T) {
	runs := []ghRunSummary{{Name: "CI", Status: "completed", Conclusion: "cancelled", URL: "u"}}
	ghStubPath(t, ghStubDir(t, `echo '[{"databaseId":1,"name":"CI","status":"completed","conclusion":"cancelled","url":"u"}]'`))
	_, report, _ := runCiWaitStdin(t, ciWaitJSON(t, ciWaitRequest{
		Selector: &ciWaitSelector{Kind: "sha", Value: strings.Repeat("a", 40)}, Repo: "o/r", TimeSecondsMax: ciWaitBudget(1800),
	}))
	if report.Status != "failure" {
		t.Fatalf("a cancelled run is a failure verdict, got %s", report.Status)
	}
	if len(report.Failures) != 1 || report.Failures[0].Classification != "cancelled" {
		t.Fatalf("cancelled must classify as cancelled, got %+v", report.Failures)
	}
	_ = runs
}

// --- error paths ---

func TestCiWaitAuthFailureIsErrorNeverSuccess(t *testing.T) {
	ghStubPath(t, ghStubDir(t, `echo "gh: To get started with GitHub CLI, please run: gh auth login" >&2; exit 1`))
	code, report, _ := runCiWaitStdin(t, ciWaitJSON(t, ciWaitRequest{
		Selector: &ciWaitSelector{Kind: "pr", Value: "5"}, Repo: "o/r", TimeSecondsMax: ciWaitBudget(1800),
	}))
	if code != 1 || report.Status != "error" {
		t.Fatalf("auth failure must be an explicit error, got code=%d report=%+v", code, report)
	}
	if !strings.Contains(report.Reason, "gh auth login") {
		t.Fatalf("error must quote gh's verbatim stderr, got %q", report.Reason)
	}
}

func TestCiWaitMalformedJSONIsError(t *testing.T) {
	ghStubPath(t, ghStubDir(t, `echo 'not json'`))
	_, report, _ := runCiWaitStdin(t, ciWaitJSON(t, ciWaitRequest{
		Selector: &ciWaitSelector{Kind: "pr", Value: "5"}, Repo: "o/r", TimeSecondsMax: ciWaitBudget(1800),
	}))
	if report.Status != "error" || !strings.Contains(report.Reason, "unreadable") {
		t.Fatalf("malformed response must be an explicit error, got %+v", report)
	}
}

func TestCiWaitMissingPRIsErrorWithVerbatimDetail(t *testing.T) {
	ghStubPath(t, ghStubDir(t, `echo "GraphQL: Could not resolve to a PullRequest with the number of 999999." >&2; exit 1`))
	_, report, _ := runCiWaitStdin(t, ciWaitJSON(t, ciWaitRequest{
		Selector: &ciWaitSelector{Kind: "pr", Value: "999999"}, Repo: "o/r", TimeSecondsMax: ciWaitBudget(1800),
	}))
	if report.Status != "error" || !strings.Contains(report.Reason, "Could not resolve to a PullRequest") {
		t.Fatalf("missing PR must be an error quoting gh, got %+v", report)
	}
}

// --- first-error extraction ---

func TestCiWaitFirstErrorLinePreferences(t *testing.T) {
	cases := []struct {
		name string
		log  string
		want string
	}{
		{"go test failure wins", "job\tbuild\tok\njob\ttest\t--- FAIL: TestX (0.5s)\n", "--- FAIL: TestX (0.5s)"},
		{"error marker", "job\tstep\tError: something broke\n", "Error: something broke"},
		{"first non-empty fallback", "job\tstep\tplain output\n", "plain output"},
		{"empty log", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ciWaitFirstErrorLine(tc.log); got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}

func TestCiWaitFirstErrorLineTruncates(t *testing.T) {
	line := strings.Repeat("x", 500)
	got := ciWaitFirstErrorLine(line)
	if len(got) > 300 {
		t.Fatalf("first error line must stay bounded, got %d", len(got))
	}
}

// --- classification ---

func TestCiWaitClassifyName(t *testing.T) {
	cases := []struct{ name, want string }{
		{"E2E Journeys", "test_failure"},
		{"Migration lifecycle tests", "build_failure"},
		{"security / Gitleaks secret scan", "lint_or_validator"},
		{"Lane bicep (infra validation)", "infrastructure"},
		{"Detect CI-relevant changes", "unknown"},
		{"Something else", "unknown"},
	}
	for _, tc := range cases {
		if got := ciWaitClassifyName(tc.name); got != tc.want {
			t.Fatalf("%s: got %s want %s", tc.name, got, tc.want)
		}
	}
}

// --- iteration counting ---

func TestCiWaitIterationsCounted(t *testing.T) {
	ghStubPath(t, ghStubDir(t, `case "$*" in
		*"pr view"*) echo '{"headRefOid":"aabb","url":"u"}';;
		*"pr checks"*) echo '[{"name":"c","state":"IN_PROGRESS","link":"l","bucket":"pending"}]';;
	esac`))
	_, report, _ := runCiWaitStdin(t, ciWaitJSON(t, ciWaitRequest{
		Selector: &ciWaitSelector{Kind: "pr", Value: "5"}, Repo: "o/r", TimeSecondsMax: ciWaitBudget(8),
	}))
	if report.Status != "pending" {
		t.Fatalf("want pending, got %s", report.Status)
	}
	if report.Iterations < 1 {
		t.Fatalf("iterations must be counted by the CLI, got %d", report.Iterations)
	}
	if report.StateFile == "" {
		t.Fatal("pending must carry the state file for resumption")
	}
	os.Remove(report.StateFile)
}

func TestCiWaitSliceBoundedByShutdownSlack(t *testing.T) {
	// A caller whose remaining budget is under the slice must terminate
	// within the shutdown tolerance, not overshoot by a whole slice.
	ghStubPath(t, ghStubDir(t, `case "$*" in
		*"pr view"*) echo '{"headRefOid":"aabb","url":"u"}';;
		*"pr checks"*) echo '[{"name":"c","state":"IN_PROGRESS","link":"l","bucket":"pending"}]';;
	esac`))
	start := time.Now()
	code, report, _ := runCiWaitStdin(t, ciWaitJSON(t, ciWaitRequest{
		Selector: &ciWaitSelector{Kind: "pr", Value: "5"}, Repo: "o/r", TimeSecondsMax: ciWaitBudget(4),
	}))
	elapsed := time.Since(start)
	if report.Status != "timeout" && report.Status != "pending" {
		t.Fatalf("unexpected status %s", report.Status)
	}
	if elapsed > 10*time.Second {
		t.Fatalf("slice overshot the shutdown tolerance: %v", elapsed)
	}
	_ = code
	_ = context.Background
}

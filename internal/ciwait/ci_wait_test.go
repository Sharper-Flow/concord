package ciwait

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
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

// ciWaitQuietReads answers the merge-queue read with no queue event and the
// required-context reads with no required context. A PR stub that is not
// about those reads appends it before its esac and keeps its own verdict.
const ciWaitQuietReads = `
	*"api graphql"*) echo '{"data":{"repository":{"pullRequest":{"state":"OPEN","headRefOid":"","timelineItems":{"nodes":[]}}}}}';;
	*"rules/branches/"*) echo '[[]]';;
	*"api repos/"*"/branches/"*) echo '{"protection":{"required_status_checks":{"contexts":[],"checks":[]}}}';;
	*"api repos/"*"/actions/runs?"*) echo '[{"total_count":0,"workflow_runs":[]}]';;
	*"/attempts/"*"/jobs?"*) echo '[{"total_count":0,"jobs":[]}]';;
`

func ghStubPath(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("XDG_STATE_HOME", t.TempDir())
}

func runCiWaitStdin(t *testing.T, body string) (int, ciWaitReport) {
	t.Helper()
	var out bytes.Buffer
	code := Run(ciWaitRequestFixture(t, body), &out)
	var report ciWaitReport
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatalf("report is not JSON: %v; stdout=%q", err, out.String())
	}
	return code, report
}

func ciWaitRequestFixture(t *testing.T, body string) Request {
	t.Helper()
	var request Request
	if err := json.Unmarshal([]byte(body), &request); err != nil {
		t.Fatalf("request fixture is not JSON: %v", err)
	}
	return request
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
	code, report := runCiWaitStdin(t, `{"repo":"o/r"}`)
	if code != 1 || report.Status != "refused" || report.Reason == "" {
		t.Fatalf("want refused with reason, got code=%d report=%+v", code, report)
	}
}

func TestCiWaitUnknownSelectorKindRefuses(t *testing.T) {
	ghStubPath(t, ghStubDir(t, "exit 97"))
	code, report := runCiWaitStdin(t, `{"selector":{"kind":"blob","value":"x"},"repo":"o/r"}`)
	if code != 1 || report.Status != "refused" {
		t.Fatalf("want refused, got code=%d report=%+v", code, report)
	}
}

func TestCiWaitPRSelectorValueMustBeNumeric(t *testing.T) {
	ghStubPath(t, ghStubDir(t, "exit 97"))
	code, report := runCiWaitStdin(t, `{"selector":{"kind":"pr","value":"abc"},"repo":"o/r"}`)
	if code != 1 || report.Status != "refused" {
		t.Fatalf("want refused, got code=%d report=%+v", code, report)
	}
}

func TestCiWaitSHASelectorValueMustBeHex40(t *testing.T) {
	ghStubPath(t, ghStubDir(t, "exit 97"))
	code, report := runCiWaitStdin(t, `{"selector":{"kind":"sha","value":"53718f"},"repo":"o/r"}`)
	if code != 1 || report.Status != "refused" {
		t.Fatalf("want refused, got code=%d report=%+v", code, report)
	}
}

func TestCiWaitRepoMustBeOwnerName(t *testing.T) {
	// The repository names fixed GitHub API paths and GraphQL variables, so
	// it must be exactly two plain path segments.
	ghStubPath(t, ghStubDir(t, "exit 97"))
	for _, repo := range []string{"", "o", "o/r/x", "o/", "/r", "o/..", "../r", "o/r?x", "o/r#x", "o/{repo}", "github.com/o/r"} {
		code, report := runCiWaitStdin(t, ciWaitJSON(t, Request{
			Selector: &Selector{Kind: "run", Value: "1"}, Repo: repo,
		}))
		if code != 1 || report.Status != "refused" {
			t.Fatalf("repo %q: want refused, got code=%d report=%+v", repo, code, report)
		}
	}
	for _, repo := range []string{"o/r", "Sharper-Flow/concord", "org_x/.github", "o/r.go"} {
		_, report := runCiWaitStdin(t, ciWaitJSON(t, Request{
			Selector: &Selector{Kind: "run", Value: "1"}, Repo: repo, TimeSecondsMax: ciWaitBudget(0),
		}))
		if report.Status != "timeout" {
			t.Fatalf("repo %q: want an admitted wait, got %+v", repo, report)
		}
	}
}

func TestCiWaitResumedStateMustNameAValidSubject(t *testing.T) {
	// A resumed state file skips the request checks, so the poll repeats the
	// repo and selector checks before any gh call.
	ghStubPath(t, ghStubDir(t, "exit 97"))
	for _, tc := range []struct{ repo, kind, value string }{
		{"o/r/../x", "pr", "5"},
		{"o/r", "pr", "@state.json"},
		{"o/r", "run", "--web"},
		{"o/r", "sha", "HEAD"},
	} {
		file := filepath.Join(t.TempDir(), "state.json")
		ciWaitSaveState(&ciWaitState{
			SchemaVersion: 1, Selector: Selector{Kind: tc.kind, Value: tc.value}, Repo: tc.repo, Mode: "checks",
			BudgetSeconds: 1800, StartedAt: time.Now().UTC(), DeadlineAt: time.Now().UTC().Add(time.Hour),
		}, file)
		code, report := runCiWaitStdin(t, ciWaitJSON(t, Request{StateFile: file}))
		if code != 1 || report.Status != "error" || strings.Contains(report.Reason, "gh ") {
			t.Fatalf("state %+v: want an error before any gh call, got code=%d report=%+v", tc, code, report)
		}
	}
}

// --- deadline enforcement ---

func TestCiWaitZeroBudgetTerminatesWithTimeout(t *testing.T) {
	// A hung gh must not prevent termination: the deadline check runs before
	// any gh invocation.
	ghStubPath(t, ghStubDir(t, "sleep 500"))
	code, report := runCiWaitStdin(t, ciWaitJSON(t, Request{
		Selector: &Selector{Kind: "pr", Value: "1"}, Repo: "o/r", TimeSecondsMax: ciWaitBudget(0),
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
	// kill the whole process group, retry once, and report a stalled pending
	// result (never success, never a hang). The command timeout is shrunk for the
	// test binary; the released bound stays 45 seconds.
	ghStubPath(t, ghStubDir(t, "sleep 500"))
	saved := ciWaitCommandTimeout
	ciWaitCommandTimeout = 50 * time.Millisecond
	t.Cleanup(func() { ciWaitCommandTimeout = saved })
	start := time.Now()
	code, report := runCiWaitStdin(t, ciWaitJSON(t, Request{
		Selector: &Selector{Kind: "pr", Value: "1"}, Repo: "o/r", TimeSecondsMax: ciWaitBudget(1800),
	}))
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("a hung gh prevented termination (took %v)", elapsed)
	}
	if report.Status != "pending" {
		t.Fatalf("want pending from a stalled gh, got %s", report.Status)
	}
	if code != 0 {
		t.Fatalf("pending report must exit 0, got %d", code)
	}
	if !strings.Contains(report.Reason, "stalled") {
		t.Fatalf("reason must name the stall: %q", report.Reason)
	}
}

func TestCiWaitDeadlineCarriedAcrossInvocations(t *testing.T) {
	ghStubPath(t, ghStubDir(t, `case "$*" in *"pr view"*) echo '{"baseRefName":"main","headRefOid":"aabb","url":"u","statusCheckRollup":[{"name":"c","status":"IN_PROGRESS"}] }';; *"pr checks"*) echo '[{"name":"c","state":"PENDING","link":"l","bucket":"pending"}]';; `+ciWaitQuietReads+`esac`))
	_, first := runCiWaitStdin(t, ciWaitJSON(t, Request{
		Selector: &Selector{Kind: "pr", Value: "1"}, Repo: "o/r", TimeSecondsMax: ciWaitBudget(8),
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
	_, second := runCiWaitStdin(t, ciWaitJSON(t, Request{
		Selector: &Selector{Kind: "pr", Value: "1"}, Repo: "o/r", TimeSecondsMax: ciWaitBudget(1800),
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
	*"pr view"*) echo '{"baseRefName":"main","headRefOid":"aabb","url":"u","statusCheckRollup":[{"name":"c","status":"COMPLETED","conclusion":"SUCCESS"}] }';;
	*"pr checks"*) echo '[{"name":"c","state":"SUCCESS","link":"l","bucket":"pass"}]';;
	"api repos/o/r/actions/runs?head_sha=` + sha + `&per_page=100 --method GET --paginate --slurp") echo '` + ciWaitSelectorRunsFixture(t, []ghRunSummary{{DatabaseID: 1, Name: "n", Status: "completed", Conclusion: "success", URL: "https://github.com/o/r/actions/runs/1"}}) + `';;
	*"run view"*) echo '{"status":"completed","conclusion":"success","url":"https://github.com/o/r/actions/runs/42","headSha":"` + sha + `","attempt":2}';;
` + ciWaitQuietReads + `esac`
	ghStubPath(t, ghStubDir(t, stub))
	cases := []struct {
		kind  string
		value string
		want  string
	}{
		{"pr", "5", "pr view 5 --repo o/r"},
		{"sha", sha, "api repos/o/r/actions/runs?head_sha=" + sha + "&per_page=100 --method GET --paginate --slurp"},
		{"run", "42", "run view 42 --repo o/r"},
		{"run", "42", "api repos/o/r/actions/runs/42/attempts/2/jobs?per_page=100 --method GET --paginate --slurp"},
	}
	for _, tc := range cases {
		code, report := runCiWaitStdin(t, ciWaitJSON(t, Request{
			Selector: &Selector{Kind: tc.kind, Value: tc.value}, Repo: "o/r", TimeSecondsMax: ciWaitBudget(8),
		}))
		if code != 0 || report.Status != "success" {
			t.Fatalf("selector %s must observe its passing subject, got code=%d report=%+v", tc.kind, code, report)
		}
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
		checks = append(checks, map[string]string{"name": fmt.Sprintf("pass-%d", i), "state": "SUCCESS", "link": "https://github.com/o/r/actions/runs/10/job/1", "bucket": "pass"})
	}
	for i := 0; i < failing; i++ {
		checks = append(checks, map[string]string{"name": fmt.Sprintf("test-%d", i), "state": "FAILURE", "link": "https://github.com/o/r/actions/runs/10/job/2", "bucket": "fail"})
	}
	for i := 0; i < skipped; i++ {
		checks = append(checks, map[string]string{"name": fmt.Sprintf("skip-%d", i), "state": "SKIPPED", "link": "https://github.com/o/r/actions/runs/10/job/3", "bucket": "skipping"})
	}
	for i := 0; i < pending; i++ {
		checks = append(checks, map[string]string{"name": fmt.Sprintf("run-%d", i), "state": "IN_PROGRESS", "link": "l", "bucket": "pending"})
	}
	_ = total
	encoded, _ := json.Marshal(checks)
	return string(encoded)
}

func prRollupFixture(checks string) string {
	var snapshots []ghPRCheck
	if err := json.Unmarshal([]byte(checks), &snapshots); err != nil {
		panic(err)
	}
	rollup := make([]ghPRRollup, 0, len(snapshots))
	for _, check := range snapshots {
		entry := ghPRRollup{Name: check.Name, DetailsURL: check.Link, Status: "COMPLETED"}
		switch check.Bucket {
		case "pass":
			entry.Conclusion = "SUCCESS"
		case "fail":
			entry.Conclusion = "FAILURE"
		case "skipping":
			entry.Conclusion = "SKIPPED"
		default:
			entry.Status = "IN_PROGRESS"
		}
		rollup = append(rollup, entry)
	}
	encoded, _ := json.Marshal(rollup)
	return string(encoded)
}

func TestCiWaitChecksModePollsOnceBelowThePageCap(t *testing.T) {
	capture := filepath.Join(t.TempDir(), "args")
	ghStubPath(t, ghStubDir(t, `echo "$*" >> "`+capture+`"
case "$*" in
	*"pr view"*) echo '{"baseRefName":"main","headRefOid":"aabb","url":"u","statusCheckRollup":[{"name":"c","status":"COMPLETED","conclusion":"SUCCESS"}] }';;
	*"pr checks"*) exit 97;;
`+ciWaitQuietReads+`esac`))
	code, report := runCiWaitStdin(t, ciWaitJSON(t, Request{
		Selector: &Selector{Kind: "pr", Value: "5"}, Repo: "o/r", TimeSecondsMax: ciWaitBudget(8),
	}))
	if code != 0 || report.Status != "success" {
		t.Fatalf("a short rollup must decide without pr checks, got code=%d report=%+v", code, report)
	}
	raw, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "pr checks") {
		t.Fatalf("a rollup below the page cap must not query pr checks, calls=%q", raw)
	}
}

func TestCiWaitFullRollupPageStillReadsThePaginatedSource(t *testing.T) {
	capture := filepath.Join(t.TempDir(), "args")
	rollup := make([]ghPRRollup, ciWaitPRRollupPageSize)
	for i := range rollup {
		rollup[i] = ghPRRollup{Name: fmt.Sprintf("check-%d", i), Status: "COMPLETED", Conclusion: "SUCCESS"}
	}
	encoded, err := json.Marshal(rollup)
	if err != nil {
		t.Fatal(err)
	}
	ghStubPath(t, ghStubDir(t, `echo "$*" >> "`+capture+`"
case "$*" in
	*"pr view"*) echo '{"baseRefName":"main","headRefOid":"aabb","url":"u","statusCheckRollup":`+string(encoded)+`}';;
	*"pr checks"*) echo '[]';;
`+ciWaitQuietReads+`esac`))
	_, report := runCiWaitStdin(t, ciWaitJSON(t, Request{
		Selector: &Selector{Kind: "pr", Value: "5"}, Repo: "o/r", TimeSecondsMax: ciWaitBudget(8),
	}))
	if report.Status != "success" || report.Checks == nil || report.Checks.Total != ciWaitPRRollupPageSize {
		t.Fatalf("a full rollup page must preserve the complete population, got %+v", report)
	}
	raw, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "pr checks") {
		t.Fatalf("a full rollup page must query the paginated source, calls=%q", raw)
	}
}

func TestCiWaitPaginatedCancelledCheckIsFailure(t *testing.T) {
	rollup := make([]ghPRRollup, ciWaitPRRollupPageSize)
	for i := range rollup {
		rollup[i] = ghPRRollup{Name: fmt.Sprintf("check-%d", i), Status: "COMPLETED", Conclusion: "SUCCESS"}
	}
	encoded, err := json.Marshal(rollup)
	if err != nil {
		t.Fatal(err)
	}
	ghStubPath(t, ghStubDir(t, `case "$*" in
	*"pr view"*) echo '{"baseRefName":"main","headRefOid":"aabb","url":"u","statusCheckRollup":`+string(encoded)+`}';;
	*"pr checks"*) echo '[{"name":"external-scan","state":"CANCELLED","link":"https://ci.example/1","bucket":"cancel"}]';;
`+ciWaitQuietReads+`esac`))
	_, report := runCiWaitStdin(t, ciWaitJSON(t, Request{
		Selector: &Selector{Kind: "pr", Value: "5"}, Repo: "o/r", TimeSecondsMax: ciWaitBudget(8),
	}))
	if report.Status != "failure" || len(report.Failures) != 1 || report.Failures[0].Classification != "cancelled" {
		t.Fatalf("a cancelled check beyond the rollup page is a failure, got %+v", report)
	}
}

func TestCiWaitPollIntervalStagesOnElapsedWaitTime(t *testing.T) {
	for _, tc := range []struct {
		elapsed time.Duration
		want    time.Duration
	}{
		{0, 15 * time.Second},
		{59 * time.Second, 15 * time.Second},
		{60 * time.Second, 30 * time.Second},
		{299 * time.Second, 30 * time.Second},
		{300 * time.Second, 60 * time.Second},
	} {
		if got := ciWaitPollInterval(tc.elapsed); got != tc.want {
			t.Fatalf("elapsed=%s: interval=%s want %s", tc.elapsed, got, tc.want)
		}
	}
}

func TestCiWaitTimedOutCommandRetriesOnceThenReportsPending(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "first-attempt")
	ghStubPath(t, ghStubDir(t, `case "$*" in
	*"pr view"*)
		if [ ! -f "`+marker+`" ]; then touch "`+marker+`"; sleep 500; fi
		echo '{"baseRefName":"main","headRefOid":"aabb","url":"u","statusCheckRollup":[{"name":"c","status":"COMPLETED","conclusion":"SUCCESS"}] }';;
	`+ciWaitQuietReads+`esac`))
	saved := ciWaitCommandTimeout
	ciWaitCommandTimeout = 50 * time.Millisecond
	t.Cleanup(func() { ciWaitCommandTimeout = saved })
	code, report := runCiWaitStdin(t, ciWaitJSON(t, Request{
		Selector: &Selector{Kind: "pr", Value: "5"}, Repo: "o/r", TimeSecondsMax: ciWaitBudget(8),
	}))
	if code != 0 || report.Status != "success" {
		t.Fatalf("a timed-out command must retry before classifying the poll, got code=%d report=%+v", code, report)
	}
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
				*"pr view"*) echo '{"baseRefName":"main","headRefOid":"aabbccdd","url":"u","statusCheckRollup":`+prRollupFixture(tc.checks)+`}' ;;
				*"pr checks"*) cat <<'EOC'
`+tc.checks+`
EOC
;;
				*"log-failed"*) cat <<'EOL'
`+tc.logs+`
EOL
;;
			`+ciWaitQuietReads+`esac`))
			budget := 1800
			if tc.wantStatus == "pending" {
				// A pending verdict ends the slice; keep the test short.
				budget = 8
			}
			code, report := runCiWaitStdin(t, ciWaitJSON(t, Request{
				Selector: &Selector{Kind: "pr", Value: "5"}, Repo: "o/r", TimeSecondsMax: ciWaitBudget(budget),
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
				if len(report.Failures) == 0 || report.Failures[0].Name != "test-0" {
					t.Fatalf("failure detail must name the observed check, got %+v", report.Failures)
				}
			}
		})
	}
}

func TestCiWaitEmptyCheckSetIsNeverSuccess(t *testing.T) {
	ghStubPath(t, ghStubDir(t, `case "$*" in
		*"pr view"*) echo '{"baseRefName":"main","headRefOid":"aabb","url":"u","statusCheckRollup":[]}';;
		*"pr checks"*) echo '[]';;
	`+ciWaitQuietReads+`esac`))
	_, report := runCiWaitStdin(t, ciWaitJSON(t, Request{
		Selector: &Selector{Kind: "pr", Value: "5"}, Repo: "o/r", TimeSecondsMax: ciWaitBudget(8),
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
		*"pr view"*) echo '{"baseRefName":"main","headRefOid":"aabb","url":"u","state":"OPEN","mergedAt":null,"mergeStateStatus":"CLEAN"}';;
		*"pr checks"*) exit 97;;
	`+ciWaitQuietReads+`esac`))
	_, report := runCiWaitStdin(t, ciWaitJSON(t, Request{
		Selector: &Selector{Kind: "pr", Value: "5"}, Repo: "o/r", Mode: "merge", TimeSecondsMax: ciWaitBudget(8),
	}))
	if report.Status != "pending" || report.MergeState != "CLEAN" || !strings.Contains(report.Reason, "can merge") {
		t.Fatalf("merge mode must keep CLEAN pending until merged, got %+v", report)
	}
}

func TestCiWaitPRPartialPopulationNeverSucceeds(t *testing.T) {
	ghStubPath(t, ghStubDir(t, `case "$*" in
		*"pr view"*) cat <<'EOC'
{"baseRefName":"main","headRefOid":"aabb","url":"u","state":"OPEN","mergedAt":null,"mergeStateStatus":"CLEAN","statusCheckRollup":[
  {"__typename":"CheckRun","name":"verify-history","status":"COMPLETED","conclusion":"SUCCESS","detailsUrl":"history"},
  {"__typename":"CheckRun","name":"title","status":"COMPLETED","conclusion":"SUCCESS","detailsUrl":"title"},
  {"__typename":"CheckRun","name":"verify-go","status":"COMPLETED","conclusion":"SUCCESS","detailsUrl":"go"},
  {"__typename":"CheckRun","name":"verify-tests","status":"COMPLETED","conclusion":"SUCCESS","detailsUrl":"tests"},
  {"__typename":"CheckRun","name":"verify-race","status":"IN_PROGRESS","conclusion":null,"detailsUrl":"race"},
  {"__typename":"CheckRun","name":"verify-acceptance","status":"QUEUED","conclusion":null,"detailsUrl":"acceptance"}
]}
EOC
;;
		*"pr checks"*) echo '[{"name":"verify-history","state":"SUCCESS","link":"history","bucket":"pass"},{"name":"title","state":"SUCCESS","link":"title","bucket":"pass"},{"name":"verify-go","state":"SUCCESS","link":"go","bucket":"pass"},{"name":"verify-tests","state":"SUCCESS","link":"tests","bucket":"pass"}]';;
	`+ciWaitQuietReads+`esac`))
	_, report := runCiWaitStdin(t, ciWaitJSON(t, Request{
		Selector: &Selector{Kind: "pr", Value: "5"}, Repo: "o/r", TimeSecondsMax: ciWaitBudget(8),
	}))
	if report.Status != "pending" {
		t.Fatalf("a partial checks snapshot must stay pending, got %+v", report)
	}
	if report.Checks == nil || report.Checks.Total != 6 || report.Checks.Pending != 2 {
		t.Fatalf("report must count the union population, got %+v", report.Checks)
	}
	seen := make(map[string]string, len(report.Checks.Entries))
	for _, check := range report.Checks.Entries {
		seen[check.Name] = check.Bucket
	}
	for name, wantBucket := range map[string]string{
		"verify-history": "pass", "title": "pass", "verify-go": "pass", "verify-tests": "pass",
		"verify-race": "pending", "verify-acceptance": "pending",
	} {
		if got := seen[name]; got != wantBucket {
			t.Fatalf("report entry %q bucket=%q want %q; entries=%+v", name, got, wantBucket, report.Checks.Entries)
		}
	}
}

func TestCiWaitPRReportNamesRollupOnlyChecks(t *testing.T) {
	ghStubPath(t, ghStubDir(t, `case "$*" in
		*"pr view"*) echo '{"baseRefName":"main","headRefOid":"aabb","url":"u","state":"OPEN","mergedAt":null,"mergeStateStatus":"CLEAN","statusCheckRollup":[{"name":"rollup-only","status":"COMPLETED","conclusion":"SUCCESS","detailsUrl":"rollup"}]}' ;;
		*"pr checks"*) echo '[]';;
	`+ciWaitQuietReads+`esac`))
	_, report := runCiWaitStdin(t, ciWaitJSON(t, Request{
		Selector: &Selector{Kind: "pr", Value: "5"}, Repo: "o/r", TimeSecondsMax: ciWaitBudget(8),
	}))
	if report.Status != "success" || report.Checks == nil || len(report.Checks.Entries) != 1 {
		t.Fatalf("a terminal rollup-only check must be reported, got %+v", report)
	}
	entry := report.Checks.Entries[0]
	if entry.Name != "rollup-only" || entry.Bucket != "pass" {
		t.Fatalf("report must name the rollup-only check and bucket, got %+v", entry)
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
				*"pr view"*) echo '{"baseRefName":"main","headRefOid":"aabb","url":"u","state":"OPEN","mergedAt":null,"mergeStateStatus":"`+tc.mergeState+`","statusCheckRollup":[{"name":"c","status":"COMPLETED","conclusion":"SUCCESS"}]}';;
				*"pr checks"*) echo '[{"name":"c","state":"SUCCESS","link":"l","bucket":"pass"}]';;
			`+ciWaitQuietReads+`esac`))
			code, report := runCiWaitStdin(t, ciWaitJSON(t, Request{
				Selector: &Selector{Kind: "pr", Value: "5"}, Repo: "o/r", TimeSecondsMax: ciWaitBudget(8),
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
		*"pr view"*) echo '{"baseRefName":"main","headRefOid":"aabb","url":"u","state":"OPEN","mergedAt":null,"mergeStateStatus":"UNKNOWN"}';;
		*"pr checks"*) exit 97;;
	`+ciWaitQuietReads+`esac`))
	_, report := runCiWaitStdin(t, ciWaitJSON(t, Request{
		Selector: &Selector{Kind: "pr", Value: "5"}, Repo: "o/r", Mode: "merge", TimeSecondsMax: ciWaitBudget(8),
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
				*"pr view"*) echo '{"baseRefName":"main","headRefOid":"aabb","url":"u","state":"`+tc.state+`","mergedAt":"`+tc.mergedAt+`","mergeStateStatus":"`+tc.name+`"}';;
				*"pr checks"*) exit 97;;
			esac`))
			_, report := runCiWaitStdin(t, ciWaitJSON(t, Request{
				Selector: &Selector{Kind: "pr", Value: "5"}, Repo: "o/r", Mode: "merge", TimeSecondsMax: ciWaitBudget(8),
			}))
			if report.Status != tc.wantState {
				t.Fatalf("status=%s want %s, report=%+v", report.Status, tc.wantState, report)
			}
		})
	}
}

func TestCiWaitMergeModeRequiresPRSelector(t *testing.T) {
	ghStubPath(t, ghStubDir(t, "exit 97"))
	code, report := runCiWaitStdin(t, ciWaitJSON(t, Request{
		Selector: &Selector{Kind: "run", Value: "42"}, Repo: "o/r", Mode: "merge",
	}))
	if code != 1 || report.Status != "refused" {
		t.Fatalf("merge mode on a run selector must refuse, got code=%d report=%+v", code, report)
	}
}

func TestCiWaitPRMergeModeCleanCanMergeIsNotSuccess(t *testing.T) {
	ghStubPath(t, ghStubDir(t, `case "$*" in
		*"pr view"*) echo '{"baseRefName":"main","headRefOid":"aabb","url":"u","state":"OPEN","mergedAt":"","mergeStateStatus":"CLEAN"}';;
		*"pr checks"*) exit 97;;
	`+ciWaitQuietReads+`esac`))
	code, report := runCiWaitStdin(t, ciWaitJSON(t, Request{
		Selector: &Selector{Kind: "pr", Value: "5"}, Repo: "o/r", Mode: "merge", TimeSecondsMax: ciWaitBudget(8),
	}))
	if report.Status != "pending" {
		t.Fatalf("a CLEAN can-merge head must stay pending for a merge-mode wait, got code=%d report=%+v", code, report)
	}
	if !strings.Contains(report.Reason, "can merge") {
		t.Fatalf("reason must name the can-merge state, got %q", report.Reason)
	}
}

// --- merge mode: observed check failures ---

// ciWaitRollupPass returns a completed passing rollup entry.
func ciWaitRollupPass(name string) ghPRRollup {
	return ghPRRollup{TypeName: "CheckRun", Name: name, Status: "COMPLETED", Conclusion: "SUCCESS", DetailsURL: "https://github.com/o/r/actions/runs/10/job/1"}
}

// ciWaitRollupFail returns a completed failing rollup entry whose job link
// points into the named check's run.
func ciWaitRollupFail(name, jobURL string) ghPRRollup {
	return ghPRRollup{TypeName: "CheckRun", Name: name, Status: "COMPLETED", Conclusion: "FAILURE", DetailsURL: jobURL}
}

// ciWaitRollupPending returns a rollup entry that is still running.
func ciWaitRollupPending(name string) ghPRRollup {
	return ghPRRollup{TypeName: "CheckRun", Name: name, Status: "IN_PROGRESS", DetailsURL: "https://github.com/o/r/actions/runs/10/job/3"}
}

// ciWaitRollupCancelled returns a completed rollup entry whose run was
// cancelled before it concluded.
func ciWaitRollupCancelled(name string) ghPRRollup {
	return ghPRRollup{TypeName: "CheckRun", Name: name, Status: "COMPLETED", Conclusion: "CANCELLED", DetailsURL: "https://github.com/o/r/actions/runs/10/job/4"}
}

// ciWaitRollupLegacyStatus returns a legacy commit status whose terminal
// state is FAILURE. A StatusContext carries its check name in Context, not
// Name, so the population must recover the name through that fallback.
func ciWaitRollupLegacyStatus(name string) ghPRRollup {
	return ghPRRollup{TypeName: "StatusContext", Context: name, State: "FAILURE", TargetURL: "https://github.com/o/r/status/1"}
}

// ciWaitMergeHeadFixture encodes the `gh pr view` body the stub serves from
// typed rollup entries.
func ciWaitMergeHeadFixture(t *testing.T, mergeState, prState, mergedAt, headRefOid string, rollup []ghPRRollup) string {
	t.Helper()
	encoded, err := json.Marshal(rollup)
	if err != nil {
		t.Fatalf("cannot encode the rollup fixture: %v", err)
	}
	return ciWaitMergeHeadBody(t, mergeState, prState, mergedAt, headRefOid, string(encoded))
}

// ciWaitMergeHeadBody wraps a statusCheckRollup JSON array in the `gh pr
// view` head envelope the stub serves.
func ciWaitMergeHeadBody(t *testing.T, mergeState, prState, mergedAt, headRefOid, rollup string) string {
	t.Helper()
	return fmt.Sprintf(`{"baseRefName":"main","headRefOid":%q,"url":%q,"state":%q,"mergedAt":%q,"mergeStateStatus":%q,"statusCheckRollup":%s}`,
		headRefOid, "https://github.com/o/r/pull/5", prState, mergedAt, mergeState, rollup)
}

// Distinct attempt instants for the newest-attempt fixtures.
const (
	ciWaitAttemptOld   = "2026-10-15T10:00:00Z"
	ciWaitAttemptNewer = "2026-10-15T11:00:00Z"
)

// ciWaitRawCheckRun renders one CheckRun rollup entry in the exact wire shape
// the gh export emits: no event key, so check identity is (name,
// workflowName), and an empty conclusion renders the null conclusion and
// completedAt of a running attempt. The check name is fixed to verify-tests;
// callers vary only workflow, state, and timing.
func ciWaitRawCheckRun(workflow, status, conclusion, startedAt, detailsURL string) string {
	entry := map[string]any{
		"__typename":   "CheckRun",
		"name":         "verify-tests",
		"workflowName": workflow,
		"status":       status,
		"startedAt":    startedAt,
		"detailsUrl":   detailsURL,
	}
	if conclusion == "" {
		entry["conclusion"] = nil
		entry["completedAt"] = nil
	} else {
		entry["conclusion"] = conclusion
		entry["completedAt"] = startedAt
	}
	encoded, _ := json.Marshal(entry)
	return string(encoded)
}

// ciWaitRawStatusContext renders one StatusContext rollup entry. The gh
// export renames the StatusContext's createdAt to the same startedAt key a
// CheckRun uses, and its identity is the context string alone.
func ciWaitRawStatusContext(context, state, startedAt string) string {
	encoded, _ := json.Marshal(map[string]any{
		"__typename": "StatusContext",
		"context":    context,
		"state":      state,
		"targetUrl":  "https://github.com/o/r/status/1",
		"startedAt":  startedAt,
	})
	return string(encoded)
}

// ciWaitRawRollup joins raw rollup entries into the JSON array body the head
// envelope expects.
func ciWaitRawRollup(entries ...string) string {
	return "[" + strings.Join(entries, ",") + "]"
}

// ciWaitPollPROnce runs one direct ciWaitPollPR poll against a stubbed gh and
// returns the report, its terminal verdict, and every captured gh invocation.
// The stub answers `pr view` with headJSON, refuses `pr checks` so a merge
// poll that builds a second population query fails loudly, and serves one
// fixed failed-log line for `run view --log-failed`.
func ciWaitPollPROnce(t *testing.T, mode, headJSON, priorHeadSHA string) (ciWaitReport, bool, string) {
	t.Helper()
	capture := filepath.Join(t.TempDir(), "args")
	stub := `echo "$*" >> "` + capture + `"
case "$*" in
	*"pr view"*) cat <<'EOC'
` + headJSON + `
EOC
;;
	*"pr checks"*) exit 97;;
	*"run view"*) printf 'job\tstep\t--- FAIL: TestThing (0.1s)\n';;
` + ciWaitQuietReads + `esac`
	ghStubPath(t, ghStubDir(t, stub))
	state := &ciWaitState{
		SchemaVersion: 1,
		Selector:      Selector{Kind: "pr", Value: "5"},
		Repo:          "o/r",
		BudgetSeconds: ciWaitDefaultBudget,
		StartedAt:     time.Now().UTC(),
		DeadlineAt:    time.Now().UTC().Add(time.Hour),
		Mode:          mode,
		HeadSHA:       priorHeadSHA,
	}
	report, terminal, err := ciWaitPollPR(context.Background(), state)
	if err != nil {
		t.Fatalf("ciWaitPollPR errored: %v", err)
	}
	raw, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	return report, terminal, string(raw)
}

func TestCiWaitPRMergeModeCheckFailures(t *testing.T) {
	failureJob := "https://github.com/o/r/actions/runs/10/job/2"
	passJob := "https://github.com/o/r/actions/runs/10/job/1"
	pendingJob := "https://github.com/o/r/actions/runs/10/job/3"
	cases := []struct {
		name        string
		mergeState  string
		rollup      []ghPRRollup
		rawRollup   string
		wantStatus  string
		wantFailing int
		failName    string
		wantReason  string
	}{
		{
			name:        "BLOCKED failure ends the wait",
			mergeState:  "BLOCKED",
			rollup:      []ghPRRollup{ciWaitRollupPass("verify-go"), ciWaitRollupFail("verify-tests", failureJob)},
			wantStatus:  "failure",
			wantFailing: 1,
			failName:    "verify-tests",
		},
		{
			name:        "UNSTABLE does not hide failure",
			mergeState:  "UNSTABLE",
			rollup:      []ghPRRollup{ciWaitRollupPass("verify-go"), ciWaitRollupFail("verify-tests", failureJob)},
			wantStatus:  "failure",
			wantFailing: 1,
			failName:    "verify-tests",
		},
		{
			name:        "unstable matches case-insensitively",
			mergeState:  "unstable",
			rollup:      []ghPRRollup{ciWaitRollupFail("verify-tests", failureJob)},
			wantStatus:  "failure",
			wantFailing: 1,
			failName:    "verify-tests",
		},
		{
			name:        "failure overrides pending peers",
			mergeState:  "BLOCKED",
			rollup:      []ghPRRollup{ciWaitRollupPass("verify-go"), ciWaitRollupFail("verify-tests", failureJob), ciWaitRollupPending("verify-race")},
			wantStatus:  "failure",
			wantFailing: 1,
			failName:    "verify-tests",
		},
		{
			name:       "a newer pending rerun suppresses an older failure",
			mergeState: "BLOCKED",
			rawRollup: ciWaitRawRollup(
				ciWaitRawCheckRun("Verify", "COMPLETED", "FAILURE", ciWaitAttemptOld, failureJob),
				ciWaitRawCheckRun("Verify", "IN_PROGRESS", "", ciWaitAttemptNewer, pendingJob),
			),
			wantStatus: "pending",
		},
		{
			name:       "a newer passing rerun suppresses an older failure",
			mergeState: "BLOCKED",
			rawRollup: ciWaitRawRollup(
				ciWaitRawCheckRun("Verify", "COMPLETED", "FAILURE", ciWaitAttemptOld, failureJob),
				ciWaitRawCheckRun("Verify", "COMPLETED", "SUCCESS", ciWaitAttemptNewer, passJob),
			),
			wantStatus: "pending",
		},
		{
			name:       "a newer failure survives an older pending attempt",
			mergeState: "BLOCKED",
			rawRollup: ciWaitRawRollup(
				ciWaitRawCheckRun("Verify", "IN_PROGRESS", "", ciWaitAttemptOld, pendingJob),
				ciWaitRawCheckRun("Verify", "COMPLETED", "FAILURE", ciWaitAttemptNewer, failureJob),
			),
			wantStatus:  "failure",
			wantFailing: 1,
			failName:    "verify-tests",
		},
		{
			name:       "a newer failure survives an older passing attempt",
			mergeState: "BLOCKED",
			rawRollup: ciWaitRawRollup(
				ciWaitRawCheckRun("Verify", "COMPLETED", "SUCCESS", ciWaitAttemptOld, passJob),
				ciWaitRawCheckRun("Verify", "COMPLETED", "FAILURE", ciWaitAttemptNewer, failureJob),
			),
			wantStatus:  "failure",
			wantFailing: 1,
			failName:    "verify-tests",
		},
		{
			name:        "cancelled check ends the wait",
			mergeState:  "CLEAN",
			rollup:      []ghPRRollup{ciWaitRollupPass("verify-go"), ciWaitRollupCancelled("verify-race")},
			wantStatus:  "failure",
			wantFailing: 1,
			failName:    "verify-race",
		},
		{
			name:        "legacy failing status ends the wait",
			mergeState:  "CLEAN",
			rollup:      []ghPRRollup{ciWaitRollupPass("verify-go"), ciWaitRollupLegacyStatus("legacy-status")},
			wantStatus:  "failure",
			wantFailing: 1,
			failName:    "legacy-status",
		},
		{
			name:       "BLOCKED without failure stays pending",
			mergeState: "BLOCKED",
			rollup:     []ghPRRollup{ciWaitRollupPass("verify-go")},
			wantStatus: "pending",
		},
		{
			name:       "CLEAN without failure explains can merge",
			mergeState: "CLEAN",
			rollup:     []ghPRRollup{ciWaitRollupPass("verify-go")},
			wantStatus: "pending",
			wantReason: "can merge",
		},
		{
			name:       "HAS_HOOKS without failure explains can merge",
			mergeState: "HAS_HOOKS",
			rollup:     []ghPRRollup{ciWaitRollupPass("verify-go")},
			wantStatus: "pending",
			wantReason: "can merge",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			headJSON := ciWaitMergeHeadFixture(t, tc.mergeState, "OPEN", "", "aabbccdd", tc.rollup)
			if tc.rawRollup != "" {
				headJSON = ciWaitMergeHeadBody(t, tc.mergeState, "OPEN", "", "aabbccdd", tc.rawRollup)
			}
			report, terminal, captured := ciWaitPollPROnce(t, "merge", headJSON, "")
			if report.Status != tc.wantStatus || terminal != (tc.wantStatus == "failure") {
				t.Fatalf("merge_state=%s: status=%s terminal=%t want %s (report=%+v)", tc.mergeState, report.Status, terminal, tc.wantStatus, report)
			}
			// The merge population comes from the rollup the poll already
			// fetched. The stub refuses `pr checks`, so the capture is the
			// evidence that no population query ran.
			if strings.Contains(captured, "pr checks") {
				t.Fatalf("merge mode must not query pr checks, calls=%q", captured)
			}
			if !strings.Contains(captured, "pr view") {
				t.Fatalf("merge mode must observe the pull request head, calls=%q", captured)
			}
			if tc.wantStatus != "failure" {
				if report.MergeState != tc.mergeState {
					t.Fatalf("pending must report the observed merge state %q, got %q", tc.mergeState, report.MergeState)
				}
				if tc.wantReason != "" && !strings.Contains(report.Reason, tc.wantReason) {
					t.Fatalf("pending reason must name the can-merge state, got %q", report.Reason)
				}
				return
			}
			if report.Checks == nil || report.Checks.Failing != tc.wantFailing {
				t.Fatalf("failure must count the observed failing checks, got %+v", report.Checks)
			}
			if len(report.Failures) == 0 || report.Failures[0].Name != tc.failName {
				t.Fatalf("failure must name the observed check %q, got %+v", tc.failName, report.Failures)
			}
			if report.RunURL == "" {
				t.Fatal("failure must carry the run URL of the observed failure")
			}
		})
	}

	t.Run("failure details carry the observed evidence", func(t *testing.T) {
		headJSON := ciWaitMergeHeadFixture(t, "BLOCKED", "OPEN", "", "aabbccdd", []ghPRRollup{
			ciWaitRollupPass("verify-go"),
			ciWaitRollupFail("verify-tests", failureJob),
		})
		report, _, captured := ciWaitPollPROnce(t, "merge", headJSON, "")
		if report.Status != "failure" || len(report.Failures) != 1 {
			t.Fatalf("want one failure entry, got status=%s failures=%+v", report.Status, report.Failures)
		}
		detail := report.Failures[0]
		if detail.Name != "verify-tests" || detail.URL != failureJob || detail.Classification != "test_failure" {
			t.Fatalf("failure detail must name the observed check, its job URL, and its class, got %+v", detail)
		}
		if report.RunURL != "https://github.com/o/r/actions/runs/10" {
			t.Fatalf("failure must carry the observed run URL, got %q", report.RunURL)
		}
		if detail.FirstError != "" || strings.Contains(captured, "--log-failed") {
			t.Fatalf("unqualified workflow completion cannot authorize a log delay: detail=%+v calls=%s", detail, captured)
		}
		if strings.Contains(captured, "pr checks") {
			t.Fatalf("merge mode must not query pr checks, calls=%q", captured)
		}
	})
}

// TestCiWaitPRMergeModeNewestAttempts pins merge mode's newest-attempt
// classification: the greatest startedAt per check identity decides the
// verdict, so a newer passing or pending rerun suppresses an older failure of
// the same identity, and an entry of a different identity never erases an
// observed failure.
func TestCiWaitPRMergeModeNewestAttempts(t *testing.T) {
	passJob := "https://github.com/o/r/actions/runs/10/job/1"
	failureJob := "https://github.com/o/r/actions/runs/10/job/2"
	pendingJob := "https://github.com/o/r/actions/runs/10/job/3"
	// Lexically before ciWaitAttemptOld but temporally after it (11:00Z):
	// selecting the newest attempt must compare instants, not strings.
	newerOffset := "2026-10-15T03:00:00-08:00"
	// The exact string the gh export emits for a null startedAt; official gh
	// sorts the zero time as the oldest attempt.
	zero := "0001-01-01T00:00:00Z"
	checkRun := func(workflow, conclusion, startedAt, url string) string {
		return ciWaitRawCheckRun(workflow, "COMPLETED", conclusion, startedAt, url)
	}
	cases := []struct {
		name        string
		entries     []string
		wantStatus  string
		wantFailing int
		failName    string
	}{
		{
			name: "newest pass suppresses an older failure, failure first",
			entries: []string{
				checkRun("Verify", "FAILURE", ciWaitAttemptOld, failureJob),
				checkRun("Verify", "SUCCESS", newerOffset, passJob),
			},
			wantStatus: "pending",
		},
		{
			name: "newest pass suppresses an older failure, pass first",
			entries: []string{
				checkRun("Verify", "SUCCESS", ciWaitAttemptNewer, passJob),
				checkRun("Verify", "FAILURE", ciWaitAttemptOld, failureJob),
			},
			wantStatus: "pending",
		},
		{
			name: "newest pending rerun suppresses an older failure, failure first",
			entries: []string{
				checkRun("Verify", "FAILURE", ciWaitAttemptOld, failureJob),
				ciWaitRawCheckRun("Verify", "IN_PROGRESS", "", ciWaitAttemptNewer, pendingJob),
			},
			wantStatus: "pending",
		},
		{
			name: "newest pending rerun suppresses an older failure, pending first",
			entries: []string{
				ciWaitRawCheckRun("Verify", "IN_PROGRESS", "", ciWaitAttemptNewer, pendingJob),
				checkRun("Verify", "FAILURE", ciWaitAttemptOld, failureJob),
			},
			wantStatus: "pending",
		},
		{
			name: "newest failure survives an older pass, pass first",
			entries: []string{
				checkRun("Verify", "SUCCESS", ciWaitAttemptOld, passJob),
				checkRun("Verify", "FAILURE", ciWaitAttemptNewer, failureJob),
			},
			wantStatus:  "failure",
			wantFailing: 1,
			failName:    "verify-tests",
		},
		{
			name: "newest failure survives an older pass, failure first",
			entries: []string{
				checkRun("Verify", "FAILURE", ciWaitAttemptNewer, failureJob),
				checkRun("Verify", "SUCCESS", ciWaitAttemptOld, passJob),
			},
			wantStatus:  "failure",
			wantFailing: 1,
			failName:    "verify-tests",
		},
		{
			name: "newest failure survives an older pending attempt, pending first",
			entries: []string{
				ciWaitRawCheckRun("Verify", "IN_PROGRESS", "", ciWaitAttemptOld, pendingJob),
				checkRun("Verify", "FAILURE", ciWaitAttemptNewer, failureJob),
			},
			wantStatus:  "failure",
			wantFailing: 1,
			failName:    "verify-tests",
		},
		{
			name: "newest failure survives an older pending attempt, failure first",
			entries: []string{
				checkRun("Verify", "FAILURE", ciWaitAttemptNewer, failureJob),
				ciWaitRawCheckRun("Verify", "IN_PROGRESS", "", ciWaitAttemptOld, pendingJob),
			},
			wantStatus:  "failure",
			wantFailing: 1,
			failName:    "verify-tests",
		},
		{
			name: "a newer pass of a different workflow never suppresses the failure",
			entries: []string{
				checkRun("Verify", "FAILURE", ciWaitAttemptOld, failureJob),
				checkRun("Lint", "SUCCESS", ciWaitAttemptNewer, passJob),
			},
			wantStatus:  "failure",
			wantFailing: 1,
			failName:    "verify-tests",
		},
		{
			name: "a newer pass of a different workflow never suppresses the failure, pass first",
			entries: []string{
				checkRun("Lint", "SUCCESS", ciWaitAttemptNewer, passJob),
				checkRun("Verify", "FAILURE", ciWaitAttemptOld, failureJob),
			},
			wantStatus:  "failure",
			wantFailing: 1,
			failName:    "verify-tests",
		},
		{
			name: "a StatusContext pass never suppresses a CheckRun failure of the same name",
			entries: []string{
				checkRun("Verify", "FAILURE", ciWaitAttemptOld, failureJob),
				ciWaitRawStatusContext("verify-tests", "SUCCESS", ciWaitAttemptNewer),
			},
			wantStatus:  "failure",
			wantFailing: 1,
			failName:    "verify-tests",
		},
		{
			name: "newest StatusContext attempt is selected by its exported startedAt",
			entries: []string{
				ciWaitRawStatusContext("legacy-status", "FAILURE", ciWaitAttemptOld),
				ciWaitRawStatusContext("legacy-status", "SUCCESS", newerOffset),
			},
			wantStatus: "pending",
		},
		{
			name: "newest StatusContext attempt wins in either array order",
			entries: []string{
				ciWaitRawStatusContext("legacy-status", "SUCCESS", ciWaitAttemptNewer),
				ciWaitRawStatusContext("legacy-status", "FAILURE", ciWaitAttemptOld),
			},
			wantStatus: "pending",
		},
		{
			name: "a zero startedAt sorts as the oldest attempt",
			entries: []string{
				checkRun("Verify", "SUCCESS", ciWaitAttemptOld, passJob),
				checkRun("Verify", "FAILURE", zero, failureJob),
			},
			wantStatus: "pending",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			headJSON := ciWaitMergeHeadBody(t, "BLOCKED", "OPEN", "", "aabbccdd", ciWaitRawRollup(tc.entries...))
			report, terminal, captured := ciWaitPollPROnce(t, "merge", headJSON, "")
			if report.Status != tc.wantStatus || terminal != (tc.wantStatus == "failure") {
				t.Fatalf("status=%s terminal=%t want %s (report=%+v)", report.Status, terminal, tc.wantStatus, report)
			}
			if strings.Contains(captured, "pr checks") {
				t.Fatalf("merge mode must classify from the rollup without a pr checks query, calls=%q", captured)
			}
			if tc.wantStatus != "failure" {
				if report.Checks == nil || report.Checks.Failing != 0 {
					t.Fatalf("a suppressed older failure must not be counted, got %+v", report.Checks)
				}
				return
			}
			if report.Checks == nil || report.Checks.Failing != tc.wantFailing {
				t.Fatalf("failure must count the surviving failing checks, got %+v", report.Checks)
			}
			if len(report.Failures) == 0 || report.Failures[0].Name != tc.failName {
				t.Fatalf("failure must name the observed check %q, got %+v", tc.failName, report.Failures)
			}
		})
	}
}

func TestCiWaitPRMergeModeFailurePrecedence(t *testing.T) {
	failureRollup := []ghPRRollup{
		ciWaitRollupPass("verify-go"),
		ciWaitRollupFail("verify-tests", "https://github.com/o/r/actions/runs/10/job/2"),
	}
	cases := []struct {
		name       string
		prState    string
		mergedAt   string
		priorHead  string
		headRefOid string
		wantStatus string
	}{
		{
			name:       "merged wins over an observed failure",
			prState:    "CLOSED",
			mergedAt:   "2026-10-15T12:00:00Z",
			headRefOid: "aabbccdd",
			wantStatus: "merged",
		},
		{
			name:       "closed wins over an observed failure",
			prState:    "CLOSED",
			headRefOid: "aabbccdd",
			wantStatus: "closed",
		},
		{
			name:       "a changed head supersedes the failing wait",
			prState:    "OPEN",
			priorHead:  "oldsha",
			headRefOid: "newsha",
			wantStatus: "superseded",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			headJSON := ciWaitMergeHeadFixture(t, "BLOCKED", tc.prState, tc.mergedAt, tc.headRefOid, failureRollup)
			report, terminal, captured := ciWaitPollPROnce(t, "merge", headJSON, tc.priorHead)
			if report.Status != tc.wantStatus || !terminal {
				t.Fatalf("status=%s terminal=%t want %s (report=%+v)", report.Status, terminal, tc.wantStatus, report)
			}
			if strings.Contains(captured, "pr checks") {
				t.Fatalf("merge mode must not query pr checks, calls=%q", captured)
			}
			if tc.wantStatus == "superseded" && (report.SHA != tc.priorHead || report.HeadSHA != tc.headRefOid) {
				t.Fatalf("superseded must name both heads, got %+v", report)
			}
		})
	}
}

func TestCiWaitPRMergeModeFullPageRollupNeedsNoChecksQuery(t *testing.T) {
	rollup := make([]ghPRRollup, 0, ciWaitPRRollupPageSize)
	for i := 0; i < ciWaitPRRollupPageSize-1; i++ {
		rollup = append(rollup, ciWaitRollupPass(fmt.Sprintf("check-%d", i)))
	}
	rollup = append(rollup, ciWaitRollupFail("verify-tests", "https://github.com/o/r/actions/runs/10/job/2"))
	headJSON := ciWaitMergeHeadFixture(t, "BLOCKED", "OPEN", "", "aabbccdd", rollup)
	report, terminal, captured := ciWaitPollPROnce(t, "merge", headJSON, "")
	if !terminal || report.Status != "failure" {
		t.Fatalf("a full page with an observed failure must end the merge wait, got status=%s terminal=%t", report.Status, terminal)
	}
	if report.Checks == nil || report.Checks.Total != ciWaitPRRollupPageSize || report.Checks.Failing != 1 {
		t.Fatalf("report must count the full rollup population, got %+v", report.Checks)
	}
	if strings.Contains(captured, "pr checks") {
		t.Fatalf("merge mode must classify from the rollup without a pr checks query, calls=%q", captured)
	}
	if !strings.Contains(captured, "pr view") {
		t.Fatalf("merge mode must observe the pull request head, calls=%q", captured)
	}
}

func TestCiWaitPRChecksModeMixedFailureAndPendingFails(t *testing.T) {
	// CD-0228 retires the mixed-failure-waits rule. Negative evidence does not
	// require the complete population needed for a positive verdict.
	rollup := []ghPRRollup{
		ciWaitRollupPass("verify-go"),
		ciWaitRollupFail("verify-tests", "https://github.com/o/r/actions/runs/10/job/2"),
		ciWaitRollupPending("verify-race"),
	}
	headJSON := ciWaitMergeHeadFixture(t, "CLEAN", "OPEN", "", "aabbccdd", rollup)
	report, terminal, _ := ciWaitPollPROnce(t, "checks", headJSON, "")
	if !terminal || report.Status != "failure" {
		t.Fatalf("checks mode must report a failure despite pending peers, got status=%s terminal=%t (report=%+v)", report.Status, terminal, report)
	}
	if report.Checks == nil || report.Checks.Failing != 1 || report.Checks.Pending != 1 {
		t.Fatalf("report must count the observed mix, got %+v", report.Checks)
	}

	t.Run("newest failure replaces an older pending attempt", func(t *testing.T) {
		pending := ciWaitRollupPending("verify-flaky")
		pending.StartedAt, _ = time.Parse(time.RFC3339, ciWaitAttemptOld)
		failed := ciWaitRollupFail("verify-flaky", "https://github.com/o/r/actions/runs/10/job/2")
		failed.StartedAt, _ = time.Parse(time.RFC3339, ciWaitAttemptNewer)
		rollup := []ghPRRollup{
			ciWaitRollupPass("verify-go"),
			pending,
			failed,
		}
		headJSON := ciWaitMergeHeadFixture(t, "CLEAN", "OPEN", "", "aabbccdd", rollup)
		report, terminal, _ := ciWaitPollPROnce(t, "checks", headJSON, "")
		if !terminal || report.Status != "failure" {
			t.Fatalf("the selected failure ends the wait, got status=%s terminal=%t (report=%+v)", report.Status, terminal, report)
		}
		if report.Checks == nil || report.Checks.Failing != 1 || report.Checks.Pending != 0 || report.Checks.Total != 2 {
			t.Fatalf("the later same-name entry must collapse the pending one, got %+v", report.Checks)
		}
		if !strings.Contains(report.Reason, "incomplete") {
			t.Fatalf("failure does not claim a complete inventory, got %q", report.Reason)
		}
	})
}

func TestCiWaitHeadSHAChangeSupersedes(t *testing.T) {
	ghStubPath(t, ghStubDir(t, `case "$*" in
		*"pr view"*) echo '{"baseRefName":"main","headRefOid":"newsha","url":"u"}';;
		*"pr checks"*) echo '[]';;
	esac`))
	statePath := filepath.Join(t.TempDir(), "state.json")
	state := ciWaitState{SchemaVersion: 1, Selector: Selector{Kind: "pr", Value: "5"}, Repo: "o/r",
		BudgetSeconds: 1800, StartedAt: time.Now().UTC(), DeadlineAt: time.Now().UTC().Add(time.Hour), HeadSHA: "oldsha"}
	if err := os.WriteFile(statePath, []byte(ciWaitJSON(t, state)), 0o600); err != nil {
		t.Fatal(err)
	}
	_, report := runCiWaitStdin(t, ciWaitJSON(t, Request{
		Selector: &Selector{Kind: "pr", Value: "5"}, Repo: "o/r",
		StateFile: statePath,
	}))
	if report.Status != "superseded" {
		t.Fatalf("a changed head SHA must supersede the wait, got %s", report.Status)
	}
	if report.SHA != "oldsha" || report.HeadSHA != "newsha" {
		t.Fatalf("superseded report must name both SHAs, got %+v", report)
	}
}

// ciWaitServeHead points gh at a stub whose pr view answers headJSON.
func ciWaitServeHead(t *testing.T, headJSON string) {
	t.Helper()
	ghStubPath(t, ghStubDir(t, `case "$*" in
	*"pr view"*) cat <<'EOC'
`+headJSON+`
EOC
;;
	*"pr checks"*) exit 97;;
	*"run view"*) exit 97;;
`+ciWaitQuietReads+`esac`))
}

// A watch started beside a push can read the pull request before the push
// lands. Named with the pushed commit, it judges only that commit: the head it
// replaces is pending, whatever its checks and merge state say, and the wait
// latches the pushed commit once the pull request reaches it.
func TestCiWaitPRExpectedHeadJudgesOnlyThePushedCommit(t *testing.T) {
	const replaced = "79dfa01fdc5852f7bdbe9510c91dc75a1a4712f9"
	const pushed = "6459f13778fcad4b42f771ac4988f55897f635f5"
	ciWaitServeHead(t, ciWaitMergeHeadFixture(t, "DIRTY", "OPEN", "", replaced,
		[]ghPRRollup{ciWaitRollupFail("verify-tests", "https://github.com/o/r/actions/runs/11/job/1")}))
	statePath := filepath.Join(t.TempDir(), "state.json")
	request := `{"selector":{"kind":"pr","value":"5"},"repo":"o/r","mode":"merge","time_seconds_max":8,"head_sha":"` + pushed + `","state_file":"` + statePath + `"}`
	code, report := runCiWaitStdin(t, request)
	if code != 0 || report.Status != "pending" || report.SHA != pushed || report.HeadSHA != replaced || report.Checks != nil || len(report.Failures) != 0 {
		t.Fatalf("the replaced head's failure must stay pending for the pushed commit, got code=%d report=%+v", code, report)
	}
	if !strings.Contains(report.Reason, "not the expected head") {
		t.Fatalf("the pending reason must say the pull request has not reached the expected head, got %q", report.Reason)
	}

	ciWaitServeHead(t, ciWaitMergeHeadFixture(t, "CLEAN", "OPEN", "", pushed, []ghPRRollup{ciWaitRollupPass("verify-tests")}))
	_, report = runCiWaitStdin(t, request)
	if report.Status != "pending" || report.SHA != pushed || report.HeadSHA != pushed || !strings.Contains(report.Reason, "can merge") {
		t.Fatalf("the pushed commit must be judged once the pull request reaches it, got %+v", report)
	}

	const later = "0123456789abcdef0123456789abcdef01234567"
	ciWaitServeHead(t, ciWaitMergeHeadFixture(t, "CLEAN", "OPEN", "", later, []ghPRRollup{ciWaitRollupPass("verify-tests")}))
	_, report = runCiWaitStdin(t, request)
	if report.Status != "superseded" || report.SHA != pushed || report.HeadSHA != later {
		t.Fatalf("a head change after the pushed commit is latched must supersede the wait, got %+v", report)
	}
}

// A pull request merged or closed at another head never reaches the expected
// commit, so the wait ends superseded instead of judging the head it found.
func TestCiWaitPRExpectedHeadNeverReachedIsSuperseded(t *testing.T) {
	const replaced = "79dfa01fdc5852f7bdbe9510c91dc75a1a4712f9"
	const pushed = "6459f13778fcad4b42f771ac4988f55897f635f5"
	for _, tc := range []struct{ name, mergedAt string }{{"merged", "2026-10-18T04:10:20Z"}, {"closed", ""}} {
		t.Run(tc.name, func(t *testing.T) {
			ciWaitServeHead(t, ciWaitMergeHeadFixture(t, "CLEAN", "CLOSED", tc.mergedAt, replaced, []ghPRRollup{ciWaitRollupPass("verify-tests")}))
			_, report := runCiWaitStdin(t, `{"selector":{"kind":"pr","value":"5"},"repo":"o/r","mode":"merge","time_seconds_max":8,"head_sha":"`+pushed+`"}`)
			if report.Status != "superseded" || report.SHA != pushed || report.HeadSHA != replaced {
				t.Fatalf("a pull request %s at another head must supersede the wait, got %+v", tc.name, report)
			}
		})
	}
}

// The merge state a poll observes before the latch belongs to the head the
// push replaces, so a wait that times out before the pull request reaches the
// expected commit names that commit and carries no merge state.
func TestCiWaitPRExpectedHeadTimeoutCarriesNoReplacedMergeState(t *testing.T) {
	const replaced = "79dfa01fdc5852f7bdbe9510c91dc75a1a4712f9"
	const pushed = "6459f13778fcad4b42f771ac4988f55897f635f5"
	ciWaitServeHead(t, ciWaitMergeHeadFixture(t, "DIRTY", "OPEN", "", replaced, nil))
	statePath := filepath.Join(t.TempDir(), "state.json")
	request := `{"selector":{"kind":"pr","value":"5"},"repo":"o/r","mode":"merge","time_seconds_max":8,"head_sha":"` + pushed + `","state_file":"` + statePath + `"}`
	if _, report := runCiWaitStdin(t, request); report.Status != "pending" {
		t.Fatalf("the replaced head must keep the wait pending, got %+v", report)
	}
	state, ok := ciWaitReadState(statePath)
	if !ok {
		t.Fatal("a pending slice must persist its state")
	}
	state.DeadlineAt = time.Now().Add(-time.Minute)
	ciWaitSaveState(state, statePath)
	code, report := runCiWaitStdin(t, request)
	if code != 1 || report.Status != "timeout" || report.SHA != pushed || report.HeadSHA != "" || report.MergeState != "" || !strings.Contains(report.Reason, "reached the expected head") {
		t.Fatalf("a timeout before the pull request reaches the pushed commit must say so and carry no replaced-head merge state, got code=%d report=%+v", code, report)
	}
}

func TestCiWaitExpectedHeadRequiresAPRSelectorAndACommitSHA(t *testing.T) {
	ghStubPath(t, ghStubDir(t, "exit 97"))
	for name, body := range map[string]string{
		"sha selector": `{"selector":{"kind":"sha","value":"6459f13778fcad4b42f771ac4988f55897f635f5"},"repo":"o/r","head_sha":"6459f13778fcad4b42f771ac4988f55897f635f5"}`,
		"short sha":    `{"selector":{"kind":"pr","value":"5"},"repo":"o/r","head_sha":"6459f137"}`,
	} {
		code, report := runCiWaitStdin(t, body)
		if code != 1 || report.Status != "refused" {
			t.Fatalf("%s: want refused, got code=%d report=%+v", name, code, report)
		}
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
			ghStubPath(t, ghStubDir(t, ciWaitSelectorRunStub(t, "completed", tc.conclusion)))
			_, report := runCiWaitStdin(t, ciWaitJSON(t, Request{
				Selector: &Selector{Kind: "run", Value: "42"}, Repo: "o/r", TimeSecondsMax: ciWaitBudget(1800),
			}))
			if report.Status != tc.status {
				t.Fatalf("conclusion %s: status=%s want %s", tc.conclusion, report.Status, tc.status)
			}
		})
	}
}

func TestCiWaitRunSelectorPendingStaysPending(t *testing.T) {
	ghStubPath(t, ghStubDir(t, ciWaitSelectorRunStub(t, "in_progress", "")))
	_, report := runCiWaitStdin(t, ciWaitJSON(t, Request{
		Selector: &Selector{Kind: "run", Value: "42"}, Repo: "o/r", TimeSecondsMax: ciWaitBudget(8),
	}))
	if report.Status != "pending" {
		t.Fatalf("an in-progress run must stay pending, got %s", report.Status)
	}
}

func TestCiWaitSHASelectorNoRunsIsPendingNeverSuccess(t *testing.T) {
	ghStubPath(t, ghStubDir(t, ciWaitSelectorRunsStub(t, nil)))
	_, report := runCiWaitStdin(t, ciWaitJSON(t, Request{
		Selector: &Selector{Kind: "sha", Value: strings.Repeat("a", 40)}, Repo: "o/r", TimeSecondsMax: ciWaitBudget(8),
	}))
	if report.Status != "pending" || report.Checks == nil || report.Checks.Total != 0 {
		t.Fatalf("no runs must be pending with an empty observed set, got %+v", report)
	}
}

func TestCiWaitSHASelectorCancelledRunIsFailure(t *testing.T) {
	runs := []ghRunSummary{{DatabaseID: 1, Name: "CI", Status: "completed", Conclusion: "cancelled", URL: "https://github.com/o/r/actions/runs/1"}}
	ghStubPath(t, ghStubDir(t, ciWaitSelectorRunsStub(t, runs)))
	_, report := runCiWaitStdin(t, ciWaitJSON(t, Request{
		Selector: &Selector{Kind: "sha", Value: strings.Repeat("a", 40)}, Repo: "o/r", TimeSecondsMax: ciWaitBudget(1800),
	}))
	if report.Status != "failure" {
		t.Fatalf("a cancelled run is a failure verdict, got %s", report.Status)
	}
	if len(report.Failures) != 1 || report.Failures[0].Classification != "cancelled" {
		t.Fatalf("cancelled must classify as cancelled, got %+v", report.Failures)
	}
}

func TestCiWaitSHAFailureRunURL(t *testing.T) {
	passURL := "https://github.com/o/r/actions/runs/10"
	failURL := "https://github.com/o/r/actions/runs/20"
	cases := []struct {
		name string
		runs []ghRunSummary
	}{
		{
			name: "passing first then failing",
			runs: []ghRunSummary{
				{DatabaseID: 10, Name: "Build", Status: "completed", Conclusion: "success", URL: passURL},
				{DatabaseID: 20, Name: "Verify", Status: "completed", Conclusion: "failure", URL: failURL},
			},
		},
		{
			name: "failing first then passing",
			runs: []ghRunSummary{
				{DatabaseID: 20, Name: "Verify", Status: "completed", Conclusion: "failure", URL: failURL},
				{DatabaseID: 10, Name: "Build", Status: "completed", Conclusion: "success", URL: passURL},
			},
		},
		{
			name: "passing first then cancelled",
			runs: []ghRunSummary{
				{DatabaseID: 10, Name: "Build", Status: "completed", Conclusion: "success", URL: passURL},
				{DatabaseID: 20, Name: "Verify", Status: "completed", Conclusion: "cancelled", URL: failURL},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ghStubPath(t, ghStubDir(t, ciWaitSelectorRunsStub(t, tc.runs)))
			code, report := runCiWaitStdin(t, ciWaitJSON(t, Request{
				Selector: &Selector{Kind: "sha", Value: strings.Repeat("a", 40)}, Repo: "o/r", TimeSecondsMax: ciWaitBudget(1800),
			}))
			if report.Status != "failure" {
				t.Fatalf("a completed failing run must end the wait with failure, got %s (report=%+v)", report.Status, report)
			}
			if code != 0 {
				t.Fatalf("a failure verdict is a successful observation; want exit 0, got %d", code)
			}
			if report.RunURL != failURL {
				t.Fatalf("failure must carry the failing run URL %q, got %q (report=%+v)", failURL, report.RunURL, report)
			}
			if report.Checks == nil || report.Checks.Total != 2 || report.Checks.Passing != 1 || report.Checks.Failing != 1 || report.Checks.Pending != 0 {
				t.Fatalf("counts must reflect the observed population total=2 pass=1 fail=1 pending=0, got %+v", report.Checks)
			}
			if len(report.Failures) != 1 || report.Failures[0].Name != "Verify" || report.Failures[0].URL != failURL {
				t.Fatalf("want one failure naming %q at %q, got %+v", "Verify", failURL, report.Failures)
			}
		})
	}
}

// --- error paths ---

func TestCiWaitAuthFailureIsErrorNeverSuccess(t *testing.T) {
	ghStubPath(t, ghStubDir(t, `echo "gh: To get started with GitHub CLI, please run: gh auth login" >&2; exit 1`))
	code, report := runCiWaitStdin(t, ciWaitJSON(t, Request{
		Selector: &Selector{Kind: "pr", Value: "5"}, Repo: "o/r", TimeSecondsMax: ciWaitBudget(1800),
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
	_, report := runCiWaitStdin(t, ciWaitJSON(t, Request{
		Selector: &Selector{Kind: "pr", Value: "5"}, Repo: "o/r", TimeSecondsMax: ciWaitBudget(1800),
	}))
	if report.Status != "error" || !strings.Contains(report.Reason, "unreadable") {
		t.Fatalf("malformed response must be an explicit error, got %+v", report)
	}
}

func TestCiWaitMissingPRIsErrorWithVerbatimDetail(t *testing.T) {
	ghStubPath(t, ghStubDir(t, `echo "GraphQL: Could not resolve to a PullRequest with the number of 999999." >&2; exit 1`))
	_, report := runCiWaitStdin(t, ciWaitJSON(t, Request{
		Selector: &Selector{Kind: "pr", Value: "999999"}, Repo: "o/r", TimeSecondsMax: ciWaitBudget(1800),
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
		*"pr view"*) echo '{"baseRefName":"main","headRefOid":"aabb","url":"u","statusCheckRollup":[{"name":"c","status":"IN_PROGRESS"}] }';;
		*"pr checks"*) echo '[{"name":"c","state":"IN_PROGRESS","link":"l","bucket":"pending"}]';;
	`+ciWaitQuietReads+`esac`))
	_, report := runCiWaitStdin(t, ciWaitJSON(t, Request{
		Selector: &Selector{Kind: "pr", Value: "5"}, Repo: "o/r", TimeSecondsMax: ciWaitBudget(8),
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
		*"pr view"*) echo '{"baseRefName":"main","headRefOid":"aabb","url":"u","statusCheckRollup":[{"name":"c","status":"IN_PROGRESS"}] }';;
		*"pr checks"*) echo '[{"name":"c","state":"IN_PROGRESS","link":"l","bucket":"pending"}]';;
	`+ciWaitQuietReads+`esac`))
	start := time.Now()
	code, report := runCiWaitStdin(t, ciWaitJSON(t, Request{
		Selector: &Selector{Kind: "pr", Value: "5"}, Repo: "o/r", TimeSecondsMax: ciWaitBudget(4),
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

// CD-0161 D2: in checks mode a merged or closed pull request still reports
// the watched head's check verdict. The pull request's terminal state proves
// nothing about CI, so it never ends a checks wait by itself.
func TestCiWaitPRChecksModeJudgesMergedAndClosedHeads(t *testing.T) {
	failJob := "https://github.com/o/r/actions/runs/10/job/2"
	populations := []struct {
		name         string
		rollup       []ghPRRollup
		wantStatus   string
		wantTerminal bool
	}{
		{"passing", []ghPRRollup{ciWaitRollupPass("verify-go")}, "success", true},
		{"failing", []ghPRRollup{ciWaitRollupPass("verify-go"), ciWaitRollupFail("verify-tests", failJob)}, "failure", true},
		{"pending", []ghPRRollup{ciWaitRollupPass("verify-go"), ciWaitRollupPending("verify-tests")}, "pending", false},
		{"empty", []ghPRRollup{}, "pending", false},
	}
	for _, ended := range []struct{ name, mergedAt string }{{"merged", "2026-10-18T15:32:40Z"}, {"closed", ""}} {
		for _, population := range populations {
			t.Run(ended.name+"/"+population.name, func(t *testing.T) {
				headJSON := ciWaitMergeHeadFixture(t, "UNKNOWN", "CLOSED", ended.mergedAt, "aabbccdd", population.rollup)
				report, terminal, _ := ciWaitPollPROnce(t, "checks", headJSON, "")
				if report.Status != population.wantStatus || terminal != population.wantTerminal {
					t.Fatalf("status=%s terminal=%t want %s terminal=%t (report=%+v)", report.Status, terminal, population.wantStatus, population.wantTerminal, report)
				}
				if report.Checks == nil || report.Checks.Total != len(population.rollup) {
					t.Fatalf("the report must count the watched head's checks, got %+v", report.Checks)
				}
				if population.name == "failing" && (len(report.Failures) != 1 || report.Failures[0].URL != failJob) {
					t.Fatalf("a failing set must name its failure, got %+v", report.Failures)
				}
			})
		}
	}
}

// The representative run of a commit comes from the provider's creation
// metadata, never from the order gh lists the runs in: a run another run
// triggers is created after it, so the earliest run stands for the commit.
// Every observed run still counts.
func TestCiWaitSHARunURLIsIndependentOfRunOrder(t *testing.T) {
	ci := ghRunSummary{DatabaseID: 39140963598, Name: "CI", Status: "completed", Conclusion: "success", URL: "https://github.com/o/r/actions/runs/39140963598", CreatedAt: "2026-10-18T15:30:01Z"}
	release := ghRunSummary{DatabaseID: 39141183868, Name: "Auto Release", Status: "completed", Conclusion: "success", URL: "https://github.com/o/r/actions/runs/39141183868", CreatedAt: "2026-10-18T15:36:12Z"}
	failing := func(run ghRunSummary) ghRunSummary { run.Conclusion = "failure"; return run }
	pending := func(run ghRunSummary) ghRunSummary { run.Status, run.Conclusion = "in_progress", ""; return run }
	cases := []struct {
		name       string
		runs       []ghRunSummary
		wantStatus string
		wantURL    string
		wantNames  []string
	}{
		{"success", []ghRunSummary{ci, release}, "success", ci.URL, nil},
		{"failure", []ghRunSummary{failing(ci), failing(release)}, "failure", ci.URL, []string{"CI"}},
		{"pending", []ghRunSummary{ci, pending(release)}, "pending", ci.URL, nil},
	}
	for _, tc := range cases {
		for _, order := range []string{"listed", "reversed"} {
			t.Run(tc.name+"/"+order, func(t *testing.T) {
				runs := append([]ghRunSummary(nil), tc.runs...)
				if order == "reversed" {
					runs[0], runs[1] = runs[1], runs[0]
				}
				ghStubPath(t, ghStubDir(t, ciWaitSelectorRunsStub(t, runs)))
				_, report := runCiWaitStdin(t, ciWaitJSON(t, Request{
					Selector: &Selector{Kind: "sha", Value: strings.Repeat("a", 40)}, Repo: "o/r", TimeSecondsMax: ciWaitBudget(8),
				}))
				if report.Status != tc.wantStatus || report.RunURL != tc.wantURL {
					t.Fatalf("status=%s run_url=%q want %s %q (report=%+v)", report.Status, report.RunURL, tc.wantStatus, tc.wantURL, report)
				}
				wantTotal := 2
				if tc.wantStatus == "failure" {
					// CD-0228 permits stopping before reading a failed run's peers.
					wantTotal = 1
					if !strings.Contains(report.Reason, "incomplete") {
						t.Fatalf("fail-fast must disclose its partial inventory, got %q", report.Reason)
					}
				}
				if report.Checks == nil || report.Checks.Total != wantTotal {
					t.Fatalf("every observed run must count; want %d, got %+v", wantTotal, report.Checks)
				}
				var names []string
				for _, failure := range report.Failures {
					names = append(names, failure.Name)
				}
				if !reflect.DeepEqual(names, tc.wantNames) {
					t.Fatalf("failures=%v want %v", names, tc.wantNames)
				}
			})
		}
	}
}

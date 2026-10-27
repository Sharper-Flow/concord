package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const ciWaitWorkflowSHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func ciWaitWorkflowRunFixture(id, workflow, attempt int, event, created, status, conclusion string) map[string]any {
	return map[string]any{"id": id, "workflow_id": workflow, "run_attempt": attempt, "event": event,
		"head_sha": ciWaitWorkflowSHA, "created_at": created, "name": fmt.Sprintf("workflow-%d", workflow),
		"status": status, "conclusion": conclusion, "html_url": fmt.Sprintf("https://github.com/o/r/actions/runs/%d", id)}
}

func ciWaitWorkflowJobFixture(id int, name, status, conclusion string) map[string]any {
	return map[string]any{"name": name, "head_sha": ciWaitWorkflowSHA, "status": status,
		"conclusion": conclusion, "html_url": fmt.Sprintf("https://github.com/o/r/actions/runs/10/job/%d", id)}
}

// Every unexpected read fails, including failed-log reads of an unfinished run.
func ciWaitWorkflowFixture(t *testing.T, head, runs, jobs string) string {
	t.Helper()
	capture := filepath.Join(t.TempDir(), "calls")
	var pages []struct {
		Runs []map[string]any `json:"workflow_runs"`
	}
	if err := json.Unmarshal([]byte(runs), &pages); err != nil {
		t.Fatal(err)
	}
	var legacy []map[string]any
	for _, page := range pages {
		for _, run := range page.Runs {
			legacy = append(legacy, map[string]any{"databaseId": run["id"], "name": run["name"], "status": run["status"], "conclusion": run["conclusion"], "url": run["html_url"], "createdAt": run["created_at"]})
		}
	}
	ghStubPath(t, ghStubDir(t, `echo "$*" >> "`+capture+`"
case "$*" in
 *"pr view"*) echo '`+head+`';;
 *"run list"*) echo '`+ciWaitJSON(t, legacy)+`';;
 *"api repos/o/r/actions/runs?"*) echo '`+runs+`';;
 *"/attempts/"*"/jobs?"*) echo '`+jobs+`';;
 *"run view 10"*"--json"*) echo '{"databaseId":10,"attempt":2,"status":"in_progress","url":"https://github.com/o/r/actions/runs/10","headSha":"`+ciWaitWorkflowSHA+`"}';;
 `+ciWaitQuietReads+`
 *) echo "unexpected read: $*" >&2; exit 97;;
esac`))
	return capture
}

func ciWaitWorkflowPages(t *testing.T, key string, entries ...map[string]any) string {
	t.Helper()
	if entries == nil {
		entries = []map[string]any{}
	}
	return ciWaitJSON(t, []any{map[string]any{"total_count": len(entries), key: entries}})
}

func TestCiWaitWorkflowPaginationAndReadFailures(t *testing.T) {
	head := ciWaitMergeHeadFixture(t, "CLEAN", "OPEN", "", ciWaitWorkflowSHA, []ghPRRollup{ciWaitRollupPass("title")})
	pass := ciWaitWorkflowRunFixture(10, 1, 2, "pull_request", ciWaitAttemptOld, "completed", "success")
	failed := ciWaitWorkflowJobFixture(2, "tests", "completed", "failure")
	passed := ciWaitWorkflowJobFixture(1, "build", "completed", "success")
	for _, tc := range []struct {
		name, runs, jobs, want string
	}{
		{"terminal full workload", ciWaitWorkflowPages(t, "workflow_runs", pass), ciWaitWorkflowPages(t, "jobs", passed), "success"},
		{"failed job on second page", ciWaitWorkflowPages(t, "workflow_runs", pass), ciWaitJSON(t, []any{
			map[string]any{"total_count": 2, "jobs": []any{passed}}, map[string]any{"total_count": 2, "jobs": []any{failed}}}), "failure"},
		{"queued workflow on second page", ciWaitJSON(t, []any{
			map[string]any{"total_count": 2, "workflow_runs": []any{pass}}, map[string]any{"total_count": 2, "workflow_runs": []any{ciWaitWorkflowRunFixture(11, 2, 1, "push", ciWaitAttemptNewer, "queued", "")}}}), ciWaitWorkflowPages(t, "jobs", passed), "pending"},
		{"truncated runs", `[{"total_count":1,"workflow_runs":[]}]`, ciWaitWorkflowPages(t, "jobs"), "error"},
		{"missing runs", `[{}]`, ciWaitWorkflowPages(t, "jobs"), "error"},
		{"missing jobs", ciWaitWorkflowPages(t, "workflow_runs", pass), `[{}]`, "error"},
		{"truncated jobs", ciWaitWorkflowPages(t, "workflow_runs", pass), `[{"total_count":1,"jobs":[]}]`, "error"},
		{"permission error is not a job failure", ciWaitWorkflowPages(t, "workflow_runs", pass), `{"message":"HTTP 403"}`, "error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			capture := ciWaitWorkflowFixture(t, head, tc.runs, tc.jobs)
			report, terminal, err := ciWaitPoll(context.Background(), ciWaitChecksState())
			if tc.want == "error" {
				if err == nil || report.Status == "failure" || terminal {
					t.Fatalf("an unreadable population is no verdict: terminal=%t err=%v report=%+v", terminal, err, report)
				}
				return
			}
			if err != nil || report.Status != tc.want || terminal != (tc.want != "pending") {
				t.Fatalf("terminal=%t err=%v report=%+v want=%s", terminal, err, report, tc.want)
			}
			raw, _ := os.ReadFile(capture)
			if !strings.Contains(string(raw), "--method GET --paginate --slurp") || !strings.Contains(string(raw), "/attempts/2/jobs?") {
				t.Fatalf("must use paginated reads and the observed attempt: %s", raw)
			}
		})
	}
}

func TestCiWaitWorkflowAttemptIdentity(t *testing.T) {
	old := ciWaitWorkflowRun{ID: 1, WorkflowID: 7, Event: "push", CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	newer := old
	newer.ID = 2
	otherEvent := newer
	otherEvent.ID, otherEvent.Event = 3, "pull_request"
	otherWorkflow := newer
	otherWorkflow.ID, otherWorkflow.WorkflowID = 4, 8
	for _, runs := range [][]ciWaitWorkflowRun{{old, newer, otherEvent, otherWorkflow}, {otherWorkflow, otherEvent, newer, old}} {
		selected := ciWaitNewestWorkflowRuns(runs)
		if len(selected) != 3 || selected[0].ID != 2 || selected[1].ID != 3 || selected[2].ID != 4 {
			t.Fatalf("different workflows/events remain distinct and ID breaks a time tie: %+v", selected)
		}
	}
}

func TestCiWaitFailureDoesNotWaitForRequiredness(t *testing.T) {
	head := ciWaitMergeHeadFixture(t, "UNSTABLE", "OPEN", "", ciWaitWorkflowSHA,
		[]ghPRRollup{ciWaitRollupLegacyStatus("optional-status"), ciWaitRollupPending("tests")})
	fixture := newCiWaitRequiredFixture(t, head, "", "", "")
	report, terminal, err := ciWaitPoll(context.Background(), ciWaitChecksState())
	if err != nil || !terminal || report.Status != "failure" || report.Failures[0].Name != "optional-status" {
		t.Fatalf("failure does not need successful requiredness discovery: terminal=%t err=%v report=%+v", terminal, err, report)
	}
	calls := fixture.calls(t)
	if strings.Contains(calls, "rules/branches") || strings.Contains(report.Reason, "required failure") {
		t.Fatalf("must not delay or invent requiredness: reason=%s calls=%s", report.Reason, calls)
	}
}

func TestCiWaitFailFastServedReport(t *testing.T) {
	head := ciWaitMergeHeadFixture(t, "UNSTABLE", "OPEN", "", ciWaitWorkflowSHA, nil)
	ciWaitWorkflowFixture(t, head, ciWaitWorkflowPages(t, "workflow_runs",
		ciWaitWorkflowRunFixture(10, 1, 2, "pull_request", ciWaitAttemptOld, "in_progress", "")),
		ciWaitWorkflowPages(t, "jobs", ciWaitWorkflowJobFixture(1, "tests", "completed", "failure"), ciWaitWorkflowJobFixture(2, "race", "queued", "")))
	stateFile := filepath.Join(t.TempDir(), "state.json")
	code, report := runCiWaitStdin(t, ciWaitJSON(t, ciWaitRequest{Repo: "o/r", Selector: &ciWaitSelector{Kind: "pr", Value: "5"}, HeadSHA: ciWaitWorkflowSHA, StateFile: stateFile, TimeSecondsMax: ciWaitBudget(8)}))
	if code != 0 || report.Status != "failure" || report.Iterations != 1 || report.Checks.Failing != 1 || report.Checks.Pending == 0 || report.StateFile != "" {
		t.Fatalf("terminal observation is delivered on the first slice: code=%d report=%+v", code, report)
	}
	if _, err := os.Stat(stateFile); !os.IsNotExist(err) {
		t.Fatalf("terminal observation must remove resumable state: %v", err)
	}
}

func TestCiWaitWorkflowSubjectAndCurrentAttempt(t *testing.T) {
	head := ciWaitMergeHeadFixture(t, "CLEAN", "OPEN", "", ciWaitWorkflowSHA,
		[]ghPRRollup{ciWaitRollupFail("old-tests", "https://github.com/o/r/actions/runs/10/job/1")})
	t.Run("same run rerun uses current attempt rather than old rollup", func(t *testing.T) {
		capture := ciWaitWorkflowFixture(t, head, ciWaitWorkflowPages(t, "workflow_runs", ciWaitWorkflowRunFixture(10, 1, 2, "pull_request", ciWaitAttemptOld, "completed", "success")),
			ciWaitWorkflowPages(t, "jobs", ciWaitWorkflowJobFixture(2, "tests", "completed", "success")))
		report, terminal, err := ciWaitPoll(context.Background(), ciWaitChecksState())
		if err != nil || !terminal || report.Status != "success" || report.Checks.Failing != 0 {
			t.Fatalf("old attempt must not fail current run: terminal=%t err=%v report=%+v", terminal, err, report)
		}
		raw, _ := os.ReadFile(capture)
		if !strings.Contains(string(raw), "/attempts/2/jobs?") || strings.Contains(string(raw), "/attempts/1/") {
			t.Fatalf("job read must bind current attempt: %s", raw)
		}
	})
	for _, foreign := range []string{"run", "job"} {
		t.Run("foreign "+foreign+" is not negative evidence", func(t *testing.T) {
			run := ciWaitWorkflowRunFixture(10, 1, 2, "pull_request", ciWaitAttemptOld, "in_progress", "")
			job := ciWaitWorkflowJobFixture(1, "tests", "completed", "failure")
			if foreign == "run" {
				run["head_sha"] = strings.Repeat("b", 40)
			} else {
				job["head_sha"] = strings.Repeat("b", 40)
			}
			ciWaitWorkflowFixture(t, ciWaitMergeHeadFixture(t, "CLEAN", "OPEN", "", ciWaitWorkflowSHA, nil), ciWaitWorkflowPages(t, "workflow_runs", run), ciWaitWorkflowPages(t, "jobs", job))
			report, terminal, err := ciWaitPoll(context.Background(), ciWaitChecksState())
			if err == nil || terminal || report.Status == "failure" {
				t.Fatalf("foreign subject cannot fail watched head: terminal=%t err=%v report=%+v", terminal, err, report)
			}
		})
	}
}

func TestCiWaitKnownRunFailureSurvivesMissingJobDetail(t *testing.T) {
	head := ciWaitMergeHeadFixture(t, "CLEAN", "OPEN", "", ciWaitWorkflowSHA, nil)
	ciWaitWorkflowFixture(t, head, ciWaitWorkflowPages(t, "workflow_runs", ciWaitWorkflowRunFixture(10, 1, 1, "push", ciWaitAttemptOld, "completed", "failure")), `not json`)
	report, terminal, err := ciWaitPoll(context.Background(), ciWaitChecksState())
	if err != nil || !terminal || report.Status != "failure" || !strings.Contains(report.Reason, "job detail unavailable") || report.Failures[0].URL != "https://github.com/o/r/actions/runs/10" {
		t.Fatalf("missing details do not erase an observed run failure: terminal=%t err=%v report=%+v", terminal, err, report)
	}
}

func TestCiWaitCompletedRunFailureQuotesItsLog(t *testing.T) {
	head := ciWaitMergeHeadFixture(t, "CLEAN", "OPEN", "", ciWaitWorkflowSHA, nil)
	ghStubPath(t, ghStubDir(t, `case "$*" in
 *"pr view"*) echo '`+head+`';;
 *"api repos/o/r/actions/runs?"*) echo '`+ciWaitWorkflowPages(t, "workflow_runs", ciWaitWorkflowRunFixture(10, 1, 2, "pull_request", ciWaitAttemptOld, "completed", "failure"))+`';;
 *"/attempts/2/jobs?"*) echo '`+ciWaitWorkflowPages(t, "jobs", ciWaitWorkflowJobFixture(1, "build", "completed", "success"), ciWaitWorkflowJobFixture(2, "tests", "completed", "failure"))+`';;
 "run view 10 --repo o/r --log-failed") printf 'tests\tstep\tsetup\ntests\tstep\t--- FAIL: TestThing (0.1s)\n';;
 `+ciWaitQuietReads+`
 *) echo "unexpected read: $*" >&2; exit 97;;
esac`))
	report, terminal, err := ciWaitPoll(context.Background(), ciWaitChecksState())
	if err != nil || !terminal || report.Status != "failure" || len(report.Failures) != 1 {
		t.Fatalf("a completed run's failed job is a failure: terminal=%t err=%v report=%+v", terminal, err, report)
	}
	if detail := report.Failures[0]; detail.Name != "tests" || detail.FirstError != "--- FAIL: TestThing (0.1s)" {
		t.Fatalf("a completed run's failure must quote its failed log: %+v", detail)
	}
}

func TestCiWaitQueuedWorkflows(t *testing.T) {
	for _, status := range []string{"queued", "in_progress"} {
		t.Run(status, func(t *testing.T) {
			head := ciWaitMergeHeadFixture(t, "CLEAN", "OPEN", "", ciWaitWorkflowSHA,
				[]ghPRRollup{ciWaitRollupPass("title"), ciWaitRollupPass("issue-link")})
			ciWaitWorkflowFixture(t, head, ciWaitWorkflowPages(t, "workflow_runs",
				ciWaitWorkflowRunFixture(10, 1, 1, "pull_request", ciWaitAttemptOld, status, "")), ciWaitWorkflowPages(t, "jobs"))
			report, terminal, err := ciWaitPoll(context.Background(), ciWaitChecksState())
			if err != nil || terminal || report.Status != "pending" || report.Checks.Pending == 0 {
				t.Fatalf("two green metadata checks cannot complete %s CI: terminal=%t err=%v report=%+v", status, terminal, err, report)
			}
		})
	}
}

func TestCiWaitFailFastReports(t *testing.T) {
	for _, selector := range []string{"pr", "sha", "run", "merge"} {
		t.Run(selector, func(t *testing.T) {
			head := ciWaitMergeHeadFixture(t, "UNSTABLE", "OPEN", "", ciWaitWorkflowSHA,
				[]ghPRRollup{ciWaitRollupPass("title"), ciWaitRollupPending("race")})
			capture := ciWaitWorkflowFixture(t, head, ciWaitWorkflowPages(t, "workflow_runs",
				ciWaitWorkflowRunFixture(10, 1, 2, "pull_request", ciWaitAttemptOld, "in_progress", "")),
				ciWaitWorkflowPages(t, "jobs", ciWaitWorkflowJobFixture(1, "tests", "completed", "failure"), ciWaitWorkflowJobFixture(2, "race", "in_progress", "")))
			state := ciWaitChecksState()
			switch selector {
			case "sha":
				state.Selector = ciWaitSelector{Kind: "sha", Value: ciWaitWorkflowSHA}
			case "run":
				state.Selector = ciWaitSelector{Kind: "run", Value: "10"}
			case "merge":
				state.Mode = "merge"
			}
			report, terminal, err := ciWaitPoll(context.Background(), state)
			if err != nil || !terminal || report.Status != "failure" || report.SHA != ciWaitWorkflowSHA {
				t.Fatalf("must report failed child of active run: terminal=%t err=%v report=%+v", terminal, err, report)
			}
			if len(report.Failures) != 1 || report.Failures[0].Name != "tests" || report.Failures[0].URL != "https://github.com/o/r/actions/runs/10/job/1" {
				t.Fatalf("failure needs actual job identity: %+v", report)
			}
			if !strings.Contains(report.Reason, "incomplete") || report.Checks.Pending == 0 {
				t.Fatalf("fail fast is not a full inventory: %+v", report)
			}
			raw, err := os.ReadFile(capture)
			if err != nil || strings.Contains(string(raw), "--log-failed") {
				t.Fatalf("must not wait for unfinished workflow logs: calls=%s err=%v", raw, err)
			}
		})
	}
}

func TestCiWaitNewestWorkflowAttempts(t *testing.T) {
	for _, mode := range []string{"checks", "merge", "sha"} {
		t.Run(mode, func(t *testing.T) {
			head := ciWaitMergeHeadFixture(t, "CLEAN", "OPEN", "", ciWaitWorkflowSHA,
				[]ghPRRollup{ciWaitRollupFail("tests", "https://github.com/o/r/actions/runs/9/job/1")})
			ciWaitWorkflowFixture(t, head, ciWaitWorkflowPages(t, "workflow_runs",
				ciWaitWorkflowRunFixture(9, 1, 1, "pull_request", ciWaitAttemptOld, "completed", "failure"),
				ciWaitWorkflowRunFixture(10, 1, 2, "pull_request", ciWaitAttemptNewer, "queued", "")), ciWaitWorkflowPages(t, "jobs"))
			state := ciWaitChecksState()
			state.Mode = mode
			if mode == "sha" {
				state.Mode = "checks"
				state.Selector = ciWaitSelector{Kind: "sha", Value: ciWaitWorkflowSHA}
			}
			report, terminal, err := ciWaitPoll(context.Background(), state)
			if err != nil || terminal || report.Status != "pending" || report.Checks.Failing != 0 {
				t.Fatalf("old run cannot fail current candidate: terminal=%t err=%v report=%+v", terminal, err, report)
			}
		})
	}
}

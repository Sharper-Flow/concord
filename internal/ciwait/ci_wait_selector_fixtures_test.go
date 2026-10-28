package ciwait

import (
	"testing"
	"time"
)

const ciWaitSelectorFixtureSHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func ciWaitSelectorRunsFixture(t *testing.T, runs []ghRunSummary) string {
	t.Helper()
	qualified := make([]ciWaitWorkflowRun, 0, len(runs))
	for _, run := range runs {
		createdAt := time.Date(2026, time.October, 18, 15, 30, 0, 0, time.UTC)
		if run.CreatedAt != "" {
			var err error
			createdAt, err = time.Parse(time.RFC3339, run.CreatedAt)
			if err != nil {
				t.Fatal(err)
			}
		}
		qualified = append(qualified, ciWaitWorkflowRun{
			ID: run.DatabaseID, WorkflowID: run.DatabaseID, Attempt: 2,
			Event: "push", HeadSHA: ciWaitSelectorFixtureSHA, CreatedAt: createdAt,
			Name: run.Name, Status: run.Status, Conclusion: run.Conclusion, URL: run.URL,
		})
	}
	return ciWaitJSON(t, []map[string]any{{"total_count": len(qualified), "workflow_runs": qualified}})
}

func ciWaitSelectorRunsStub(t *testing.T, runs []ghRunSummary) string {
	t.Helper()
	return `case "$*" in
	"api repos/o/r/actions/runs?head_sha=` + ciWaitSelectorFixtureSHA + `&per_page=100 --method GET --paginate --slurp") echo '` + ciWaitSelectorRunsFixture(t, runs) + `';;
	*"--log-failed"*) exit 0;;
` + ciWaitQuietReads + `
	*) exit 97;;
esac`
}

func ciWaitSelectorRunStub(t *testing.T, status, conclusion string) string {
	t.Helper()
	run := ghRunDetail{Status: status, Conclusion: conclusion,
		URL: "https://github.com/o/r/actions/runs/42", HeadSHA: ciWaitSelectorFixtureSHA, Attempt: 2}
	return `case "$*" in
	"run view 42 --repo o/r --json status,conclusion,url,headSha,attempt") echo '` + ciWaitJSON(t, run) + `';;
	"api repos/o/r/actions/runs/42/attempts/2/jobs?per_page=100 --method GET --paginate --slurp") echo '[{"total_count":0,"jobs":[]}]';;
	"run view 42 --repo o/r --log-failed") exit 0;;
	*) exit 97;;
esac`
}

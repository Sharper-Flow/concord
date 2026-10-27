package main

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Runs keep workflows alive before their jobs register. The run and attempt
// identities also keep a rerun's old failed jobs out of the current verdict.
type ciWaitWorkflowRun struct {
	ID         int64     `json:"id"`
	WorkflowID int64     `json:"workflow_id"`
	Attempt    int       `json:"run_attempt"`
	Event      string    `json:"event"`
	HeadSHA    string    `json:"head_sha"`
	CreatedAt  time.Time `json:"created_at"`
	Name       string    `json:"name"`
	Status     string    `json:"status"`
	Conclusion string    `json:"conclusion"`
	URL        string    `json:"html_url"`
}

type ciWaitWorkflowJob struct {
	Name       string `json:"name"`
	HeadSHA    string `json:"head_sha"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	URL        string `json:"html_url"`
}

func ciWaitWorkflowRuns(ctx context.Context, state *ciWaitState, sha string) ([]ciWaitWorkflowRun, error) {
	body, err := ciWaitGH(ctx, "api", "repos/"+state.Repo+"/actions/runs?head_sha="+url.QueryEscape(sha)+"&per_page=100",
		"--method", "GET", "--paginate", "--slurp")
	if err != nil {
		return nil, err
	}
	var pages []struct {
		Total *int                `json:"total_count"`
		Runs  []ciWaitWorkflowRun `json:"workflow_runs"`
	}
	if err := ciWaitDecode(body, &pages); err != nil {
		return nil, err
	}
	var runs []ciWaitWorkflowRun
	for _, page := range pages {
		if page.Total == nil || page.Runs == nil {
			return nil, fmt.Errorf("workflow run response has no population")
		}
		runs = append(runs, page.Runs...)
	}
	if len(pages) == 0 || len(runs) < *pages[0].Total {
		return nil, fmt.Errorf("workflow run population is incomplete")
	}
	for _, run := range runs {
		if !strings.EqualFold(run.HeadSHA, sha) || run.ID <= 0 || run.WorkflowID <= 0 || run.Attempt <= 0 || run.Event == "" || run.CreatedAt.IsZero() {
			return nil, fmt.Errorf("workflow run has no qualified subject or attempt identity")
		}
	}
	return runs, nil
}

func ciWaitNewestWorkflowRuns(runs []ciWaitWorkflowRun) []ciWaitWorkflowRun {
	// Sorting first makes the representative run and reporting order stable.
	sorted := append([]ciWaitWorkflowRun(nil), runs...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if !sorted[i].CreatedAt.Equal(sorted[j].CreatedAt) {
			return sorted[i].CreatedAt.Before(sorted[j].CreatedAt)
		}
		return sorted[i].ID < sorted[j].ID
	})
	positions := make(map[struct {
		workflow int64
		event    string
	}]int)
	var newest []ciWaitWorkflowRun
	for _, run := range sorted {
		key := struct {
			workflow int64
			event    string
		}{run.WorkflowID, run.Event}
		if position, ok := positions[key]; ok {
			newest[position] = run
		} else {
			positions[key] = len(newest)
			newest = append(newest, run)
		}
	}
	return newest
}

func ciWaitWorkflowJobs(ctx context.Context, state *ciWaitState, run ciWaitWorkflowRun) ([]ciWaitCheck, error) {
	endpoint := fmt.Sprintf("repos/%s/actions/runs/%d/attempts/%d/jobs?per_page=100", state.Repo, run.ID, run.Attempt)
	body, err := ciWaitGH(ctx, "api", endpoint, "--method", "GET", "--paginate", "--slurp")
	if err != nil {
		return nil, err
	}
	var pages []struct {
		Total *int                `json:"total_count"`
		Jobs  []ciWaitWorkflowJob `json:"jobs"`
	}
	if err := ciWaitDecode(body, &pages); err != nil {
		return nil, err
	}
	var population []ciWaitCheck
	for _, page := range pages {
		if page.Total == nil || page.Jobs == nil {
			return nil, fmt.Errorf("workflow job response has no population")
		}
		for _, job := range page.Jobs {
			if !strings.EqualFold(job.HeadSHA, run.HeadSHA) {
				return nil, fmt.Errorf("workflow job subject differs from the selected run")
			}
			population = append(population, ciWaitWorkflowCheck(job.Name, job.URL, job.Status, job.Conclusion))
		}
	}
	if len(pages) == 0 || len(population) < *pages[0].Total {
		return nil, fmt.Errorf("workflow job population is incomplete")
	}
	return population, nil
}

func ciWaitWorkflowCheck(name, link, status, conclusion string) ciWaitCheck {
	check := ciWaitCheck{Name: name, URL: link, Bucket: ciWaitRunBucket(ghRunSummary{Status: status, Conclusion: conclusion})}
	if conclusion == "cancelled" && check.Bucket == "fail" {
		check.Classification = "cancelled"
	}
	return check
}

// A known Actions run is replaced as a whole, not job by job. A queued rerun
// can have no jobs yet and must still replace every old attempt's failure.
func ciWaitWithoutWorkflowChecks(population []ciWaitCheck, runs []ciWaitWorkflowRun) []ciWaitCheck {
	known := make(map[string]bool, len(runs))
	for _, run := range runs {
		known[strconv.FormatInt(run.ID, 10)] = true
	}
	var kept []ciWaitCheck
	for _, check := range population {
		if !known[ghRunIDFromURL(check.URL)] {
			kept = append(kept, check)
		}
	}
	return kept
}

// Stop on negative evidence without reading unrelated peers. The caller's
// failure reason discloses that the returned inventory may be incomplete.
func ciWaitObserveWorkflows(ctx context.Context, state *ciWaitState, runs []ciWaitWorkflowRun, population []ciWaitCheck) ([]ciWaitCheck, error) {
	for _, run := range runs {
		bucket := ciWaitRunBucket(ghRunSummary{Status: run.Status, Conclusion: run.Conclusion})
		jobs, err := ciWaitWorkflowJobs(ctx, state, run)
		if err != nil {
			if bucket == "fail" {
				check := ciWaitWorkflowCheck(run.Name, run.URL, run.Status, run.Conclusion)
				check.DetailError = err.Error()
				return append(population, check), nil
			}
			return population, err
		}
		population = append(population, jobs...)
		_, failures := ciWaitCountPRChecks(jobs)
		// An active run is pending even if its currently registered jobs all
		// passed. A failed run without a failed job still carries negative evidence.
		if bucket == "pending" || len(jobs) == 0 || (bucket == "fail" && len(failures) == 0) {
			population = append(population, ciWaitWorkflowCheck(run.Name, run.URL, run.Status, run.Conclusion))
		}
		if len(failures) > 0 || bucket == "fail" {
			// Only completed runs can provide failed logs. An unfinished
			// workflow never holds the basic failure report behind a log read.
			if run.Status == "completed" {
				_, details := ciWaitCountPRChecks(population)
				ciWaitCollectFailureExcerpts(ctx, state, details)
				for i := range population {
					for _, detail := range details {
						if population[i].URL == detail.URL {
							population[i].FirstError = detail.FirstError
						}
					}
				}
			}
			return population, nil
		}
	}
	return population, nil
}

func ciWaitObservedFailure(state *ciWaitState, report ciWaitReport, population []ciWaitCheck) (ciWaitReport, bool) {
	counts, failures := ciWaitCountPRChecks(population)
	report.Checks = counts
	if len(failures) == 0 {
		return report, false
	}
	report.Status = "failure"
	report.Reason = "failure observed; population may be incomplete, pending peers do not delay this report"
	for _, check := range population {
		if check.DetailError != "" {
			report.Reason += "; job detail unavailable: " + check.DetailError
		}
	}
	report.Failures = failures
	report.RunURL = ciWaitFirstRunURLForPopulation(state.Repo, population)
	return report, true
}

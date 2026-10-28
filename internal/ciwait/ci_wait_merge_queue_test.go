package ciwait

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The merge-queue fixtures follow the GitHub GraphQL timeline shape: the
// newest AddedToMergeQueueEvent or RemovedFromMergeQueueEvent, whose
// beforeCommit names the merge-group commit the queue ran checks on.

const ciWaitQueueHead = "aabbccdd"

var ciWaitQueueMergeGroup = strings.Repeat("c", 40)

const ciWaitQueueRuns = `[{"databaseId":77,"name":"queue-gate","status":"completed","conclusion":"failure","url":"https://github.com/o/r/actions/runs/77"},{"databaseId":78,"name":"queue-title","status":"completed","conclusion":"success","url":"https://github.com/o/r/actions/runs/78"}]`

func ciWaitQueueAnswer(prState, headRefOid, node string) string {
	nodes := "[]"
	if node != "" {
		nodes = "[" + node + "]"
	}
	return `{"data":{"repository":{"pullRequest":{"state":"` + prState + `","headRefOid":"` + headRefOid + `","timelineItems":{"nodes":` + nodes + `}}}}}`
}

func ciWaitQueueRemoved(id, reason, mergeGroup string) string {
	commit := "null"
	if mergeGroup != "" {
		commit = `{"oid":"` + mergeGroup + `"}`
	}
	return `{"__typename":"RemovedFromMergeQueueEvent","id":"` + id + `","reason":"` + reason + `","beforeCommit":` + commit + `}`
}

func ciWaitQueueAdded(id string) string {
	return `{"__typename":"AddedToMergeQueueEvent","id":"` + id + `"}`
}

func ciWaitQueueState(baseline *string) *ciWaitState {
	return &ciWaitState{
		SchemaVersion:      1,
		Selector:           Selector{Kind: "pr", Value: "5"},
		Repo:               "o/r",
		BudgetSeconds:      ciWaitDefaultBudget,
		StartedAt:          time.Now().UTC(),
		DeadlineAt:         time.Now().UTC().Add(time.Hour),
		Mode:               "merge",
		HeadSHA:            ciWaitQueueHead,
		MergeQueueBaseline: baseline,
	}
}

// ciWaitPollQueueOnce runs one merge-mode poll of an open pull request whose
// rollup holds one passing check. The stub answers the merge-queue read with
// queueAnswer (a failed read when empty) and the merge-group run list with
// runList (a failed read when empty), and records every gh invocation. Any
// other gh call fails, so a merge poll that reads requiredness fails loudly.
func ciWaitPollQueueOnce(t *testing.T, state *ciWaitState, queueAnswer, runList string) (ciWaitReport, bool, string, error) {
	t.Helper()
	headJSON := ciWaitMergeHeadFixture(t, "BLOCKED", "OPEN", "", ciWaitQueueHead, []ghPRRollup{ciWaitRollupPass("verify-go")})
	return ciWaitPollQueueHead(t, state, headJSON, queueAnswer, runList)
}

// ciWaitPollQueueHead is ciWaitPollQueueOnce with a caller-supplied pr view
// body.
func ciWaitPollQueueHead(t *testing.T, state *ciWaitState, headJSON, queueAnswer, runList string) (ciWaitReport, bool, string, error) {
	t.Helper()
	capture := filepath.Join(t.TempDir(), "args")
	queueCase := "echo 'HTTP 502: Bad Gateway' >&2; exit 1"
	if queueAnswer != "" {
		queueCase = "cat <<'EOQ'\n" + queueAnswer + "\nEOQ"
	}
	runListCase := "echo 'HTTP 502: Bad Gateway' >&2; exit 1"
	if runList != "" {
		runListCase = "cat <<'EOR'\n" + runList + "\nEOR"
	}
	stub := `echo "$*" >> "` + capture + `"
case "$*" in
	*"pr view"*) cat <<'EOC'
` + headJSON + `
EOC
;;
	*"api graphql"*) ` + queueCase + `
;;
	*"run list"*) ` + runListCase + `
;;
	*"log-failed"*) printf 'job\tstep\t--- FAIL: TestQueueGate (0.1s)\n';;
	*"/actions/runs?"*) echo '[{"total_count":0,"workflow_runs":[]}]';;
	*) exit 97;;
esac`
	ghStubPath(t, ghStubDir(t, stub))
	report, terminal, err := ciWaitPollPR(context.Background(), state)
	raw, readErr := os.ReadFile(capture)
	if readErr != nil {
		t.Fatal(readErr)
	}
	return report, terminal, string(raw), err
}

func TestCiWaitPRMergeModeQueueRemovalEndsTheWait(t *testing.T) {
	added := "ADD-1"
	answer := ciWaitQueueAnswer("OPEN", ciWaitQueueHead, ciWaitQueueRemoved("REMOVED-1", "failed_checks", ciWaitQueueMergeGroup))
	report, terminal, captured, err := ciWaitPollQueueOnce(t, ciWaitQueueState(&added), answer, ciWaitQueueRuns)
	if err != nil {
		t.Fatalf("ciWaitPollPR errored: %v", err)
	}
	if !terminal || report.Status != "failure" {
		t.Fatalf("a merge-queue removal must end the merge wait as failure, got status=%s terminal=%t (report=%+v)", report.Status, terminal, report)
	}
	if !strings.Contains(report.Reason, "merge queue removed the pull request") || !strings.Contains(report.Reason, "failed_checks") {
		t.Fatalf("reason must name the removal and GitHub's removal reason, got %q", report.Reason)
	}
	if report.RunURL != "https://github.com/o/r/actions/runs/77" {
		t.Fatalf("run_url must name the failed queue run, got %q", report.RunURL)
	}
	if len(report.Failures) != 1 || report.Failures[0].Name != "queue-gate" || report.Failures[0].FirstError != "--- FAIL: TestQueueGate (0.1s)" {
		t.Fatalf("failures must name the failed merge-group run with its log excerpt, got %+v", report.Failures)
	}
	if report.HeadSHA != ciWaitQueueHead || report.MergeState != "BLOCKED" {
		t.Fatalf("the report must carry the observed head and merge state, got %+v", report)
	}
	if report.Checks == nil || report.Checks.Total != 1 || report.Checks.Passing != 1 {
		t.Fatalf("the removal report keeps checks from the head rollup, got %+v", report.Checks)
	}
	if !strings.Contains(captured, "run list --repo o/r --commit "+ciWaitQueueMergeGroup) {
		t.Fatalf("the failed queue run must come from the merge-group commit, calls=%q", captured)
	}
	if strings.Contains(captured, "pr checks") || strings.Contains(captured, "branches/") {
		t.Fatalf("merge mode reads no population or requiredness source, calls=%q", captured)
	}
}

func TestCiWaitPRMergeModeQueueRemovalPrecedesCheckFailures(t *testing.T) {
	// The head rollup holds a failed check. In UNSTABLE that failure alone
	// keeps the wait pending, and in BLOCKED it alone ends the wait. Either
	// way the queue removal is the verdict and names the queue run.
	added := "ADD-1"
	answer := ciWaitQueueAnswer("OPEN", ciWaitQueueHead, ciWaitQueueRemoved("REMOVED-1", "failed_checks", ciWaitQueueMergeGroup))
	rollup := []ghPRRollup{ciWaitRollupPass("verify-go"), ciWaitRollupFail("verify-tests", "https://github.com/o/r/actions/runs/10/job/2")}
	for _, mergeState := range []string{"UNSTABLE", "BLOCKED"} {
		t.Run(mergeState, func(t *testing.T) {
			headJSON := ciWaitMergeHeadFixture(t, mergeState, "OPEN", "", ciWaitQueueHead, rollup)
			report, terminal, _, err := ciWaitPollQueueHead(t, ciWaitQueueState(&added), headJSON, answer, ciWaitQueueRuns)
			if err != nil {
				t.Fatalf("ciWaitPollPR errored: %v", err)
			}
			if !terminal || report.Status != "failure" || !strings.Contains(report.Reason, "merge queue removed") {
				t.Fatalf("the removal must be the verdict, got status=%s terminal=%t (report=%+v)", report.Status, terminal, report)
			}
			if len(report.Failures) != 1 || report.Failures[0].Name != "queue-gate" {
				t.Fatalf("failures must name the queue run, not the head check, got %+v", report.Failures)
			}
		})
	}
}

func TestCiWaitPRMergeModeQueueEventsThatKeepTheWaitOpen(t *testing.T) {
	added := "ADD-1"
	removed := "REMOVED-1"
	none := ""
	failedRemoval := ciWaitQueueRemoved("REMOVED-2", "failed_checks", ciWaitQueueMergeGroup)
	for _, tc := range []struct {
		name     string
		baseline *string
		answer   string
	}{
		{"a removal by the merge itself", &added, ciWaitQueueAnswer("OPEN", ciWaitQueueHead, ciWaitQueueRemoved("REMOVED-2", "merged", ciWaitQueueMergeGroup))},
		{"a removal on a closed pull request", &added, ciWaitQueueAnswer("CLOSED", ciWaitQueueHead, failedRemoval)},
		{"a removal on a merged pull request", &added, ciWaitQueueAnswer("MERGED", ciWaitQueueHead, failedRemoval)},
		{"a removal after the head changed", &added, ciWaitQueueAnswer("OPEN", "eeff0011", failedRemoval)},
		{"the removal the wait started after", &removed, ciWaitQueueAnswer("OPEN", ciWaitQueueHead, ciWaitQueueRemoved("REMOVED-1", "failed_checks", ciWaitQueueMergeGroup))},
		{"a re-enqueue after a removal", &removed, ciWaitQueueAnswer("OPEN", ciWaitQueueHead, ciWaitQueueAdded("ADD-2"))},
		{"no queue event", &none, ciWaitQueueAnswer("OPEN", ciWaitQueueHead, "")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			report, terminal, captured, err := ciWaitPollQueueOnce(t, ciWaitQueueState(tc.baseline), tc.answer, ciWaitQueueRuns)
			if err != nil {
				t.Fatalf("ciWaitPollPR errored: %v", err)
			}
			if terminal || report.Status != "pending" {
				t.Fatalf("the merge wait must stay pending, got status=%s terminal=%t (report=%+v)", report.Status, terminal, report)
			}
			if strings.Contains(captured, "run list") {
				t.Fatalf("a pending poll must not name queue runs, calls=%q", captured)
			}
		})
	}
}

func TestCiWaitPRMergeModeQueueBaselineIsTheFirstRead(t *testing.T) {
	removal := ciWaitQueueAnswer("OPEN", ciWaitQueueHead, ciWaitQueueRemoved("REMOVED-1", "failed_checks", ciWaitQueueMergeGroup))

	t.Run("a removal before the first read stays pending", func(t *testing.T) {
		state := ciWaitQueueState(nil)
		report, terminal, _, err := ciWaitPollQueueOnce(t, state, removal, ciWaitQueueRuns)
		if err != nil || terminal || report.Status != "pending" {
			t.Fatalf("an older removal must not end the wait, got status=%s terminal=%t err=%v", report.Status, terminal, err)
		}
		if state.MergeQueueBaseline == nil || *state.MergeQueueBaseline != "REMOVED-1" {
			t.Fatalf("the first read must record the newest event as the baseline, got %v", state.MergeQueueBaseline)
		}
	})

	t.Run("an empty first read still counts as read", func(t *testing.T) {
		state := ciWaitQueueState(nil)
		if _, terminal, _, err := ciWaitPollQueueOnce(t, state, ciWaitQueueAnswer("OPEN", ciWaitQueueHead, ""), ""); err != nil || terminal {
			t.Fatalf("an empty timeline must stay pending, terminal=%t err=%v", terminal, err)
		}
		if state.MergeQueueBaseline == nil || *state.MergeQueueBaseline != "" {
			t.Fatalf("an empty timeline must record an empty baseline, got %v", state.MergeQueueBaseline)
		}
		// The next slice resumes from the state file. An enqueue and its
		// removal that both land between two polls still end the wait.
		encoded, err := json.Marshal(state)
		if err != nil {
			t.Fatal(err)
		}
		var resumed ciWaitState
		if err := json.Unmarshal(encoded, &resumed); err != nil {
			t.Fatal(err)
		}
		if resumed.MergeQueueBaseline == nil || *resumed.MergeQueueBaseline != "" {
			t.Fatalf("the state file must keep an empty baseline distinct from no read, got %s", encoded)
		}
		report, terminal, _, err := ciWaitPollQueueOnce(t, &resumed, removal, ciWaitQueueRuns)
		if err != nil || !terminal || report.Status != "failure" {
			t.Fatalf("a removal after an empty baseline must end the wait, got status=%s terminal=%t err=%v", report.Status, terminal, err)
		}
	})
}

func TestCiWaitPRMergeModeQueueRemovalWithoutRunDetail(t *testing.T) {
	// A manual dequeue has no failed queue run, and run naming is best-effort
	// detail: the removal stays the verdict and its reason stays named.
	added := "ADD-1"
	passingRuns := `[{"databaseId":78,"name":"queue-title","status":"completed","conclusion":"success","url":"https://github.com/o/r/actions/runs/78"}]`
	for _, tc := range []struct {
		name        string
		mergeGroup  string
		runList     string
		wantRunList bool
	}{
		{"no merge-group commit", "", ciWaitQueueRuns, false},
		{"a merge-group commit that is not a full SHA", "not-a-sha", ciWaitQueueRuns, false},
		{"a run list read that fails", ciWaitQueueMergeGroup, "", true},
		{"no failed merge-group run", ciWaitQueueMergeGroup, passingRuns, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			answer := ciWaitQueueAnswer("OPEN", ciWaitQueueHead, ciWaitQueueRemoved("REMOVED-1", "dequeued", tc.mergeGroup))
			report, terminal, captured, err := ciWaitPollQueueOnce(t, ciWaitQueueState(&added), answer, tc.runList)
			if err != nil {
				t.Fatalf("ciWaitPollPR errored: %v", err)
			}
			if !terminal || report.Status != "failure" || !strings.Contains(report.Reason, "dequeued") {
				t.Fatalf("a removal must end the wait naming its reason, got status=%s terminal=%t (report=%+v)", report.Status, terminal, report)
			}
			if len(report.Failures) != 0 {
				t.Fatalf("no failed queue run means no failure entry, got %+v", report.Failures)
			}
			if got := strings.Contains(captured, "run list"); got != tc.wantRunList {
				t.Fatalf("run list read=%t want %t, calls=%q", got, tc.wantRunList, captured)
			}
		})
	}
}

func TestCiWaitPRMergeModeQueueReadFailureIsError(t *testing.T) {
	added := "ADD-1"
	for _, tc := range []struct {
		name   string
		answer string
	}{
		{"gh fails", ""},
		{"no pull request", `{"data":{"repository":{"pullRequest":null}}}`},
		{"no repository", `{"data":{"repository":null}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, terminal, _, err := ciWaitPollQueueOnce(t, ciWaitQueueState(&added), tc.answer, ciWaitQueueRuns)
			if err == nil || terminal {
				t.Fatalf("a failed merge-queue read must be an error, got terminal=%t err=%v", terminal, err)
			}
		})
	}
}

func TestCiWaitPRMergeModeStalledQueueReadIsPending(t *testing.T) {
	headJSON := ciWaitMergeHeadFixture(t, "BLOCKED", "OPEN", "", ciWaitQueueHead, []ghPRRollup{ciWaitRollupPass("verify-go")})
	ghStubPath(t, ghStubDir(t, `case "$*" in
	*"pr view"*) cat <<'EOC'
`+headJSON+`
EOC
;;
	*"api graphql"*) sleep 500;;
	*) exit 97;;
esac`))
	saved := ciWaitCommandTimeout
	ciWaitCommandTimeout = 50 * time.Millisecond
	t.Cleanup(func() { ciWaitCommandTimeout = saved })
	code, report := runCiWaitStdin(t, ciWaitJSON(t, Request{
		Selector: &Selector{Kind: "pr", Value: "5"}, Repo: "o/r", Mode: "merge", TimeSecondsMax: ciWaitBudget(8),
	}))
	if code != 0 || report.Status != "pending" || !strings.Contains(report.Reason, "stalled") || !strings.Contains(report.Reason, "api graphql") {
		t.Fatalf("a stalled merge-queue read must be pending and name the command, got code=%d report=%+v", code, report)
	}
}

// The removal report names its failed merge-group runs in creation order,
// whatever order gh lists them in, so run_url is the earliest failed run.
func TestCiWaitPRMergeModeQueueRemovalRunURLIsIndependentOfRunOrder(t *testing.T) {
	gate := `{"databaseId":77,"name":"queue-gate","status":"completed","conclusion":"failure","url":"https://github.com/o/r/actions/runs/77","createdAt":"2026-10-18T15:30:01Z"}`
	follow := `{"databaseId":79,"name":"queue-follow","status":"completed","conclusion":"failure","url":"https://github.com/o/r/actions/runs/79","createdAt":"2026-10-18T15:36:12Z"}`
	for _, runs := range []string{"[" + gate + "," + follow + "]", "[" + follow + "," + gate + "]"} {
		added := "ADD-1"
		answer := ciWaitQueueAnswer("OPEN", ciWaitQueueHead, ciWaitQueueRemoved("REMOVED-1", "failed_checks", ciWaitQueueMergeGroup))
		report, _, _, err := ciWaitPollQueueOnce(t, ciWaitQueueState(&added), answer, runs)
		if err != nil {
			t.Fatalf("ciWaitPollPR errored: %v", err)
		}
		if report.RunURL != "https://github.com/o/r/actions/runs/77" || len(report.Failures) != 2 || report.Failures[0].Name != "queue-gate" {
			t.Fatalf("runs %s: want the earliest failed run first, got run_url=%q failures=%+v", runs, report.RunURL, report.Failures)
		}
	}
}

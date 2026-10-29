package ciwait

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestCiWaitMergeOutcomeAfterThirtyMinutes(t *testing.T) {
	for _, outcome := range []string{"merged", "dropped", "failed_check"} {
		t.Run(outcome, func(t *testing.T) {
			mergedAt, checkState, conclusion := "", "COMPLETED", "SUCCESS"
			queueEvent := `{"__typename":"AddedToMergeQueueEvent","id":"added"}`
			want := "failure"
			switch outcome {
			case "merged":
				mergedAt, want = "2026-01-01T00:00:00Z", "merged"
			case "dropped":
				queueEvent = `{"__typename":"RemovedFromMergeQueueEvent","id":"removed","reason":"manual"}`
			case "failed_check":
				conclusion = "FAILURE"
			}
			ghStubPath(t, ghStubDir(t, `case "$*" in
			*"pr view"*) echo '{"headRefOid":"aabb","state":"OPEN","mergedAt":"`+mergedAt+`","mergeStateStatus":"BLOCKED","statusCheckRollup":[{"name":"test","status":"`+checkState+`","conclusion":"`+conclusion+`"}]}';;
			*"api graphql"*) echo '{"data":{"repository":{"pullRequest":{"state":"OPEN","headRefOid":"aabb","timelineItems":{"nodes":[`+queueEvent+`]}}}}}';;
			`+ciWaitQuietReads+`esac`))
			file := filepath.Join(t.TempDir(), "wait.json")
			state, _, err := ciWaitLoadOrCreate(Request{Repo: "o/r", Selector: &Selector{Kind: "pr", Value: "1"}, Mode: "merge", StateFile: file})
			if err != nil {
				t.Fatal(err)
			}
			// Advance the carried clock, preserving the original budget and deadline.
			state.StartedAt = state.StartedAt.Add(-31 * time.Minute)
			state.DeadlineAt = state.DeadlineAt.Add(-31 * time.Minute)
			baseline := "added"
			state.MergeQueueBaseline, state.HeadSHA, state.LastSHA = &baseline, "aabb", "aabb"
			ciWaitSaveState(state, file)
			code, report := runCiWaitStdin(t, ciWaitJSON(t, Request{StateFile: file}))
			if code != 0 || report.Status != want || report.Elapsed < 1860 {
				t.Fatalf("one carried merge watch after 31 minutes: want %s, got code=%d report=%+v", want, code, report)
			}
		})
	}
}

func TestCiWaitMergeTimeoutRetainsQueueObservation(t *testing.T) {
	ghStubPath(t, ghStubDir(t, `case "$*" in
	*"api graphql"*) echo '{"data":{"repository":{"pullRequest":{"state":"OPEN","headRefOid":"aabb","mergeQueueEntry":{"state":"AWAITING_CHECKS","position":1},"timelineItems":{"nodes":[]}}}}}';;
	esac`))
	file := filepath.Join(t.TempDir(), "wait.json")
	state, _, err := ciWaitLoadOrCreate(Request{Repo: "o/r", Selector: &Selector{Kind: "pr", Value: "1"}, Mode: "merge", StateFile: file})
	if err != nil {
		t.Fatal(err)
	}
	state.HeadSHA = "aabb"
	if _, err := ciWaitMergeQueueRemoval(context.Background(), state); err != nil {
		t.Fatal(err)
	}
	state.DeadlineAt = time.Now().Add(-time.Second)
	ciWaitSaveState(state, file)
	code, report := runCiWaitStdin(t, ciWaitJSON(t, Request{StateFile: file}))
	if code != 1 || report.Status != "timeout" || report.MergeQueue == nil || !report.MergeQueue.Present || report.MergeQueue.State != "AWAITING_CHECKS" || report.MergeQueue.Position != 1 || !strings.Contains(report.Reason, "AWAITING_CHECKS at position 1") {
		t.Fatalf("timeout must name the last queue observation: code=%d report=%+v", code, report)
	}
}

func TestCiWaitModeBudgets(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	for _, tc := range []struct {
		mode    string
		ceiling int
	}{{"", 1800}, {"checks", 1800}, {"merge", 7200}} {
		t.Run("default-"+tc.mode, func(t *testing.T) {
			request := Request{Repo: "o/r", Selector: &Selector{Kind: "pr", Value: "1"}, Mode: tc.mode}
			state, _, err := ciWaitLoadOrCreate(request)
			if err != nil || state.BudgetSeconds != tc.ceiling || state.DeadlineAt.Sub(state.StartedAt) != time.Duration(tc.ceiling)*time.Second {
				t.Fatalf("default budget for %q: state=%+v err=%v", tc.mode, state, err)
			}
			for _, budget := range []int{-1, 0, 600, 1800, 1801, 7200, 7201} {
				request.TimeSecondsMax = ciWaitBudget(budget)
				state, _, err := ciWaitLoadOrCreate(request)
				if budget < 0 || budget > tc.ceiling {
					if err == nil {
						t.Fatalf("mode %q admitted out-of-bound budget %d", tc.mode, budget)
					}
				} else if err != nil || state.BudgetSeconds != budget {
					t.Fatalf("mode %q budget %d: state=%+v err=%v", tc.mode, budget, state, err)
				}
			}
		})
	}
}

func TestCiWaitMergeQueueObservationQualification(t *testing.T) {
	previous := &ciWaitQueue{Present: true, State: "QUEUED", Position: 3}
	for _, tc := range []struct {
		name, prState, head, entry string
		want                       *ciWaitQueue
	}{
		{"observed absence", "OPEN", "aabb", "null", &ciWaitQueue{}},
		{"observed entry", "OPEN", "aabb", `{"state":"AWAITING_CHECKS","position":1}`, &ciWaitQueue{Present: true, State: "AWAITING_CHECKS", Position: 1}},
		{"changed head retains last watched observation", "OPEN", "ccdd", "null", previous},
		{"closed pull request retains last open observation", "CLOSED", "aabb", "null", previous},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"data":{"repository":{"pullRequest":{"state":"` + tc.prState + `","headRefOid":"` + tc.head + `","mergeQueueEntry":` + tc.entry + `,"timelineItems":{"nodes":[]}}}}}`
			ghStubPath(t, ghStubDir(t, "echo '"+body+"'"))
			state := &ciWaitState{Repo: "o/r", Selector: Selector{Kind: "pr", Value: "1"}, HeadSHA: "aabb", LastMergeQueue: previous}
			if _, err := ciWaitMergeQueueRemoval(context.Background(), state); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(state.LastMergeQueue, tc.want) {
				t.Fatalf("queue=%+v want %+v", state.LastMergeQueue, tc.want)
			}
		})
	}
}

func TestCiWaitResumeCannotChangeModeOrDeadline(t *testing.T) {
	for _, mode := range []string{"checks", "merge"} {
		t.Run(mode, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "wait.json")
			state, _, err := ciWaitLoadOrCreate(Request{Repo: "o/r", Selector: &Selector{Kind: "pr", Value: "1"}, Mode: mode, TimeSecondsMax: ciWaitBudget(600), StateFile: file})
			if err != nil {
				t.Fatal(err)
			}
			ciWaitSaveState(state, file)
			otherMode := "merge"
			if mode == "merge" {
				otherMode = "checks"
			}
			resumed, _, err := ciWaitLoadOrCreate(Request{StateFile: file, Mode: otherMode, TimeSecondsMax: ciWaitBudget(7200)})
			if err != nil || !reflect.DeepEqual(resumed, state) {
				t.Fatalf("resume must preserve original mode and deadline: state=%+v resumed=%+v err=%v", state, resumed, err)
			}
		})
	}
}

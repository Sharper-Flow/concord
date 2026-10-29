package ciwait

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// The merge queue runs its checks on a merge-group commit, not on the pull
// request head, so a failed queue run never reaches the head's status rollup.
// A merge wait reads the queue's own record instead: the newest merge-queue
// event on the pull request timeline (CD-0232). The query document is
// constant; owner, name, and number travel as GraphQL variables.
const ciWaitMergeQueueQuery = `query($owner:String!,$name:String!,$number:Int!){repository(owner:$owner,name:$name){pullRequest(number:$number){state headRefOid mergeQueueEntry{state position} timelineItems(itemTypes:[ADDED_TO_MERGE_QUEUE_EVENT,REMOVED_FROM_MERGE_QUEUE_EVENT],last:1){nodes{__typename ... on Node{id} ... on RemovedFromMergeQueueEvent{reason beforeCommit{oid}}}}}}}`

// Nil means no qualified queue read; Present=false records an observed absence.
type ciWaitQueue struct {
	Present  bool   `json:"present"`
	State    string `json:"state,omitempty"`
	Position int    `json:"position,omitempty"`
}

type ghMergeQueueAnswer struct {
	Data struct {
		Repository *struct {
			PullRequest *ghMergeQueuePR `json:"pullRequest"`
		} `json:"repository"`
	} `json:"data"`
}

type ghMergeQueuePR struct {
	State           string `json:"state"`
	HeadRefOid      string `json:"headRefOid"`
	MergeQueueEntry *struct {
		State    string `json:"state"`
		Position int    `json:"position"`
	} `json:"mergeQueueEntry"`
	TimelineItems struct {
		Nodes []ghMergeQueueEvent `json:"nodes"`
	} `json:"timelineItems"`
}

type ghMergeQueueEvent struct {
	TypeName     string `json:"__typename"`
	ID           string `json:"id"`
	Reason       string `json:"reason"`
	BeforeCommit *struct {
		Oid string `json:"oid"`
	} `json:"beforeCommit"`
}

// ciWaitMergeQueueRemoval reads the newest merge-queue event and returns it
// when it is a removal that ends the wait. The first answer only records the
// baseline: a removal from before the wait may precede a fresh enqueue the
// wait has not observed yet, so it never ends the wait.
func ciWaitMergeQueueRemoval(ctx context.Context, state *ciWaitState) (*ghMergeQueueEvent, error) {
	owner, name, _ := strings.Cut(state.Repo, "/")
	body, err := ciWaitGH(ctx, "api", "graphql", "-f", "query="+ciWaitMergeQueueQuery,
		"-f", "owner="+owner, "-f", "name="+name, "-F", "number="+state.Selector.Value)
	if err != nil {
		return nil, err
	}
	var answer ghMergeQueueAnswer
	if err := ciWaitDecode(body, &answer); err != nil {
		return nil, err
	}
	if answer.Data.Repository == nil || answer.Data.Repository.PullRequest == nil {
		return nil, fmt.Errorf("gh api graphql returned no pull request")
	}
	pr := answer.Data.Repository.PullRequest
	if pr.HeadRefOid == state.HeadSHA && strings.EqualFold(pr.State, "OPEN") {
		queue := &ciWaitQueue{}
		if pr.MergeQueueEntry != nil {
			queue.Present, queue.State, queue.Position = true, pr.MergeQueueEntry.State, pr.MergeQueueEntry.Position
		}
		state.LastMergeQueue = queue
	}
	var newest *ghMergeQueueEvent
	if nodes := pr.TimelineItems.Nodes; len(nodes) > 0 {
		newest = &nodes[len(nodes)-1]
	}
	if state.MergeQueueBaseline == nil {
		baseline := ""
		if newest != nil {
			baseline = newest.ID
		}
		state.MergeQueueBaseline = &baseline
		return nil, nil
	}
	if newest == nil || newest.TypeName != "RemovedFromMergeQueueEvent" || newest.ID == *state.MergeQueueBaseline {
		return nil, nil
	}
	// A removal by the merge itself, or one the same answer places on a pull
	// request that is no longer open on the watched head, is not a drop: the
	// next pr view reports merged, closed, or superseded.
	if strings.EqualFold(newest.Reason, "merged") || !strings.EqualFold(pr.State, "OPEN") || pr.HeadRefOid != state.HeadSHA {
		return nil, nil
	}
	return newest, nil
}

// ciWaitQueueRemovalReport ends the wait on an established merge-queue
// removal. Naming the failed queue runs is best-effort detail, like the log
// excerpt: a missing merge-group commit, a failed run list read, or no failed
// run leaves failures empty, and the removal stays the verdict.
func ciWaitQueueRemovalReport(ctx context.Context, state *ciWaitState, report ciWaitReport, removal *ghMergeQueueEvent) ciWaitReport {
	reason := removal.Reason
	if reason == "" {
		reason = "no reason reported"
	}
	report.Status = "failure"
	report.Reason = "the merge queue removed the pull request without merging (" + reason + ")"
	if removal.BeforeCommit == nil || !ciWaitHex40(removal.BeforeCommit.Oid) {
		return report
	}
	readCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	body, err := ciWaitGH(readCtx, "run", "list", "--repo", state.Repo,
		"--commit", removal.BeforeCommit.Oid, "--json", ciWaitRunListFields)
	cancel()
	var runs []ghRunSummary
	if err != nil || json.Unmarshal(body, &runs) != nil {
		return report
	}
	ciWaitSortRuns(runs)
	for _, run := range runs {
		if ciWaitRunBucket(run) == "fail" {
			report.Failures = append(report.Failures, ciWaitRunFailure(run))
		}
	}
	if len(report.Failures) > 0 {
		report.RunURL = report.Failures[0].URL
		ciWaitCollectFailureExcerpts(ctx, state, report.Failures)
	}
	return report
}

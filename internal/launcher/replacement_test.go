package launcher

import (
	"context"
	"errors"
	"testing"
)

func TestReplacementCandidateOrderUsesPinsMRUThenRank(t *testing.T) {
	input := []Candidate{
		{ID: "ranked", Rank: 1},
		{ID: "recent", LastUsed: "2026-09-04T02:00:00Z", Rank: 9},
		{ID: "pinned-old", Pinned: true, LastUsed: "2026-09-04T01:00:00Z", Rank: 9},
		{ID: "pinned-new", Pinned: true, LastUsed: "2026-09-04T03:00:00Z", Rank: 9},
	}
	got := OrderCandidates(input)
	want := []string{"pinned-new", "pinned-old", "recent", "ranked"}
	for i, candidate := range got {
		if candidate.ID != want[i] {
			t.Fatalf("candidate %d = %q, want %q", i, candidate.ID, want[i])
		}
	}
	if input[0].ID != "ranked" {
		t.Fatal("candidate ordering mutated the input slice")
	}
}

func TestCandidateOrderUsesWorkTiersBeforeRecency(t *testing.T) {
	input := []Candidate{
		{ID: "ready", Lifecycle: "needed", Ready: true, UpdatedAt: "2026-09-10T03:00:00Z"},
		{ID: "changed", Lifecycle: "planning", UpdatedAt: "2026-09-10T04:00:00Z"},
		{ID: "active", Lifecycle: "in_progress", UpdatedAt: "2026-09-10T01:00:00Z"},
		{ID: "done", Lifecycle: "completed", Terminal: true, UpdatedAt: "2026-09-10T05:00:00Z"},
	}
	got := OrderCandidates(input)
	for i, want := range []string{"active", "changed", "ready", "done"} {
		if got[i].ID != want {
			t.Fatalf("candidate %d = %q, want %q", i, got[i].ID, want)
		}
	}
}

func TestOperatorPostureUsesWorkflowStep(t *testing.T) {
	for step, want := range map[string]string{"execution": "implement", "repair": "implement", "verify": "review", "research": "research", "planning": "plan", "unknown": "operator"} {
		if got := OperatorPosture(step); got != want {
			t.Fatalf("posture for %q = %q, want %q", step, got, want)
		}
	}
}

func TestReplacementCandidateFilterIsExactSubstringOnly(t *testing.T) {
	values := []Candidate{{ID: "concord", Name: "Concord"}, {ID: "project", Path: "/tmp/project"}}
	if got := FilterCandidates(values, "cord"); len(got) != 1 || got[0].ID != "concord" {
		t.Fatalf("filter result = %#v", got)
	}
	if got := FilterCandidates(values, "cncrd"); len(got) != 0 {
		t.Fatalf("typo-tolerant match entered the first build: %#v", got)
	}
}

type replacementProbePort struct {
	failed bool
}

func (p replacementProbePort) Read(context.Context, ReadRequest) (Snapshot, error) {
	if p.failed {
		return Snapshot{Coverage: "unreachable"}, errors.New("authority unavailable")
	}
	return Snapshot{Coverage: "authoritative"}, nil
}

func (p replacementProbePort) Probe(context.Context) []ProbeStatus {
	return []ProbeStatus{{Name: "vision", Reason: "unavailable"}, {Name: "lgrep", Reason: "unavailable"}}
}

func TestReplacementProbeFailureStaysInPreview(t *testing.T) {
	model := New(replacementProbePort{})
	if err := model.Enter(context.Background()); err != nil {
		t.Fatal(err)
	}
	snapshot := model.Snapshot()
	if snapshot.Coverage != "authoritative" || len(snapshot.Probes) != 2 {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	if snapshot.Probes[0].Available || snapshot.Probes[1].Available {
		t.Fatalf("failed probes were not degraded: %#v", snapshot.Probes)
	}
}

func TestReplacementCandidatePreviewCarriesLaunchContext(t *testing.T) {
	model := New(nil)
	model.RestoreSnapshot(Snapshot{Coverage: "authoritative", Candidates: []Candidate{{
		ID: "work-1", Kind: CandidateWork, Name: "Fix launcher", State: "in_progress", Blocked: true,
		Worktree: "/worktrees/work-1", Live: 2, Available: true,
	}}})
	if got := model.Snapshot().Candidates[0]; got.Worktree != "/worktrees/work-1" || got.Live != 2 || !got.Blocked {
		t.Fatalf("candidate context = %#v", got)
	}
}

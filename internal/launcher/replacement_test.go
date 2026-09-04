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
		return Snapshot{Screen: ScreenPortfolio, Coverage: "unreachable"}, errors.New("authority unavailable")
	}
	return Snapshot{Screen: ScreenPortfolio, Coverage: "authoritative"}, nil
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
	model.RestoreSnapshot(Snapshot{Screen: ScreenPortfolio, Coverage: "authoritative", Candidates: []Candidate{{
		ID: "work-1", Kind: CandidateWork, Name: "Fix launcher", State: "in_progress", Blocked: true,
		Worktree: "/worktrees/work-1", Live: 2, Available: true,
	}}})
	if got := model.Snapshot().Candidates[0]; got.Worktree != "/worktrees/work-1" || got.Live != 2 || !got.Blocked {
		t.Fatalf("candidate context = %#v", got)
	}
}

package store

import (
	"path/filepath"
	"testing"
)

// The releaseDeadOccupantByObservation unit tests pin the occupancy release
// rule: a live session holds a worktree claim only while the host observes it
// inside that worktree, or while the host cannot read where it runs.

// TestRetargetedLiveSessionReleasesStaleClaim pins the release side: the host
// observes the recorded occupant alive in a directory the worktree does not
// contain, so a work_start move has retargeted it and the stale claim
// releases.
func TestRetargetedLiveSessionReleasesStaleClaim(t *testing.T) {
	t.Parallel()
	entry := WorktreeEntry{Path: filepath.Join(t.TempDir(), "tree"), OccupantSessionRef: "ses_live"}

	observed := &[]SessionDirectory{{SessionRef: "ses_live", Directory: filepath.Join(t.TempDir(), "elsewhere")}}
	if !releaseDeadOccupantByObservation(entry, observed) {
		t.Fatal("an occupant observed alive elsewhere must release the claim")
	}

	withBystander := &[]SessionDirectory{
		{SessionRef: "ses_other", Directory: filepath.Join(t.TempDir(), "unrelated")},
		{SessionRef: "ses_live", Directory: filepath.Join(t.TempDir(), "moved")},
	}
	if !releaseDeadOccupantByObservation(entry, withBystander) {
		t.Fatal("an occupant observed alive elsewhere must release even beside other outside sessions")
	}
}

// TestOccupantInsideWorktreeKeepsClaim pins the strand-guard: a session the
// host observes inside the worktree — the recorded occupant or any other —
// keeps the claim, and an absent observation attests nothing.
func TestOccupantInsideWorktreeKeepsClaim(t *testing.T) {
	t.Parallel()
	entry := WorktreeEntry{Path: filepath.Join(t.TempDir(), "tree"), OccupantSessionRef: "ses_live"}
	for name, observed := range map[string]*[]SessionDirectory{
		"occupant at the root": &[]SessionDirectory{{SessionRef: "ses_live", Directory: entry.Path}},
		"occupant nested":      &[]SessionDirectory{{SessionRef: "ses_live", Directory: filepath.Join(entry.Path, "nested")}},
		"other session nested": &[]SessionDirectory{{SessionRef: "ses_other", Directory: filepath.Join(entry.Path, "pkg")}},
		"absent observation":   nil,
	} {
		if releaseDeadOccupantByObservation(entry, observed) {
			t.Fatalf("%s: the claim must keep", name)
		}
	}
}

// TestOccupantWithoutReadableDirectoryKeepsClaim pins the conservative side:
// the host observed the occupant but could not read where it runs, so the
// observation cannot prove the tree is empty. An unreadable directory on a
// session other than the occupant carries no such protection.
func TestOccupantWithoutReadableDirectoryKeepsClaim(t *testing.T) {
	t.Parallel()
	entry := WorktreeEntry{Path: filepath.Join(t.TempDir(), "tree"), OccupantSessionRef: "ses_live"}

	keep := &[]SessionDirectory{{SessionRef: "ses_live", Directory: ""}}
	if releaseDeadOccupantByObservation(entry, keep) {
		t.Fatal("an occupant observed with no readable directory must keep the claim")
	}

	release := &[]SessionDirectory{{SessionRef: "ses_other", Directory: ""}}
	if !releaseDeadOccupantByObservation(entry, release) {
		t.Fatal("an unreadable directory on a non-occupant session must not keep the claim")
	}
}

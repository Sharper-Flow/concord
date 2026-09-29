package store

import (
	"context"
	"os"
	"testing"
	"time"
)

// The vacate split pins three behaviors: a committed relocation request
// leaves every occupancy row standing (a refused host move must not leave a
// live session in a worktree recorded as empty); the adapter-only
// vacate-landing verb records the verified landing and releases the session's
// rows in one transaction; and a recorded work.session_vacated of the prior
// payload version still releases on fold, replayed unchanged.
// TestSessionVacateReleasesEveryWorkItemRow in
// worktree_occupancy_release_test.go carries the prior-version replay.

func vacateRequestEvent(eventID, source, destination string) Event {
	return Event{
		EventID: eventID, Kind: "work.session_vacated", SubjectType: SubjectWorkItem, SubjectID: "work-w",
		Actor: "ses-x", OccurredAt: time.Unix(50, 0).UTC(), PayloadVersion: 2,
		Payload: jsonRaw(`{"work_id":"work-w","project_id":"project-w","session_ref":"ses-x","source_directory":"` + source + `","destination_directory":"` + destination + `"}`),
	}
}

func TestSessionVacateRequestLeavesOccupancyStanding(t *testing.T) {
	t.Parallel()
	s, git, _ := worktreeFixture(t)
	req := baseClaim(git)
	req.SessionRef = "ses-x"
	if _, err := s.ClaimWorktree(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	auditWork(t, s, git, "work-b", true)
	setWorktreeOccupant(t, s, "work-b", "ses-x")

	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{
		vacateRequestEvent("vacate-request-stands", claimPath(s), "/data/repo-main"),
	}}); err != nil {
		t.Fatal(err)
	}
	if got := worktreeOccupancyByEntry(t, s, WorktreeSetID("work-w"), "project-w", "wt-op-1"); got != "ses-x" {
		t.Fatalf("the vacate request released the vacated worktree's row: %q", got)
	}
	if got := worktreeOccupancyByEntry(t, s, WorktreeSetID("work-b"), "project-w", "wt-work-b"); got != "ses-x" {
		t.Fatalf("the vacate request released a stale row on another work item: %q", got)
	}
	var version int
	var landed string
	if err := s.db.QueryRow(`SELECT payload_version, COALESCE(json_extract(payload,'$.landed_directory'),'') FROM domain_events WHERE kind='work.session_vacated' AND event_id='vacate-request-stands'`).Scan(&version, &landed); err != nil {
		t.Fatal(err)
	}
	if version != 2 || landed != "" {
		t.Fatalf("recorded request version=%d landed_directory=%q, want version 2 with no landing", version, landed)
	}
}

func TestSessionVacateReplayTargetResolvesTheCommittedRequest(t *testing.T) {
	t.Parallel()
	s, git, _ := worktreeFixture(t)
	req := baseClaim(git)
	req.SessionRef = "ses-x"
	if _, err := s.ClaimWorktree(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{
		vacateRequestEvent("vacate-replay", claimPath(s), "/data/repo-main"),
	}}); err != nil {
		t.Fatal(err)
	}
	var target SessionVacateReplayTarget
	err := s.Transact(context.Background(), func(transaction *Transaction) error {
		var resolveErr error
		target, resolveErr = ResolveSessionVacateReplayTargetTx(context.Background(), transaction, "project-w", "/data/repo-main", "ses-x")
		return resolveErr
	})
	if err != nil {
		t.Fatal(err)
	}
	if target.WorkID != "work-w" || target.ProjectID != "project-w" || target.DestinationDirectory != "/data/repo-main" || target.SourceDirectory != claimPath(s) {
		t.Fatalf("replay target=%+v", target)
	}
	err = s.Transact(context.Background(), func(transaction *Transaction) error {
		_, resolveErr := ResolveSessionVacateReplayTargetTx(context.Background(), transaction, "project-w", "/data/elsewhere", "ses-x")
		return resolveErr
	})
	if failureKind(err) != KindProjectionNotFound {
		t.Fatalf("err=%v, want projection_not_found off the committed destination", err)
	}
}

// The replay resolves a pending request only (CD-0190 D3/D4): a version 2
// request with no recorded landing after it. A request its recorded landing
// already completed, or a version 1 request that released on fold, refuses
// the replay and appends no new landing: the session holds no stale row from
// such a request, and resolving one could append a landing that releases
// rows a later claim still holds.
func TestSessionVacateReplayRefusesACompletedRequest(t *testing.T) {
	t.Parallel()
	s, git, _ := worktreeFixture(t)
	req := baseClaim(git)
	req.SessionRef = "ses-x"
	if _, err := s.ClaimWorktree(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{
		vacateRequestEvent("vacate-completes", claimPath(s), "/data/repo-main"),
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecordSessionVacateLanding(context.Background(), SessionVacateLandingRequest{
		WorkID: "work-w", SessionRef: "ses-x", LandedDirectory: "/data/repo-main", HostPID: os.Getpid(), Now: time.Unix(60, 0).UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	resolve := func(session string) error {
		return s.Transact(context.Background(), func(transaction *Transaction) error {
			_, resolveErr := ResolveSessionVacateReplayTargetTx(context.Background(), transaction, "project-w", "/data/repo-main", session)
			return resolveErr
		})
	}
	// The recorded landing completed the request, so the replay from the
	// verified destination refuses with the completed state.
	err := resolve("ses-x")
	if failure, ok := err.(*Failure); !ok || failure.Kind != KindInvalidOperation {
		t.Fatalf("err=%v, want invalid_operation for the completed request", err)
	}
	// A recorded version 1 request released on fold, so the replay refuses
	// it at the payload version.
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{{
		EventID: "vacate-v1-completed", Kind: "work.session_vacated", SubjectType: SubjectWorkItem, SubjectID: "work-w",
		Actor: "ses-old", OccurredAt: time.Unix(40, 0).UTC(), PayloadVersion: 1,
		Payload: jsonRaw(`{"work_id":"work-w","project_id":"project-w","session_ref":"ses-old","source_directory":"` + claimPath(s) + `","destination_directory":"/data/repo-main","landed_directory":"/data/repo-main"}`),
	}}}); err != nil {
		t.Fatal(err)
	}
	err = resolve("ses-old")
	if failure, ok := err.(*Failure); !ok || failure.Kind != KindInvalidOperation {
		t.Fatalf("err=%v, want invalid_operation for the version 1 request", err)
	}
	var landings int
	if err := s.db.QueryRow(`SELECT count(*) FROM domain_events WHERE kind='work.session_vacate_landed' AND subject_id='work-w'`).Scan(&landings); err != nil {
		t.Fatal(err)
	}
	if landings != 1 {
		t.Fatalf("landing events=%d, want only the one recorded landing", landings)
	}
}

func TestSessionVacateLandingReleasesRowsInOneTransaction(t *testing.T) {
	t.Parallel()
	s, git, _ := worktreeFixture(t)
	req := baseClaim(git)
	req.SessionRef = "ses-x"
	if _, err := s.ClaimWorktree(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	// The same session holds a stale row on another work item's worktree, and
	// another session holds a row on the vacated worktree that must stay.
	auditWork(t, s, git, "work-b", true)
	setWorktreeOccupant(t, s, "work-b", "ses-x")
	setWorktreeOccupant(t, s, "work-w", "ses-other")
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{
		vacateRequestEvent("vacate-request-landing", claimPath(s), "/data/repo-main"),
	}}); err != nil {
		t.Fatal(err)
	}

	// A landing outside the committed request's registered main checkout
	// records nothing and releases nothing.
	_, err := s.RecordSessionVacateLanding(context.Background(), SessionVacateLandingRequest{
		WorkID: "work-w", SessionRef: "ses-x", LandedDirectory: "/data/elsewhere", HostPID: os.Getpid(), Now: time.Unix(60, 0).UTC(),
	})
	failure, ok := err.(*Failure)
	if !ok || failure.Kind != KindProjectionNotFound {
		t.Fatalf("err=%v, want projection_not_found", err)
	}
	if got := worktreeOccupancyByEntry(t, s, WorktreeSetID("work-w"), "project-w", "wt-op-1"); got != "ses-x" {
		t.Fatalf("a refused landing released the row: %q", got)
	}

	// A landing with no committed request for this work item and session
	// refuses.
	_, err = s.RecordSessionVacateLanding(context.Background(), SessionVacateLandingRequest{
		WorkID: "work-b", SessionRef: "ses-x", LandedDirectory: "/data/repo-main", HostPID: os.Getpid(), Now: time.Unix(60, 0).UTC(),
	})
	if failure, ok := err.(*Failure); !ok || failure.Kind != KindProjectionNotFound {
		t.Fatalf("err=%v, want projection_not_found for the missing request", err)
	}

	landing, err := s.RecordSessionVacateLanding(context.Background(), SessionVacateLandingRequest{
		WorkID: "work-w", SessionRef: "ses-x", LandedDirectory: "/data/repo-main", HostPID: os.Getpid(), Now: time.Unix(60, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if landing.AlreadyRecorded || landing.DestinationDirectory != "/data/repo-main" {
		t.Fatalf("landing result=%+v", landing)
	}
	if len(landing.ReleasedSources) != 2 {
		t.Fatalf("released sources %v, want both occupied rows", landing.ReleasedSources)
	}
	if got := worktreeOccupancyByEntry(t, s, WorktreeSetID("work-w"), "project-w", "wt-op-1"); got != "ses-other" {
		t.Fatalf("vacated worktree records %q, want only the other session's row", got)
	}
	if got := worktreeOccupancyByEntry(t, s, WorktreeSetID("work-b"), "project-w", "wt-work-b"); got != "" {
		t.Fatalf("the landing left a stale row on another work item: %q", got)
	}

	// The same landing replays idempotently with no second event.
	replay, err := s.RecordSessionVacateLanding(context.Background(), SessionVacateLandingRequest{
		WorkID: "work-w", SessionRef: "ses-x", LandedDirectory: "/data/repo-main", HostPID: os.Getpid(), Now: time.Unix(65, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !replay.AlreadyRecorded {
		t.Fatalf("replay result=%+v, want already_recorded", replay)
	}
	var landings int
	if err := s.db.QueryRow(`SELECT count(*) FROM domain_events WHERE kind='work.session_vacate_landed' AND subject_id='work-w' AND json_extract(payload,'$.session_ref')='ses-x'`).Scan(&landings); err != nil {
		t.Fatal(err)
	}
	if landings != 1 {
		t.Fatalf("landing events=%d, want 1", landings)
	}
}

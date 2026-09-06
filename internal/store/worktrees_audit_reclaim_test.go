package store

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"
)

func completeAuditWork(t *testing.T, s *Store, workID string, version int64) {
	t.Helper()
	payload := `{"from":"needed","to":"completed","reason":"merged","expected_version":` + strconv.FormatInt(version, 10) + `,"resulting_version":` + strconv.FormatInt(version+1, 10) + `}`
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{{EventID: workID + "-complete", Kind: "work.transitioned", SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "operator", OccurredAt: time.Unix(30, 0).UTC(), PayloadVersion: 1, Payload: jsonRaw(payload)}}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}}); err != nil {
		t.Fatal(err)
	}
}

// A worktree that is present on disk, still claimed, and whose work item is
// terminal is the one drift class no reader needed until now: it is neither
// orphaned nor absent, so the audit never saw it, and nothing reclaimed it.
// It is exactly the shape a merged branch leaves behind.
func TestWorktreeAuditClassifiesTerminalPresentWorktrees(t *testing.T) {
	s, git, _ := worktreeFixture(t)
	ctx := context.Background()
	auditWork(t, s, git, "work-live", true)
	git.ahead["work/work-live"] = 1
	auditWork(t, s, git, "work-done", true)
	completeAuditWork(t, s, "work-done", 3)

	audit, err := s.WorktreeAudit(ctx, WorktreeAuditRequest{ProductID: "product-w", Limit: 100, Runner: git, DefaultRef: "origin/main"})
	if err != nil {
		t.Fatal(err)
	}
	byClass := auditRowsByClass(audit.Drift)
	rows := byClass[WorktreeDriftTerminalPresent]
	if len(rows) != 1 || rows[0].WorkID != "work-done" || rows[0].Lifecycle != "completed" || rows[0].RecoveryAction != WorktreeRecoveryReclaim {
		t.Fatalf("terminal-present rows=%+v", rows)
	}
	for _, row := range audit.Drift {
		if row.WorkID == "work-live" {
			t.Fatalf("live worktree reported as drift: %+v", row)
		}
	}
}

// The audit performs the one safe action it names: reclaim a terminal
// worktree, through the same gates a direct reclaim runs. Every other class
// stays report-only, and a row the gates refuse is reported typed rather
// than skipped silently.
func TestWorktreeAuditReclaimsMergedTerminalWork(t *testing.T) {
	s, git, _ := worktreeFixture(t)
	ctx := context.Background()
	auditWork(t, s, git, "work-live", true)
	git.ahead["work/work-live"] = 1
	donePath := auditWork(t, s, git, "work-done", true)
	completeAuditWork(t, s, "work-done", 3)
	dirtyPath := auditWork(t, s, git, "work-dirty", true)
	completeAuditWork(t, s, "work-dirty", 3)
	git.dirty[dirtyPath] = true

	result, err := s.WorktreeAuditReclaim(ctx, WorktreeAuditReclaimRequest{ProductID: "product-w", DefaultRef: "origin/main", PrincipalRef: "principal-1", RequestID: "audit-reclaim-1", Now: time.Unix(40, 0).UTC(), Runner: git, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	outcomes := map[string]WorktreeAuditReclaimRow{}
	for _, row := range result.Rows {
		outcomes[row.WorkID] = row
	}
	if got := outcomes["work-done"]; got.Outcome != WorktreeAuditReclaimed || got.Path != donePath {
		t.Fatalf("merged terminal worktree: %+v", got)
	}
	if got := outcomes["work-dirty"]; got.Outcome != WorktreeAuditRefused || got.RefusalKind != string(KindInvalidOperation) || !strings.Contains(got.Detail, "dirty") {
		t.Fatalf("dirty terminal worktree must be refused typed, got %+v", got)
	}
	if _, present := outcomes["work-live"]; present {
		t.Fatalf("live work must not appear in a reclaim pass: %+v", outcomes["work-live"])
	}
	if _, still := git.worktrees[donePath]; still {
		t.Fatal("native worktree was not removed")
	}
	if _, kept := git.worktrees[dirtyPath]; !kept {
		t.Fatal("refused worktree must remain")
	}
	// The reclaim landed durably: the entry is no longer active.
	var state string
	if err := s.DatabaseForTesting().QueryRow(`SELECT state FROM worktree_entries WHERE set_id=? AND project_id='project-w'`, worktreeSetPrefix+"work-done").Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state == "active" {
		t.Fatal("reclaimed entry still active")
	}
	// A second pass finds nothing to reclaim and refuses nothing new.
	again, err := s.WorktreeAuditReclaim(ctx, WorktreeAuditReclaimRequest{ProductID: "product-w", DefaultRef: "origin/main", PrincipalRef: "principal-1", RequestID: "audit-reclaim-2", Now: time.Unix(50, 0).UTC(), Runner: git, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range again.Rows {
		if row.Outcome == WorktreeAuditReclaimed {
			t.Fatalf("second pass reclaimed again: %+v", row)
		}
	}
}

// The store defines terminal once, in isTerminalLifecycle, and superseded is
// in it. Every worktree gate that spelled terminal by hand as completed or
// cancelled left a superseded item's worktree invisible to the audit and
// refused by destroy without approval. This enumerates the store's own
// terminal set: for each, a present, clean, merged worktree is classified
// terminal_present and reclaims under the pass with no approval.
func TestWorktreeAuditTreatsEveryStoreTerminalLifecycleAsTerminal(t *testing.T) {
	for _, lifecycle := range []string{"completed", "cancelled", "superseded"} {
		t.Run(lifecycle, func(t *testing.T) {
			if !isTerminalLifecycle(lifecycle) {
				t.Fatalf("fixture premise: %q is not terminal to the store", lifecycle)
			}
			s, git, _ := worktreeFixture(t)
			ctx := context.Background()
			auditWork(t, s, git, "work-done", true)
			switch lifecycle {
			case "superseded":
				auditWork(t, s, git, "work-next", true)
				if err := ApplyOperation(ctx, s, Operation{Events: []Event{workSupersededEvent("done-superseded", "work-next", "work-done", 3, 4)}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "work-done"): 3}}); err != nil {
					t.Fatal(err)
				}
			default:
				payload := `{"from":"needed","to":"` + lifecycle + `","reason":"fixture","expected_version":3,"resulting_version":4}`
				if err := ApplyOperation(ctx, s, Operation{Events: []Event{{EventID: "done-" + lifecycle, Kind: "work.transitioned", SubjectType: SubjectWorkItem, SubjectID: "work-done", Actor: "operator", OccurredAt: time.Unix(30, 0).UTC(), PayloadVersion: 1, Payload: jsonRaw(payload)}}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "work-done"): 3}}); err != nil {
					t.Fatal(err)
				}
			}
			audit, err := s.WorktreeAudit(ctx, WorktreeAuditRequest{ProductID: "product-w", Limit: 100, Runner: git, DefaultRef: "origin/main"})
			if err != nil {
				t.Fatal(err)
			}
			var classified bool
			for _, row := range audit.Drift {
				if row.WorkID == "work-done" && row.Class == WorktreeDriftTerminalPresent && row.Lifecycle == lifecycle {
					classified = true
				}
			}
			if !classified {
				t.Fatalf("%s worktree not classified terminal_present: %+v", lifecycle, audit.Drift)
			}
			result, err := s.WorktreeAuditReclaim(ctx, WorktreeAuditReclaimRequest{ProductID: "product-w", DefaultRef: "origin/main", PrincipalRef: "principal-1", RequestID: "audit-" + lifecycle, Now: time.Unix(40, 0).UTC(), Runner: git, Limit: 100})
			if err != nil {
				t.Fatal(err)
			}
			for _, row := range result.Rows {
				if row.WorkID == "work-done" && row.Outcome != WorktreeAuditReclaimed {
					t.Fatalf("%s worktree not reclaimed: %+v", lifecycle, row)
				}
			}
		})
	}
}

// A session observed inside a terminal worktree keeps the stranding gate:
// the audit must not remove the directory a live session runs in.
func TestWorktreeAuditReclaimRefusesOccupiedWorktree(t *testing.T) {
	s, git, _ := worktreeFixture(t)
	ctx := context.Background()
	donePath := auditWork(t, s, git, "work-done", true)
	completeAuditWork(t, s, "work-done", 3)

	result, err := s.WorktreeAuditReclaim(ctx, WorktreeAuditReclaimRequest{ProductID: "product-w", DefaultRef: "origin/main", PrincipalRef: "principal-1", RequestID: "audit-reclaim-occupied", Now: time.Unix(40, 0).UTC(), Runner: git, Limit: 100, ObservedSessionDirectories: []SessionDirectory{{SessionRef: "ses-1", Directory: donePath}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Rows) != 1 || result.Rows[0].Outcome != WorktreeAuditRefused || result.Rows[0].RefusalKind != string(KindWorktreeOwnershipConflict) {
		t.Fatalf("occupied worktree must be refused typed, got %+v", result.Rows)
	}
	if _, kept := git.worktrees[donePath]; !kept {
		t.Fatal("occupied worktree must remain")
	}
}

// A needed work item whose claimed worktree is present, clean, and holds no
// commit beyond the default ref is unstarted drift (CD-0118): the checkout
// cost is real, nothing a merge could lose exists, and the audit names the
// same reclaim the terminal class names. Work in flight — a dirty tree or a
// branch with commits — stays healthy, and so does work that has moved past
// needed.
func TestWorktreeAuditClassifiesUnstartedPresentWorktrees(t *testing.T) {
	s, git, _ := worktreeFixture(t)
	ctx := context.Background()
	unstartedPath := auditWork(t, s, git, "work-unstarted", true)
	auditWork(t, s, git, "work-ahead", true)
	git.ahead["work/work-ahead"] = 2
	dirtyPath := auditWork(t, s, git, "work-dirty-unstarted", true)
	git.dirty[dirtyPath] = true
	auditWork(t, s, git, "work-started", true)
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{{EventID: "work-started-begin", Kind: "work.transitioned", SubjectType: SubjectWorkItem, SubjectID: "work-started", Actor: "operator", OccurredAt: time.Unix(20, 0).UTC(), PayloadVersion: 1, Payload: jsonRaw(`{"from":"needed","to":"in_progress","reason":"started","expected_version":3,"resulting_version":4}`)}}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "work-started"): 3}}); err != nil {
		t.Fatal(err)
	}

	audit, err := s.WorktreeAudit(ctx, WorktreeAuditRequest{ProductID: "product-w", Limit: 100, Runner: git, DefaultRef: "origin/main", Now: time.Unix(80, 0).UTC()})
	if err != nil {
		t.Fatal(err)
	}
	rows := auditRowsByClass(audit.Drift)[WorktreeDriftUnstartedPresent]
	if len(rows) != 1 || rows[0].WorkID != "work-unstarted" || rows[0].Path != unstartedPath || rows[0].Lifecycle != "needed" || rows[0].ClaimState != worktreeStateVerified || rows[0].RecoveryAction != WorktreeRecoveryReclaim {
		t.Fatalf("unstarted rows=%+v", rows)
	}
	if rows[0].CommitsAhead != 0 {
		t.Fatalf("unstarted row commits_ahead=%d", rows[0].CommitsAhead)
	}
	// The claim was recorded at Unix(10) and the audit ran at Unix(80).
	if rows[0].ClaimAgeSeconds != 70 {
		t.Fatalf("unstarted row claim_age_seconds=%d", rows[0].ClaimAgeSeconds)
	}
	for _, row := range audit.Drift {
		switch row.WorkID {
		case "work-ahead", "work-dirty-unstarted", "work-started":
			t.Fatalf("work in flight or started classified as drift: %+v", row)
		}
	}
}

// The CD-0118 obligation: the audit pass reclaims an unstarted worktree
// through its own gate, refuses a row the gate refuses typed, leaves the
// work item at needed, and reclaims nothing on a second pass.
func TestWorktreeAuditReclaimsUnstartedPresentWorktrees(t *testing.T) {
	s, git, _ := worktreeFixture(t)
	ctx := context.Background()
	unstartedPath := auditWork(t, s, git, "work-unstarted", true)
	auditWork(t, s, git, "work-ahead", true)
	git.ahead["work/work-ahead"] = 1

	result, err := s.WorktreeAuditReclaim(ctx, WorktreeAuditReclaimRequest{ProductID: "product-w", DefaultRef: "origin/main", PrincipalRef: "principal-1", RequestID: "unstarted-reclaim-1", Now: time.Unix(40, 0).UTC(), Runner: git, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	outcomes := map[string]WorktreeAuditReclaimRow{}
	for _, row := range result.Rows {
		outcomes[row.WorkID] = row
	}
	if got := outcomes["work-unstarted"]; got.Outcome != WorktreeAuditReclaimed || got.Path != unstartedPath || got.Lifecycle != "needed" {
		t.Fatalf("unstarted worktree: %+v", got)
	}
	if _, present := outcomes["work-ahead"]; present {
		t.Fatalf("branch with commits beyond the default ref must not reach the pass: %+v", outcomes["work-ahead"])
	}
	if _, still := git.worktrees[unstartedPath]; still {
		t.Fatal("native unstarted worktree was not removed")
	}
	var state string
	if err := s.DatabaseForTesting().QueryRow(`SELECT state FROM worktree_entries WHERE set_id=? AND project_id='project-w'`, worktreeSetPrefix+"work-unstarted").Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state == "active" {
		t.Fatal("reclaimed unstarted entry still active")
	}
	var lifecycle string
	if err := s.DatabaseForTesting().QueryRow(`SELECT lifecycle FROM work_items WHERE id='work-unstarted'`).Scan(&lifecycle); err != nil {
		t.Fatal(err)
	}
	if lifecycle != "needed" {
		t.Fatalf("reclaim must not change the work lifecycle, got %q", lifecycle)
	}
	again, err := s.WorktreeAuditReclaim(ctx, WorktreeAuditReclaimRequest{ProductID: "product-w", DefaultRef: "origin/main", PrincipalRef: "principal-1", RequestID: "unstarted-reclaim-2", Now: time.Unix(50, 0).UTC(), Runner: git, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range again.Rows {
		if row.WorkID == "work-unstarted" && row.Outcome == WorktreeAuditReclaimed {
			t.Fatalf("second pass reclaimed again: %+v", row)
		}
	}
}

// The unstarted tier's git gate is a commit count, not tree identity: a
// branch whose tree equals the default ref's while holding commits still
// refuses, because the commits exist and are not Concord's to discard.
func TestWorktreeAuditReclaimRefusesUnstartedWorktreeWithEquivalentTree(t *testing.T) {
	s, git, _ := worktreeFixture(t)
	ctx := context.Background()
	path := auditWork(t, s, git, "work-revert-pair", true)
	git.ahead["work/work-revert-pair"] = 2
	git.content["work-revert-pair"] = git.branches["main"]

	result, err := s.WorktreeAuditReclaim(ctx, WorktreeAuditReclaimRequest{ProductID: "product-w", DefaultRef: "origin/main", PrincipalRef: "principal-1", RequestID: "unstarted-equivalent-tree", Now: time.Unix(40, 0).UTC(), Runner: git, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	// The classification itself skips the row: the branch holds commits, so
	// it is work in flight, not unstarted drift.
	for _, row := range result.Rows {
		if row.WorkID == "work-revert-pair" {
			t.Fatalf("commit-bearing branch entered the pass: %+v", row)
		}
	}
	if _, kept := git.worktrees[path]; !kept {
		t.Fatal("commit-bearing worktree must remain")
	}
}

// A session observed inside an unstarted worktree keeps the stranding gate:
// the pass must not remove the directory a live session runs in, even though
// the branch holds nothing.
func TestWorktreeAuditReclaimRefusesOccupiedUnstartedWorktree(t *testing.T) {
	s, git, _ := worktreeFixture(t)
	ctx := context.Background()
	path := auditWork(t, s, git, "work-unstarted-occupied", true)

	result, err := s.WorktreeAuditReclaim(ctx, WorktreeAuditReclaimRequest{ProductID: "product-w", DefaultRef: "origin/main", PrincipalRef: "principal-1", RequestID: "unstarted-occupied", Now: time.Unix(40, 0).UTC(), Runner: git, Limit: 100, ObservedSessionDirectories: []SessionDirectory{{SessionRef: "ses-1", Directory: path}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Rows) != 1 || result.Rows[0].Outcome != WorktreeAuditRefused || result.Rows[0].RefusalKind != string(KindWorktreeOwnershipConflict) {
		t.Fatalf("occupied unstarted worktree must be refused typed, got %+v", result.Rows)
	}
	if _, kept := git.worktrees[path]; !kept {
		t.Fatal("occupied unstarted worktree must remain")
	}
}

// The direct reclaim surface refuses the unstarted tier on work that has
// moved past needed, so a classification captured before a transition cannot
// reclaim a worktree the driver has since occupied with real work.
func TestReclaimWorktreeUnstartedTierRefusesStartedWork(t *testing.T) {
	s, git, _ := worktreeFixture(t)
	ctx := context.Background()
	auditWork(t, s, git, "work-started", true)
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{{EventID: "started-begin", Kind: "work.transitioned", SubjectType: SubjectWorkItem, SubjectID: "work-started", Actor: "operator", OccurredAt: time.Unix(20, 0).UTC(), PayloadVersion: 1, Payload: jsonRaw(`{"from":"needed","to":"in_progress","reason":"started","expected_version":3,"resulting_version":4}`)}}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "work-started"): 3}}); err != nil {
		t.Fatal(err)
	}
	_, err := s.ReclaimWorktree(ctx, WorktreeReclaimRequest{WorkID: "work-started", ProjectID: "project-w", DefaultRef: "origin/main", PrincipalRef: "principal-1", RequestID: "unstarted-started-refusal", ExpectedVersion: 4, Now: time.Unix(30, 0).UTC(), Runner: git, RequireUnstarted: true})
	if err == nil {
		t.Fatal("unstarted tier must refuse work past needed")
	}
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindInvalidTransition {
		t.Fatalf("refusal must be a typed invalid transition, got %+v", err)
	}
}

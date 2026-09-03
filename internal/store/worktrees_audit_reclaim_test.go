package store

import (
	"context"
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
	auditWork(t, s, git, "work-done", true)
	completeAuditWork(t, s, "work-done", 3)

	audit, err := s.WorktreeAudit(ctx, "product-w", 100)
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
			audit, err := s.WorktreeAudit(ctx, "product-w", 100)
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

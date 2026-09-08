package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestResolveSessionVacateTargetUsesRegisteredMainCheckout(t *testing.T) {
	s, worktreePath := realGitTiersFixture(t)
	var target SessionVacateTarget
	err := s.Transact(context.Background(), func(transaction *Transaction) error {
		var resolveErr error
		target, resolveErr = ResolveSessionVacateTargetTx(context.Background(), transaction, "project-w", worktreePath)
		return resolveErr
	})
	if err != nil {
		t.Fatal(err)
	}
	locators, err := s.ProjectLocators(context.Background(), "project-w")
	if err != nil {
		t.Fatal(err)
	}
	if target.WorkID != "work-w" || target.ProjectID != "project-w" {
		t.Fatalf("target=%+v", target)
	}
	if target.SourceDirectory != filepath.Clean(worktreePath) {
		t.Fatalf("source=%q, want %q", target.SourceDirectory, filepath.Clean(worktreePath))
	}
	if len(locators) != 1 || target.DestinationDirectory != locators[0].NormalizedValue {
		t.Fatalf("target=%+v locators=%+v", target, locators)
	}
}

func TestResolveSessionVacateTargetRefusesRegisteredMainCheckout(t *testing.T) {
	s, _ := realGitTiersFixture(t)
	locators, err := s.ProjectLocators(context.Background(), "project-w")
	if err != nil {
		t.Fatal(err)
	}
	err = s.Transact(context.Background(), func(transaction *Transaction) error {
		_, resolveErr := ResolveSessionVacateTargetTx(context.Background(), transaction, "project-w", locators[0].NormalizedValue)
		return resolveErr
	})
	if failureKind(err) != KindProjectionNotFound {
		t.Fatalf("err=%v, want linked-worktree refusal", err)
	}
}

func TestVacateThenReclaimPassesOccupancyGate(t *testing.T) {
	s, worktreePath := realGitTiersFixture(t)
	ctx := context.Background()
	var target SessionVacateTarget
	if err := s.Transact(ctx, func(transaction *Transaction) error {
		var err error
		target, err = ResolveSessionVacateTargetTx(ctx, transaction, "project-w", worktreePath)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	entry, err := s.ReclaimWorktree(ctx, WorktreeReclaimRequest{
		WorkID: "work-w", ProjectID: "project-w", DefaultRef: "main",
		PrincipalRef: "principal-1", RequestID: "reclaim-after-vacate",
		ExpectedVersion: 3, Now: time.Unix(20, 0).UTC(),
		ObservedSessionDirectories: []SessionDirectory{{SessionRef: "session-1", Directory: target.DestinationDirectory}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if entry.State != worktreeEntryReclaimed {
		t.Fatalf("entry=%+v, want reclaimed worktree", entry)
	}
}

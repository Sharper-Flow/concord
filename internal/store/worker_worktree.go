package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"sort"
)

func validateWorkerDispatchWorktree(ctx context.Context, q queryer, workID, sessionWorktree string) error {
	if workID == "" || sessionWorktree == "" {
		return newFailure(KindUnauthorizedDispatch, "worker_dispatch", "worker dispatch requires a work item and host session worktree", false, "refresh the host session boundary")
	}
	entries, err := worktreeEntriesCore(ctx, q, workID)
	if err != nil {
		return err
	}
	active := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.State != worktreeEntryActive {
			continue
		}
		canonical, canonicalErr := canonicalWorkerWorktreePath(entry.Path)
		if canonicalErr != nil {
			return newFailure(KindUnavailable, "worker_dispatch", "active worktree identity cannot be resolved", true, "verify the active worktree claim")
		}
		active = append(active, canonical)
	}
	if len(active) == 0 {
		return newFailure(KindUnauthorizedDispatch, "worker_dispatch", "worker dispatch requires an active worktree claim", false, "claim the work item's worktree before dispatch")
	}
	session, err := canonicalWorkerWorktreePath(sessionWorktree)
	if err != nil {
		return newFailure(KindUnauthorizedDispatch, "worker_dispatch", "host session worktree identity cannot be resolved", false, "refresh the host session boundary")
	}
	for _, expected := range active {
		if expected == session {
			return nil
		}
	}
	return workerWorktreeMismatchFailure(active, workerWorktreeIdentity(session))
}

func validateWorkerDispatchWorktreeIdentity(ctx context.Context, q queryer, workID, expectedIdentity string) error {
	if workID == "" || expectedIdentity == "" {
		return newFailure(KindUnauthorizedDispatch, "worker_dispatch", "worker dispatch is missing its claimed worktree identity", false, "open a new dispatch from the claimed worktree")
	}
	entries, err := worktreeEntriesCore(ctx, q, workID)
	if err != nil {
		return err
	}
	active := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.State != worktreeEntryActive {
			continue
		}
		canonical, canonicalErr := canonicalWorkerWorktreePath(entry.Path)
		if canonicalErr != nil {
			return newFailure(KindUnavailable, "worker_dispatch", "active worktree identity cannot be resolved", true, "verify the active worktree claim")
		}
		active = append(active, canonical)
		if workerWorktreeIdentity(canonical) == expectedIdentity {
			return nil
		}
	}
	if len(active) == 0 {
		return newFailure(KindUnauthorizedDispatch, "worker_dispatch", "the dispatch worktree claim is no longer active", false, "claim the work item's worktree before recording worker evidence")
	}
	return workerWorktreeMismatchFailure(active, expectedIdentity)
}

func workerWorktreeMismatchFailure(active []string, sessionIdentity string) error {
	sort.Strings(active)
	identities := make([]string, len(active))
	for i, expected := range active {
		identities[i] = workerWorktreeIdentity(expected)
	}
	return newFailure(KindUnauthorizedDispatch, "worker_dispatch",
		fmt.Sprintf("host session worktree does not match the active claim (expected %s, session boundary %s)", joinWorkerWorktreeIdentities(identities), sessionIdentity),
		false, "move the host session to the claimed worktree")
}

func canonicalWorkerWorktreePath(value string) (string, error) {
	absolute, err := filepath.Abs(value)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", err
	}
	return filepath.Clean(resolved), nil
}

func workerWorktreeIdentity(path string) string {
	sum := sha256.Sum256([]byte(path))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func joinWorkerWorktreeIdentities(values []string) string {
	if len(values) == 1 {
		return values[0]
	}
	result := "["
	for i, value := range values {
		if i > 0 {
			result += ","
		}
		result += value
	}
	return result + "]"
}

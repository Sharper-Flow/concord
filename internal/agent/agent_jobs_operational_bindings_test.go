package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

// This file implements the TS1 operational scenarios bound for the #162
// tranche. AJ8 is the "execute ops" job, and the accepted mutation contract
// (docs/agent-mutation-tool-contract.md section 5) is explicit that native
// execution, rollback, and reclamation are deliberately not claimed as Concord
// mutations: the native authority performs and proves the real operation while
// Concord records intent, authority, and evidence. The reclamation scenario is
// the one AJ8 case whose Concord-visible half is fully built, so it binds here;
// the other three name capabilities that do not exist and are tracked
// separately rather than approximated.

// worktreeReclamationRepo builds a real git repository whose branch is merged
// into the default ref and whose tree is clean — the ground truth the scenario
// declares. ExecGitRunner runs against this repository, so the probes under
// test are real git probes rather than stubs.
func worktreeReclamationRepo(t *testing.T) (string, string) {
	t.Helper()
	repoRoot := t.TempDir()
	gitRun(t, repoRoot, "init", "-b", "main")
	gitRun(t, repoRoot, "config", "user.email", "concord@example.invalid")
	gitRun(t, repoRoot, "config", "user.name", "Concord Reclamation Test")
	if err := os.WriteFile(filepath.Join(repoRoot, "README.md"), []byte("# reclamation fixture\n"), 0o644); err != nil {
		t.Fatalf("write base file: %v", err)
	}
	gitRun(t, repoRoot, "add", "README.md")
	gitRun(t, repoRoot, "commit", "-m", "reclamation base")
	return repoRoot, gitRun(t, repoRoot, "rev-parse", "HEAD")
}

func workItemVersion(t *testing.T, s *store.Store, workID string) int64 {
	t.Helper()
	var version int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM work_items WHERE id=?`, workID).Scan(&version); err != nil {
		t.Fatalf("read version for %s: %v", workID, err)
	}
	return version
}

func workEventCount(t *testing.T, s *store.Store, workID string) int {
	t.Helper()
	count := 0
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id=?`, workID).Scan(&count); err != nil {
		t.Fatalf("count events for %s: %v", workID, err)
	}
	return count
}

// AJ8-ground-truth-reclamation: work-done is complete, its branch is merged and
// its tree is clean, but the Concord projection still records the worktree as
// active. Git is the stronger authority for whether the native resource may go,
// so reclamation proceeds from git facts and the stale projection never blocks
// it. Reclaiming the native resource must not cost the durable history.
func bindAJ8GroundTruthReclamation(t *testing.T, sc jobScenario) jobObservation {
	t.Helper()
	ctx := context.Background()
	s, service, grant, _, _ := agentJobsMutationPM1Fixture(t)

	gitState, _ := sc.InitialState["git"].(map[string]any)
	merged, _ := gitState["merged"].(bool)
	clean, _ := gitState["clean"].(bool)
	if !merged || !clean {
		t.Fatalf("AJ8-ground-truth-reclamation expects merged and clean git state, got %+v", gitState)
	}
	projection, _ := sc.InitialState["projection"].(map[string]any)
	if status, _ := projection["status"].(string); status != "active" {
		t.Fatalf("AJ8-ground-truth-reclamation expects an active projection, got %+v", projection)
	}

	repoRoot, baseSHA := worktreeReclamationRepo(t)
	projectVersion := int64(0)
	if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM projects WHERE id=?`, "proj-web").Scan(&projectVersion); err != nil {
		t.Fatalf("read proj-web version: %v", err)
	}
	if err := s.AddProjectLocator(ctx, "proj-web", store.ProjectLocator{ID: "reclamation-path", Kind: store.LocatorCanonicalPath, Value: repoRoot}, projectVersion); err != nil {
		t.Fatalf("add canonical path locator: %v", err)
	}

	env := agentJobsMutationEnvelope(t, s, grant, "proj-web", "prod-alpha")
	worktreePath := filepath.Join(t.TempDir(), "work-done-wt")

	// History that must survive the reclamation: PM1 seeds work-done with its
	// full created → in_progress → completed lifecycle.
	historyBefore := workEventCount(t, s, "work-done")
	if historyBefore == 0 {
		t.Fatal("PM1 seeded no history for work-done")
	}

	claimInput, _ := json.Marshal(map[string]any{
		"work_id": "work-done", "project_id": "proj-web",
		"branch": "work/reclaim-done", "base_sha": baseSHA, "path": worktreePath,
		"expected_version": workItemVersion(t, s, "work-done"), "idempotency_key": "aj8-reclaim-claim",
	})
	claim := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "worktree_claim", Input: claimInput}, env)
	if claim.Outcome != OutcomeOK {
		t.Fatalf("worktree claim failed outcome=%s err=%+v", claim.Outcome, claim.Error)
	}

	// Make the branch genuinely merged: commit inside the linked worktree, then
	// merge it into the default ref. The tree is left clean.
	if err := os.WriteFile(filepath.Join(worktreePath, "done.md"), []byte("# completed\n"), 0o644); err != nil {
		t.Fatalf("write worktree file: %v", err)
	}
	gitRun(t, worktreePath, "add", "done.md")
	gitRun(t, worktreePath, "commit", "-m", "work-done change")
	gitRun(t, repoRoot, "merge", "--ff-only", "work/reclaim-done")
	if dirty := gitRun(t, worktreePath, "status", "--porcelain"); dirty != "" {
		t.Fatalf("worktree is not clean before reclamation: %q", dirty)
	}

	// The projection is stale by construction: git has merged the branch while
	// Concord still records the worktree as active.
	before, err := s.WorktreeEntries(ctx, "work-done")
	if err != nil || len(before) != 1 || before[0].State != "active" {
		t.Fatalf("expected one active worktree entry before reclamation, got %+v err=%v", before, err)
	}

	reclaimInput, _ := json.Marshal(map[string]any{
		"work_id": "work-done", "project_id": "proj-web", "default_ref": "main",
		"expected_version": workItemVersion(t, s, "work-done"), "idempotency_key": "aj8-reclaim-1",
	})
	resp := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "worktree_reclaim", Input: reclaimInput}, env)
	if resp.Outcome != OutcomeOK {
		t.Fatalf("reclamation refused outcome=%s err=%+v", resp.Outcome, resp.Error)
	}

	// Ground truth: the native worktree is gone and the projection agrees.
	after, err := s.WorktreeEntries(ctx, "work-done")
	if err != nil || len(after) != 1 || after[0].State != "reclaimed" {
		t.Fatalf("worktree entry after reclamation=%+v err=%v", after, err)
	}
	if strings.Contains(gitRun(t, repoRoot, "worktree", "list"), "work-done-wt") {
		t.Fatal("native worktree still present after reclamation")
	}
	if _, statErr := os.Stat(worktreePath); !os.IsNotExist(statErr) {
		t.Fatalf("worktree path still exists after reclamation: %v", statErr)
	}
	if len(after[0].GitFacts) == 0 || string(after[0].GitFacts) == "null" {
		t.Fatalf("reclamation recorded no git facts: %q", string(after[0].GitFacts))
	}

	// Reclaiming a native resource must never cost durable history.
	historyAfter := workEventCount(t, s, "work-done")
	if historyAfter < historyBefore {
		t.Fatalf("reclamation dropped history: %d events before, %d after", historyBefore, historyAfter)
	}
	lifecycle, _ := readWorkFromStore(t, s, "work-done")
	if lifecycle != "completed" {
		t.Fatalf("work-done lifecycle after reclamation=%q, want completed", lifecycle)
	}

	obs := envelopeToObservation(resp)
	obs.State = map[string]any{
		"worktree": map[string]any{"exists": false},
		"work": map[string]any{
			"work-done": map[string]any{"history_retained": true},
		},
	}
	obs.Communication["ground_truth_evidence"] = string(after[0].GitFacts)
	// The prohibited effect is the reclamation being blocked by the stale
	// projection. Probing it means proving the projection really was stale at
	// the moment of the call and that the call still succeeded.
	obs.Effects["blocked_by_stale_projection"] = probedAbsent{
		Evidence: "projection read active immediately before the call while git had merged the branch; the reclamation returned ok and the entry moved active -> reclaimed",
	}
	return obs
}

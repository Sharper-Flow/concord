package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

// The audit read joins durable claims and work lifecycle against the real
// worktree root, so this dispatch test drives each drift class through the
// surfaces an operator would: a tool-surface claim at the canonical locator
// path, a manual native removal, and a stray directory under the root.
func TestWorktreeAuditReadClassifiesDriftThroughToolSurface(t *testing.T) {
	ctx := context.Background()
	s, service, grant, repoRoot, _ := worktreeDispatchFixture(t)
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}

	location, err := s.LocateWorktree(ctx, "project-1", "work-1", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	claimInput, _ := json.Marshal(map[string]any{
		"work_id": "work-1", "project_id": "project-1",
		"branch": location.Branch, "base_sha": location.BaseSHA, "path": location.Path,
		"expected_version": 2, "idempotency_key": "wt-audit-claim",
	})
	claim, err := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "worktree_claim", Input: claimInput}, mutationEnvelope(grant, scopeVersion))
	if err != nil || claim.Outcome != OutcomeOK {
		t.Fatalf("claim response=%+v err=%v", claim, err)
	}

	// Manual removal: the worktree vanishes without Concord's reclaim, which
	// is exactly the drift class the audit owns.
	gitRun(t, repoRoot, "worktree", "remove", location.Path)

	// Orphan: a directory under the root with no claim behind it.
	orphanPath := filepath.Join(filepath.Dir(s.Path()), "worktrees", "project-1", "work-orphan")
	if err := os.MkdirAll(orphanPath, 0o755); err != nil {
		t.Fatal(err)
	}

	auditInput, _ := json.Marshal(map[string]any{
		"product_id": "product-1",
		"page":       map[string]any{"cursor": nil, "limit": 50},
	})
	response, err := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_browse", Operation: "worktree_audit", Input: auditInput}, mutationEnvelope(grant, scopeVersion))
	if err != nil {
		t.Fatal(err)
	}
	if response.Outcome != OutcomeOK {
		t.Fatalf("audit read failed: %+v", response.Error)
	}
	if response.QueryID != "PM1.Q16" {
		t.Fatalf("query_id=%q want PM1.Q16", response.QueryID)
	}
	var page struct {
		Root  string                `json:"root"`
		Drift []store.WorktreeDrift `json:"drift"`
	}
	if err := json.Unmarshal(response.Result, &page); err != nil {
		t.Fatal(err)
	}
	if page.Root != filepath.Join(filepath.Dir(s.Path()), "worktrees") {
		t.Fatalf("root=%q", page.Root)
	}
	byClass := map[string]store.WorktreeDrift{}
	for _, row := range page.Drift {
		if _, repeated := byClass[row.Class]; repeated {
			t.Fatalf("unexpected extra row for class %s: %+v", row.Class, row)
		}
		byClass[row.Class] = row
	}
	if len(byClass) != 3 {
		t.Fatalf("expected one row per drift class, got %+v", page.Drift)
	}

	orphan := byClass[store.WorktreeDriftOrphan]
	if orphan.ProjectID != "project-1" || orphan.WorkID != "work-orphan" || orphan.Path != orphanPath || orphan.RecoveryAction != store.WorktreeRecoveryRemoveOrphan {
		t.Fatalf("orphan row=%+v", orphan)
	}
	stale := byClass[store.WorktreeDriftStaleClaim]
	if stale.WorkID != "work-1" || stale.Path != location.Path || stale.ClaimState != "verified" || stale.RecoveryAction != store.WorktreeRecoveryReclaim {
		t.Fatalf("stale_claim row=%+v", stale)
	}
	stranded := byClass[store.WorktreeDriftStrandedNeeded]
	if stranded.WorkID != "work-1" || stranded.Path != location.Path || stranded.Lifecycle != "needed" || stranded.RecoveryAction != store.WorktreeRecoveryClaim {
		t.Fatalf("stranded_needed row=%+v", stranded)
	}
}

// A product with no drift answers with a valid empty page, not an error or a
// null array: the caller must be able to trust a clean audit.
func TestWorktreeAuditReadReportsCleanProductAsEmptyDrift(t *testing.T) {
	ctx := context.Background()
	s, service, grant, _, _ := worktreeDispatchFixture(t)
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	response, err := Dispatch(ctx, s, service, InvokeRequest{
		Tool:      "concord_work_browse",
		Operation: "worktree_audit",
		Input:     json.RawMessage(`{"product_id":"product-1","page":{"cursor":null,"limit":50}}`),
	}, mutationEnvelope(grant, scopeVersion))
	if err != nil {
		t.Fatal(err)
	}
	if response.Outcome != OutcomeOK {
		t.Fatalf("clean audit failed: %+v", response.Error)
	}
	if string(response.Result) == "" || string(response.Result) == "null" {
		t.Fatalf("result=%q", response.Result)
	}
	var page struct {
		Drift []store.WorktreeDrift `json:"drift"`
	}
	if err := json.Unmarshal(response.Result, &page); err != nil {
		t.Fatal(err)
	}
	if page.Drift == nil || len(page.Drift) != 0 {
		t.Fatalf("drift=%+v, want empty non-nil array", page.Drift)
	}
}

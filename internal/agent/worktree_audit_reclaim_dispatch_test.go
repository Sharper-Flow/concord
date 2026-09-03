package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// The audit performs the one safe action it names, through the agent
// surface against real git. A terminal worktree reclaims under the direct
// reclaim's own gates; a live one is left alone; a replay under the same
// key returns the recorded pass rather than running the audit again.
func TestWorktreeAuditReclaimDispatchReclaimsTerminalWorkOnly(t *testing.T) {
	s, _, _, second, secondGrant, _ := tiersFixture(t)
	root := filepath.Join(filepath.Dir(s.Path()), "worktrees", "project-1")
	completeWork(t, s, "work-2", 3)

	response := authorityInvoke(t, s, second, secondGrant, "concord_work_transition", "worktree_audit_reclaim", map[string]any{
		"product_id": "product-1", "default_ref": "main", "idempotency_key": "audit-reclaim-1",
	})
	if response.Outcome != OutcomeOK {
		t.Fatalf("audit reclaim response=%+v", response.Error)
	}
	if _, err := json.Marshal(response); err != nil {
		t.Fatalf("audit reclaim result does not marshal: %v", err)
	}
	var result struct {
		Rows []struct {
			WorkID  string `json:"work_id"`
			Outcome string `json:"outcome"`
		} `json:"rows"`
		ReportOnly []any `json:"report_only"`
	}
	if err := json.Unmarshal(response.Result, &result); err != nil {
		t.Fatalf("decode result: %v: %s", err, string(response.Result))
	}
	if len(result.Rows) != 1 || result.Rows[0].WorkID != "work-2" || result.Rows[0].Outcome != "reclaimed" {
		t.Fatalf("rows=%+v", result.Rows)
	}
	if _, err := os.Stat(filepath.Join(root, "work-2")); !os.IsNotExist(err) {
		t.Fatal("terminal worktree still present")
	}
	if _, err := os.Stat(filepath.Join(root, "work-1")); err != nil {
		t.Fatalf("live worktree must remain: %v", err)
	}
	if version := workVersion(t, s, "work-2"); version != 5 {
		t.Fatalf("work-2 version=%d, want the reclamation bump", version)
	}
	if response.ChangedRefs == nil || len(*response.ChangedRefs) != 1 || (*response.ChangedRefs)[0].ID != "work-2" {
		t.Fatalf("changed refs=%+v", response.ChangedRefs)
	}

	// Replay: same key, recorded result, no second pass.
	replay := authorityInvoke(t, s, second, secondGrant, "concord_work_transition", "worktree_audit_reclaim", map[string]any{
		"product_id": "product-1", "default_ref": "main", "idempotency_key": "audit-reclaim-1",
	})
	if replay.Outcome != OutcomeOK || !replay.Replayed {
		t.Fatalf("replay=%+v err=%+v", replay.Replayed, replay.Error)
	}
	if version := workVersion(t, s, "work-2"); version != 5 {
		t.Fatalf("replay moved work-2 to version %d", version)
	}
}

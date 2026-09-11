package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

// The audit performs the one safe action it names, through the agent
// surface against real git. A terminal worktree reclaims under the direct
// reclaim's own gates; a live one is left alone; a replay under the same
// key returns the recorded pass rather than running the audit again.
func TestWorktreeAuditReclaimDispatchReclaimsTerminalWorkOnly(t *testing.T) {
	s, _, _, second, secondGrant, _ := tiersFixture(t)
	root := filepath.Join(filepath.Dir(s.Path()), "worktrees", "project-1")
	completeWork(t, s, "work-2", 3)
	// work-1 stays needed, so it is live only while its branch holds work:
	// a commit beyond the default ref keeps it out of the unstarted class
	// the same pass reclaims (CD-0118).
	livePath := filepath.Join(root, "work-1")
	if err := os.WriteFile(filepath.Join(livePath, "live-work.md"), []byte("# in flight\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, livePath, "add", "live-work.md")
	gitRun(t, livePath, "-c", "user.email=fixture@example.com", "-c", "user.name=fixture", "commit", "-m", "work in flight")

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

func TestWorktreeAuditReclaimDispatchReportsMixedEffects(t *testing.T) {
	s, _, _, second, secondGrant, _ := tiersFixture(t)
	completeWork(t, s, "work-2", 3)
	workOnePath := filepath.Join(filepath.Dir(s.Path()), "worktrees", "project-1", "work-1")

	response := authorityInvoke(t, s, second, secondGrant, "concord_work_transition", "worktree_audit_reclaim", map[string]any{
		"product_id": "product-1", "default_ref": "main", "idempotency_key": "audit-reclaim-mixed",
		"observed_session_directories": []map[string]any{{"session_ref": "session-1", "directory": workOnePath}},
	})
	if response.Outcome != OutcomeOK || response.Error != nil {
		t.Fatalf("mixed audit reclaim response=%+v", response)
	}
	var result struct {
		Rows []struct {
			WorkID      string `json:"work_id"`
			Outcome     string `json:"outcome"`
			RefusalKind string `json:"refusal_kind"`
		} `json:"rows"`
	}
	if err := json.Unmarshal(response.Result, &result); err != nil {
		t.Fatal(err)
	}
	byWork := map[string]struct{ outcome, refusal string }{}
	for _, row := range result.Rows {
		byWork[row.WorkID] = struct{ outcome, refusal string }{row.Outcome, row.RefusalKind}
	}
	if byWork["work-2"].outcome != "reclaimed" {
		t.Fatalf("successful sweep row was lost: %+v", byWork)
	}
	if byWork["work-1"].outcome != "refused" || byWork["work-1"].refusal != string(store.KindWorktreeRelocationRequired) {
		t.Fatalf("refused sweep row was lost: %+v", byWork)
	}
	if response.ChangedRefs == nil || len(*response.ChangedRefs) != 1 || (*response.ChangedRefs)[0].ID != "work-2" {
		t.Fatalf("mixed sweep changed refs=%+v", response.ChangedRefs)
	}
}

// The CD-0118 route end to end: a needed work item whose claimed worktree
// is clean and holds no commit beyond the default ref reclaims through the
// agent surface, the work item stays at needed, and the schema accepts the
// row the pass returns.
func TestWorktreeAuditReclaimDispatchReclaimsUnstartedWork(t *testing.T) {
	s, _, _, second, secondGrant, _ := tiersFixture(t)
	root := filepath.Join(filepath.Dir(s.Path()), "worktrees", "project-1")
	completeWork(t, s, "work-2", 3)

	response := authorityInvoke(t, s, second, secondGrant, "concord_work_transition", "worktree_audit_reclaim", map[string]any{
		"product_id": "product-1", "default_ref": "main", "idempotency_key": "unstarted-reclaim-1",
	})
	if response.Outcome != OutcomeOK {
		t.Fatalf("audit reclaim response=%+v err=%+v", response, response.Error)
	}
	if _, err := json.Marshal(response); err != nil {
		t.Fatalf("audit reclaim result does not marshal: %v", err)
	}
	var result struct {
		Rows []struct {
			WorkID    string `json:"work_id"`
			Outcome   string `json:"outcome"`
			Lifecycle string `json:"lifecycle"`
		} `json:"rows"`
	}
	if err := json.Unmarshal(response.Result, &result); err != nil {
		t.Fatalf("decode result: %v: %s", err, string(response.Result))
	}
	unstarted := 0
	for _, row := range result.Rows {
		if row.WorkID == "work-1" {
			unstarted++
			if row.Outcome != "reclaimed" || row.Lifecycle != "needed" {
				t.Fatalf("unstarted row=%+v", row)
			}
		}
	}
	if unstarted != 1 {
		t.Fatalf("work-1 must reclaim once as needed, rows=%+v", result.Rows)
	}
	if _, err := os.Stat(filepath.Join(root, "work-1")); !os.IsNotExist(err) {
		t.Fatal("unstarted worktree still present")
	}
	if _, err := os.Stat(filepath.Join(root, "work-2")); !os.IsNotExist(err) {
		t.Fatal("terminal worktree still present")
	}
	var lifecycle string
	if err := s.DatabaseForTesting().QueryRow(`SELECT lifecycle FROM work_items WHERE id='work-1'`).Scan(&lifecycle); err != nil || lifecycle != "needed" {
		t.Fatalf("work-1 lifecycle=%q err=%v; the reclaim must not change it", lifecycle, err)
	}
}

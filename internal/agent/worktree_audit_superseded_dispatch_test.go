package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

// supersedeWork drives the predecessor to the superseded lifecycle, the one
// terminal state no agent transition can request. Supersession is atomic with
// its relation, so the fixture records the event the store folds rather than a
// lifecycle transition.
func supersedeWork(t *testing.T, s *store.Store, predecessor, successor string, version int64) {
	t.Helper()
	payload := `{"superseded":"` + predecessor + `","successor":"` + successor +
		`","reason":"fixture supersession","expected_version":` + strconv.FormatInt(version, 10) +
		`,"resulting_version":` + strconv.FormatInt(version+1, 10) + `}`
	err := store.ApplyOperation(context.Background(), s, store.Operation{Events: []store.Event{
		{EventID: predecessor + "-fixture-superseded", Kind: "work.superseded", SubjectType: store.SubjectWorkItem, SubjectID: predecessor, Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(payload)},
	}, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectWorkItem, predecessor): version}})
	if err != nil {
		t.Fatal(err)
	}
}

// A worktree on a superseded item is terminal drift the audit must classify.
// The response schema validates the whole payload, so a lifecycle the schema
// omitted did not hide one row: it refused every row in the page.
func TestWorktreeAuditReturnsSupersededDrift(t *testing.T) {
	s, service, grant, _, _, _ := tiersFixture(t)
	supersedeWork(t, s, "work-2", "work-1", 3)

	response := tiersInvoke(t, s, service, grant, "concord_work_browse", "worktree_audit", map[string]any{
		"product_id": "product-1",
		"page":       map[string]any{"cursor": nil, "limit": 50},
	})
	if response.Outcome != OutcomeOK {
		t.Fatalf("audit response=%+v err=%+v", response, response.Error)
	}
	var page struct {
		Drift []store.WorktreeDrift `json:"drift"`
	}
	if err := json.Unmarshal(response.Result, &page); err != nil {
		t.Fatal(err)
	}
	var terminal *store.WorktreeDrift
	for i, row := range page.Drift {
		if row.Class == store.WorktreeDriftTerminalPresent {
			terminal = &page.Drift[i]
		}
	}
	if terminal == nil {
		t.Fatalf("no terminal_present row in %+v", page.Drift)
	}
	if terminal.WorkID != "work-2" || terminal.Lifecycle != "superseded" {
		t.Fatalf("terminal row=%+v, want work-2 on the superseded lifecycle", *terminal)
	}
}

// The reclaim answers with both arrays populated and both carrying a
// lifecycle: the superseded worktree in rows, a stranded needed worktree in
// report_only. One hand-spelled lifecycle in either array refuses the whole
// response, so both must reach the same def.
func TestWorktreeAuditReclaimReturnsSupersededRowsAndReportOnly(t *testing.T) {
	s, _, _, second, secondGrant, repoRoot := tiersFixture(t)
	root := filepath.Join(filepath.Dir(s.Path()), "worktrees", "project-1")
	supersedeWork(t, s, "work-2", "work-1", 3)

	// A native removal behind Concord's back strands the needed work item,
	// which is the report_only class that carries a lifecycle of its own.
	gitRun(t, repoRoot, "worktree", "remove", filepath.Join(root, "work-1"))

	response := tiersInvoke(t, s, second, secondGrant, "concord_work_transition", "worktree_audit_reclaim", map[string]any{
		"product_id": "product-1", "default_ref": "main", "idempotency_key": "audit-reclaim-superseded",
	})
	if response.Outcome != OutcomeOK {
		t.Fatalf("audit reclaim response=%+v err=%+v", response, response.Error)
	}
	var result struct {
		Rows []struct {
			WorkID    string `json:"work_id"`
			Lifecycle string `json:"lifecycle"`
			Outcome   string `json:"outcome"`
		} `json:"rows"`
		ReportOnly []store.WorktreeDrift `json:"report_only"`
	}
	if err := json.Unmarshal(response.Result, &result); err != nil {
		t.Fatalf("decode result: %v: %s", err, string(response.Result))
	}
	if len(result.Rows) != 1 || result.Rows[0].WorkID != "work-2" || result.Rows[0].Lifecycle != "superseded" {
		t.Fatalf("rows=%+v, want the superseded worktree", result.Rows)
	}
	var stranded *store.WorktreeDrift
	for i, row := range result.ReportOnly {
		if row.Class == store.WorktreeDriftStrandedNeeded {
			stranded = &result.ReportOnly[i]
		}
	}
	if stranded == nil || stranded.Lifecycle != "needed" {
		t.Fatalf("report_only=%+v, want a stranded_needed row carrying its lifecycle", result.ReportOnly)
	}
	if _, err := os.Stat(filepath.Join(root, "work-2")); !os.IsNotExist(err) {
		t.Fatal("superseded worktree still present")
	}
}

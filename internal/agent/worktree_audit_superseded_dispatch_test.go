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

// A reclaimed work item that carries a workflow instance has a readable work
// pin, so the CD-0113 enrichment stamps work_pins onto the audit-reclaim
// result. The closed result schema must declare that member, or every sweep
// that reclaims one such item refuses its own answer after the core commits.
func TestWorktreeAuditReclaimResultCarriesWorkPins(t *testing.T) {
	s, _, _, second, secondGrant, _ := tiersFixture(t)
	root := filepath.Join(filepath.Dir(s.Path()), "worktrees", "project-1")
	// work-1 stays needed with in-flight work on its branch, so the same
	// sweep never probes it as drift.
	livePath := filepath.Join(root, "work-1")
	if err := os.WriteFile(filepath.Join(livePath, "live-work.md"), []byte("# in flight\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, livePath, "add", "live-work.md")
	gitRun(t, livePath, "-c", "user.email=fixture@example.com", "-c", "user.name=fixture", "commit", "-m", "work in flight")
	definition := store.BuiltinWorkflowDefinitions()[0]
	registered, err := store.BuiltinWorkflowRegistry().Register(definition)
	if err != nil {
		t.Fatal(err)
	}
	var version int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM work_items WHERE id='work-2'`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err := s.Transact(context.Background(), func(tx *store.Transaction) error {
		return store.InitializeWorkflowTx(context.Background(), tx, store.WorkflowInitializationRequest{WorkID: "work-2", Definition: registered, Actor: store.WorkflowActor{PrincipalRef: "principal/fixture", ClientRef: "client/fixture", AgentRef: "agent/fixture", SessionRef: "session/fixture", ActorClass: store.ActorAgent}, Now: fixedTime()})
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM work_items WHERE id='work-2'`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	supersedeWork(t, s, "work-2", "work-1", version)

	response := tiersInvoke(t, s, second, secondGrant, "concord_work_transition", "worktree_audit_reclaim", map[string]any{
		"product_id": "product-1", "default_ref": "main", "idempotency_key": "audit-reclaim-work-pins",
	})
	if response.Outcome != OutcomeOK {
		t.Fatalf("audit reclaim response=%+v err=%+v", response, response.Error)
	}
	var result struct {
		Rows []struct {
			WorkID string `json:"work_id"`
		} `json:"rows"`
		WorkPins []store.WorkPin `json:"work_pins"`
	}
	if err := json.Unmarshal(response.Result, &result); err != nil {
		t.Fatalf("decode result: %v: %s", err, string(response.Result))
	}
	if len(result.Rows) != 1 || result.Rows[0].WorkID != "work-2" {
		t.Fatalf("rows=%+v, want the superseded worktree reclaimed", result.Rows)
	}
	if len(result.WorkPins) != 1 || result.WorkPins[0].WorkID != "work-2" || result.WorkPins[0].Lifecycle != "superseded" {
		t.Fatalf("work pins=%+v, want the reclaimed item's superseded pin", result.WorkPins)
	}
}

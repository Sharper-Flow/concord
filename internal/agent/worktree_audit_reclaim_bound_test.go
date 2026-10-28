package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/store"
)

// auditReclaimTerminalFixture claims, completes, and vacates count worktrees.
// A managed item carries a workflow pin; a long title makes that pin large.
func auditReclaimTerminalFixture(t *testing.T, count int, managed bool, title string) (*store.Store, *Service, Authority, string) {
	t.Helper()
	s, service, grant, repoRoot := tiersRepoFixture(t)
	baseSHA := gitRun(t, repoRoot, "rev-parse", "HEAD")
	ctx := context.Background()
	events := make([]store.Event, 0, count*2)
	versions := map[store.SubjectRef]int64{
		store.VersionRef(store.SubjectProduct, "product-1"): 0,
		store.VersionRef(store.SubjectProject, "project-1"): 0,
	}
	for i := 1; i <= count; i++ {
		workID := fmt.Sprintf("work-%02d", i)
		events = append(events,
			store.Event{EventID: "audit-bound-work-" + workID, Kind: "work.created", SubjectType: store.SubjectWorkItem, SubjectID: workID, Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: store.CurrentEventPayloadVersion("work.created"), Payload: json.RawMessage(`{"work_kind":"task","title":"` + title + `","priority":1}`)},
			store.Event{EventID: "audit-bound-membership-" + workID, Kind: "work.memberships_replaced", SubjectType: store.SubjectWorkItem, SubjectID: workID, Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"memberships":[{"project_id":"project-1","role":"primary"}],"expected_version":1,"resulting_version":2}`)},
		)
		versions[store.VersionRef(store.SubjectWorkItem, workID)] = 0
	}
	if err := store.ApplyOperation(ctx, s, store.Operation{Events: events, ExpectedVersions: versions}); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(filepath.Dir(s.Path()), "worktrees", "project-1")
	var definition store.WorkflowDefinition
	if managed {
		definition = store.BuiltinWorkflowDefinitions()[0]
	}
	for i := 1; i <= count; i++ {
		workID := fmt.Sprintf("work-%02d", i)
		if response := tiersInvoke(t, s, service, grant, "concord_work_transition", "worktree_claim", map[string]any{"host_pid": os.Getpid(),
			"work_id": workID, "project_id": "project-1", "base_sha": baseSHA, "expected_version": 2, "idempotency_key": "audit-bound-claim-" + workID,
		}); response.Outcome != agentcontract.OutcomeOK {
			t.Fatalf("claim %s response=%+v err=%+v", workID, response, response.Error)
		}
		completeWork(t, s, workID)
		vacateLinkedWorktree(t, s, service, grant, filepath.Join(root, workID), "audit-bound-vacate-"+workID)
		if managed {
			initializeMutationPinWorkflow(t, s, workID, definition)
		}
	}
	return s, service, grant, root
}

type auditReclaimBoundResult struct {
	Rows []struct {
		WorkID  string `json:"work_id"`
		Outcome string `json:"outcome"`
	} `json:"rows"`
	ReportOnly []struct {
		WorkID string `json:"work_id"`
		Class  string `json:"class"`
	} `json:"report_only"`
	ChangedRefs []map[string]any  `json:"changed_refs"`
	WorkPins    []json.RawMessage `json:"work_pins"`
}

// requireAuditReclaimCommitsWhatItReports checks one pass: an ok envelope
// within the cap whose changed refs are exactly its reclaimed rows, whose
// reclaimed rows are gone from disk, and whose declined rows stay on disk
// as unattempted terminal_present report rows. It returns the reclaimed ids.
func requireAuditReclaimCommitsWhatItReports(t *testing.T, response agentcontract.Envelope, root string, managed bool) []string {
	t.Helper()
	if response.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("a pass that committed must report ok, got %+v", response.Error)
	}
	if err := response.Validate(); err != nil {
		t.Fatalf("committed-effect report must validate: %v", err)
	}
	type wire agentcontract.Envelope
	encoded, err := json.Marshal(wire(response))
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > agentcontract.MaxResultEnvelopeBytes {
		t.Fatalf("envelope=%d bytes, want at most %d", len(encoded), agentcontract.MaxResultEnvelopeBytes)
	}
	for _, notice := range response.Omissions {
		if notice.Kind == "changed_refs_truncated" {
			t.Fatalf("the pass must not commit more than it reports: %+v", notice)
		}
	}
	var result auditReclaimBoundResult
	if err := json.Unmarshal(response.Result, &result); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	reclaimed := []string{}
	for _, row := range result.Rows {
		if row.Outcome != "reclaimed" {
			t.Fatalf("row %s outcome=%s, want reclaimed", row.WorkID, row.Outcome)
		}
		if _, err := os.Stat(filepath.Join(root, row.WorkID)); !os.IsNotExist(err) {
			t.Fatalf("reclaimed %s is still on disk: %v", row.WorkID, err)
		}
		reclaimed = append(reclaimed, row.WorkID)
	}
	changed := derefChangedRefs(response.ChangedRefs)
	if len(changed) != len(reclaimed) || len(result.ChangedRefs) != len(reclaimed) {
		t.Fatalf("changed refs=%d payload changed_refs=%d, want exactly the %d reclaimed rows", len(changed), len(result.ChangedRefs), len(reclaimed))
	}
	if managed && len(result.WorkPins) != len(reclaimed) {
		t.Fatalf("work pins=%d, want one per reclaimed row (%d)", len(result.WorkPins), len(reclaimed))
	}
	for _, drift := range result.ReportOnly {
		if drift.Class != "terminal_present" {
			continue
		}
		if _, err := os.Stat(filepath.Join(root, drift.WorkID)); err != nil {
			t.Fatalf("declined %s must stay on disk: %v", drift.WorkID, err)
		}
	}
	return reclaimed
}

// Workflow-managed items carry a work pin each. A pass whose pins outgrow the
// result envelope commits only the rows its ok envelope reports, and a later
// pass reclaims the rest.
func TestAuditReclaimLongPinsCommitsOnlyWhatItReports(t *testing.T) {
	t.Parallel()
	const total = 20
	s, service, grant, root := auditReclaimTerminalFixture(t, total, true, strings.Repeat("t", 250))
	input := map[string]any{"product_id": "product-1", "default_ref": "main", "idempotency_key": "audit-long-pins-1", "limit": total}
	first := tiersInvoke(t, s, service, grant, "concord_work_transition", "worktree_audit_reclaim", input)
	reclaimed := requireAuditReclaimCommitsWhatItReports(t, first, root, true)
	if len(reclaimed) == 0 || len(reclaimed) == total {
		t.Fatalf("reclaimed=%d of %d, want the fixture to exceed one envelope", len(reclaimed), total)
	}
	var result auditReclaimBoundResult
	if err := json.Unmarshal(first.Result, &result); err != nil {
		t.Fatal(err)
	}
	replay := tiersInvoke(t, s, service, grant, "concord_work_transition", "worktree_audit_reclaim", input)
	if !replay.Replayed || !bytes.Equal(replay.Result, first.Result) {
		t.Fatalf("replay must return the recorded bounded result: replayed=%v", replay.Replayed)
	}
	done := len(reclaimed)
	for pass := 2; done < total; pass++ {
		if pass > total {
			t.Fatalf("passes stopped converging at %d of %d reclaimed", done, total)
		}
		input["idempotency_key"] = fmt.Sprintf("audit-long-pins-%d", pass)
		next := tiersInvoke(t, s, service, grant, "concord_work_transition", "worktree_audit_reclaim", input)
		more := requireAuditReclaimCommitsWhatItReports(t, next, root, true)
		if len(more) == 0 {
			t.Fatalf("pass %d reclaimed nothing with %d rows left", pass, total-done)
		}
		done += len(more)
	}
}

// The result envelope caps changed_refs at 32. A pass of 33 pinless terminal
// rows commits 32 and reports the 33rd unattempted, so the committed effect
// is exactly what the ok envelope reports.
func TestAuditReclaimStopsAtChangedRefBound(t *testing.T) {
	t.Parallel()
	const total = agentcontract.MaxChangedRefs + 1
	s, service, grant, root := auditReclaimTerminalFixture(t, total, false, "Audit Bound")
	input := map[string]any{"product_id": "product-1", "default_ref": "main", "idempotency_key": "audit-bound-pass-1", "limit": total}
	first := tiersInvoke(t, s, service, grant, "concord_work_transition", "worktree_audit_reclaim", input)
	if reclaimed := requireAuditReclaimCommitsWhatItReports(t, first, root, false); len(reclaimed) != agentcontract.MaxChangedRefs {
		t.Fatalf("reclaimed=%d, want the changed-ref bound %d", len(reclaimed), agentcontract.MaxChangedRefs)
	}
	var result auditReclaimBoundResult
	if err := json.Unmarshal(first.Result, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.ReportOnly) != 1 || result.ReportOnly[0].Class != "terminal_present" {
		t.Fatalf("report_only=%+v, want the one unattempted row", result.ReportOnly)
	}
	input["idempotency_key"] = "audit-bound-pass-2"
	second := tiersInvoke(t, s, service, grant, "concord_work_transition", "worktree_audit_reclaim", input)
	if reclaimed := requireAuditReclaimCommitsWhatItReports(t, second, root, false); len(reclaimed) != 1 || reclaimed[0] != result.ReportOnly[0].WorkID {
		t.Fatalf("second pass reclaimed %v, want %s", reclaimed, result.ReportOnly[0].WorkID)
	}
}

// The admission bound admits a candidate whose minimal envelope is exactly
// the cap and declines one byte more, and declines a reclaim beyond the
// changed-ref bound.
func TestAuditReclaimAdmissionBoundaryIsTheEnvelopeCap(t *testing.T) {
	t.Parallel()
	base := agentcontract.NewBase("audit-admission", "concord_work_transition", "worktree_audit_reclaim")
	intents := []agentcontract.NextIntent{{Tool: "concord_work_browse", Operation: "worktree_audit", QueryID: "PM1.Q16", ReasonCode: "audit_after_reclaim", RequiredFields: []string{"product_id"}}}
	remaining := []store.WorktreeDrift{{Class: store.WorktreeDriftTerminalPresent, ProjectID: "project-1", WorkID: "work-later", Path: "/w/work-later", Lifecycle: "completed"}}
	row := func(path string) []store.WorktreeAuditReclaimRow {
		return []store.WorktreeAuditReclaimRow{
			{ProjectID: "project-1", WorkID: "work-refused", Path: "/w/work-refused", Lifecycle: "completed", Outcome: store.WorktreeAuditRefused, RefusalKind: "invalid_transition", Detail: strings.Repeat("d", 4000)},
			{ProjectID: "project-1", WorkID: "work-1", Path: path, Lifecycle: "completed", Outcome: store.WorktreeAuditReclaimed, Version: 5},
		}
	}
	admit := auditReclaimAdmission(base, intents)
	measured := auditReclaimAdmissionBytes(base, intents, "/w", row("/w/work-1"), remaining)
	exact := "/w/work-1" + strings.Repeat("p", agentcontract.MaxResultEnvelopeBytes-measured)
	if got := auditReclaimAdmissionBytes(base, intents, "/w", row(exact), remaining); got != agentcontract.MaxResultEnvelopeBytes {
		t.Fatalf("padded measurement=%d, want exactly %d", got, agentcontract.MaxResultEnvelopeBytes)
	}
	if !admit("/w", row(exact), remaining) {
		t.Fatal("a candidate at exactly the cap must be admitted")
	}
	if admit("/w", row(exact+"p"), remaining) {
		t.Fatal("a candidate one byte past the cap must be declined")
	}
	rows := make([]store.WorktreeAuditReclaimRow, 0, agentcontract.MaxChangedRefs+1)
	for i := 0; i <= agentcontract.MaxChangedRefs; i++ {
		rows = append(rows, store.WorktreeAuditReclaimRow{ProjectID: "project-1", WorkID: fmt.Sprintf("work-%02d", i), Path: "/w", Lifecycle: "completed", Outcome: store.WorktreeAuditReclaimed, Version: 5})
	}
	if !admit("/w", rows[:agentcontract.MaxChangedRefs], nil) {
		t.Fatal("the changed-ref bound itself must be admitted")
	}
	if admit("/w", rows, nil) {
		t.Fatal("a reclaim beyond the changed-ref bound must be declined")
	}
}

// The store calls the admission bound while the row transaction holds the
// pool's one connection, so the bound must decide without a pool call. It
// returns while a transaction holds that connection.
func TestAuditReclaimAdmissionMakesNoPoolCall(t *testing.T) {
	t.Parallel()
	s, _, _, _, _, _ := tiersFixture(t)
	admit := auditReclaimAdmission(agentcontract.NewBase("audit-admission-pool", "concord_work_transition", "worktree_audit_reclaim"), nil)
	rows := []store.WorktreeAuditReclaimRow{{ProjectID: "project-1", WorkID: "work-1", Path: "/w/work-1", Lifecycle: "completed", Outcome: store.WorktreeAuditReclaimed, Version: 5}}
	if err := s.Transact(context.Background(), func(*store.Transaction) error {
		decided := make(chan bool, 1)
		go func() { decided <- admit("/w", rows, nil) }()
		select {
		case admitted := <-decided:
			if !admitted {
				t.Error("a small candidate must be admitted")
			}
		case <-time.After(5 * time.Second):
			t.Error("the admission bound waited on the pool while a transaction held its connection")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// A genuine fault after rows committed keeps its kind, marks effects
// possible, and carries exactly the committed refs.
func TestAuditReclaimPostCommitFailureCarriesCommittedRefs(t *testing.T) {
	t.Parallel()
	base := agentcontract.NewBase("audit-bound-mapping", "concord_work_transition", "worktree_audit_reclaim")
	changed := make([]agentcontract.ChangedRef, agentcontract.MaxChangedRefs)
	for i := range changed {
		changed[i] = agentcontract.ChangedRef{EntityKind: "work_item", ID: fmt.Sprintf("work-%02d", i), Version: "5"}
	}
	failure := coreError(base, "budget_refused", "requested_budget_seconds 600 exceeds supported 300", "adjust_budget", false)
	failure.Error.SupportedBudgetSeconds = 300
	mapped := auditReclaimPostCommitFailure(base, changed, failure)
	if mapped.Outcome != agentcontract.OutcomeError || mapped.Error == nil || mapped.Error.Kind != "budget_refused" {
		t.Fatalf("mapped=%+v", mapped)
	}
	if mapped.Error.EffectState != agentcontract.EffectPossible {
		t.Fatalf("effect_state=%q, want possible", mapped.Error.EffectState)
	}
	if err := mapped.Validate(); err != nil {
		t.Fatalf("post-commit failure must stay deliverable, got %v", err)
	}
	if mapped.ChangedRefs == nil || len(*mapped.ChangedRefs) != agentcontract.MaxChangedRefs || len(mapped.Omissions) != 0 {
		t.Fatalf("changed refs=%v omissions=%+v, want the %d committed refs and no notice", mapped.ChangedRefs, mapped.Omissions, agentcontract.MaxChangedRefs)
	}
}

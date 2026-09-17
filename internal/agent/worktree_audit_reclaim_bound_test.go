package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

// The result envelope caps changed_refs at 32. A bulk audit reclaim that
// reclaims more rows than that still committed every row, so its report —
// success or failure — must stay deliverable: the ref list is bounded to
// the envelope capacity and an omission notice accounts for the rest.
func TestAuditReclaimPostCommitFailureTruncatesBeyondEnvelopeCapacity(t *testing.T) {
	t.Parallel()
	base := NewBase("audit-bound-mapping", "concord_work_transition", "worktree_audit_reclaim")
	changed := make([]ChangedRef, 40)
	for i := range changed {
		changed[i] = ChangedRef{EntityKind: "work_item", ID: fmt.Sprintf("work-%02d", i), Version: "5"}
	}

	failure := coreError(base, "budget_refused", "mutation result exceeds requested max_bytes budget", "adjust_budget", false)
	failure.Error.SupportedBudgetSeconds = 300
	mapped := auditReclaimPostCommitFailure(base, changed, failure)
	if mapped.Outcome != OutcomeError || mapped.Error == nil {
		t.Fatalf("mapped=%+v", mapped)
	}
	if mapped.Error.EffectState != EffectPossible {
		t.Fatalf("effect_state=%q, want possible: all 40 rows committed", mapped.Error.EffectState)
	}
	if err := mapped.Validate(); err != nil {
		t.Fatalf("a 40-ref post-commit failure must stay deliverable, got %v", err)
	}
	if mapped.ChangedRefs == nil || len(*mapped.ChangedRefs) != 32 {
		t.Fatalf("changed refs=%d, want the envelope capacity 32", len(*mapped.ChangedRefs))
	}
	var notice *Notice
	for i := range mapped.Omissions {
		if mapped.Omissions[i].Kind == "changed_refs_truncated" {
			notice = &mapped.Omissions[i]
		}
	}
	if notice == nil {
		t.Fatalf("omissions=%+v, want a changed_refs_truncated notice", mapped.Omissions)
	}
	if notice.Count != 8 {
		t.Fatalf("notice count=%d, want the 8 dropped refs", notice.Count)
	}
	if notice.Details["committed"] != int64(40) || notice.Details["reported"] != int64(32) {
		t.Fatalf("notice details=%+v, want committed 40 and reported 32", notice.Details)
	}
}

// The full reproduction: a bulk audit reclaim over 33 terminal, vacated
// worktrees commits all 33 rows, then reports the pass. The committed
// effect must reach the caller as a valid OK envelope with the bounded ref
// list and the truncation notice — not as a destroyed report the adapter
// turns into unknown_effect.
func TestAuditReclaimBeyondChangedRefBoundReportsCommittedEffect(t *testing.T) {
	t.Parallel()
	const total = 33
	s, service, grant, repoRoot := tiersRepoFixture(t)
	baseSHA := gitRun(t, repoRoot, "rev-parse", "HEAD")

	ctx := context.Background()
	events := make([]store.Event, 0, total*2)
	versions := map[store.SubjectRef]int64{
		store.VersionRef(store.SubjectProduct, "product-1"): 0,
		store.VersionRef(store.SubjectProject, "project-1"): 0,
	}
	for i := 1; i <= total; i++ {
		workID := fmt.Sprintf("work-%02d", i)
		events = append(events,
			store.Event{EventID: "audit-bound-work-" + workID, Kind: "work.created", SubjectType: store.SubjectWorkItem, SubjectID: workID, Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 2, Payload: json.RawMessage(`{"work_kind":"task","title":"Audit Bound ` + workID + `","priority":1}`)},
			store.Event{EventID: "audit-bound-membership-" + workID, Kind: "work.memberships_replaced", SubjectType: store.SubjectWorkItem, SubjectID: workID, Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"memberships":[{"project_id":"project-1","role":"primary"}],"expected_version":1,"resulting_version":2}`)},
		)
		versions[store.VersionRef(store.SubjectWorkItem, workID)] = 0
	}
	if err := store.ApplyOperation(ctx, s, store.Operation{Events: events, ExpectedVersions: versions}); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(filepath.Dir(s.Path()), "worktrees", "project-1")
	for i := 1; i <= total; i++ {
		workID := fmt.Sprintf("work-%02d", i)
		if response := tiersInvoke(t, s, service, grant, "concord_work_transition", "worktree_claim", map[string]any{
			"work_id": workID, "project_id": "project-1", "base_sha": baseSHA, "expected_version": 2, "idempotency_key": "audit-bound-claim-" + workID,
		}); response.Outcome != OutcomeOK {
			t.Fatalf("claim %s response=%+v err=%+v", workID, response, response.Error)
		}
		completeWork(t, s, workID, 3)
		vacateLinkedWorktree(t, s, service, grant, filepath.Join(root, workID), "audit-bound-vacate-"+workID)
	}

	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	op, ok := ValidateContractOperation("concord_work_transition", "worktree_audit_reclaim")
	if !ok {
		t.Fatal("worktree_audit_reclaim operation is not registered")
	}
	raw, _ := json.Marshal(map[string]any{
		"product_id": "product-1", "default_ref": "main", "idempotency_key": "audit-bound-pass-1", "limit": total,
	})
	r := runtime{Store: s, Authority: service, Envelope: env, Tool: "concord_work_transition", Operation: "worktree_audit_reclaim", Budget: budgetInput{}, Reader: grant}
	base := NewBase("audit-bound-pass-1", "concord_work_transition", "worktree_audit_reclaim")
	response, err := r.mutateWorktreeAuditReclaim(ctx, base, raw, grant, op)
	if err != nil {
		t.Fatalf("planner err=%v", err)
	}
	if response.Outcome != OutcomeOK {
		t.Fatalf("a fully successful 33-row pass must report ok, got %+v", response.Error)
	}
	if err := response.Validate(); err != nil {
		t.Fatalf("committed-effect report must validate: %v", err)
	}
	if response.ChangedRefs == nil || len(*response.ChangedRefs) != 32 {
		t.Fatalf("changed refs=%d, want the envelope capacity 32", len(*response.ChangedRefs))
	}
	var rows struct {
		Rows []struct {
			WorkID  string `json:"work_id"`
			Outcome string `json:"outcome"`
		} `json:"rows"`
		ChangedRefs []map[string]any `json:"changed_refs"`
	}
	if err := json.Unmarshal(response.Result, &rows); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if len(rows.Rows) != total {
		t.Fatalf("rows=%d, want all %d reclaimed rows reported", len(rows.Rows), total)
	}
	for _, row := range rows.Rows {
		if row.Outcome != "reclaimed" {
			t.Fatalf("row %s outcome=%s, want reclaimed", row.WorkID, row.Outcome)
		}
	}
	if len(rows.ChangedRefs) != 32 {
		t.Fatalf("payload changed_refs=%d, want the bounded 32", len(rows.ChangedRefs))
	}
	var notice *Notice
	for i := range response.Omissions {
		if response.Omissions[i].Kind == "changed_refs_truncated" {
			notice = &response.Omissions[i]
		}
	}
	if notice == nil {
		t.Fatalf("omissions=%+v, want a changed_refs_truncated notice", response.Omissions)
	}
	if notice.Count != 1 || notice.Details["committed"] != int64(total) {
		t.Fatalf("notice=%+v, want count 1 and committed %d", notice, total)
	}
}

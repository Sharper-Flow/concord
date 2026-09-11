package agent

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
)

// Issue #757: the audit commits each reclaimed row in its own transaction,
// then builds the result. A failure after the commit (result enrichment,
// result validation, budget admission, idempotency insert) must not claim
// no effect: the reclaimed rows already applied. This fixture runs a mixed
// sweep — one row reclaimed, one row refused — under a result budget the
// payload cannot fit, so the commit happens and the budget refusal follows.
func TestWorktreeAuditReclaimPostCommitFailurePreservesCommittedRefs(t *testing.T) {
	s, _, _, second, secondGrant, _ := tiersFixture(t)
	root := filepath.Join(filepath.Dir(s.Path()), "worktrees", "project-1")
	completeWork(t, s, "work-1", 3)
	completeWork(t, s, "work-2", 3)
	ctx := context.Background()
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(secondGrant, scopeVersion)
	op, ok := ValidateContractOperation("concord_work_transition", "worktree_audit_reclaim")
	if !ok {
		t.Fatal("worktree_audit_reclaim operation is not registered")
	}
	// work-1 stays occupied, so its row refuses; work-2 reclaims.
	raw, _ := json.Marshal(map[string]any{
		"product_id": "product-1", "default_ref": "main", "idempotency_key": "audit-effect-1",
		"observed_session_directories": []map[string]any{
			{"session_ref": "ses-1", "directory": filepath.Join(root, "work-1")},
		},
	})
	r := runtime{Store: s, Authority: second, Envelope: env, Tool: "concord_work_transition", Operation: "worktree_audit_reclaim", Budget: budgetInput{MaxBytes: 1}, Reader: secondGrant}
	base := NewBase("audit-effect-1", "concord_work_transition", "worktree_audit_reclaim")
	response, err := r.mutateWorktreeAuditReclaim(ctx, base, raw, secondGrant, op)
	if err != nil {
		t.Fatalf("planner err=%v", err)
	}
	if response.Outcome != OutcomeError {
		t.Fatalf("post-commit budget refusal must be an error, got %+v", response)
	}
	if response.Error == nil {
		t.Fatal("error envelope carries no typed error")
	}
	if response.Error.Kind != "budget_refused" {
		t.Fatalf("kind=%q, want the budget refusal the payload cannot fit", response.Error.Kind)
	}
	// The reclaim committed before the refusal: the envelope must not claim
	// no effect, and must carry exactly the committed row.
	if response.Error.EffectState != EffectPossible {
		t.Fatalf("effect_state=%q, want %q: work-2 already reclaimed", response.Error.EffectState, EffectPossible)
	}
	if response.ChangedRefs == nil || len(*response.ChangedRefs) != 1 || (*response.ChangedRefs)[0].ID != "work-2" {
		t.Fatalf("changed refs=%+v, want exactly the reclaimed work-2", response.ChangedRefs)
	}
	// The coupled budget recovery is untouched: only the effect lie changes.
	if response.Error.RecoveryAction.Kind != "adjust_budget" {
		t.Fatalf("recovery=%q, want the coupled adjust_budget", response.Error.RecoveryAction.Kind)
	}
	if response.Error.SupportedBudgetSeconds < 1 {
		t.Fatalf("budget refusal lacks the typed ceiling: %+v", response.Error)
	}
	if response.Error.RetrySafe {
		t.Fatal("identical retry under the same budget refuses again")
	}
	if err := response.Validate(); err != nil {
		t.Fatalf("post-commit failure envelope is invalid: %v: %+v", err, response.Error)
	}

	// Reconcile under the same key with a fitting budget: the recorded pass
	// is absent, so the audit re-runs, finds the reclaimed row gone, and
	// converges without re-executing the completed reclaim.
	work2Version := workVersion(t, s, "work-2")
	settled := authorityInvoke(t, s, second, secondGrant, "concord_work_transition", "worktree_audit_reclaim", map[string]any{
		"product_id": "product-1", "default_ref": "main", "idempotency_key": "audit-effect-1",
		"observed_session_directories": []map[string]any{
			{"session_ref": "ses-1", "directory": filepath.Join(root, "work-1")},
		},
	})
	if settled.Outcome != OutcomeOK {
		t.Fatalf("reconcile response=%+v err=%+v", settled, settled.Error)
	}
	if version := workVersion(t, s, "work-2"); version != work2Version {
		t.Fatalf("reconcile moved work-2 to version %d", version)
	}
	var settledRows struct {
		Rows []struct {
			WorkID  string `json:"work_id"`
			Outcome string `json:"outcome"`
		} `json:"rows"`
	}
	if err := json.Unmarshal(settled.Result, &settledRows); err != nil {
		t.Fatalf("decode settled result: %v", err)
	}
	if len(settledRows.Rows) != 1 || settledRows.Rows[0].WorkID != "work-1" || settledRows.Rows[0].Outcome != "refused" {
		t.Fatalf("settled rows=%+v, want only the still-occupied work-1 refused", settledRows.Rows)
	}
}

// The mapping underneath the fixture, pinned directly: post-commit failures
// keep their kind and coupled recovery, gain possible effects with the
// committed refs, and leave the no-effect path untouched when nothing
// committed.
func TestAuditReclaimPostCommitFailureMapping(t *testing.T) {
	base := NewBase("audit-effect-mapping", "concord_work_transition", "worktree_audit_reclaim")
	changed := []ChangedRef{{EntityKind: "work_item", ID: "work-2", Version: "5"}}

	failure := coreError(base, "malformed_response", "mutation result failed closed-schema validation", "contact_operator", false)
	mapped := auditReclaimPostCommitFailure(base, changed, failure)
	if mapped.Outcome != OutcomeError || mapped.Error == nil {
		t.Fatalf("mapped=%+v", mapped)
	}
	if mapped.Error.Kind != "malformed_response" || mapped.Error.RecoveryAction.Kind != "contact_operator" {
		t.Fatalf("mapping must keep kind and recovery: %+v", mapped.Error)
	}
	if mapped.Error.EffectState != EffectPossible {
		t.Fatalf("effect_state=%q, want possible", mapped.Error.EffectState)
	}
	if mapped.ChangedRefs == nil || len(*mapped.ChangedRefs) != 1 || (*mapped.ChangedRefs)[0].ID != "work-2" {
		t.Fatalf("changed refs=%+v", mapped.ChangedRefs)
	}
	if err := mapped.Validate(); err != nil {
		t.Fatalf("mapped envelope is invalid: %v", err)
	}

	untouched := coreError(base, "budget_refused", "mutation result exceeds requested max_bytes budget", "adjust_budget", false)
	untouched.Error.SupportedBudgetSeconds = 300
	kept := auditReclaimPostCommitFailure(base, nil, untouched)
	if kept.Error.EffectState != EffectNone || kept.ChangedRefs != nil {
		t.Fatalf("empty commit set must keep the no-effect refusal: %+v", kept.Error)
	}

	invalid := coreError(base, "budget_refused", "mutation result exceeds requested max_bytes budget", "adjust_budget", false)
	validated := auditReclaimPostCommitFailure(base, changed, invalid)
	if err := validated.Validate(); err != nil {
		t.Fatalf("invalid post-commit failure was not repaired to a valid envelope: %v", err)
	}
	if validated.Error == nil || validated.Error.Kind != "malformed_response" || validated.Error.EffectState != EffectPossible {
		t.Fatalf("invalid post-commit failure was not typed with its committed effect: %+v", validated)
	}
}

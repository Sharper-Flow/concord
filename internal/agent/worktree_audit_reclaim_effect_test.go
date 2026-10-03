package agent

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
)

// Issue #757: the audit commits each reclaimed row in its own transaction,
// then builds the result, and any failure after the commit (result
// enrichment, result validation, idempotency insert) must not claim no
// effect: the reclaimed rows already applied. This fixture runs the mixed
// sweep — one row reclaimed, one row refused — and pins the committed
// effect and the same-key convergence the post-commit path relies on.
func TestWorktreeAuditReclaimMixedSweepCommitsAndConverges(t *testing.T) {
	t.Parallel()
	s, _, _, second, secondGrant, _ := tiersFixture(t)
	root := filepath.Join(filepath.Dir(s.Path()), "worktrees", "project-1")
	completeWork(t, s, "work-1")
	completeWork(t, s, "work-2")
	// work-2 reclaims, so its claiming session vacates first; work-1 stays
	// occupied, so its row refuses.
	vacateLinkedWorktree(t, s, second, secondGrant, filepath.Join(root, "work-2"), "audit-effect-vacate-2")
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
	// work-1 stays occupied, so its row refuses; work-2 reclaims. The
	// recorded Concord occupant is the refusal authority.
	raw, _ := json.Marshal(map[string]any{
		"product_id": "product-1", "default_ref": "main", "idempotency_key": "audit-effect-1",
	})
	r := runtime{Store: s, Authority: second, Envelope: env, Tool: "concord_work_transition", Operation: "worktree_audit_reclaim", Reader: secondGrant}
	base := NewBase("audit-effect-1", "concord_work_transition", "worktree_audit_reclaim")
	response, err := r.mutateWorktreeAuditReclaim(ctx, base, raw, secondGrant, op)
	if err != nil {
		t.Fatalf("planner err=%v", err)
	}
	if response.Outcome != OutcomeOK {
		t.Fatalf("mixed sweep must reclaim work-2 and report ok, got %+v", response.Error)
	}
	if response.Error != nil {
		t.Fatalf("success envelope carries no error: %+v", response.Error)
	}
	if err := response.Validate(); err != nil {
		t.Fatalf("pass envelope is invalid: %v: %+v", err, response.Error)
	}
	var pass struct {
		Rows []struct {
			WorkID  string `json:"work_id"`
			Outcome string `json:"outcome"`
		} `json:"rows"`
	}
	if err := json.Unmarshal(response.Result, &pass); err != nil {
		t.Fatalf("decode pass result: %v", err)
	}
	if len(pass.Rows) != 2 {
		t.Fatalf("rows=%+v, want the reclaimed work-2 and the refused work-1", pass.Rows)
	}

	// Reconcile under the same key: the recorded pass returns and the audit
	// does not run again, so the completed reclaim never re-executes.
	work2Version := workVersion(t, s, "work-2")
	settled := authorityInvoke(t, s, second, secondGrant, "concord_work_transition", "worktree_audit_reclaim", map[string]any{
		"product_id": "product-1", "default_ref": "main", "idempotency_key": "audit-effect-1",
	})
	if settled.Outcome != OutcomeOK {
		t.Fatalf("reconcile response=%+v err=%+v", settled, settled.Error)
	}
	if version := workVersion(t, s, "work-2"); version != work2Version {
		t.Fatalf("reconcile moved work-2 to version %d", version)
	}
}

// The mapping underneath the fixture, pinned directly: post-commit failures
// keep their kind and coupled recovery, gain possible effects with the
// committed refs, and leave the no-effect path untouched when nothing
// committed.
func TestAuditReclaimPostCommitFailureMapping(t *testing.T) {
	t.Parallel()
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

	untouched := coreError(base, "malformed_response", "mutation result failed closed-schema validation", "contact_operator", false)
	kept := auditReclaimPostCommitFailure(base, nil, untouched)
	if kept.Error.EffectState != EffectNone || kept.ChangedRefs != nil {
		t.Fatalf("empty commit set must keep the no-effect refusal: %+v", kept.Error)
	}

	// A budget_refused without the typed ceiling is an invalid failure; the
	// mapping repairs it to a valid envelope typed with the committed effect.
	invalid := coreError(base, "budget_refused", "requested_budget_seconds 600 exceeds supported 300", "adjust_budget", false)
	validated := auditReclaimPostCommitFailure(base, changed, invalid)
	if err := validated.Validate(); err != nil {
		t.Fatalf("invalid post-commit failure was not repaired to a valid envelope: %v", err)
	}
	if validated.Error == nil || validated.Error.Kind != "malformed_response" || validated.Error.EffectState != EffectPossible {
		t.Fatalf("invalid post-commit failure was not typed with its committed effect: %+v", validated)
	}
}

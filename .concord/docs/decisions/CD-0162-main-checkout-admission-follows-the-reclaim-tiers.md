# CD-0162: The main-checkout admission follows the reclaim tiers

- **Status:** Accepted
- **Date:** 2026-09-20
- **Scope:** The main-checkout authority for the worktree retirement
  operations; the allowlist in `internal/agent/authority.go`; the rationale
  that admits `worktree_audit_reclaim` without a worktree anchor
- **Approval:** The operator approved the objective for
  `work-2b315bf5d5d3e2241bb6911d` at its repair step on 2026-09-20.
- **Related:** CD-0092, CD-0105, CD-0118, issues #674, #723
- **Amends:** CD-0105 D4 at its rationale, which rested on the pass
  reclaiming only terminal work
- **Preserves:** The CD-0092 D2 refusal for every operation whose row gates
  do not close the implementation surface; every tier gate on every reclaimed
  row

## Context

CD-0092 D2 refuses implementation-bearing authority from a registered
Project's main checkout. Issue #674 admitted `worktree_reclaim` there for
terminal work, because a terminal item holds no live implementation surface.
CD-0105 D4 gave `worktree_audit_reclaim` the same admission on the same
ground: the pass reclaimed only terminal work.

CD-0118 broke that ground. The pass now reclaims `unstarted_present` rows
whose work items are `needed`, not terminal. The operation resolved nowhere:
the authorization firewall refused it at the main checkout, although the
planner holds no main-checkout check of its own, and the store pass gates
every row it reclaims. The allowlist map named
`mainCheckoutTerminalWorkOperations` could not honestly carry the operation,
because terminality is no longer the admission's ground.

The admission's surviving ground is the tiers. The terminal tier reclaims a
merged tree. The unstarted tier reclaims a clean tree whose branch holds no
commit beyond the default ref. Per-row eligibility in the store admits no
other class. Neither tier can strand committed or uncommitted implementation
work, which is the surface CD-0092 D2 protects.

## Decision

### D1. The admission rests on the tier gates

A main-checkout grant may run a worktree retirement operation when every row
the operation can act on passes a tier gate that establishes no live
implementation surface. The terminal tier requires a merged head by tree
identity. The unstarted tier requires a clean tree and zero commits beyond
the default ref. Per-row eligibility in the store admits no other class, so
the surface stays closed.

### D2. `worktree_audit_reclaim` resolves from the main checkout

`concord_work_transition.worktree_audit_reclaim` joins the allowlist beside
`worktree_reclaim`. The store pass applies the tier gates to each row in its
own transaction, so the planner adds no main-checkout check. The direct
reclaim keeps its planner-side terminality condition, because its one
addressed row may hold any lifecycle.

### D3. The allowlist name states the ground

`mainCheckoutTerminalWorkOperations` becomes
`mainCheckoutWorktreeRetirementOperations`. Terminality is one tier's
condition, not the admission's ground. Each later entry requires a decision
naming the row gate that closes the surface, as CD-0092 D3 requires at the
capability boundary.

## Consequences

- A session in the main checkout runs the audit pass and clears both
  accumulated drift classes. No worktree anchor sits between the drift and
  the reclaim.
- The structural lock moves with the name. The both-sides test pins the two
  retirement operations and refuses a third without a decision.
- CD-0105 D4 keeps its outcome and loses its rationale. The admission stands
  on the tiers.

## Rejected alternatives

- **Carry the operation in the terminal-only map.** The name would misstate
  the ground CD-0118 already broke, and the next reader would inherit the
  same doubt this record answers.
- **A main-checkout check in the audit planner.** A second gate beside the
  row gates. The store already refuses a non-eligible row with its typed
  kind, so a planner check decides nothing the tier gates have not decided.

## Verification

- `internal/agent.TestMainCheckoutAllowlistDeclaresBothSides` pins the
  renamed map at exactly the two retirement operations.
- `internal/agent.TestAuditReclaimResolvesFromMainCheckout` grants the
  operation at the authorization boundary and runs a pass from the main
  checkout through real git.
- `internal/agent.TestMainCheckoutRefusesImplementationOperations` keeps the
  typed refusal for every unnamed operation.
- `internal/agent.TestWorktreeReclaimFromMainCheckoutRequiresTerminalWork`
  keeps the direct reclaim's terminality condition.

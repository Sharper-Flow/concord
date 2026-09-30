# CD-0094: Allow operation-scoped main-checkout lifecycle authority

- **Status:** Accepted
- **Date:** 2026-09-01
- **Scope:** Operation-level authority for
  `concord_work_transition.lifecycle` from a registered Project's default
  checkout; the authority boundary in `internal/agent/authority.go`; issue #660
- **Approval:** The operator approved the disposition in issue #660 and
  directed its completion on 2026-09-01.
- **Related:** CD-0008, CD-0072, CD-0089, CD-0092, issue #660
- **Amends:** CD-0092 D2 and D3 at the `work_transition.lifecycle` boundary only

## Context

CD-0092 scopes the main-checkout refusal by capability. It refuses the entire
`work_transition` capability because that capability includes operations that
claim or reclaim implementation worktrees.

`concord_work_transition.lifecycle` has a different write surface. Its plan
writes exactly one `work.transitioned` event in the store. It does not write a
repository checkout path and it does not claim a worktree. Terminal targets
already require verification evidence and operator approval through structural
workflow checks.

After a merged change, a host-captured work item can need a lifecycle
transition from `needed` to `completed`. The merged pull request is completion
authority, but CD-0092 prevents the state event from the default checkout. The
capability-level refusal therefore blocks post-merge reconciliation even though
this operation writes Product state only.

## Decision

### D1. Main-checkout authority is operation-scoped

The main checkout allows a capability when the capability is in the closed
capability allowlist, or when the pair of capability and operation is in the
closed operation allowlist. The declaration remains in one authority module.

`work_transition.lifecycle` is the first operation-scoped allowance. The
allowance does not apply to other operations under `work_transition`.

### D2. Lifecycle is Product-state-only

`concord_work_transition.lifecycle` writes one `work.transitioned` event in the
store. It writes no checkout path and claims no implementation worktree.

The operation keeps its existing gates. A terminal `completed` or `cancelled`
target without verification evidence returns `missing_evidence`. Evidence then
permits the existing operator approval challenge, and the transition applies
only after the approval is accepted.

`worktree_claim`, `worktree_reclaim`, and `workflow_action` remain refused from
the main checkout with the typed CD-0092 refusal. `worktree_reclaim` can remove
a Git worktree, and `workflow_action` is execution authority.

### D3. The boundary is declared once and tested on both sides

`internal/agent/authority.go` declares the capability allowlist and the
operation-scoped extension. The authority-boundary tests prove that lifecycle
authority grants from a registered Project's default checkout and that the
implementation-bearing operations keep their typed refusal.

The same tests prove that terminal evidence and operator approval remain
required. They also prove that a terminal refusal without evidence is
`missing_evidence`, not `unauthorized`.

### D4. CD-0092 is amended at this boundary

CD-0092 D2 and D3 retain the capability-level rule for every operation except
the allowance defined here. This record is the amendment of record. Both
records retain their historical text.

## Consequences

- A registered Project can reconcile a merged work item from its default
  checkout by recording a lifecycle transition with merge evidence.
- Terminal lifecycle transitions still require verification evidence and
  operator approval.
- Worktree claims, worktree reclamation, and workflow actions still require a
  linked worktree or another eligible implementation authority.
- A future main-checkout exception requires an operation-level decision that
  names its write surface and adds tests for both the grant and refusal sides.

## Verification

- `TestLifecycleTransitionAllowsMainCheckout` proves a non-terminal lifecycle
  transition from the main checkout.
- `TestMainCheckoutLifecyclePreservesTerminalGates` proves the missing-evidence
  refusal and the approved terminal transition.
- `TestMainCheckoutRefusesImplementationOperations` proves typed refusal for
  `worktree_claim`, `worktree_reclaim`, and `workflow_action`.
- `TestMainCheckoutAllowlistDeclaresBothSides` proves the two declarations and
  their closed entries.
- The document contract and knowledge-closure validators pass.

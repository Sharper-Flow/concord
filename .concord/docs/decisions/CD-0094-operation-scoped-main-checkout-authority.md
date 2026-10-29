# CD-0094: Allow operation-scoped main-checkout lifecycle authority

- **Status:** Accepted
- **Date:** 2026-09-01
- **Scope:** Operation-level authority from a registered Project's default checkout for `concord_work_transition.lifecycle` and D5's conditional `concord_work_transition.remove` allowance; the authority boundary in `internal/agent/authority.go`.
- **Approval:** The operator approved the disposition in issue #660 and
  directed its completion on 2026-09-01.
- **Related:** CD-0008, CD-0072, CD-0089, CD-0092, issue #660

**Approval amendment:** [CD-0243 D7](CD-0243-four-roles-and-six-operator-approval-classes.md#d7-the-remaining-owners-use-the-same-approval-boundary)
qualifies every operator-consent clause in this record, including tables and
acceptance criteria. Other admission, safety, evidence, and historical-pin
requirements remain unchanged.

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

The operation-scoped checkout authorization remains independent of consent. CD-0013 D11 removes approval only for routine acceptance-backed completion; it preserves terminal evidence, live-workflow completion refusal, cancellation approval, and historical-pin requirements. A main checkout acquires no implementation authority from this change.


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

`worktree_claim` and `workflow_action` remain refused from the main checkout with the typed CD-0092 refusal. `worktree_reclaim` and `worktree_audit_reclaim` retain CD-0162's tier-gated admission. Removal receives only D5's conditional allowance.

### D3. The boundary is declared once and tested on both sides

`internal/agent/authority.go` declares the capability allowlist and the
operation-scoped extension. The authority-boundary tests prove that lifecycle
authority grants from a registered Project's default checkout and that the
implementation-bearing operations keep their typed refusal.

The same tests prove that terminal evidence and operator approval remain
required. They also prove that a terminal refusal without evidence is
`missing_evidence`, not `unauthorized`.

### D4. CD-0092 is amended at this boundary

Main-checkout authorization follows the closed capability allowlist and the
closed operation allowlist declared in `internal/agent/authority.go`.

### D5. Default-checkout shelving after cancellation and verified retirement

From a registered Project's default checkout, `concord_work_transition.remove` may remove one item with reason `shelved` only when all of the following hold:

1. Its sole Project is the caller's resolved Project, its sole Product is the selected Product, and existing caller authority covers both.
2. Its lifecycle and any workflow instance are `cancelled`, under the existing cancellation evidence, approval and historical-pin requirements.
3. At least one worktree claim is recorded, all recorded claims are reclaimed, and complete native inventory plus filesystem checks prove every recorded checkout path absent.
4. Every recorded native retirement effect, across all claim generations and incarnations, is settled or durably retained with no cleanup owed. Pending deletion or restoration, active or pending claims, and live or unknown occupants refuse removal.
5. Every existing removal safety, version, handoff, authority and approval requirement passes.

Authorization admits only this operation pair, not general `work_transition` authority. The removal planner and core check fresh eligibility before issuing a challenge and revalidate state, scope, work version and claim identities in the removal transaction. Native observations run outside that transaction and bind to those identities. Incomplete proof, observed change or uncertainty refuses removal without force or recovery bypass.

The allowance performs only the existing removal's store effects and receipt; it grants no checkout or provider execution authority. Other operations and removal routes keep their admission.

An identical committed removal replays through its retained authorized scope before live-item eligibility is evaluated. Replay checks current caller authority and the original request binding; it neither recreates the identity nor issues new removal authority.

## Consequences

- A registered Project can reconcile a merged work item from its default
  checkout by recording a lifecycle transition with merge evidence.
- Terminal lifecycle transitions still require verification evidence and
  operator approval.
- Main-checkout worktree retirement retains CD-0162's tier gates. Removal receives only D5's allowance; other implementation-bearing operations remain refused.
- A future main-checkout exception requires an operation-level decision that
  names its write surface and adds tests for both the grant and refusal sides.

## Verification

- `TestLifecycleTransitionAllowsMainCheckout` proves a non-terminal lifecycle
  transition from the main checkout.
- `TestMainCheckoutLifecyclePreservesTerminalGates` proves the missing-evidence
  refusal and the approved terminal transition.
- `TestMainCheckoutRefusesImplementationOperations` proves the typed CD-0092 refusal for `worktree_claim`, nonterminal `worktree_reclaim`, and `workflow_action`. D5 authority tests prove eligible default-checkout shelving removal and refusal outside its conditions, preserving all existing removal gates.
- `TestMainCheckoutAllowlistDeclaresBothSides` proves the two declarations and
  their closed entries.
- The document contract and knowledge-closure validators pass.

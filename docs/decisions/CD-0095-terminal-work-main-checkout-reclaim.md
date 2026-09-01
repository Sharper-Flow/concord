# CD-0095: Allow main-checkout worktree reclamation for terminal work

- **Status:** Accepted
- **Date:** 2026-09-01
- **Scope:** Operation-level authority for
  `concord_work_transition.worktree_reclaim` from a registered Project's
  default checkout when the addressed work item is terminal; the authority
  boundary in `internal/agent/authority.go` and the reclaim planner in
  `internal/agent/mutations.go`; issue #674
- **Approval:** The operator approved the disposition in issue #674 and
  directed its completion on 2026-09-01.
- **Related:** CD-0008, CD-0089, CD-0092, CD-0094, issue #674
- **Amends:** CD-0094 D2 at the `worktree_reclaim` boundary only

## Context

CD-0094 keeps `worktree_reclaim` refused from the main checkout because the
operation can remove a Git worktree.

That refusal blocks the state a merged change leaves behind. On 2026-09-01 a
work item was completed with merge evidence, its pull request was merged, and
its branch was deleted. The main checkout still refused `worktree_reclaim`,
so the operator removed the worktree by hand and the Concord projection kept
a stale claim.

A terminal work item holds no live implementation surface. Its branch is
merged or retired, its workflow is closed, and no lane holds authority in its
worktree. Removing that worktree from the default checkout retires an
implementation surface instead of disturbing active work.

## Decision

### D1. `worktree_reclaim` is conditionally allowed from the main checkout

The main-checkout authorization admits one more operation pair:
`work_transition.worktree_reclaim`. The closed set lives beside the CD-0094
allowlists in `internal/agent/authority.go`.

Authorization does not decide terminality. The grant records that it resolved
from the main checkout, and the reclaim planner enforces the condition.

### D2. The planner refuses non-terminal work with the CD-0092 refusal

When the grant resolved from the main checkout, the plan reads the addressed
lifecycle inside the reclaim transaction. A lifecycle that is neither
`completed` nor `cancelled` returns the typed `unauthorized` refusal with the
CD-0092 D2 message.

Terminal work proceeds. The store layer keeps its own gates: a dirty tree
refuses, an unmerged branch refuses, and an absent worktree is recorded as
reclaimed.

### D3. Linked-worktree authority is unchanged

A grant from a linked worktree reclaims work items of every lifecycle, as
before. The terminality condition applies only to main-checkout grants.

## Consequences

- A terminal work item can be reclaimed from the registered Project's
  default checkout after its change merges.
- Non-terminal work still requires a linked worktree for reclamation.
- `worktree_claim` and `workflow_action` still require a linked worktree.
- A future main-checkout exception still requires an operation-level
  decision that names its write surface.

## Verification

- `TestWorktreeReclaimFromMainCheckoutRequiresTerminalWork` proves the
  refusal for non-terminal work and the reclamation for completed and
  cancelled work from the main checkout.
- `TestMainCheckoutRefusesImplementationOperations` keeps proving the typed
  refusal for a non-terminal work item alongside `worktree_claim` and
  `workflow_action`.
- `TestWorktreeClaimAndReclaimThroughToolSurface` keeps proving the
  linked-worktree path for a non-terminal work item.
- `TestMainCheckoutAllowlistDeclaresBothSides` proves the closed conditional
  set and its single entry.
- The document contract and knowledge-closure validators pass.

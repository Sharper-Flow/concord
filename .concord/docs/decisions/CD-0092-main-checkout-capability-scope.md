# CD-0092: Scope the main-checkout refusal by capability class

- **Status:** Accepted
- **Date:** 2026-09-01
- **Scope:** Which capabilities the main checkout refuses; the authority
  boundary in `internal/agent/authority.go`; issue #627
- **Approval:** The operator approved the decision contract in issue #627 and
  directed its completion on 2026-09-01.
- **Related:** CD-0008, CD-0072, CD-0035, issue #611
- **Amends:** CD-0072 D2's grant-level refusal clause and CD-0008 D1's
  main-checkout read-only rule, at the capability boundary only

## Context

`internal/agent/authority.go` refuses every capability except `product_read`
when the resolved scope is the main checkout. The rule uses a code-isolation
guard to gate Product-state writes.

CD-0008 D1 draws the opposite line for state: Product and workflow state never
branches with Git code, and worktree possession grants no Product authority.
Capturing a work item writes no file in any checkout; it writes the store
outside all repositories.

The fusion produced the bootstrap loop issue #611 describes: capture needs a
linked worktree because every mutation is refused from the main checkout, and
the atomic worktree bootstrap needs a captured work item to claim a worktree
for. Issue #627 recorded the required outcome and acceptance criteria; this
decision accepts them.

## Decision

### D1. The refusal follows the capability's write surface

A capability is refused on the main checkout when its operations can write into
the repository checkout or claim implementation worktrees. A capability whose
operations write only Product state — the store outside all repositories — is
allowed from a registered Project's default checkout.

### D2. `work_define` is Product-state-only

`concord_work_define` operations (`capture`, `revise_intent`, and the research
authoring operations) write work items, memberships, and research packs in the
store. None writes a checkout path. The capability is therefore allowed from
the default checkout.

All other mutating capabilities — including `work_transition`, whose
`worktree_claim` claims an implementation worktree, and dispatch — remain
refused there with the existing typed refusal.

### D3. The boundary is declared once

A single declaration in `internal/agent` names the main-checkout capability
allowlist. A test proves each side: `work_define` grants from the main
checkout, and each refused capability keeps its typed refusal.

### D4. CD-0072 D2 and CD-0008 D1 are amended at this boundary

CD-0072 D2 names the grant-level refusal as remaining in force; this decision
amends that clause to the capability-scoped form above. CD-0008 D1's
read-only rule for the main checkout now governs implementation-bearing
mutations, not Product-state capture. Both records retain their historical
text; this record is the amendment of record.

## Consequences

- The operator and agents can record work from the default checkout without
  first claiming a worktree, closing the #611 bootstrap loop at the authority
  layer.
- Implementation work still cannot run from the default checkout; the
  trunk-stays-clean invariant is unchanged for everything that touches files.
- A future capability joins the allowlist only through a decision naming its
  write surface.

## Verification

- `concord_work_define.capture` and `revise_intent` succeed from a registered
  Project's default checkout.
- Implementation-bearing capabilities keep the typed refusal from the same
  checkout.
- The allowlist is one declaration, and a test pins each capability's side.
- The knowledge record, coverage shard, and repository validators pass.

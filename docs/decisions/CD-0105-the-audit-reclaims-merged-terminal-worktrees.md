# CD-0105: The worktree audit reclaims merged terminal worktrees

- **Status:** Accepted
- **Date:** 2026-09-03
- **Scope:** The trigger that reclaims a worktree whose work is terminal; the
  drift class the audit reports for such a worktree; the one action the audit
  performs itself
- **Approval:** The operator approved the audit as the reclaim trigger in
  session on 2026-09-03, over a completion-time hook, and approved the
  contract for `work-cc3853268a41489a1ee347dd` under CD-0092 pinning.
- **Related:** CD-0082, CD-0092, CD-0095, CD-0096, CD-0104, issues #635, #674,
  #722
- **Amends:** CD-0096 D3 Destroy at the trigger, which it left unnamed
- **Preserves:** Every CD-0095 and CD-0096 D3 gate on removal: the clean
  tree, the merged head by tree identity, the observed-session stranding
  refusal, and operator approval for non-terminal or destructive removal

## Context

CD-0096 D3 Destroy defines what may remove a worktree and under which gates.
It never says what starts the removal. In practice nothing did. A worktree
whose branch merged and whose work item completed stayed on disk until an
operator remembered it, and on the operator host seventeen such worktrees
had accumulated by the day this record was written.

The audit, `concord_work_browse.worktree_audit`, already computes every fact
a reclaim needs. It reports each drift row with its work item, path, claim
state, lifecycle, and the recovery action it recommends. It had three
classes: an orphan directory nobody claims, a claim whose directory is gone,
and needed work whose directory is gone. It had no class for the one case
that actually accumulates: a directory that is present, still claimed, and
whose work is finished. Nothing is wrong with such a worktree. Nothing will
use it again.

Two triggers were considered. A completion-time hook reclaims inside the
lifecycle transition. The audit reclaims on its own pass. The hook cannot
honor the stranding gate without the lifecycle call carrying host session
observations, which pulls a filesystem effect into a Product transition that
CD-0104 just separated from host facts. The hook also never reaches a
worktree that was already stale when the hook shipped.

## Decision

### D1. The audit names a fourth class

A worktree that is present on disk, holds an active entry, and belongs to a
work item whose lifecycle is `completed` or `cancelled` is drift of class
`terminal_present`. Its recovery action is `worktree_reclaim`. The row carries
the lifecycle so a reader can tell which terminal state produced it.

### D2. The audit performs that one action

`concord_work_transition.worktree_audit_reclaim` runs the audit for one
Product and reclaims each `terminal_present` row through the direct reclaim.
Every gate the direct reclaim applies stays as it is. The caller supplies the
same session observations the direct reclaim takes, and a row an observed
session occupies refuses with `worktree_ownership_conflict`. A dirty tree and
an unmerged head refuse with their existing kinds. Every other class stays
report-only, because its named action is not a store decision: an orphan is
removed by hand, a stale claim is reclaimed by name, stranded work is claimed
again.

### D3. Rows are independent

Each row reclaims in its own transaction. One refused row never rolls back
another row's reclamation. The result reports every row with its outcome,
and a refused row carries the typed kind and detail the gate produced. A
second pass over the same state reclaims nothing and reports the same
refusals, and a replay under the same idempotency key returns the recorded
pass without running the audit again.

### D4. No worktree anchor is required

Issue #674 opened the direct reclaim of terminal work to a main-checkout
grant, because terminal work holds no live implementation surface. The pass
reclaims only terminal work, so the same grant runs it. A session in any
linked worktree runs it too.

## Consequences

- A worktree is reclaimed on the next audit pass after its work completes,
  by whichever session runs the pass. No operator step sits between the
  completion and the reclaim.
- The seventeen stale worktrees on the operator host are reclaimed by the
  same pass, which a completion-time hook could never have reached.
- `work-1bd58d1d2badc7d43003d007`, reclaim on complete, is superseded for its
  remaining scope by this record. Its delivered half, the squash-merged head
  accepted by tree identity, stands as #635 landed it.
- The audit's read result admits `terminal_present` and terminal lifecycles.
  A reader that switched on the class must handle the fourth.

## Rejected alternatives

- **A completion-time hook.** Reclaim inside the lifecycle transition. It
  cannot honor the stranding gate without host observations on a Product
  transition, and it reaches nothing already stale. Rejected by the operator.
- **A proposed intent the adapter executes.** The core returns a reclaim
  intent on completion and the host performs it. The adapter consumes no
  `next_valid_intents` today, and teaching it to act on one is a second
  mechanism for what the audit already does.
- **One transaction for the whole pass.** Atomic across rows. A single dirty
  tree would then block every clean reclaim, which is the opposite of the
  pass's purpose.

## Verification

- `internal/store.TestWorktreeAuditClassifiesTerminalPresentWorktrees`
  asserts a present, claimed, completed worktree is reported as
  `terminal_present` with `worktree_reclaim`, and a live one is not.
- `internal/store.TestWorktreeAuditReclaimsMergedTerminalWork` asserts a
  merged terminal worktree reclaims, a dirty one refuses typed, live work is
  absent from the pass, and a second pass reclaims nothing.
- `internal/store.TestWorktreeAuditReclaimRefusesOccupiedWorktree` asserts
  the stranding gate holds under the pass.
- `internal/agent.TestWorktreeAuditReclaimDispatchReclaimsTerminalWorkOnly`
  runs the pass through the agent surface against real git, asserts the
  terminal worktree is gone and the live one remains, and asserts a replay
  returns the recorded pass.
- `python3 scripts/generate-agent-contracts.py --check` is clean with 63
  operations.

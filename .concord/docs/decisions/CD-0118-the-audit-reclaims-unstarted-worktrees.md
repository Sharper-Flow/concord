# CD-0118: The worktree audit reclaims unstarted worktrees

- **Status:** Accepted
- **Date:** 2026-09-06
- **Scope:** The drift class for a claimed worktree whose work has not
  started; the gate that reclaims it without operator approval; the facts the
  drift row carries
- **Approval:** The operator approved the contract for
  `work-2b85cd5008dba4e50005c3e8` in session on 2026-09-06, under CD-0092
  pinning, selecting a zero-commit clean tree as the authorizing gate.
- **Related:** CD-0095, CD-0096, CD-0105, issue #723
- **Amends:** CD-0096 D3 Destroy at the approval sentence, which reserved
  approval-free removal to merged terminal work; CD-0105 D1 and D2 at the
  class count and the pass's action set
- **Preserves:** Every CD-0095 and CD-0096 D3 gate on removal except the one
  sentence this record amends: the clean tree, the observed-session
  stranding refusal, and operator approval for every other non-terminal
  removal and for destructive removal

## Context

CD-0105 named the trigger that reclaims a finished worktree. It left the
other accumulation unnamed: a work item captured as `needed`, whose canonical
worktree `concord_work_start` claimed at capture, and whose session never
wrote a commit. On the operator host, fourteen of fifty-four active
worktrees held no commit beyond the default ref on the day this record was
written. Each is a full checkout that cost disk and clone time, and none can
lose work a merge could recover, because none holds work.

The claim sits at capture because CD-0088 D1 derives the canonical worktree
intent when the item is captured or resumed. Moving that claim to contract
approval is a separate change to the capture route; this record does not
make it. It answers the narrower question: once such a worktree exists, what
may remove it.

CD-0096 D3 Destroy reserved approval-free removal to merged terminal work.
An unstarted worktree fails that gate twice over: its work is `needed`, and
its branch is not merged. Yet the reason those gates exist — committed or
uncommitted work that a removal would strand — is absent by construction.

## Decision

### D1. The audit names a fifth class

A worktree that is present on disk, holds an active entry, belongs to a work
item whose lifecycle is `needed`, has a clean tree, and whose branch holds no
commit beyond the Project's default ref is drift of class
`unstarted_present`. Its recovery action is `worktree_reclaim`, the same
action the terminal class names. The row carries two facts: `commits_ahead`,
the gate input, and `claim_age_seconds`, an operator display fact that no
gate reads.

Work in flight is not this class. A dirty tree, a branch with commits beyond
the default ref, or work past `needed` stays unclassified, because a session
may be mid-turn in it.

### D2. The gate is a commit count, not tree identity

The terminal tier asks whether a branch merged: tree identity answers that,
and a squash merge defeats commit reachability. The unstarted tier asks a
narrower question: does anything exist to lose. `rev-list --count` from the
default ref to the branch answers it. A branch whose commits cancel to the
default tree — a commit and its revert — satisfies tree identity while
holding commits, so tree identity cannot answer this question and the count
does. The count must be zero.

### D3. The pass reclaims through this gate without operator approval

`concord_work_transition.worktree_audit_reclaim` reclaims each
`unstarted_present` row through the direct reclaim under the CD-0095 store
gates, with this tier's gate in place of the merged-head gate: clean tree,
zero commits beyond the default ref, no observed session inside. The work
item's lifecycle does not change; the reclaim is not a verdict on the work.
A replay of the item's claim recreates the worktree on demand, so the
checkout cost is the only cost. The stranding gate and every refusal the
direct reclaim produces keep their kinds.

### D4. A read that cannot derive the gate skips the class

The audit read derives the default ref from the repository's `origin/HEAD`
or a caller-supplied override. A Project whose default ref cannot be
resolved contributes no `unstarted_present` rows to the read, and the read
keeps serving every other class: one underivable fact must not refuse the
page, which is the lesson of issue #831. The reclaim pass, a mutation,
refuses typed and names the missing ref instead, because a mutation cannot
silently skip its own gate.

## Consequences

- A captured item whose session never wrote a commit loses its worktree on
  the next audit pass, whichever session runs it. The item stays `needed`
  and remains drivable; `concord_work_start` reclaims a fresh worktree when
  the item is next started.
- The operator host's stranded checkouts clear without per-item approvals.
- The audit's read result admits `unstarted_present`, `needed` rows in the
  reclaim result, and the two new row facts. A reader that switched on the
  class must handle the fifth.
- `claim_age_seconds` is display-only. No present or future gate may read
  it; an age-based gate would reclaim on a timer rather than on a structural
  fact, and the operator selected the structural gate.

## Rejected alternatives

- **Tree identity as the gate.** Reuse the terminal tier's merged-tree test.
  A commit-and-revert branch satisfies it while holding commits, so it
  authorizes removing a branch with history the default ref does not hold.
- **An age threshold.** Reclaim worktrees whose claim is older than N days.
  Age does not establish that work is absent, and the operator host's store
  is days old, so every row would pass on age alone. Rejected by the
  operator in session on 2026-09-06.
- **Moving the claim to contract approval.** Stop claiming at capture; claim
  when a contract is approved. It removes the cause rather than the symptom
  and amends CD-0088 D1 and CD-0098 D1. Larger than this record's scope; it
  remains open as its own work.

## Verification

- `internal/store.TestWorktreeAuditClassifiesUnstartedPresentWorktrees`
  asserts the class, its two row facts, and that dirty, commit-bearing, and
  started work stays unclassified.
- `internal/store.TestWorktreeAuditReclaimsUnstartedPresentWorktrees`
  asserts the pass reclaims the row, leaves the item at `needed`, and a
  second pass reclaims nothing.
- `internal/store.TestWorktreeAuditReclaimRefusesUnstartedWorktreeWithEquivalentTree`
  asserts the commit-and-revert shape stays out of the pass.
- `internal/store.TestWorktreeAuditReclaimRefusesOccupiedUnstartedWorktree`
  asserts the stranding gate holds for this class.
- `internal/store.TestReclaimWorktreeUnstartedTierRefusesStartedWork`
  asserts the direct surface refuses work past `needed` typed.
- `internal/agent.TestWorktreeAuditReclaimDispatchReclaimsUnstartedWork`
  runs the route through the agent surface against real git.
- `python3 scripts/generate-agent-contracts.py --check` is clean.

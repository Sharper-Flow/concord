# CD-0135: A worktree removal relocates occupying sessions instead of refusing

- CD number: CD-0135
- Status: accepted
- Home Domain: product-root:concord
- Affected Domains: product-root:concord, durable-authority, agent-surface
- Law type: decision
- Date: 2026-09-11
- Owner: work-1d0676d0ea6fa1e14b2b7592

## Context

Issue #722 gave worktree removal an occupancy gate: `worktree_reclaim` and
`worktree_destroy` refused while a live host session ran inside the worktree,
because the host resolves a session's directory once per prompt and a removed
directory leaves the session failing every later prompt with no statement of
cause. The refusal's recovery text said "end that session, or move it out of
the worktree".

Two facts broke that design in practice.

First, the host session list is persisted history, not liveness. A worker lane
runs as a child Task session anchored to the work item's worktree, and its
record names that directory forever after the lane finishes. The gate therefore
read finished sessions as occupants.

Second, no route existed for the caller to move another session. `session_vacate`
moves only the calling session, and the refusal offered the operator no typed
action. A finished worker session cannot be ended again and cannot be prompted
to vacate itself, so the refusal was a dead end.

The observed consequence: after any lane dispatch, `worktree_reclaim` and
`worktree_destroy` refused permanently through the non-destructive route, and
terminal worktrees accumulated. A completed work item with a merged branch and
a clean tree stayed unreclaimable because a finished child session's record
still named the directory.

A separate defect compounds this one and is recorded here for scope clarity:
nothing enforces the reverse direction of the worktree-claim relation. Three
work items once held verified claims on one path and one branch. The
enforcement of that invariant — deriving the claim's branch and path from the
work item and indexing them uniquely — belongs to the work item that owns
claim convergence (work-ac978026965355e534e583bb, AC3), not to this decision.

## Decision

### D1. A removal returns a relocation step, not a refusal

When host sessions occupy the worktree, the removal removes nothing and
returns the typed `worktree_relocation_required` step. The step carries every
occupying session and the core-derived registered main checkout, the same
destination `session_vacate` derives from the Project's canonical path
locator. The step is retry-safe: nothing was removed, so the same request
repeats freely once the sessions are elsewhere.

### D2. The adapter relocates and retries

The adapter owns which sessions are live and how they move; the core owns the
worktree path and the destination. On receiving the relocation step, the
adapter moves each named session to the derived destination through the host's
move-session route, reads the landing back from the host rather than assuming
it, and retries the removal once with a fresh observation. A move that fails
or lands elsewhere stops the removal with a typed error: the worktree still
stands, so nothing is stranded and the retry stays safe. A retry that still
names an occupant surfaces as an operator decision, because the host refused
or reverted the move and no caller route remains.

### D3. No approval skips the step

The destructive tier's operator approval covers the clean-tree and
merged-branch gates, which protect committed and uncommitted work. It never
skips the relocation step, because no approval authorizes stranding a session.
The step sits below the already-absent branch, so stale-claim recovery stays
reachable.

### D4. The host observation input stays

`observed_session_directories` remains the removal inputs' host-truth source.
The store cannot hold liveness: no event records a session leaving a
directory, so a stored answer would go stale silently. The input's meaning
changes from "the gate that refuses" to "the observation the relocation step
decides on".

## Consequences

- A worktree that ever hosted a lane dispatch is reclaimable again. The
  finished worker session is relocated by the adapter, not waited out.
- The operator sees no dead-end refusal for occupied worktrees. The typed step
  either completes automatically or names the session the host would not move.
- The `worktree_ownership_conflict` failure kind is removed. Its single
  producer became the relocation step.
- The agent envelope schema gains the structured `worktree_relocation` member,
  mapped to outcome kind `operation_conflict` with retry-safe semantics.
- The CLI path, which has no host to ask, omits the observation and reaches
  the git gates alone, unchanged from issue #722.

## Verification

- A reclaim with an occupying session returns the relocation step naming every
  occupant and the registered main checkout, removes nothing, and leaves the
  claim active.
- A destructive destroy with an operator approval still returns the
  relocation step.
- A session elsewhere, and a path that merely shares a prefix, leave the
  removal alone.
- The adapter relocates the named sessions, verifies the landing, and retries
  once; a refused or mislanding move stops the removal with a typed error and
  a surviving worktree.

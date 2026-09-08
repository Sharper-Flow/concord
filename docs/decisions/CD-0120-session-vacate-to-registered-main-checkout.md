# CD-0120: A session can vacate its worktree to the registered main checkout

- **Status:** Accepted
- **Date:** 2026-09-08
- **Scope:** The session move surface beyond work start; the occupancy gate's
  vacate path; `cmd/concord` session-prepare and the adapter move route;
  issue #950
- **Approval:** The operator approved this decision in-session on 2026-09-08
  after the manual relocation it removes.
- **Related:** CD-0092, CD-0096, CD-0098, CD-0103, issue #722, issue #950
- **Amends:** CD-0098 D1/D2 at the move scope only
- **Preserves:** CD-0096 D2 path ownership, CD-0096 D3 occupancy protection,
  and the CD-0098 read-back and no-fallback rules

## Context

CD-0098 D1 moves a session at work start, forward, into the claimed worktree
of the work the session captured or resumed. CD-0098 D2 binds the move route
to that operation and permits no fallback. No route moves a session back.

CD-0096 D3 refuses a worktree removal while a live session runs in the
worktree, because removing that directory leaves the session unable to send
another prompt. The refusal is correct, and it leaves one motion with no
owner: a session that completed its work cannot leave the worktree it
occupies, so the operator must relocate the host session by hand before the
reclaim can pass. This happened on 2026-09-08: session
`ses_f7d2e7524ffe2V2igjC9McmEI5` completed `work-15563aecb6c5c019e1fb66bc`,
the reclaim refused on occupancy, and the operator moved the session manually.

## Decision

### D1. One new move surface: vacate to the registered main checkout

A typed operation moves the calling session from its linked worktree to the
registered Project default checkout. The core derives the destination from
the registered Project; the caller names no path. The operation uses the
existing OpenCode move-session route through the adapter, with the same
read-back verification and the same refusal on a destination mismatch that
CD-0098 D3 requires. The move takes effect from the next turn, as amended by
CD-0103.

### D2. The move is authority-decreasing only

The operation's only destination is the registered default checkout, where
CD-0092 refuses implementation-bearing authority. A session therefore cannot
use the vacate operation to reach a stronger position; it can only reach a
weaker one. A move to another work item's worktree stays bound to work start
and worktree claim, where a recorded claim anchors it.

### D3. The vacate composes with the occupancy gate

After a vacate, the caller reports the observed session directories honestly
and the session no longer occupies the worktree, so the CD-0096 D3 gate
passes the reclaim on its own terms. The vacate never removes a directory; it
relocates the session before any removal is requested.

### D4. A vacate records a durable event

The operation records a work-item event naming the actor, the destination,
and the read-back result, so the trace shows why the session left and where
it landed. The event changes no lifecycle state and opens no step.

## Acceptance Criteria

```gherkin
Given a session that runs in a terminal work item's linked worktree
When the session submits the vacate operation
Then the core derives the registered default checkout as the only destination
And the adapter moves the session and verifies the landing
And the move takes effect from the next turn.

Given a session that vacated its worktree
When the session submits worktree reclaim with the honest session observation
Then the occupancy gate passes because no session occupies the worktree.

Given a vacate request that names a destination
When the operation is validated
Then it refuses before any effect because the caller names no path.

Given a session that runs in the main checkout
When the session submits the vacate operation
Then it refuses because the session has no linked worktree to leave.

Given a completed vacate
When the trace is read
Then it names the actor, the derived destination, and the verified landing.
```

## Consequences

- A completed session frees its own worktree without operator relocation;
  the 2026-09-08 manual step becomes a typed operation.
- The move surface grows beyond work start, which CD-0098 D2 forbade; this
  record amends that clause to admit one authority-decreasing exception.
- The adapter gains a third caller of the move route beside work start and
  worktree claim, with the same failure contract.
- Agents gain no path-naming power; CD-0096 D2 ownership is unchanged.

## Verification

- The core unit tests cover destination derivation, the main-checkout
  refusal, and the no-path-input refusal.
- The adapter tests cover the move call, the read-back mismatch refusal, and
  the honest observation after landing.
- A scenario exercises vacate-then-reclaim end to end on a terminal work
  item.

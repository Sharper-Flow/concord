---
{
  "id": "CD-0098",
  "title": "Retarget the running session at work start instead of launching one",
  "authority": {
    "tier": "legislated",
    "legislated_by": "work-8d7bd788b2890355460bb5b7",
    "contract_version": 1
  },
  "home_domain_id": "agent-adapter",
  "applies_to_domain_ids": []
}
---
# CD-0098: Work start retargets the running session instead of launching one

- **Status:** Accepted
- **Date:** 2026-09-02
- **Scope:** The `concord_work_start` route, the launch path it owned, and the
  identity checks that guard a retarget; issue #689
- **Approval:** The operator approved this replacement in-session on 2026-09-02
  and directed that a worktree never requires a relaunch.
- **Related:** CD-0088, CD-0092, CD-0093, CD-0096, CD-0103,
  CD-0104, issue #689
- **Preserves:** The `session-prepare` directory refusal, the primary definition
  check, and the read-back identity assertion

## Context

CD-0088 D4 binds launch authority to a prepared worktree and agent. The host
launches a named agent with the worktree as its process directory, then refuses
success unless the read-back identity matches.

The launch route produces a session the operator did not ask for. The work
starts somewhere the operator is not, and the session that requested the work
keeps no part in it. The operator rejected that outcome and required that a
worktree never costs a relaunch.

OpenCode moves a running session between directories through an experimental
control-plane route. The session keeps its history, its identity, and its
current turn. That route did not exist when CD-0088 was accepted.

## Decision

### D1. Work start moves the calling session

`concord_work_start` captures or resumes the work item, claims its canonical
worktree, and retargets the calling session to that worktree. It creates no
session, launches no agent, and starts no process.

The route keeps its store order. The worktree claim is recorded before the
session moves, so a failed move leaves a claim the session can resume, never a
moved session with no claim.

### D2. The move route is required, and its absence fails closed

The retarget uses the OpenCode move-session route. Concord requires it now. When
the route is absent, `work_start` refuses with a typed failure that names the
missing route and the host version.

No fallback runs. Concord does not launch a session, spawn a process, or
continue in the current directory when the move is unavailable.

### D3. The move has preconditions, and the result is verified

The default checkout must be clean before the move. The move carries no
changes. Uncommitted work stays where it is, and Concord refuses rather than
moving a dirty tree it did not author.

The retarget takes effect from the next turn: a running turn resolves its paths
before the move lands. After the move, `work_start`
reads the session directory back and refuses success unless it is the claimed
worktree.

### D4. Identity guarantees now guard the retarget

`session-prepare` still refuses
a directory other than the active claimed worktree, still checks the installed
primary definitions, and still obtains the core-derived session packet. Those
checks now guard the destination of a move instead of the directory of a launch.

The read-back assertion is preserved in D3. The agent identity assertion is
unchanged: the moved session is the same session, so its agent identity cannot
be substituted by the move.

### D5. One route reaches the worktree claim

`concord_work_start` is the single route to claim and verified placement,
and it replays to convergence on its derived identity.

### D6. The launch path is removed

The move keeps its existing claim and placement authority. Native Task belongs
to the host and requires no Concord dispatch authorization (CD-0242 D6).

## Consequences

- The operator stays in the session that requested the work. The worktree
  changes under it, and no second terminal appears.
- Concord depends on an experimental host route. A host without it cannot start
  work, which is the stated fail-closed outcome rather than a silent downgrade.
- A dirty default checkout blocks work start until the operator resolves it.
- This record claims no replacement readiness.

## Rejected alternatives

**Launch when the move route is absent.** Rejected because a fallback reproduces
the outcome this record removes, and it does so exactly when the operator is
least able to see it.

**Move a dirty checkout by carrying changes.** Rejected because Concord did not
author that work and cannot attribute it. The refusal keeps the operator's
uncommitted work where the operator left it.

**Move without regard for the turn boundary.** Rejected because a turn that
starts in one directory and ends in another resolves paths in both. CD-0103
records why the outcome holds without a wait the route does not offer.

## Verification

- `TestWorkStartRetargetsCallingSession` proves D1, including the claim recorded
  before the move.
- The adapter test `work start refuses when the move route is absent` proves D2.
- The adapter test `work start refuses a dirty default checkout` and
  `work start verifies the session directory after the move` prove D3.
- `TestWorkStartRefusesDestinationOtherThanClaimedWorktree` proves D4.
- A repository check verifies D1's no-process boundary: no `opencode run`
  invocation remains in the adapter.
- `python3 scripts/check-json.py` and `python3 scripts/check-doc-links.py`
  pass.

# CD-0098: Work start retargets the running session instead of launching one

- **Status:** Accepted
- **Date:** 2026-09-02
- **Scope:** The `concord_work_start` route, the launch path it owned, and the
  identity checks that guard a retarget; issue #689
- **Approval:** The operator approved this replacement in-session on 2026-09-02
  and directed that a worktree never requires a relaunch.
- **Related:** CD-0059, CD-0088, CD-0092, CD-0093, CD-0096, CD-0102, CD-0103,
  CD-0104, issue #689
- **Amended by:** CD-0103 at the D3 turn-end clause; CD-0104 D3 at the failure
  contract, which removes the `partial` outcome and the launch record
- **Amends:** CD-0096 D6 and CD-0088 D4 at their launch clauses
- **Preserves:** The `session-prepare` directory refusal, the lane definition
  check, and the read-back identity assertion

## Context

CD-0088 D4 binds launch authority to a prepared worktree and agent. The host
launches a named agent with the worktree as its process directory, then refuses
success unless the read-back identity matches.

CD-0096 D6 keeps that route as the pre-launch path and adds an in-session
retarget beside it. Two routes then reach one worktree claim.

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
before the move lands. CD-0103 amends this clause, which previously claimed the
route waits for a turn boundary it does not offer. After the move, `work_start`
reads the session directory back and refuses success unless it is the claimed
worktree.

### D4. Identity guarantees now guard the retarget

CD-0088 D4 is amended at its launch clause only. `session-prepare` still refuses
a directory other than the active claimed worktree, still checks the installed
lane definitions, and still obtains the core-derived session packet. Those
checks now guard the destination of a move instead of the directory of a launch.

The read-back assertion is preserved in D3. The agent identity assertion is
unchanged: the moved session is the same session, so its agent identity cannot
be substituted by the move.

### D5. One route reaches the worktree claim

CD-0096 D6 is amended. The pre-launch route is retired, so the two routes it
reconciles are one route. The convergence guarantee it stated is now structural:
a second owner cannot arise from a route that no longer exists.

### D6. The launch path is removed

The `opencode run` invocation, its argument construction, its child session
export, and its stream recovery are removed with the route they served. CD-0059
keeps its authorize-before-start rule, which now applies to the move and to lane
dispatch under CD-0102.

## Consequences

- The operator stays in the session that requested the work. The worktree
  changes under it, and no second terminal appears.
- Concord depends on an experimental host route. A host without it cannot start
  work, which is the stated fail-closed outcome rather than a silent downgrade.
- A dirty default checkout blocks work start until the operator resolves it.
- Removing the launch path removes child session export and stream recovery, and
  with them the failure class where a launched session is lost after it starts.
- CD-0088 D5 is untouched. This record claims no replacement readiness.
- The floor manifest moves with the route. `fc1-session-handoff` is satisfied
  today by a launch test, so this record rewrites that item instead of leaving a
  satisfied condition whose evidence the change deletes.

## Rejected alternatives

**Keep both routes.** Rejected because two routes to one claim is the condition
CD-0096 D6 had to reconcile. Retiring one removes the reconciliation instead of
maintaining it.

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
- A repository check proves D6: no `opencode run` invocation remains in the
  adapter.
- `docs/floor-readiness.v1.json` states `fc1-session-handoff` as a retarget
  requirement, and its evidence names the retarget tests. The launch anchor
  `cmd/concord.TestSessionBootPassesCorePacketToOpenCodeBeforeSessionStarts` is
  removed with the route it proved, so no satisfied item cites deleted evidence.
- `python3 scripts/check-floor-readiness.py` passes, which it cannot do while an
  evidence anchor names a test this record deletes.
- `docs/law-coverage.v1.json` records CD-0098 as proved once the tests above
  pass.
- `python3 scripts/check-doc-contract.py`, `python3 scripts/check-json.py`,
  `python3 scripts/check-doc-links.py`, `python3 scripts/check-knowledge-index.py`,
  and `python3 scripts/check-cd-allocation.py` pass.

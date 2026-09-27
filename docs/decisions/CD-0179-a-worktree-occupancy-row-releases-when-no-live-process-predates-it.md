# CD-0179: A worktree occupancy row releases when no live process can predate it, and the reclaim limit counts attempts

- **Status:** Accepted
- **Date:** 2026-09-27
- **Scope:** When a worktree occupancy row releases, what a claim records at
  creation, where a session goes after its work ends, and what the audit
  reclaim limit bounds;
  [Concord (CON) issue 509](https://linear.app/sharper-flow/issue/CON-509)
- **Amends:** CD-0178 D3 at the stored occupant
- **Preserves:** CD-0178 D1, D2, and D4 in full, and every content gate the
  removal operations run
- **Related:** CD-0096, CD-0104, CD-0111, CD-0118, CD-0176
- **Approval:** The operator accepted the work item CON-510 contract that
  carries this record on 2026-09-27.

## Context

CD-0178 D3 records one occupancy row per session, with the host process
identity, and releases a row whose host process ended. Two gaps remain.

The first gap is the row that has no process identity. A claim wrote such a
row before that rule, and the rule left it releasable only by session_vacate
or an operator-approved removal. A terminal, clean, merged worktree then
refused reclaim with worktree_ownership_conflict forever, because the session
that recorded the row no longer exists and nobody can vacate on its behalf.

The second gap is scope. A landing released the session's rows inside its own
work item only, and a vacate released the one row at the vacated worktree. A
session that moved between work items without a landing carried a stale row
in the work item it left, and no route from its current work item could clear
that row.

Two further defects shared the same root. A claim wrote no process identity
at creation, so every claim-born row waited for its landing before any
release rule could read it. And a worktree_audit_reclaim pass truncated the
classified drift list to its limit before it separated report-only rows from
reclaimable ones, so a crowd of report-only rows could leave a pass nothing
to reclaim.

## Decision

### D1. A verified landing or a session_vacate releases every row of that session

The host runs a session in one directory at a time. A verified landing of a
session, or a session_vacate of that session, therefore proves every other
occupancy row of the same session stale, in any work item, and releases all
of them. The landing records the released source paths in its event, and the
vacate fold releases them with the vacated row. A row another session holds
is never released by a rule of this record.

### D2. A claim records the host process identity at creation

A worktree claim records the process id of the OpenCode process whose adapter
records the claim, and the core derives the process start time from the
kernel at claim time, so a claim-born occupancy row carries process identity
from creation. The work_bootstrap route carries the same identity. The agent
surface refuses a claim whose request names no host process id; the adapter
injects its own process pid, and an agent never supplies one.

### D3. A row without process identity releases on the lease-set proof

A row without process identity releases when every live host lease started
after the row's recorded_at. No process alive today existed when the row was
recorded, so the process that recorded it has ended. An unreadable lease set
releases nothing, and so does any live lease that started at or before the
row's recorded_at. The caller reads the lease set before the reclaim
transaction opens and carries the observation with the request.

### D4. A terminal transition moves the calling session to the main checkout

After a lifecycle transition to completed or cancelled succeeds, or a
supersede succeeds, the adapter moves a calling session that runs in that work
item's worktree to the registered main checkout. The core names the vacate
target on the transition result only when the calling session's linked
worktree is that work item's active worktree. The adapter then runs
session_vacate, which releases the occupancy rows, and moves the session to
the derived destination. A failed vacate or move never undoes the transition;
the adapter reports it and the session can replay session_vacate.

### D5. The audit reclaim limit bounds attempts, not classification

A worktree_audit_reclaim pass classifies every drift row before its limit
applies. The limit bounds reclaim attempts only: a report-only row never
consumes the limit, and a reclaimable row beyond the limit stays classified
and reported unattempted. The worktree_audit read keeps its own limit on its
report.

### D6. The content gates do not change

CD-0178 D4 stands. A merged terminal worktree and a clean unstarted worktree
stay eligible, uncommitted content stays report-only, and every git gate the
reclaim and destroy operations run is unchanged.

## Alternatives considered

- Release a legacy row on a lease count alone. Rejected: a count proves
  nothing about order, and a lease that predates the row names a process that
  may be the recorder.
- Release a legacy row when its own session's host process is unprovable.
  Rejected: a row without identity names no process, so no per-row proof
  exists; the lease set is the only order evidence.
- Move the session before the vacate on a terminal transition. Rejected: the
  move erases the directory the vacate resolves, so the release could never
  run.
- Attach the vacate target to the claim gate and admit a cross-work-item
  claim. Rejected: the claim still commits before the host move lands, and a
  refused move would leave a row cleared for a session still in place.
- Let the truncation stand and rerun the pass until it drains. Rejected: each
  pass costs a full classification, a crowd of report-only rows can outlast
  every rerun, and the operator sees no reclaimable row the pass skipped.
- Bound the report-only list by the limit too. Rejected: an unreported row is
  an unclassified row to the operator, which is the defect this record
  closes.

## Consequences

A terminal, clean, merged worktree whose occupant is gone reclaims in one
pass. A stale row in a work item the session left releases at the next
landing or vacate, and a claim-born row is releasable from creation without
waiting for a landing. A worktree whose occupant session is live is still
never removed by a reclaim or an audit pass: its row carries identity, its
process is provable, and a legacy row under a live recorder stays on the
predating lease.

The lease-set proof converts a wall-clock question into an order question,
so its answer is only as good as the recorded_at each row carries and the
lease set the caller reads. A row whose recorded_at is wrong releases early
or late by that error; a lease set that is unreadable at read time releases
nothing, which is the safe direction.

The terminal-transition move adds one core call and one host move after a
completed, cancelled, or superseded transition from a worktree. A session
that ran the transition from the main checkout moves nowhere, and the
transition result of every other caller is unchanged.

The reclaim pass reports more than it acts on by design. Its report-only list
now names every classified row the pass did not attempt, including
reclaimable rows beyond the limit, so the next pass or the operator can act
on them.

## Verification

- `go test ./internal/store/` proves the release rules: a claim with a host
  pid records process identity, a vacate and a landing release rows in every
  work item, a legacy row releases when every live lease started after its
  recorded_at, an unreadable lease set releases nothing, and a predating
  lease keeps the row.
- `go test ./internal/store/ -run TestAuditReclaimLimitCountsReclaimAttemptsOnly`
  proves the limit bounds attempts while every report-only row stays
  classified.
- `bun test adapter/opencode/terminal-vacate.test.ts` proves the terminal
  transition vacates and moves the session, skips every caller without a
  vacate target, and keeps the transition on a failed move.
- `python3 scripts/check-doc-contract.py` passes over the registered corpus
  with this record on the current profile.
- `python3 scripts/check-knowledge-closure.py` and
  `python3 scripts/check-knowledge-index.py` prove this record is registered
  and no unprocessed document remains.

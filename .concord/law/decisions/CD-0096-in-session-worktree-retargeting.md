# CD-0096: Allow in-session worktree retargeting under tiered authority

- **Status:** Accepted
- **Date:** 2026-09-01
- **Scope:** The persistent worktree target of a running session; tiered
  authority for same-Project access to another work item's worktree; the
  session-directory clause of CD-0093 D2; issue #689
- **Approval:** The operator approved the tiered authority model in session on
  2026-09-01 (issue #689).
- **Related:** CD-0088, CD-0090, CD-0092, CD-0093, CD-0094, CD-0095, CD-0104,
  issue #689

**Approval amendment:** [CD-0243 D6](CD-0243-four-roles-and-six-operator-approval-classes.md#d6-existing-approval-requirements-follow-these-classes)
limits this record's operator-consent requirements to its six classes; its D5 replaces an unconditional recovery veto. Occupancy, identity, and other non-consent checks remain required.

## Context

CD-0088 gives the host one route into a canonical worktree. `concord_work_start`
captures or resumes the work item, creates and claims the worktree, and launches
the child session in that directory. The authority envelope binds before launch,
so the route exists only before a session starts.

A running session has no such route. A session that starts in a registered
Project's default checkout can define work there under CD-0092, and CD-0092
still refuses implementation-bearing capabilities there. When the current work
item needs implementation writes, the operator must end the session and start a
new one. The session's context does not survive that move.

CD-0093 D2 states the obstacle as law: "The directory D1 resolves is established
first and is the only directory the session uses." Read as a lifetime clause, it
forbids a later directory change even when the new target is the canonical
worktree of the session's own current work item.

Two properties of that clause must survive any amendment. The host process
directory cannot change, because the host process keeps the working directory it
started with. And the identity verified at start was verified against the start
directory, so re-deriving it against another directory would assert an identity
that was never checked.

What is missing is a separate binding: the tree that later Concord operations
resolve. This record names that binding the effective target, makes it
retargetable along one typed route, and tiers the authority for touching any
other worktree in the same Project.

Work start moves the host session. Later Concord operations use the
host-reported directory and the current claim, with the tiered authority in D3.

## Decision

### D1. A session may adopt its current work item's canonical worktree as the effective target

There is no stored effective target and no retarget
operation. A session enters its work item's canonical worktree through
`concord_work_start`, which moves the session (CD-0098), and later Concord
operations resolve the tree from the directory the host reports per call.

### D2. The effective target comes from identity, never from input

No operation in this record accepts a worktree path from a caller. Work start
derives the canonical worktree from the registered Project locator and the
current work identity, through the same `worktree-locate` policy CD-0088 D2
names. A directory the store cannot derive does not exist as a target.

### D3. Same-Project cross-worktree authority is tiered

Access to a worktree of another work item in the same Project follows three
tiers. Each tier is a typed authority, and no tier implies the one above it.

**Inspect.** Reading files, Git status, and diffs is allowed against any active
worktree in the same Project. Inspection is read-only and never moves the
session.
Concord does not execute repository checks, issue verification leases, retain
their output, or expose an oracle-output reader. Verification follows
[CD-0242 D8](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d8-evidence-is-captured-by-its-producer-and-bound-to-its-subject).

**Take over.** There is no typed transfer, because
there is no stored holder to transfer from. A session that needs to edit
another work item's worktree starts that work item.

**Destroy.** Normal reclaim removes merged terminal work under the
CD-0095 store gates. A dirty tree refuses, and an unmerged branch refuses.
The trigger for merged terminal work is the audit pass (CD-0105), which
CD-0118 extends to unstarted work; this tier defines the gates, not the trigger.
Destructive destroy discards local development content without the clean-tree
and merged-branch checks. Repository identity, protected-default, changed-tip,
foreign-checkout, and occupancy checks still apply.

### D4. Possession grants no Product authority

Holding, claiming, or targeting a worktree changes no capability boundary.
Authority still flows only from the typed operation grants CD-0092 and CD-0094
define. Moving a session into a canonical worktree grants no Product authority.

### D5. Continuity carries no verification lease

The pinned continuity projection carries no verification lease.

### D6. The bootstrap route and its identity guarantees are unchanged

`concord_work_start` is the one route, and it replays
to convergence on its derived key. `session-prepare` still refuses a directory
other than the active claimed worktree and records nothing. Preparation
validates the destination, and work start verifies the landing.

## Consequences

- A session can move from Product-state work in the default checkout to
  implementation in the canonical worktree without ending. Later operations
  use the host-reported directory and current claim.
- Reading and verifying another session's worktree need no transfer, so review
  and verification do not wait on ownership inside one Project.
- An ownership conflict surfaces as a typed refusal that names the owner and
  the recovery action, not as a write to a tree someone else holds.
- Issue #683 keeps its launcher drill-down scope. This record adds no launcher
  surface.
- The runtime surface is outstanding. Issue #689 tracks it, and the coverage
  record carries that pointer.

## Rejected alternatives

**Restart the session in the worktree.** Rejected because it is the current
behavior. It ends the session, discards its context, and spends a bootstrap to
reach a tree the store can name.

**Accept an operator-supplied worktree path.** Rejected because a path input
defeats canonical derivation. Ownership and recovery cannot name a tree the
store never registered, and CD-0088 D2 already refuses recomputing path policy
at the point of use.

**Grant authority by possession.** Rejected because it collapses the tiers. Any
session holding a worktree would bypass the CD-0092 capability classes, and
takeover would be meaningless.

**Adopt one flat cross-worktree permission.** Rejected because it must refuse
inspection to stay safe, or admit writes to stay useful. Reading, verifying,
and editing a tree are different risks, and the tiers keep them separated.

**Re-derive the identity trio against the worktree after a retarget.** Rejected
because it re-opens the substitution class CD-0093 closed. A worktree checkout
can carry its own agent definitions, so identity that resolves through the
retargeted tree is an identity no one verified.

## Verification

- Inspection of any active same-Project worktree succeeds read-only.
- Reclaim obeys the CD-0095 store gates for terminal work and the unstarted
  gate CD-0118 defines.
- The continuity projection carries no verification lease.
- `python3 scripts/check-json.py`, `python3 scripts/check-doc-links.py`, and
  `python3 scripts/check-knowledge-index.py` pass on this record.

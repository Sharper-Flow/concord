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
- **Amends:** CD-0093 D2 at the session-directory clause only
- **Amended by:** CD-0104 at Context, D1, D3 Take over, D5, and D6. The
  effective target was a stored copy of the session directory, kept because
  the session could not move. CD-0098 moved the session, and CD-0104 removed
  the copy. D2, D3 Inspect, Verify, and Destroy, and D4 stand.

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

*Amended by CD-0104.* The first property did not survive CD-0098, which moves
the host session. Once the directory can change, the effective target is a
stored copy of a fact the host reports, and CD-0104 D1 removes it. The second
property stands, and the tiers in D3 stand with it.

## Decision

### D1. A session may adopt its current work item's canonical worktree as the effective target

*Amended by CD-0104 D1.* There is no stored effective target and no retarget
operation. A session enters its work item's canonical worktree through
`concord_work_start`, which moves the session (CD-0098), and later Concord
operations resolve the tree from the directory the host reports per call. The
original clause follows for the record.

A running session bound to a current work item may create or claim that item's
canonical worktree and retarget the session's effective target to it. The
retarget is persistent. Later Concord operations that resolve a tree resolve
through the effective target, and the host process directory stays unchanged.

CD-0093 D2 is amended at this clause only. The directory its D1 resolves remains
the one directory for agent definition resolution, the registry probe, and host
execution, and it is still established first. The effective target is a distinct
binding, initially the resolved start directory, and this amendment adds the one
typed route that changes it.

### D2. The effective target comes from identity, never from input

No operation in this record accepts a worktree path from a caller. The retarget
derives the canonical worktree from the registered Project locator and the
current work identity, through the same `worktree-locate` policy CD-0088 D2
names. A directory the store cannot derive does not exist as a target.

### D3. Same-Project cross-worktree authority is tiered

Access to a worktree of another work item in the same Project follows four
tiers. Each tier is a typed authority, and no tier implies the one above it.

**Inspect.** Reading files, Git status, and diffs is allowed against any active
worktree in the same Project. Inspection is read-only and never changes the
persistent effective target.

**Verify.** Tests, validators, builds, and reproductions run against a
same-Project worktree under an exclusive lease. Tracked files must remain
unchanged. Completion refuses when they changed, because a verifier that edits
its subject verifies nothing.

**Take over.** *Amended by CD-0104 D5.* There is no typed transfer, because
there is no stored holder to transfer from. A session that needs to edit
another work item's worktree starts that work item. Two sessions in one
worktree are refused only at the command level, by the Verify lease. The
original clause follows for the record.

Editing, committing, pushing, and retargeting to another work item's worktree
require a typed authority transfer. An active owner must release its
authority, or the operator must approve an override. A takeover refused for
authority fails typed, naming the owner identity and the recovery action.

**Destroy.** Removing a worktree reclaims merged terminal work under the
CD-0095 store gates. A dirty tree refuses, and an unmerged branch refuses.
Removal of non-terminal work, and any destructive removal, requires operator
approval.

### D4. Possession grants no Product authority

Holding, claiming, or targeting a worktree changes no capability boundary.
Authority still flows only from the typed operation grants CD-0092 and CD-0094
define. A session retargeted into a canonical worktree holds the same authority
it held before the retarget.

### D5. Continuity re-pins the target and the lease

*Amended by CD-0104 D1.* The pinned continuity projection carries the reading
session's active verify leases and no target. There is no target version to
pin. The original clause follows for the record.

The pinned continuity projection carries the effective target and any active
lease. The CD-0090 per-turn re-pin re-asserts both each turn. A Concord
operation whose pinned target version is stale fails closed with a typed
refusal, because a silently re-derived target is a directory change no one
authorized.

### D6. The bootstrap route and its identity guarantees are unchanged

*Amended by CD-0104 D3.* `concord_work_start` is the one route, and it replays
to convergence on its derived key. `session-prepare` still refuses a directory
other than the active claimed worktree and records nothing. There is no
in-session route to converge with. The original clause follows for the record.

CD-0088 keeps its whole authority envelope. `concord_work_start` remains the
pre-launch route, `session-prepare` still refuses a directory other than the
active claimed worktree, and the read-back identity check is unchanged. The
in-session route converges on the same stored worktree claim, so the two routes
cannot produce two owners.

## Consequences

- A session can move from Product-state work in the default checkout to
  implementation in the canonical worktree without ending. The retarget is
  durable and survives context boundaries through the per-turn re-pin.
- The host process directory and the effective target stop being the same
  thing. Host surfaces that read the process directory keep reading the start
  directory. This record changes what Concord operations resolve, and the gap
  is named here rather than hidden.
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
- Verification under an exclusive lease refuses completion when tracked files
  changed.
- Destroy obeys the CD-0095 store gates for terminal work, and operator
  approval gates every other removal.
- The continuity projection re-pins the active lease each turn.
- `python3 scripts/check-doc-contract.py`, `python3 scripts/check-json.py`,
  `python3 scripts/check-doc-links.py`, `python3 scripts/check-knowledge-closure.py`,
  and `python3 scripts/check-knowledge-index.py` pass on this record.

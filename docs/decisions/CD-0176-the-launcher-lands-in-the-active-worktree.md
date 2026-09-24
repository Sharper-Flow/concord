# CD-0176: The launcher lands in the active worktree

- **Status:** Accepted
- **Date:** 2026-09-24
- **Scope:** Where a launcher-started session for selected work runs when the
  work holds an active worktree in its primary Project; the session landing
  directory and everything that directory governs
- **Amends:** CD-0093
- **Related:** CD-0008, CD-0088, CD-0103
- **Approval:** The operator approved the objective in the Concord (CON)
  work contract CON-448 (v1). The pull request is the public record.
- **Preserves:** CD-0093 D2's one-directory binding, CD-0093 D3's fail-closed
  canonical path, CD-0093 D4's fixed host command

## Context

CD-0093 D1 lands every work-selected session in the Project canonical path.
That decision predates the native worktree workflow it now serves. When
Concord claims a worktree for a work item (CD-0008), the work lives in the
worktree, and the canonical path is the repository's main checkout.

An operator who resumes such a work item through the launcher lands in the
main checkout. The session must then reach its worktree through a
`concord_work_start` resume, which moves the session in a later turn (CD-0088
owns the worktree bootstrap). The move costs a turn and a directory change
after the host already started.

The launcher holds both facts at start: the selected work and the work's
active worktree. `worktree_entries` records the folded on-disk state, and the
unique active claim per work and Project pins one path. Git itself treats a
linked worktree as a full working tree of the same repository, so the path
serves as a project root exactly as the canonical path does.

The adapter keeps its own landing guard for sessions it starts. That guard is
unchanged here. Landing a captured work item on the capture turn itself stays
out of scope under CD-0103.

## Decision

### D1. The session lands in the active worktree when one is usable

When the selected work holds an active worktree in its primary Project, and
that worktree is a usable directory on the machine the launcher runs on,
`concord session` starts the host in the worktree. `concord zl` forwards to
the session command, so both surfaces land in the worktree.

The mechanism is the CD-0093 D1 mechanism, unchanged: the child process
working directory. The session directory read returns two candidates, the
Project canonical path and the active worktree path, and the session command
starts in the worktree when the worktree candidate is usable.

### D2. The Project canonical path stands without a usable worktree

The worktree candidate needs three facts: an active entry under a verified
claim, in the primary Project, at a path that is a usable directory on this
machine. An absent entry, a reclaimed entry, an active entry under a
reclaimed claim, an entry in a Project other than the primary one, and a
path missing on this machine each remove the candidate. The session
then lands in the Project canonical path exactly as CD-0093 D1 decides.

The fallback is not a failure and carries no diagnostic. The canonical path
is the landing CD-0093 already admits. A worktree the store records but the
machine does not hold is a worktree without a landing.

### D3. One directory, fixed host, and fail-closed canonical path stand

CD-0093 D2 binds unchanged. The one landing directory governs agent
definition resolution, the host registry probe, and host execution, and it
resolves before identity verification. CD-0093 D3 binds unchanged for the
canonical path. A canonical path that does not resolve refuses the launch
with a typed diagnostic. CD-0093 D4 binds unchanged: the host command stays
fixed.

The landing choice happens inside the existing resolution, so no launch step
reads a second directory, a new setting, or the process working directory.

## Alternatives considered

- Land in the worktree from the store record alone, without the on-disk
  check. Rejected: the launcher would start the host in a path the machine
  does not hold, and the session would refuse after the operator chose the
  work.
- Refuse the launch when the active worktree is missing on disk. Rejected:
  the canonical path is a valid landing under CD-0093, and a missing worktree
  is a worktree absence, not a resolution failure.
- Move the host to the worktree after start. Rejected: that is the multi-turn
  move this record removes, and a directory change after identity
  verification is the substitution class CD-0093 D2 closes.
- Resolve the worktree landing in the adapter. Rejected: the adapter's
  landing guard verifies the directory Concord already sent, and a second
  resolver would own the same fact twice.
- Extend the landing to a captured work item on the capture turn. Out of
  scope under CD-0103 and left to its own record.

## Consequences

An operator who resumes work that holds an active worktree starts in that
worktree, and `concord_work_start` resume lands in the first turn with no
move. Work without a worktree keeps the CD-0093 landing. The session
directory read returns the two candidates, so a caller that needs the plain
canonical path can read it directly.

A worktree recorded active but deleted out-of-band lands its session in the
canonical path silently. The next worktree audit or claim retarget owns that
state, and the launcher does not repair it here. A worktree in a Project
other than the primary one never lands a session, because the primary Project
owns the session directory under CD-0093 D1.

No schema, migration, event, or operator setting changes. The adapter is
untouched.

## Verification

- `go test ./internal/store/ -run TestResolveSessionDirectory` proves the
  read: the active worktree path travels beside the canonical path, and a
  reclaimed entry, an active entry under a reclaimed claim, an entry in a
  foreign Project, and no claim each leave the canonical path alone.
- `go test ./cmd/concord/ -run TestSession` proves the launch: the host
  starts in the active worktree when it exists on disk, and in the Project
  canonical path when it does not, with the registry probe and execution in
  the one landing directory.
- `python3 scripts/check-doc-contract.py`,
  `python3 scripts/check-knowledge-index.py`,
  `python3 scripts/generate-knowledge-index.py --check`,
  `python3 scripts/check-knowledge-closure.py`, and
  `python3 scripts/check-cd-allocation.py --no-fetch` pass.

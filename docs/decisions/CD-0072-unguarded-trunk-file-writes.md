# CD-0072: File-write trunk protection is accepted as unguarded

- **Status:** Accepted
- **Date:** 2026-08-24
- **Scope:** Trunk file-write enforcement; the predecessor-coverage record for
  worktree isolation; issues #317, #130
- **Approval:** Operator decision of 2026-08-21 accepted losing this
  enforcement rather than shipping an editor plugin to retain it; recorded
  here on that authority (surfaced by issue #317)
- **Related:** #130 (worktree isolation as external safety authority),
  CD-0008, [`capability-placement.md`](../capability-placement.md) §1,
  [`predecessor-operational-coverage.md`](../predecessor-operational-coverage.md)
- **Preserves:** the grant-level main-checkout refusal
  (`internal/agent/authority.go`), the one-active-worktree-per-Project refusal
  (`internal/store/worktrees.go`), and reclamation derived from git facts
- **Supersedes:** nothing; records an exclusion the coverage table implied
  was covered

## Context

The predecessor enforced trunk protection in two halves. The first —
refusing a *mutating grant* bound to the main checkout — is Concord law and
code: `authority.go` refuses the grant before any operation runs. The second
— intercepting raw file writes (edit, write, delete) in the trunk checkout
through an editor-host hook before execution — the predecessor shipped as a
plugin in its own tooling.

Concord has no surface that could hold that hook.
[`capability-placement.md`](../capability-placement.md) §1 enumerates
Concord's surfaces — durable state, agent tool operations, the operator CLI,
generated agent definitions, host scripts for standalone cross-tool
executables — and an interception point inside another tool's write path is
none of them: a host script cannot intercept a call another tool makes.

On 2026-08-21 the operator chose to accept losing this enforcement rather
than ship an editor plugin to retain it. The choice was never recorded, and
the coverage row for worktree isolation read as though the full predecessor
guarantee stood.

## Decision

### D1. Raw file-write trunk protection is excluded, deliberately

No Concord component guards ordinary file writes to the trunk checkout made
through host tooling. The enforcement gap is accepted, not pending: no
follow-up intends to close it, because closing it requires a surface
Concord's placement rubric does not offer.

### D2. What remains in force

- Grant-level refusal: a mutating grant bound to the main checkout is
  refused before any Concord operation runs (`internal/agent/authority.go`).
- One active worktree per Project: a concurrent claim for the same set is
  refused (`internal/store/worktrees.go`).
- Reclamation derived from git facts, not caller claims
  (`internal/store/worktrees.go`).

These are weaker than the predecessor's combined guarantee and are not
presented as equivalent. An operator wanting trunk protection at the file
level must configure it host-side; Concord records the boundary honestly
instead.

### D3. The coverage record separates the halves

The worktree-isolation outcome row splits its enforced evidence from this
exclusion and cites this record, so the table stops overstating what is
enforced.

## Invariants

1. No Concord change introduces an editor-host write-interception hook.
2. The grant-level main-checkout refusal is not weakened to make the halves
   read consistently.

## Verification

- `scripts/check-predecessor-coverage.py` passes with the split row and the
  updated Excluded tally.
- The grant refusal and worktree claim refusals keep their existing tests.

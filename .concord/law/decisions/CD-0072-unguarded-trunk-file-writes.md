# CD-0072: File-write trunk protection is accepted as unguarded

- **Status:** Accepted
- **Date:** 2026-08-24
- **Scope:** Trunk file-write enforcement and worktree isolation; issues #317, #130
- **Approval:** Operator decision of 2026-08-21 accepted losing this
  enforcement rather than shipping an editor plugin to retain it; recorded
  here on that authority (surfaced by issue #317)
- **Related:** #130 (worktree isolation as external safety authority),
  CD-0008, [`capability-placement.md`](../../docs/capability-placement.md) §1
- **Preserves:** the grant-level main-checkout refusal
  (`internal/agent/authority.go`)

## Context

The predecessor enforced trunk protection in two halves. The first —
refusing a *mutating grant* bound to the main checkout — is Concord law and
code: `authority.go` refuses the grant before any operation runs. The second
— intercepting raw file writes (edit, write, delete) in the trunk checkout
through an editor-host hook before execution — the predecessor shipped as a
plugin in its own tooling.

Ordinary file-write protection belongs to the host that owns write execution.
Concord does not intercept ordinary writes by their target or checkout.

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
- Host tools own worktree isolation and removal (CD-0088 D1).

These are weaker than the predecessor's combined guarantee and are not
presented as equivalent. An operator wanting trunk protection at the file
level must configure it host-side; Concord records the boundary honestly
instead.

## Invariants

1. Concord does not intercept ordinary file writes by their target or checkout.
   Native tool permissions remain host-owned; Concord supplies no filesystem sandbox.
2. The grant-level main-checkout refusal is not weakened to make the halves
   read consistently.

## Verification

- The grant refusal keeps its existing tests.

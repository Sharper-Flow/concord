# CD-0104: A session's worktree is its actual directory

- **Status:** Accepted
- **Date:** 2026-09-03
- **Scope:** Per-call host directory, transient exclusion, and verification leases
- **Related:** CD-0088, CD-0092, CD-0093, CD-0096, CD-0103
- **Preserves:** CD-0096 D2, D3 Inspect and Verify, and D4

## Context

Authorization resolves the Project and main-checkout flag from the calling
directory through Git at every call. A stored copy of that host fact drifts.

## Decision

### D1. The session's worktree is the directory it runs in

The directory a session runs in is the directory the host reports for it,
read per call from the host's tool context. No Concord operation sets an
effective session target ahead of the host. `session-prepare` performs
verification and boot-packet derivation and records nothing.

### D2. Concord stores what Concord owns

Work items and command verification leases remain Concord facts. Retained
entry and claim metadata supports native verification, inspection, continuity,
and Review reads; it does not authorize a new ownership writer. CD-0088 D1
assigns worktree creation, placement, recovery, and removal to host tools.
This decision drops no table or persisted row.

### D4. Transient exclusion is `resource_busy`

A refusal that recorded nothing and that a retry may clear is
`resource_busy`, coupled to `retry_same_request`. The verify-lease refusal maps
to it. `operation_conflict` stays coupled to `reconcile_operation` and keeps
its meaning: an operation may have taken effect and its outcome must be
established.

### D5. One-session-per-worktree is not a stored guarantee

Two sessions may run in one worktree. The verify lease refuses two commands
on one tree, and nothing refuses two editors, which is the guarantee a shell
gives. The continuity projection re-pins held verify leases only.

## Verification

- `internal/agent.TestRetrySafeStoreKindsNeverCoupleToReconcile` proves no
  store kind proposing `retry_same_request` maps to a public kind coupled to
  `reconcile_operation`.
- `internal/agent.TestWorktreeVerifyConcurrentLeaseRefusesTyped` asserts the
  lease-held refusal is `resource_busy` with `retry_same_request`.
- `internal/store.TestContinuityRePinsHeldLease` and
  `internal/agent.TestContinuityDispatchRePinsHeldLease` assert the continuity
  projection re-pins held leases and carries no target.

# CD-0104: A session's worktree is its actual directory

- **Status:** Accepted
- **Date:** 2026-09-03
- **Scope:** Per-call host directory and transient exclusion
- **Related:** CD-0088, CD-0092, CD-0093, CD-0103
- **Preserves:** CD-0088 D3's rule that host placement grants no Product authority

## Context

Authorization resolves the Project and main-checkout flag from the calling
directory through Git at every call. A stored copy of that host fact drifts.

## Decision

### D1. The session's worktree is the directory it runs in

The directory a session runs in is the directory the host reports for it,
read per call from the host's tool context. No Concord operation sets an
effective session target ahead of the host. `session-prepare` performs
verification and boot-packet derivation and records nothing.

Each authenticated call resolves its Project from that host-reported directory.
An explicit work ID selects work context; directory resolution never supplies
one from worktree ownership. CD-0016 governs explicit continuity reads.

### D2. Concord stores what Concord owns

Work items remain Concord facts. Concord retains no worktree entry or claim
metadata and provides no worktree inspection operation. CD-0088 D1 assigns
worktree creation, placement, recovery, and removal to host tools; its D2
requires verified snapshots before table removal. Session preparation,
continuity, and Review do not acquire a replacement worktree locator service.
The host-reported directory still resolves ordinary Project authority.
Concord holds no repository-check execution lease and continuity carries none.

### D4. Transient exclusion is `resource_busy`

A refusal that recorded nothing and that a retry may clear is
`resource_busy`, coupled to `retry_same_request`. `operation_conflict` stays coupled to `reconcile_operation` and keeps
its meaning: an operation may have taken effect and its outcome must be
established.

### D5. One-session-per-worktree is not a stored guarantee

Two sessions may run in one worktree. Concord provides no verification-command
lease or editor exclusion.

## Verification

- `internal/agent.TestEveryLiteralRefusalPairsAContractualRecovery` checks
  literal refusal sites against the public recovery coupling.
- Session preparation records no placement; continuity carries no verification
  lease or effective target.
- `python3 scripts/check-json.py`, `python3 scripts/check-doc-links.py`, and
  `python3 scripts/check-knowledge-index.py` pass on this record.

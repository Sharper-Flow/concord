# CD-0176: The claimed worktree is the managed session directory

- **Status:** Accepted
- **Date:** 2026-09-24
- **Scope:** Where a managed session for selected work runs; the claimed
  directory and everything that directory governs
- **Approval:** Concord (CON) issue CON-910 (2026-10-13), under the Snowball coordinator's operator-delegated approval.
- **Related:** CD-0008, CD-0088, CD-0098
- **Approval:** The operator approved the objective in the Concord (CON)
  work contract CON-448 (v1). The pull request is the public record.
- **Preserves:** CD-0093 D2's one-directory binding and D3's fail-closed
  resolution, CD-0098 D3's verified read-back, and CD-0098 D4's
  session-prepare directory refusal

## Context

Managed work runs in its claimed worktree, because the canonical path is the repository's main checkout while
the work lives in the claimed worktree (CD-0008).

`concord_work_start`
claims the canonical worktree, moves the calling session into it, and
refuses success unless the session directory reads back as the claimed
worktree (CD-0098).

## Decision

### D1. Retired: the launch landing

A host-started session reaches the
worktree only through `concord_work_start`, which establishes one directory
through a verified claim and read-back.

### D2. Retired: the member-Project landing selector

A different Project in the same repository is reached through an explicit
`worktree_claim`; a different repository is reached through a host-started
session there, with the cross-repository claim refused by the store and the
placement answered by CON-771's typed session placement detail.

### D3. One claimed directory, verified read-back, and fail-closed resolution stand

CD-0093 D2 binds unchanged for session-prepare. The one claimed directory
governs agent definition resolution, the calling-host registry probe, and
the context read, and it is established by the verified move before identity
verification. CD-0093 D3 binds unchanged: a resolution that fails refuses
with a typed diagnostic before managed action.

The verified read-back is the placement proof. A move that lands anywhere
other than the claimed worktree refuses success (CD-0098 D3).

## Alternatives considered

- Keep the two-candidate landing read for context derivation. Rejected: the
  continuity read resolves work from the session's active claim and
  directory (CD-0093 as amended), and a second candidate read would own the
  same fact twice.
- Resolve the session directory in the adapter. Rejected: the adapter
  verifies the directory the core claims, and a second resolver would own
  the same fact twice.
- Extend the landing to a captured work item on the capture turn. Out of
  scope under CD-0103 and left to its own record.

## Consequences

A managed session enters the worktree its claim recorded through the
verified move and read-back; there is no launch fallback. An active entry
whose directory is missing on disk is restored or refused by work resume
under CD-0088, never absorbed by an arbitrary fallback. No schema,
migration, event, or operator setting changes.

## Verification

- `go test ./internal/store/ -run TestResolveContinuityWorkByDirectory`
  proves the surviving directory binding: the continuity read binds the
  session's directory to its active claim.
- `go test ./internal/store/ -run
  TestClaimWorktreeStillRefusesForeignWorkOccupancy` proves the claim side:
  foreign-work occupancy still refuses.
- `bun test adapter/opencode/concord.test.ts -t "work start verifies the
  session directory after the move"` proves the verified move and read-back
  that establish the directory (CD-0098).
- `python3 scripts/check-doc-contract.py`,
  `python3 scripts/check-knowledge-index.py`,
  `python3 scripts/generate-knowledge-index.py --check`, and
  `python3 scripts/check-knowledge-closure.py` pass.

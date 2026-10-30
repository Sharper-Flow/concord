# CD-0176: The active work directory governs managed session preparation

- **Status:** Accepted
- **Date:** 2026-09-24
- **Scope:** The directory that governs managed session context and identity
- **Approval:** Concord (CON) issue CON-910 (2026-10-13), under the Snowball coordinator's operator-delegated approval.
- **Related:** CD-0008, CD-0088, CD-0093
- **Preserves:** CD-0093 D2's one-directory binding and D3's fail-closed resolution

## Decision

### D3. One work directory and fail-closed resolution stand

Host tools place the session under CD-0088 D1. CD-0093 D2 binds unchanged for
`session-prepare`: the active work directory governs agent definition
resolution, the calling-host registry probe, and the context read. That
directory is established before identity verification. CD-0093 D3 binds
unchanged: failed resolution refuses with a typed diagnostic before managed
action. Preparation does not create a worktree, move the session, or write a
landing.

The existing continuity read still binds the session directory through
retained worktree metadata. Missing or unresolvable metadata does not permit
a fallback directory or a replacement ownership writer.

## Verification

- `internal/store.TestResolveContinuityWorkByDirectory` proves the surviving
  directory binding.
- `cmd/concord.TestSessionPrepare` cases prove the directory, identity, and
  registry checks under CD-0093.

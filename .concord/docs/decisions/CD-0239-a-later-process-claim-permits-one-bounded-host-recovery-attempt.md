# CD-0239: A later process claim refreshes the recorded host identity

- **Status:** Accepted
- **Date:** 2026-10-19
- **Scope:** Host process claims and landing refresh
- **Related:** CD-0178 D3, CD-0179 D3, CD-0201, CD-0242

## Context

An occupying host can restart while its session identity stays the same. An
old process identity must not prevent the existing landing route recording
the replacement process.

## Decision

### D3. A landing refreshes its process observation

A same-session resume landing from a different process updates occupancy with
the new process identity in one transaction. A legacy row without process
identity receives the same refresh. The core checks liveness and derives
start time under CD-0178 D3. The existing host-lease owner supplies lease
observations under CD-0179 D3. A replay for the same process remains idempotent.

Process identity remains a caller-supplied claim checked against the live
process and lease. It proves neither that the session runs in that process nor
that repaired plugin code loaded. It authorizes no workflow result, native
receipt or subagent execution. A real restart is evidence that new host code
can run, not proof that the fault was fixed. Cancellation alone proves no cause.

## Alternatives considered

- Infer a repair from process replacement. Rejected: an authorized shell can
  submit a process claim without fixing or restarting its actual host.

## Consequences

Occupancy refresh retains its host-liveness and shared-store owner without a
worker-retry eligibility or attribution subsystem.

## Verification

- `internal/store.TestClaimLandingRefreshesRestartedHostIdentity` checks refresh,
  repeated landing, retained identity and reconciliation.
- `python3 scripts/check-knowledge-index.py` checks identity and hashes.

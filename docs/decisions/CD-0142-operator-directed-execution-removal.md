# CD-0142: Operator-directed execution removal preserves the planning record

- **Status:** Accepted
- **Date:** 2026-09-14
- **Scope:** Shelving and cancellation of nonterminal Concord execution items,
  liveness evidence, planning handoff, and replay-safe projection removal
- **Approval:** The operator approved this bounded removal contract on 2026-09-14.
- **Related:** PM4, PM6, PM7, CD-0066, CD-0121
- **Amends:** PM4 lifecycle and reopen semantics; PM7 live-query and renewed-work
  boundary; CD-0066 D1 only for explicitly directed nonterminal execution removal
- **Preserves:** The five-state lifecycle, exact event history, Product and Project
  identity, accepted law, Linear issue identity, and the no-polling boundary

## Context

Shelving execution does not mean cancellation. Both actions must preserve the
current findings and remove the Concord execution identity from operational
projections. A hidden or sixth work state would leave an agent able to select or
resume the execution. A deleted row without an event would return on replay.

## Decision

### D1. Removal is a distinct typed operation

Concord keeps the closed lifecycle states `needed`, `in_progress`, `completed`,
`cancelled`, and `superseded`. Shelving is not a lifecycle state and does not
emit cancellation. Cancellation uses the same removal machinery with the reason
`cancelled`, but its reason remains distinct in the durable receipt and event.

The operation appends `work.removed` and atomically removes the work item plus
its owned execution projections. The durable receipt and append-only event keep
the exact audit handoff without exposing an actionable WorkItem.

### D2. Preparation fences execution and requires a complete handoff

Preparation pins an operation identity, idempotency key, work revision, reason,
and handoff digest. It refuses stale revisions, live or unknown worker attempts,
occupied sessions or worktrees, pending writes, effects, claims, required
consumers, dependencies, and incomplete artifact or handoff evidence. Unknown
host state remains unknown and never grants removal authority.

The handoff contains findings, remaining scope, blockers, artifact references,
and renewal conditions. A final commit revalidates the gates in the same SQLite
transaction. No force removal or automatic process termination exists.

### D3. The planning destination remains the authority

Linear-enabled Products require a verified Product-scoped issue identity and
handoff digest before removal. The operation does not close, archive, or delete
the Linear issue. An existing issue uses adoption or update evidence. A missing
issue uses one durable creation intent. Ambiguous or failed external writes stop
local removal.

Non-Linear destinations remain an explicit operator choice. Concord does not
invent a Git note, GitHub adapter, second database, or generic provider layer.

### D4. Absence survives reads, replay, and renewal

Operational lists, exact-ID reads, launcher navigation, continuity, work start,
claims, and overlap computation exclude a removed ID. Audit reads return only
the explicit historical receipt. Replay, restart, migration, and knowledge
rebuild preserve the absence. A retry with the original identity returns the
same committed receipt and does not append another removal event.

Renewed work starts with a new Concord identity and contract. It may reuse the
same confirmed Linear issue without creating a duplicate or inheriting old
approvals, attempts, reservations, or checkpoints.

### D5. Liveness is read-time evidence

Liveness is derived from worker attempts, bounded waits, failures, and progress
events. A dispatched attempt is operational evidence, not proof of host process
health. A work item without verified active or waiting evidence reports
`unknown`; liveness never authorizes removal.

## Consequences

- Shelving removes execution while preserving the planning handoff and history.
- Cancellation no longer leaves an actionable cancelled WorkItem.
- Terminal compaction remains a separate retention operation.
- Every removal is per-item, operator-directed, fenced, bounded, and replay-safe.

## Verification

- `internal/store.TestWorkRemovalDeletesExecutionAndReplaysAbsence` verifies
  projection removal, audit retention, replay absence, and idempotent retry.
- `internal/store.TestWorkRemovalRefusesMissingSafetyEvidence` verifies a required
  removal gate.
- `go test ./internal/store -run 'TestProductRow|TestWorkRemoval|TestMigrateV80'`
  verifies removal, migration, and the single Product-row liveness query.
- `python3 scripts/generate-agent-contracts.py --check` verifies generated
  contract projections after this decision is indexed.

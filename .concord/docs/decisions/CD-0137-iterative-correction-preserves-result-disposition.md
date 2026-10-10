# CD-0137: Iterative correction preserves result disposition

- **Status:** Accepted
- **Date:** 2026-09-12
- **Scope:** Worker result disposition and fresh-attempt correction
- **Related:** CD-0008, CD-0017, CD-0027, CD-0112, CD-0113, CD-0130, CD-0133
- **Preserves:** Approved contracts, immutable attempt history, and fail-closed completion

## Context

Worker failure recovery records transport failure, but a completed worker result
has no durable rejection route. A rejected result therefore cannot teach the next
attempt what failed or prevent reuse of the same attempt identity.

## Decision

### D1. Record a result disposition

The workflow accepts or rejects each completed worker result exactly once. A
rejection records its attempt, diagnosis, strategy, predicate IDs, and evidence.

For oracle-capable histories, `reject_worker_result` and `request_correction`
also bind `open_finding_ids` to the exact derived ranked-finding set.
The field is required for a nonempty set and absent exactly for an empty
set. Predicate IDs are not finding IDs. Supported `resolved_findings`
closures name canonical IDs and qualifying evidence (CD-0056, CD-0197).
A shorter list, omitted finding, relabeling, or changed confidence closes
nothing. Explicit contract supersession retains its existing authority.

### D2. Require a fresh correction attempt

A failed or rejected result exposes bounded correction context through the work
pin and lane packet. A fresh packet must consume that context before dispatch.
The context names the source event of the record that opened the correction,
and the failed or rejected attempt when one exists. Dispatch compares these
identities, so an equal diagnosis and strategy from another record do not
consume the correction.

The same reader carries every open oracle blocker and retained receipt
reference into correction context. Earlier-subject receipts remain baselines,
not proof for the new subject. New uncovered cases remain blockers until
supported closure or contract supersession. A report's closure claim neither
changes the once-only result disposition nor supplies workflow acceptance.

### D3. Bound retries

The workflow permits at most three worker attempts for one correction sequence.
The next dispatch requires a diagnosis and strategy. The fourth dispatch requires
operator escalation and is refused by the engine.

### D4. Preserve historical pins

The rejection route is a recovery action outside the pinned definition action
list. It preserves historical definition digests and all prior attempt records.

## Verification

- Completed worker results expose accept and reject dispositions.
- Failed and rejected results expose bounded correction context.
- Fresh dispatch rejects missing or stale correction context.
- The fourth attempt is refused and prior attempts remain unchanged.
- Work-pin and packet journeys use the same correction projection.
- `TestOwnerOracleConvergence`, `TestOwnerOracleRequestOpenFindingsComparison`, and `TestOwnerOracleEmptyDerivedSetEquality` cover exact correction sets and supported shrinkage.

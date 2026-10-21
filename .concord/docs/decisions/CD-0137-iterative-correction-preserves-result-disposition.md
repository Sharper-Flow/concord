# CD-0137: Iterative correction preserves result disposition

- **Status:** Accepted
- **Date:** 2026-09-12
- **Scope:** Worker result disposition and fresh-attempt correction
- **Related:** CD-0008, CD-0017, CD-0027, CD-0112, CD-0113, CD-0130, CD-0133, CD-0148, CD-0193, CD-0205
- **Preserves:** Approved contracts, immutable attempt history, and fail-closed completion
- **Approval:** The Snowball coordinator approved this law change on 2026-10-16 under the operator's delegation.
- **Amended by:** [CD-0242](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md) D2, D6

## Context

Worker failure recovery records transport failure, but a completed worker result
has no durable rejection route. A rejected result therefore cannot teach the next
attempt what failed or prevent reuse of the same attempt identity.

## Decision

### D1. Record a result disposition

[CD-0242 D6](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d6-attempts-carry-their-own-authority-not-a-budget) governs: each terminal result is accepted or rejected once, and authorization, actual start, terminal result, and result acceptance stay distinct recorded facts.

### D2. Require a fresh correction attempt

[CD-0242 D6](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d6-attempts-carry-their-own-authority-not-a-budget) governs: each launch is one admitted request with its own scope and subject bindings, and a reconciled prior result's remaining obligations join that scope.

### D3. Bound retries

[CD-0242 D6](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d6-attempts-carry-their-own-authority-not-a-budget) governs: no attempt count, correction count, or escalation wall exists; each launch is admitted on its own scope and safety checks.

### D4. Preserve historical pins

The rejection route is a recovery action outside the pinned definition action
list. It preserves historical definition digests and all prior attempt records.

## Verification

- Completed worker results expose accept and reject dispositions.
- Failed and rejected results expose bounded correction context.
- The fourth-attempt refusal binds the CD-0242 D6 anchor: no attempt count or escalation wall exists, and a launch is admitted on its own scope and safety checks. Until that change lands, `python3 scripts/check-knowledge-index.py` is the executable anchor for this record's law registration.
- Work-pin and packet journeys use the same correction projection.
- Forged pairs and prior dispositions refuse without a durable effect.
- A stale result cannot advance.
- Work-pin admission, single-result rejection, and human-checkpoint protections
  retain the same disposition boundaries.
- `TestOwnerOracleConvergence`, `TestOwnerOracleRequestOpenFindingsComparison`, and `TestOwnerOracleEmptyDerivedSetEquality` cover exact correction sets and supported shrinkage.

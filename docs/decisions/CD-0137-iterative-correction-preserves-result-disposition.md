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

### D2. Require a fresh correction attempt

A failed or rejected result exposes bounded correction context through the work
pin and lane packet. A fresh packet must consume that context before dispatch.

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

# CD-0145: Domain overlap blocks only shared writes

- **Status:** Accepted
- **Date:** 2026-09-13
- **Scope:** CD-0041 D6; the workflow Domain-overlap predicate and its execution guard
- **Approval:** The operator approved the narrower write-conflict predicate on
  2026-09-13.
- **Related:** CD-0041 D5–D7, CD-0144
- **Amends:** CD-0041 D6
- **Preserves:** Domain-overlap detail, write classifications, version-pinned
  resolutions, and the transactional execution guard

## Context

CD-0041 D6 justified the Domain-overlap gate by the failure it prevents: two
agents independently enacting contradictory Product truth. The implementation
also blocked pairs that shared only an affected Domain. That tested shared topic,
not a shared write, and caused architecture-only pairs to grow quadratically as
active work increased per Domain.

## Decision

### D1. A write intersection is the blocking predicate

An active Product-changing pair blocks execution authority when it shares at
least one law addition or modification, Domain modification, or exact
Domain-relation tuple. A shared affected Domain without a shared write does not
block and does not require an operator-approved resolution.

The overlap projection still computes shared affected Domains, shared law IDs,
shared Domain modifications, and shared relation tuples. A blocking pair keeps
the `architecture` class when it also shares an affected Domain, so refusal
detail continues to describe the complete bounded surface.

### D2. Existing resolutions remain immutable history

Existing overlap resolutions remain unchanged. A resolution for a pair that no
longer blocks is not consulted. Concord does not migrate, invalidate, or rewrite
operator decision history because the blocking predicate became narrower.

## Consequences

- Architecture proximity no longer creates an execution refusal.
- Shared Product writes still require the existing resolution choices.
- Read-only overlap detail continues to expose every derived intersection.

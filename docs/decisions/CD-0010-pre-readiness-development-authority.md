# CD-0010 — Pre-readiness development authority

**Status:** Accepted.
**Date:** 2026-08-07.
**Approval:** Operator plan approval.
**Type:** Development-authority decision.

## Decision

Before Concord reaches replacement readiness, public GitHub and Git remain the
development authority:

- GitHub Issues are the authority for planned work and defects.
- Pull requests and required checks are the authority for review and merge evidence.
- Git branches and worktrees are the authority for implementation isolation.
- Concord decision records, specifications, and constitutional docs are the
  authority for Product law.
- Advance is reference-only. Concord does not dual-write predecessor state.

Concord does not self-host its own development workflow before the replacement-ready
floor is proven. Migration is deferred until then and proceeds later under the
accepted Product-at-a-time fix-forward policy.

## Rationale

This is the smallest public authority model that preserves reviewability and
isolation without importing private orchestration history or creating a second
source of truth. It records the operator's 2026-08-07 execution approval while
preserving CD-0007's earlier 2026-08-06 contract-only boundary.

## Consequence

Any change to this authority model requires a new accepted decision record, public
issue/PR evidence, and an explicit replacement-readiness rationale.

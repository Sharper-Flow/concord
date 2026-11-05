---
{
  "id": "CD-0061",
  "title": "Session identity verification writes no assertion",
  "authority": {
    "tier": "derived"
  },
  "home_domain_id": "operator-surface",
  "applies_to_domain_ids": []
}
---
# CD-0061: Session identity verification writes no assertion

- **Status:** Accepted
- **Date:** 2026-08-23
- **Scope:** Session bootstrap and agent identity assertion
- **Approval:** Operator approval for CD-0061
- **Related:** CD-0016 (context continuity), CD-0049 (agent delivery and identity assertion), issue #254

## Context

CD-0049 requires Concord to verify the identity a managed session presents.
Identity verification does not require a stored assertion. No projection or
authorization consumer reads the session identity assertion; recording it adds
a write and a possible failure to session preparation without informing a
decision.

## Decision

A host-started session with no selected work stays identity-only and performs
no preparation (CD-0252).

### D4. Identity verification records no assertion

Session preparation verifies that the active agent definition resolves, its
invocation handle matches the selected agent, and the calling host registers
that handle. Failure remains typed and refuses preparation under CD-0049.

Preparation creates no session identity assertion, source manifest, or ruleset
digest. It performs no identity write. Existing assertion receipts remain
untouched and confer no authority. Concurrent preparations must not contend on
an identity write or fail because an identity assertion ID already exists.

This retirement uses [CD-0243 D3](CD-0243-four-roles-and-six-operator-approval-classes.md#d3-decisions-outside-those-classes-belong-to-agents).
It removes the assertion and digest obligations and their tests, not identity
verification, authentication, authorization, or directory and scope checks.

## Invariants

1. No durable Concord record defines an orchestrator persona.
2. A managed session that proceeds has verified the orchestrator identity;
   one that cannot verify refuses with a typed
   failure naming the required identity, the observed identity, and the paths
   searched.

## Consequences

Session preparation verifies coordinator identity without a lane-identity check
or an identity write. No entry surface performs a durable store write
(CD-0108 as amended).

## Supersession

CD-0049's recording clauses are amended with D4's retirement. Law changes
follow CD-0243's approval boundary and the accepted law-amendment path.

## Verification

- Concurrent preparations with a resolvable, registered active-agent definition
  succeed without changing identity receipts or the store revision.
- A prepared managed session with no resolvable orchestrator definition refuses
  with a typed diagnostic naming required identity, observed identity, and
  searched paths, and writes nothing.

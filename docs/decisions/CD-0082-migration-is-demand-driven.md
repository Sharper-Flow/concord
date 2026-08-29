# CD-0082: Migration and replacement controls are demand-driven

- **Status:** Accepted
- **Date:** 2026-08-29
- **Scope:** Product-wide migration, correction, replacement-readiness, and
  replacement-relation policy; issue #571
- **Approval:** Operator approved the Product-law cleanup for issue #571 on
  2026-08-29.
- **Related:** CD-0006 D1-D2, CD-0053 D4, CD-0041, PM4, C15, and issue #571
- **Amends:** CD-0006 D1-D2 and CD-0053 D4's cutover framing
- **Preserves:** CD-0006's full-successor and pre-readiness authority boundary;
  CD-0053's three accepted predecessor-strength answers; CD-0041's
  Domain-overlap execution block and Domain `replaces` relation; PM4 work-item
  supersession; append-only events, fold-only projections, immutable schema
  migrations, and other data-integrity controls
- **Supersedes:** No whole-record relation. This record removes future policy
  obligations without rewriting historical decision records.

## Context

Current documents treated migration and replacement as a global Product-at-a-time
program. They also described generic replacement state, cutover steps, shadow
requirements, rollback limits, and final-retirement work that no accepted runtime
surface implements or needs.

The existing predecessor inventory and import commands already provide bounded
utilities with local input, idempotency, and provenance safeguards. They do not
need a Product-wide authority transition. Migration and correction should start
from a concrete demand and use an operation whose scope and evidence fit that
demand.

## Decision

### D1. Migration and correction are demand-driven

Concord does not require a global Product-at-a-time migration policy. It does not
require all Projects to move together, a shadow requirement, a cutover checklist
or state, a rollback prohibition, a final-retirement sequence, or a mandatory
migration record.

When a concrete demand calls for migration or correction, the selected operation
defines its local authority, input, idempotency, provenance, recovery, and native
execution boundary. The existing predecessor inventory and import commands remain
utilities with their current safeguards. They do not become a global authority
transition.

### D2. Replacement readiness remains an evidence claim

Replacement readiness remains the claim defined by the current floor and release
evidence. Migration activity does not prove, trigger, or sequence that claim.
The pre-readiness GitHub authority and the rule that Concord does not self-host
its development remain unchanged.

### D3. Bounded existing relations remain

CD-0041's Domain-overlap execution block and Domain-specific `replaces` relation
remain current. PM4's `supersedes` relation remains the bounded work-item
operation that atomically changes the predecessor's terminal state.

No generic Product, Project, resource, or workflow replacement relation is added
by this decision. Append-only events, fold-only projections, immutable schema
migrations, native authority, and the other data-integrity controls remain current.

## Explicit amendments

CD-0006 D1 remains the rule that no partial slice is usable or the operator's
primary coordination system, and that replacement readiness requires the full
accepted floor. This decision clarifies that D1 does not make later migration or
correction a global prerequisite, sequence, or authority transition.

CD-0006 D2 no longer governs a Product-at-a-time unit, all-Projects movement,
fix-forward-only correction, rollback prohibition, or final predecessor retirement.
Its full-successor intent and the distinction between readiness and later demand
remain preserved.

CD-0053 D4 still records the accepted answers to CD-0019 D3. Its statement that
those answers are cutover-blocking for the first Product migration, and its
required first-Product shadow framing, are amended. The answers and their cited
mechanisms remain evidence that may inform a future demand, not a global
cutover obligation.

## Consequences

- Current documents describe migration and correction as local, demand-driven
  operations.
- The floor records replacement-readiness evidence rather than migration activity.
- Existing managed-resource and Domain-attachment operator surfaces remain in
  scope, with no generic resource-replacement model added.
- Historical CD-0006, CD-0053, CD-0007, and CD-0010 files retain their original
  text.

## Rejected alternatives

**Keep a Product-at-a-time migration program.** Rejected because no current
demand requires a global authority transition, and the existing utilities have
local safeguards.

**Add a generic replacement state machine.** Rejected because the accepted
Domain and PM4 relations already own their bounded semantics, while other
endpoint families lack a demonstrated Product need.

**Make shadow, cutover, or retirement a readiness gate.** Rejected because the
floor is an evidence claim and those activities do not establish its current
conditions.

## Verification

- Historical decision files named in this record remain unchanged.
- Current non-historical documents no longer state the removed global migration
  or generic replacement policies.
- The knowledge record and out-of-scope coverage shard identify this decision;
  generated manifests are rebuilt by their owning scripts.

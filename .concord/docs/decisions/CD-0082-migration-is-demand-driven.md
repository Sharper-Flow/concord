# CD-0082: Migration and replacement controls are demand-driven

- **Status:** Accepted
- **Date:** 2026-08-29
- **Scope:** Product-wide migration and correction, bounded Domain and work-item
  relations, and data-integrity controls; issue #571
- **Approval:** Operator approved the Product-law cleanup for issue #571 on
  2026-08-29.
- **Amended:** 2026-10-17. Coordinator approval under the operator's delegation
  for [Concord (CON) issue 1063](https://linear.app/sharper-flow/issue/CON-1063).
  The full-successor framing is removed; D2 follows current Product law.
- **Related:** CD-0006 D1-D2, CD-0053 D4, CD-0222, PM4, C15, and issue #571
- **Amends:** CD-0006 D1-D2 and CD-0053 D4's cutover framing
- **Preserves:** CD-0053's three accepted predecessor-strength answers; CD-0222's
  Domain-overlap execution block and Domain `replaces` relation; PM4 work-item
  supersession; immutable schema migrations and the other data-integrity
  controls that keep current state authoritative
  ([CD-0242](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md) D2)
- **Supersedes:** No whole-record relation. This record governs local migration
  and correction without changing unrelated runtime contracts.
- **Amended by:** [CD-0242](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md) D2

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

### D2. Readiness follows current Product law

CD-0006 D1 owns Product direction, without a full-successor requirement.
The recorded first-usable assessment remains in
[`floor-readiness.md`](../floor-readiness.md) and its manifest. Accepted release,
maturity, and development-authority requirements keep their owning records;
migration activity does not prove them or create an adoption gate.

### D3. Bounded existing relations remain

CD-0222's Domain-overlap execution block and Domain-specific `replaces` relation
remain current. PM4's `supersedes` relation remains the bounded work-item
operation that atomically changes the predecessor's terminal state.

No generic Product, Project, resource, or workflow replacement relation is added
by this decision. [CD-0242 D2](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)
governs: the store keeps current state in one transaction per mutation; immutable
schema migrations, native authority, and the other data-integrity controls
remain current.

## Explicit amendments

CD-0053 D4 still records the accepted answers to CD-0019 D3. Its statement that
those answers are cutover-blocking for the first Product migration, and its
required first-Product shadow framing, are amended. The answers and their cited
mechanisms remain evidence that may inform a future demand, not a global
cutover obligation.

## Consequences

- Current documents describe migration and correction as local, demand-driven
  operations.
- Existing managed-resource and Domain-attachment operator surfaces remain in
  scope, with no generic resource-replacement model added.
- CD-0053, CD-0007, and CD-0010 retain their governing text.

## Rejected alternatives

**Keep a Product-at-a-time migration program.** Rejected because no current
demand requires a global authority transition, and the existing utilities have
local safeguards.

**Add a generic replacement state machine.** Rejected because the accepted
Domain and PM4 relations already own their bounded semantics, while other
endpoint families lack a demonstrated Product need.

**Make shadow, cutover, or retirement a readiness gate.** Rejected because the
activities do not establish accepted release or maturity evidence.

## Verification

- CD-0053, CD-0007, and CD-0010 remain unchanged.
- Current non-historical documents no longer state the removed global migration
  or generic replacement policies.
- The knowledge record and out-of-scope coverage shard identify this decision;
  generated manifests are rebuilt by their owning scripts.

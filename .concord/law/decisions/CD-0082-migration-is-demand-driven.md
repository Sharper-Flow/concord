---
{
  "id": "CD-0082",
  "title": "Migration and replacement controls are demand-driven",
  "authority": {
    "tier": "derived"
  },
  "home_domain_id": "product-root:concord",
  "applies_to_domain_ids": [],
  "product_wide_rationale": "Keeps migration and correction demand-driven without a predecessor-replacement mission, while preserving current release, maturity, and data-integrity requirements across Domains."
}
---
# CD-0082: Migration and replacement controls are demand-driven

- **Status:** Accepted
- **Date:** 2026-08-29
- **Scope:** Product-wide migration and correction, bounded Domain and work-item
  relations, and data-integrity controls; issue #571
- **Approval:** Operator approved the Product-law cleanup for issue #571 on
  2026-08-29.
- **Approval:** 2026-10-17. Coordinator approval under the operator's delegation
  for [Concord (CON) issue 1063](https://linear.app/sharper-flow/issue/CON-1063).
- **Related:** CD-0006 D1-D2, CD-0053 D4, CD-0222, PM4, and issue #571
- **Preserves:** CD-0053's three accepted predecessor-strength answers; CD-0222's
  Domain-overlap execution block and Domain `replaces` relation; PM4 work-item
  supersession; immutable schema migrations and the other data-integrity
  controls that keep current state authoritative
  ([CD-0242](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md) D2)

## Context

Current documents treated migration and replacement as a global Product-at-a-time
program. They also described generic replacement state, cutover steps, shadow
requirements, rollback limits, and final-retirement work that no accepted runtime
surface implements or needs.

Migration and correction start from a concrete demand and use an operation
whose scope and evidence fit that demand. No current demand requires an
Advance inventory, importer, or parallel migration mode.

## Decision

### D1. Migration and correction are demand-driven

Concord does not require a global Product-at-a-time migration policy. It does not
require all Projects to move together, a shadow requirement, a cutover checklist
or state, a rollback prohibition, a final-retirement sequence, or a mandatory
migration record.

When a concrete demand calls for migration or correction, the selected operation
defines its local authority, input, idempotency, provenance, recovery, and native
execution boundary. Concord has no predecessor inventory or import utility,
snapshot or harvest contract, or parallel predecessor migration mode. Their
implementation, importer-only store probes, and tests of those retired
behaviors are removed, not disabled or retained for compatibility.

Work already imported remains ordinary Concord work. Its identity, intent,
provenance metadata, lifecycle, and Project membership remain unchanged and
readable through ordinary work reads. Retirement performs no schema or data
rewrite. The predecessor lessons and CD-0019/CD-0053 strength obligations remain.

### D2. Readiness follows current Product law

CD-0006 D1 owns Product direction, without a full-successor requirement.
The recorded first-usable assessment remains in
[`floor-readiness.md`](../../docs/floor-readiness.md) and its manifest. Accepted release,
maturity, and development-authority requirements keep their owning records;
migration activity does not prove them or create an adoption gate.

### D3. Bounded existing relations remain

CD-0222's Domain-overlap execution block and Domain-specific `replaces` relation
remain current. PM4's `supersedes` relation remains the bounded work-item
operation that atomically changes the predecessor's terminal state.

No generic Product, Project, resource, or workflow replacement relation is added
by this decision. The store keeps current state in one transaction per mutation; immutable schema migrations, native authority, and the other data-integrity controls remain current ([CD-0242 D2](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)).

## Explicit amendments

The accepted predecessor-strength answers and their cited mechanisms may
inform a concrete migration demand. They create no global cutover obligation.

## Consequences

- Current documents describe migration and correction as local, demand-driven
  operations.

## Rejected alternatives

**Keep a Product-at-a-time migration program.** Rejected because no current
demand requires a global authority transition.

**Add a generic replacement state machine.** Rejected because the accepted
Domain and PM4 relations already own their bounded semantics, while other
endpoint families lack a demonstrated Product need.

**Make shadow, cutover, or retirement a readiness gate.** Rejected because the
activities do not establish accepted release or maturity evidence.

## Verification

- Current documents state local migration authority and safeguards without a
  global migration or generic replacement policy.
- The knowledge record identifies this decision;
  generated manifests are rebuilt by their owning scripts.

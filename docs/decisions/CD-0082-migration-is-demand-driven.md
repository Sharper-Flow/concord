# CD-0082: Migration is demand-driven

- **Status:** Accepted
- **Date:** 2026-08-29
- **Scope:** Migration, cutover, and non-Domain replacement obligations in
  Product law and planning text; the scope of tracking issue
  [#295](https://github.com/Sharper-Flow/concord/issues/295); issue
  [#571](https://github.com/Sharper-Flow/concord/issues/571)
- **Approval:** Operator approved the direction in issue #571 on 2026-08-28:
  apply the demand-driven boundary, keep migration and correction ad hoc, and
  retain the proven controls
- **Related:** CD-0010 (development authority, unchanged until replacement
  readiness is claimed and accepted), CD-0019 and CD-0053 (predecessor
  strength answers, which remain satisfied), CD-0036 (law cutover process for
  this change itself)
- **Preserves:** the delivered importer safety in full; the Domain-overlap
  execution block; append-only events, fold-only projections, and immutable
  schema migrations; the v4.7.0 managed-resource and Domain-attachment
  operator surfaces
- **Supersedes:** the prescriptive one-Product-at-a-time retirement sequence
  in `docs/rollout-plan.md` §4 and `docs/priorities.md` §First-usable floor,
  and the unimplemented shadow-evaluation, cutover-checklist, and
  migration-record obligations of issue #295

## Context

The delivered migration machinery is an inventory verb and an import verb
with structural safeguards. The law around it prescribed more: shadow
evaluation before the first cutover, a cutover checklist per Product, a
mandatory migration record, a retirement sequence tied to the final Product,
and an open question about a generic non-Domain replacement relation.

No demonstrated workflow needs those mechanisms. Law that prescribes
mechanisms before a concrete failure requires them makes agents predict
future situations and can block ordinary Concord setup. The operator
directed the demand-driven boundary in issue #571.

## Decision

### D1. Migration is demand-driven

A Product migrates when a concrete workflow needs it. There is no prescribed
sequence and no retirement-sequence obligation. Advance remains authority
for Products not yet migrated, and retirement remains the end intent, but
neither is gated on a mandated ordering of Products.

### D2. No speculative migration machinery

Nothing mandates shadow operation, a cutover checklist, a migration record,
a quarantine mechanism, or a generic non-Domain replacement relation without
a current workflow. A concrete failure may justify a stronger mechanism
through a later accepted decision, not through silent accretion.

### D3. The delivered importer is the migration contract

When a Product migrates, the delivered safeguards bind: one whole Product at
a time with partial-Product refusal, deliberately selected active work,
recorded provenance, idempotent re-runs, and fix-forward with no rollback
mechanism.

### D4. Proven controls and released surfaces are untouched

The Domain-overlap execution block, append-only events, fold-only
projections, and immutable schema migrations are unchanged. The v4.7.0
managed-resource and Domain-attachment commands, store APIs, contracts, and
tests are unchanged. Issues #387 and #560 and PR #574 stay outside this
change.

## Invariants

1. No active law or planning text mandates shadow operation, a cutover
   checklist, a migration record, or a generic non-Domain replacement
   relation without a current workflow.
2. Two unresolved Product-changing work items affecting one Domain still
   produce the typed Domain-overlap refusal on consequential execution.
3. An out-of-band event, projection, or applied-migration rewrite is still
   refused by the existing structural guards.
4. The delivered importer keeps its partial-Product refusal, selection,
   provenance, and idempotency safeguards.

## Consequences

- `docs/priorities.md` and `docs/rollout-plan.md` now describe migration as
  demand-driven.
- Issue #295 is re-scoped to delivered importer safety; its unimplemented
  shadow and cutover criteria are removed with this record as authority.
- Issue #571 closes against this record and the reconciled text.
- A future concrete failure argues for a mechanism through a new decision,
  not through reinstating the obligations removed here.

## Verification

- `internal/store.TestD7ConsequentialBoundariesRefuseUnresolvedOverlap` and
  `internal/store.TestD7ExecutionClaimBoundaryRefusesUnresolvedOverlap`
  still pass unchanged (Invariant 2).
- `internal/store.TestPM5MembershipTablesAreFoldOnlyAndProjectDeletionIsRestricted`
  still passes unchanged (Invariant 3).
- `python3 scripts/check-json.py`, `python3 scripts/check-floor-readiness.py`,
  and `python3 scripts/check-doc-contract.py` pass on the reconciled text.
- A read of the reconciled `docs/priorities.md` and `docs/rollout-plan.md`
  finds no mandate named in Invariant 1.

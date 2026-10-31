---
{
  "id": "CD-0170",
  "title": "Completed receipts restate approved predicates",
  "authority": {
    "tier": "derived"
  },
  "home_domain_id": "workflow-engine",
  "applies_to_domain_ids": [],
  "law_relations": [
    {
      "kind": "refines",
      "target_id": "CD-0113"
    }
  ]
}
---
# CD-0170: Completed receipts restate approved predicates

- **Status:** Accepted
- **Date:** 2026-09-21
- **Scope:** WorkPin terminal projections, generated agent payload schemas, and the OpenCode closure receipt
- **Approval:** The operator approved the receipt to restate only the predicates that the completion gate verifies.
- **Related:** CD-0006, CD-0084, CD-0113

## Context

A completed work item has a terminal pin and a completion envelope, but the
closure receipt does not identify the facts that completion proved. The approved
contract predicates are the only pre-implementation record whose items the
completion gate verifies individually. Proposal outcomes and design decisions
do not provide that proof.

## Decision

### D1. The active contract predicates own receipt content

The store adds optional `WorkPin.verified_criteria`. Each entry carries the
active contract predicate, its typed outcome payload, and its latest per-
predicate verdict kind. The field is populated only when both the work item and
workflow instance are `completed`, and only after one active contract is read.

### D2. Ambiguous reads degrade without blocking recovery

When the active contract projection is ambiguous, the pin remains readable and
omits `verified_criteria`. The existing contract recovery intent remains the
only repair route. Cancelled and superseded pins also omit the field.

### D3. The adapter renders bounded typed labels

The Go-rendered Markdown receipt prints one `✓` row per verified predicate. An
`exists` or `absent` line uses the outcome kind, first subject, and surface. An
`outcome` line uses the first allowed token. A `check` line uses the check ref
and expected result. Each label is bounded by CD-0169 D2's sixty-four-code-point limit.

## Verification

- Store pin tests prove the completed projection carries the approved criteria,
  while non-completed and ambiguous projections omit them.
- Go receipt golden tests prove completed receipts list typed checkmarked rows.
  Adapter delegation tests prove cancelled or superseded closures queue no notice.
- The agent contract generator regenerates the payload schema and Go and
  TypeScript projections.

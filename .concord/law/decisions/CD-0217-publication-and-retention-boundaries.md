---
{
  "id": "CD-0217",
  "title": "Completion and retention boundaries",
  "authority": {"tier": "derived"},
  "home_domain_id": "product-memory",
  "applies_to_domain_ids": ["durable-authority"]
}
---
# CD-0217: Completion and retention boundaries

## Context

Reported delivery closes work without replacing independent historical readers
or durable knowledge promotion.

## Decision

### D1. Completion retains identity and links, not workflow steps

[PM6 §1](../specs/canonical-git-note-placement.md#1-decision) governs reported-link
completion without publication, reviewed closure hashes, claim/settle or deferred
cleanup. It retains terminal current-state identity and delivery links, not
post-delivery workflow step records. Historical note locators, content hashes
and lesson tags remain readable. No new archive replaces execution state.

CD-0142 remains the distinct per-item execution-removal route with its planning
handoff.

### D2. Authority guarantees remain in force

| Guarantee | Obligation |
|---|---|
| Git knowledge | independent reviewed decision, specification and lesson routes |
| Store state | current rows and finite verified snapshots under CD-0242 D2 |
| Operations and effect receipts | retry and reconciliation identity remains attributable and idempotent |
| Historical projections | existing Git-derived locators and frozen scope remain readable |
| Live/historical queries | deduplicate by work ID and compose authority |

### D3. Retention follows the consuming category

Each retained category names its consumer and removal condition under CD-0242 D2
and [compaction-retention-policy](../specs/compaction-retention-policy.md).
Completion creates no publication-gated cleanup category or post-delivery step
retention. Operations and effect receipts remain while retries or reconciliation
need them; historical readers retain their independent consumers.
Unavailable execution data receives typed absence, never a false inspectable
reference. No timer, pressure threshold or calendar sweep grants destruction.

### D4. Backup retention remains separate

PM10 owns backup, restore, snapshot aging and reclamation. This decision adds no
`VACUUM`, snapshot deletion, secure-erasure promise or backup-retention duration.
`concord backup` refuses an existing destination.

### D5. Durable promotion

PM8 excludes WIP-byte retention; PM9 excludes a separate process-exhaust receipt.
Operator decisions follow
[CD-0243 D2](CD-0243-four-roles-and-six-operator-approval-classes.md#d2-the-operator-approves-six-classes-of-decision);
D5 governs recovery and permanent loss.

### D6. Coverage follows current obligations

Proof must establish reported-link completion, atomic step disposal, historical
locator and lesson-tag preservation. No source anchor alone proves those
obligations executed.

## Verification

Completion, upgrade, every terminal route, owner removal, restart and restore
require integrated evidence on the implementing subject.

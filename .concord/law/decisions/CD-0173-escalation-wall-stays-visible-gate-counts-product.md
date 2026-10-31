---
{
  "id": "CD-0173",
  "title": "The investigation gate counts the Product",
  "authority": {
    "tier": "legislated",
    "legislated_by": "work-05a228deeb2cfc25eaea9acb",
    "contract_version": 2
  },
  "home_domain_id": "workflow-engine",
  "applies_to_domain_ids": [],
  "law_relations": [
    {
      "kind": "refines",
      "target_id": "CD-0129"
    }
  ]
}
---
# CD-0173: The investigation gate counts the Product

- **Status:** Accepted
- **Date:** 2026-09-23
- **Scope:** Work pin next intents and the investigation gate
- **Related:** CD-0013, CD-0166
- **Approval:** The operator approved the dead-end removal so a single-item Product must not be wedged out of its own premise gate.

## Context

Native Task belongs to the host under CD-0242 D6. The investigation gate demands an
observation that names another work item. A Product that holds one work item
has no other work item to name, so its premise gate can never open.

## Decision

### D1. Native Task mints no Concord authority

Native Task execution creates no Concord authorization, dispatch window or
attempt. Scope and safety gates govern the coordinator's recorded workflow
actions under [CD-0242 D6](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d6-native-task-belongs-to-the-host).

### D2. The comparison-work condition is counted by the core

The artifact names a current Domain ref. The observation names another work item only when the item's
Product holds a work item other than this one, in any lifecycle. The store
counts that condition in the same transaction that resolves the refs, so no
agent assertion decides it. A single-item Product admits an artifact that
carries only the current Domain ref.

## Verification

- `TestBudgetStateAbsentFromCurrentCorrectionAndAdmission` proves that no
  attempt budget, escalation wall or convergence basis governs admission.
  Committed law-folder snapshot validation checks this record's registration.
- The store tests `TestInvestigationArtifactAdmitsSingleItemProductByDomainRef`
  and `TestInvestigationArtifactSingleItemProductStillRequiresDomainRef` prove
  both halves of D2.

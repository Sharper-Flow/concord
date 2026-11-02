---
{
  "id": "CD-0173",
  "title": "Native Task mints no Concord authority",
  "authority": {
    "tier": "derived"
  },
  "home_domain_id": "workflow-engine",
  "applies_to_domain_ids": []
}
---
# CD-0173: Native Task mints no Concord authority

- **Status:** Accepted
- **Date:** 2026-09-23
- **Scope:** Native Task authority
- **Related:** CD-0013, CD-0166

## Decision

### D1. Native Task mints no Concord authority

Native Task execution creates no Concord authorization, dispatch window or
attempt. Scope and safety gates govern the coordinator's recorded workflow
actions under [CD-0242 D6](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d6-native-task-belongs-to-the-host).

## Verification

- `TestBudgetStateAbsentFromCurrentCorrectionAndAdmission` proves that no
  attempt budget, escalation wall or convergence basis governs admission.
  Committed law-folder snapshot validation checks this record's registration.

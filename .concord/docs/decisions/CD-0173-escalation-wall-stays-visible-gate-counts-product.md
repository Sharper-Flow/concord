# CD-0173: The investigation gate counts the Product

- **Status:** Accepted
- **Date:** 2026-09-23
- **Scope:** Work pin next intents and the investigation gate
- **Related:** CD-0013, CD-0166
- **Approval:** The operator approved the dead-end removal so a single-item Product must not be wedged out of its own premise gate.

## Context

Each launch carries its own authority, and its scope and safety checks remain ([CD-0242 D6](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d6-attempts-carry-their-own-authority-not-a-budget)). Separately, the investigation gate demands an
observation that names another work item. A Product that holds one work item
has no other work item to name, so its premise gate can never open.

## Decision

### D1. A launch carries its own authority

Each launch carries its own authority and safety checks ([CD-0242 D6](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d6-attempts-carry-their-own-authority-not-a-budget)).

### D2. The comparison-work condition is counted by the core

The artifact names a current Domain ref. The observation names another work item only when the item's
Product holds a work item other than this one, in any lifecycle. The store
counts that condition in the same transaction that resolves the refs, so no
agent assertion decides it. A single-item Product admits an artifact that
carries only the current Domain ref.

## Verification

- The work-pin refusal paths bind the
   CD-0242 D6 anchor: no attempt count, escalation wall, or convergence basis
   exists, and no dispatch mints a challenge. Until that change lands,
  `python3 scripts/check-knowledge-index.py` is the executable anchor for this
  record's law registration.
- The store tests `TestInvestigationArtifactAdmitsSingleItemProductByDomainRef`
  and `TestInvestigationArtifactSingleItemProductStillRequiresDomainRef` prove
  both halves of D2.

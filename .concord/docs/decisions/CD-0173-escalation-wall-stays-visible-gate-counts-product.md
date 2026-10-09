# CD-0173: The escalation wall stays visible and the gate counts the Product

- **Status:** Accepted
- **Date:** 2026-09-23
- **Scope:** Work pin next intents, the investigation gate, and the correction dispatch wall
- **Amends:** CD-0129 D2, CD-0148
- **Related:** CD-0013, CD-0166
- **Approval:** The operator approved both repairs as one dead-end removal: an agent at the escalation wall must find the wall, and a single-item Product must not be wedged out of its own premise gate.

## Context

Two dead ends stop managed sessions. When a correction reaches the three-attempt
limit, the work pin drops the `dispatch_worker` intent, so an agent that reads
the pin sees no route at all and never learns that CD-0148 gates the retry
behind an operator approval. Separately, the investigation gate demands an
observation that names another work item. A Product that holds one work item
has no other work item to name, so its premise gate can never open.

## Decision

### D1. The escalated pin advertises the convergence-gated retry

When the correction is escalated, the pin keeps the `dispatch_worker` intent
and marks it with the reason `escalated_retry_requires_convergence` instead
of removing the intent. When the store derives a convergence basis, the
reason becomes `escalated_retry_convergence_recorded`. The fold refuses the
dispatch without a basis, and CD-0148 names the closed basis families. The
pin states a route that reaches the wall, so an agent finds the convergence
boundary instead of a silent dead end.

### D2. The comparison-work condition is counted by the core

CD-0129 D2 required the artifact to name a current Domain ref and another work
item. The Domain half stays mandatory. The comparison-work half is now
conditional: the observation names another work item only when the item's
Product holds a work item other than this one, in any lifecycle. The store
counts that condition in the same transaction that resolves the refs, so no
agent assertion decides it. A single-item Product admits an artifact that
carries only the current Domain ref.

## Verification

- The store test `TestWorkPinEscalatedCorrectionAdvertisesApprovalGatedRetry`
  proves the escalated pin carries `dispatch_worker` with
  `escalated_retry_requires_convergence` without a basis and
  `escalated_retry_convergence_recorded` with one.
- The store tests around the correction bound prove the fourth dispatch
  refuses with `missing_evidence` and no minted challenge, and admits one
  attempt behind a derived convergence basis.
- The store tests `TestInvestigationArtifactAdmitsSingleItemProductByDomainRef`
  and `TestInvestigationArtifactSingleItemProductStillRequiresDomainRef` prove
  both halves of D2.

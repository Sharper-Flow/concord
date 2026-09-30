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

### D1. The escalated pin advertises the approval-gated retry

When the correction is escalated, the pin keeps the `dispatch_worker` intent
and marks it with the reason `escalated_retry_requires_approval` instead of
removing the intent. The fold still refuses the dispatch until the operator
approval of CD-0148 is consumed. The pin states a route that reaches the wall,
so an agent finds the approval boundary instead of a silent dead end.

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
  proves the escalated pin carries `dispatch_worker` with the escalated
  reason.
- The store tests around the correction bound prove the fourth dispatch still
  refuses with `approval_required` and admits one attempt behind the approval.
- The store tests `TestInvestigationArtifactAdmitsSingleItemProductByDomainRef`
  and `TestInvestigationArtifactSingleItemProductStillRequiresDomainRef` prove
  both halves of D2.

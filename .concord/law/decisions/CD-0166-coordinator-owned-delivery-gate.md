---
{
  "id": "CD-0166",
  "title": "Coordinator-owned delivery gate",
  "authority": {
    "tier": "derived"
  },
  "home_domain_id": "workflow-engine",
  "applies_to_domain_ids": [],
  "law_relations": [
    {
      "kind": "refines",
      "target_id": "CD-0156"
    }
  ]
}
---
# CD-0166: Coordinator-owned delivery gate on workflow effect steps

- **Status:** Accepted
- **Date:** 2026-09-21
- **Scope:** Built-in workflow definitions, the delivery action, terminal lifecycle transitions, and workflow read surfaces
- **Approval:** The operator approved the step-gating route over an additive evidence kind, and set the boundary: the coordinating agent owns the delivery assertion, and no session waits on a merge queue.
- **Related:** CD-0156, CD-0043
- **Preserves:** Active workflow instances

Operator consent for delivery assertions is limited to the six classes in [CD-0243 D6](CD-0243-four-roles-and-six-operator-approval-classes.md#d6-existing-approval-requirements-follow-these-classes). Delivery evidence, subject binding, and other non-consent checks remain required.

## Context

Every built-in workflow declared a `record_delivery` action at its
external-effect step, but the action was inert. Its policy carried no payload
fields, so it recorded that delivery happened and never what was delivered.
It also shared its step with other advance actions, so the step crossed
without it. Completion asked for verification and review evidence only, and
the terminal folds never read a branch.

Two failure modes followed. Work parked at the release step with an open pull
request and no driving session stayed invisible, because the item never
transitioned again. And an item superseded or cancelled while its pull request
stayed open left that pull request with no owner, because supersession and
cancellation never looked at the branch.

Concord holds no pull request identity anywhere: no column stores a PR number,
URL, or merge state, and the worktree durability gate proves a branch was
pushed, not merged.

## Decision

### D1. The delivery action carries an asserted artifact

`record_delivery` requires `delivery_artifact` (a bounded reference, such as a
pull request URL or merge commit) and `delivery_state` (enum, one value:
`asserted`). The core records the assertion. It does not observe GitHub, it
acquires no client, and it runs no prober. The coordinating agent owns the
truth of the assertion.

### D2. A gated step with one advance action

`workflow.implementation` and `workflow.break_fix` splice one `delivery` step
between `refine` and the verdict step, using the CD-0156 shape: the step
declares `record_delivery` and the two continuity holds, so its single forward
edge cannot be crossed without a recorded delivery. Existing records remain
editable through their ordinary actions under
[workflow-engine contract §4.2](../specs/workflow-engine-contract.md#42-ordinary-retry-and-record-correction).
Those replacements hold step and lifecycle; `record_delivery` stays the gate's
only forward exit. The remaining workflow
types keep the action at its existing step and gain the payload only, because
their delivery surface is optional or internal.

### D3. Terminal reconciliation is split by outcome

A lifecycle transition to `completed` on an item sitting on its delivery gate
refuses: admission returns an unreconciled-delivery failure naming
`record_delivery`; the check reads current rows in the transition's transaction ([CD-0242 D2](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)). Cancellation and supersession stay legal, because an
operator must be able to abandon work whose change will never merge. The
workflow read projection marks such items `unreconciled`, so the abandoned
pull request stays visible instead of silently orphaned.

### D4. Parked work is visible and resumable

The workflow read projection reports a `parked_delivery` object, with the
step, the resume action, a read-time parked duration, and the unreconciled
flag. This is a read-time derivation over the current
definition: elapsed time labels, never
resolves, and no timer, poller, or background process exists.

### D5. No session waits on a merge queue

A session that does not yet hold the required merge fact ends the turn with the
item parked at the gate. A later coordinator resumes the item and uses the
existing `record_delivery` action when its recorded acceptance and admission
requirements permit.

For eligible changes in a declared rapid phase, CD-0241 D1–D2 define phase-local
integration evidence. Otherwise, delivery evidence requires the change to reach
the default branch. Phase-local integration does not prove main-branch delivery,
release publication, or installation. It does not by itself complete a workflow
item.

Existing independent-review obligations, acceptance predicates, verdicts, and
delivery admission remain required. Record replacement supplies no fresh proof
or approval ([CD-0242 D2](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)). The installed
release governs workflow admission throughout the phase. This rule creates no
new completion action or admission bypass.

### D6. Delivery progression requires current ready Review

A result that the refinement pass rejected, and the repair accepted after it,
carry no fresh review. On `workflow.break_fix` and `workflow.implementation`,
the refinement step refuses each advance that carries such a repaired result
toward delivery: its recorded delivery and the
gate's delivery exit. An accepted Review clears the refusal when its
disposition of ready for final proof binds the current integrated candidate
and depended-on revisions, with no open in-scope blocking finding (CD-0242 D5).
A Review becomes stale exactly when a depended-on digest changes; a relaunch
or corrected admission request does not invalidate it by itself, and Review's
corrective edits need no further review (CD-0242 D5, D7). The gate carries the
same refusal. The coordinator records a fresh subject-bound Review through
`record_review` without returning to an earlier step. A record-only update
neither advances the gate nor supplies the missing Review.

## Verification

- `TestRecordDeliveryExitsTheStepTheSessionExecuted` proves delivery advances
  the effect step into the gate and the gate into the verdict step, under the
  required payload.
- `TestWorkflowReadParksDeliveryAtGate` proves the read projection parks one
  parked delivery for a gated item and carries its duration.
- `TestWorkflowReadMarksUnreconciledAfterCancellation` proves a cancelled
  item reads as unreconciled.
- `TestWorkflowCompletionRefusedAtDeliveryGate` proves the completed
  transition refuses at the gate and names the unreconciled delivery.
- `TestReviewOfReplacedCandidateCannotAuthorizeDelivery` and
  `TestReviewApplicabilityFollowsCandidateNotDispatchOrder` prove that
  a rejected Review or a Review of an earlier candidate holds delivery of a
  changed candidate until a fresh accepted Review clears it, and that a later
  launch or Review's corrective output leaves an applicable Review standing;
  dispatch order alone does not decide applicability.
- `TestDeliveryGateTakesOrdinaryReviewUpdate` and
  `TestAdmissionModelGateKeepsProgressDuringReviewUpdate` prove a parked gate
  whose depended-on criterion changed refuses its crossing without effect,
  takes `record_review` without a producer return or step advance, stays held
  after a non-ready update, and opens its exit after a fresh ready Review.
- `TestDeliveryGateRetainsStaleReviewThroughAgent` proves the tool surface
  refuses delivery of a changed candidate under a stale Review with no effect.
- `TestReviewCurrentAdmissionNegativeCases` proves an open in-scope blocking
  finding refuses progression.

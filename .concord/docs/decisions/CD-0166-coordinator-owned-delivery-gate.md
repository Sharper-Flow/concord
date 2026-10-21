# CD-0166: Coordinator-owned delivery gate on workflow effect steps

- **Status:** Accepted
- **Date:** 2026-09-21
- **Scope:** Built-in workflow definitions, the delivery action, terminal lifecycle transitions, and workflow read surfaces
- **Approval:** The operator approved the step-gating route over an additive evidence kind, and set the boundary: the coordinating agent owns the delivery assertion, and no session waits on a merge queue.
- **Related:** CD-0156, CD-0043
- **Preserves:** Active workflow instances, the ci-wait no-store-write boundary, and the await-health no-prober boundary
- **Amended by:** [CD-0242](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md) D2, D14

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

For `approval_policy=consequence_v1`, the evidence-backed corrective return requires no operator consent. Delivery admission, the latest review and its settlement verdict, debt preservation, and the declared return target remain required. Approval follows each action's declared policy; no unsigned action turns a no_ship review into settlement.


### D1. The delivery action carries an asserted artifact

`record_delivery` requires `delivery_artifact` (a bounded reference, such as a
pull request URL or merge commit) and `delivery_state` (enum, one value:
`asserted`). The core records the assertion. It does not observe GitHub, it
acquires no client, and it runs no prober. The coordinating agent owns the
truth of the assertion.

### D2. A gated step with one advance action

`workflow.implementation` and `workflow.break_fix` splice one `delivery` step
between `refine` and the verdict step, using the CD-0156 shape: the step
declares `record_delivery` plus the two continuity holds and nothing else, so
its single forward edge cannot be crossed without a recorded delivery. The
gate amendment adds one declared action to the step: `request_correction`, the
evidence-bearing corrective return. It is a hold. Its admission returns a parked,
unreviewed change to the correction target step for a fresh review, so
`record_delivery` stays the gate's only forward exit; [CD-0242 D2](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history) governs: the return is a current-row write in one transaction. [CD-0242 D14](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d14-generated-contracts-derive-from-one-source) governs: definitions generate from one canonical source, and in-flight items complete under their recorded dispatch requirements. The remaining workflow
types keep the action at its existing step and gain the payload only, because
their delivery surface is optional or internal.

### D3. Terminal reconciliation is split by outcome

A lifecycle transition to `completed` on an item sitting on its delivery gate
refuses: admission returns an unreconciled-delivery failure naming
`record_delivery`; [CD-0242 D2](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history) governs: the check reads current rows in the transition's transaction. Cancellation and supersession stay legal, because an
operator must be able to abandon work whose change will never merge. The
workflow read projection marks such items `unreconciled`, so the abandoned
pull request stays visible instead of silently orphaned.

### D4. Parked work is visible and resumable

The workflow read projection reports a `parked_delivery` object, with the
step, the resume action, a read-time parked duration, and the unreconciled
flag. The Product row counts live items sitting on their gate as
`parked_deliveries`. Both are read-time derivations over the current
definition, in the await-health tradition: elapsed time labels, never
resolves, and no timer, poller, or background process exists.

### D5. No session waits on a merge queue

A session that does not yet hold the required merge fact ends the turn with the
item parked at the gate. A later coordinator resumes the item and uses the
existing `record_delivery` action when its pinned acceptance and admission
requirements permit.

For eligible changes in a declared rapid phase, CD-0241 D1–D2 define phase-local
integration evidence. Otherwise, delivery evidence requires the change to reach
the default branch. Phase-local integration does not prove main-branch delivery,
release publication, or installation. It does not by itself complete a workflow
item.

Existing independent-review obligations, acceptance predicates, verdicts, and
delivery admission remain required. [CD-0242 D2](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history) and [D14](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d14-generated-contracts-derive-from-one-source) govern: recorded items keep their recorded requirements, definitions generate from one canonical source, and no recorded fact is rewritten. The installed
release governs workflow admission throughout the phase. This rule creates no
new completion action or admission bypass.

### D6. A rejected review holds delivery, and a parked gate can return

A result that the refinement pass rejected, and the repair accepted after it,
carry no fresh review. On `workflow.break_fix` and `workflow.implementation`,
the refinement step refuses each advance that carries such a repaired result
toward delivery: its accepted worker result, its recorded delivery, and the
gate's delivery exit. A fresh accepted review clears the refusal: a
review-lane attempt dispatched after the rejection and after the repaired
result's latest dispatch or completion at the refinement step, so the review
covers the result it accepts. A review dispatched before that repair
activity settles nothing, and repair activity after a review's dispatch
reopens the debt. The gate carries the
same refusal, so a parked, unreviewed
change leaves only through the D2 corrective return. That return names the
rejected result's predicates, or the active contract's predicates, and binds
durable evidence. It refuses without an outstanding review, with evidence
nothing durably bound, on a terminal instance, and without the operator
identity the action policy demands. Recorded items on the closed-gate shape
take the same return: the admission reads the gate shape, so no recorded fact or definition source
changes (CD-0242 D2, D14).

## Verification

- `TestRecordDeliveryExitsTheStepTheSessionExecuted` proves delivery advances
  the effect step into the gate and the gate into the verdict step, under the
  required payload.
- `TestWorkflowDefinitionVersionPinsHold` and
  `TestBuiltinDefinitionsCoverExactlyThePinnedVersions` prove recorded digests
  read back unchanged and the generated definitions cover the live families;
  the implementing change binds the current-state anchor for definitions
  generated from one source (CD-0242 D2, D14).
- `TestWorkflowReadParksDeliveryAtGate` proves the read projection parks one
  parked delivery for a gated item and carries its duration; the Product-row
  count retired with the row under CD-0224.
- `TestWorkflowReadMarksUnreconciledAfterCancellation` proves a cancelled
  item reads as unreconciled.
- `TestWorkflowCompletionRefusedAtDeliveryGate` proves the completed
  transition refuses at the gate and names the unreconciled delivery.
- `TestPostRejectionReviewGateHoldsBreakFixDelivery` proves the rejected
  review holds every advance toward delivery and a fresh accepted review
  clears it.
- `TestPostRejectionReviewGateRefusesPreRejectionReviewAccept` and
  `TestPostRejectionReviewGateRefusesReviewDispatchedBeforeRepair` prove the
  dispatch-order rule: a review dispatched before the rejection, or before
  the repaired result, settles nothing, and the pin advertises only a
  completed settling review's acceptance.
- `TestPinnedDeliveryGateRecoversThroughEvidenceBearingReturn` proves a
  parked gate returns to repair with no delivery assertion and no
  change to recorded facts or the definition source (CD-0242 D2, D14).
- `TestDeliveryGateRecoveryAdmitsPinnedReturnThroughAgent` proves the
  tool-surface admission: the return refuses unauthenticated, then one
  operator approval admits it.

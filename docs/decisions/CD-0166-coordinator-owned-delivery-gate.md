# CD-0166: Coordinator-owned delivery gate on workflow effect steps

- **Status:** Accepted
- **Date:** 2026-09-21
- **Scope:** Built-in workflow definitions, the delivery action, terminal lifecycle folds, and workflow read surfaces
- **Approval:** The operator approved the step-gating route over an additive evidence kind, and set the boundary: the coordinating agent owns the delivery assertion, and no session waits on a merge queue.
- **Related:** CD-0156, CD-0043
- **Preserves:** Pinned workflow instances, the ci-wait no-store-write boundary, and the await-health no-prober boundary

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
declares `record_delivery` plus the two continuity holds and nothing else, so
its single forward edge cannot be crossed without a recorded delivery. The
prior versions of both definitions stay registered with their original graphs
and payload shapes, so pinned in-flight items replay unchanged. The remaining
workflow types keep the action at its existing step and gain the payload only,
because their delivery surface is optional or internal.

### D3. Terminal reconciliation is split by outcome

A lifecycle transition to `completed` on an item sitting on its delivery gate
refuses: the fold returns an unreconciled-delivery failure naming
`record_delivery`. Cancellation and supersession stay legal, because an
operator must be able to abandon work whose change will never merge. The
workflow read projection marks such items `unreconciled`, so the abandoned
pull request stays visible instead of silently orphaned.

### D4. Parked work is visible and resumable

The workflow read projection reports a `parked_delivery` object, with the
step, the resume action, a read-time parked duration, and the unreconciled
flag. The Product row counts live items sitting on their gate as
`parked_deliveries`. Both are read-time derivations over the pinned
definition, in the await-health tradition: elapsed time labels, never
resolves, and no timer, poller, or background process exists.

### D5. No session waits on a merge queue

A session that does not yet hold the merge fact ends the turn with the item
parked at the gate. The parked surface is the return route: a later
coordinator reads the Product row, resumes the item by work id, and records
delivery once the change reached the default branch. The gate relocates the
hang from the pull request into Concord, and the surface makes what relocated
findable.

## Verification

- `TestRecordDeliveryExitsTheStepTheSessionExecuted` proves delivery advances
  the effect step into the gate and the gate into the verdict step, under the
  required payload.
- `TestWorkflowDefinitionVersionPinsHold` and
  `TestBuiltinDefinitionsCoverExactlyThePinnedVersions` prove prior digests
  replay unchanged and the version chain covers every pinned definition.
- `TestWorkflowReadParksDeliveryAtGate` proves the read projection parks an
  item on the gate with a duration.
- `TestWorkflowReadMarksUnreconciledAfterCancellation` proves a cancelled
  item reads as unreconciled.
- `TestWorkflowCompletionRefusedAtDeliveryGate` proves the completed
  transition refuses at the gate and names the unreconciled delivery.
- `TestProductRowsCountParkedDeliveries` proves the Product row counts one
  parked delivery for a live item and none for a terminal one.

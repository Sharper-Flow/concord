# CD-0183: Workflow actions start the lifecycle and the completion closes it

- **Status:** Accepted
- **Date:** 2026-09-27
- **Scope:** The workflow start and complete actions, the Domain-overlap peer
  marker, the lifecycle completed gate, and the tests that pin them
- **Related:** CD-0144, CD-0094, CD-0013, CD-0166
- **Approval:** The operator approved this change in session 2026-09-27 on
  work item `work-4c428253aab7dc64d936e08b`
  ([Concord (CON) issue 158](https://linear.app/sharper-flow/issue/CON-158)).

## Context

A workflow completion recorded `workflow.completed` and left the work item
`in_progress`. The lifecycle and the Linear status stranded while the instance
showed completed. The exclusivity marker rode the lifecycle (CD-0144 D3), and
the lifecycle moved only when an external-effect step began, so the marker
named a boundary the lifecycle no longer guaranteed once any action could
start an item. A lifecycle `completed` target could also bypass the workflow
completion gate, which stranded items whose workflow had already finished
(CON-221, CON-192).

## Decision

### D1. The first workflow action starts the lifecycle

The first workflow_action the store applies to a needed item moves it to
`in_progress` and enqueues the Linear issue_update in the same transaction.
Every advancing action carries the move, checkpoint and dispatch included; the move is a current-row write in the action's one transaction ([CD-0242 D2](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)). Only a
needed item moves, and the move adds no version of its own. The item stops
reading ready.

### D2. Execution start is an instance fact

The workflow instance carries `execution_started_at`. The action that starts
execution on an external-effect step records it, and the first recorded start
wins. The value is a current row written in the starting transaction ([CD-0242 D2](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)). Migration 107 backfilled existing instances, and recovery restores the recorded value with the instance snapshot. The Domain-overlap peer
query selects peers by this fact, not by the lifecycle, so an `in_progress`
item that has not started execution blocks no peer. A subject keeps its
prospective footprint at its own boundary (CD-0144 D3).

### D3. The workflow completion closes the lifecycle

The workflow complete action records `work.transitioned` and the workflow's
terminal state in one transaction, so the lifecycle and the Linear status
follow; these are current rows ([CD-0242 D2](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)). The completion gate admits only `ok`
verdicts, so that terminal state is `completed`. A completion that names
`cancelled` or `superseded` refuses before any effect: cancellation keeps the
lifecycle route, and supersession stays atomic with its relation. The instance keeps its completed
state: the terminal-lifecycle close admits an item whose instance is already
terminal and writes nothing there.

### D4. The lifecycle cannot bypass the completion gate

`concord_work_transition.lifecycle` refuses a `completed` target while the
item's workflow instance has not reached a terminal state. The refusal is
typed, and its remedy names the `workflow_action complete` action. At a parked
delivery gate the remedy names `record_delivery` first, then the same
completion action. It admits
the target when the instance is already terminal: a completed instance is the
stranded-item repair, and a cancelled or superseded instance closed before the
item ended keeps the lifecycle route with its evidence and approval gates. It
also admits the target when the item carries no instance, which keeps imported
items completable (CD-0094 D2). A `cancelled` target is unchanged. Recorded rows read back as they stand, and none is rewritten ([CD-0242 D2](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)).

## Alternatives considered

- Keep the lifecycle as the exclusivity marker and leave the first action from
  moving it. Rejected: D1 stops the lifecycle from naming execution, so the
  marker would keep blocking items that never began external effect.
- Derive the peer set by scanning stored state at read time. Rejected: the peer
  query runs inside the claim boundary, and the instance column keeps the
  check a current-row read.
- Move the lifecycle at `workflow.completed` without a recorded row. Rejected:
  an unrecorded write breaks agreement between current rows (CD-0242 D2).

## Consequences

- A workflow item is `in_progress` from its first action, and ready work reads
  only items no workflow has started.
- External-effect execution, not the lifecycle, decides which peer blocks
  which.
- A workflow item reaches `completed` through the completion action, through a
  terminal-instance repair, or as an imported item with no instance; each path
  keeps its own evidence and approval gates.
- Recorded rows are not rewritten; recovery restores them from verified
  snapshots (CD-0242 D2).

## Verification

- `TestFirstWorkflowActionStartsLifecycle` proves D1: the first action moves
  the lifecycle, enqueues the Linear update, and the item stops reading ready.
- `TestExecutionStartStepSetMatchesRegistry` pins the migration's
  external-effect step set to the registered definitions.
  `TestExecutionStartBackfillMatchesFoldDerivation` pins the recorded
  execution-start fact; the fold, backfill, and rebuild halves retire at the
  cutover, and the implementing change binds the current-state anchor
  (CD-0242 D2).
- `TestUnstartedContractDoesNotBlockAPeer` and
  `TestExecutionStartMovesLifecycleIntoTheClaim` prove the peer marker.
  `TestWorkflowDomainOverlapGuardsLifecycleExecutionEntry` keeps the claim
  boundary mutual.
- `TestWorkflowCompleteClosesLifecycle` proves D3, and
  `TestTerminalLifecycleLeavesACompletedInstanceAlone` proves the instance
  keeps its completed record.
- `TestTerminalLifecycleRefusesCompletedWhileWorkflowLive` proves the D4
  refusal and its remedy.
- `python3 scripts/check-migration-compatibility.py` classifies migration 107
  as non-breaking.

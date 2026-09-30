# CD-0183: Workflow actions start the lifecycle and the completion closes it

- **Status:** Accepted
- **Date:** 2026-09-27
- **Scope:** The workflow start and complete actions, the Domain-overlap peer
  marker, the lifecycle completed gate, and the tests that pin them
- **Related:** CD-0144, CD-0094, CD-0013, CD-0166
- **Amends:** CD-0144 D3 (the exclusivity marker) and CD-0094 D2 (the
  lifecycle completed gate), at those clauses only
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
Every action fold carries the move, checkpoint and dispatch included. Only a
needed item moves, and the move adds no version of its own. The item stops
reading ready.

### D2. Execution start is an instance fact

The workflow instance carries `execution_started_at`. The fold that starts a
workflow action on an external-effect step sets it, and the first start in
log order wins. The log orders events by sequence and does not require their
timestamps to rise with it. Migration 107 backfills existing instances from
the lowest-sequence qualifying start, and RebuildFromLog derives the same
value. The Domain-overlap peer
query selects peers by this fact, not by the lifecycle, so an `in_progress`
item that has not started execution blocks no peer. A subject keeps its
prospective footprint at its own boundary (CD-0144 D3).

### D3. The workflow completion closes the lifecycle

The workflow complete action appends `work.transitioned` to the workflow's
terminal state in the same transaction as `workflow.completed`, so the
lifecycle and the Linear status follow. The completion gate admits only `ok`
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
items completable (CD-0094 D2). A `cancelled` target is unchanged. Replay
folds historical rows as they stand and rewrites none.

## Alternatives considered

- Keep the lifecycle as the exclusivity marker and leave the first action from
  moving it. Rejected: D1 stops the lifecycle from naming execution, so the
  marker would keep blocking items that never began external effect.
- Derive the peer set from the event log at read time. Rejected: the peer
  query runs inside the claim boundary, and an instance column keeps the check
  a projection read whose single derivation source is the log.
- Move the lifecycle at `workflow.completed` without an event. Rejected: a
  projection write the log does not carry breaks rebuild agreement.

## Consequences

- A workflow item is `in_progress` from its first action, and ready work reads
  only items no workflow has started.
- External-effect execution, not the lifecycle, decides which peer blocks
  which.
- A workflow item reaches `completed` through the completion action, through a
  terminal-instance repair, or as an imported item with no instance; each path
  keeps its own evidence and approval gates.
- Historical rows are not rewritten, and a rebuild reproduces them.

## Verification

- `TestFirstWorkflowActionStartsLifecycle` proves D1: the first action moves
  the lifecycle, enqueues the Linear update, and the item stops reading ready.
- `TestExecutionStartStepSetMatchesRegistry` pins the migration's
  external-effect step set to the registered definitions.
  `TestExecutionStartBackfillMatchesFoldDerivation` proves the fold, the
  backfill SQL, and RebuildFromLog derive the same value.
- `TestUnstartedContractDoesNotBlockAPeer` and
  `TestExecutionStartMovesLifecycleIntoTheClaim` prove the peer marker.
  `TestWorkflowDomainOverlapGuardsLifecycleExecutionEntry` keeps the claim
  boundary mutual.
- `TestWorkflowCompleteClosesLifecycle` proves D3, and
  `TestTerminalLifecycleLeavesACompletedInstanceAlone` proves the instance
  keeps its completed record.
- `TestTerminalLifecycleRefusesCompletedWhileWorkflowLive` proves the D4
  refusal and its remedy.
  `TestArchitectureSpikeCompletionFailsClosedBeforeDecisionWorkflow` and
  `TestSpikeCompletionBindsToTheRecordedDecisionRecord` prove the
  decision-record gate on the lifecycle path.
- `python3 scripts/check-migration-compatibility.py` classifies migration 107
  as non-breaking.

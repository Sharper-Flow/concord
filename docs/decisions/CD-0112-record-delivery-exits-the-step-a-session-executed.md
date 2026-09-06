# CD-0112: record_delivery exits the step a session executed

- **Status:** Accepted
- **Date:** 2026-09-05
- **Scope:** the exit from an external-effect step with no dispatched lane
  attempt, the execution mode of the two context-continuity actions, and the
  exits from the architecture-spike `decision_record` and `review` steps
- **Approval:** The operator selected the explicit delivery action over an
  advancing hand-off summary on 2026-09-05 in Concord
  work-cabca4e5913be1f9519322f0, after issue #833 established that no
  external-effect step could exit without a completed lane attempt.
- **Related:** CD-0013, CD-0016, CD-0027, CD-0109, and issues #801, #833, #837
- **Amends:** CD-0016 (context continuity) by fixing the execution mode of
  `checkpoint_context` and `cross_context_boundary` to hold; extends CD-0013
  §12 with the `record_delivery` action

## Context

An external-effect step declares one advancing action, `accept_worker_result`,
and that action requires a completed row in `worker_attempts`. When a session
executes the step itself, or when a dispatched lane fails before
`worker.dispatched` folds, no such row exists. The step then has no exit.

`cross_context_boundary` looked like the exit. `builtinActionPolicies`
declared it `advance`, and twenty-eight conformance walks left external-effect
steps through a `workflow.action_completed` event that named it. The runtime
never emits that event: `appendGenericWorkflowCompletion` skips the generic
completion for both continuity actions, so the fold that moves `current_step`
never ran for them. The declared mode and the recorded effect disagreed, and
the corpus proved paths the engine could not take.

The architecture-spike family had a second gap of the same kind. Its
`decision_record` step held only `record_decision` in checkpoint mode, and its
`review` step held only `record_verdict` in hold mode. Neither step had a live
exit, so no spike could reach `acceptance`.

## Decision

### D1. A hand-off is not progress

`checkpoint_context` and `cross_context_boundary` are `hold` actions on every
step. They record a durable summary for the next session and move nothing.
The declared mode now matches the fold, and the scenario corpus no longer
leaves a step through either action.

### D2. record_delivery is the exit for a step the session executed

Every external-effect step declares `record_delivery`: `internal_sqlite`
consequence, no approval, `advance` mode, generic completion event. The
dispatcher admits it only when the step's fenced start action ran in the
current attempt, since delivery states that the step's own work finished. The
completion fold keeps refusing any advance other than `accept_worker_result`
once a lane attempt was dispatched in that attempt, so a step with a lane
exits through acceptance and a step without one exits through delivery. The
two routes never overlap.

### D3. The decision record advances, and acceptance of it lives on review

`record_decision` keeps its typed checkpoint event and now advances
`decision_record` to `review`. The assembler keys its checkpoint branch on the
declared event shape rather than the execution mode, so an action may append
a typed checkpoint and still advance. `accept_decision` moves from
`acceptance` to `review` and advances, so verdicts are recorded and the
operator accepts the decision on one step, and `acceptance` holds premise
confirmation alone.

## Acceptance Criteria

```gherkin
Scenario: A session delivers the step it executed
  Given a workflow on an external-effect step whose fenced start action ran
  And no lane attempt was dispatched in that attempt
  When the session submits record_delivery
  Then the completion is recorded
  And the step advances

Scenario: Delivery requires a start
  Given a workflow on an external-effect step whose fenced start never ran
  When the session submits record_delivery
  Then the submission is refused as an invalid operation

Scenario: A dispatched step does not exit through delivery
  Given a workflow on an external-effect step with a dispatched lane attempt
  When the session submits record_delivery
  Then the fold refuses the advance
  And the step exits only through accept_worker_result

Scenario: A hand-off holds the step
  Given a workflow on any step
  When the session submits cross_context_boundary in summary mode
  Then the boundary is recorded
  And current_step is unchanged
```

## Consequences

All seven built-in definition digests change, and the conformance corpus
re-pins them. The corpus walks that left an external-effect step through
`cross_context_boundary` now leave through `record_delivery`, and the spike
walk leaves `decision_record` through `record_decision` and `review` through
`accept_decision`. The definition-wide gate invariant in issue #837 passes for
every `cross_context_boundary` row; the three ops and static-analysis rows it
names remain open there.

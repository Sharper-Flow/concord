# CD-0112: record_delivery exits the step a session executed

- **Status:** Accepted
- **Date:** 2026-09-05
- **Scope:** the exit from an external-effect step with no dispatched lane
  attempt, failed lane-result recording and retry, the execution mode of the
  two context-continuity actions, and the exits of a pinned architecture-spike definition
- **Approval:** The operator selected the explicit delivery action over an
  advancing hand-off summary on 2026-09-05 in Concord
  work-cabca4e5913be1f9519322f0. The operator selected a dedicated
  `record_worker_failure` action over widening `accept_worker_result` on
  2026-09-08 in Concord work-8bc9259e3a54d5db68e3a60e.
- **Related:** CD-0013, CD-0016, CD-0027, CD-0109, and issues #801, #833, #837,
  and #869

## Context

A worker-dispatch step can receive a completed or failed lane result.
`accept_worker_result` requires a completed attempt with a readback model.
A failed attempt needs a recorded recovery route that does not claim delivery.
Context continuity needs to preserve a session's findings without claiming
that its step finished.

## Decision

### D1. A hand-off is not progress

`checkpoint_context` and `cross_context_boundary` are `hold` actions on every
step. They record a durable summary for the next session and move nothing.
The declared hold mode matches the recorded admission behavior. Neither
continuity action advances a stage
([CD-0242 D3](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d3-stage-exits-are-the-unit-of-enforcement)).

### D2. record_delivery is the exit for a step the session executed

Every external-effect step declares `record_delivery` as its exit, with an
`internal_sqlite` consequence, no approval, and `advance` mode
([CD-0242 D3](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d3-stage-exits-are-the-unit-of-enforcement)).
The dispatcher admits it only when the step's fenced start action ran in the
current attempt, since delivery states that the step's own work finished.
Admission refuses `record_delivery` after a lane dispatch in that attempt ([CD-0242 D3](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d3-stage-exits-are-the-unit-of-enforcement)). A completed lane exits through `accept_worker_result`. A failed lane
holds the step until the owner records the failure and starts a fresh attempt.

### D3. The decision record advances, and acceptance of it lives on review

The following exits apply to a pinned architecture-spike definition, which
[CD-0218 D1](CD-0218-the-live-workflow-catalog-retains-three-execution-families.md#d1-three-families-own-new-execution)
excludes from new execution.

`record_decision` records its typed checkpoint and advances
`decision_record` to `review`, and an action may record a typed checkpoint and
still advance ([CD-0242 D2](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)).
`accept_decision` moves from
`acceptance` to `review` and advances, so verdicts are recorded and the
operator accepts the decision on one step, and `acceptance` holds premise
confirmation alone.

### D4. record_worker_failure holds the step for a fresh start

Every current worker-dispatch step declares `record_worker_failure` with an
`internal_sqlite` consequence, no approval, and `hold` mode. Its payload names
the attempt ID and the current step epoch.

Admission requires an exact failed attempt from this work item, and checks
the dispatch order, step epoch, and authenticated actor
([CD-0242 D3](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d3-stage-exits-are-the-unit-of-enforcement)).
The action
refuses active, completed, foreign, stale, and previously recorded attempts
without mutation.

The failure record does not satisfy delivery and does not advance the step. A
new fenced start increments the step epoch. The old failed dispatch then sits
before that start, so `record_delivery` can exit only if the session performs
the fresh attempt itself. A newly dispatched successful lane still exits only
through `accept_worker_result`.

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
  Then admission refuses the advance
  And the step exits only through accept_worker_result

Scenario: A failed lane result is recorded without progress
  Given a workflow on an external-effect step with a failed lane attempt
  When the owner submits record_worker_failure for that attempt and step epoch
  Then the completion names the exact failed attempt
  And current_step is unchanged

Scenario: Invalid failed lane results do not mutate the workflow
  Given a failed lane result is foreign, stale, active, completed, or recorded
  When the owner submits record_worker_failure for that result
  Then the action is refused
  And the work version is unchanged

Scenario: A fresh attempt exits after a recorded lane failure
  Given the owner recorded the failed lane attempt
  When the owner starts and delivers a fresh external-effect attempt
  Then record_delivery advances the step
  And the old failed dispatch cannot block that advance
```

## Consequences

`record_worker_failure` belongs to the worker-action set in current definitions
and the generated agent payload contract. The conformance corpus keeps successful
lane exits on `accept_worker_result` and session exits on `record_delivery`.

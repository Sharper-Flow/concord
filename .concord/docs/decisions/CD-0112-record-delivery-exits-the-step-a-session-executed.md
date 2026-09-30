# CD-0112: record_delivery exits the step a session executed

- **Status:** Accepted
- **Date:** 2026-09-05
- **Scope:** the exit from an external-effect step with no dispatched lane
  attempt, failed lane-result recording and retry, the execution mode of the
  two context-continuity actions, and the architecture-spike exits
- **Approval:** The operator selected the explicit delivery action over an
  advancing hand-off summary on 2026-09-05 in Concord
  work-cabca4e5913be1f9519322f0. The operator selected a dedicated
  `record_worker_failure` action over widening `accept_worker_result` on
  2026-09-08 in Concord work-8bc9259e3a54d5db68e3a60e.
- **Related:** CD-0013, CD-0016, CD-0027, CD-0109, and issues #801, #833, #837,
  and #869
- **Amends:** CD-0016 (context continuity) by fixing the execution mode of
  `checkpoint_context` and `cross_context_boundary` to hold; extends CD-0013
  §12 with `record_delivery`; extends CD-0059 with failed-result recording

## Context

A worker-dispatch step can receive a completed or failed lane result.
`accept_worker_result` requires a completed attempt with a readback model.
Before this amendment, no workflow action could record a failed attempt.
`record_delivery` also refused every post-dispatch advance, so the step had no
recorded recovery route after `worker.failed`.

`cross_context_boundary` also appeared to advance a step, but its runtime event
never moved `current_step`. The declared mode and the recorded effect differed.
The architecture-spike `decision_record` and `review` steps had no live exits.
The original decision repaired those two gaps with hold and advance modes that
match the events the runtime records.

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
completion fold refuses `record_delivery` after a lane dispatch in that
attempt. A completed lane exits through `accept_worker_result`. A failed lane
holds the step until the owner records the failure and starts a fresh attempt.

### D3. The decision record advances, and acceptance of it lives on review

`record_decision` keeps its typed checkpoint event and now advances
`decision_record` to `review`. The assembler keys its checkpoint branch on the
declared event shape rather than the execution mode, so an action may append
a typed checkpoint and still advance. `accept_decision` moves from
`acceptance` to `review` and advances, so verdicts are recorded and the
operator accepts the decision on one step, and `acceptance` holds premise
confirmation alone.

### D4. record_worker_failure holds the step for a fresh start

Every current worker-dispatch step declares `record_worker_failure` with an
`internal_sqlite` consequence, no approval, and `hold` mode. Its payload names
the attempt ID and the current step epoch.

The fold requires an exact failed attempt from this work item. It also checks
the dispatch order, step epoch, authenticated actor, and evaluator
distinctness. It refuses active, completed, foreign, stale, and previously
recorded attempts without mutation.

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
  Then the fold refuses the advance
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

All seven current built-in definition digests change and receive new version
numbers. Prior definitions keep their existing bytes and digest pins.
`record_worker_failure` joins the worker-action set in current definitions and
the generated agent payload contract. The conformance corpus keeps successful
lane exits on `accept_worker_result` and session exits on `record_delivery`.

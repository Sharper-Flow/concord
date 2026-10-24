# CD-0109: the dispatched lane is the step's executing actor

- **Status:** Accepted
- **Date:** 2026-09-04
- **Scope:** worker-dispatch evidence, workflow actor identity, and
  `accept_worker_result`; Implement cannot accept its own result ([CD-0242 D5](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d5-execution-is-a-loop-that-main-controls))
- **Approval:** The operator selected the lane-as-executing-actor reading of
  the refusal on 2026-09-04 in Concord work-dbb32e3679f758b553ae0745, after
  issue #800 established that no dispatched result could be accepted.
- **Related:** CD-0013, CD-0017, CD-0034, CD-0059, CD-0067, and issues
  #781, #794, #800

Under [CD-0242 D5](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md), ordinary acceptance records Main's disposition and leaves progression to Main's separate act.

## Context

The dispatch route reached the store end to end on v7.1.6: the window opened,
the lane executed, `worker.dispatched` and `worker.completed` folded, and
`worker_attempts` held the completed attempt. `accept_worker_result` then
refused every acceptance with `executing actor cannot author its own verdict`.

Two adjacent checks in `validateAcceptedWorkerResult` contradicted each
other. One required the accepting actor to be the authenticated workflow
owner; the other required the accepting actor to differ from
`workflow_instances.execution_actor_ref`, which the step-start fold sets to
that same owner. No actor can satisfy both, so the action was unsatisfiable
by construction.

The boundary test never caught this because its fixture built the topology
the law intended — a distinct worker actor starting the step, a
distinct owner actor accepting — while production always had the
orchestrator start the step and never recorded the lane as any workflow
actor at all. The guard was enforced against an arrangement that only
existed in the fixture.

## Decision

### D1. The lane executes as a derived workflow actor

A `worker.dispatched` v4 dispatch carries `lane_actor_ref`. The dispatch
operation records one derived workflow actor for the lane in the
same transaction as the attempt, through a closed-shape write
route that admits exactly that pair. The lane actor's tuple is derived, not
supplied: the agent reference is the lane, the session reference is the
attempt, and the principal and client references come from the host identity
that authenticated the signed dispatch assertion. A host cannot choose the
identity its lane executes as; these are current rows written in one transaction ([CD-0242 D2](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)).

### D2. The dispatch pins the executing actor

A dispatch whose `lane_actor_ref` does not name a recorded workflow actor
refuses; otherwise the recorded actor pins the workflow instance's executing
actor. The pin is a current row on the instance, written in the dispatch's one transaction ([CD-0242 D2](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)). A work item without a running workflow instance keeps its rows
untouched.

### D3. A lane cannot accept its own result

Implement cannot accept its own result, so the owner's acceptance of a dispatched lane's completed result is compared against the recorded executing actor, and the lane's own identity submitting `accept_worker_result` refuses ([CD-0242 D5](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d5-execution-is-a-loop-that-main-controls)). No
separate final evaluator and no executing-actor distinctness beyond the loop's
rules is required, so `record_verdict` and completion add no actor comparison.

### D4. Recorded attempts read back unchanged

Each attempt result is a recorded current fact ([CD-0242 D2](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)).

## Acceptance Criteria

```gherkin
Scenario: The owner accepts a completed lane result
  Given a workflow on an external-effect step started by the owner
  And a dispatched lane whose derived actor is recorded with its dispatch
  When the owner submits accept_worker_result for the completed attempt
  Then the acceptance is recorded
  And the step does not change

Scenario: A dispatch cannot invent an actor
  Given a dispatch whose lane_actor_ref is not recorded
  When the dispatch is admitted
  Then the store refuses it with a typed failure

Scenario: A lane cannot accept its own result
  Given a dispatched lane pinned as the executing actor
  When the lane's own identity submits accept_worker_result
  Then the submission is refused as unauthorized
```

## Consequences

The CLI `worker-dispatch` verb gains no request fields: the enrichment
happens inside the evidence boundary from the authenticated assertion. The
adapter is unchanged. `worker_authority_boundary_test.go` gains a
production-topology test that starts, dispatches, and accepts as the owner,
so the fixture arrangement and the produced arrangement cannot drift apart
again. Issue #800 closes as the seventh and last blocker of this class on
the dispatch route.

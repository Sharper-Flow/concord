# CD-0109: the dispatched lane is the step's executing actor

- **Status:** Accepted
- **Date:** 2026-09-04
- **Scope:** worker-dispatch evidence, workflow actor identity, and the
  evaluator-distinctness comparison behind `accept_worker_result`
- **Approval:** The operator selected the lane-as-executing-actor reading of
  the refusal on 2026-09-04 in Concord work-dbb32e3679f758b553ae0745, after
  issue #800 established that no dispatched result could be accepted.
- **Related:** CD-0013, CD-0017, CD-0034, CD-0059, CD-0067, and issues
  #781, #794, #800
- **Extends:** CD-0013 D5 (evaluator-actor distinctness) with the actor the
  dispatched lane executes as; CD-0017 D4 (a worker legitimately runs its own
  step) with the recording that makes the claim durable

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
the law intended — a distinct `agent/worker` actor starting the step, a
distinct `agent/owner` accepting — while production always had the
orchestrator start the step and never recorded the lane as any workflow
actor at all. The guard was enforced against an arrangement that only
existed in the fixture.

## Decision

### D1. The lane executes as a derived workflow actor

A `worker.dispatched` v4 event carries `lane_actor_ref`. The dispatch
operation prepends one `workflow.actor_recorded` event for the lane, in the
same transaction as the attempt projection, through a closed-shape append
route that admits exactly that pair. The lane actor's tuple is derived, not
supplied: the agent reference is the lane, the session reference is the
attempt, and the principal and client references come from the host identity
that authenticated the signed dispatch assertion. A host cannot choose the
identity its lane executes as.

### D2. The dispatch pins the executing actor

The `worker.dispatched` fold refuses a `lane_actor_ref` that is not a
recorded workflow actor, then pins it as the workflow instance's executing
actor. A work item without a running workflow instance keeps its projection
untouched.

### D3. The distinctness comparison now binds

`accept_worker_result` still runs CD-0013 D5 unchanged. With the lane pinned
as the executing actor, the owner's acceptance is compared against the party
that actually executed, and the comparison refuses a lane accepting its own
result while admitting the owner. `record_verdict` and workflow completion
compare against the same pinned actor, so an owner who both dispatched and
evaluates remains fenced at the verdict surfaces where CD-0013 D5 was
written to bind.

### D4. Replayed history keeps its original behavior

A pre-v4 `worker.dispatched` event upcasts with an empty `lane_actor_ref`
and pins no executing actor, matching the store that recorded it. Legacy
attempts remain acceptable under the checks that precede the distinctness
comparison only where the executing actor was never a lane; the comparison
itself continues to compare against whatever actor the instance records.

## Acceptance Criteria

```gherkin
Scenario: The owner accepts a completed lane result
  Given a workflow on an external-effect step started by the owner
  And a dispatched lane whose actor event precedes its dispatch event
  When the owner submits accept_worker_result for the completed attempt
  Then the acceptance is recorded
  And the step advances

Scenario: A dispatch cannot invent an actor
  Given a worker.dispatched event whose lane_actor_ref is not recorded
  When the event folds
  Then the fold refuses the event

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

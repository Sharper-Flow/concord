# CD-0107: the outcome is the operator's, the lane report is the orchestrator's

- **Status:** Accepted
- **Date:** 2026-09-04
- **Scope:** the actor that may accept a worker attempt, the actor that may
  record a verdict, and the CD-0013 D5 distinctness rule at each boundary
- **Approval:** The operator set the rule on 2026-09-04 in Concord
  work-af9c7f08c9414260969eeb89 after the first lane completion to reach
  `accept_worker_result` was refused.
- **Related:** CD-0013, CD-0017, CD-0059, CD-0102, and issue #801
- **Refines:** CD-0013 D5
- **Preserves:** the authenticated actor tuple, the operator approval path,
  and the CD-0017 D6 readback-model dimension for workflows that declare it

## Context

CD-0013 D5 refuses a verdict whose `agent_ref` and `session_ref` equal the
recorded executing values. Issue #98 applied the same check to
`accept_worker_result` and to `complete`. Each of the three assumed a second
agent session evaluates what the first one ran.

CD-0102 D7 made the dispatching session the session that runs the lane. So
the executing actor recorded on the instance is the coordinator, and the
coordinator is the only session that holds the lane's result. The first lane
completion to succeed end to end was then refused at acceptance:
`executing actor cannot author its own verdict`. No work item in the live
store had ever exited an external-effect step.

Concord is single-operator. A second agent session exists only when the
operator opens one, and any second session passes the check. The rule as
applied measured nothing and blocked everything.

## Decision

### D1. The lane report is the orchestrator's to accept

`accept_worker_result` records that a worker attempt finished. The attempt
carries its own readback identity, its own signed evidence, and its own
lifecycle. The session that dispatched the attempt accepts it. D5
distinctness does not apply to this action.

The fold keeps every other check: the attempt exists, belongs to this work,
was dispatched after the current action start, is `completed`, reports a
readback model, and is accepted by the authenticated owner.

### D2. The verdict is the operator's to record

`record_verdict` requires operator approval and consumes the verified
operator identity, as `confirm_premise` does today. The operator becomes
`verdict_actor_ref`. D5 holds by construction: the operator is never the
executing agent.

The preflight that refused an executing actor from evaluating its own
delivery now refuses a verdict whose actor is not the operator.

### D3. Completion compares against the operator's verdict

`complete` keeps its distinctness check. The verdict it compares is
operator-authored, so a session that drove the step completes the item.

### D4. The second-agent evaluator remains for workflows that declare it

A workflow definition that sets the CD-0017 D6 readback-model dimension
still requires a distinct agent evaluator with a distinct model. That path
is opt-in. No built-in workflow declares it.

## Consequences

- One session drives an item from capture to completion. The operator's two
  acts are the contract and the verdict.
- The seven work items held at `repair` and `execution` on 2026-09-04 can
  exit through the dispatching session.
- `record_verdict` gains the approval challenge every other operator act
  carries. A session that records a verdict without one is refused with
  `approval_required`.
- Scenarios and generated contracts that pin `record_verdict` as
  approval-free move with this record.

## Rejected alternatives

- **A second coordinator session accepts.** Any second session passes D5,
  so the check would prove nothing while adding a hop to every lane.
- **The operator confirms each acceptance.** A lane report is bookkeeping a
  distinct actor already produced. Charging the operator for it spends the
  operator's attention on the wrong boundary.
- **Drop D5 everywhere.** The verdict boundary is where independence
  matters, and the operator path already supplies it.

## Verification

- `internal/store`: the dispatching session accepts its lane result; a
  verdict without operator identity refuses; a verdict with operator
  identity passes D5; completion after an operator verdict passes.
- `internal/agent`: `record_verdict` consumes an operator approval and
  stamps the operator as the verdict actor.
- Repository document, knowledge-index, and generated-contract validators
  pass.

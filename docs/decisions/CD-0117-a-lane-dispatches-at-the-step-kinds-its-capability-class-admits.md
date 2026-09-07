# CD-0117: A lane dispatches at the step kinds its capability class admits

- **Status:** Accepted
- **Date:** 2026-09-06
- **Scope:** the join between agent-lane capability classes and workflow
  step kinds: which steps carry the worker-dispatch action pair, and which
  lane a step of a given kind may dispatch
- **Approval:** The operator approved the contract in Concord
  work-96cc2a107e2dc7dc83660d02 on 2026-09-06, after the research lane was
  found unreachable from every pre-contract step (issue #892).
- **Related:** CD-0013, CD-0059, CD-0115, and issues #892, #893
- **Extends:** CD-0059 (worker dispatch actions) at the composition rule the
  action pair was attached by; amends the CD-0059 authoring decision that
  excluded the research family

## Context

CD-0059 composed `dispatch_worker` and `accept_worker_result` onto a
family's `external_effect` steps only, and excluded the research family
entirely on the reading that a research work item delegates no external
effect. Two registries validated their own artifacts and nothing joined
them: `contracts/agent-lanes.v1.json` said which lanes exist,
`contracts/workflow-definition.schema.json` said which actions a step lists,
and no contract said which step kinds a lane may be dispatched at.

The word "research" named both a work kind and a lane capability class, and
the composition rule treated the one as the other. A research work item does
cross-authority reads, but the research lane is a worker that performs
bounded reads. The result: every pre-contract step — `proposal`,
`discovery`, `design`, `reproduce`, `diagnose`, `frame`, `investigate` —
could not dispatch any lane, so a coordinator answered research questions by
hand and no attempt evidence existed (issue #892).

## Decision

### D1. The join is authored law

`contracts/lane-step-dispatch.v1.json` binds each agent-lane capability
class to the workflow step kinds that may dispatch it. Read-only classes
(`research`, `review`) reach `internal_sqlite` and `cross_authority` steps.
Classes that edit repository files or run commands (`implementation`,
`design`, `verification`) stay on `external_effect` steps, where the
workflow already fences worker attempts. No class binds `human_checkpoint`,
and terminal steps never dispatch.

### D2. Definition composition derives from the join

`withWorkerActions` composes the action pair onto every non-terminal step
whose kind the join admits, except approval-gated steps: a step whose only
advancing exit is an approval-required action keeps that exit alone, so a
worker acceptance cannot leave it without the operator. The research-family
exclusion is repealed; a research work item dispatches read-only lanes at
its read steps like any other family.

### D3. Dispatch time enforces the lane against the step

Definition composition is per-step and the join is per-lane, so the
`dispatch_worker` fold resolves the packet's lane identity through the lane
registry and refuses with `unauthorized_dispatch` when that lane's
capability class is not admitted at the current step's kind. A step that
carries the action does not by itself admit every lane.

### D4. The generator is the reachability gate

`scripts/generate-lane-step-dispatch.py` validates the join against its
schema and refuses when the classes the join names are not exactly the
classes `contracts/agent-lanes.v1.json` carries: a lane no binding admits,
or a binding no lane carries, fails generation and CI. The generated
`internal/store/generated_lane_step_dispatch.go` is the only table the
composition and the dispatch-time gate read.

### D5. Released definitions keep their content

Per CD-0115, the pre-join content of every family stays registered: the
four families with frozen version 1 gain a frozen pre-join version 2, and
the three families that shipped version 1 content keep it registered as
version 1 beside the joined version 2. The shipped shapes carry the join at
the next version, and the version pins table gains one row per new version.

## Acceptance Criteria

```gherkin
Scenario: A read-only lane dispatches at a read step
  Given a break_fix item at the reproduce step
  When the coordinator dispatches the research lane
  Then the dispatch folds and the attempt window opens

Scenario: An editing lane is refused at a read step
  Given a break_fix item at the reproduce step
  When the coordinator dispatches the implement lane
  Then the core refuses with unauthorized_dispatch

Scenario: A read-only lane is refused at an effect step
  Given a break_fix item at the repair step
  When the coordinator dispatches the research lane
  Then the core refuses with unauthorized_dispatch

Scenario: An approval-gated step keeps its only exit
  Given a step whose advancing actions require approval
  When the join composes the worker pair
  Then the step carries neither dispatch_worker nor accept_worker_result

Scenario: A lane the join does not admit fails generation
  Given a lane class the join does not name
  When the generator runs
  Then generation fails and names the mismatch
```

## Consequences

A coordinator that meets a question the repository does not answer in one
read can dispatch the research lane from the step it is on, and the attempt
lands as durable evidence. The packet a lane receives is projected from the
pinned contract, so a contract's outcome predicates are the lane's mandate:
predicates that name a file invite a file-shaped delivery, which is how
issue #892's first attempt produced a stub. The coordinator prompt rule that
names the research-lane trigger is host configuration and lands separately
(issue #893). Every family's definition version moves by one, and the
registry carries all released versions forever.

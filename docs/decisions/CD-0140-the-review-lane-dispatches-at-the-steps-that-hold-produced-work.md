# CD-0140: The review lane dispatches at the steps that hold produced work

- **Status:** Accepted
- **Date:** 2026-09-13
- **Scope:** the `review` capability-class binding in
  `contracts/lane-step-dispatch.v1.json`
- **Approval:** The operator approved this bounded contract change on
  2026-09-13 after the binding made review evidence unreachable in two live
  Toolbox work items.
- **Related:** CD-0117, CD-0138, CD-0059, CD-0115
- **Amends:** CD-0117 D1, which bound read-only classes to read steps only
- **Preserves:** CD-0117 D2–D5, the research binding, the human-checkpoint and
  terminal-step exclusions, and worker fences

## Context

CD-0117 D1 bound the read-only classes (`research`, `review`) to
`internal_sqlite` and `cross_authority` steps. Its safety argument is
one-directional: classes that edit files or run commands must stay on fenced
`external_effect` steps. Nothing in it shows that a read-only class at a fenced
step is unsafe.

CD-0138 then inserted a mandatory `refine` step of kind `external_effect`
between production and verdict. Review evidence about produced work can only
exist after production has run, and the steps that hold produced work are
`external_effect`. The two decisions compose into an unreachable state: a
contract that requires review evidence cannot bind it, because the review lane
is refused at every step that could produce the work under review. Two Toolbox
work items hit this refusal on 2026-09-13; one was cancelled with the change
already shipped, which is the bypass the workflow exists to prevent.

The discriminating principle: research answers questions that precede or
accompany production, so read steps are its natural site. Review inspects
produced work, so the steps that hold produced work are its natural site.

## Decision

### D1. Review reaches fenced steps

The `review` capability-class binding gains `external_effect`. The `research`
binding is unchanged: nothing in this decision admits research at a step that
produces work, and the existing refusal test keeps that property.

### D2. No other surface moves

Definition composition already places the dispatch pair on `external_effect`
steps, so no workflow definition, version pin, or digest changes. The generated
projection is regenerated from the amended contract, and its digest moves with
the contract.

## Acceptance Criteria

```gherkin
Scenario: The review lane dispatches at an effect step
  Given a break_fix item at the repair step
  When the coordinator dispatches the review lane
  Then the dispatch folds and the attempt window opens

Scenario: The research lane is still refused at an effect step
  Given a break_fix item at the repair step
  When the coordinator dispatches the research lane
  Then the core refuses with unauthorized_dispatch

Scenario: The generated projection matches the contract
  Given the amended join contract
  When the generator runs in check mode
  Then generation passes
```

## Consequences

A coordinator at `refine`, `execution`, or `repair` can dispatch the review
lane and bind review evidence where the produced work exists. Contracts that
require review evidence become reachable again. The change widens where a
read-only lane may run; it grants the lane no write capability, and the
attempt fence at `external_effect` steps is unchanged.

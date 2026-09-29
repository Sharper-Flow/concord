# CD-0187: The review lane dispatches at the confirmation steps that enforce review evidence

- **Status:** Accepted
- **Date:** 2026-09-28
- **Scope:** the `review` capability-class binding in
  `contracts/lane-step-dispatch.v1.json` and the confirmation-step worker
  route
- **Approval:** The operator approved this bounded join change through the
  work record that carries it.
- **Related:** CD-0059, CD-0133, CD-0140, CD-0166
- **Amends:** CD-0140, which preserved the human-checkpoint exclusion
- **Amended by:** [CD-0193](CD-0193-cd-0187-d2-and-d3-release-the-checkpoint-gate-at-the-recorded-failure.md) at D2's composed action set and D3's hold release: the engine admits the failure record at a confirmation step as a hold-mode recovery, and the hold keys on attempts a worker actually dispatched.
- **Refines:** CD-0133 D3 and D4 on the confirmation-step surface this record
  opens

## Context

A work contract can name review evidence as a requirement. The review lane
could not dispatch at a confirmation step, because no capability class bound
`human_checkpoint`. An item that reached its confirmation step with the review
kind unbound could not satisfy the requirement: `confirm_premise` refused, the
bind recovery admits the kind but no review exists to bind, and the CD-0166
gate covers only a rejected refinement result. The item wedged at acceptance
with an unsatisfiable review requirement. The implementation family recorded
one instance in that state as
[Concord (CON) issue 517](https://linear.app/sharper-flow/issue/CON-517).

The join is the single authority for definition composition and dispatch
validation. The fix belongs in the join, with the definition versions and the
hold rule that the amended join implies.

## Decision

### D1. The review class binds the confirmation steps

The `review` binding gains `human_checkpoint`. No other capability class gains
the step kind. A confirmation step that enforces review evidence can dispatch
the review lane, and the lane's report can bind there, so an item cannot wedge
with an unsatisfiable review requirement.

### D2. The confirmation steps carry the checkpoint pair

The workflow families promoted after this record compose
`dispatch_worker` and `accept_worker_evidence` onto their `human_checkpoint`
steps. The hold-mode accept binds a completed attempt's report as evidence and
keeps the step, so the operator's own gate stays the step's only advancing
exit. The advancing accept and the failure record stay off these steps.
Definitions pinned to older versions keep their digests and reach the amended
steps through the existing repin route.

### D3. The dispatch hold releases when an accept resolves the attempt

The hold that keeps advancing exits off a step with an open worker attempt
also admits `accept_worker_evidence` as a resolution: an attempt whose report
an accept has dispositioned no longer holds the step. A rejected or failed
attempt keeps holding, so the effect-step recovery routes stay as CD-0133
recorded them. The work pin reads the same rule, so the pin never offers an
advance the fold refuses.

## Alternatives considered

- Relax `confirm_premise` to skip an unbound review kind. Rejected: it removes
  the gate the contract approved.
- Compose `accept_worker_result` on the confirmation steps. Rejected: its
  advance would leave the step without the operator verdict.
- Leave the route to out-of-band `bind_evidence` calls. Rejected: a contract
  would be satisfiable without any review, which is the bypass this record
  closes.
- Add a second lane class for confirmation steps. Rejected: a second class
  duplicates the review class and splits the join's authority.
- Keep the hold blind to resolution. Rejected: the operator gate would stay
  locked after the review evidence was bound.

## Consequences

A confirmation step that enforces review evidence hosts the review lane. The
review report binds at the step and the operator gate passes on it, so the
CON-517 shape cannot recur on the promoted definitions. Every frozendefinition version keeps its digest. The new versions carry new digests, and
the version pins record them. The hold rule changes only for attempts an
accept resolves, so no effect-step recovery route moves. The join stays the
one authority: composition and dispatch validation both read the amended
binding.

## Verification

```gherkin
Scenario: The review lane dispatches at the confirmation step
  Given a break_fix item at the verify step whose contract requires review evidence
  When the coordinator dispatches the review lane
  Then the dispatch folds and the attempt window opens

Scenario: The checkpoint accept binds the report and holds the step
  Given a completed review attempt at the verify step
  When the coordinator runs accept_worker_evidence
  Then the report binds as review evidence and the step stays at verify

Scenario: The operator gate passes on the bound review
  Given the bound review evidence and a recorded verdict
  When the operator confirms the premise
  Then the item leaves the verify step

Scenario: No other lane dispatches at a confirmation step
  Given a break_fix item at the verify step
  When the coordinator dispatches the research lane
  Then the core refuses with unauthorized_dispatch

Scenario: A pinned instance reaches the amended steps by repin
  Given an item pinned to break_fix version 14 at the verify step
  When the coordinator repins the item to the current definition
  Then the review lane dispatches at the verify step
```

- `go test ./internal/store/ -run TestAcceptanceReviewRoundTrip` proves the
  first three scenarios as one route.
- `go test ./internal/store/ -run TestJoinRefusesLaneAtUnadmittedStepKind`
  proves the closed class set.
- `go test ./internal/store/ -run TestRepinReachesTheCheckpointReviewDefinition`
  proves the repin route.
- `go test ./internal/store/ -run TestWorkflowDefinitionVersionPinsHold`
  proves the frozen and promoted digests.
- `python3 scripts/generate-lane-step-dispatch.py --check` proves the
  generated projections match the amended contract.

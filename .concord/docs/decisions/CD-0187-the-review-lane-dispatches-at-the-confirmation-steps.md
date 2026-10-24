# CD-0187: The review lane dispatches at the confirmation steps that enforce review evidence

- **Status:** Accepted
- **Date:** 2026-09-28
- **Scope:** the `review` capability-class binding in
  `contracts/lane-step-dispatch.v1.json` and the confirmation-step worker
  route, with hold-mode review evidence recovery at refinement
- **Approval:** The operator approved this bounded join change through the
  work record that carries it. The operator approved D4 on 2026-10-06.
- **Related:** CD-0059, CD-0133, CD-0140, CD-0166
- **Amends:** CD-0140, which preserved the human-checkpoint exclusion
- **Amended by:** [CD-0193](CD-0193-cd-0187-d2-and-d3-release-the-checkpoint-gate-at-the-recorded-failure.md) at D2's composed action set and D3's hold release: the engine admits the failure record at a confirmation step as a hold-mode recovery, and the hold keys on attempts a worker actually dispatched.
- **Amended by:** [CD-0242](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md) D3, D5, D14
- **Refines:** CD-0133 D3 and D4 on the confirmation-step surface this record
  opens

**Approval amendment:** [CD-0243 D6](CD-0243-four-roles-and-six-operator-approval-classes.md#d6-existing-approval-requirements-follow-these-classes)
limits this record's operator-consent requirements to its six classes. Required review, actor authentication, and other non-consent checks remain required.

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

For `approval_policy=consequence_v1`, confirmation-step review dispatch remains routine and correction supersession requires no operator signature. Human confirmation depends on the recorded decision domains, not the step-kind label. Review acceptance cannot itself confirm a premise, create evaluator independence, or bypass acceptance deliverables. A record about an earlier subject authorizes nothing about a changed one ([CD-0242 D7](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d7-a-revision-invalidates-only-what-depended-on-it)).


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
[CD-0242 D14](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d14-generated-contracts-derive-from-one-source)
governs: the families derive from the one canonical source, and an item
recorded at these steps before the amended binding is revalidated against it.

### D3. The dispatch hold releases when an accept resolves the attempt

The hold that keeps advancing exits off a step with an open worker attempt
also admits `accept_worker_evidence` as a resolution: an attempt whose report
an accept has dispositioned no longer holds the step. A rejected or failed
attempt keeps holding, so the effect-step recovery routes stay as CD-0133
recorded them. The work pin reads the same rule, so the pin never offers an
advance admission refuses
([CD-0242 D3](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d3-stage-exits-are-the-unit-of-enforcement)).

### D4. Refinement can accept review evidence without delivery

At refinement, `accept_worker_evidence` admits a completed review-class report
in the implementation and break-fix families. This is an engine recovery the
one canonical source composes, not a new declared action
([CD-0242 D14](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d14-generated-contracts-derive-from-one-source)).
It binds the exact current report and holds the step without asserting delivery.

Actual dispatch, work identity, actor independence, readback, epoch, and newest
review checks remain required. Missing, foreign, unfinished, stale, and already
dispositioned reports refuse. Implementation and verification reports do not
gain this route. Delivery fields refuse on this hold-mode acceptance.

Both acceptance actions follow [CD-0242 D5](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d5-execution-is-a-loop-that-main-controls): a blocking disposition binds its findings
and holds delivery, and evidence acceptance still does not advance refinement.
The delivery exits retain their verification and assertion requirements.

CD-0133 D1 permits operator-approved correction after this disposition only
when every current authorization has a recorded terminal disposition. A new
authorization closes correction before native dispatch evidence exists. The
operator approves the exact successor contract; evidence acceptance supplies
no approval and does not reinterpret an old report as a successor result.

The work pin, preflight, and mutation share the recovery predicate.
[CD-0242 D14](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d14-generated-contracts-derive-from-one-source)
governs: derived definitions are generated from the one canonical source, and
no released version or digest is retained.

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
CON-517 shape cannot recur on the promoted definitions. [CD-0242 D14](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d14-generated-contracts-derive-from-one-source)
governs: the amended binding is generated from the one canonical source, and
its digest is the source's. Refinement review evidence has a hold-mode
recovery without a definition change. The join stays the
one authority: composition and dispatch validation both read the amended
binding.

## Verification

```gherkin
Scenario: The review lane dispatches at the confirmation step
  Given a break_fix item at the verify step whose contract requires review evidence
  When the coordinator dispatches the review lane
  Then the dispatch is admitted and the attempt opens

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

Scenario: A recorded item reaches the amended steps after revalidation
  Given an item recorded at the verify step before the amended binding
  When its recorded steps are revalidated against the canonical source
  Then the review lane dispatches at the verify step

Scenario: Refinement accepts review evidence without delivery
  Given a completed current review at refinement
  When the coordinator accepts worker evidence without delivery fields
  Then the report binds and refinement remains current without a delivery assertion

Scenario: Refinement recovery refuses a different worker class
  Given a completed implementation report at refinement
  When the coordinator accepts worker evidence
  Then the recovery refuses without changing the work

Scenario: Evidence acceptance keeps a blocking disposition
  Given a review report with the disposition correction required at refinement
  When the coordinator accepts its evidence and later accepts a ready for final proof report
  Then delivery stays held until the ready report while refinement remains current
```

- `go test ./internal/store/ -run TestAcceptanceReviewRoundTrip` proves the
  first three scenarios as one route.
- `go test ./internal/store/ -run TestJoinRefusesLaneAtUnadmittedStepKind`
  proves the closed class set.
- [CD-0242 D14](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d14-generated-contracts-derive-from-one-source) retires old-definition repin and digest-pin witnesses in favor of one canonical source; `TestCurrentDefinitionsHaveOneSourcePerLiveFamily` checks that source without retained versions.
- `python3 scripts/generate-lane-step-dispatch.py --check` proves the
  generated projections match the amended contract.

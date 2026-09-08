# CD-0116: the operator signs the verdict after an in-session delivery

- **Status:** Accepted
- **Date:** 2026-09-06
- **Scope:** the evaluator identity for `record_verdict` on an item whose
  external-effect work exited through `record_delivery` or
  `accept_worker_result`
- **Approval:** The operator selected this amendment on 2026-09-06 in Concord
  work-670e07fe80d7b4b2d67811c7, choosing it over closing the live item by
  hand-recorded verdicts from a second session identity (#888).
- **Amendment:** The operator selected the lane-exit extension on 2026-09-08
  in Concord work-2304a1e490993be0752cc908 (#890): D2 and D3 admit the
  accepted lane exit.
- **Related:** CD-0013, CD-0109, CD-0112, and issues #801, #865, #888, #890
- **Amends:** CD-0013 D5 (evaluator-actor distinctness) by adding one
  conditioned operator-identity path; CD-0109's distinct-actor rule is
  unchanged everywhere else

## Context

CD-0112's `record_delivery` is the exit for an external-effect step the
session executed itself: no lane ran, no attempt row exists. The capturing
session is the instance's pinned executing actor, so when the workflow
reaches its verdict step, CD-0013 D5 refuses the session's `record_verdict`
(`executing actor cannot evaluate its own delivery`) — correctly. But no
distinct evaluator exists either. The item cannot complete unless the
operator hand-records verdicts from another session identity, which is a
per-item tax on exactly the delivery shape the law now encourages.

`work-cf638e555f8f333fb087f704` (#865, fix merged in v7.8.1) wedged here on
2026-09-06, which produced this record.

PR #810 once proposed operator-authored verdicts generally and was closed by
operator decision in favor of CD-0109's "any actor distinct from the lane."
That decision stands: wherever a lane executed, a distinct evaluator exists
and the ordinary rule applies. This record covers only the shape where none
exists.

The lane-exit amendment came from the same wedge one step later. After
`accept_worker_result`, the accepting session authored the acceptance, so
CD-0109's authorship set refuses its verdict while no in-session distinct
evaluator exists. `work-456c7660f51529851977488d` wedged at `verify` on
2026-09-07 (#890), which produced the D2-D3 extension.

## Decision

### D1. The operator's signed identity is the evaluator after an in-session exit

`record_verdict` and its terminal act `complete` accept the operator
identity — the `confirm_premise` mechanism: a host approval assertion
consumed with `RequireOperatorIdentity`, the operator stamped as
`verdict_actor_ref` and the events' actor, the operator row recorded by the
guard. D5 holds by construction because the operator cannot hold a delivery
lease or author a delivery action; `ValidateDistinctWorkflowActors` already
treats an agent executor with an operator verdict as distinct (the
`confirm_premise` precedent). Completion needs the same identity because the
completion fold compares the completing event actor against the executing
lease, which after an in-session delivery is the session itself.

### D2. The path is conditioned on an in-session exit

The operator identity is admitted only when the item carries a
`workflow.action_completed` with `action_id` `record_delivery` or
`accept_worker_result`. Both exits leave the submitting session inside the
authorship set and offer no in-session distinct evaluator. Any other state —
no exit at all, or an exit still in flight — refuses with a typed
`invalid_operation`.

### D3. The agent surface mints the challenge at the wedge

When the session submitting `record_verdict` holds the item's executing
lease after either admitted exit, the surface mints the operator approval
challenge instead of letting the store refuse blind, exactly as
approval-required actions do. A distinct session keeps the ordinary verdict
route and is never asked for an approval.

## Acceptance Criteria

```gherkin
Scenario: The operator signs the verdict after an in-session delivery
  Given a workflow whose external-effect step exited through record_delivery
  And the submitting session is the pinned executing actor
  When the session submits record_verdict with the operator's signed approval
  Then the verdict is recorded with the operator as the verdict actor
  And the completion gate accepts it

Scenario: The operator signs the verdict after an accepted lane
  Given a workflow whose external-effect step exited through accept_worker_result
  And the submitting session is the pinned executing actor
  When the session submits record_verdict with the operator's signed approval
  Then the verdict is recorded with the operator as the verdict actor
  And the completion gate accepts it

Scenario: The exiting session is still refused
  Given the same item after either admitted exit
  When the session submits record_verdict without the operator identity
  Then the submission is refused as self-evaluation

Scenario: The condition binds
  Given a workflow with neither a record_delivery nor an accept_worker_result exit
  When any caller submits record_verdict with the operator identity
  Then the submission is refused
  And the refusal names the in-session exit condition

Scenario: The lane route is unchanged
  Given a workflow whose external-effect step exited through accept_worker_result
  When a session distinct from the lane submits record_verdict
  Then the verdict is recorded
```

## Consequences

`guardOperatorPremiseActor` admits the operator identity on `record_verdict`
besides `confirm_premise`. The verdict arm maps the operator ref explicitly,
mirroring the premise arm. The store exposes `WorkflowExecutingActor` so the
agent surface can mint the challenge only for the lease-holding session.
`requireOperatorVerdictExit` admits both in-session exits. The complexity
budget records the verdict arm's growth.

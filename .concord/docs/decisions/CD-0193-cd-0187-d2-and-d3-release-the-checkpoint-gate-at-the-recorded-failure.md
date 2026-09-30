# CD-0193: CD-0187 D2 and D3 release the checkpoint gate at the recorded failure, and an unused dispatch window holds

- **Status:** Accepted
- **Date:** 2026-09-29
- **Scope:** The worker actions CD-0187 D2 composes onto the
  `human_checkpoint` steps, the dispatch hold CD-0187 D3 reads at those
  steps, and the hold-mode failure record the workflow engine admits there;
  [Concord (CON) issue 430](https://linear.app/sharper-flow/issue/CON-430)
- **Amends:** CD-0187 D2 at its sentence ("The advancing accept and the
  failure record stay off these steps."), and CD-0187 D3 at its sentence
  ("The hold that keeps advancing exits off a step with an open worker
  attempt also admits `accept_worker_evidence` as a resolution.")
- **Preserves:** CD-0187 D1 in full, both amended clauses at every other
  sentence, the hold-mode accept, and the CD-0133 effect-step recovery routes
- **Related:** CD-0059, CD-0133, CD-0166, CD-0179,
  [Concord (CON) issue 430](https://linear.app/sharper-flow/issue/CON-430)
- **Approval:** The operator accepted the work item CON-430 contract that
  carries this record on 2026-09-29.

## Context

CD-0187 D2 keeps the failure record off the confirmation steps, and D3
releases the dispatch hold only when an accept resolves the attempt. A review
lane that failed at a verify checkpoint then had no declared recovery:
`confirm_premise` refused with the dispatched-attempt hold, the failure
record refused because no step declared it, and the item wedged at the
operator's own gate.

A second defect sat beside the first. A new `dispatch_worker` window opened,
no worker ever used it, and the hold lifted anyway. The pin offered
`confirm_premise`, the confirm folded, and the item followed the failure edge
to refine on a window no worker had reported from.

Both defects share one cause. The hold keyed on the latest workflow action
start, and a window open is an action start. The window moved the baseline
past the failed attempt, so the attempt stopped holding, and the step offered
no route that could disposition it.

## Decision

### D1. The checkpoint failure record is a hold-mode engine recovery

The confirmation steps keep the CD-0187 pair composed. The failure record
stays off their declared actions, and the engine admits
`record_worker_failure` at a `human_checkpoint` step when the latest attempt
a worker actually dispatched there is failed and no failure record
dispositions it yet. The admission is a recovery: the record carries the
pinned definition's hold-mode action, the step keeps its position, and the
pinned definition digest never moves, so no repin is needed.

CD-0187 D2 says: "The advancing accept and the failure record stay off these
steps." That sentence reads instead: the advancing accept stays off these
steps, and the failure record stays off their declared actions while the
engine admits it as the hold-mode recovery of this clause. The operator's own
gate remains the step's only advancing exit.

### D2. The checkpoint hold releases on the record, the accept, or a used window

The hold rule is one rule the fold and the work pin both read, with two
step-shaped branches.

At a `human_checkpoint` step, the latest attempt a worker actually dispatched
holds the step. An attempt stops holding only when an accept dispositions its
completed report, a failure record dispositions it at the checkpoint, or a
later worker actually dispatches on the step. An authorized dispatch window
no worker used is not a disposition, so a failed attempt keeps holding the
operator's own gate until a worker uses a window or a failure record releases
it.

CD-0187 D3 says: "The hold that keeps advancing exits off a step with an open
worker attempt also admits `accept_worker_evidence` as a resolution." That
sentence reads instead: at a confirmation step the hold also admits the
failure record of D1 as a resolution, and keys on attempts a worker actually
dispatched rather than on windows the step opened.

Every other step keeps the CD-0133 timing: an attempt dispatched after the
latest workflow action start holds the step, so the recorded effect-step
route that records the failed attempt and starts a fresh fenced attempt
reopens the exits exactly as CD-0133 recorded it. A rejected attempt keeps
holding on every step. The work pin reads the same rule, so the pin never
offers an advance the fold refuses, and it advertises the failure record
exactly while the fold admits it.

## Alternatives considered

- Compose `record_worker_failure` onto the checkpoint steps. Rejected: the
  composition moves every promoted definition digest and wedges the instances
  pinned to them, while the engine recovery keeps the digests frozen.
- Release the hold when a window opens, on every step. Rejected: the unused
  window was the second defect, and the confirm followed the failure edge on
  a window no worker used.
- Release the hold on the failure record at every step. Rejected: CD-0133's
  effect-step route opens the exits through the fresh fenced attempt, and an
  early release would advance before any fresh attempt exists.
- Keep the hold blind to the failed attempt and repin the item to a repaired
  definition. Rejected: a repin is not a recovery for a failed attempt, and
  the digest pins exist so the recorded contract stays stable.

## Consequences

A failed review at a confirmation step has a declared recovery. The record
dispositions the attempt, the checkpoint stops holding, and the operator's
own gate opens, so the failure edge to refine stays reachable.

An authorized window no worker used releases nothing at a confirmation step,
so a failed attempt can no longer be advanced past on an unused window. The
pinned definitions keep their digests, and instances reach the recovery
without a repin.

The pin advertises the failure record while the failed attempt holds and
stops advertising once the record lands, so the pin and the fold state one
answer.

## Verification

```gherkin
Scenario: The failed checkpoint review records and releases the gate
  Given a break_fix item at the verify checkpoint with one failed review attempt
  When the coordinator runs record_worker_failure and then confirm_premise
  Then the record holds the step and the confirmation leaves the step

Scenario: An unused dispatch window keeps the hold
  Given the failed review attempt and a dispatch window no worker used
  When the operator confirms the premise
  Then the fold refuses with the dispatched-attempt hold

Scenario: The pin matches the fold on the recovery
  Given the failed review attempt unrecorded
  Then the pin advertises record_worker_failure as worker_failure_recovery
  When the failure record lands
  Then the pin stops advertising it and the pinned digest is unchanged

Scenario: A later worker actually dispatching supersedes the failed attempt
  Given the failed review attempt and a retry attempt a worker dispatched
  When the coordinator records the failed attempt
  Then the record refuses and the retry attempt is the one that holds

Scenario: The effect-step recovery route is unchanged
  Given a live worker attempt at an effect step
  When the failure is recorded and a fresh fenced attempt starts
  Then the delivery exit reopens as CD-0133 recorded it
```

- `go test ./internal/store/ -run TestCheckpointFailedReview` proves the
  first and third scenarios.
- `go test ./internal/store/ -run
  TestCheckpointUnspawnedDispatchKeepsTheFailedReviewHold` proves the second
  scenario.
- `go test ./internal/store/ -run
  TestCheckpointRetryDispatchSupersedesTheFailedReview` proves the fourth
  scenario.
- `go test ./internal/store/ -run
  TestIssue1013ContractCorrectionAtFailedWorkerDispatch` proves the fifth
  scenario.
- `python3 scripts/check-doc-contract.py` proves this record carries the
  current decision outline and passes the writing rules.
- `python3 scripts/check-knowledge-index.py` and
  `python3 scripts/check-knowledge-closure.py` prove this record registers
  with a current content hash and no unprocessed document remains.
- `python3 scripts/check-cd-allocation.py --no-fetch` proves the CD-0193
  identifier allocates once.

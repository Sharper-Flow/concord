# CD-0201: Workflow action admission is derived once and every reachable state stays live

- **Status:** Accepted
- **Date:** 2026-10-01
- **Scope:** Workflow action admission in the store; the post-rejection
  review debt settlement clause; the workflow liveness checks
- **Extends:** CD-0172 at D1 and CD-0193 at D2, which require shared
  predicates; CD-0115 at D2, which keeps released definition versions in
  scope
- **Amends:** CD-0166's settlement clause in one point — a fresh accepted
  review settles the post-rejection review debt only when its verdict is
  ship, or absent for the pre-CD-0197 reports
- **Preserves:** every other route refusal keeps its own scope (CD-0186);
  payload and identity checks stay in the guards; no definition version
  changes
- **Related:** CD-0043, CD-0133, CD-0137, CD-0143, CD-0164, CD-0166,
  CD-0173, CD-0187, CD-0197
- **Approval:** The operator approved the approach in session chat on
  2026-10-01, including the well-formed-state exit check and excluding
  runtime invariant guards and random-walk conformance.

## Context

About seventeen defects since 2026-09-28 report one failure shape. A
nonterminal work item reaches a workflow state where every forward and
recovery action refuses. The item strands.

Admission for each action is computed by hand at four or more sites:
preflight, the guard table, the work pin intents, the dispatch fold, and the
correction binding. Conditions such as settled review debt, the same-step
wall, and an unsettled attempt are re-derived per site. The copies drift, and
each drift ships as another guard clause fix.

The only liveness check was a bounded sampler. It skipped the dispatch
action, stopped at twelve steps, and called the result inconclusive when the
budget ran out. No check detected a stranded state, so the class kept
recurring one guard clause at a time.

## Decision

### D1. One derivation owns action admission

A tx-scoped loader folds one work item's history once into a comparable
abstract admission state. A pure function `workflowAdmit` decides one
action's admission over that folded state and returns the decision, the
ready review, and the typed refusal. Preflight, the guard table, the work
pin intents, the dispatch fold, and the correction binding call the loader
and the pure function. They no longer re-derive the folded conditions per
site.

The loader takes a queryer, not a store, so a caller inside a transaction
never calls back into the pooled connection (the store connection
invariant). The request payload checks and the attempt identity checks stay
in the guards. The accept guard binds the request's attempt against the
ready review the decision names.

The folded state covers every condition the five sites read when this
record was accepted: the step and lifecycle, the instance state, the active
contract count, the law and registry pin staleness, the breaking impact
notices, the design currency, the latest worker attempt and its capability
class, the latest result disposition, the post-rejection review debt and
its settling verdict, the recovery routes, the same-step wall and its
operator approval, the dispatch hold, the evidence-binding recovery, and
the correction classifications. A duplicated contract projection folds on:
the count classifies the supersede recovery, and the singular-reader folds
degrade instead of refusing the route that recovery owns.

The liveness checks count only exits the folded state proves, so they fail
rather than assume. Widening the liveness, well-formed-exit, and
conformance checks to the full folded state is later work in a separate
record and does not change this record's law.

### D2. The liveness law

For every registered definition version:

- Every well-formed nonterminal abstract state admits at least one action
  that moves the work out of the state. A hold action whose successor equals
  the state counts as no exit.
- From every model-reachable nonterminal abstract state, a terminal step is
  reachable through admitted actions. Operator-approvable routes count as
  admitted.

Both checks run over the abstract state space of every registered version,
because stranded items run on old pins (CD-0115 D2). The checks fail with a
witness and never skip. A conformance test replays the real engine on
witness paths and compares the loaded state with the abstract successor at
every step, so the model cannot drift from the fold.

No runtime guard enforces the law. The law binds design and review through
the checks alone. The model states which admission families it folds; an
exit the model cannot prove is not counted, so the checks fail rather than
assume.

### D3. The review debt settles only on a settling verdict

A fresh accepted review settles the post-rejection review debt only when its
typed verdict is ship, or absent for the reports that predate CD-0197. A
no_ship review binds its findings and leaves the debt outstanding.

The consequence: refine does not advance to delivery behind a no_ship
review, and an item parked on the delivery gate keeps the evidence-bearing
corrective return. The gate admits request_correction while the debt stands.
This amends CD-0166's settlement clause in this one point. Every other
refusal on every other route keeps its scope (CD-0186).

### D4. No definition version changes

CD-0115 D1 freezes released definition content. Admission code is not
definition content, so the correction ships without a new definition
version. The definition digests stay unchanged, and pinned instances receive
the corrected admission on release.

## Alternatives considered

- Keep per-site predicates and add a cross-site agreement test. Rejected:
  the test detects drift after it ships and leaves five owners.
- Move admission into the definition JSON. Rejected: workflows/ is a
  reserved boundary, and runtime predicates read event history a definition
  cannot express.
- Require only one admitted action per state. Rejected: a hold loop admits
  actions forever, so the weaker property passes hold loops.
- Check only the latest version per family. Rejected: pinned instances run
  old versions, so the strand survives where it hurts.
- Raise the sampler depth and budget. Rejected: still bounded, still slow,
  still inconclusive.
- Treat a no_ship review as settled debt. Rejected: it advertises delivery
  for a result the review refused.
- Add a delivery-gate special case for no_ship. Rejected: one more clause at
  one site is the pattern this record removes.
- Require reject_worker_result for a no_ship review. Rejected: it discards
  valid findings and blames the reviewer.

## Consequences

Every admission site that folds one of these conditions answers identically
for the same history. A change that strands a reachable state fails the
liveness checks with a witness before merge. A no_ship review can no longer
carry an unreviewed result to delivery, and a parked gate keeps its typed
corrective return.

The checks count only exits the folded state proves, so a future strand
surfaces as a failing check rather than a silent pass. Each later family
grows the same fold and the same pure function.

## Verification

```gherkin
Scenario: A well-formed nonterminal state keeps a non-continuity exit
  Given every registered definition version
  When the check enumerates each well-formed nonterminal abstract state
  Then each state admits at least one action that moves the work out of it

Scenario: Every reachable nonterminal state reaches a terminal step
  Given every registered definition version
  When the check walks the abstract successor from the start state
  Then every reachable nonterminal state reaches a terminal step through admitted actions

Scenario: A seeded stranded step fails the exit check
  Given a definition whose step declares only hold actions
  When the exit check enumerates its states
  Then the check fails and names the stranded state

Scenario: The loaded state matches the abstract successor
  Given a witness path of real actions on a seeded work item
  When the replay performs each action
  Then the loader folds the state the abstract successor computed

Scenario: A no_ship review keeps the debt outstanding
  Given a rejected refinement result and a fresh review with a no_ship verdict
  When the coordinator accepts the no_ship review
  Then the debt stays outstanding and the pin hides the delivery exits

Scenario: A parked gate keeps the corrective return behind a no_ship review
  Given an item parked on the delivery gate with the debt outstanding
  When the operator requests correction with evidence
  Then the gate admits request_correction and the item returns to repair
```

- `go test ./internal/store/ -run 'TestWellFormedAdmissionStateHasNonContinuityExit|TestWellFormedExitCheckNamesSeededStrandedState'` proves the first and third scenarios.
- `go test ./internal/store/ -run TestReachableAdmissionStateReachesTerminal` proves the second scenario.
- `go test ./internal/store/ -run TestAdmissionConformanceReplayRejectionReviewAndSettlingAccept` proves the fourth scenario.
- `go test ./internal/store/ -run 'TestNoShipReviewKeepsRefineExitAndGateCorrectionHonest|TestNoShipReviewLeavesParkedGateCorrectionAdmitted'` proves the fifth and sixth scenarios.
- `go test ./internal/store/ -run TestWorkflowAdmitDecisionTable` proves the pure function's total decision table and the settlement verdict rule.
- `python3 scripts/check-doc-contract.py --report-only` proves this record carries the current decision outline and passes the writing rules.
- `python3 scripts/check-cd-allocation.py --no-fetch` proves the CD-0201 identifier allocates once.

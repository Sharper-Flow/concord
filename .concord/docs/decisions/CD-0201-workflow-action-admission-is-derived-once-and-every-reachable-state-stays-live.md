# CD-0201: Workflow action admission is derived once and every reachable state stays live

- **Status:** Accepted
- **Date:** 2026-10-01
- **Scope:** Workflow action admission in the store; the workflow liveness
  checks
- **Preserves:** every other route refusal keeps its own scope (CD-0186);
  payload and identity checks stay in the guards; no generated contract
  changes
- **Related:** CD-0043, CD-0143, CD-0166, CD-0173, CD-0197
- **Approval:** The operator approved the approach in session chat on
  2026-10-01, including the well-formed-state exit check and excluding
  runtime invariant guards and random-walk conformance.

## Context

About seventeen defects since 2026-09-28 report one failure shape. A
nonterminal work item reaches a workflow state where every forward and
recovery action refuses. The item strands.

Admission for each action is computed by hand at four or more sites:
preflight, the guard table, the work pin intents, the dispatch recording, and
the correction binding. Conditions such as the accepted Review disposition, the same-step
wall, and an unsettled attempt are re-derived per site. The copies drift, and
each drift ships as another guard clause fix.

The only liveness check was a bounded sampler. It skipped the dispatch
action, stopped at twelve steps, and called the result inconclusive when the
budget ran out. No check detected a stranded state, so the class kept
recurring one guard clause at a time.

## Decision

### D1. One derivation owns action admission

A tx-scoped loader derives one work item's admission state once from current
rows into a comparable abstract admission state
([CD-0242 D2](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)).
A pure function `workflowAdmit` decides one
action's admission over that derived state and returns the decision, the
ready review, and the typed refusal. Preflight, the guard table, the work
pin intents, the dispatch recording, and the correction binding call the loader
and the pure function.

The loader takes a queryer, not a store, so a caller inside a transaction
never calls back into the pooled connection (the store connection
invariant). Request payload checks stay in the guards. Admission binds the
recorded Review to its exact subject and dependencies before it decides.

The derived admission state covers every condition the five sites read: the step and lifecycle, the instance state, the active
contract count, the law and registry pin staleness, the breaking impact
notices, design currency, the accepted Review disposition of the
current candidate, recovery routes, evidence-binding recovery and
the correction classifications. A duplicated contract projection follows the
same rule: the count classifies the supersede recovery, and the singular-reader
derivations degrade instead of refusing the route that recovery owns
([CD-0242 D2](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)).

Review, correction and context projections use their current findings and
retained receipts without inventing worker lineage. Native preparation and
execution keep their own bundle, readiness and current-subject checks.
They do not create a second admission derivation or new evidence authority.

The liveness checks count only exits the derived state proves, so they fail
rather than assume. Widening the liveness, well-formed-exit, and
conformance checks to the full derived state is later work in a separate
record and does not change this record's law.

### D2. The liveness law

For CD-0013 D11's consequence policy, the same derivation derives the policy staleness pin, decision-domain class, confirmation authority, and evaluator prerequisites. Engineering states retain contract and confirmation actions without an operator question. Independent evaluator actions must be genuinely admitted before the model counts their exit. Removed consent never supplies evidence or clears a recovery wall. Both liveness checks and real conformance witnesses cover engineering, decision-bearing, and the registered current definitions
([CD-0242 D14](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d14-generated-contracts-derive-from-one-source)).

For every registered definition:

- Every well-formed nonterminal abstract state admits at least one action
  that moves the work out of the state. A hold action whose successor equals
  the state counts as no exit.
- From every model-reachable nonterminal abstract state, a terminal step is
  reachable through admitted actions. Operator-approvable routes count as
  admitted.

Both checks run over the abstract state space of the registered definitions,
because every definition a stranded item could run is registered current law
([CD-0242 D14](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d14-generated-contracts-derive-from-one-source)).
The checks fail with a
witness and never skip. A conformance test replays the real engine on
witness paths and compares the loaded state with the abstract successor at
every step, so the model cannot drift from the derivation.

No runtime guard enforces the law. The law binds design and review through
the checks alone. The model states which admission families it derives; an
exit the model cannot prove is not counted, so the checks fail rather than
assume.

Missing oracle readiness, an oracle defect, or a new uncovered case must
retain declared preparation, correction, contract-supersession, and operator-stop
recovery as applicable. Recovery never erases open blockers, and no attempt
count, limit, or reset exists
([CD-0242 D6](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d6-native-task-belongs-to-the-host)).
Each family extends the same admission owner; liveness checks count
only the oracle dimensions their model actually derives.

### D4. No generated contract changes

Admission code is not generated contract content, so the correction changes
no generated contract, and active work derives the identities it needs at the
integration boundary
([CD-0242 D14](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d14-generated-contracts-derive-from-one-source)).

## Alternatives considered

- Keep per-site predicates and add a cross-site agreement test. Rejected:
  the test detects drift after it ships and leaves five owners.
- Move admission into the definition JSON. Rejected: workflows/ is a
  reserved boundary, and runtime predicates read current state a definition
  cannot express
  ([CD-0242 D2](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)).
- Require only one admitted action per state. Rejected: a hold loop admits
  actions forever, so the weaker property passes hold loops.
- Raise the sampler depth and budget. Rejected: still bounded, still slow,
  still inconclusive.

## Consequences

Every admission site that derives one of these conditions answers identically
for the same current state and host observations (CD-0239 D3). A change that strands
a reachable state fails the
liveness checks with a witness before merge.

The checks count only exits the derived state proves, so a future strand
surfaces as a failing check rather than a silent pass. Each later family
grows the same derivation and the same pure function
([CD-0242 D2](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)).

## Verification

```gherkin
Scenario: A well-formed nonterminal state keeps a non-continuity exit
  Given the registered definitions
  When the check enumerates each well-formed nonterminal abstract state
  Then each state admits at least one action that moves the work out of it

Scenario: Every reachable nonterminal state reaches a terminal step
  Given the registered definitions
  When the check walks the abstract successor from the start state
  Then every reachable nonterminal state reaches a terminal step through admitted actions

Scenario: A seeded stranded step fails the exit check
  Given a definition whose step declares only hold actions
  When the exit check enumerates its states
  Then the check fails and names the stranded state

Scenario: The loaded state matches the abstract successor
  Given a witness path of real actions on a seeded work item
  When the replay performs each action
  Then the loader derives the state the abstract successor computed

```

- `go test ./internal/store/ -run 'TestWellFormedAdmissionStateHasNonContinuityExit|TestWellFormedExitCheckNamesSeededStrandedState'` proves the first and third scenarios.
- `go test ./internal/store/ -run TestReachableAdmissionStateReachesTerminal` proves the second scenario.
- `go test ./internal/store/ -run 'TestAdmissionConformanceLocalJobAcceptHolds|TestCurrentReviewAdmissionLoaderConformsToAcceptedDisposition'` proves the fourth scenario through current Main intake and all four Review dispositions.
- `go test ./internal/store/ -run TestWorkflowAdmitDecisionTable` proves the pure function's total decision table.
- `TestOwnerOracleRecovery` covers the open-set bound and declared recovery without truncation or a second admission owner.

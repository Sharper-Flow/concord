# CD-0234: The engine has no external-condition resolver

- **Status:** Accepted
- **Date:** 2026-10-17
- **Scope:** Live evaluation of typed external conditions in the workflow engine
- **Preserves:** CD-0008 D5's closed condition fields, `blocks` participation,
  no-polling rule, and resolution evidence rules; CD-0013 D10 item 2 and
  criteria 27-28; recorded conditions stay readable as current rows
  ([CD-0242](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md) D2)
- **Approval:** 2026-10-17. Coordinator approval under the operator's delegation
  for [Concord (CON) issue 1081](https://linear.app/sharper-flow/issue/CON-1081).

## Context

CD-0013 D6 and criterion 26 require a resolver for the closed `await_type` set,
evaluated on explicit request and at consequential boundaries. CD-0008 D5
requires the same evaluation. Only `workflow.ops_runbook` declares
`add_condition`, `resolve_condition`, and `cancel_condition`, and CD-0218
archives it. No retained family can open a condition, so no production route
can reach a resolver.

## Decision

### D1. No resolver exists while no family declares a condition action

No retained workflow family declares `add_condition`, `resolve_condition`, or
`cancel_condition`, and the engine has no condition resolver. A family may
declare a condition action only under an accepted decision that also adds a
resolver bound to the closed `await_type` set. That resolver evaluates on
explicit request and at consequential boundaries, and resolves only with the
owning authority's evidence.

### D2. An open condition refuses and stays readable

A consequential action on work with an open condition refuses before any write.
Recorded conditions stay readable as current rows
([CD-0242 D2](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)).
No
wait authority exists.

## Alternatives considered

- Wire a resolver for the closed set at the action boundary. Rejected: no
  retained family can open a condition for it to resolve.
- Declare the condition actions on a retained family. Rejected: it reverses
  CD-0218 D1's catalog and adds behavior no accepted need asks for.

## Consequences

The workflow-engine contract §10 states that no resolver exists. CD-0008 D5's
timer, failure, and heuristic bullets bind a future resolver only.

## Verification

- `internal/store.TestNoRetainedWorkflowVersionDeclaresAConditionAction` walks
  every registered definition version and fails when a retained family
  declares a condition action.
- `internal/store.TestWorkflowActionBoundaryRefusesOpenConditionThroughAdmission`
  proves that a consequential action on work with an open condition refuses
  through the admission refusal with no resolver argument.
- `WF26` and `WF28`, run by
  `internal/store.TestWorkflowScenarioCorpusExecutesExactProductionActions`,
  assert the archived-family refusal of every condition action and that
  recorded conditions read back unchanged (CD-0242 D2).

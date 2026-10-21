# CD-0235: The independent verdict is the outcome strength evaluation

- **Status:** Accepted
- **Date:** 2026-10-17
- **Scope:** Who evaluates outcome strength, and what completion refuses on
- **Amends:** [CD-0012](CD-0012-bind-stated-goals-to-delivered-outcomes.md) D3
  (second paragraph); [CD-0013](CD-0013-workflow-engine-mechanism.md) D4
  (strength comparison paragraph)
- **Preserves:** CD-0012 D3's strengthen-only rule, D4's omission refusal,
  D7's authorship fencing and evaluator distinctness, and D9's
  premise confirmation; CD-0013 D4's closed predicate union and outcome-token
  evidence rule, D5's actor tuple, and D10's completion clauses;
  [CD-0218](CD-0218-the-live-workflow-catalog-retains-three-execution-families.md)
  D6's independent-verdict obligation
- **Approval:** 2026-10-17. Coordinator approval under the operator's delegation
  for [Concord (CON) issue 1079](https://linear.app/sharper-flow/issue/CON-1079).
- **Amended by:** [CD-0242](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md) D9

## Context

CD-0012 D3 calls the refusal of a weaker delivery "a structural refusal
rather than a reviewer's opinion". CD-0013 D4 makes strength comparison
mechanical for `exists` and `absent`, trivial for `outcome`, and delegated to a
registered evaluator for `check`. No delivered predicate is recorded:
`workflow.verdict_recorded` carries the verdict kind and the incomparable flag
(CD-0013 D3). No ground-truth surface registry or check catalog exists
(CD-0013 deferred items 2 and 3). Completion refuses on the recorded verdict of
an actor distinct from the executor (CD-0013 D10).

## Decision

### D1. The independent verdict actor evaluates strength

For each approved predicate, the verdict actor applies CD-0012 D3's
strengthen-only rule and the per-kind rules of the workflow-engine contract
§6. It records the result as `verdict_kind` and `incomparable_with_approved`,
bound to evidence. It records `ok` only when the delivered end-state is equal
to or stronger than the approved one and holds on the named surface. An
approved absence that was archived, relocated, renamed, or disabled is
`outcome_mismatch`. A result that cannot be read or determined is never `ok`.
For a definition that requires a decision record, it records `ok` only when
the latest accepted decision record's token is in the approved allowed set.
The verdict actor is distinct from the executing actor (CD-0012 D7).

### D2. Completion refuses on the recorded verdict

[CD-0242 D9](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d9-completion-needs-proof-approvals-delivery-and-publication)
governs: completion requires a passing receipt for every accepted criterion on
the final candidate, the accepted ready disposition on that candidate, no open
in-scope blocking finding, every applicable approval, and delivery evidence.

### D3. The verdict actor interprets surfaces and checks

Until an accepted decision supplies the check catalog or the ground-truth
surface registry (CD-0013 deferred items 2 and 3), no registry resolves
`surface` and `check_ref` values. Approval checks their grammar, and the
verdict actor interprets them.

## Alternatives considered

- Wire a mechanical evaluator into completion. Rejected: no delivered predicate
  is recorded and no resolver exists, so wiring needs a new event field, a
  surface registry, and a check catalog that no accepted need asks for.
- Record a delivered predicate and compare it without resolvers. Rejected: the
  verdict actor would author both sides of the comparison.

## Consequences

The test-only evaluator in `internal/store/workflow_outcome.go` is deleted.
CD-0012 scenarios 4 and 5 and CD-0013 criteria 4 and 5 are proved by
recorded verdicts. Refusal of a relocated absence is proved by a recorded
`outcome_mismatch` verdict on an `absent` predicate. A weaker delivery is
refused only when the independent evaluator records it as weaker or
incomparable. An evaluator error is not caught mechanically; the operator's premise confirmation (CD-0012 D9) remains.

## Verification

- Completion refuses a recorded `outcome_mismatch` verdict and a recorded
  incomparable verdict, and appends no `workflow.completed` event
  (`WF04-weaker-delivery`, `WF50-one-conjunct-weaker`,
  `internal/store.TestConjunctiveCompletionRefusesOnTheRecordedVerdict`).
- Completion accepts when every approved predicate has an `ok`, comparable,
  independent verdict (`WF05-stronger-delivery`,
  `internal/store.TestPinnedConjunctiveContractCompletes`).
- Completion refuses a verdict whose actor executed a step of the item
  (`internal/store.TestLaneDispatchRotatesTheLeaseNotTheAuthorship`,
  `WF13-verdict-actor-distinctness`).

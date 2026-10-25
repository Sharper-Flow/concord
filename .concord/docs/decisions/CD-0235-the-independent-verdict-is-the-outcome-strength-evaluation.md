# CD-0235: The outcome strength evaluation is Review's judgment

- **Status:** Accepted
- **Date:** 2026-10-17
- **Scope:** Who evaluates outcome strength, and what completion refuses on
- **Preserves:** CD-0012 D3's strengthen-only rule, D4's omission refusal,
  and D9's premise confirmation; CD-0013 D4's closed predicate union and
  outcome-token evidence rule; Review's report and Main's acceptance of the
  disposition ([CD-0242](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md) D5)
- **Approval:** 2026-10-17. Coordinator approval under the operator's delegation
  for [Concord (CON) issue 1079](https://linear.app/sharper-flow/issue/CON-1079).

## Context

CD-0013 D4 makes strength comparison mechanical for `exists` and `absent`,
trivial for `outcome`, and delegated to a registered evaluator for `check`.
No delivered predicate is recorded:
`workflow.verdict_recorded` carries the verdict kind and the incomparable flag
(CD-0013 D3). No ground-truth surface registry or check catalog exists
(CD-0013 deferred items 2 and 3). Completion refuses on the recorded verdict of
an actor distinct from the executor (CD-0013 D10).

## Decision

### D1. Review evaluates strength

Review inspects the accumulated solution, may commit corrective changes, verifies the corrected candidate, and reports one disposition, Main records its acceptance, and Implement cannot accept its own result ([CD-0242 D5](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d5-execution-is-a-loop-that-main-controls)).

### D2. Completion refuses on the recorded verdict

Completion requires a passing receipt for every accepted criterion on the final candidate, the accepted ready disposition on that candidate, no open in-scope blocking finding, every applicable approval, and delivery evidence ([CD-0242 D9](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d9-completion-needs-proof-approvals-delivery-and-publication)).

### D3. The review pass interprets surfaces and checks

Until an accepted decision supplies the check catalog or the ground-truth
surface registry (CD-0013 deferred items 2 and 3), no registry resolves
`surface` and `check_ref` values. Approval checks their grammar, and
the review pass that verifies the candidate interprets them, and the receipts record what ran ([CD-0242 D5](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d5-execution-is-a-loop-that-main-controls)).

## Alternatives considered

- Wire a mechanical evaluator into completion. Rejected: no delivered predicate
  is recorded and no resolver exists, so wiring needs a new recorded field, a
  surface registry, and a check catalog that no accepted need asks for.
- Record a delivered predicate and compare it without resolvers. Rejected: one
  actor would author both sides of the comparison.

## Consequences

The loop carries the strength judgment:
Review inspects the accumulated solution, verifies the corrected candidate, and reports one disposition Main records, and Implement cannot accept its own result ([CD-0242 D5](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d5-execution-is-a-loop-that-main-controls)). A weaker or incomparable delivery is Review's finding
against the approved criteria; the operator's premise confirmation
(CD-0012 D9) remains.

## Verification

- The implementing change binds the anchor for the
  accepted ready disposition on the final candidate; until it lands,
  `python3 scripts/check-knowledge-index.py` is the executable anchor for
  this record's law registration.

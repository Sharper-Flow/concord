# CD-0235: The outcome strength evaluation is Review's judgment

- **Status:** Accepted
- **Date:** 2026-10-17
- **Scope:** Who evaluates outcome strength, and what completion refuses on
- **Amends:** [CD-0012](CD-0012-bind-stated-goals-to-delivered-outcomes.md) D3
  (second paragraph); [CD-0013](CD-0013-workflow-engine-mechanism.md) D4
  (strength comparison paragraph)
- **Preserves:** CD-0012 D3's strengthen-only rule, D4's omission refusal,
  and D9's premise confirmation; CD-0013 D4's closed predicate union and
  outcome-token evidence rule; Review's report and Main's acceptance of the
  disposition ([CD-0242](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md) D5)
- **Approval:** 2026-10-17. Coordinator approval under the operator's delegation
  for [Concord (CON) issue 1079](https://linear.app/sharper-flow/issue/CON-1079).
- **Amended by:** [CD-0242](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md) D5, D9

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

### D1. Review evaluates strength

[CD-0242 D5](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md)
governs: no separate final evaluator exists; Review inspects the accumulated
solution, may commit corrective changes, verifies the corrected candidate, and
reports one disposition, Main records its acceptance, and Implement cannot
accept its own result.

### D2. Completion refuses on the recorded verdict

[CD-0242 D9](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d9-completion-needs-proof-approvals-delivery-and-publication)
governs: completion requires a passing receipt for every accepted criterion on
the final candidate, the accepted ready disposition on that candidate, no open
in-scope blocking finding, every applicable approval, and delivery evidence.

### D3. The review pass interprets surfaces and checks

Until an accepted decision supplies the check catalog or the ground-truth
surface registry (CD-0013 deferred items 2 and 3), no registry resolves
`surface` and `check_ref` values. Approval checks their grammar, and
[CD-0242 D5](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md)
governs: no verdict actor exists — the review pass that verifies the candidate
interprets them, and the receipts record what ran.

## Alternatives considered

- Wire a mechanical evaluator into completion. Rejected: no delivered predicate
  is recorded and no resolver exists, so wiring needs a new recorded field, a
  surface registry, and a check catalog that no accepted need asks for.
- Record a delivered predicate and compare it without resolvers. Rejected: one
  actor would author both sides of the comparison.

## Consequences

The loop carries the strength judgment:
[CD-0242 D5](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md)
governs — Review inspects the accumulated solution, verifies the corrected
candidate, and reports one disposition Main records, and Implement cannot
accept its own result. A weaker or incomparable delivery is Review's finding
against the approved criteria; the operator's premise confirmation
(CD-0012 D9) remains.

## Verification

- [CD-0242 D5](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md)
  governs: no separate final evaluator exists, and no verdict-actor
  distinctness is required. The implementing change binds the anchor for the
  accepted ready disposition on the final candidate; until it lands,
  `python3 scripts/check-knowledge-index.py` is the executable anchor for
  this record's law registration.

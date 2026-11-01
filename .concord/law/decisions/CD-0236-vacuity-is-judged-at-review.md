---
{
  "id": "CD-0236",
  "title": "Vacuity is judged at review",
  "authority": {
    "tier": "derived"
  },
  "home_domain_id": "workflow-engine",
  "applies_to_domain_ids": [],
  "law_relations": [
    {
      "kind": "refines",
      "target_id": "CD-0012"
    },
    {
      "kind": "refines",
      "target_id": "CD-0013"
    }
  ]
}
---
# CD-0236: Vacuity is judged at review

- **Status:** Accepted
- **Date:** 2026-10-17
- **Scope:** Who decides that an approved end-state already held before execution
- **Preserves:** CD-0012 D4's Omission bullet, D3's strengthen-only rule, D7's
  authorship fencing and evaluator distinctness, and D9's premise confirmation;
  [CD-0218](CD-0218-the-live-workflow-catalog-retains-three-execution-families.md)
  D6's independent-verdict obligation
- **Approval:** 2026-10-17. Coordinator approval under the operator's delegation
  for [Concord (CON) issue 525](https://linear.app/sharper-flow/issue/CON-525).

## Context

CD-0012 D4 refuses at approval a postcondition already satisfied by the state
of the world before execution. Deciding that needs the state of the named
surface before execution. No ground-truth surface registry exists (CD-0013
deferred item 2), so approval cannot read that state. The shipped refusal read
a `ground_truth` label that the approving caller wrote beside the predicate
and refused when its suffix matched the predicate kind. The current
`approve_contract` schema accepts no such label, and the published tool schema
refuses it. Work pinned to an older definition version that declares no
payload schema could still carry it through the CLI. Where the refusal ran, it
decided on the caller's claim, not on the world.

## Decision

### D1. Approval does not decide vacuity

Approval checks each predicate's grammar and the Omission refusal. It does not
decide whether a predicate already held before execution, and it reads no
caller-supplied ground-truth label. An accepted decision that adds a
ground-truth surface registry may restore a mechanical vacuity refusal at
approval.

### D2. Review judges vacuity

A predicate that held before execution cannot bind, and it remains a defect.
The approving authority does not approve one. Independent review fails a
delivery whose approved predicate already held before execution. Premise
confirmation (CD-0012 D9) does not confirm a premise that rests on such a
predicate.

## Alternatives considered

- Resolve the named surface at approval. Rejected: no surface registry exists,
  and the approved cut forbids a general evaluator for this case.
- Keep the label check. Rejected: the approving caller authors both the
  predicate and the label and can omit the label, so the check decides nothing.

## Consequences

The label check in `internal/store/workflow_dispatch.go`, its unit test, and
the corpus scenarios `WF03-vacuous-end-state` and `WF52-vacuous-conjunct` are
deleted. Corpus numbers 3 and 52 are retired, not reused. CD-0012 scenario 3
and CD-0013 outcome-contract criterion 3 now read: a change contract whose
required end-state already held before execution fails independent review;
approval does not decide it. CD-0012's Cost bullet now reads "review judges
vacuous cases (CD-0236)" in place of "D4 refuses the obviously vacuous cases".
A vacuous predicate that the approving authority approves and review misses is
not caught mechanically.

## Verification

- `internal/store.TestApprovalDecisionIgnoresGroundTruthLabels` proves both
  witnesses. On a current `workflow.implementation` pin, the action schema
  refuses a `ground_truth` member in an outcome payload and in the action
  payload. On a pin older than the payload schema (`workflow.implementation`
  v2), `approve_contract` decides a request whose action payload carries a
  `-present` or `-absent` label beside an `exists` or `absent` predicate the
  same way as the same request without one.
- `internal/store.TestWorkflowScenarioCorpusExecutesExactProductionActions`
  runs the corpus with no vacuity scenario and with numbers 3 and 52 retired;
  `WF02-planning-requires-outcome` keeps the Omission refusal.

---
{
  "id": "CD-0124",
  "title": "Workflow verify evidence recovery",
  "authority": {
    "tier": "legislated",
    "legislated_by": "work-8d7bd788b2890355460bb5b7",
    "contract_version": 1
  },
  "home_domain_id": "workflow-engine",
  "applies_to_domain_ids": [],
  "law_relations": [
    {
      "kind": "refines",
      "target_id": "CD-0013"
    }
  ]
}
---
# CD-0124: Workflow verify evidence recovery

- **Status:** Accepted
- **Date:** 2026-09-09
- **Scope:** The break-fix verify route for an outstanding contract evidence kind
- **Approval:** The operator approved this Product law amendment for the
  workflow recovery repair.
- **Related:** CD-0013, CD-0122, CD-0143, issue #837
- **Preserves:** Contract authority, evidence provenance,
  operator approval, and lifecycle gates.

## Context

An accepted break-fix delivery can reach `verify` with a contract-required
evidence kind still outstanding. The existing completion gate correctly refuses
premise confirmation, but the prior action set provides no typed route that can
repair the missing evidence without changing the step.

Verdict correction is a separate route. It requires delivered work and a current
unhealthy verdict, returns to the declared `repair` step, and does not replace this
evidence-binding recovery.

## Decision

### D1. Verify has one typed recovery route

Break-fix declares `bind_evidence` on `verify`. The action
has hold execution mode and no approval requirement. It binds only one evidence
kind that the active contract and the current definition still require and that
the recorded evidence does not satisfy.

### D2. Confirmation remains the advancing gate

`bind_evidence` never advances `verify`. `confirm_premise` remains the only
advancing action on that step. Recovery and acceptance use the same outstanding
requirement calculation. They do not create a second requirement list.

### D3. Recovery preserves authority and refuses unsafe input

The route preserves the active contract, prior evidence, verdicts,
and operator decisions. It refuses an unrelated kind, a satisfied requirement,
an unauthorized actor, a stale version, and terminal work without a durable
effect. An exact replay is idempotent and creates no duplicate binding.

Recovery does not fabricate evidence, relabel a producer's evidence, invent an
obligation, reset history, or bypass a lifecycle gate.

### D5. Delivery evidence stays separate

Source delivery, supported deployment, and recovery of the four stranded work
items are separate evidence obligations. Source code and tests do not claim
deployment or stranded-work recovery.

## Verification

- The break-fix definition declares `bind_evidence` on `verify` with hold
  execution mode, while `confirm_premise` remains the only advancing action.
- Recovery and acceptance share the typed outstanding-requirement calculation.
- Refusal tests prove no effect for unrelated, satisfied, unauthorized, stale,
  and terminal requests.
- Replay tests prove one durable binding for one recovery identity.
- Verdict correction requires delivered work and a current unhealthy verdict, binds
  a comparable healthy predicate set, and returns break-fix work to the
  `repair` step without rewriting recorded facts.

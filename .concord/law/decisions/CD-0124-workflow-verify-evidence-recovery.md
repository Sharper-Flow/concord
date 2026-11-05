---
{
  "id": "CD-0124",
  "title": "Workflow verify evidence recovery",
  "authority": {
    "tier": "derived"
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
- **Scope:** Ordinary evidence binding in break-fix workflows
- **Authority:** Derived amendment under the ordinary Product-law route.
- **Related:** CD-0013, CD-0122, issue #837
- **Preserves:** Contract authority, evidence provenance,
  operator approval, and lifecycle gates.

## Context

Evidence may be bound without advancing the current workflow step.

## Decision

### D1. Verify admits ordinary evidence binding

Break-fix declares `bind_evidence` on `verify`. The action has hold execution
mode and no approval requirement. An authenticated actor may bind evidence for
an active contract once the workflow reaches or passes a step declaring the
action. The binding must satisfy ordinary payload, producer, subject and
evidence-kind checks and include the producer watermark. A prior binding does
not prevent a fresh binding.

### D2. Confirmation remains the advancing gate

`bind_evidence` never advances `verify`. `confirm_premise` remains the only
advancing action on that step. Binding is not a premise-confirmation gate.

### D3. Binding preserves authority and refuses unsafe input

The binding preserves the active contract, prior evidence and operator
decisions. It refuses an invalid kind, an unauthorized actor, a
stale version, and terminal work without a durable effect. An exact successful
replay is idempotent and creates no duplicate binding.

Binding does not fabricate evidence, relabel a producer's evidence, invent an
obligation, reset history, or bypass a lifecycle gate.

### D5. Delivery evidence stays separate

Source delivery, supported deployment, and recovery of the four stranded work
items are separate evidence obligations. Source code and tests do not claim
deployment or stranded-work recovery.

## Verification

- The break-fix definition declares `bind_evidence` on `verify` with hold
  execution mode, while `confirm_premise` remains the only advancing action.
- Refusal tests prove no effect for invalid, unauthorized, stale, and terminal requests.
- Replay tests prove one durable binding for one successful request identity.

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

An accepted break-fix delivery can reach `verify` with a contract-required
evidence kind still outstanding. Binding evidence holds the step; premise
confirmation retains its contract evidence requirements.

## Decision

### D1. Verify admits ordinary evidence binding

Break-fix declares `bind_evidence` on `verify`. The action has hold execution
mode and no approval requirement. An authenticated actor may bind evidence for
an active contract once the workflow reaches or passes a step declaring the
action. The binding must satisfy ordinary payload, producer, subject,
evidence-kind and obligation checks. A previously satisfied requirement does not
prevent a fresh binding.

### D2. Confirmation remains the advancing gate

`bind_evidence` never advances `verify`. `confirm_premise` remains the only
advancing action on that step. Confirmation and completion retain the
outstanding-requirement calculation; it does not restrict ordinary binding.

### D3. Binding preserves authority and refuses unsafe input

The binding preserves the active contract, prior evidence, verdicts, and operator
decisions. It refuses an unrelated kind or obligation, an unauthorized actor, a
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
- Recovery and acceptance share the typed outstanding-requirement calculation.
- Confirmation and completion retain the typed outstanding-requirement calculation.
- Refusal tests prove no effect for unrelated, unauthorized, stale, and terminal requests.
- Replay tests prove one durable binding for one successful request identity.

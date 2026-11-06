---
{
  "id": "CD-0124",
  "title": "Workflow evidence binding",
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
# CD-0124: Workflow evidence binding

- **Status:** Accepted
- **Date:** 2026-09-09
- **Scope:** Ordinary evidence binding on steps that declare it
- **Authority:** Derived amendment under the ordinary Product-law route.
- **Related:** CD-0013, CD-0122, issue #837
- **Preserves:** Contract authority, evidence provenance,
  operator approval, and lifecycle gates.

## Context

Evidence may be bound without advancing the current workflow step.

## Decision

### D1. Declaring steps admit ordinary evidence binding

The workflow step templates declare which required steps carry `bind_evidence`.
The action has hold execution
mode and no approval requirement. An authenticated actor may bind evidence once
the workflow reaches or passes a step declaring the action. While the plan is
incomplete, a missing active contract does not block the binding; an ambiguous
active-contract projection still refuses. After the plan completes, binding
requires exactly one active contract. The binding must satisfy ordinary
payload, producer, subject and evidence-kind checks and include the producer
watermark. A prior binding does not prevent a fresh binding.

### D2. Confirmation remains a separate gate

`bind_evidence` never advances a step. `confirm_premise` remains the
premise-confirmation action. Binding is not a premise-confirmation gate.

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

- The step templates declare which required steps carry `bind_evidence`; its
  action
  policy gives it hold execution mode, and `confirm_premise` remains the
  premise-confirmation action.
- Each refusal leg has an executable witness: invalid and stale requests —
  `internal/store.TestLateBindingRefusesUndeclaredEvidenceKind` and
  `internal/store.TestExternalCheckRecoveryPreservesEvidenceRefusals` refuse
  dispatch with work version and effect receipts unchanged; unauthorized
  actors — `internal/store.TestWorkflowPlanAdmissionRefusalParity` refuses
  the missing actor tuple and a principal mismatch with work version,
  evidence bindings and effect receipts unchanged; terminal work —
  `internal/store.TestTerminalWorkflowRefusesEvidenceBindingWithoutEffects`
  refuses with work version, plan, effect receipts and durable operations
  unchanged.
- Replay tests prove one durable binding for one successful request identity.

---
{
  "id": "CD-0129",
  "title": "Premise confirmation and architecture at contract approval",
  "authority": {
    "tier": "derived"
  },
  "home_domain_id": "workflow-engine",
  "applies_to_domain_ids": [],
  "law_relations": [
    {
      "kind": "refines",
      "target_id": "CD-0113"
    }
  ]
}
---
# CD-0129: Premise confirmation and architecture at contract approval

- **Status:** Accepted
- **Date:** 2026-09-09
- **Scope:** Coordinator premise confirmation and architecture at contract approval
- **Related:** CD-0113, and issues #903, #1010
- **Preserves:** Contract authority, architecture binding at approval,
  evidence provenance, and the recorded facts the store keeps
  ([CD-0242](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md) D2)

## Context

A coordinator can spend an operator decision on a question its own
investigation should settle. An operator question must not spend a decision
on an investigation the agent can settle.

## Decision

### D1. Premise confirmation requires acceptance deliverables

`confirm_premise` requires the acceptance
deliverables. It records the authenticated coordinator, with no semantic choice
or human decision-context digest. Operator decisions follow CD-0243 in
conversation; the step machine projects no operator question or withholding.

### D4. Architecture stays where it already was

The architecture obligation stays at approval for Product-changing work,
where the complete architecture binding is already refused when absent.
This record adds no approval gate for architecture.

## Verification

```text
go test ./internal/store/ -run 'TestEngineeringConfirmation'
go test ./internal/store/ ./internal/agent/...
cd adapter/opencode && bun test dispatch_route_end_to_end
```

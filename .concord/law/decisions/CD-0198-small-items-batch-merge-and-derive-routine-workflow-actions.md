---
{
  "id": "CD-0198",
  "title": "Small items batch, merge, and derive routine workflow actions",
  "authority": {
    "tier": "derived"
  },
  "home_domain_id": "workflow-engine",
  "applies_to_domain_ids": [],
  "law_relations": [
    {
      "kind": "refines",
      "target_id": "CD-0183"
    }
  ]
}
---
# CD-0198: Small items batch, merge, and derive routine workflow actions

- **Status:** Accepted
- **Date:** 2026-09-30
- **Scope:** Batched verdicts, typed diagnosis and derived producing-step starts
- **Related:** CD-0012, CD-0173, CD-0183, CD-0242, CD-0243

## Context

Separate calls for each verdict, diagnosis section and step start add
coordination cost without adding an independent judgment.

## Decision

Contract authority, evidence and independent Review remain mandatory. Consent
follows CD-0243's six classes in conversation. Premise confirmation uses
authenticated coordinator identity. Batching never silently
omits an approved predicate or supplies fabricated proof.

### D1. One record_verdict call carries each predicate it judges

The batched form carries one or more verdict objects, each naming an approved
predicate of the active contract and its verdict kind, evaluation evidence and
incomparable flag. Duplicate predicate IDs refuse. Each verdict retains its
actor and records atomically. An omitted predicate stays missing and blocks
completion. Independent Review and bound verification reports remain required;
Concord attests no subagent identity.

### D2. Break-fix records one typed diagnosis

`record_diagnosis` records its reproduction, alignment-search and
root-cause sections, each retaining its fields and guards. The search
includes `searched`, `outcome` and `related_ids`. It writes the same
alignment declaration and creates no relation. An outcome of found-nothing
is distinct from not searched. The record holds the diagnose step; the
step completes only through `complete_step` with the typed diagnosis
recorded, and the plan's alignment read replaces the retired
implementation alignment step. Analysis cannot complete without the search
beside it.

### D3. The fenced implement start derives the step-start fact

The fenced `start_execution` at the item's implement step derives the
step-start fact in its own transaction. The first start wins, opens the
epoch and binds the fence from its actor. Delivery requires the accepted Review and, when the active contract
requires verification evidence, a passing verification report covering the
current reviewed candidate; no step start, lease acquisition, live Git
freshness or exact tooling argv comparison is required. Current rows own these facts;
per-item plans copy one canonical template contract.

Whole-work delivery uses `record_delivery` and its complete admission. A Review
outcome is recorded separately through `record_review`; it does not combine
subagent acceptance, delivery or retry dispatch.

## Alternatives considered

- Replace typed actions with a progress paragraph. Rejected: prose cannot
  preserve per-action validation.
- Remove contract or verdict gates to save calls. Rejected: those judgments
  remain independent requirements.

## Consequences

Batching changes transport and derivation, not evidence or approval owners.
No worker-failure record, combined worker acceptance or retry launcher exists.

## Verification

- `go test ./internal/store ./internal/agent` checks batching, duplicate and
  omitted predicates, diagnosis completeness, fenced `start_execution` starts and delivery
  refusal for missing, failed, unresolved or mixed-candidate verification.
- Committed law-folder snapshot validation checks the record's identity and
  content digest.

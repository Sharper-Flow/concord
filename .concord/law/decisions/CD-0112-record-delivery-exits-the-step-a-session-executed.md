---
{
  "id": "CD-0112",
  "title": "record_delivery records delivery at its declared step",
  "authority": {
    "tier": "derived"
  },
  "home_domain_id": "workflow-engine",
  "applies_to_domain_ids": []
}
---
# CD-0112: record_delivery records delivery at its declared step

- **Status:** Accepted
- **Date:** 2026-09-05
- **Scope:** Producing-step delivery and context-continuity actions
- **Related:** CD-0013, CD-0016, CD-0027, CD-0242

## Context

A context handoff must preserve findings without claiming that production
finished. Delivery needs its own explicit result.

## Decision

### D1. A hand-off is not progress

`checkpoint_context` and `cross_context_boundary` are hold actions. They record
continuity and never advance a stage.

### D2. record_delivery records at its declared step without moving it

A plan step that declares `record_delivery` carries it with an internal
SQLite consequence and no independent approval. Applicable approvals remain
required. Native
Task execution neither supplies delivery nor prevents this route. Steps
complete only through `complete_step`; delivery requires no step start.

Replacing an existing delivery record follows
[workflow-engine contract §4.2](../specs/workflow-engine-contract.md#42-ordinary-retry-and-record-correction).
The replacement holds the current step and lifecycle, including completed work;
it reruns no completion and asserts no new execution.

### D3. Retained decision records stay readable

`workflow_decision_records` rows written before the step-plan cutover remain
readable as recorded decisions. No current action writes new rows: in-flight
decisions travel in the six-part plan record, and steps complete only
through `complete_step`.

## Alternatives considered

- Advance on a handoff summary. Rejected: continuity does not establish delivery.
- Infer delivery from a subagent result. Rejected: the coordinator owns the
  explicit subject-bound delivery claim.

## Consequences

The workflow has one producing-step delivery route and no lane-result exit or
worker-failure recording lifecycle. Required proof remains independent of the
host's subagent execution.

## Verification

- `go test ./internal/store ./internal/agent` checks hold-mode continuity
  and delivery admission.
- Committed law-folder snapshot validation checks the record's identity and
  content digest.

---
{
  "id": "CD-0112",
  "title": "record_delivery exits the step a session executed",
  "authority": {
    "tier": "derived"
  },
  "home_domain_id": "workflow-engine",
  "applies_to_domain_ids": []
}
---
# CD-0112: record_delivery exits the step a session executed

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

### D2. record_delivery exits a producing step

A plan step that declares `record_delivery` carries it with an internal
SQLite consequence and no independent approval. Delivery applies the
complete delivery admission, including Review, a bound passing verification
report and applicable approvals. Native Task execution neither supplies
delivery nor prevents this route. Steps complete only through
`complete_step`; delivery no longer requires a step's fenced start.

Replacing an existing delivery record follows
[workflow-engine contract §4.2](../specs/workflow-engine-contract.md#42-ordinary-retry-and-record-correction).
The replacement holds the current step and lifecycle, including completed work;
it does not repeat the producing step's advance or assert new execution.

### D3. Decision-record exits retain their declared meaning

A retained architecture-spike definition remains excluded from new execution
under CD-0218. Its `record_decision` records its typed checkpoint and advances
to review. Its declared acceptance and premise-confirmation exits remain
distinct. A typed checkpoint may accompany an advance.

## Alternatives considered

- Advance on a handoff summary. Rejected: continuity does not establish delivery.
- Infer delivery from a subagent result. Rejected: the coordinator owns the
  explicit subject-bound delivery claim.

## Consequences

The workflow has one producing-step delivery route and no lane-result exit or
worker-failure recording lifecycle. Required proof remains independent of the
host's subagent execution.

## Verification

- `go test ./internal/store` checks hold-mode continuity and delivery admission,
  including refusal without a current start and required evidence.
- Committed law-folder snapshot validation checks the record's identity and
  content digest.

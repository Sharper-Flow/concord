---
{
  "id": "CD-0229",
  "title": "Plain workflow definitions without work-item size classification",
  "authority": {
    "tier": "derived"
  },
  "home_domain_id": "workflow-engine",
  "applies_to_domain_ids": [],
  "law_relations": [
    {
      "kind": "refines",
      "target_id": "CD-0006"
    },
    {
      "kind": "refines",
      "target_id": "CD-0013"
    },
    {
      "kind": "refines",
      "target_id": "CD-0138"
    },
    {
      "kind": "refines",
      "target_id": "CD-0213"
    },
    {
      "kind": "refines",
      "target_id": "CD-0218"
    }
  ]
}
---
# CD-0229: Plain workflow definitions without work-item size classification

- **Status:** Accepted
- **Date:** 2026-10-17
- **Scope:** Plain workflow definitions and the absence of work-item size classification
- **Preserves:** CD-0218 D1's three live families; CD-0242 D4-D8's mandatory
  stages, independent review, current-subject proof and evidence requirements;
  CD-0242 D13's verified-snapshot upgrade; CD-0242 D14's single canonical source

## Decision

### D1. Workflows use plain definitions

The implementation, break-fix and research families each have a plain Go
`WorkflowDefinition` value. Each definition directly declares its steps,
actions, edges, evidence, outcomes and recovery routes. The registry validates
the value and derives its canonical manifest and digest. Common runtime action
and payload implementations remain shared under CD-0242 D3.

There is no workflow parts catalog, part-use mapping or composition compiler.
No replacement assembly framework or workflow language is introduced. Public
schemas and tool contracts derive from the Go definitions under
[CD-0242 D14](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d14-generated-contracts-derive-from-one-source).
This changes definition authoring, not live execution semantics. Composition
across work items remains forward-link only under CD-0013 D7-D8.

Requirements for unique step ownership by part, compiling compositions and
assembling a new workflow kind without step code are retired. Tests solely of
the parts catalog, duplicate part ownership, part-use validation or composition
compilation retire with those mechanisms. Registered-definition validation,
canonical identity, generator parity and runtime route tests remain.

### D4. Work has no size classification

Concord assigns no work-item size and retains no size model, model inputs,
assessments, proofs, overrides or size-specific timing obligations. Capture,
revision, reads and published contracts expose no size fields. No workflow
selection or review exit depends on a size class.

Every live family keeps its mandatory steps and transitions. Independent
review and current-subject proof remain required. An open in-scope blocking
finding still blocks execution exit under CD-0242 D5; no size-specific severity
gate adds another condition.

The store upgrade drops size facts only after the existing verified-snapshot
procedure under CD-0242 D13. Retired size events refuse append rather than
recreating their projections. Tests whose only obligation is the size model,
its storage or proofs, its public inputs or the size-specific review gate are
retired with those behaviors. Tests of retained review, route and snapshot
requirements remain.

## Verification

Plain definitions require complete canonical-manifest and digest equivalence
when removing assembly, plus registration and generated-contract parity checks.
Size retirement requires checks for absent public fields, refusal of retired
events, preserved review blockers and snapshot-backed migration of existing
stores. Presence of a migration or a declaration alone does not prove those
outcomes.

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
- **Preserves:** CD-0218 D1's workflow types; CD-0242 D4-D8's mandatory
  stages, independent review, current-subject proof and evidence requirements;
  CD-0242 D13's verified-snapshot upgrade; CD-0242 D14's single canonical source

## Decision

### D1. Workflows use one template contract and per-item plans

Each workflow type's required steps live once in the machine-readable template
contract `contracts/workflow-step-templates.v1.json`; when the type is picked,
its steps are copied into the item's own stored plan (CON-1574). There is no
definition registry, digest, or per-kind fixed chain. Common runtime action
and payload implementations remain shared under CD-0242 D3.

There is no workflow parts catalog, part-use mapping or composition compiler.
No replacement assembly framework or workflow language is introduced. Public
schemas and tool contracts derive from the template contract under
[CD-0242 D14](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d14-generated-contracts-derive-from-one-source).
This changes definition authoring, not live execution semantics. Composition
across work items remains forward-link only under CD-0013 D7-D8.

Retry and correction use ordinary actions under
[workflow-engine contract §4.2](../specs/workflow-engine-contract.md#42-ordinary-retry-and-record-correction),
without a recovery-route registry. Definition validation does not require
correction triggers, targets or route readers.

Requirements for unique step ownership by part, compiling compositions and
assembling a new workflow kind without step code are retired. Tests solely of
the parts catalog, duplicate part ownership, part-use validation or composition
compilation retire with those mechanisms. Template-contract validation,
canonical identity, generator parity and runtime route tests remain.

### D4. Work has no size classification

Concord assigns no work-item size and retains no size model, model inputs,
assessments, proofs, overrides or size-specific timing obligations. Capture,
revision, reads and published contracts expose no size fields. No workflow
selection or review exit depends on a size class.

Every workflow type keeps its mandatory steps and transitions. Independent
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

Plain definitions require complete canonical-manifest and digest equivalence,
plus template-contract and generated-contract parity checks.
Size retirement requires refusal of retired events, preserved review blockers
and snapshot-backed migration of existing stores, and public contracts that
carry no size fields. Runtime result-schema validation rejects undeclared fields
in closed result contracts. Presence of a migration or a declaration alone does
not prove those outcomes.

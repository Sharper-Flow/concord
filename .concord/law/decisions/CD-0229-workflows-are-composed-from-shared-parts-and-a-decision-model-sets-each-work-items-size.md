# CD-0229: Workflows compose shared parts without work-item size classification

- **Status:** Accepted
- **Date:** 2026-10-17
- **Scope:** Shared workflow definitions and the absence of work-item size classification
- **Preserves:** CD-0218 D1's three live families; CD-0242 D4-D8's mandatory
  stages, independent review, current-subject proof and evidence requirements;
  CD-0242 D13's verified-snapshot upgrade; CD-0242 D14's single canonical source

## Decision

### D1. A part defines its steps once

A part is a code-defined, versioned unit that defines one or more workflow
steps: their actions, payload contracts, required evidence and recovery routes.
Each step is defined in exactly one part. A part may declare parameters.

A family or Domain that needs a variant gets it through a parameter of the
shared part, never through a copy. When the part cannot serve the need without
a special case, file a follow-up item to redesign it.

### D2. A workflow definition is a composition of parts

A definition declares its family, its parts at pinned versions with their
parameters, and the edges between them. Parts and compositions are Go values,
not a workflow language. The registry compiles each composition to its canonical
definition and computes the digest from that result.

New execution uses only CD-0218 D1's implementation, break-fix and research
families. Composition inside one definition is assembly, not nested execution.
Composition across work items remains forward-link only.

### D3. Compositions derive from one source

Derived schemas and compositions are generated from one canonical source
([CD-0242 D14](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d14-generated-contracts-derive-from-one-source)).

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

Shared-part ownership and canonical compilation remain subject to D1-D3.
Size retirement requires checks for absent public fields, refusal of retired
events, preserved review blockers and snapshot-backed migration of existing
stores. Presence of a migration or a declaration alone does not prove those
outcomes.

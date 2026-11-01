---
{
  "id": "CD-0077",
  "title": "Storage vocabularies have one machine-readable authority",
  "authority": {
    "tier": "derived"
  },
  "home_domain_id": "workflow-engine",
  "applies_to_domain_ids": []
}
---
# CD-0077: Storage vocabularies have one machine-readable authority

- **Status:** Accepted
- **Date:** 2026-08-25
- **Scope:** Work-item operator kinds, workflow-family composition, native-run
  phase/status pairs, their SQLite projections, and their public contract
  projections; issues #502, #508, and #512
- **Approval:** The operator selected contract-generated registry enforcement for
  #502, selected workflow-definition family resolution for #508, and directed
  #512 into the same change on 2026-08-25.
- **Related:** CD-0002, CD-0013 D14, CD-0039, CD-0040 D11, CD-0055
- **Preserves:** The live workflow-family set; the `successor_kind` work-item
  meaning on a recorded successor link; migration immutability;
  SQLite as the sole local state authority

## Context

`work_items.kind` and workflow-family names answer different questions. A work-item
kind states what an operator says the item is. A workflow family states which
workflow the item runs. The operator set contains `task`, `bug`, `decision`,
`research`, and `other`, with `initiative` reserved for its dedicated operation.
The workflow-family set contains seven names fixed by CD-0013 D14. Only `research`
appears in both sets, and that spelling does not make the concepts identical.

The code hid this distinction. The database column is named `kind`, the agent
payload calls it `work_kind`, and the Go type `WorkKind` names the workflow family.
Two composition checks cast the database value to `WorkKind`. That cast compiled,
but it compared two separate vocabularies and refused all ordinary combinations
except `research`.

The operator vocabulary was governed at the agent boundary, but the SQLite column
was `TEXT NOT NULL`. Store replay, reconstruction, tests, and future import paths
could therefore write values the agent schema refused. Existing scenarios did so:
they placed workflow-family names into `work.created.work_kind` because the broken
composition check required that shape.

Native-run status had the same ownership problem. CD-0039 defines four phases and
ten statuses. A Go map, two runtime checks, and an agent JSON Schema repeated the
set. `workflow_native_runs.phase` carried a `CHECK`, but `status` was unbounded
text. No mechanism proved that the copies matched or that a status belonged to
the supplied phase.

## Decision

### D1. Work-item kind and workflow family remain separate vocabularies

The work-item operator vocabulary is:

- `task`, `bug`, `decision`, `research`, and `other`: stored, revisable, and
  accepted by agent capture;
- `initiative`: stored only through the dedicated Initiative operation and never
  accepted by ordinary capture or intent revision;
- `epic`: retired and never stored.

Workflow-family names remain the seven values fixed by CD-0013 D14. They do not
enter `work_items.kind`.

### D2. Each vocabulary has one machine-readable declaration

`contracts/work-kinds.v1.json` owns stored, create, revise, and agent
capture policy for every work-item term. `contracts/native-run-statuses.v1.json`
owns each native-run phase/status pair and its failure classification.

Deterministic generators derive the Go runtime projections and content digests.
The native-run closure validator compares its declaration with generated Go,
the literal migration rows, the registry triggers, and the runtime guard call.
A changed native-run contract without a matching migration decision fails
validation.
Work-kind enforcement uses the generated projections and SQLite registry;
it requires no cross-surface closure audit.

### D3. SQLite enforces both declarations through registries and triggers

Migration 49 creates `work_kinds` and `workflow_native_run_statuses` and seeds
literal rows. The native-run closure validator compares its rows with its
contract. The migration makes both registries immutable with insert, update, and delete
refusal triggers. Consumer insert and update triggers refuse an unstored
work-item kind or an undeclared native-run phase/status pair.

The migration keeps literal SQL. Generated Go does not construct migration text.
This preserves immutable migration checksums and makes an old database upgrade
independent of the current generator.

The approved registry-backed foreign-key direction for work kinds is implemented
with a registry trigger rather than a table rebuild. Thirty-seven tables reference
`work_items`; rebuilding that central table adds referential migration risk but no
stronger membership invariant. The trigger performs the same registry lookup at
the database boundary without replacing the referenced table.

### D4. Composition resolves the successor's definition identity

A forward-link composition check reads the successor's
`workflow_instances.definition_ref`, resolves that builtin definition
identity, and
compares its workflow family with the source definition's allowed families. A
successor without a workflow instance has no family and is refused.

Recorded successor links read back with their recorded meaning, and no payload version, upcast, or replay reinterprets them ([CD-0242 D2](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)). The
`successor_kind` value keeps its work-item meaning, checked against
`work_items.kind` at admission.

### D5. Scenarios state each concept at its owning record

`work.created.work_kind` carries an operator kind. Existing family-shaped fixture
values become `task` unless the scenario explicitly states `bug` or `research`.
`workflow.definition_selected.work_kind` continues to carry the workflow family.
The scenario schema uses separate definitions for these payloads and rejects a
mixed value.

### D6. Product Row uses the declared defect term

The Product Row projection treats active `bug` items as active problems.
Database membership checks reject undeclared work kinds.

## Consequences

- A reader can start at either contract and trace every enforced projection.
- SQLite rejects bypassing writers, not only requests that entered through the
  agent schema.
- A bare Go string cannot silently join operator kinds to workflow families.
- Native-run failure classification cannot drift from phase/status membership.
- Reconstruction scratch work uses `task`; it no longer relies on a private kind.
- Contract changes that alter stored rows require a new immutable migration.

## Rejected alternatives

**Keep boundary-only validation.** Rejected because snapshot restore, tests,
and future import paths do not all enter through the agent schema. The database
must protect its own vocabulary.

**Use workflow families as work-item kinds.** Rejected because an item's identity
and its execution method are independent. One operator kind can run different
workflow families, and one family can serve different operator kinds.

**Rebuild `work_items` to add a foreign key.** Rejected for this migration because
37 tables reference it. A registry trigger enforces the same membership rule
without replacing the central referenced table.

**Use a `CHECK` with literal values.** Rejected because it would add a third
vocabulary copy, hide policy columns, and require another table rebuild for each
new stored kind.

**Generate migration SQL from the current contract.** Rejected because old
migrations must remain byte-stable. Literal seed rows preserve immutable
checksums; the native-run closure validator also binds its rows to its contract.

## Verification

- Store tests prove every registry row and policy bit, accepted rows, rejected
  unknown values, wrong-phase status refusal, and valid and invalid upgrades from
  schema version 48.
- Store composition tests prove the recorded successor meaning, the derived
  definition identity, and refusal of a successor without a workflow instance
  (CD-0242 D2, D14).
- An agent-surface test captures operator-kind `task` items, selects different
  workflow families, forward-links the allowed family, and proves the operation
  refuses an unpinned successor.
- Vocabulary tests cover every declared native-run pair and both work-kind policy
  boundaries.
- The native-run Python suite mutates migration rows, trigger predicates,
  registry immutability triggers, runtime guard calls, and generated output to
  prove its closure validator fails.
- The workflow scenario corpus passes with operator kinds at creation and
  workflow families at definition selection.

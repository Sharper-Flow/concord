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
- **Preserves:** The workflow-family set fixed by CD-0013 D14; the meaning of the
  `workflow.successor_linked.successor_kind` event field; migration immutability;
  SQLite as the sole local state authority
- **Supersedes:** The implicit treatment of `work_items.kind` as a workflow family
  in composition checks; duplicated native-run status declarations

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

`contracts/work-kinds.v1.json` owns stored, fold-create, fold-revise, and agent
capture policy for every work-item term. `contracts/native-run-statuses.v1.json`
owns each native-run phase/status pair and its failure classification.

Deterministic generators derive the Go runtime projections and content digests.
Closure validators compare the declarations with agent schemas, scenario schemas,
generated Go, and the literal migration rows. A changed contract without a
matching migration decision fails validation.

### D3. SQLite enforces both declarations through registries and triggers

Migration 49 creates `work_kinds` and `workflow_native_run_statuses`. It seeds
literal rows that the closure validators compare with the contracts. The
migration then makes both registries immutable with insert, update, and delete
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

### D4. Composition resolves the pinned workflow definition

A forward-link composition check reads the successor's
`workflow_instances.definition_ref`, resolves the pinned builtin definition, and
compares its workflow family with the source definition's allowed families. A
successor without a workflow instance has no family and is refused.

`workflow.successor_linked` payload version 2 requires `definition_ref`. Its v1
upcast preserves replay for an earlier valid `research` successor that had no
workflow instance. Only an event read from stored v1 data can use that narrow
compatibility rule. A newly appended event cannot select it.

The event field `successor_kind` retains its work-item meaning. Fold replay still
checks that value against `work_items.kind`; changing the event meaning would make
old records ambiguous.

### D5. Scenarios state each concept at its owning event

`work.created.work_kind` carries an operator kind. Existing family-shaped fixture
values become `task` unless the scenario explicitly states `bug` or `research`.
`workflow.definition_selected.work_kind` continues to carry the workflow family.
The scenario schema uses separate definitions for these payloads and rejects a
mixed value.

### D6. Product Row uses the declared defect term

The Product Row projection treats active `bug` items as active problems. The
undeclared literal `problem` is removed. The database trigger prevents that value
from entering the store.

## Consequences

- A reader can start at either contract and trace every enforced projection.
- SQLite rejects bypassing writers, not only requests that entered through the
  agent schema.
- A bare Go string cannot silently join operator kinds to workflow families.
- Native-run failure classification cannot drift from phase/status membership.
- Reconstruction scratch work uses `task`; it no longer relies on a private kind.
- Contract changes that alter stored rows require a new immutable migration.

## Rejected alternatives

**Keep boundary-only validation.** Rejected because replay, reconstruction, tests,
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
migrations must remain byte-stable. Literal seed rows plus closure validation keep
history immutable and current declarations accountable.

## Verification

- Store tests prove every registry row and policy bit, accepted rows, rejected
  unknown values, wrong-phase status refusal, and valid and invalid upgrades from
  schema version 48.
- Store composition tests prove v1 replay, the v2 definition pin, and refusal of
  a successor without a workflow instance.
- An agent-surface test captures operator-kind `task` items, selects different
  workflow families, forward-links the allowed family, and proves the operation
  refuses an unpinned successor.
- Vocabulary tests cover every declared native-run pair and both work-kind policy
  boundaries.
- Paired Python suites mutate contracts, schemas, migration rows, trigger
  predicates, and runtime call sites to prove each closure validator fails.
- The workflow scenario corpus passes with operator kinds on creation events and
  families on definition-selection events.

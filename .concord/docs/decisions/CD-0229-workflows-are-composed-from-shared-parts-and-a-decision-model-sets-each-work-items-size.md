# CD-0229: Workflows are composed from shared parts, and a decision model sets each work item's size

- **Status:** Accepted
- **Date:** 2026-10-17
- **Scope:** How a workflow definition is built from part definitions; the size
  a work item carries; the decision model that sets it; how the size selects a
  workflow; and the timing record a sized workflow keeps
- **Implements:** [`goals.md`](../goals.md) goal B (right-sized delivery and
  its decision model), goal C (composable workflows), goal F for the sizing
  model, and Appendix A2 rows 5 and 8
- **Preserves:** the derived definitions generated from the one canonical
  source
  ([CD-0242](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md) D14);
  CD-0006 R1 and CD-0013 D8,
  forward-link composition across work items; CD-0218 D1, the three live
  execution families and their predicate forms;
  [CD-0077](CD-0077-storage-vocabularies-have-one-authority.md) D1, the
  separate work-kind and family vocabularies; CD-0006 D4 and R2 and
  [CD-0091](CD-0091-maturity-promotion-ladder.md), the obligation bands and
  the promotion ladder; [CD-0012](CD-0012-bind-stated-goals-to-delivered-outcomes.md)
  D10, an outcome contract at every size;
  CD-0138 D1, mandatory refinement for break-fix and for `larger` and
  `crucial` implementation;
  [CD-0201](CD-0201-workflow-action-admission-is-derived-once-and-every-reachable-state-stays-live.md),
  the single admission derivation.
- **Approval:** The Snowball coordinator approved this record on 2026-10-17
  under the operator's delegation. The record is serious under P12 because it
  affects more than one Domain.

[CD-0243 D6](CD-0243-four-roles-and-six-operator-approval-classes.md#d6-existing-approval-requirements-follow-these-classes)
limits operator consent to its six classes. The operator owns size overrides; agents may request the model to run again.

## Context

Goal C asks for workflows built from shared parts: "we snap them together in
different combinations for different workflows, not define each separately."
Its measure is that a new workflow size or kind is made by combining existing
parts, with no new step code, and that each step is defined in exactly one
part.

Goal B asks for workflows of "dynamically different sizes", chosen by "a
defined decision model", not by the operator case by case and not by the
agent's own judgment. Its inputs are subject, sensitivity, blast radius,
security, relative size, and span. For a task that proves bigger than its size, the model runs again, the
newly required steps are added, and work continues without redoing completed
work (Appendix A2 row 8 ([CD-0242 D4](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d4-families-compose-shared-stages-and-rules-set-the-minimum))).

The builtin registry holds 146 definition versions across seven families. Each
version is a fully expanded manifest, so a step two families share is written
into each. CD-0006 D3 asks for a purpose-built type for each kind of work, so a
new size would be a new full definition. No work item carries a size, and the
registry gives every implementation item the same steps whatever the change.

## Decision

### D1. A part defines its steps once

A part is a code-defined, versioned unit that defines one or more workflow
steps: their actions, payload contracts, required evidence, and recovery
routes. Each step is defined in exactly one part. A part may declare
parameters.

A family or Domain that needs a variant of a part gets it through a parameter
of the shared part, never through a copy (Appendix A P6). When the part cannot
serve the need without a special case, the change files a follow-up item to
redesign the part.

### D2. A workflow definition is a composition of parts

A workflow definition is a declared composition: its family, its size, the
parts it uses at pinned part versions with their parameters, and the edges
between them. Parts and compositions are Go values in the registry, not a
workflow language. A new size, family, or kind is a new composition and adds
no step code.

The registry compiles each composition to its canonical definition and computes the digest from the compiled definition. The
source of a definition is its composition, and the canonical definition is its
compiled result.

Workflow types are compositions of shared parts, not
separately written definitions. New execution composes only the families
CD-0218 D1 retains.

Composition inside one definition is assembly, not nested execution.
Composition across work items stays forward-link only.

### D3. Compositions derive from one source

Derived schemas and compositions are generated from one canonical source ([CD-0242 D14](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d14-generated-contracts-derive-from-one-source)).

### D4. Every sized work item has one size from the decision model

A versioned decision model sets each
work item's size from goal B's inputs, never from an agent's proposal; the
operator may move it; agents may only request a rerun or add steps ([goal B](../goals.md) and [CD-0242 D4](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d4-families-compose-shared-stages-and-rules-set-the-minimum)).

### D5. The size selects the composition and its least proof

Change type and risk set each item's
mandatory stages, and a small change runs define, implement, review, and ship
with its footprint checks and one review ([CD-0242 D4](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d4-families-compose-shared-stages-and-rules-set-the-minimum) and [goal B](../goals.md)).

### D6. Only the model and the operator change a size

For a task that proves bigger than its size, the decision model runs again,
adds the newly required steps, and work continues without redoing completed
work or creating a successor item ([CD-0242 D4](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d4-families-compose-shared-stages-and-rules-set-the-minimum)).

### D7. A sized workflow records its own timing

A sized workflow records when the define step starts, as the agent's first
action on the defined item, and when the change enters the merge queue. Goal
B's measure for a small change, one hour or less, reads these two records.
The targets for `larger` and `crucial` stay open under goals.md §5. No time
target refuses admission until the operator sets one as a gate.

## Alternatives considered

- Let the agent choose the size. Goal B rules out the agent's own judgment.
- Let the operator choose each size. Goal B rules out a case-by-case choice;
  the operator keeps the override in D4.
- Keep full definitions and add size-specific copies. Each copy would
  redefine shared steps, against goal C's measure.
- Compose by inheritance between definitions. A child that overrides a parent
  step defines that step twice.
- Recompile released versions from current parts. Derived definitions generate from one canonical source; released versions are not retained for history ([CD-0242 D14](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d14-generated-contracts-derive-from-one-source)).
- Re-pin a running instance to the new size's composition. A task that proves bigger than its size reruns the model, adds the newly required steps, and continues in the same work item ([CD-0242 D4](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d4-families-compose-shared-stages-and-rules-set-the-minimum)).

## Consequences

The registry gains a part layer and a compiler, and the work record gains a
size and its model inputs. That needs a store migration; the cutover is one documented migration with a verified snapshot ([CD-0242 D13](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d13-the-cutover-is-one-documented-migration-with-a-verified-snapshot)). The size vocabulary needs one machine-readable declaration under
CD-0077 D2.

A part change now regenerates several compositions in one
release, and reviewers see each affected composition
([CD-0242 D14](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d14-generated-contracts-derive-from-one-source)).

A resize costs no successor; the decision model runs again, the newly required steps are added, and work continues without redoing completed work ([CD-0242 D4](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d4-families-compose-shared-stages-and-rules-set-the-minimum)). The define-start record
stays the start of the change for the D7 measure.

The decision model's first version is a calibration. Its inputs are recorded
with each size, so a later model version can be judged against the sizes it
would have given.

Concord's own development stays outside the workflow until the switch point
goals.md §2 names. This record does not move that point.

## Verification

- `python3 scripts/check-doc-contract.py` and
  `python3 scripts/check-knowledge-closure.py` pass when the record is
  registered.
- `go test ./internal/store` must prove that no step is defined in two parts,
  that a new composition compiles without new step code, and that generated
  definitions derive from the one canonical source (CD-0242 D14).
- `go test ./internal/store` must prove that the decision model gives the same
  size for the same inputs and model version, that the record keeps the size,
  model version, and inputs, and that a size an agent proposes is refused.
- The operator-override, rerun, and resize probes bind the CD-0242 D6 anchor:
  no production-line cap, size measurement, or mandatory split exists, so no
  resize successor supersedes an item for outgrowing a size. Until that change
  lands, `python3 scripts/check-knowledge-index.py` is the executable anchor
  for this record's law registration.
- `go test ./internal/store` must prove that a `small` implementation item
  cannot ship without its outcome contract, its footprint checks, and one
  independent review, and that the define-start and merge-queue records exist.

# CD-0229: Workflows are composed from shared parts, and a decision model sets each work item's size

- **Status:** Accepted
- **Date:** 2026-10-17
- **Scope:** How a workflow definition is built from part definitions; the size
  a work item carries; the decision model that sets it; how the size selects a
  workflow; and the timing record a sized workflow keeps
- **Implements:** [`goals.md`](../goals.md) goal B (right-sized delivery and
  its decision model), goal C (composable workflows), goal F for the sizing
  model, and Appendix A2 rows 5 and 8
- **Amends:** [CD-0006](CD-0006-concord-root-product-policy.md) D3;
  [CD-0013](CD-0013-workflow-engine-mechanism.md) D1;
  [CD-0138](CD-0138-mandatory-refinement-pass.md) D1, the refine requirement
  for the `small` implementation composition;
  [CD-0218](CD-0218-the-live-workflow-catalog-retains-three-execution-families.md)
  D2, the steps every implementation item walks;
  [CD-0213](CD-0213-linear-mcp-is-the-only-planning-authority.md) D3, the issue
  identity of a resize successor
- **Preserves:** [CD-0115](CD-0115-a-released-workflow-definition-never-changes-content-in-place.md),
  released definition bytes, digests, and pins; CD-0006 R1 and CD-0013 D8,
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
  the single admission derivation;
  [CD-0225](CD-0225-worker-job-size-overruns-split-without-raising-the-cap.md),
  worker-job size caps
- **Approval:** The Snowball coordinator approved this record on 2026-10-17
  under the operator's delegation. The record is serious under P12 because it
  affects more than one Domain.
- **Amended:** 2026-10-18; 2026-10-19.
- **Revision choice:** Compatible amendments under
  [CD-0036](CD-0036-breaking-law-cutovers.md) D2; the law ID stays unchanged.
  The 2026-10-19 proof rules apply only to newly released size-aware definition
  pins, not to in-flight instances on earlier definitions.
- **Amendment reason:** On 2026-10-18: D5 already fixes the small
  implementation composition; its relationship to CD-0138 D1 must be
  explicit. No composition, released definition, pin, or runtime behavior
  changes in that amendment. On 2026-10-19: D5 must preserve capture when a
  small composition cannot execute its least proof, without weakening that
  proof. D7's timing records must name the events that stamp them and must
  distinguish the actor's submission from the external queue's state.
- **P12 (2026-10-18):** That amendment affects only the workflow-engine Domain.
  It removes or reverses no user-visible behavior, touches no security or
  data-safety rule, and changes no UI/UX. It is not serious under
  [CD-0230](CD-0230-the-conflict-rules-are-one-checked-table-and-an-agent-changes-law-that-is-not-serious.md)
  D4.
- **P12 (2026-10-19):** The changed law's child-Domain count is one:
  `workflow-engine`. Its [knowledge record](../knowledge/records/CD-0229.json)
  has that home and no applicability Domains, before and after this amendment.
  No Domain registry entry or relation changes. The multi-Domain test is false.
  User-visible removal or reversal: yes. Some model-small items keep the full
  steps rather than the small steps, and a newly pinned small item that could
  previously complete without a named check must now wait for an executing
  suite or an operator size override. Security or data safety: no; the amendment
  changes no privilege, credential, destructive operation, or recovery guarantee.
  UI/UX: yes; the workflow changes which size-override questions reach the
  operator. The amendment is serious under CD-0230 D4.
- **Amendment approval boundary (2026-10-19):** This amendment requires the
  operator's approval of its exact text before merge.
- **Amendment approval (2026-10-19):** The operator approved the exact text.
  Approval reference:
  `sha256:e4152063369264fcc924060c5db8944548899a1a432e03f80dd5a4a36c20a22d`.
  Approved source:
  `sha256:b1531fbe01296491efcfca4ac3f329901af1697552674b7f2a03abd5f6a597bf`.

## Context

Goal C asks for workflows built from shared parts: "we snap them together in
different combinations for different workflows, not define each separately."
Its measure is that a new workflow size or kind is made by combining existing
parts, with no new step code, and that each step is defined in exactly one
part.

Goal B asks for workflows of "dynamically different sizes", chosen by "a
defined decision model", not by the operator case by case and not by the
agent's own judgment. Its inputs are subject, sensitivity, blast radius,
security, relative size, and span. A task that proves bigger than its size
stops, the model runs again, and work continues at the new size (Appendix A2
row 8).

The builtin registry holds 146 definition versions across seven families. Each
version is a fully expanded manifest, so a step two families share is written
into each. CD-0006 D3 asks for a purpose-built type for each kind of work, so a
new size would be a new full definition. No work item carries a size, and
CD-0218 D2 gives every implementation item the same steps whatever the change.

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

The registry compiles each composition to the canonical definition that
CD-0013 D1 lists and computes the digest from the compiled definition. An
instance still pins ref, version, and digest. This amends CD-0013 D1: the
source of a definition is its composition, and the fields D1 lists are its
compiled result.

This amends CD-0006 D3. Workflow types are compositions of shared parts, not
separately written definitions. New execution composes only the families
CD-0218 D1 retains.

Composition inside one definition is assembly, not nested execution.
Composition across work items stays forward-link only.

### D3. Released compositions stay frozen

CD-0115 applies to compiled definitions. A composition pins the version of
each part it uses, so a released version keeps compiling to its released bytes
and digest. A change to a part's content is a new part version. The release
that carries it registers a new version of each composition that adopts it and
adds its pin row.

The registry derives the latest version of each composition. Tests read the
latest version from the registry, not from a hand-written expectation.

### D4. Every sized work item has one size from the decision model

A work item that enters a sized workflow carries one size: `small`, `larger`,
or `crucial`, with the meanings goal B gives them.

The decision model is one programmed, versioned function. It reads the inputs
goal B names. Span is three counts read from the change's footprint record
(goal A): the Domains, the packages, and the repositories the change touches.
The declared maturity and audience commitment of the work's subject are also
inputs. The same inputs and model version always give the same size.

The work record keeps the size, the model version, and the inputs the model
read. An agent supplies only the inputs a tool cannot derive, and independent
review checks each one. A wrong input is a review finding, and the corrected
inputs run the model again. The model never reads a size an agent proposes.

Source-free research may omit optional sizing inputs when its workflow does not require a size. Omission records no size: it is neither a zero production-line estimate nor a `small` classification. An agent must not invent production-line estimates or edit paths to obtain a size. This rule does not waive sizing required by a composition, change the admitted range of supplied estimates, or lower any proof obligation.

The obligation bands of CD-0006 D4 and R2 still apply at every size. A size
sets the steps, proof, and testing above that floor; it never removes an
obligation a band requires.

### D5. The size selects the composition and its least proof

Each family declares one composition for each size. A family whose steps do
not vary by size may declare one composition for all three. The implementation
family follows goal B's examples: `small` composes define, implement, review,
and ship; `larger` adds research, architect, and refine; `crucial` adds
repeated implement steps and repeats review and refine. This amends CD-0218
D2 for implementation at every size and CD-0138 D1 only for the `small`
implementation composition. An implementation item walks its pinned
composition, selected by its size subject to the full-retention rules below.
At every size it keeps its outcome contract, independent acceptance, and
closure.

No size ships with zero proof. A `small` change runs the checks its footprint
record names and receives one independent review.

If a named check has no declared executing suite, capture keeps the full
implementation composition and the model's `small` result. The pin records a
typed reason naming that check. The full composition is also retained when an
existing full instance cannot change composition in place. Neither case changes
the size or earns proof credit for an unexecuted check.

For instances pinned to a size-aware implementation definition, the effective
size governs proof: the operator's size under D6 when set, otherwise the model's
result. This includes a full instance first sized after capture. A small item
cannot cross acceptance or ship without its footprint checks and independent
review. It proceeds once its named checks execute through declared suites, or
after the operator changes its size through D6. Instances pinned to earlier
definitions keep their released behavior.

Review findings follow Appendix A2 row 5 by size. At `small`, the change fixes
the problems inside its scope and files follow-up items for the rest. At
`larger`, one refine step fixes them and one more review follows. At `crucial`,
review and refine repeat until review finds no high or medium problem, and the
coordinator ends the loop. At any size, a change does not ship with a high or
medium problem inside its scope; problems outside its scope become follow-up
items (R1). A `larger` change whose second review still finds one inside its
scope has proved bigger than its size under D6. Those open findings are a
model input, and the model returns `crucial` for them.

### D6. Only the model and the operator change a size

The operator may move a size up or down. The record keeps the operator's size
beside the model's result, and the operator's size governs until the operator
changes it.

An agent may only ask the model to run again with the inputs that changed. A
task that proves bigger than its size stops at its current step, and the model
runs again. When the size changes, a successor work item at the new size
supersedes the work item through a forward link and pins its own composition.
The superseded item ends without completing. The successor takes over the
item's Linear issue identity; this amends CD-0213 D3 for a resize successor
only. The successor references the recorded evidence, which still binds only
its unchanged immutable subject (CD-0008 D2). No approval carries to a step
the earlier instance did not reach.

### D7. A sized workflow records its own timing

A sized workflow records when the define step starts, as the agent's first
action on the defined item, and when the change enters the merge queue. Goal
B's measure for a small change, one hour or less, reads these two records.
The targets for `larger` and `crucial` stay open under goals.md §5. No time
target refuses admission until the operator sets one as a gate.

For this measure, define starts at the first agent workflow action-completed
event on the item, excluding core-derived worker-failure completions.
Merge-queue entry means the ship step's delivery assertion, with its PR artifact,
that the delivering actor submitted the change to the repository's merge route.
The core stamps both times. These records apply at every size. They do not
assert the external queue's state or impose a wait.

## Alternatives considered

- Let the agent choose the size. Goal B rules out the agent's own judgment.
- Let the operator choose each size. Goal B rules out a case-by-case choice;
  the operator keeps the override in D6.
- Keep full definitions and add size-specific copies. Each copy would
  redefine shared steps, against goal C's measure.
- Compose by inheritance between definitions. A child that overrides a parent
  step defines that step twice.
- Recompile released versions from current parts. A part change would move
  released digests, the failure CD-0115 forbids.
- Re-pin a running instance to the new size's composition. CD-0008 D4 and D6
  and CD-0013 D1 keep an active execution on its accepted version; a successor
  keeps every pin and uses the forward link CD-0006 R1 already accepts.

## Consequences

The registry gains a part layer and a compiler, and the work record gains a
size and its model inputs. That needs a store migration and a registry
rebuild. The size vocabulary needs one machine-readable declaration under
CD-0077 D2. Instances pinned before the change keep their pins and walk their
released definitions unchanged.

A part change now produces new versions of several compositions in one
release. The pins table grows faster, and reviewers see each affected
composition.

A resize costs one forward-linked successor. The define-start record of the
first item stays the start of the change for the D7 measure.

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
  that a new composition compiles without new step code, and that every
  released version compiles to its pinned digest.
- `go test ./internal/store` must prove that the decision model gives the same
  size for the same inputs and model version, that the record keeps the size,
  model version, and inputs, and that a size an agent proposes is refused.
- `go test ./internal/store` must prove the operator override, the agent
  rerun route, the `crucial` result for open `larger` findings, and a resize
  whose successor supersedes the item without completing it, takes over its
  issue identity, keeps evidence by reference, and carries no approval forward.
- `go test ./internal/store` must prove that a `small` implementation item
  cannot ship without its outcome contract, its footprint checks, and one
  independent review, and that the define-start and merge-queue records exist.
- `go test ./internal/store` must prove that capture retains a full pin with a
  typed reason naming an unsupported check and leaves the model's small size
  unchanged. Neither that fallback nor first sizing of a new full instance
  earns proof credit for an unexecuted footprint check.

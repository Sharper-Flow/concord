# CD-0180: Spec criteria bind to work-item predicates

- **Status:** Accepted
- **Date:** 2026-09-27
- **Scope:** The knowledge manifest's criterion bindings, the doc-contract checker's resolution rule, the law context projection, and the lane packet's law block
- **Related:** CD-0114, CD-0159, CD-0177
- **Approval:** The operator approved this decision in contract version 1 of
  work item `work-439bd3dfda3079826824710f`.

## Context

Two acceptance-criteria systems exist with no structural bridge. Spec Gherkin
criteria bind to scenario ids through criterion_bindings in the knowledge
manifest, and the doc-contract checker accepts only a scenario id or an
exemption reason. Work items carry outcome predicates, and completion
evaluates each predicate through its own verdict. Nothing links a predicate to
the criterion it discharges, so verify evidence cannot chain from a command
result to a predicate to a criterion to law.

## Decision

### D1. criterion_bindings gains the work-predicate form

A criterion binding carries exactly one of three forms: a scenario id, a
recorded exemption reason, or a work-item predicate reference made of
`work_id` and `predicate_id`. The closed patterns are
`^work-[0-9a-f]{8,64}$` and `^predicate:[A-Za-z0-9][A-Za-z0-9._:-]*$`. The
JSON Schema declares the form, and the store's Go manifest model declares it
in the same change (CD-0177 D2). The form is additive, so the schema_version
stays at 1.3.

### D2. The checker resolves the form, and verdicts stay on the work item

`scripts/check-doc-contract.py` resolves a predicate-bound criterion as bound
and validates the two field shapes. The checker carries no verdict-level
discharge. It cannot reach the store, so whether the named predicate passed is
the work-item side's per-predicate verdict, never a finding of this check.
The knowledge-index checker and the shard generator validate the same closed
shapes at authoring.

### D3. The packet's law context lists the bound criteria

The law_subjects projection carries each law's authored bindings, and the
continuity read resolves the ones naming the dispatching work item into that
law's criteria. The packet's law block lists them on the law's line, so the
worker reads which criterion each of this item's predicates discharges. The
verdict path is unchanged, because the predicate verdicts and the
law-revision pins already bind the item to the exact law it discharges.

## Alternatives considered

- Let the checker verify discharge against the store. Rejected: a CI checker
  cannot reach the store, and a checker that pretended to would decide
  correctness it cannot observe.
- Teach the packet to parse the manifest itself. Rejected: the adapter would
  grow a second manifest reader beside the store's, and the two would drift.
- Bind criteria to whole work items without predicates. Rejected: the
  per-predicate verdict is the discharge unit, so an item-level link proves
  less than the evidence it points at.

## Consequences

A spec criterion can name the work item and predicate that discharge it, and
verify evidence chains from a command result to a predicate to a criterion to
law. Older cores read a manifest carrying the new form without refusal,
because the reader drops what its model does not declare (CD-0177 D1). Cores
since this change model both fields. A database keeps listing no criteria
until its rebuild writes the bindings, which matches the authority-tier fill
rule of migration 98.

## Verification

- `TestKnowledgeManifestAdmitsPredicateCriterionBindings` proves the parse
  rule: the three forms compose one valid manifest, while a half predicate
  reference, a mixed form, and each malformed shape refuse.
- `TestContinuityLawContextCarriesThisWorkPredicateCriteria` proves the
  resolution: the law context lists this work's predicate bindings in
  criterion order and drops another work's bindings and scenario bindings.
- `python3 scripts/test-doc-contract.py` carries the checker cases: a
  predicate-bound criterion resolves as bound, and each bad shape reports.
- `python3 scripts/test-knowledge-index.py` proves the authoring side
  validates the closed shapes.
- `bun test adapter/opencode/packet.test.ts` proves the packet lists the
  bound criteria on the law's line.

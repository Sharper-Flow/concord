---
{
  "id": "CD-0180",
  "title": "Spec criteria bind to work-item predicates",
  "authority": {
    "tier": "derived"
  },
  "home_domain_id": "product-memory",
  "applies_to_domain_ids": []
}
---
# CD-0180: Spec criteria bind to work-item predicates

- **Status:** Accepted
- **Date:** 2026-09-27
- **Scope:** The spec law file's criterion bindings and the continuity law-context projection
- **Related:** CD-0159
- **Approval:** The operator approved this decision in contract version 1 of
  work item `work-439bd3dfda3079826824710f`.

## Context

Two acceptance-criteria systems exist with no structural bridge. Spec Gherkin
criteria bind to scenario ids or exemption reasons through
criterion_bindings in the spec law file's metadata. Work items carry outcome predicates, and completion
evaluates each predicate through its own verdict. Nothing links a predicate to
the criterion it discharges, so verify evidence cannot chain from a command
result to a predicate to a criterion to law.

## Decision

### D1. criterion_bindings gains the work-predicate form

A criterion binding carries exactly one of three forms: a scenario id, a
recorded exemption reason, or a work-item predicate reference made of
`work_id` and `predicate_id`. The closed patterns are
`^work-[0-9a-f]{8,64}$` and `^predicate:[A-Za-z0-9][A-Za-z0-9._:-]*$`. The
law file's metadata declares the form, and the store's law model declares it
in the same change. The form is additive, so it changes no existing binding.

### D2. Authoring validates the form, and verdicts stay on the work item

Law-metadata parsing validates the two closed
field shapes at authoring. No repository check judges discharge. A CI check
cannot reach the store, so whether the named predicate passed is the work-item
side's per-predicate verdict.

### D3. The continuity law context lists the bound criteria

The law context read carries each law's authored bindings from the law file's
metadata, and the
continuity read resolves the ones naming the current work item into that
law's criteria. Its `law_context` carries them on the law's
entry, so the agent reads which criterion each of this item's predicates
discharges. Predicate verdicts remain on the work item and bind their exact
predicate and evidence subject; the contract names law by ID, without freshness
pins.

## Alternatives considered

- Let a repository check verify discharge against the store. Rejected: a CI check
  cannot reach the store, and a check that pretended to would decide
  correctness it cannot observe.
- Parse the law files again in the adapter. Rejected: the adapter would
  grow a second law reader beside the store's, and the two would drift.
- Bind criteria to whole work items without predicates. Rejected: the
  per-predicate verdict is the discharge unit, so an item-level link proves
  less than the evidence it points at.

## Consequences

A spec criterion can name the work item and predicate that discharge it, and
verify evidence chains from a command result to a predicate to a criterion to
law.

## Verification

- `TestLawSpecAdmitsPredicateCriterionBindings` proves the parse
  rule: the three forms compose one valid record, while a half predicate
  reference, a mixed form, and each malformed shape refuse.
- `TestContinuityLawContextCarriesThisWorkPredicateCriteria` proves the
  resolution: the law context lists this work's predicate bindings in
  criterion order and drops another work's bindings and scenario bindings.
- Committed law-folder snapshot validation proves the authoring side
  validates the closed shapes.

---
name: concord-work-shaping
description: Shape work before ticket refinement, allocation, or implementation. Check independent outcomes, required mechanism, and operator decisions; reuse recorded readiness when scope is unchanged. Also use to triage whether a reported problem is real or already handled.
---

# Work shaping

Use before refinement, allocation, and implementation. Reuse an existing record
when its scope and evidence still apply. This method grants no scope, approval,
or workflow authority; the Product's planning system owns the record.

## Canonical references

- [Shipped skill delivery and portability](https://github.com/Sharper-Flow/concord/tree/02c0bae9c28eafe6414cab2f390f940dc1f79625/.concord/docs/decisions/CD-0245-concord-ships-the-skill-that-operates-its-domain-shape.md).
- [Operator approval classes](https://github.com/Sharper-Flow/concord/tree/02c0bae9c28eafe6414cab2f390f940dc1f79625/.concord/docs/decisions/CD-0243-four-roles-and-six-operator-approval-classes.md):
  a shaping choice in one of these classes goes to the operator.
- [Work-item size](https://github.com/Sharper-Flow/concord/tree/02c0bae9c28eafe6414cab2f390f940dc1f79625/.concord/docs/decisions/CD-0229-workflows-are-composed-from-shared-parts-and-a-decision-model-sets-each-work-items-size.md):
  in Concord-managed work, the size model sets an item's size; shaping does not.

## Triage

1. Read the request, comments, and earlier triage; do not re-ask settled questions.
2. Search code by concept and planning for duplicates or declined work. Resolve
   recorded law through `concord_knowledge.search`. Report where you looked.
3. Reproduce a defect; use diagnosis when its cause is uncertain. For a change,
   confirm current behavior. Report confirmed, refuted, or missing evidence.
4. Recommend already handled, needs information, ready, or declined, with evidence.
   The existing owner decides within its authority.

## Write the work so it stands alone

State the observed problem, affected party, and delivery-checkable end state.
Cite paths, symbols, commands, errors, and records. Mark unapproved assumptions.

## Record readiness before execution

Record these answers on the existing issue before allocation or implementation.
Missing answers need shaping, not an executing agent guessing them.

- **Outcomes:** list each independently deliverable result, its done condition,
  evidence, and real dependencies. More than one result is a program: present
  separate deliverable assignments through planning, not one undivided ticket.
  Each crosses the layers it needs; size alone does not establish a split.
- **Mechanism:** map each proposed tool, checker, abstraction, or code move to an
  outcome it is necessary to deliver. Name what fails without it and the simpler
  alternative considered. Omit unmatched mechanism; separately propose genuinely
  needed work outside scope. A useful improvement is not automatically required.
- **Decisions:** identify each operator-reserved choice, its class or existing
  reservation, concrete consequence, evidence, and current approval or unanswered
  question. Resolve known choices before affected execution. Record none when no
  class applies; do not turn routine engineering into a new approval gate.

## Choose the next delivery

Assign an outcome whose dependencies and required decisions are resolved. Plan
small green merges within it; atomicity alone does not establish reviewability.
For an inseparable cutover, record why a smaller merge cannot preserve the
required behavior. Temporary coexistence needs a current requirement and a
retirement outcome, not automatic compatibility machinery.

Before adding mechanism or changing scope, revisit its outcome mapping and
decisions. Record the proposed change through planning; do not silently enlarge
the assignment or close or rewrite its parent without the owner's direction.

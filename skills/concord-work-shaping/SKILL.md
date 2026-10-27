---
name: concord-work-shaping
description: Triage a request or defect report and shape it into delivery-checkable work. Use when refining a ticket, checking whether a reported problem is real or already handled, or breaking a plan into independently verifiable outcomes with real dependencies.
---

# Work shaping

Use this method to turn a request, report, or plan into work an executing agent
can deliver without guessing. It reads and proposes. It does not approve scope,
set product direction, or start work. The Product's planning system stays the
only planning authority; this skill adds no state, label, or backlog beside it.

## Canonical references

- [Shipped skill delivery and portability](https://github.com/Sharper-Flow/concord/tree/02c0bae9c28eafe6414cab2f390f940dc1f79625/.concord/docs/decisions/CD-0245-concord-ships-the-skill-that-operates-its-domain-shape.md).
- [Operator approval classes](https://github.com/Sharper-Flow/concord/tree/02c0bae9c28eafe6414cab2f390f940dc1f79625/.concord/docs/decisions/CD-0243-four-roles-and-six-operator-approval-classes.md):
  a shaping choice in one of these classes goes to the operator.
- [Work-item size](https://github.com/Sharper-Flow/concord/tree/02c0bae9c28eafe6414cab2f390f940dc1f79625/.concord/docs/decisions/CD-0229-workflows-are-composed-from-shared-parts-and-a-decision-model-sets-each-work-items-size.md):
  in Concord-managed work, the size model sets an item's size; shaping does not.

## Triage

1. **Read everything recorded.** Read the full request, its comments, and any
   earlier triage notes. Do not re-ask a question already answered.
2. **Check whether it already exists.** Search the code by the concept the
   request describes, not only its wording. Search the planning system and
   accepted law for duplicate, superseded, or previously declined requests.
   Report where you looked. Use `concord_knowledge.search` for recorded law.
3. **Verify the claim.** For a defect, reproduce it from the reported steps;
   when the cause is uncertain, use the diagnosis method. For a proposed change,
   confirm that the current behavior is what the request says it is. Report the
   result: confirmed with its code path, refuted, or not enough detail.
4. **Recommend an outcome**: already handled (point to where), needs
   information (ask specific, answerable questions), ready to deliver, or
   declined with the reason. Leave the decision to whoever owns it.

## Write the work so it stands alone

State the problem with the evidence observed, who or what it affects, and the
end state that closes it, written so a check or reviewer can decide it at
delivery. Keep concrete citations: paths, symbols, commands, error text, and
record identifiers. Mark every assumption the owner has not approved as
unapproved.

## Decompose into outcomes

Split only when the work holds outcomes that can be delivered and verified
separately. Each outcome cuts a narrow but complete path through every layer it
needs, so it is verifiable on its own; do not split by layer. Size alone is not
a reason to split.

- Record a dependency only where one outcome truly cannot start before another
  finishes. Do not invent sequencing for tidiness.
- Do enabling restructuring first, as its own outcome, when it makes the later
  change simple.
- Keep a mechanical change together when it can land green atomically. If it
  cannot, establish the actual dependency and cutover constraints before
  proposing a sequence. Temporary coexistence needs a current requirement and
  a retirement outcome; do not add compatibility machinery automatically.
- The next work to take is any outcome whose dependencies are all done.

Present the proposed outcomes with what each delivers and what blocks it.
Revise until the owner agrees, then record them through the planning system.
Do not close or rewrite a parent record unless its owner asks.

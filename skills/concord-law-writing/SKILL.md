---
name: concord-law-writing
description: Draft, amend, or prune Product law held by Concord. Use when writing decisions, specifications, or standing rules, or shortening an existing law without changing its obligations.
---

# Law writing

Work on one selected Product and agreed change. This skill operates Concord's
knowledge structure; it supplies neither law nor approval and does not prescribe
a worker's lane methodology. Keep proposed wording distinct from accepted law.

## Canonical references

These Concord references define the knowledge and authority machinery, not the
adopting Product's behavior. Read that Product's own accepted law for its rules.
Concord's development-only paths and decision-number conventions apply only to
Concord development, not automatically to another Product.

- [Skill admission and portability](https://github.com/Sharper-Flow/concord/tree/2ad87865e640ce2ebcf8bf0582d003955566b701/.concord/docs/decisions/CD-0245-concord-ships-the-skill-that-operates-its-domain-shape.md).
- [Knowledge classification and formalization](https://github.com/Sharper-Flow/concord/tree/2ad87865e640ce2ebcf8bf0582d003955566b701/.concord/docs/knowledge-formalization-procedure.md).
- [Knowledge identity and publication](https://github.com/Sharper-Flow/concord/tree/2ad87865e640ce2ebcf8bf0582d003955566b701/.concord/docs/concord-knowledge-index.md).
- [Domain ownership](https://github.com/Sharper-Flow/concord/tree/2ad87865e640ce2ebcf8bf0582d003955566b701/.concord/docs/decisions/CD-0222-architecture-bound-product-law-with-package-boundary-code-ownership.md).
- [Compatible amendment and breaking replacement](https://github.com/Sharper-Flow/concord/tree/2ad87865e640ce2ebcf8bf0582d003955566b701/.concord/docs/decisions/CD-0036-breaking-law-cutovers.md).
- [Law-change authority and conflict rules](https://github.com/Sharper-Flow/concord/tree/2ad87865e640ce2ebcf8bf0582d003955566b701/.concord/docs/decisions/CD-0230-the-conflict-rules-are-one-checked-table-and-an-agent-changes-law-that-is-not-serious.md).
- [Operator approval boundary](https://github.com/Sharper-Flow/concord/tree/2ad87865e640ce2ebcf8bf0582d003955566b701/.concord/docs/decisions/CD-0243-four-roles-and-six-operator-approval-classes.md).

## Find the owner

Use `concord_product_view.resolve` and `concord_domain.list` to establish the
Product and Domain. Resolve current law through `concord_knowledge.search` and
`concord_knowledge.resolve_note`; a heading, filename, or confident sentence is
not acceptance. A missing record is not permission to invent one. If a required
source is unavailable, stop the dependent claim and name the missing evidence.

Read the owning clause and its references before drafting. State what fails,
who is affected, and the end state the change must make decidable. Use
`concord_domain.footprint` for affected ownership and law, not permission to
expand scope. Prefer amending the owner to adding another rule beside it;
classify a compatible amendment or breaking replacement under the linked law.

## Draft the rule

Write the obligation first: who must do what, under which conditions, with which
observable result. Make conjunctions, exceptions, refusal behavior and authority
explicit where they change the outcome. Keep implementation recipes, evidence
logs and delivery plans in their owning artifacts, not inside the obligation.

Give each meaning one canonical home. Link an existing safety or approval rule
instead of repeating it. Keep a definition beside its conditions and exceptions.
Keep every requirement needed to decide this rule visible; disclose supporting
reference only when its branch is needed. Use the Product's vocabulary rather
than inventing another name for an existing concept.

## Prune without changing meaning

- Delete a sentence that changes no decision or behavior. Prefer deleting the
  sentence to shortening each of its words. Retain rationale that explains a
  non-obvious constraint; remove repetition and history already held by Git.
- State the target behavior positively. Keep prohibitions that enforce a real
  boundary, with the permitted next action when one exists.
- Compare old and new actor, scope, trigger, required outcome, exception,
  evidence, timing and authority. Test both an admitted case and a refused case
  in review. A shorter rule that admits another case is a behavior change.
- Classify each changed obligation as preserved, changed or retired in the work
  record. Word count is not acceptance. Preserve current protections during an
  editorial edit; route an intended change through the existing law-change path.

## Authorize, verify and publish

Apply the existing operator approval classes to the meaning change. Law editing,
seriousness and Domain count do not independently require operator approval.
When a class applies, obtain the required approval before implementation; a
change to the approval boundary requires approval of its exact wording. Otherwise
record the agent-owned reason and required authority tier. An implementation gap
or tool refusal does not waive a current gate.

Read the selected Product's `.concord/tooling.v1.json` and use its declared
checks and existing publication route. Update the canonical artifact and its
required record, hash, Domain home, relations and coverage together. Regenerate
generated material through its owner. Structural checks prove identity, closure
or shape, not that prose preserves meaning; review owns that comparison.

Report the exact wording, meaning changes, applicable approval and observed
checks in the existing work record. Distinguish candidate validation from
acceptance and publication. After publication, verify the record and revision
with `concord_knowledge.resolve_note`; a local edit or passing check alone does
not establish current accepted law.

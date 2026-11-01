---
name: concord-work-shaping
description: Shape work before refinement, allocation, or implementation. Triage requests, clarify open questions, and plan independently verifiable outcomes; reuse recorded readiness when scope is unchanged.
---

# Work shaping

Reuse applicable readiness in the existing work record. This method adds no
workflow step or gate and grants no scope, approval, or execution authority.

## Canonical references

- [Shipped skill delivery and portability](https://github.com/Sharper-Flow/concord/tree/main/.concord/law/decisions/CD-0245-concord-ships-the-skill-that-operates-its-domain-shape.md).
- [Operator approval classes](https://github.com/Sharper-Flow/concord/tree/main/.concord/law/decisions/CD-0243-four-roles-and-six-operator-approval-classes.md):
  a shaping choice in one of these classes goes to the operator.

## Intake

Read the request, comments, and decisions. Search code by concept and planning
for existing or declined work. Resolve law through the workflow's resolved law
context. Cite findings and where you looked. Check in order:

1. **Freshness:** correct stale paths, lines, links, and status before judging relevance.
2. **Overtaken:** has another ticket, removal, or decision changed what this touches?
3. **Already delivered:** does the requested behavior exist?
4. **Still reproducible:** does the reported failure recur?
5. **Observed or theoretical:** what evidence supports the problem?
6. **Defect or feature:** is overlap with an existing feature actually a defect?
7. **Duplicate or overlap:** does an open ticket cover it?
8. **Right owner:** this Product, host/toolbox, upstream, or another Product?
9. **Conflicts:** does it contradict accepted law or a recorded operator decision?
10. **Value:** does it serve this Product's goals? For Concord only, does it improve agents over non-Concord agents?
11. **Cost against value:** is there a cheaper route or workaround; is it worth doing?
12. **One outcome or several:** split independent outcomes into separate assignments, not by layer or size.
13. **Checkable end state:** can delivery evidence decide success?
14. **Blocked:** does it depend on unmerged work?

Record which checks found something and recommend **proceed**, **clarify**,
**already handled**, **decline**, or **split or merge**, with evidence. The owner
decides within its authority.

## Clarify

Compare the request, decisions, behavior, and end state for unproven premises,
ambiguous scope, or missing results. Record open questions or explicitly none.
Answer from evidence first, citing where; ask the operator only what remains,
one question at a time. Record answers. Reuse settled answers; ask later
questions where they arise.

## Plan Change and Fix work

Before allocation or implementation, record the observed problem, affected party,
checkable end state, and six-part plan. Diagnose unproven defect causes first.

1. **Outcomes and checks:** each independent result, done condition, evidence, and dependencies.
2. **Approach:** map each mechanism to its outcome, what fails without it, and the simpler alternative. Omit unmatched mechanism.
3. **Decisions:** choices, reasons, rejected options; operator-reserved choices with class or reservation, consequence, and approval or question. Record none when none apply.
4. **Touches:** files, interfaces, and law affected.
5. **Merge split:** small green merges; explain an inseparable cutover. Temporary coexistence needs a current requirement and retirement outcome.
6. **Risks:** material uncertainties, failure modes, and how they will be checked.

Allocate only outcomes whose dependencies and required decisions are resolved.
Record plan changes and why; ask only for operator-reserved choices. Propose work
outside the assignment separately rather than silently enlarging it.

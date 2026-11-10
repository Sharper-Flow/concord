---
name: concord-work-shaping
description: Shape work before refinement, allocation, or implementation. Triage requests, including production configuration-only captures, clarify open questions, and plan independently verifiable outcomes; reuse recorded readiness when scope is unchanged.
---

# Work shaping

Reuse applicable readiness in the existing work record. This method adds no
workflow step or gate and grants no scope, approval, or execution authority.

## Canonical references

- [Shipped skill delivery and portability](https://github.com/Sharper-Flow/concord/tree/main/.concord/law/decisions/CD-0245-concord-ships-release-versioned-portable-skills.md).
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

Record which checks found something and recommend **proceed**, **already handled**,
**decline**, or **split or merge**, with evidence. The owner
decides within its authority.

Before capturing a production configuration-only operation, establish an
admitted execution and acceptance route for its actual target. If no route
exists, report the limitation before capture. Distinguish host-owned work from
new managed capability and obtain any applicable operator decision; do not invent
a recipe, receipt, or research classification. Host-owned work has no Concord
workflow authority, evidence, review, or completion claim. Keep an existing
managed item held when its supported execution or handoff is unavailable.

## Clarify

After the type pick, new plans require Clarify before the planning step for every
type; Fix diagnoses first. Older plans without Clarify do not gain it on a type
switch. Run Clarify inline with the driving agent; it has no helper. Synthesize instructions, the ticket, recorded decisions,
and relevant investigation or diagnosis findings; the investigation may inform
the step without first drafting an implementation plan. Check intended product
outcomes, product direction, request intention, and scope. Answer from evidence
first, citing where; ask the operator only remaining questions, one at a time
in the pane; on a [ticket frontend](https://github.com/Sharper-Flow/concord/tree/main/.concord/law/specs/workflow-engine-contract.md#81-operator-decisions-stay-in-conversation), one comment carrying that step's
independent questions, and a question that depends on an earlier answer waits
for it. Follow `records.md` for quoting operator answers, recording settled
decisions and approval evidence. Reserved approvals stay with the host question tool until
agents post under their own Linear identity, except that a session the
frontend starts with that tool refused publishes them through the frontend,
where an answer carries approval only under the ticket-frontend skill's author
rule. Record question resolutions or
the explicit result that no operator questions remain. Running the step is
mandatory where the plan carries it; asking is not. Clarify settles what to
build and grants no blanket implementation or UI/UX
approval. Reuse settled answers; ask later questions where they arise.

### Example: decision record

Before: copy the complete ordinary discussion into a Linear comment.

After: the ticket states the chosen export format and its consequence; the
Concord step record quotes the answer and links its evidence.

## Plan Change and Fix work

Before allocation or implementation, record the observed problem, affected party,
checkable end state, and six-part plan. Diagnose unproven defect causes first.

1. **Outcomes and checks:** each independent result, done condition, evidence, and dependencies.
2. **Approach:** map each mechanism to its outcome, what fails without it, and the simpler alternative. Omit unmatched mechanism.
3. **Decisions:** choices, reasons, rejected options; operator-reserved choices with class or reservation, consequence, and approval or question. Record none when none apply.
4. **Touches:** files, interfaces, and law affected.
5. **Merge split:** small green merges; explain an inseparable cutover. Temporary coexistence needs a current requirement and retirement outcome.
6. **Risks:** material uncertainties, failure modes, and how they will be checked.

An architectural change — one that alters a Domain boundary, a governed
relation, state authority, a cross-Domain interface or runtime flow, or a claim
in current architecture reference — adds detail within the plan parts and
final-review routing. Load the shipped `concord-architecture-writing` skill
before planning one.

Allocate only outcomes whose dependencies and required decisions are resolved.
Record plan changes and why; ask only for operator-reserved choices. Propose work
outside the assignment separately rather than silently enlarging it.

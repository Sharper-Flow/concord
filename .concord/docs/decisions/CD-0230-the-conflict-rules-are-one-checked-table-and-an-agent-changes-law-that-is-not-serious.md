# CD-0230: The conflict rules are one checked table, and law changes follow the operator's approval boundary

- **Status:** Accepted
- **Date:** 2026-10-17
- **Scope:** How Concord applies the conflict rules of `goals.md` Appendix A:
  the machine-readable rule table, the mechanism that enforces each rule, the
  record of each application, law-change authority under R3 and P12, the
  operator approval classes, and the follow-up ledger
- **Implements:** [`goals.md`](../goals.md) goal F for the conflict rules,
  Appendix A, and goal E's measure that follow-up items get closed
- **Amends:** [CD-0006](CD-0006-concord-root-product-policy.md) D5 and D10;
  [CD-0013](CD-0013-workflow-engine-mechanism.md) D9;
  [CD-0036](CD-0036-breaking-law-cutovers.md) D2, who chooses amendment or
  replacement; [CD-0159](CD-0159-authority-tier-for-product-law.md) D2, D4, and D5;
  [CD-0218](CD-0218-the-live-workflow-catalog-retains-three-execution-families.md)
  D1, its operator-approved law route;
  [CD-0222](CD-0222-architecture-bound-product-law-with-package-boundary-code-ownership.md)
  D1, D4, and D5, the law outcome and law mandate of Product-changing work
- **Preserves:** CD-0159 D1, D3, D6, and D7, the authority tier and its checks;
  the ban on a silent scope cut and a substituted outcome in CD-0006 and
  [CD-0012](CD-0012-bind-stated-goals-to-delivered-outcomes.md); CD-0036 D1
  and D3 to D6, law revision identity and breaking cutovers;
  [CD-0122](CD-0122-concord-workflow-scope-and-defect-repair.md), work outside
  the workflow; [CD-0213](CD-0213-linear-mcp-is-the-only-planning-authority.md),
  Linear as the only planning authority
- **Approval:** The Snowball coordinator approved this record on 2026-10-17
  under the operator's delegation. The record is serious under P12 because it
  affects more than one Domain.
  The operator approved the D5 coordinator-routing amendment as exact text on
  2026-10-19 for
  [Concord (CON) issue 1212](https://linear.app/sharper-flow/issue/CON-1212).
- **Amended:** 2026-10-19, approved by the operator as exact text
  for [Concord (CON) issue 1200](https://linear.app/sharper-flow/issue/CON-1200).
  D4's P12 Domain test counts the child Domains of the laws a change adds,
  modifies, or removes, not its footprint record. The change is serious under
  P12 because it narrows when an operator approval applies, which removes
  behavior the operator sees.
- **Amended:** 2026-10-19, for
  [Concord (CON) issue 1212](https://linear.app/sharper-flow/issue/CON-1212).
  D5 routes a choice that carries a security risk, changes UI/UX, sets a
  preference, or sets product direction, and that no approval point holds,
  through the item's coordinator before the operator. The change is serious
  under P12 because it changes which questions reach the operator.
- **Amended by:** [CD-0242](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md) D9, D10

## Context

Appendix A of `goals.md` is accepted constitutional law. It gives a rule for
each pair of goals (A1) and for each conflict situation by change size (A2).
Goal F asks that the rules become "a mechanism inside Concord, not prose", so
the operator's spot checks can show that agents follow them.

Today the rules exist only as text in `goals.md` and in coordinator
instructions. No record shows which rule settled a conflict. Accepted records
still require operator approval for every law change: CD-0006 D5 ("Human
approval is always required to enact law changes or governed scope cuts"),
CD-0159 D4 ("Human approval remains required to enact a law change"),
CD-0222 D1 ("it carries an operator-approved mandate"), CD-0218 D1
("Product-law changes require the existing operator-approved Git law route"),
and CD-0036 D2 ("The operator makes this legislative choice through the Git
delta"). CD-0006 D10, CD-0013 D9, and CD-0222 D4 and D5 bound each law change
by an operator-approved spec mandate. Rule R3 says instead: "change the law or
if serious than consult the user", and P12 defines serious.

## Decision

### D1. One machine-readable table holds the rules

A contract file under `contracts/` holds every A1 cell and every A2 row: the
rule ID, the goals or situation in conflict, the rule for each size, the actor
who applies it, and its enforcing mechanism. The rule text stays in
`goals.md`. The table cites each rule by its ID and records the `goals.md`
content hash it was checked against.

A validator in the repository's required checks refuses the table when a cell
or row of Appendix A is missing, when a rule names no mechanism kind, or when
the `goals.md` content hash differs from the recorded one.

### D2. Each rule names how it is enforced

A rule's mechanism kind is one of four: a programmed check that refuses or
reports; a coordinator decision that a tool records; an operator boundary
where the operator approves or accepts; or a gap. A prose instruction is not a
mechanism (Appendix A P3 and P7).

A gap row names the follow-up item that will build its mechanism. The
validator reports every gap, so the gap count measures progress toward goal F.

### D3. Applying a rule leaves a record

When an agent, the coordinator, or the operator settles a conflict, the work
record names the rule ID, the change's size, and the outcome. The operator's
spot checks read these records.

A conflict between a law's letter and its stated intent is a law conflict. The
agent raises it under R3 and does not apply the letter silently.

### D4. Law changes follow the operator's approval boundary

An agent changes a law and records why. Operator approval applies only when
the change falls within a class defined by
[CD-0243 D2](CD-0243-four-roles-and-six-operator-approval-classes.md#d2-the-operator-approves-six-classes-of-decision).
Seriousness, Domain count, and crucial size do not independently require
approval. Adding, removing, or reclassifying an operator approval always
requires the operator's approval of its exact wording.

This amends CD-0006 D5 and D10, CD-0013 D9, CD-0036 D2, CD-0159 D4, CD-0218
D1, and CD-0222 D1, D4, and D5. Where they require human or operator approval
for a law change, a law mandate, or the choice between amendment and
replacement, that approval is required only under CD-0243 D2. An agent-owned
change carries the agent's recorded reason and its CD-0036 choice.

This amends CD-0159 D2 and D5. A law revision enacted without the operator's
approval gives the record the `derived` tier, whatever tier it held before,
including a default tier from the closed D5 cohort.

A governed scope cut is not a law change. It is a product decision under D5,
keeps the operator's approval, and is never silent.

[CD-0242 D10](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d10-knowledge-is-an-input-and-a-law-edit-changes-only-what-it-changes)
governs: an agent corrects a factual documentation error inside its work item
without an operator prompt when the correction changes neither intended
behavior nor an operator decision, and such a correction is not a law change
under P12.

### D5. The operator approves only at the operator's boundaries

The six classes and their boundaries are defined by CD-0243 D2: product
direction, UI/UX, production application data, security, increased spending,
and legal obligations. Every applicable class counts. Decisions outside them
belong to agents under CD-0243 D3. A gap in the table's classification is not
an independent approval requirement; each point must be classified under a
class or as agent-owned.

[CD-0242 D9](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d9-completion-needs-proof-approvals-delivery-and-publication)
governs the UI/UX class: it includes approval of a development preview of the
final candidate before it is pushed to a shared remote or merged.

The agent asks the operator directly, with its recommendation and material
risks. The release train engineer (RTE) coordinates dependencies; it does not decide operator choices.
An advisor supplies a second opinion, not operator approval. A spawned review
subagent counts as external review, with required checks and evidence intact.

CD-0243 D5 governs recovery and permanent loss. The operator may explicitly
approve explained irreversible loss without a verified recovery route.
Customer-data loss remains reserved. Lack of recovery outside the classes is
not a separate approval requirement or absolute veto. An implementation gap
does not authorize bypassing a current tool gate (CD-0243 D6).

### D6. Follow-up items form a checkable ledger

A follow-up item that a rule creates is a Linear issue. It carries a label
naming the rule ID and a link to the change that raised it. The rule table
names that label for each rule that files follow-up items. A Linear view
filtered by those labels lists the open items by rule, age, and raising
change, so goal E's measure, that follow-up items get closed and not only
filed, can be checked. Until a check enforces the label, the rule table lists
the D6 label duty as a gap.

## Alternatives considered

- Keep the rules as prose and rely on review. Goal F rules this out.
- Copy the rule text into the table. Two copies would drift.
- Require approval based on seriousness or Domain count alone. CD-0243 D2
  reserves approval for six classes, including changes to that boundary.
- Keep the follow-up ledger in the store. CD-0213 keeps planning facts in
  Linear only.

## Consequences

A law change outside the six classes needs no operator turn. An agent records
its reason and review checks the applicable approval classes.

A record the operator legislated becomes `derived` when an agent revises it
without operator approval, so its standing shows who last enacted it.

Missing classifications are implementation gaps, not approval classes. Current
tool refusals still follow their admitted recovery routes until aligned.

Every change to `goals.md` moves its content hash, so the change that edits
`goals.md` also checks the table against it and records the new hash.

## Verification

- `python3 scripts/check-doc-contract.py` and
  `python3 scripts/check-knowledge-closure.py` pass when the record is
  registered.
- `python3 scripts/run-script-tests.py` must prove that the rule-table
  validator refuses a missing A1 cell or A2 row, a rule with no mechanism
  kind, and a stale `goals.md` hash, that it reports each gap, and that each
  rule that files follow-up items names its label.
- `go test ./internal/store` must prove consent follows the six classes,
  including exact-text approval for approval-boundary changes, while an
  agent-enacted law revision records the `derived` tier. Classification and
  runtime alignment are separate implementation obligations under CD-0243 D6.
- `python3 scripts/check-conflict-rules.py` checks each recorded conflict rule
  and its source hash; it does not by itself prove six-class runtime consent.

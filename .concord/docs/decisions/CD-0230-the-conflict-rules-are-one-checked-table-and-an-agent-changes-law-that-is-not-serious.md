# CD-0230: Each conflict rule names its mechanism, and law changes follow the operator's approval boundary

- **Status:** Accepted
- **Date:** 2026-10-17
- **Scope:** How Concord applies the conflict rules of `priorities.md` Appendix A:
  the mechanism that enforces each rule, the record of each application, law-change authority under R3 and P12, the
  operator approval classes, and the follow-up ledger
- **Implements:** [`goals.md`](../goals.md) goal F for the conflict rules in
  [`priorities.md`](../priorities.md) Appendix A, and goal E's measure that follow-up items get closed
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

## Context

Appendix A of `priorities.md` is accepted constitutional law. It gives a rule for
each pair of goals (A1) and for each conflict situation by change size (A2).
Goal F asks that the rules become "a mechanism inside Concord, not prose", so
the operator's spot checks can show that agents follow them.

Today the rules exist only as text in `priorities.md` and in coordinator
instructions. No record shows which rule settled a conflict. Accepted records
still require operator approval for every law change: CD-0006 D5 ("Human
approval is always required to enact law changes or governed scope cuts"),
CD-0159 D4 ("Human approval remains required to enact a law change"),
CD-0222 D1 ("it carries an operator-approved mandate"), and CD-0218 D1
("Product-law changes require the existing operator-approved Git law route"). CD-0006 D10, CD-0013 D9, and CD-0222 D4 and D5 bound each law change
by an operator-approved spec mandate. Rule R3 says instead: "change the law or
if serious than consult the user", and P12 defines serious.

## Decision

### D1. Appendix A is the only copy of the rules

The rule text lives only in `priorities.md` Appendix A. No contract table,
generated catalog, or content-hash tie copies it. This record's Verification
names each rule's enforcing mechanism or its gap.

### D2. Each rule names how it is enforced

A rule's mechanism kind is one of five: a programmed check that refuses or
reports; a review that records the rule it applied; a coordinator decision
that a tool records; an operator boundary where the operator approves or
accepts; or a gap. Each rule uses the cheapest kind that upholds it (Appendix
A P3). A prose instruction alone is not a mechanism.

A gap names, in this record's Verification, the follow-up item that will
build its mechanism.

For S, P10, A2 row 9 and P8:

Use recorded independent review for the data-loss and quality-cost conflict
rules. Before risking permanent loss, the agent records what may be lost and
the verified recovery route, or its absence. Where existing approval rules
require consent, the operator must approve that specific loss after receiving
the explanation; general task approval is insufficient. Before building an
operator-requested behavior the agent judges unnecessarily complex, the agent
records its quality-cost explanation once, with evidence, and honors the
operator's decision. Review cites the source records and checks that the
explanation, and any required approval, preceded execution. Missing, late or
unverifiable evidence leaves compliance unproved. No new approval class or
automatic execution gate is added. Existing consent and safety gates remain.
Review may detect a violation only after the action, and Concord cannot
intercept external tools it does not mediate.

For A2 row 5:

Review repairs in-scope findings in its own pass and verifies the repairs
(CD-0242 D5); the rest become D6 follow-up items. A high or medium problem is a
Review finding of severity `P0`, `P1` (high) or `P2` (medium); `P3` is low. A
crucial change cannot leave execution or complete while the current Review state
holds an open in-scope high or medium finding, whatever the reporter's blocking
flag. The coordinator ends the loop by dispatching corrections and Review until
that holds. Small and larger changes keep CD-0242 D5's blocking rule. No review
count, ledger or separate final evaluator is added.

### D3. Applying a rule leaves a record

When an agent, the coordinator, or the operator settles a conflict, the work
record names the rule ID, the change's size, and the outcome. The operator's
spot checks read these records. No dedicated store record carries a
settlement: Review records it as a finding, and other settlements name the rule
in the existing work record or pull request.

A conflict between a law's letter and its stated intent is a law conflict. The
agent raises it under R3 and does not apply the letter silently.

### D4. Law changes follow the operator's approval boundary

An agent changes a law and records why. Operator approval applies only when
the change falls within a class defined by
[CD-0243 D2](CD-0243-four-roles-and-six-operator-approval-classes.md#d2-the-operator-approves-six-classes-of-decision).
Seriousness, Domain count, and crucial size do not independently require
approval. The P12 Domain test counts the child Domains of the laws a change
adds, modifies, or removes, not its footprint record. Adding, removing, or reclassifying an operator approval always
requires the operator's approval of its exact wording.

Law changes and law mandates require operator approval only under CD-0243 D2.
An agent-owned change carries the agent's recorded reason.

A law revision enacted without the operator's
approval gives the record the `derived` tier, whatever tier it held before,
including a default tier from the closed D5 cohort.

A governed scope cut is not a law change. It follows D5 and CD-0243 D3's
Concord deletion exemption, and is never silent. Within assigned work on
Concord, retiring Concord's own law, unwanted mechanisms and systems, and tests
of retired behavior needs no per-deletion product-direction, UI/UX or governed-scope-cut approval.
CD-0243 D3 owns the exemption's delivery duties, exclusions and retained gates.

An agent corrects a factual documentation error inside its work item without an operator prompt when the correction changes neither intended behavior nor an operator decision, and such a correction is not a law change under P12 ([CD-0242 D10](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d10-knowledge-is-an-input-and-a-law-edit-changes-only-what-it-changes)).

### D5. The operator approves only at the operator's boundaries

The six classes and their boundaries are defined by CD-0243 D2: product
direction, UI/UX, production application data, security, increased spending,
and legal obligations. Every applicable class counts. Decisions outside them
belong to agents under CD-0243 D3.

The UI/UX class includes approval of a development preview of the final candidate before it is pushed to a shared remote or merged ([CD-0242 D9](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d9-completion-needs-proof-approvals-delivery-and-publication)).

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
naming the rule ID and a link to the change that raised it. A Linear view
filtered by those labels lists the open items by rule, age, and raising
change, so goal E's measure, that follow-up items get closed and not only
filed, can be checked. Until a check enforces the label, the D6 label duty is
a gap.

## Alternatives considered

- Keep the rules as prose and rely on review. Goal F rules this out.
- Keep a machine-readable copy of the rules, checked against `priorities.md`
  by content hash. The copy cost a CI round on every `priorities.md` edit and
  its generation counter collided across changes, while review still decided
  whether a rule was applied.
- Require approval based on seriousness or Domain count alone. CD-0243 D2
  reserves approval for six classes, including changes to that boundary.
- Keep the follow-up ledger in the store. CD-0213 keeps planning facts in
  Linear only.

## Consequences

A law change outside the six classes needs no operator turn. An agent records
its reason and review checks the applicable approval classes.

A record the operator legislated becomes `derived` when an agent revises it
without operator approval, so its standing shows who last enacted it.

Current tool refusals still follow their admitted recovery routes until aligned.

## Verification

Complete rule enforcement is not implemented. R3, P12 and A2 row 3 reuse the managed-workflow law-change checks.
`internal/store.TestLawChangeAgentOwnedReasonAndTier` proves a reason and
derived-tier enactment.
`internal/agent.TestWorkflowUnclassifiedLawChangeAdmission` checks admission
through public dispatch. These controls do not infer the
consequence class or judge a reason's substance; review owns those judgments.
They do not detect code that contradicts current law without a law edit;
review and goal A's spot checks own that detection.
They do not establish D4 enforcement on the ad-hoc development route or
whole-record runtime alignment under CD-0243 D6.

P5 reuses the declared decision-domain fact. `internal/store.TestSixClassDecisionDomainsAuthoring`
admits exactly the six classes and refuses every historical or unknown member,
and `internal/store.TestSixClassDecisionDomainsRoundTrip` proves each authored
class stores and projects byte-identically. The authored class remains a review
judgment.

U and A2 row 7 use the existing operator boundary. The agent asks the operator
directly for preview and early visual-design approval under CD-0242 D9 and
records the answer in the existing work record. `internal/store.TestSixClassDecisionDomainsAuthoring`
proves the declared UI/UX class boundary. They do not prove approval of a final
preview or mockup, renewed approval of a changed experience, or interception of
a Git push or merge.

S, P10, A2 row 9 and P8 use D2's recorded-review method.
`internal/store.TestReviewCompletionRetainsAppliedRuleContextClaims` follows a
live Review completion into the work-pin and continuity reads. It checks exact
claim text and evidence references, retained producer and source ordering,
and that claims grant neither approval nor acceptance.
`internal/store.TestDispatchRefusesStaleWorkContextPacket` and
`internal/store.TestDispatchRefusesTamperedWorkContextPacket` protect the
context supplied to the reviewer. These tests prove carriers and source
bindings, not the judgment that an explanation is adequate or consent informed.
`scripts/testdata/conflict-review-cases.md` supplies safe positive and
negative cases for independent application of the method, including late or
unverifiable evidence, informed approval without recovery, and an operator's
quality-cost decision. The delivery's recorded independent review supplies
that application evidence; shipping the instruction alone does not.
Public reads expose current context claims, within the existing context bounds
and selection lifecycle, not a permanent review history. Missing source or
ordering proof remains unproved, never compliant by default.

P1, P2 and P3 use D2's recorded-review method.
`internal/store.TestReviewCompletionRetainsAppliedRuleContextClaims` also retains
P1/P2/P3 claims; the same source-binding controls protect their inputs.
`scripts/testdata/conflict-review-law-cases.md` supplies ownership, retirement
order, cheapest-means, and missing-evidence cases for independent application of
the existing method.
Review judges the law's behavior owner rather than a declared home name, and
whether the chosen means catches the relevant violation rather than merely
resolving an anchor. For P2, review checks that the merged change retires the
requirement itself, not only its wording or location, and that no merged state
removes code a current law still requires; a law retirement in the same change
complies, as
[CD-0042 D3](CD-0042-pre-go-live-single-path.md#d3-superseded-development-paths-are-deleted)
expects. P3 permits adequate guidance for an ordinary law; D2's ban
on prose-only mechanisms governs the conflict rules. The cases' source
labels and recorded sizes are fictional inputs, not real operation receipts or
an assessment of the delivery's size. Carrier tests do not prove those judgments.
The delivery's recorded independent review supplies the application evidence;
the method adds no runtime gate or ad-hoc-route enforcement.

P7, P9 and P11 use D2's recorded-review method.
`internal/store.TestReviewCompletionRetainsAppliedRuleContextClaims` also retains
P7/P9/P11 claims; the same source-binding controls protect their inputs.
`scripts/testdata/conflict-review-parts-cases.md` supplies first- and
second-occurrence, prose-versus-tool, shared-part repair, follow-up sizing and
operator-request cases for independent application of the existing method.
Review judges occurrence from recorded source and planning evidence, not the
author's claim; a missing search or size assessment leaves the outcome unproved.
Carrier tests do not prove those judgments; the delivery's recorded independent
review supplies the application evidence.
No repetition counter, part registry or automatic tool builder is added. The
follow-up item's rule label, raising-change link enforcement and saved view
remain D6's open duty below.

P4 and P6 use D2's recorded-review method.
`internal/store.TestReviewCompletionRetainsAppliedRuleContextClaims` also retains
P4/P6 claims; the same source-binding controls protect their inputs.
`scripts/testdata/conflict-review-architecture-cases.md` supplies kept, crossed
and moved Domain boundaries, forced duplication, and parameterized, copied and
special-cased shared-part variant cases for independent application of the
existing method. Review judges a boundary move against a recorded architect
step, and a variant's fit against analysis of the shared part, not the author's
claim; missing records leave the outcome unproved. Carrier tests do not prove
those judgments; the delivery's recorded independent review supplies the
application evidence. A2 row 4's boundary-change handling remains open below.

R1 and A2 row 1 use D2's recorded-review method.
`internal/store.TestReviewCompletionRetainsAppliedRuleContextClaims` also retains
R1/A2-1 claims; the same source-binding controls protect their inputs.
`scripts/testdata/conflict-review-bloat-cases.md` supplies removed and retained
fix-made bloat, older bloat with and without a follow-up item, older bloat removed
outside the change's scope, and unknown-origin cases for independent application
of the existing method. Review judges whether the fix made the bloat against the
change's recorded base, not the author's claim. When a follow-up item covers the
bloat, missing base evidence leaves the outcome unproved; bloat shipped without
one violates R1 whatever its origin. Carrier tests do not prove those judgments; the delivery's
recorded independent review supplies the application evidence. The follow-up
item's rule label, raising-change link enforcement and saved view remain D6's
open duty below.

A2 row 5 uses a programmed check in the existing Review admission.
`internal/store.TestCrucialReviewExitRefusesHighOrMediumFinding` proves a crucial
change cannot leave execution with an open in-scope `P0`-`P2` finding marked
non-blocking, while `P3`, out-of-scope, closed, larger-size and group-opening
cases stay admitted; `internal/store.TestReviewStateReadsCrucialFromEffectiveSize`
proves the rule follows the effective size, including the operator's size.
`internal/store.TestReviewCurrentAdmissionNegativeCases` proves the every-size
blocking rule. `scripts/testdata/conflict-review-repair-cases.md` supplies
in-scope repair, unverified fix, dropped and claimed follow-up, and crucial
medium-finding cases for independent application of the existing review method,
which judges in-scope repair and follow-up filing that the check does not decide.
The follow-up item's rule label, link and saved view remain D6's open duty.

D3 has no dedicated carrier. Review records an applied rule as a finding whose
statement starts `conflict-rule:<rule ID>`;
`internal/store.TestReviewCompletionRetainsAppliedRuleContextClaims` proves the
carrier. Nothing checks that a settlement outside Review names its rule.

The remaining enforcement and proof is assigned below:

| Clauses | Open owner and limit of delivered proof |
|---|---|
| D3's letter-versus-intent conflict | [CON-1097](https://linear.app/sharper-flow/issue/CON-1097). The law-side review cases exercise raised and silently applied conflicts. They do not prove that agents raise a conflict they never report. |
| R2, A2 rows 2 and 4 | [CON-1098](https://linear.app/sharper-flow/issue/CON-1098). `internal/agent.TestResolveOverlapDependsOnRecordsDirectedSequencingWithoutConsent` proves directed work-item sequencing, not selection of the shared-Domain step or release when the other change completes it. Shared stages do not by themselves prove A2 row 4's boundary-change handling. |
| D4 and D5; CD-0243 runtime alignment | [CON-1100](https://linear.app/sharper-flow/issue/CON-1100). Recorded review does not establish prevention of external data loss, automatic six-class inference, or enforcement on the ad-hoc route. Contract-time consent does not establish informed approval of a specific loss. Classifications, carrier tests and the selected synthetic reviews do not prove whole-record runtime alignment. |
| A2 row 6; D6 | [CON-1101](https://linear.app/sharper-flow/issue/CON-1101). Nothing enforces labels, raising-change links, the Linear view, or closure tracking. Nothing records or checks that the coordinator schedules a shipping-blocking unrelated bug first. |
| A2 row 8 | [CON-1344](https://linear.app/sharper-flow/issue/CON-1344). Same-item resizing does not prove adding required stages while retaining completed work. CD-0229's Verification also retains the separate small-work proof and timing owners. |

The surviving outcomes still need executable proof or an explicit
owning law disposition. CD-0242 D4–D7 replaces the old
execution machinery; D6 retires production-line caps and mandatory splitting.
CD-0243 replaces seriousness, Domain count, crucial size, and unclassified
approval points as independent consent triggers. Neither replacement makes
an unproved retained rule satisfied or permits bypassing a current gate.

- `go test ./internal/store` must prove an agent-enacted law revision records
  the `derived` tier and a declared law removal is absent from the verified
  inventory.

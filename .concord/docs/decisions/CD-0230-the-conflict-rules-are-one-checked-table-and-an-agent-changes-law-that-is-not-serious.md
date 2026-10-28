# CD-0230: The conflict rules are one checked table, and law changes follow the operator's approval boundary

- **Status:** Accepted
- **Date:** 2026-10-17
- **Scope:** How Concord applies the conflict rules of `goals.md` Appendix A:
  the machine-readable rule table, the mechanism that enforces each rule, the
  record of each application, law-change authority under R3 and P12, the
  operator approval classes, and the follow-up ledger
- **Implements:** [`goals.md`](../goals.md) goal F for the conflict rules,
  Appendix A, and goal E's measure that follow-up items get closed
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

Appendix A of `goals.md` is accepted constitutional law. It gives a rule for
each pair of goals (A1) and for each conflict situation by change size (A2).
Goal F asks that the rules become "a mechanism inside Concord, not prose", so
the operator's spot checks can show that agents follow them.

Today the rules exist only as text in `goals.md` and in coordinator
instructions. No record shows which rule settled a conflict. Accepted records
still require operator approval for every law change: CD-0006 D5 ("Human
approval is always required to enact law changes or governed scope cuts"),
CD-0159 D4 ("Human approval remains required to enact a law change"),
CD-0222 D1 ("it carries an operator-approved mandate"), and CD-0218 D1
("Product-law changes require the existing operator-approved Git law route"). CD-0006 D10, CD-0013 D9, and CD-0222 D4 and D5 bound each law change
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

A rule's mechanism kind is one of five: a programmed check that refuses or
reports; a review that records the rule it applied; a coordinator decision
that a tool records; an operator boundary where the operator approves or
accepts; or a gap. Each rule uses the cheapest kind that upholds it (Appendix
A P3). A prose instruction alone is not a mechanism.

A gap row names the follow-up item that will build its mechanism. The
validator reports every gap, so the gap count measures progress toward goal F.

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
approval. The P12 Domain test counts the child Domains of the laws a change
adds, modifies, or removes, not its footprint record. Adding, removing, or reclassifying an operator approval always
requires the operator's approval of its exact wording.

Law changes and law mandates require operator approval only under CD-0243 D2.
An agent-owned change carries the agent's recorded reason.

A law revision enacted without the operator's
approval gives the record the `derived` tier, whatever tier it held before,
including a default tier from the closed D5 cohort.

A governed scope cut is not a law change. It is a product decision under D5,
keeps the operator's approval, and is never silent.

An agent corrects a factual documentation error inside its work item without an operator prompt when the correction changes neither intended behavior nor an operator decision, and such a correction is not a law change under P12 ([CD-0242 D10](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d10-knowledge-is-an-input-and-a-law-edit-changes-only-what-it-changes)).

### D5. The operator approves only at the operator's boundaries

The six classes and their boundaries are defined by CD-0243 D2: product
direction, UI/UX, production application data, security, increased spending,
and legal obligations. Every applicable class counts. Decisions outside them
belong to agents under CD-0243 D3. A gap in the table's classification is not
an independent approval requirement; each point must be classified under a
class or as agent-owned.

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

The table and its census are implemented; complete rule enforcement is not.
`python3 scripts/test-conflict-rules.py` exercises D1 and D2's structural
checks, including `recorded_review` with resolving evidence anchors and refusal
of missing or prose-only evidence. These checks do not prove that a review
applied a rule. `internal/store.TestOperatorApprovalInventoryTableCoversCensus` and
`internal/store.TestOperatorApprovalInventoryRegistryExpansionIsCovered`
check D5's declared approval-point inventory, not six-class consent behavior.
`python3 scripts/check-conflict-rules.py` reports both rule and approval-point
gaps. Passing that validator does not satisfy an outstanding entry.

R3, P12 and A2 row 3 reuse the managed-workflow law-change checks.
`internal/store.TestLawChangeAgentOwnedReasonAndTier` proves a reason and
derived-tier enactment without treating seriousness, size or Domain count as
consent triggers. `internal/store.TestLawChangeSixClassesPinExactText` and
`internal/store.TestLawChangeApprovalRequiresConsumedIdentity` check six-class
consent, exact enacted text and consumed operator identity.
`internal/agent.TestWorkflowClassifiedLawChangeApproval` exercises signed
exact-text approval through public dispatch, including tampering refusal;
`internal/agent.TestWorkflowUnclassifiedLawChangeAdmission` checks admission
without approval for an unclassified change. These controls do not infer the
consequence class or judge a reason's substance; review owns those judgments.
They do not detect code that contradicts current law without a law edit;
review and goal A's spot checks own that detection.
They do not establish D4 enforcement on the ad-hoc development route or
whole-record runtime alignment under CD-0243 D6.

P5 reuses the six-class classifier. `internal/store.TestSixClassApprovalPolicy`
demands consent for each authored class and none for an unclassified contract,
`internal/store.TestSixClassDecisionDomainsAuthoring` admits exactly the six
classes, and `internal/agent.TestWorkflowApprovalBoundaryConsent` checks that a
routine action raises no challenge. The authored class remains a review
judgment.

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
and [CD-0221 D4](CD-0221-accepted-law-cites-the-checked-revision.md#d4-keep-amendments-and-proof-together)
expect. P3 permits adequate guidance for an ordinary law; D2's ban
on prose-only mechanisms governs the conflict-rule table. The cases' source
labels and recorded sizes are fictional inputs, not real operation receipts or
an assessment of the delivery's size. Carrier tests do not prove those judgments.
The delivery's recorded independent review supplies the application evidence;
the method adds no runtime gate or ad-hoc-route enforcement.

D3's live recorder retains readable current settlement evidence.
`internal/agent.TestConflictObservationHistoryRoundTrip` records then reads
the full rule, effective size and outcome through the existing history read,
including retry identity and paging. `internal/agent.TestConflictObservationHistoryRespectsScope`
checks the reader's Product boundary. `internal/store.TestConflictApplicationCurrentRecordSurvivesSizeAndReopen`
checks that later sizing and reopening preserve the recorded assessment.
`internal/store.TestConflictApplicationCurrentRecordMigrationDoesNotRestoreReceipts`
checks that old digest-only receipts are not reconstructed.
`internal/store.TestConflictApplicationCurrentRecordGuardsAndCleanup` and
`internal/store.TestConflictApplicationCurrentRecordRemovedWithWork` check
removal with the work record. This is spot-check evidence under CD-0242 D2,
not history, replay, approval or proof that the settlement followed its rule.
The review method records applicable-rule findings through existing report
carriers; its projection tests prove shipping, not semantic rule application.

`python3 scripts/check-law-coverage.py` validates the outstanding coverage
pointer, not the remaining enforcement and proof assigned below:

| Clauses | Open owner and limit of delivered proof |
|---|---|
| D3's letter-versus-intent conflict | [CON-1097](https://linear.app/sharper-flow/issue/CON-1097). The law-side review cases exercise raised and silently applied conflicts. They do not prove that agents raise a conflict they never report. |
| R2, P4, P6, A2 rows 2 and 4 | [CON-1098](https://linear.app/sharper-flow/issue/CON-1098). `internal/agent.TestResolveOverlapDependsOnRecordsDirectedSequencingWithoutConsent` proves directed work-item sequencing, not selection of the shared-Domain step or release when the other change completes it. Shared stages do not by themselves prove boundary-change or shared-part-variant handling. |
| P7, P9, P11 | [CON-1099](https://linear.app/sharper-flow/issue/CON-1099). Shared parts alone do not prove second-occurrence handling, in-place part repair, or the rule's follow-up outcome. |
| D4 and D5; CD-0243 runtime alignment | [CON-1100](https://linear.app/sharper-flow/issue/CON-1100). Recorded review does not establish prevention of external data loss, automatic six-class inference, or enforcement on the ad-hoc route. Contract-time consent and worktree waivers do not establish informed approval of a specific loss. Classifications, carrier tests and the selected synthetic reviews do not prove whole-record runtime alignment. |
| U, A2 row 7 | [CON-1343](https://linear.app/sharper-flow/issue/CON-1343). Contract-time `ui_ux` consent does not prove approval of a development preview of the final candidate, or renewed approval of a changed experience, as CD-0242 D9 requires. |
| R1, A2 rows 1, 5 and 6; D6 | [CON-1101](https://linear.app/sharper-flow/issue/CON-1101). Naming follow-up labels in the table does not enforce labels, raising-change links, the Linear view, or closure tracking. CD-0242 D5 and D9 own repairing review and final-candidate proof, not old size-specific review counts. |
| A2 row 8 | [CON-1344](https://linear.app/sharper-flow/issue/CON-1344). Same-item resizing does not prove adding required stages while retaining completed work. CD-0229's Verification also retains the separate small-work proof and timing owners. |

Passing `python3 scripts/check-law-coverage.py` does not establish whole-record
coverage. The surviving outcomes still need executable proof or an explicit
owning law disposition. CD-0242 D4–D7 replaces the old
execution machinery; D6 retires production-line caps and mandatory splitting.
CD-0243 replaces seriousness, Domain count, crucial size, and unclassified
approval points as independent consent triggers. Neither replacement makes
an unproved retained rule satisfied or permits bypassing a current gate.

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

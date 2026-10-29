# Concord Priorities

> **Status:** Public constitutional planning authority.\
> **Codename:** Concord (concordance: bringing scattered work into one view).\
> **Created:** 2026-07-25, from predecessor-state analysis and product-design work.\
> **Authority:** The public [`Sharper-Flow/concord`](https://github.com/Sharper-Flow/concord) repository is the authority for this document and its accepted companions. This file ranks first among Concord's law. It is the **sole canonical source** for the operating envelope, the constraints, the conflict rules (Appendix A), and the standing law that serves the operator's goals in [`goals.md`](./goals.md). `goals.md` is guidance, not law. A decision record it cites stays the authority for what that record decides. Companion documents link here; they do not redefine them.

---

## Purpose

[`goals.md`](./goals.md) records Concord's purpose, the operator's goals, and
their measures as guidance; it is not law. This file holds the rules that serve
those goals: the constraints, the conflict rules that decide between goals
(Appendix A), and the standing law.

Precedence:

- A companion document contradicts this file: this file wins.
- This file conflicts with an accepted decision record: this file sets the
  direction, and the conflicting record is amended under [`CD-0036`](./decisions/CD-0036-breaking-law-cutovers.md) D2
  (in place when compatible, by a superseding record when breaking) before
  work relies on it.

### Agent boost and automatic bookkeeping

Concord must supply the current work context, required files, and approved
bounded job when an agent starts or resumes work and when that context changes.
The agent needs no separate call to fetch them.

Concord records every fact it can derive from the worktree, Git, CI, its store,
or the planning link. An agent never transcribes those facts: `HEAD`, base,
ancestry, merge and CI results, delivery, lifecycle after merge, request identity,
and expected versions are derived at their owning boundary. A missing fact that
Concord can derive is derived at the point of use, not refused back to the agent
as bookkeeping.

An agent call to Concord is for a decision, such as an approval, a verdict, or a
choice between routes. Other coordination writes happen as side effects of work
already performed, with **zero extra bookkeeping calls**. Every refusal names
the exact supported next call, or Concord takes that call itself.

Automatic bookkeeping preserves required approvals, expected-version and
idempotency checks ([TS5](./agent-call-context-contract.md) §§5–7), and
evidence binding to its immutable subject
([CD-0008](./decisions/CD-0008-concord-mechanism-hardening.md) D2). A derived
expected version is the version the agent's work read, never the version
current at the write. Bookkeeping never silently rebases a mutation onto newer
state, invents missing evidence, or takes over a native system's external
effects.

Concord records workflow calls, extra bookkeeping calls, and coordinator minutes
per work item, together with the observation window. Call counts include refused
and retried calls; unavailable measurements stay unknown, not zero. Targets for
total workflow calls and coordinator minutes stay open, with no numeric ceiling
or admission gate until the operator sets one.

---

## Operating envelope

Concord is permanently scoped to one operator per installation, while remaining
optimized for many concurrent agents.

- **Many concurrent agent sessions.** Many OpenCode sessions run in parallel against the same local state.
- **One operator per installation.** Another human may run an independent Concord installation against shared git knowledge, but live workflow memory is not shared.
- **Agents are first-class machine participants.** Agents read, write, and execute alongside the operator; they are not second-class consumers of a human-oriented GUI.
- **Web UI is optional.** A browser-based view may appear later, but it is not the design center.
- **No team-server ambition.** Shared assignments, boards, identity, permissions, and multi-human live workflow coordination are non-goals. Git shares durable Product knowledge between independent installations.

If a proposed capability conflicts with this envelope, it does not belong in
Concord.

---

## Constraints

- Single machine.
- OpenCode is the agent host.
- Data safety follows S and P10. Modifying production application data and risking irreversible
  customer-data loss require operator approval. The operator may explicitly approve an explained
  irreversible loss without a verified recovery route. Lack of recovery is not a separate approval
  class or absolute stop outside the six classes (CD-0243 D2, D5).
- Must not change, even where it slows agents down: "Not allowing slop or dead code to continue existing or go
  unnoticed". Bloat a change did not make still gets a follow-up item (rule R1), and those items must be closed
  (goal E in `goals.md`).

---

## Ownership-aligned placement

See [`capability-placement.md`](./capability-placement.md).

---

## Standing law

The order of the sections below carries no rank. The labels R3, P12, and U name
conflict rules in Appendix A.
[`CD-0230`](./decisions/CD-0230-the-conflict-rules-are-one-checked-table-and-an-agent-changes-law-that-is-not-serious.md)
owns their enforcement, recorded application, and law-change authority.

### Product law and Domain architecture (goal A)

Concord keeps a pruned, versioned, architecture-bound statement of what each
Product must keep true. Accepted specifications and decisions are law. Code,
workflows, and evidence serve that law. Agents enact and change law under R3.

- Durable Product knowledge is navigated through **Product → Domain**, with
  current law, active work, evidence, decisions, and resources together.
- Every current law record has one canonical Domain home; superseded law remains
  historical but leaves the default current view.
- A clean Git merge, passing tests, or heuristic similarity score cannot prove
  Product concordance.
- Semantic analysis may suggest duplicate or conflicting law; it never owns
  persistence, supersession, or blocking.
- Every change keeps a footprint record at every size, and a tool fills it
  ([`goals.md`](./goals.md) §2, goal A). The checked package map and the
  footprint read that joins it to the law index are owned by
  [`CD-0222`](./decisions/CD-0222-architecture-bound-product-law-with-package-boundary-code-ownership.md)
  D2.

See [`decisions/CD-0222-architecture-bound-product-law-with-package-boundary-code-ownership.md`](./decisions/CD-0222-architecture-bound-product-law-with-package-boundary-code-ownership.md),
[`specs-as-laws.md`](./specs-as-laws.md), and
[`product-data-model.md`](./product-data-model.md).

### Data safety (constraint)

Concord-owned state is trustworthy, durable, and evolvable without destructive
migration or manual repair. The data model can grow, shrink, and reshape
without corrupting in-flight work or historical records.

- Agent-facing writes queue behind one writer and are admitted within the accepted bound: no escaped busy failure, no lost or duplicated effect, no retry storm. Reads are lock-free. See CD-0045.
- The store keeps current state and finite verified snapshots, and each correction writes the corrected current rows ([CD-0242 D2](decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)).
- Acknowledged consequential operations — approvals, workflow dispatch
  and terminal transitions, cross-authority step claims, and producer evidence —
  are durable across power loss and OS crash (CD-0050); ordinary writes are
  durable through the last consequential boundary and never corrupt the store.
- Workflow and schema changes must be safe for in-flight work.
- **SQLite sole authority** for operational state (CD-0002 I1), subject to CD-0011's accepted
  falsifiers. It writes current rows in one transaction, checked by an expected version and recorded by its idempotency key ([CD-0242 D2](decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)).

### Quality (goal E)

Work is done correctly before it is declared done. Quality is defined by
attributes, not by a fixed gate count.

**Concord's quality attributes are:**
- Intent fidelity — the delivered outcome matches the stated intent.
- End-to-end traceability — from need to law to evidence to artifact.
- Evidence-backed completion — completion claims are supported by recorded proof.
- Independent challenge — a party other than the author reviews every change.
  A reviewer's own corrective edits are verified, not reviewed again.
- No silent drift — deviations from spec, architecture binding, or plan are surfaced, not papered over.
- Required-obligation blocking — unmet blocking obligations halt the work.
- Human authority at consequence boundaries — the operator decides the six classes in [CD-0243 D2](decisions/CD-0243-four-roles-and-six-operator-approval-classes.md#d2-the-operator-approves-six-classes-of-decision), including law changes within those classes (U, R3, P12). Seriousness, Domain count, and crucial size do not independently require approval.
- Durable proof — evidence is retained and inspectable after the work is finished.
- Proportional rigor — the decision model (goal B) sets each change's minimum steps, proof, and testing and its mandatory stages, and no agent may waive a mandatory stage or an operator approval ([CD-0242 D4](decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d4-families-compose-shared-stages-and-rules-set-the-minimum)).

**What scales proportional rigor.** One input to the decision model is the
**declared lifecycle stage** of the thing being worked on (`maturity` and
user-declared `audience_commitment`, at Product, Domain, repo, and resource
level; see [`product-data-model.md`](./product-data-model.md) §8). No stage is
an evidence exemption. Independent maturity and audience obligations combine;
Product-local policy may strengthen but never weaken Concord's global floor.

See [`design-constraints.md`](./design-constraints.md) §10 and
[`workflows.md`](./workflows.md).

### Visibility and continuity

The operator and agents can see the current state of the Product, its Domain
architecture, and its portfolio without blind spots, and they can trust that the
view is fresh enough to act on.

- Reads are bounded, fast, and scoped to a Product.
- Staleness is reviewed before action, with execution blocked according to risk.
- Continuity is preserved across agent sessions and worktree switches.
- Specs, decisions, runbooks, and durable workflow documents remain browsable
  through their Domain home; knowledge does not live only in chat history or a
  flat workflow list.
- **Scope boundary (CD-0033):** "without blind spots" covers what enters Product
  truth, not the session channel that directs agent labor. Operator direction
  given out-of-band is legitimate and invisible to Concord by design; when it
  changes a required end-state, it binds through contract supersession — the
  single write path, operator-approved only when a CD-0243 D2 class applies —
  and completion's premise confirmation
  names the recorded premise, so an unsuperseded redirect surfaces as a premise
  the operator will not recognize. The blind spot that remains is documented,
  not accidental.

See [`design-constraints.md`](./design-constraints.md) §2, §5 for the staleness
and read-path constraints.

### Planning and coordination

Work is planned and coordinated through the Product's Domain architecture, not
just inside individual repositories, initiatives, or changes. Dependencies,
sequences, architectural overlap, and resource contention are visible before
they become blockers or contradictory Product truth.

- Product is the primary planning scope; Domain is the primary architectural lane.
- Cross-project and cross-Domain dependencies are first-class.
- The operator sees what is ready, what is blocked, what overlaps, and what is next.
- Agents operate inside a Product and Domain context rather than hopping between disconnected projects.
- Business grouping lives in Linear under CD-0213 and owns no architectural or law authority.
- See [`CD-0230`](./decisions/CD-0230-the-conflict-rules-are-one-checked-table-and-an-agent-changes-law-that-is-not-serious.md)
  for applying and recording the R2 sequencing rule in
  Appendix A.

### Composable workflows (goal C)

A workflow is a combination of shared parts (goal C in
[`goals.md`](./goals.md) §2).
Families compose shared stages, and the decision model sets each execution's mandatory stages ([CD-0242 D4](decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d4-families-compose-shared-stages-and-rules-set-the-minimum)). Parts define their steps once, and a definition composes parts (CD-0229 D1, D2).

- Concord owns a full workflow coordination engine: progression, branching,
  retries, evidence, recovery, and the accepted composition model. Native
  systems retain external effects.
- Parts are code-defined and versioned.
- New parts and combinations can be added without breaking in-flight work.
- Each workflow declares its artifacts, completion evidence, and whether it
  changes Product truth.

See [`workflows.md`](./workflows.md).

### How Concord's own development runs during the rebuild

- New assignments to develop Concord use Concord agents and Concord's workflow.
- Coordinators delegate through native Task, obtain independent Review, and
  record subject-bound outcomes under CD-0242 D5/D8. Concord does not launch
  or attest subagents.
- Work already underway outside the workflow finishes through its existing
  authorized route. It acquires no Concord workflow authority or evidence
  retroactively.
- An authorized repair of a Concord defect that blocks safe workflow recording
  or execution may proceed outside the workflow under
  [CD-0122 D2](./decisions/CD-0122-concord-workflow-scope-and-defect-repair.md#d2-defect-repair-has-an-outside-work-application).
- Delivery timings remain diagnostic; they do not gate workflow adoption.

---

## Companion documents

[`README.md`](./README.md) names the companion documents and what each owns.

---

## Appendix A. Conflict rules (approved by the operator, 2026-10-16)
The operator: "We need to have more matrix thinking. For each conflict, we should have a defined rule." and "I
was thinking a cross-tabulation chart so we have an answer for each combo". The operator asked for proposals
("Fill this in to the best of your ability with rule proposals as well, and I will review") and approved them.

### A1. Goal-by-goal chart
Axes: Law = product law. Arch = domain architecture. Speed = fast, right-sized delivery. Quality = quality and
bloat removal. Safety = data safety. You = the six operator approval classes in CD-0243 D2. Parts =
reusable workflow parts and programmed tools. Each pair appears once.

|  | Arch | Speed | Quality | Safety | You | Parts |
|---|---|---|---|---|---|---|
| **Law** | P1 | R3 | P2 | S | R3 | P3 |
| **Arch** |  | R2 | P4 | S | P5 | P6 |
| **Speed** |  |  | R1 | S | U | P7 |
| **Quality** |  |  |  | S | P8 | P9 |
| **Safety** |  |  |  |  | P10 | S |
| **You** |  |  |  |  |  | P11 |

Rules the operator stated:
- R1 (Quality vs Speed): "if the bloat was made by the fix then remove, otherwise create a follow up tech debt
  item".
- R2 (Arch vs Speed): The coordinator identifies the work step that changes the shared Domain and records
  which change waits. The coordinator may pause the whole waiting change during that shared step, and
  explicitly releases it when the other change completes that step. Other changes and work before or after
  that shared step may continue independently.
- R3 (Law vs Speed, Law vs You): change the law and record why; ask the operator only when P12 requires it (CD-0243).

From the operator's earlier answers:
- S: establish data-loss risk and recovery before acting; apply P10 and CD-0243 D5.
- U: a new or changed UI/UX design or interaction waits for operator review and acceptance.
  Minor corrections that introduce no new design or interaction need no fresh approval (CD-0243 D2).

Approved proposals (situation, then rule):
- P1 Law vs Arch. A law's home domain no longer owns the behavior the law governs. The law states what must be
  true; the architecture states where it lives. Fix whichever is wrong, and move the law to the domain that
  owns the behavior. Apply R3's approval test.
- P2 Law vs Quality. A current law requires code the agent judges bloat. Never delete what a current law requires.
  Change or retire the law under R3 in the change that removes the code, or in an earlier change. Never merge
  the removal while the law still requires the code.
- P3 Law vs Parts. A law needs enforcement that no reusable part provides. The change that adds or changes the
  law names how the law is upheld, using the cheapest adequate means: an existing check, a focused test,
  review, or clear guidance. Build a new programmed check only when none of these catches a violation that
  matters. Never build a runtime system to enforce a rule that one of these already upholds.
- P4 Arch vs Quality. The cleanest code crosses a domain boundary, or a boundary forces duplicate code. Keep the
  boundary inside the change. An architect step may move the boundary. With no architect step, file a
  follow-up item.
- P5 Arch vs You. An architecture choice also changes what the operator sees or decides. Agents own
  architecture. Consult the operator only when the choice falls within CD-0243 D2's six approval classes.
- P6 Arch vs Parts. A domain needs a variant of a shared part. Extend the shared part with a parameter. Never
  copy it into a domain-local version. If the part cannot fit without a special case, file a follow-up item to
  redesign the part.
- P7 Speed vs Parts. A one-off script or prose instruction ships today; a reusable part or tool takes longer.
  The first time a need appears, ship the one-off and file a follow-up item for the part or tool. The second
  time, build the part or tool before shipping. Never add a new prose instruction where a tool can do the job.
- P8 Quality vs You. The operator asks for a behavior the agent judges adds complexity. The operator's decision
  wins. The agent states the quality cost once, with evidence, before building it.
- P9 Quality vs Parts. A shared part has bloat or fits the change badly. Use the part and fix the part itself,
  not a local workaround. If the fix is larger than the change's size, file a follow-up item on the part.
- P10 Safety vs You. The agent explains permanent-loss risk and any missing recovery route before asking
  for required approval. The operator may explicitly approve irreversible loss. Without that approval,
  do not risk irreversible customer-data loss. Outside the six classes, lack of recovery alone is not
  an operator-approval requirement or an absolute stop (CD-0243 D2, D5).
- P11 You vs Parts. The operator wants a behavior that no reusable part supports. The operator decides what;
  the agent builds it as a new part or as a parameter of an existing part, not as a one-off.
- P12 Approval test for law changes under R3. Apply CD-0243 D2's six approval classes. Seriousness,
  Domain count, and crucial size do not independently require approval. Adding, removing, or reclassifying
  an operator approval always requires the operator's approval of its exact wording.

### A2. Conflict situations by change size
Columns are the sizes the decision model gives a change. "Same" means the rule does not change with size.

| # | Conflict | Small | Larger | Crucial | Applied by |
|---|---|---|---|---|---|
| 1 | A fix meets bloat | R1 | Same | Same | Agent |
| 2 | Two agents change one domain at once | R2 | Same | Same | Coordinator |
| 3 | A change breaks a product law | R3, with P12 for approval | Same | Same | Agent; operator when P12 requires approval |
| 4 | A change needs a new domain or moves a domain boundary | Resize the task (row 8); apply P12 | The architect step decides; apply P12 | Same | Agent; operator when P12 requires approval |
| 5 | Review finds problems | Review fixes problems inside the change's scope and verifies its fixes; file follow-up items for the rest | Same | Same; the coordinator runs more review until review reports no high or medium problem | Review fixes; the coordinator ends the loop |
| 6 | A check fails for a cause the change did not make | Do not fix it in the current change. File a bug item. If it blocks shipping, the coordinator schedules the bug first | Same | Same | Coordinator |
| 7 | A change affects UI/UX | U | Same | Same | Operator for new design or interaction; agent for minor corrections under U |
| 8 | A task proves bigger than its workflow size | Run the decision model again, add the newly required steps, and continue without redoing completed work | Same | Same | Decision model; coordinator |
| 9 | Data safety is at risk | S; P10 | Same | Same | Agent assesses risk; operator when CD-0243 D2 requires approval |

## Appendix B. Open conflicts with current Concord text
Not a full sweep. In each case this file's rule wins, and the conflicting text is amended. Row numbers are stable identifiers.

| # | Current text | Conflicting decision |
|---|---|---|
| 3 | CD-0006 D5: "Human approval is always required to enact law changes or governed scope cuts." CD-0222 D4: "modified/added law is operator-mandated". | Agents change law with a recorded reason; approval follows R3 and P12's six-class test. |
| 4 | CD-0222 D6: "only the operator may approve compatibility, merger, supersession, or a sequence that changes Product scope." | The coordinator sequences the overlapping step (R2). |
| 10 | Prose instruction files under `.concord/instructions/` given to every agent session. | Programmed tools wherever a tool can do the job (goal D, P7). |

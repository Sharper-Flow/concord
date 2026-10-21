# Concord Priorities

> **Status:** Public constitutional planning authority.\
> **Codename:** Concord (concordance: bringing scattered work into one view).\
> **Created:** 2026-07-25, from predecessor-state analysis and product-design work.\
> **Authority:** The public [`Sharper-Flow/concord`](https://github.com/Sharper-Flow/concord) repository is the authority for this document and its accepted companions. [`goals.md`](./goals.md) ranks first. This file is the **sole canonical source** for the operating envelope and states the standing law that serves the goals. A decision record it cites stays the authority for what that record decides. Companion documents link here; they do not redefine them.

---

## Purpose

[`goals.md`](./goals.md) is the single home of Concord's purpose, its goals and
their measures (§2), its constraints (§4), and the conflict rules that decide
between goals (Appendix A). This file does not repeat them.

Precedence:

- A companion document contradicts Concord's direction:
  [`goals.md`](./goals.md) wins, then this file.
- [`goals.md`](./goals.md) conflicts with an accepted decision record:
  [`goals.md`](./goals.md) sets the direction, and the conflicting record is
  amended under [`CD-0036`](./decisions/CD-0036-breaking-law-cutovers.md) D2
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

## Ownership-aligned placement

See [`capability-placement.md`](./capability-placement.md).

---

## Standing law

The order of the sections below carries no rank. The labels R3, P12, and U name
conflict rules in [`goals.md`](./goals.md) Appendix A.
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
- [CD-0242 D2](decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history) governs: the store keeps current state and finite verified snapshots, not an event log or replay, and each correction writes the corrected current rows.
- Acknowledged consequential operations — approvals, workflow dispatch
  and terminal transitions, cross-authority step claims, and worker evidence —
  are durable across power loss and OS crash (CD-0050); ordinary writes are
  durable through the last consequential boundary and never corrupt the store.
- Workflow and schema changes must be safe for in-flight work.
- **SQLite sole authority** for operational state (CD-0002 I1), subject to CD-0011's accepted
  falsifiers. [CD-0242 D2](decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)
  governs each mutation: it writes current rows in one transaction, checked by an expected version
  and recorded by its idempotency key.

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
- Human authority at consequence boundaries — the operator decides UI/UX, product, and serious law changes (U, R3, P12).
- Durable proof — evidence is retained and inspectable after the work is finished.
- Proportional rigor — the decision model (goal B) sets each change's minimum steps, proof, and testing; [CD-0242 D4](decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d4-families-compose-shared-stages-and-rules-set-the-minimum) governs its mandatory stages, and no agent may waive a mandatory stage or an operator approval.

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
  single write path, operator-approved — and completion's premise confirmation
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
  [`goals.md`](./goals.md) Appendix A.

### Composable workflows (goal C)

Goal C in [`goals.md`](./goals.md) §2 defines a workflow as a combination of
shared parts.
[CD-0242 D4](decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d4-families-compose-shared-stages-and-rules-set-the-minimum)
governs: families compose shared stages, and the decision model sets each execution's mandatory
stages. Parts define their steps once, and a definition composes parts (CD-0229 D1, D2).

- Concord owns a full workflow coordination engine: progression, branching,
  retries, evidence, recovery, and the accepted composition model. Native
  systems retain external effects.
- Parts are code-defined and versioned.
- New parts and combinations can be added without breaking in-flight work.
- Each workflow declares its artifacts, completion evidence, and whether it
  changes Product truth.

See [`workflows.md`](./workflows.md).

---

## Companion documents

[`README.md`](./README.md) names the companion documents and what each owns.

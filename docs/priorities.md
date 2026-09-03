# Concord Priorities

> **Status:** Public constitutional planning authority, amended by CD-0041; implementation remains staged by accepted readiness rules.\
> **Codename:** Concord (concordance: bringing scattered work into one view).\
> **Created:** 2026-07-25, from predecessor-state analysis and product-design work.\
> **Authority:** After bootstrap, the public `Sharper-Flow/concord` repository is the authority for this document and its accepted companions. This file is the **sole canonical source** for the six ranked priorities, the operating envelope, and the predecessor relationship. Companion documents link here; they do not redefine the order.

---

## Purpose

Concord is a product-development coordination system for **one operator and many local AI agents working on one machine**. Its priorities, user boundary, interface direction, and quality governance are recorded here so that later design, inventory, and rollout documents can reference one authoritative statement.

Use this document when:

- A design tradeoff needs to be resolved — return to the priority order.
- A predecessor capability is being evaluated for Concord impact — check the operating envelope and the public predecessor lessons.
- A companion document appears to contradict Concord's direction — this file wins.

---

## Operating envelope

Concord is permanently scoped to one operator per installation, while remaining optimized for many concurrent agents.

- **One operator per installation.** Another human may run an independent Concord installation against shared git knowledge, but live workflow memory is not shared.
- **Many concurrent OpenCode TUIs.** Multiple agents run in parallel against the same local state.
- **Agents are first-class machine participants.** Agents read, write, and execute alongside the operator; they are not second-class consumers of a human-oriented GUI.
- **Primary operator surface is a Product-first terminal launcher.** The operator starts, navigates, and acts from a launcher that is organized by Product, not by project or repository.
- **Web UI is optional.** A browser-based view may appear later, but it is not the design center.
- **No team-server ambition.** Shared assignments, boards, identity, permissions, and multi-human live workflow coordination are non-goals. Git shares durable Product knowledge between independent installations.

If a proposed capability conflicts with this envelope, it does not belong in Concord's first usable form.

---

## Relationship to Advance

Concord is a clean rewrite of Advance (D2, 2026-08-05); see [`README.md`](./README.md).

- **Architecture is derived from Concord's priorities.** Concord's shape comes from the six priorities below, not from copying Advance's commands, gates, or workflow steps.
- **Advance capability coverage is a release floor.** Concord must be able to cover the same operational territory before claiming replacement readiness, but it does not have to mirror Advance's shape.
- **Concord is Advance's full successor.** No partial slice is usable or primary. Migration and correction are demand-driven operations with local scope and safeguards; no global migration sequence is required.
- **Advance is a source of evidence, not a design mandate or a blocker.** Where Advance already produces reliable, agent-native state, Concord may consume it as evidence. Where Advance is unstable, that instability is **design input** (an anti-pattern to structurally prevent), not a gate Concord waits on.
- **No command-for-command or seven-gate clone.** Concord may use a 7-gate implementation workflow where it fits, but workflow plurality is governed by Priority 6. The seven gates are not a universal requirement.
- **Advance stabilization work is lesson evidence, not a prerequisite or a blueprint.** The public predecessor lessons document reachable issue-backed failure patterns; they are neither converted into Concord architecture decisions nor required to ship before Concord begins.

---

## Ownership-aligned placement

See [`capability-placement.md`](./capability-placement.md).

---

## Ranked priorities

The order is deliberate. Lower-numbered priorities are more foundational; a higher-numbered priority must not compromise a lower-numbered one.

### 1. Product law and architectural concordance

**What it means.** Concord's primary deliverable is a pruned, versioned,
architecture-bound statement of what each Product must keep true. Accepted
specifications and decisions are human-enacted law. Code, workflows,
initiatives, and evidence serve that law.

**Why it is first.** Many agents can produce individually plausible changes that
merge cleanly and still contradict one another. Concord succeeds only when all
work acts through one legible Product architecture and one current body of
accepted law.

**Key implications.**
- Durable Product knowledge is navigated through **Product → Domain**, with
  current law, active work, evidence, decisions, and resources together.
- Every current law record has one canonical Domain home; superseded law remains
  historical but leaves the default current view.
- Product-changing work declares its architectural home, affected Domains, exact
  governing law revisions, authorized law changes, and verification obligations.
- Concurrent work that overlaps one Domain requires an explicit, version-pinned
  compatibility, sequencing, merger, supersession, or terminal resolution before
  both items hold execution authority.
- A clean Git merge, passing tests, or heuristic similarity score cannot prove
  Product concordance.
- Semantic analysis may suggest duplicate or conflicting law for operator review;
  it never owns persistence, supersession, or blocking.

See [`decisions/CD-0041-architecture-bound-product-law.md`](./decisions/CD-0041-architecture-bound-product-law.md),
[`specs-as-laws.md`](./specs-as-laws.md), and
[`product-data-model.md`](./product-data-model.md).

### 2. Data governance, reliability, and safe evolution

**What it means.** Concord-owned state is trustworthy, durable, and evolvable
without destructive migration or manual repair. The data model can grow, shrink,
and reshape without corrupting in-flight work or historical records.

**Why it is second.** Product law cannot govern work through an unsafe
substrate. Data safety is the first implementation obligation serving the
Product-law model.

**Key implications.**
- Agent-facing writes queue behind one writer and are admitted within the accepted bound: no escaped busy failure, no lost or duplicated effect, no retry storm. Reads are lock-free. See CD-0045.
- No hand-repair of history; corrections are appended, not destructive.
- Acknowledged consequential operations — approvals, workflow dispatch
  and terminal transitions, cross-authority step claims, and worker evidence —
  are durable across power loss and OS crash (CD-0050); ordinary writes are
  durable through the last consequential boundary and never corrupt the store.
- Workflow and schema changes must be safe for in-flight work.
- Storage and IO are a founding design decision, not an afterthought. State
  authority is **SQLite sole authority** per CD-0002 (invariants I1–I6) and
  remains subject to CD-0011's accepted falsifiers.

### 3. Quality governance

**What it means.** Work is done correctly before it is declared done. Quality is
defined by attributes, not by a fixed gate count.

**Concord's quality attributes are:**
- Intent fidelity — the delivered outcome matches the stated intent.
- End-to-end traceability — from need to law to evidence to artifact.
- Evidence-backed completion — completion claims are supported by recorded proof.
- Independent challenge — high-risk work is reviewed by a party other than the author.
- No silent drift — deviations from spec, architecture binding, or plan are surfaced, not papered over.
- Required-obligation blocking — unmet blocking obligations halt the work.
- Human authority at consequence boundaries — the operator decides when tradeoffs affect safety or scope.
- Durable proof — evidence is retained and inspectable after the work is finished.
- Proportional rigor — the depth of process matches the risk of the work.

This is **not** a mandate to use Advance's seven-gate lifecycle for every work
kind. Different workflow types may use different gates, but they must satisfy
these attributes.

**What scales proportional rigor.** The last attribute — proportional rigor —
needs an input, and that input is the **declared lifecycle stage** of the thing
being worked on (`maturity` and user-declared `audience_commitment`, at Product,
Domain, repo, and resource level; see [`product-data-model.md`](./product-data-model.md)
§8). Stage governs the **evidence bar** — what depth of testing is expected and
what proof of testing is required before work is called done — **not** the
workflow shape. A prototype does not skip gates; it satisfies them with
proportionate evidence. No stage is an evidence exemption. Independent
maturity/audience obligations combine; Product-local policy may strengthen but
never weaken Concord's global floor.

See [`design-constraints.md`](./design-constraints.md) §10 and
[`workflows.md`](./workflows.md).

### 4. Visibility and continuity

**What it means.** The operator and agents can see the current state of the
Product, its Domain architecture, and its portfolio without blind spots, and they
can trust that the view is fresh enough to act on.

**Why it matters.** The original pain was portfolio blindness: research, ops,
implementation, and infrastructure work were scattered across tools and
repositories. Concord must make the whole Product and its governing law visible
in one place.

**Key implications.**
- Reads are bounded, fast, and scoped to a Product.
- Staleness is reviewed before action, with execution blocked according to risk.
- Continuity is preserved across agent sessions and worktree switches.
- The terminal launcher is the primary visibility surface.
- Specs, decisions, runbooks, and durable workflow documents remain browsable
  through their Domain home; knowledge does not live only in chat history or a
  flat workflow list.
- **Scope boundary (CD-0031):** "without blind spots" covers what enters Product
  truth, not the session channel that directs agent labor. Operator direction
  given out-of-band is legitimate and invisible to Concord by design; when it
  changes a required end-state, it binds through contract supersession — the
  single write path, operator-approved — and completion's premise confirmation
  names the recorded premise, so an unsuperseded redirect surfaces as a premise
  the operator will not recognize. The blind spot that remains is documented,
  not accidental.

See [`design-constraints.md`](./design-constraints.md) §2, §5 for the staleness
and read-path constraints.

### 5. Planning and coordination

**What it means.** Work is planned and coordinated through the Product's Domain
architecture, not just inside individual repositories, initiatives, or changes.
Dependencies, sequences, architectural overlap, and resource contention are
visible before they become blockers or contradictory Product truth.

**Key implications.**
- Product is the primary planning scope; Domain is the primary architectural lane.
- Cross-project and cross-Domain dependencies are first-class.
- The operator sees what is ready, what is blocked, what overlaps, and what is next.
- Agents operate inside a Product and Domain context rather than hopping between disconnected projects.
- Initiative is an optional business/outcome overlay and owns no architectural or law authority.

### 6. Workflow versatility

**What it means.** Different kinds of work get different purpose-built workflow
shapes. Research, investigation, static analysis, ops runbooks, and spec-driven
implementation each have a workflow type that fits them, rather than being
forced into a single 7-gate shape.

**Key implications.**
- Concord owns a full workflow coordination engine: progression, branching,
  retries, evidence, recovery, and the accepted composition model. Native systems
  retain external effects.
- Workflow types are code-defined, versioned, purpose-built built-ins plus one generic type for true one-offs.
- The 7-gate implementation change remains one type among many.
- New types can be added without breaking existing in-flight work.
- Each workflow type declares its own artifacts, steps, completion evidence, and whether it changes Product truth.
- Workflow shape never bypasses Priority 1's architecture binding.

See [`workflows.md`](./workflows.md).

---

## First-usable floor

Concord's first usable form is a **complete, replacement-ready coordination surface** for one operator and many agents on one machine, anchored to a **Product-first terminal launcher**. It must cover the full operational scope that Advance currently provides for this operator (Product-scoped planning, visibility, implementation changes, research/investigation tracking, ops runbooks, and durable product knowledge) while materially improving every one of the six priorities.

Incremental design, build, replay, and evaluation are allowed, but a partial slice cannot be called usable or replacement-ready. Replacement readiness is an evidence claim, not migration activity.

Distance from the floor is recorded in [`floor-readiness.md`](./floor-readiness.md) and its validated manifest, which decomposes each condition below into items whose state is checked in CI. That record is authorizing for *where Concord stands*; this section remains authorizing for *what the floor is*.

The six numbered conditions below define the **usability floor — the bar Concord must clear for one operator and many agents to do real work on this machine**. Replacement readiness is a higher bar: it additionally requires the release, install, privacy, and Linux amd64 release-evidence bar owned by [`rollout-plan.md`](./rollout-plan.md) §3. Both bars are decomposed in the floor manifest, and the manifest's `source` for each condition names the document and section that bears it.

It becomes usable when:

1. The operator can see, plan, and act across the full Product scope from the launcher through Product → Domain context. Per CD-0021, "across the full Product scope" means every Product is reachable from the launcher, not that a result set spans Products. Per the same decision, "plan" is satisfied by reaching work and opening a session that authors it. The launcher is read-only by C18, so work creation keeps one write authority.
2. Agents can read, write, and execute within that Product and Domain context through tools.
3. Every supported work kind (implementation, research, ops, etc.) has a defined workflow type and completion evidence.
4. Required authority/freshness is explicit, CD-0006 R3's accepted cross-workflow impact policy is enforced, and CD-0041's architecture-overlap policy prevents unresolved concurrent Product-changing work from holding execution authority.
5. Concord-owned data and workflow boundaries satisfy Priority 2 (bounded writer admission, no hand-repair, safe evolution).
6. The Advance surfaces that Concord consumes are stable enough to serve as evidence, and Concord can replace them without losing operational coverage.

---

## Product-level decision tracker

The current questions have resolved directions. Future migration or correction
operations need their own demand, local scope, and evidence.

| Question | Why it matters | Current direction, if any |
|---|---|---|
| **Storage model for Concord-owned state** | Foundation for Priority 2 and the derived projections serving Priority 1. | **Resolved** — CD-0002 + accepted PM2/PM3 (2026-08-05) establish one global local SQLite DB, append-only `domain_events` authority, and explicit typed projections updated in one transaction. Accepted PM6/PM7 (2026-08-05/06) add git-proof-backed durable notes plus bounded lazy projection pruning without event deletion. CD-0041 retains this authority under CD-0011's falsifiers. |
| **Workflow evolution semantics** | Affects Priority 2, 3, and 6. | **Resolved by CD-0006:** Concord owns full coordination; purpose-built types are code-defined and versioned, with one generic one-off type. Work composes through forward-linked successors with independent authority/recovery. |
| **Read-path implementation language** | Affects Priority 4. | **Go** — the Concord core is Go from day one. See [`core-architecture.md`](./core-architecture.md). Latency target (sub-100ms) remains; language is no longer conditional. |
| **Where the Product entity lives** | Affects architecture and storage. | **Resolved by CD-0007:** standalone `Sharper-Flow/concord` Product/repository, Go module `github.com/sharper-flow/concord`; implementation authority moves there after the audited public bootstrap. |
| **Product row fields** | Affects what the operator sees in the launcher. | **Resolved by C14 (2026-08-06):** identity, declared stage, reliance, typed action counts, and one deterministic focus item. See [`product-row-contract.md`](./product-row-contract.md). |
| **External-system membership model** | Affects how azure jobs, crons, etc. attach to a Product. | Treat as opaque signals pulled into the dashboard, not as first-class Concord-owned state. See [`product-data-model.md`](./product-data-model.md). |
| **Research trackable shape** | Affects Priority 6. | **Resolved by CD-0009:** independent research uses ordinary `kind=research` work; embedded research stays inside its owner; retention-bounded active packs are context/output, not another trackable. |
| **Managed-resource inventory shape** | Affects the ownership model and storage. Sharing a resource across Products is a stated requirement; per-resource stage depends on the answer. | **Resolved by C15 (2026-08-06):** first-class resource registry, singular owner Product, consumer links, explicit stage, typed locators and work links. See [`managed-resource-inventory.md`](./managed-resource-inventory.md). |
| **Stage-to-evidence mapping** | Affects Priority 3. Stage is inert until it resolves to a concrete evidence bar. | **Resolved by CD-0006 R2:** independent maturity and audience obligations, global proof floor, high-water-mark combination, and upward-only local policy. |
| **Cross-workflow impact/freshness** | Prevents one workflow from acting after related work invalidates its assumptions. | **Resolved by CD-0006 R3 and amended by CD-0041:** declared edges, completion notices, bounded consequential-boundary checks, hard-edge-plus-breaking blocking, deterministic version fallback, and explicit version-pinned resolution for concurrent work whose affected Domains overlap. |
| **Goal-to-outcome binding** | Affects Priority 3. *Intent fidelity* and *no silent drift* stay inert until a stated goal is checkable against what was actually delivered. | **Resolved by CD-0012 (2026-08-08):** three-part outcome contract (premise, required end-state, candidate set) approved at planning beside the CD-0006 D10 spec mandate; a weaker delivered end-state fails; mid-execution discoveries forward-link rather than substitute. See [`decisions/CD-0012-bind-stated-goals-to-delivered-outcomes.md`](./decisions/CD-0012-bind-stated-goals-to-delivered-outcomes.md). |
| **Agent context continuity** | A lossy working-window boundary must not silently drop law, approvals, workflow position, or evidence. | **Resolved by CD-0016 (2026-08-11):** durable bounded checkpoints and monotonic boundaries; pinned state is re-derived on every call; summaries are advisory and typed restart is unimplemented pending issue #120. |
| **Replacement relation home** | Affects Priority 1 and 2. | **Bounded by CD-0041 and PM4:** Domain `replaces` and work-item `supersedes` remain current. No generic Product, Project, resource, or workflow replacement home is implied. |

---

## Companion documents

| Document | What it owns |
|---|---|
| [`README.md`](./README.md) | Navigation hub; links to canonical decisions. |
| [`floor-readiness.md`](./floor-readiness.md) | Authorizing per-item record of distance from the first-usable floor. |
| [`predecessor-operational-coverage.md`](./predecessor-operational-coverage.md) | Authorizing enumeration of predecessor operational territory and Concord's coverage of it (floor condition 6). |
| [`core-architecture.md`](./core-architecture.md) | Go-core direction, consolidated resilience invariants; storage settled by CD-0002. |
| [`design-constraints.md`](./design-constraints.md) | NFRs and hard constraints derived from the priorities. |
| [`rollout-plan.md`](./rollout-plan.md) | Dependency-driven sequencing and entry conditions. |
| [`advance-predecessor-lessons.md`](./advance-predecessor-lessons.md) | Public issue-linked predecessor lessons; non-authorizing design input. |
| [`development-authority.md`](./development-authority.md) | Concord development workflow under CD-0089, with GitHub planning and merge authority. |
| [`clarifications.md`](./clarifications.md) | Open decisions needing operator direction before build. |
| [`canonical-git-note-placement.md`](./canonical-git-note-placement.md) | **Accepted PM6:** deterministic durable-note home and git publish proof. |
| [`compaction-retention-policy.md`](./compaction-retention-policy.md) | **Accepted PM7:** bounded lazy projection pruning, retained event authority, and immutable pruned IDs. |
| [`workflows.md`](./workflows.md) | Purpose-built workflow types. |
| [`product-data-model.md`](./product-data-model.md) | Product ownership and membership model. |
| [`specs-as-laws.md`](./specs-as-laws.md) | Spec-law conflict surfacing and evolution. |
| [`self-documentation.md`](./self-documentation.md) | Browsable specs and durable workflow docs. |
| [`feature-inventory.md`](./feature-inventory.md) | Capability inventory and placement rubric. |
| [`capability-placement.md`](./capability-placement.md) | Where each capability belongs by shape, including external/native ownership. |
| [`market-landscape.md`](./market-landscape.md) | Competitor and adjacent-tool research. |
| [`vertical-integration.md`](./vertical-integration.md) | Product-scoping for lgrep / vision / ZLauncher. |

---

*Concord is a direction, not a deadline. Each phase earns the next through evidence, not calendar pressure.*

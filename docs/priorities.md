# Concord Priorities

> **Status:** Public constitutional planning authority; implementation remains staged by accepted readiness rules.\
> **Codename:** Concord (concordance: bringing scattered work into one view).\
> **Created:** 2026-07-25, from predecessor-state analysis and product-design work.\
> **Authority:** After bootstrap, the public `Sharper-Flow/concord` repository is the authority for this document and its accepted companions. This file is the **sole canonical source** for the six ranked priorities, the operating envelope, and the predecessor relationship. Companion documents link here; they do not redefine the order.

---

## Purpose

Concord is a product-development coordination system for **one operator and many local AI agents working on one machine**. It is vision-led, not Advance-led. Its priorities, user boundary, interface direction, and quality governance are recorded here so that later design, inventory, and rollout documents can reference one authoritative statement.

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
- **Advance capability coverage is a release floor.** Concord must be able to cover the same operational territory before replacing any Advance surface, but it does not have to mirror Advance's shape.
- **Concord is Advance's full successor.** After full replacement readiness, migrate one Product at a time; all Projects in that Product move together, only selected active changes are imported, and migrated Products fix forward in Concord.
- **Advance is a source of evidence, not a design mandate or a blocker.** Where Advance already produces reliable, agent-native state, Concord may consume it as evidence. Where Advance is unstable, that instability is **design input** (an anti-pattern to structurally prevent), not a gate Concord waits on.
- **No command-for-command or seven-gate clone.** Concord may use a 7-gate implementation workflow where it fits, but workflow plurality is governed by Priority 5. The seven gates are not a universal requirement.
- **Advance stabilization work is lesson evidence, not a prerequisite or a blueprint.** The public predecessor lessons document reachable issue-backed failure patterns; they are neither converted into Concord architecture decisions nor required to ship before Concord begins.

---

## Ownership-aligned placement

See [`capability-placement.md`](./capability-placement.md).

---

## Ranked priorities

The order is deliberate. Lower-numbered priorities are more foundational; a higher-numbered priority must not compromise a lower-numbered one.

### 1. Data governance, reliability, and safe evolution

**What it means.** Concord-owned state is trustworthy, durable, and evolvable without destructive migration or manual repair. The data model can grow, shrink, and reshape without corrupting in-flight work or historical records.

**Why it is first.** Every other priority depends on a substrate that agents and the operator can rely on. If the data layer is unsafe, visibility, planning, and knowledge all become unreliable.

**Key implications.**
- No database lock contention for agent-facing writes.
- No hand-repair of history; corrections are appended, not destructive.
- Workflow and schema changes must be safe for in-flight work.
- Storage and IO are a founding design decision, not an afterthought. State authority is **SQLite sole authority** per CD-0002 (invariants I1–I6).

### 2. Quality governance

**What it means.** Work is done correctly before it is declared done. Quality is defined by attributes, not by a fixed gate count.

**Concord's quality attributes are:**
- Intent fidelity — the delivered outcome matches the stated intent.
- End-to-end traceability — from need to evidence to artifact.
- Evidence-backed completion — completion claims are supported by recorded proof.
- Independent challenge — high-risk work is reviewed by a party other than the author.
- No silent drift — deviations from spec or plan are surfaced, not papered over.
- Required-obligation blocking — unmet blocking obligations halt the work.
- Human authority at consequence boundaries — the operator decides when tradeoffs affect safety or scope.
- Durable proof — evidence is retained and inspectable after the work is finished.
- Proportional rigor — the depth of process matches the risk of the work.

This is **not** a mandate to use Advance's seven-gate lifecycle for every work kind. Different workflow types may use different gates, but they must satisfy these attributes.

**What scales proportional rigor.** The last attribute — proportional rigor — needs an input, and that input is the **declared lifecycle stage** of the thing being worked on (`maturity` and user-declared `audience_commitment`, at Product, repo/component, and resource level; see [`product-data-model.md`](./product-data-model.md) §8). Stage governs the **evidence bar** — what depth of testing is expected and what proof of testing is required before work is called done — **not** the workflow shape. A prototype does not skip gates; it satisfies them with proportionate evidence. No stage is an evidence exemption. Independent maturity/audience obligations combine; Product-local policy may strengthen but never weaken Concord's global floor.

See [`design-constraints.md`](./design-constraints.md) §10 and [`workflows.md`](./workflows.md).

### 3. Visibility and continuity

**What it means.** The operator and agents can see the current state of the portfolio without blind spots, and they can trust that the view is fresh enough to act on.

**Why it matters.** The original pain was portfolio blindness: research, ops, implementation, and infrastructure work were scattered across tools and repositories. Concord must make the whole Product visible in one place.

**Key implications.**
- Reads are bounded, fast, and scoped to a Product.
- Staleness is reviewed before action, with execution blocked according to risk.
- Continuity is preserved across agent sessions and worktree switches.
- The terminal launcher is the primary visibility surface.

See [`design-constraints.md`](./design-constraints.md) §2, §5 for the staleness and read-path constraints.

### 4. Planning and coordination

**What it means.** Work is planned and coordinated at the Product scope, not just inside individual repositories or changes. Dependencies, sequences, and resource contention are visible before they become blockers.

**Key implications.**
- Product is the primary planning unit.
- Cross-project dependencies are first-class.
- The operator sees what is ready, what is blocked, and what is next.
- Agents operate inside a Product context rather than hopping between disconnected projects.

### 5. Workflow versatility

**What it means.** Different kinds of work get different purpose-built workflow shapes. Research, investigation, static analysis, ops runbooks, and spec-driven implementation each have a workflow type that fits them, rather than being forced into a single 7-gate shape.

**Key implications.**
- Concord owns a full workflow coordination engine: progression, branching, retries, evidence, recovery, and the accepted composition model. Native systems retain external effects.
- Workflow types are code-defined, versioned, purpose-built built-ins plus one generic type for true one-offs.
- The 7-gate implementation change remains one type among many.
- New types can be added without breaking existing in-flight work.
- Each workflow type declares its own artifacts, steps, and completion evidence.

See [`workflows.md`](./workflows.md).

### 6. Durable product knowledge

**What it means.** Specs, decisions, runbooks, and project context are durable, browsable, and co-located with the work they govern. Knowledge does not live in ephemeral chat history or scattered notes.

**Key implications.**
- Specs are browsable by Product and component.
- Durable workflow documents are visible and linked to their work.
- Locality of behavior: a component's specs, changes, and runbooks live near each other.
- Self-documentation is a designed read surface, not a side effect of file persistence.

See [`self-documentation.md`](./self-documentation.md) and [`product-data-model.md`](./product-data-model.md).

---

## First-usable floor

Concord's first usable form is a **complete, replacement-ready coordination surface** for one operator and many agents on one machine, anchored to a **Product-first terminal launcher**. It must cover the full operational scope that Advance currently provides for this operator (Product-scoped planning, visibility, implementation changes, research/investigation tracking, ops runbooks, and durable product knowledge) while materially improving every one of the six priorities.

Incremental design, build, replay, and shadow evaluation are allowed, but a partial slice cannot be called usable or replacement-ready. Migration begins only after the full floor below is proven.

Migration then proceeds one Product at a time. Advance remains authority for unmigrated Products; each migrated Product fixes forward in Concord. Advance retires after the final Product moves. This is a bounded transition, not permanent coexistence or rollback.

It becomes usable when:

1. The operator can see, plan, and act across the full Product scope from the launcher.
2. Agents can read, write, and execute within that Product context through tools.
3. Every supported work kind (implementation, research, ops, etc.) has a defined workflow type and completion evidence.
4. Required authority/freshness is explicit and CD-0006 R3's accepted cross-workflow
   impact policy is enforced.
5. Concord-owned data and workflow boundaries satisfy Priority 1 (no locks, no hand-repair, safe evolution).
6. The Advance surfaces that Concord consumes are stable enough to serve as evidence, and Concord can replace them without losing operational coverage.

---

## Product-level decision tracker

Eleven questions have resolved directions. Replacement-relation home remains the one
open Product-level question and must be answered by a later design pass rather than
silently decided by implication.

| Question | Why it matters | Current direction, if any |
|---|---|---|
| **Storage model for Concord-owned state** | Foundation for Priority 1 and 6. | **Resolved** — CD-0002 + accepted PM2/PM3 (2026-08-05) establish one global local SQLite DB, append-only `domain_events` authority, and explicit typed projections updated in one transaction. Accepted PM6/PM7 (2026-08-05/06) add git-proof-backed durable notes plus bounded lazy projection pruning without event deletion. |
| **Workflow evolution semantics** | Affects Priority 1, 2, and 5. | **Resolved by CD-0006:** Concord owns full coordination; purpose-built types are code-defined and versioned, with one generic one-off type. Work composes through forward-linked successors with independent authority/recovery. |
| **Read-path implementation language** | Affects Priority 3. | **Go** — the Concord core is Go from day one. See [`core-architecture.md`](./core-architecture.md). Latency target (sub-100ms) remains; language is no longer conditional. |
| **Where the Product entity lives** | Affects architecture and storage. | **Resolved by CD-0007:** standalone `Sharper-Flow/concord` Product/repository, Go module `github.com/sharper-flow/concord`; implementation authority moves there after the audited public bootstrap. |
| **Product row fields** | Affects what the operator sees in the launcher. | **Resolved by C14 (2026-08-06):** identity, declared stage, reliance, typed action counts, and one deterministic focus item. See [`product-row-contract.md`](./product-row-contract.md). |
| **External-system membership model** | Affects how azure jobs, crons, etc. attach to a Product. | Treat as opaque signals pulled into the dashboard, not as first-class Concord-owned state. See [`product-data-model.md`](./product-data-model.md). |
| **Research trackable shape** | Affects Priority 5. | **Resolved by CD-0009:** independent research uses ordinary `kind=research` work; embedded research stays inside its owner; retention-bounded active packs are context/output, not another trackable. |
| **Managed-resource inventory shape** | Affects the ownership model and storage. Sharing a resource across Products is a stated requirement; per-resource stage and cross-Product replacement both depend on the answer. | **Resolved by C15 (2026-08-06):** first-class resource registry, singular owner Product, consumer links, explicit stage, typed locators/work/replacement edges. See [`managed-resource-inventory.md`](./managed-resource-inventory.md). |
| **Stage-to-evidence mapping** | Affects Priority 2. Stage is inert until it resolves to a concrete evidence bar. | **Resolved by CD-0006 R2:** independent maturity and audience obligations, global proof floor, high-water-mark combination, and upward-only local policy. |
| **Cross-workflow impact/freshness** | Prevents one workflow from acting after related work invalidates its assumptions. | **Resolved by CD-0006 R3:** declared edges, completion notices, bounded consequential-boundary checks, hard-edge-plus-breaking blocking, and deterministic version fallback. |
| **Goal-to-outcome binding** | Affects Priority 2. *Intent fidelity* and *no silent drift* stay inert until a stated goal is checkable against what was actually delivered. | **Resolved by CD-0012 (2026-08-08):** three-part outcome contract (premise, required end-state, candidate set) approved at planning beside the CD-0006 D10 spec mandate; a weaker delivered end-state fails; mid-execution discoveries forward-link rather than substitute. See [`decisions/CD-0012-bind-stated-goals-to-delivered-outcomes.md`](./decisions/CD-0012-bind-stated-goals-to-delivered-outcomes.md). |
| **Agent context continuity** | A lossy working-window boundary must not silently drop law, approvals, workflow position, or evidence. | **Resolved by CD-0016 (2026-08-11):** durable bounded checkpoints and monotonic boundaries; pinned state is re-derived on every call; summaries are advisory and restart is unavailable until typed-agent registry issue #57. |
| **Replacement relation home** | Affects Priority 1 and 6. | **Open.** A property of the ownership model, or its own relation store. Couples to the Product-entity home question above. See [`product-data-model.md`](./product-data-model.md) §10. |

---

## Companion documents

| Document | What it owns |
|---|---|
| [`README.md`](./README.md) | Navigation hub; links to canonical decisions. |
| [`core-architecture.md`](./core-architecture.md) | Go-core direction, consolidated resilience invariants; storage settled by CD-0002. |
| [`design-constraints.md`](./design-constraints.md) | NFRs and hard constraints derived from the priorities. |
| [`rollout-plan.md`](./rollout-plan.md) | Dependency-driven sequencing and entry conditions. |
| [`advance-predecessor-lessons.md`](./advance-predecessor-lessons.md) | Public issue-linked predecessor lessons; non-authorizing design input. |
| [`development-authority.md`](./development-authority.md) | Accepted authority model before replacement readiness. |
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
| [`vertical-integration.md`](./vertical-integration.md) | Product-scoping for lgrep / vision / episode / ZLauncher. |

---

*Concord is a direction, not a deadline. Each phase earns the next through evidence, not calendar pressure.*

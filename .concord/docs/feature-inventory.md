# Concord Feature Inventory

> **Status:** **Predecessor capability evidence — non-authorizing.** Companion to [`README.md`](./README.md) and
> [`priorities.md`](./priorities.md) (canonical priorities).
> **Purpose:** A clear, grounded list of every Concord capability, categorized
> as **Transfer** (carried from Advance as-is), **Refactor/Extend** (materially
> reworked but not rewritten), or **New** (net-new, no Advance precedent). Each
> capability is aligned with the canonical Concord priorities without restating
> the ranked list.
> **Primary surface:** Defined by [`priorities.md`](./priorities.md); admin
> panel / web UI are optional projections.
> **Snapshot evidence for "what existed in the predecessor":** public issue-linked
> lessons and the predecessor postmortem. Refresh against reachable public sources
> before implementation or cutover decisions; this inventory does not establish
> current operational truth.
> **Tool-surface hold:** predecessor capability names are evidence about jobs and
> failure patterns, not a transfer plan. TS1–TS9 in
> [`clarifications.md`](./clarifications.md) own Concord's minimal tool surface.
> **Readiness authority:** this inventory does not record distance from the
> first-usable floor and never has.
> [`floor-readiness.md`](./floor-readiness.md) and its validated manifest own
> that record; this document remains non-authorizing capability evidence.
> **Coverage authority:** whether Concord can produce a given predecessor outcome
> today is owned by
> [`predecessor-operational-coverage.md`](./predecessor-operational-coverage.md),
> which is organized by operational territory and authorizing for floor condition
> 6. This inventory records capability *lineage* only. Where the two disagree
> about whether something exists, the coverage document wins — its evidence paths
> are checked.

## How to read this

| Bucket | Definition |
|---|---|
| **Transfer** | The **user/domain outcome remains required**. Implementation, tool identity, and workflow shape do not transfer by implication. |
| **Refactor/Extend** | The predecessor outcome remains useful but its domain shape changes materially in Concord. |
| **New** | Net-new capability with **no complete Advance precedent**. Built fresh on the accepted Concord core/domain model. |

Throughout the historical inventory below, **"unchanged" means outcome/constraint
continuity only**. It never means "keep this tool," "keep this implementation," or
"expose this operation to agents." TS1–TS9 decide the agent surface.

### Terminology note (important)

The framing "transfer vs refactor vs new" records **capability lineage only**. It
does not authorize implementation reuse, tool identity, or command compatibility.
Concord is a clean rewrite (D2); a predecessor capability may be preserved through
a radically smaller domain operation or omitted from the agent surface entirely.

### What is dropped

No required **user outcome** is silently lost, but Advance's tools, compatibility
machinery, process exhaust, and internal repair surfaces do not transfer by
default. TS1–TS9 decide what agents actually see; CD-0002's compaction model
explicitly drops process exhaust.

The canonical Concord priorities are maintained in [`priorities.md`](./priorities.md); this
inventory follows them without restating the ranked list.

---

## 1. Predecessor outcomes retained

These are ADV outcomes/constraints worth preserving as predecessor evidence.
Concord does not inherit their implementation or tool surface. Grouping follows
the old catalog only so evidence remains traceable.

### 1.1 The 7-gate lifecycle + enforcement
- Gate model: proposal → discovery → design → planning → execution → acceptance
  → release.
- `adv_gate_complete`, `adv_gate_status` — sequential gate machine, machine-
  enforced human-in-the-loop (HITL) at planning.
- **Why transfer:** the core strength; the spec-driven implementation workflow
  works extremely well. Unchanged.

### 1.2 Change management
- `adv_change_create`, `adv_change_list`, `adv_change_show`, `adv_change_update`,
  `adv_change_close`, `adv_change_bulk_close`, `adv_change_archive`,
  `adv_change_reenter`, `adv_change_validate`, `adv_change_update_issues`,
  `adv_change_repair_origin`.
- Cross-project via `target_path` + `target_confirmed` — the **current Advance
  mechanism**, recorded as an explicit **anti-pattern reference** for
  Product-scoped fan-out, not the foundation to build it on. In Advance,
  capability correlates with whether a given tool threads the parameter, so
  anything resolving state from ambient session context degrades. Concord
  requires ambient Product context instead — see
  [`design-constraints.md`](./design-constraints.md) §13 and the public lessons in
  [`advance-predecessor-lessons.md`](./advance-predecessor-lessons.md).
- **Why transfer:** the change lifecycle is the unit of spec-driven work.
  Unchanged.

### 1.3 Spec system (specs as laws)
- `adv_spec` (list/show/search).
- Deltas: `adv_delta_add`, `adv_delta_amend`, `adv_delta_list`,
  `adv_delta_modify`, `adv_delta_remove`, `adv_delta_rename`,
  `adv_delta_retract`, `adv_delta_show`.
- Archive remains the sole global-spec writer.
- **Why preserve the outcome:** accepted Product law is Concord's primary
  deliverable. CD-0222 redesigns it around canonical Domain ownership,
  architecture-bound work, concurrent-overlap resolution, and Git-only law
  authority; predecessor tool identity does not transfer.

### 1.4 Contract system
- `adv_contract_mint`, `adv_contract_review_matrix_set`.
- Typed ChangeContract from agreement; review matrix blocks acceptance.
- **Why transfer:** unchanged.

### 1.5 Task system
- `adv_task_add`, `adv_task_list`, `adv_task_ready`, `adv_task_show`,
  `adv_task_update`, `adv_task_cancel`, `adv_task_checkpoint`,
  `adv_task_reclassify_tdd`.
- Tasks, test-driven-development (TDD) intent, dependencies, evidence policy, contract refs.
- **Why transfer:** unchanged.

### 1.6 Durable execution + safety substrate
- Advance's Temporal substrate is held as *evidence*, not re-implemented.
  Concord's durable state is SQLite sole authority (CD-0002). The Temporal
  tooling remains relevant as the substrate Advance currently runs on while
  Concord's own surface is being built.
- **Trunk-write firewall** (target-relative, `rq-crossProjectTrunkFirewall01`).
  Unchanged.
- **Worktree lifecycle:** `adv_worktree_create`, `adv_worktree_resume`,
  `adv_worktree_delete`, `adv_worktree_cleanup`, `adv_worktree_triage`.
  Unchanged.
- **Conformance:** `adv_conformance` (external CI-isolated spec conformance).
  Unchanged.

### 1.7 Knowledge + reflection
- `adv_wisdom_add`, `adv_wisdom_list`, `adv_reflect`, `adv_reflection_list`.
- **Why transfer:** unchanged.

### 1.8 Review / verification / design dispositions
- `adv_subagent_report_submit`, `adv_report_followup_promote`.
- `adv_design_concern_disposition`, `adv_verification_evidence_disposition`.
- `adv_lightweight_profile_evaluate`.
- **Why transfer:** unchanged.

### 1.9 Operations: test, archive, repair
- `adv_run_test` (typed pass/fail evidence).
- `adv_archive_purge` (archived-only purge).
- Repair/maintenance: `adv_doctor`, `adv_change_workflow_terminate`,
  `adv_snapshot_health`, `adv_store_cleanup`, `adv_store_consolidate`,
  `adv_launcher_projection_rebuild`.
- **Why transfer:** unchanged. (Note: standalone *ops runbooks* are a separate
  Refactor item in §2 — the *repair* tools stay; the *operational runbook
  hosting* extends.)

### 1.10 Session, project, introspection
- `adv_session_list`, `adv_session_show`.
- `adv_project_context`, `adv_project_metadata`.
- `adv_tool_catalog`, `adv_tool_describe`, `adv_tool_invoke`.
- **Why transfer:** unchanged.

**Predecessor evidence represented here:** many Advance catalog entries overlap a
much smaller set of domain jobs. This is **not** a Concord tool count or transfer
estimate. The accepted surface is derived from TS1's canonical jobs and TS2's
evaluation, with no 1:1 mapping from these entries.

---

## 2. Refactor / Extend (materially reworked)

Predecessor outcomes and failure evidence that Concord may preserve or reshape
only after the Product-memory and tool-surface decisions. This section does not
authorize a predecessor implementation, transport, or tool identity; each item
references the dependency-driven sequence in [`rollout-plan.md`](./rollout-plan.md).

### 2.1 `adv_status` aggregation
- **Evidence authority (AC7):** historical predecessor-state claims in this section
  are non-authorizing and must be checked against reachable public records before
  any readiness or cutover decision; see [`advance-predecessor-lessons.md`](./advance-predecessor-lessons.md).
- **Today:** times out (10s+) on summary/product scope; the headline overview
  tool is broken.
- **Concord implication:** Product-memory must answer bounded portfolio queries
  without inheriting this timeout/fan-out shape. `fixHealthViewTimeouts` remains
  Advance stabilization evidence, not a Concord prerequisite or implementation
  plan. PM1/PM2 and TS1–TS3 decide the query and agent-surface shape.
- **Bucket call:** predecessor evidence for a Product-memory outcome; no
  aggregator implementation or tool transfers.

### 2.2 `adv_resume_projection` lifecycle fidelity
- **Today:** flattens all changes to `ready_to_start`, `active: []` despite
  in-progress tasks (e.g. 9/16, 12/13 done read as "ready").
- **Concord implication:** the accepted lifecycle/read model must distinguish
  active from ready work and explain what is next. PM1/PM4 and TS1–TS3 decide
  the representation and exposure; no Advance projection logic transfers.
- **Bucket call:** predecessor failure evidence, not a projection implementation
  plan.

### 2.3 Business grouping in Linear
- **Today:** predecessor grouping commands provide scope, ordering, and
  requiredness context for initiatives.
- **Concord:** [CD-0213](./decisions/CD-0213-linear-mcp-is-the-only-planning-authority.md)
  places business grouping in Linear Projects and Initiatives through the
  Linear MCP server. Concord keeps managed execution and recorded issue identity,
  with no Initiative entry, narrative, ordering, or requiredness surface.
- **Bucket call:** External planning — predecessor grouping is evidence of the
  planning need, not a Concord runtime capability or transfer plan.

### 2.4 Ops runbooks decoupled from change
- **Today:** `adv_ops_run_upsert`, `adv_ops_run_evidence_add`,
  `adv_ops_evidence_add`, `adv_ops_followup_resolution_upsert`,
  `adv_followup_promote` — all scoped to a *change's* ops_followup profile.
- **Concord:** operational work selects a retained execution family;
  `ops_runbook` is retired for new execution, with no compatibility alias.
  [CD-0218 D1](./decisions/CD-0218-the-live-workflow-catalog-retains-three-execution-families.md#d1-three-families-own-new-execution)
  adds no provider executor, health observer, rollback claim, or timer service.
- **Bucket call:** predecessor operational evidence, not a standalone runbook
  host or a transfer plan for its choreography.

### 2.5 Backlog → research trackable
- **Today:** `adv_backlog_add`, `adv_backlog_list`, `adv_backlog_show`,
  `adv_backlog_archive`, `adv_backlog_promote` — promotable future-work items
  with `context_packet`.
- **Concord:** [CD-0215 D2](./decisions/CD-0215-retained-research-context.md#d2-research-resolves-to-ordinary-work)
  keeps independent research in ordinary work items and embedded research with
  its owner. Research packs are owned context, not another trackable.
- **Bucket call:** Refactor.

### 2.6 Predecessor read surface
- **Historical evidence:** the predecessor exposed overlapping read jobs through
  multiple client-facing surfaces. The public lesson is portability and authority,
  not a retained transport or tool list.
- **Concord implication:** the predecessor evidence demonstrates overlapping read
  jobs and a portability tradeoff. It does not authorize wiring, extending, or
  retaining an MCP server; TS1–TS3 establish the read jobs and accepted TS6 selects
  the `concord.ts` custom-tool adapter, not MCP.
- **Bucket call:** predecessor transport evidence, not a Concord MCP plan.

### 2.7 Cross-repo visibility reads
- **Today:** `adv_change_list`/`adv_status`/`adv_wip_state` accept `scope:
  "product"` and `target_path`, but product scope is brittle (adv_status times
  out) and not Product-entity-aware.
- **Concord implication:** Product-scoped visibility must not depend on brittle
  per-call path plumbing. PM1/PM2/PM5 decide the authority scope and query path;
  TS3/TS5 decide any agent exposure. Fan-out is a candidate to falsify, not a
  selected implementation.
- **Bucket call:** predecessor failure evidence, not a cross-repository read
  design.

### 2.8 Spec-conflict HITL surfacing + evolution flow
- **Today:** `adv_change_validate` detects spec conflicts and pushes agents back,
  but when a *user* request challenges a spec law, agents too often **silently
  cut scope** during research/design/prep instead of surfacing the conflict. The
  legislator (user) is bypassed.
- **Concord:** evolve specs-as-laws so conflicts surface via HITL with an explicit
  choice — (a) clarify intent, (b) evolve the spec via a delta, (c) consciously
  accept scope reduction — enforced structurally (not just by instruction) and
  recorded auditably. Agents never silently cut scope.
- **Bucket call:** Refactor/Extend — reuses transferred validate/delta/gate
  machinery (§1.1, §1.3) but layers net-new HITL surfacing, structural
  enforcement, and auditability. Full model in
  [`specs-as-laws.md`](./specs-as-laws.md).

---

## 3. New (net-new)

Capabilities with no complete Advance precedent. Built fresh on the accepted
Concord core/domain model. The list
is the inventory; the full model for each lives in the doc linked in its
*Where detailed* column. Sections §3.1, §3.13a, and §3.18–§3.20 retain their full
descriptions because they are referenced from elsewhere in the docset.

| # | Capability | One-line summary | Where detailed |
|---|---|---|---|
| 3.1 | Product entity (first-class) + ownership data model | Durable Product that declaratively owns Domains, Projects, and managed resources — no bridges or external API authority. Product → Domain navigation. | [`product-data-model.md`](./product-data-model.md) |
| 3.2 | Fast read-path / portfolio dashboard | Sub-100ms cross-Product read layer (Go) over the materialised state + external signals. | [`core-architecture.md`](./core-architecture.md) §1 |
| 3.3 | External-signal ingestion | Pull azure/cron/health/MCP outputs into Product view as **opaque read-only signals** (not ADV-authored state). | [`product-data-model.md`](./product-data-model.md) §3.3 |
| 3.4 | Cross-Product scoped read | Reads one global local authority through explicit Product/Project scope; no per-repo fan-out or identity reconciliation. | [`product-memory-authority-scope.md`](./product-memory-authority-scope.md), [`product-memory-domain-schema.md`](./product-memory-domain-schema.md) |
| 3.5 | Operational work | `ops_runbook` is retired for new execution; retained families add no provider executor or runbook host. | [CD-0218 D1](./decisions/CD-0218-the-live-workflow-catalog-retains-three-execution-families.md#d1-three-families-own-new-execution) |
| 3.6 | Research context owned by ordinary work | Independent research uses ordinary work; embedded research stays with its owner. Packs are context, not a peer entity. | [CD-0215 D2](./decisions/CD-0215-retained-research-context.md#d2-research-resolves-to-ordinary-work) |
| 3.7 | Portfolio-review tooling + cadence | "What should I work on across everything" with a periodic portfolio-review cadence, Product-scoped (per-repo `/adv-triage` and `/adv-cleanup` Transfer). | — |
| 3.8 | Product wishlist | Planning backlog lives in Linear through the Linear MCP server, not in a repository or a Concord planning projection. | [`decisions/CD-0213-linear-mcp-is-the-only-planning-authority.md`](./decisions/CD-0213-linear-mcp-is-the-only-planning-authority.md) |
| 3.9 | Infra status tracking | Tracked surface over §3.3 signal ingestion; presents azure/cron/health/ops status within a Product. | [`product-data-model.md`](./product-data-model.md) |
| 3.10 | Admin panel (lightweight grid/table projection) | Optional human-facing projection over the fast read-path; **not** the primary operator surface (the session entry is). The CD-0108 replacement owns the session bootstrap role through the entry route. | [`design-constraints.md`](./design-constraints.md) §5 |
| 3.11 | Work-type taxonomy / phase-spanning work | Business work categories and operator kinds stay separate from execution families; the size decision model selects the shared-part composition. | [`workflows.md`](./workflows.md) §3, §7 |
| 3.12 | Spec & document browse surface (self-documentation) | Navigable Product → Domain browse surface over current Product law, evidence, and durable workflow docs. | [`self-documentation.md`](./self-documentation.md) |
| 3.13 | Workflow-family system | Three retained execution families composed from shared parts, with size set by the decision model. | [`workflows.md`](./workflows.md) §4, §7 |
| 3.13a | Architecture decision obligation | `architecture_spike` is retired for new execution; the separate reviewed and accepted decision obligation remains with the cutover owner's approved successor set. | [`workflows.md`](./workflows.md) §4; [CD-0215 D2](./decisions/CD-0215-retained-research-context.md#d2-research-resolves-to-ordinary-work) |
| 3.14 | Capability-placement rubric (optimal shape) | Rubric for placing each capability by shape; **kept dynamic** as tooling evolves. | [`capability-placement.md`](./capability-placement.md) |
| 3.15 | Cross-project GitHub-issue integration | Native cross-project GH-issue create/update — bugs canonical in GH, not in `target_path` ADV backlog. | [`design-constraints.md`](./design-constraints.md) §13 |
| 3.16 | One-sentence value statement invariant | Every managed work item carries `If this succeeds, what concrete product capability or risk is changed?` | [`workflows.md`](./workflows.md) §2.1 |
| 3.16a | Outcome contract (goal bound to delivery) | Three-part premise / required end-state / candidate set, approved at planning and verified at completion; a weaker delivered end-state fails. | [`workflows.md`](./workflows.md) §2.1a; CD-0012 |
| 3.17 | Lifecycle stage + proportional-rigor governance | Independent `maturity` + user-declared `audience_commitment` bands at Product/Domain/Project/resource; global evidence floor with upward-only local overrides. | [`product-data-model.md`](./product-data-model.md) §8; CD-0006 |
| 3.18 | Managed-resource inventory with cross-Product linking | First-class declarative identity for shared infra/SaaS — underneath §3.3 (live status) and §3.9 (presented surface). | [`product-data-model.md`](./product-data-model.md) §9 |
| 3.19 | Canonical Domain architecture | Stable Product-internal Domain identity, hierarchy, law ownership, and endpoint-specific `depends_on` / shared-contract / `replaces` relations. | [`decisions/CD-0222-architecture-bound-product-law-with-package-boundary-code-ownership.md`](./decisions/CD-0222-architecture-bound-product-law-with-package-boundary-code-ownership.md) D2–D4 |
| 3.20 | Concurrent architecture-overlap control | Product-changing contracts declare Domain/law footprints; unresolved overlap blocks consequential authority until a version-pinned operator resolution exists. | [`decisions/CD-0222-architecture-bound-product-law-with-package-boundary-code-ownership.md`](./decisions/CD-0222-architecture-bound-product-law-with-package-boundary-code-ownership.md) D5–D7 |

### 3.1 Product entity (first-class) + ownership data model
- A durable `Product` type that **declaratively owns its members**: repositories,
  infrastructure (azure jobs, crons, deployed services), and SaaS solutions
  (Supabase, PostHog, …). Concord records *what they are* and *which Product owns
  them* — it does **not** bridge to them or call their APIs. Full model in
  [`product-data-model.md`](./product-data-model.md).
- Product knowledge is navigated **Product → Domain**, with current law and
  architecture-bound work together.
- **Why new:** predecessor grouping containers group initiatives; `scope_repos` is
  per-*change*; nothing today is a durable cross-repo+external-system *product*
  identity that records ownership and that a dashboard pivots on.
- **Builds on:** Product/Project scope evidence, not predecessor hierarchy or
  cross-project ambient-path mutation.

### 3.13a Architecture decision obligation
- [CD-0218 D1](./decisions/CD-0218-the-live-workflow-catalog-retains-three-execution-families.md#d1-three-families-own-new-execution)
  retires `architecture_spike` for new execution, without an alias to research
  or another retained family. Released pins remain exact frozen history (D4).
- [CD-0215 D2](./decisions/CD-0215-retained-research-context.md#d2-research-resolves-to-ordinary-work)
  still requires a separate reviewed and accepted decision. The cutover owner's
  approved successor set must preserve that obligation; this inventory chooses
  no successor route. Research reports do not enact legislation.
- [`architecture-spike.md`](./architecture-spike.md) describes the historical
  shaped model, not an eligible new-execution family.

### 3.18 Managed-resource inventory with cross-Product linking
- An inventory of **managed infrastructure and SaaS solutions** that Products
  link to, supporting resources **shared by more than one Product** — a shared
  database, queue, observability account, or CI runner pool serving several
  Products at once.
- **Distinct from two neighbours:** §3.3 signal ingestion is the *live status*
  mechanism; §3.9 infra-status tracking is the *presented surface*. This is the
  **declarative resource identity** layer underneath both — true whether or not
  any external system is reachable.
- **Shape accepted by C15:** one canonical resource entity, singular owning Product,
  zero-or-more consuming Products, explicit stage, and typed locator/work edges.
  See [`managed-resource-inventory.md`](./managed-resource-inventory.md).

### 3.19 Canonical Domain architecture
- A stable Product-internal Domain identity, hierarchy, law ownership, and
  endpoint-specific `depends_on` / shared-contract / `replaces` relations.
  See [`decisions/CD-0222-architecture-bound-product-law-with-package-boundary-code-ownership.md`](./decisions/CD-0222-architecture-bound-product-law-with-package-boundary-code-ownership.md).

### 3.20 Concurrent architecture-overlap control
- Product-changing contracts declare Domain/law footprints; unresolved overlap
  blocks consequential authority until a version-pinned operator resolution exists.
  See [`decisions/CD-0222-architecture-bound-product-law-with-package-boundary-code-ownership.md`](./decisions/CD-0222-architecture-bound-product-law-with-package-boundary-code-ownership.md).

---

## 4. Summary

| Bucket | What this inventory establishes | Agent-tool implication |
|---|---|---|
| **Retained predecessor outcomes** | Quality, safety, worktree, contract, spec, lifecycle, and knowledge needs remain evidence-backed. | **None by identity.** TS1/TS2 may satisfy many outcomes with a much smaller surface. |
| **Refactor/Extend** | Status, Product scope, cross-repo reads, research context, and conflict handling need a different domain shape. | Derived from accepted jobs, never from old tool names. |
| **New** | Product memory, lifecycle/ownership semantics, workflow plurality, and self-documentation have no complete Advance precedent. | New outcomes still do not automatically earn tools. |

**Headline:** this is a **capability-evidence map, not a port or tool plan**. The
net-new cluster spans seven
themes: (a) the Product concept + fast read-path (§3.1–§3.4, §3.7); (b) the
**product-facing surfaces** — wishlist, infra status, admin panel projection
(§3.8–§3.10); (c) the **all-phase work model** — a work-type taxonomy for
sprawling idea/bug/optimization work (§3.11); (d) **self-documentation** —
browsable specs + durable workflow docs (§3.12); (e) **workflow plurality +
shape discipline** — shared-part workflow compositions (§3.13), the
capability-placement rubric (§3.14), and the value-statement invariant (§3.16);
(f) **ownership semantics** — lifecycle stage governing proportional rigor
(§3.17), and a shareable managed-resource inventory (§3.18); and (g)
**Product-law architecture** — canonical Domains plus concurrent
architecture-overlap control (§3.19–§3.20).
The hard *constraints* governing how these outcomes are rebuilt (no-locks/no-repair,
workflow-evolution, agent-buildable UI) live in
[`design-constraints.md`](./design-constraints.md).

---

## 5. Boundary judgments (where the call was close)

| Capability | Call | Why it's not the other bucket |
|---|---|---|
| Business grouping in Linear (§2.3) | External planning | CD-0213 leaves grouping to Linear MCP; Concord retains managed execution, not a planning mirror. |
| Product aggregation extension in §2.1 | Refactor (shades New) | Reuses the aggregator; the Product pivot is the New-flavored part. Kept in Refactor to avoid double-counting with §3.1. |
| Operational work (§2.4 / §3.5) | Predecessor evidence | CD-0218 retires the ops-runbook family; external effects keep their native owners. No standalone host transfers. |
| Research context (§2.5 / §3.6) | Refactor | CD-0215 keeps ordinary work plus owned pack context, not a peer entity. |
| Architecture decision (§3.13a) | Retained obligation | Retirement of the spike family does not discharge CD-0215 D2's separate reviewed and accepted decision requirement or turn research into legislation. |
| Portfolio-review tooling (§3.7) | New | The per-repo `/adv-triage`/`adv-cleanup` skills Transfer; the Product-scoped, cadence-aware layer does not exist. |

---

## 6. Open questions specific to this inventory

1. **Business grouping — resolved by CD-0213.** Linear owns Projects and Initiatives;
   Concord offers no grouping authoring or membership surface.
2. **Is the fast read-path inside ADV or a separate tool?** (§3.2, [`clarifications.md`](./clarifications.md) C1 and [`rollout-plan.md`](./rollout-plan.md)) Inside-ADV keeps one source of truth + agent-native mutations;
   separate keeps the orchestrator untouched. Phase 2 design.
3. **Research context — resolved by CD-0215 D2:** ordinary work owns research packs; no peer trackable.
4. **Does signal ingestion (§3.3) need a durable signal cache, or is live-fetch
   at read time enough?** Depends on read-latency targets (§3.2 measurement).

---

## 7. Priority lens

Each capability above is aligned with one or more of the six Concord priorities
maintained in [`priorities.md`](./priorities.md). The exact ordering is not restated here; the
inventory is organized by capability shape (Transfer / Refactor / New) rather
than by priority rank. A future alignment pass can tag each section explicitly
once [`priorities.md`](./priorities.md) is stable.

---

## 8. Relationship to other docs

| Doc | Link |
|---|---|
| [`priorities.md`](./priorities.md) | Canonical priorities and operating envelope. |
| [`rollout-plan.md`](./rollout-plan.md) | Dependency-driven sequence and entry conditions. |
| `workflows.md` | Workflow-type system, work-kind taxonomy, value statement, staleness rules. |
| `product-data-model.md` | Product entity and ownership model. |
| `self-documentation.md` | Spec/document browse surface. |
| `design-constraints.md` | Hard constraints on storage, UI, workflow evolution. |
| `specs-as-laws.md` | Spec-conflict HITL evolution flow. |
| `clarifications.md` | Open questions and resolved directions. |
| `vertical-integration.md` | Product-scoping of lgrep/vision. |

---

*This inventory is a snapshot. As Phase 2 design decisions land (§6), bucket
boundaries may shift — update [`clarifications.md`](./clarifications.md) and [`rollout-plan.md`](./rollout-plan.md) when they do.*

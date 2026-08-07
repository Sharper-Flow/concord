# Concord Workflows: A Plurality of Purpose-Built Types

> **Status:** Aligned v4 with CD-0006/CD-0007. Companion to [`README.md`](./README.md),
> [`feature-inventory.md`](./feature-inventory.md),
> [`specs-as-laws.md`](./specs-as-laws.md),
> [`product-data-model.md`](./product-data-model.md).
> **Purpose:** Concord supports **multiple workflow types** — not just the one
> singular 7-gate implementation change Advance has today. Different work shapes
> get one of seven required workflow families: implementation, break-fix/RCA,
> research/investigation, architecture spike, ops runbook, static analysis, or
> generic one-off. Database/configuration/infrastructure map to implementation or
> ops until a distinct recurring lifecycle proves another type necessary.
> **Primary surface:** The Product-first terminal launcher is the primary
> operator surface; an admin panel or web UI is optional and secondary.
> **Origin:** User direction, 2026-07-25.

## TL;DR

Advance has **one** workflow: the spec-driven 7-gate implementation change. It's
excellent — for spec-driven implementation. But forcing operations, database
work, break-fix, configuration, infrastructure, research, RCA, and static
analysis through that one shape is wrong. Concord introduces a **full workflow
coordination engine** with code-defined, versioned purpose-built types plus one
generic type for true one-offs. Each type is shaped for its work kind.
The primary operator interacts through a **Product-first terminal launcher**;
optional grid/table views are projections, not the primary interface.

The canonical Concord priorities are maintained in [`priorities.md`](./priorities.md); this document
follows them without restating the ranked list.

---

## 0. Audience and primary surface (resolved)

- **Primary operator surface:** a Product-first terminal launcher that opens the
  Product view directly from the shell.
- **Launcher responsibility:** context-rich navigation and narrow open/start/resume/
  launch actions. Substantive workflow decisions happen after selection.
- **Human questions:** provide purpose, relevant context, concrete examples, and
  consequences before asking one decision at a time.
- **Web / admin panel:** optional; a grid/table projection for humans, not the
  daily operating surface.
- **ZLauncher:** remains the session/project bootstrap layer; it is **not** a
  candidate for Concord's primary interface.
- **Agent surface:** CD-0005's eight scenario-validated tools through the accepted
  `concord.ts` custom-tool adapter and short-lived Go CLI. No plugin/MCP v1 and no
  separate human-only GUI required for correctness.

---

## 1. The shift: from one workflow to a plurality

Concord owns durable progression, branching, retries, evidence, and recovery across
all workflow types. Native systems still execute git, CI, cloud, database, and other
external effects. Repeated generic patterns graduate into purpose-built built-ins;
arbitrary inline workflow languages are out of scope.

- **Today (Advance):** one workflow — proposal → discovery → design → planning →
  execution → acceptance → release. Everything is a "change."
- **Problem:** implementation, operations, database work, break-fix, configuration,
  infrastructure, options research, evidence research, RCA, and static analysis
  don't fit the 7-gate shape. They're either forced into it (wrong) or live as
  ad-hoc skills outside the tracked lifecycle (`adv-tron`, `adv-slop-scan`,
  `adv-arch-scan`, `adv-audit`, …).
- **Concord:** a **plurality of workflow types**. Each work shape maps to a
  purpose-built workflow with the right steps, artifacts, and completion criteria.
  Some are gateless; some are lightweight; the 7-gate remains the heaviest, for
  spec-driven implementation.

---

## 2. The workflow-type model

A **workflow type** is a purpose-built definition:

| Aspect | Meaning |
|---|---|
| **Steps** | The sequence/phases (gated or gateless). |
| **Artifacts** | What it produces (a spec delta? a report? a runbook record? nothing durable?). |
| **Completion criteria** | How it's "done" (a contract satisfied? a report submitted? a signal fired?). |
| **Work kind** | What shape of work it's for (implementation, operations, database, break-fix, configuration, infrastructure, options research, evidence research, RCA, static-analysis variants, …). |
| **Value statement** | One sentence stating the Epic/change value: what it delivers and why it matters. |
| **Staleness rule** | Whether execution is blocked while the workflow's inputs or upstream state are stale. |
| **Active visibility** | Which gates/problems are surfaced by default vs. hidden behind history. |

Workflow types are **registered** (not hardcoded as one-off skills). New types can
be added as Concord evolves — this is the "different types of workflows" the
operator named.

### 2.1 Value statement invariant

Every Epic and every change must carry a **one-sentence value statement**,
recorded at creation and reviewed at each gate. It is not a marketing blurb; it
answers:

> *If this succeeds, what concrete product capability or risk is changed?*

The value statement is part of the durable workflow record and is visible in the
Product → component browse surface.

### 2.2 Factored lifecycle truth

See [`clarifications.md`](./clarifications.md) R3.

### 2.3 Structural staleness review / block rules

See [`clarifications.md`](./clarifications.md) R2.

### 2.4 Minimal active-work visibility

Default Concord views are intentionally minimal:

- Show **active gates** and **active problems** first.
- Show **what blocks execution right now**.
- Completed history, archived work, and passive context are available through
  explicit drill-down, not cluttering the default Product view.

This applies to the terminal launcher, any admin panel, and agent-tool read
surfaces.

### 2.5 Product → component navigation

Workflows are reached primarily by **Product → component**, not by a flat list of
changes.

- A Product owns components (repos, services, capabilities).
- Each component lists its active workflows, specs, and recent changes.
- A workflow or change is **linked history** from the component view, not the
  top-level browse path.

---

## 3. Complete work-kind taxonomy

Concord covers the full spectrum of work a Product accumulates:

| Work kind | Typical workflow type | Notes |
|---|---|---|
| **Implementation** | 7-gate implementation change | Spec-driven code work; the heaviest type. |
| **Operations** | Ops runbook | Plan/approval/execute/health/rollback/cleanup; evidence at each step. |
| **Database work** | Ops runbook or implementation change | Schema/data migrations; often coupled with rollback. |
| **Break-fix** | Lightweight defect workflow | RCA → fix → verify; may be GitHub-issue-driven. |
| **Configuration** | Lightweight change or ops runbook | Infra/tooling config; must be reproducible/auditable. |
| **Infrastructure** | Ops runbook | Azure jobs, crons, deployed services; signal-aware. |
| **Options research** | Research / investigation | Compare alternatives; may resolve to "no change." |
| **Evidence research** | Research / investigation | Gather source evidence for a decision. |
| **Architectural decision / de-risking** | Architecture spike | Decides, rather than ships. Binding decision record; hard-blocks dependent Epic entries. See [`architecture-spike.md`](./architecture-spike.md). |
| **RCA (root-cause analysis)** | Investigation / static-analysis variant | Defect-driven; produces durable findings. |
| **Static-analysis variants** | Purpose-built analysis workflows | Architecture-inconsistency, AI-slop, spec/impl drift, etc. |

Each kind gets a registered workflow type with the appropriate steps, artifacts,
and completion criteria. A single workflow can spawn another: research may
surface a defect, which matures into a break-fix workflow, which may become an
implementation change.

---

## 4. Example workflow types

| Type | Work kind | Shape | Replaces / relates |
|---|---|---|---|
| **Epic / initiative** | Product-scoped finite initiative | Frame value/scope → order independent child work → maintain shared active context → close only when required children/conditions are terminal; no nested execution | CD-0009 resolves the former standalone Epic container into ordinary work identity. |
| **Implementation change** | Spec-driven code work | 7 gates, spec deltas, contract | The existing ADV workflow — kept as the heaviest type. |
| **Research / investigation** | Open-ended; may never become a change | Lightweight; gateless or few steps; ordinary `kind=research` work owning a CD-0009 active research pack; produces findings and may conclude `no change` | Today forced into premature changes or lost. |
| **Architecture spike** | Architectural decision / de-risking | Frame → research → options → optional throwaway POC → decision record → reviewer → user acceptance; flat (has tasks, no sub-spikes); no timebox | Peer to the implementation change. Distinct from research: research *may* resolve to "no change"; a spike *must* resolve to a decision, and that decision **binds until superseded**. Full model: [`architecture-spike.md`](./architecture-spike.md). |
| **Static-analysis workflows** | Code-quality / architecture analysis | Purpose-built per analysis kind; produces a report | **Replaces `adv-tron`, `adv-slop-scan`, `adv-arch-scan`, `adv-audit`** as proper workflow types. |
| **Ops runbook** | Operational procedure | Steps (plan/approval/execute/health/rollback/cleanup); evidence | inventory §2.4 / §3.5. |
| **Break-fix workflow** | Defect / RCA | RCA → fix → verify; GitHub issue as canonical home | Builds on cross-project GH-issue integration. |
| **Database workflow** | Schema/data migration | Ops-runbook shape with migration-specific rollback | Reuses ops runbook primitives. |
| **Configuration workflow** | Infra/tooling config | Lightweight change or ops-runbook shape | Reproducible, auditable. |

Static analysis deserves **variants** (the operator's point): architecture-
inconsistency detection, AI-slop detection, spec/impl drift audit, arch-detection
— each a purpose-built workflow, not one overloaded skill.

---

## 5. Replacing the ad-hoc analysis skills

Today's `adv-tron` / `adv-slop-scan` / `adv-arch-scan` / `adv-audit` are skills —
procedural methodology loaded on demand, outside the tracked lifecycle. Concord
promotes each to a **first-class workflow type** with:

- A defined shape (inputs, analysis steps, report artifact, completion).
- Durable output (the report is tracked, queryable — not ephemeral).
- Composability (a research workflow can spawn an analysis workflow; a change can
  trigger a drift audit).

The skills may remain as the *methodology* the workflow type loads, but the
**workflow** is the tracked, durable thing.

---

## 6. Relationship to work-type taxonomy

- **Work types** (inventory §3.11: ideas / bugs / optimization) describe *what
  kind of work* something is.
- **Workflow types** (this doc) describe *how that work is executed*.
- Mapping is many-to-many: a bug might use the break-fix workflow or the
  implementation-change workflow; an idea might start as research then mature
  into implementation. The taxonomy tags the work; the workflow type shapes the
  execution.

---

## 7. Composition and remaining implementation design

1. **Composition model — resolved:** forward-link the successor workflow/work item,
   then let the current workflow finish. Each retains independent authority and
   recovery. No nested child execution or parent waiting (CD-0006 R1).
2. **Per-type shape** — each code-defined type declares its own steps/evidence under
   the shared coordination constitution and CD-0007 conformance floor. Concrete
   step shapes remain implementation decisions, not one universal gate palette.

---

## 8. Relationship to other docs

| Doc | Link |
|---|---|
| `feature-inventory.md` §3.13 | The capability entry. |
| [`architecture-spike.md`](./architecture-spike.md) | Full model for the architecture-spike type (§3 taxonomy, §4 example table). |
| `product-data-model.md` §6 | Product → component navigation. |
| [`workflows.md`](./workflows.md) §1 | The 7-gate implementation change (Transfer) — one workflow type among many. |
| `specs-as-laws.md` | Workflow evolution couples to spec evolution. |
| `design-constraints.md` §3 | Workflow evolution without migration — now applies to a *plurality* of types. |

---

*One workflow was a start. A plurality of purpose-built workflows is the goal —
each work kind gets the shape it deserves, navigated from the Product-first
terminal launcher.*

# Concord Workflows: A Plurality of Purpose-Built Types

> **Status:** Aligned v5 with CD-0006/CD-0007/CD-0041. Companion to [`README.md`](./README.md),
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
- **Terminal launcher:** the CD-0108 replacement owns the daily browse and
  session bootstrap role. ZLauncher retires only after the replacement
  acceptance test passes.
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
  do not fit the 7-gate shape. They are either forced into it or remain outside
  the tracked lifecycle.
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
| **Value statement** | One sentence stating the Initiative/work value: what it delivers and why it matters. |
| **Outcome contract** | The premise, required end-state, and candidate set the type requires at approval. See §2.1a. |
| **Product-truth effect** | Closed `changes_product_truth` classification. `true` requires CD-0041's architecture binding; `false` cannot write Domain identity, Domain relations, Product law, or Product behavior. |
| **Staleness rule** | Whether execution is blocked while the workflow's inputs or upstream state are stale. |
| **Active visibility** | Which gates/problems are surfaced by default vs. hidden behind history. |

Workflow types are **registered** (not hardcoded as one-off skills). New types can
be added as Concord evolves — this is the "different types of workflows" the
operator named.

### 2.1 Value statement invariant

Every Initiative and every work item must carry a **one-sentence value statement**,
recorded at creation and reviewed at each gate. It is not a marketing blurb; it
answers:

> *If this succeeds, what concrete product capability or risk is changed?*

The value statement is part of the durable workflow record and is visible in the
Product → Domain browse surface.

The value statement answers *why the work matters*. It does not answer *what must
be true when the work is done*, and it is not falsified by a weaker delivery — a
value statement about removal survives an archive that removes nothing. That
second question belongs to the outcome contract below.

### 2.1a Outcome contract

Accepted CD-0012 gives the value statement a binding counterpart. Every work item
additionally carries a three-part **outcome contract**, each part with its own
revision authority:

| Part | Content | Revision authority |
|---|---|---|
| **Premise** | Why this work exists, in the operator's terms. | Operator only; revision supersedes rather than edits. |
| **Required end-state** | Falsifiable postconditions carrying the operative verb. | Operator only, at planning approval. |
| **Candidate set** | The specific instances the end-state ranges over. | The executing agent, during execution, by appended event. |

The required end-state is approved at planning beside CD-0006 D10's spec mandate,
and verified at completion. A delivered end-state may only be stronger than the
approved one, never weaker. Work discovered mid-execution that lies outside the
approved end-state forward-links as successor work under CD-0006 R1; it never
substitutes.

Every workflow type carries an outcome contract; only the verb differs by type.
Research concluding `no change` and a spike concluding `insufficient evidence`
both satisfy their required end-state. Binding form and per-type verbs:
[`decisions/CD-0012-bind-stated-goals-to-delivered-outcomes.md`](./decisions/CD-0012-bind-stated-goals-to-delivered-outcomes.md).

### 2.2 Factored lifecycle truth

See [`clarifications.md`](./clarifications.md) R3.

### 2.3 Structural staleness review / block rules

See [`clarifications.md`](./clarifications.md) R2.

CD-0041 adds one non-optional preflight for Product-changing workflow types. An
approved contract carries one home Domain, all affected Domains, exact governing
law revisions, authorized law/Domain/relation modifications, and law-linked
verification obligations. Overlap with another nonterminal Product-changing
contract requires a current, version-pinned resolution before both items hold
execution authority. The same check reruns at every consequential mutation.

### 2.4 Minimal active-work visibility

Default Concord views are intentionally minimal:

- Show **active gates** and **active problems** first.
- Show **what blocks execution right now**.
- Completed history, archived work, and passive context are available through
  explicit drill-down, not cluttering the default Product view.

This applies to the terminal launcher, any admin panel, and agent-tool read
surfaces.

### 2.5 Product → Domain navigation

Workflows are reached primarily by **Product → Domain**, not by a flat list of
changes or Initiatives.

- A Product owns canonical Domains; Projects and resources attach without becoming
  architecture identity.
- Each Domain lists current law, typed dependencies, active workflows, evidence,
  decisions, resources, and recent changes.
- A workflow or change is **architecture-bound history** from the Domain view, not the
  top-level browse path.
- Initiative is a secondary business/outcome overlay across Domain-bound work.

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
| **Architectural decision / de-risking** | Architecture spike | Decides, rather than ships. Binding decision record; hard-blocks dependent Initiative entries. See [`architecture-spike.md`](./architecture-spike.md). |
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
| **Initiative** | Product-scoped finite business/outcome context | Frame value/scope → order independent Domain-bound work → maintain shared active context → close only when required entries/conditions are terminal; no nested execution or architecture authority | CD-0041 and CD-0042: #196 establishes Initiative on the pre-go-live primary path; Initiative remains an ordinary work identity. |
| **Implementation change** | Spec-driven code work | 7 gates, spec deltas, contract | The existing ADV workflow — kept as the heaviest type. |
| **Research / investigation** | Open-ended; may never become a change | Lightweight; gateless or few steps; ordinary `kind=research` work owning a CD-0009 active research pack; produces findings and may conclude `no change` | Today forced into premature changes or lost. |
| **Architecture spike** | Architectural decision / de-risking | Frame → research → options → optional throwaway POC → decision record → reviewer → user acceptance; flat (has tasks, no sub-spikes); no timebox | Peer to the implementation change. Distinct from research: research *may* resolve to "no change"; a spike *must* resolve to a decision, and that decision **binds until superseded**. Full model: [`architecture-spike.md`](./architecture-spike.md). |
| **Static-analysis workflows** | Code-quality / architecture analysis | Coordinates an external analysis run and records its report and verdict | External analysis tools own scanner implementation; Concord owns the tracked workflow. |
| **Ops runbook** | Operational procedure | Steps (plan/approval/execute/health/rollback/cleanup); evidence | inventory §2.4 / §3.5. |
| **Break-fix workflow** | Defect / RCA | RCA → fix → verify; GitHub issue as canonical home | Builds on cross-project GH-issue integration. |
| **Database workflow** | Schema/data migration | Ops-runbook shape with migration-specific rollback | Reuses ops runbook primitives. |
| **Configuration workflow** | Infra/tooling config | Lightweight change or ops-runbook shape | Reproducible, auditable. |

Static analysis supports purpose-built variants, such as architecture-
inconsistency detection, AI-slop detection, and spec/implementation drift audit.
The host selects and runs an external tool. Concord records the declared scope,
report, evidence, and verdict.

---

## 5. Coordinating external analysis tools

Analysis tooling is an external native authority. Concord does not implement or
ship scanners. The `static_analysis` workflow commissions the external run and
records its scope, report, evidence, and verdict. Host-owned methodology selects
the tool and analysis procedure. This boundary follows
[`predecessor-operational-coverage.md` §3](./predecessor-operational-coverage.md#3-implementation-changes),
[CD-0043 D1](./decisions/CD-0043-host-owned-lane-methodology.md#d1-lane-methodology-is-never-concord-durable-state),
and [CD-0055 D4](./decisions/CD-0055-repository-check-authority.md#d4-heuristic-judgment-warns-it-never-blocks).

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
| `product-data-model.md` §6 | Product → Domain navigation. |
| [`workflows.md`](./workflows.md) §1 | The 7-gate implementation change (Transfer) — one workflow type among many. |
| `specs-as-laws.md` | Workflow evolution couples to spec evolution. |
| `design-constraints.md` §3 | Workflow evolution without migration — now applies to a *plurality* of types. |

---

*One workflow was a start. A plurality of purpose-built workflows is the goal —
each work kind gets the shape it deserves, navigated from the Product-first
terminal launcher.*

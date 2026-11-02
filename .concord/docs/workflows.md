# Concord Workflows

> **Authority:**
> [CD-0218](../law/decisions/CD-0218-the-live-workflow-catalog-retains-three-execution-families.md)
> and
> [CD-0229](../law/decisions/CD-0229-workflows-are-composed-from-shared-parts-and-a-decision-model-sets-each-work-items-size.md).
> [CD-0242](../law/decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md) D2, D4, D6, D13, and D14 govern generated definitions, current state, and finite snapshots.
> Companion to [`README.md`](./README.md),
> [`specs-as-laws.md`](./specs-as-laws.md),
> [`product-data-model.md`](./product-data-model.md).
> **Purpose:** New execution selects one of three retained workflow families
> (CD-0218 D1); §4 lists them. Each family has a plain Go definition;
> work has no size class (§7; CD-0229).
> **Primary surface:** Defined by [`../law/constitution/priorities.md`](../law/constitution/priorities.md); an admin
> panel or web UI is optional and secondary.

**Approval amendment:** [CD-0243 D7](../law/decisions/CD-0243-four-roles-and-six-operator-approval-classes.md#d7-the-remaining-owners-use-the-same-approval-boundary)
qualifies current outcome-contract consent and revision authority. Definitions
derive from one canonical source with no released definition version retained
for history (CD-0242 D14). The operator-owned release cutover retains its
existing requirements.

## TL;DR

The live workflow catalog retains three execution families for new work
(CD-0218 D1; §4). Workflows are plain Go definitions, and
work has no size class (§7; CD-0229). Public projections
are generated from those definitions, and no released workflow-definition
version, pin, or catalog list is retained for history (CD-0242 D14). The cutover
is one documented migration with a verified snapshot and the operator's approval
of its destructive operation (CD-0242 D13).

The primary operator interacts through host-started agent sessions: the
operator starts each session, and agents use Concord's typed work and workflow
operations. Optional grid/table views are projections, not the
primary interface.

The canonical Concord priorities are maintained in [`../law/constitution/priorities.md`](../law/constitution/priorities.md); this document
follows them without restating them.

---

## 0. Audience and primary surface (resolved)

- **Primary operator surface:** follows [`../law/constitution/priorities.md`](../law/constitution/priorities.md).
  The host starts each session and owns its worktree placement (CD-0088 D1).
- **Entry responsibility:** narrow start/resume actions. Substantive
  workflow decisions happen after selection.
- **Human questions:** provide purpose, relevant context, concrete examples, and
  consequences before asking one decision at a time.
- **Web / admin panel:** optional; a grid/table projection for humans, not the
  daily operating surface.
- **Session entry:** the host owns operator session start and resume
  (CD-0078) and worktree lifecycle (CD-0088 D1). Concord derives session
  context; no Concord command starts a session or owns its worktree.
- **Agent surface:** CD-0005's eight scenario-validated tools through the accepted
  `concord.ts` custom-tool adapter and short-lived Go CLI. No plugin/MCP v1 and no
  separate human-only GUI required for correctness.

---

## 1. The shift: from one workflow to a plurality

Concord owns durable progression, branching, attempts, evidence, and recovery
across its workflow families; each launch is one admitted attempt with its own
authority (CD-0242 D6). Native systems still execute git, CI, cloud,
database, and other external effects. Arbitrary inline workflow languages are
out of scope.

- **Families:** new execution selects one of three retained families; a
  retired ref refuses before any capture or contract write (CD-0218 D1).
  §4 lists them.
- **Canonical definitions:** plain Go definitions own the generated public
  projections, and no released workflow-definition version, pin, or catalog list is
  retained for history (CD-0242 D14). No event log, fold, upcaster, or replay
  exists; the store keeps current state and finite snapshots (CD-0242 D2).

---

## 2. The workflow-type model

A **workflow family** is a code-defined execution type in the registry. Its
definitions are plain Go values (§7; CD-0229 D1), and the live
catalog retains exactly three families (CD-0218 D1). What a definition fixes:

| Aspect | Meaning |
|---|---|
| **Steps** | The steps the family's definition declares (CD-0229 D1). |
| **Artifacts** | What it produces (a spec delta? a report? findings? nothing durable?). |
| **Completion criteria** | How it's "done" (a contract satisfied? a report submitted? a signal fired?). |
| **Value statement** | One sentence stating the work value: what it delivers and why it matters. |
| **Outcome contract** | The premise, required end-state, and candidate set the family requires at approval. See §2.1a. |
| **Product-truth effect** | Closed `changes_product_truth` classification; architecture bindings and permitted law revisions follow [CD-0222 D5](../law/decisions/CD-0222-architecture-bound-product-law-with-package-boundary-code-ownership.md), as amended by [CD-0230 D4](../law/decisions/CD-0230-the-conflict-rules-are-one-checked-table-and-an-agent-changes-law-that-is-not-serious.md). |
| **Staleness rule** | Whether execution is blocked while the workflow's inputs or upstream state are stale. |
| **Active visibility** | Which gates/problems are surfaced by default vs. hidden behind history. |

The registry validates each definition and derives its canonical manifest and
digest. Common runtime action and payload implementations remain shared; no
parts catalog or composition compiler assembles definitions (CD-0229 D1).

### 2.1 Value statement invariant

Every work item must carry a **one-sentence value statement**,
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
additionally carries a three-part **outcome contract** with separate revision
rules:

| Part | Content | Revision authority |
|---|---|---|
| **Premise** | Why this work exists, in the operator's terms. | Authenticated coordinator through the ordinary complete contract update; operator decisions follow CD-0243. |
| **Required end-state** | Falsifiable postconditions carrying the operative verb. | Authenticated coordinator through the ordinary complete contract update; operator decisions follow CD-0243. |
| **Candidate set** | The specific instances the end-state ranges over. | The executing agent, during execution, by a recorded revision. |

The required end-state is approved at planning beside CD-0006 D10's spec mandate,
and verified at completion. A delivered end-state may only be stronger than the
approved one, never weaker. Work discovered mid-execution that lies outside the
approved end-state forward-links as successor work under CD-0006 R1; it never
substitutes.

Contract changes use [workflow-engine contract §4.1](../law/specs/workflow-engine-contract.md#41-ordinary-contract-updates).
The update appends the next retained version without resetting the step or
discarding evidence. Earlier version content remains immutable; current
criteria and candidate still need valid independent proof. An execution-time
law conflict still stops work for planning resolution before execution resumes.

Every workflow carries an outcome contract. Research may record `no_change`
when its approved outcome predicate allows it. The retained families' outcome
tokens and predicate forms are fixed in
[CD-0218 D1](../law/decisions/CD-0218-the-live-workflow-catalog-retains-three-execution-families.md).
Binding form and per-type verbs:
[`../law/decisions/CD-0012-bind-stated-goals-to-delivered-outcomes.md`](../law/decisions/CD-0012-bind-stated-goals-to-delivered-outcomes.md).

### 2.2 Factored lifecycle truth

See [`clarifications.md`](./clarifications.md) R3.

### 2.3 Structural staleness review / block rules

See [`clarifications.md`](./clarifications.md) R2.

CD-0222 adds one non-optional preflight for Product-changing workflow types. An
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

This applies to the session entry, any admin panel, and agent-tool read
surfaces.

### 2.5 Product → Domain navigation

Workflows are reached primarily by **Product → Domain**, not by a flat list of
changes or external planning groups.

- A Product owns canonical Domains and has Project memberships. Projects do not
  become architecture identity.
- Each Domain lists current law, typed dependencies, active workflows, evidence,
  decisions, and recent changes.
- A workflow or change is **architecture-bound history** from the Domain view, not the
  top-level browse path.
- Business grouping lives in Linear under CD-0213 and owns no architecture authority.

---

## 3. Complete work-kind taxonomy

Concord covers the full spectrum of work a Product accumulates:
implementation, operations, database work, break-fix, configuration,
infrastructure, options research, evidence research, architectural decisions,
RCA, and static-analysis runs. These are business work categories, not the
operator-kind vocabulary.

Operator kinds (task, bug, research, decision, other) and workflow families
are two closed vocabularies, kept separate (CD-0077 D1). The capture defaults
map operator kinds to families: tasks → `workflow.implementation`, bugs →
`workflow.break_fix`, research → `workflow.research`; decision and other
operator kinds require an explicit retained family (CD-0218 D1). Definitions
are plain Go values (§7; CD-0229 D1). Work has no size class.

A single workflow can spawn another: research may surface a defect, which
matures into a break-fix workflow, which may become an implementation change.

---

## 4. Example workflow types

| Family | Captures | Shape |
|---|---|---|
| **`workflow.implementation`** | Tasks default here | Declares the family's mandatory steps, including refine. The outcome contract, review acceptance and closure apply to every item (CD-0229 D4; CD-0242 D4). |
| **`workflow.break_fix`** | Bugs default here | One typed diagnosis with required reproduction, backlog-search, and root-cause sections, then repair, the mandatory refine pass, and verification (CD-0242 D4; CD-0138 D1). |
| **`workflow.research`** | Research defaults here | Records findings, conclusions, and recommendations; may conclude `no_change`; outcome predicates only (CD-0218 D1). [CD-0215](../law/decisions/CD-0215-retained-research-context.md) owns the research-pack capability. |

The retired families — `architecture_spike`, `ops_runbook`, `static_analysis`,
`generic_one_off` — select no new execution, and no alias maps a retired
family to a retained one (CD-0218 D1).
[CD-0215 D2](../law/decisions/CD-0215-retained-research-context.md) requires
an architecture spike to resolve through a separate reviewed and accepted
decision; CD-0218 assigns that obligation to the cutover owner's approved
successor set. Research reports do not enact legislation (CD-0218 D1).

External analysis covers architecture-inconsistency detection, AI-slop
detection, and spec/implementation drift audit, among others. The host
selects and runs the external tool; a retained family's tracked work records
the declared scope, report, and evidence (§5).

---

## 5. Coordinating external analysis tools

Analysis tooling is an external native authority. Concord does not implement
or ship scanners. Tracked analysis work runs in a retained family and records
the external run's scope, report, and evidence in the steps its definition
declares; that output is advisory input, not a verdict (CD-0138 D3).
Break-fix and implementation keep the mandatory `refine` step (CD-0242 D4).
The refine proof rule is
[CD-0192](../law/decisions/CD-0192-the-refine-pass-proves-a-declared-analysis-ran.md),
which amends
[CD-0138 D3](../law/decisions/CD-0138-mandatory-refinement-pass.md).
Host-owned methodology selects the tool and analysis procedure. This boundary
follows
[CD-0043 D1](../law/decisions/CD-0043-host-owned-lane-methodology.md#d1-methodology-is-not-durable-coordination-state),
and [CD-0055 D4](../law/decisions/CD-0055-repository-check-authority.md#d4-heuristic-judgment-warns-it-never-blocks).

---

## 6. Relationship to work-type taxonomy

- **Business work categories** (ideas / bugs / optimization;
  the taxonomy in §3) describe *what the work is*.
- **Workflow families** describe *how that work executes*; §3 maps operator kinds to families.

---

## 7. Composition

[CD-0229](../law/decisions/CD-0229-workflows-are-composed-from-shared-parts-and-a-decision-model-sets-each-work-items-size.md)
is accepted law. This section states its model, not the current implementation
status.

1. **Composition across work items — resolved:** forward-link the successor
   workflow/work item, then let the current workflow finish. Each retains
   independent authority and recovery. No nested child execution or parent
   waiting (CD-0006 R1).
2. **Plain definitions:** each family directly declares its step graph, actions,
   evidence, outcomes and recovery routes in a Go value. The registry validates
   it and derives its canonical manifest and digest. Public schemas and tool
   contracts generate from those definitions, without a parts catalog or
   composition compiler (CD-0229 D1; CD-0242 D14).
3. **Mandatory stages:** each live family's definition supplies its stages.
   Work has no size classification or size-derived review gate (CD-0229 D4).
   Main may add research, architecture or planning depth but cannot waive a
   mandatory stage or operator approval (CD-0242 D4).

---

## 8. Relationship to other docs

| Doc | Link |
|---|---|
| `product-data-model.md` §6 | Product → Domain navigation. |
| `specs-as-laws.md` | Workflow evolution couples to spec evolution. |
| `design-constraints.md` §3 | Workflow evolution constraints; public projections derive from the canonical Go definitions (§7; CD-0229 D1; CD-0242 D14). |
| [CD-0218](../law/decisions/CD-0218-the-live-workflow-catalog-retains-three-execution-families.md) | The three retained execution families (CD-0218 D1); the store keeps current state, and the cutover is one documented migration (CD-0242 D2, D13). |
| [CD-0229](../law/decisions/CD-0229-workflows-are-composed-from-shared-parts-and-a-decision-model-sets-each-work-items-size.md) | Plain workflow definitions without size classification. |

---

*New execution runs in one of the three retained families (§4), with plain Go
definitions (§7) that own generated public projections (CD-0242 D14).*

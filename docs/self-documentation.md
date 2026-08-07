# Concord Self-Documentation

> **Status:** Aligned v3 with CD-0006. Companion to [`README.md`](./README.md),
> [`feature-inventory.md`](./feature-inventory.md),
> [`design-constraints.md`](./design-constraints.md),
> [`product-data-model.md`](./product-data-model.md),
> [`specs-as-laws.md`](./specs-as-laws.md),
> [`workflows.md`](./workflows.md).
> **Purpose:** Concord is self-documenting — users/agents can **browse the spec law
> by Product and component** and **see into the durable documents** every
> workflow creates. This doc covers the self-documentation surfaces *and* the
> optimal IO/storage/memory model for them.
> **Primary surface:** Terminal-first tools; an optional admin panel is a
> projection, not the operating center.
> **Origin:** User direction, 2026-07-25.

## TL;DR

Concord is self-documenting. Open it and you can: (1) **browse the spec law
organized by Product and component**; (2) **see into the durable documents** every
workflow type declares. These surfaces are **first-class read targets with
optimal IO/storage/memory** — not opaque files on disk. The default view is
minimal: active gates and problems first; history is one click down.

The canonical Concord priorities are maintained in [`priorities.md`](./priorities.md); this document
follows them without restating the ranked list.

---

## 1. The three self-documentation surfaces

### 1.1 Spec law, browsable by Product and component
- The spec system (capabilities → requirements → scenarios) is **navigable by
  Product → component**. Open Concord → see the Product → component tree → drill
  into a component's capabilities, requirements, scenarios, and deltas.
- ADV has `adv_spec` (list/show/search); Concord makes it a **browsable,
  navigable surface** — via tools and, optionally, in a lightweight admin panel.
- The primary operator surface is the **Product-first terminal launcher**, not a
  web admin panel. Any grid/table view is an optional projection.
- Connects to [`specs-as-laws.md`](./specs-as-laws.md): the laws are **visible**,
  not hidden. A legislator can't govern what they can't read.

### 1.2 Durable workflow documents, visible
- Every purpose-built workflow type declares the durable documents/knowledge it
  produces. The generic one-off type uses a bounded generic record; it does not
  inherit the implementation workflow's full document set.
- These must be **visible** — a user can open Concord and trace any change's full
  document trail, end to end.
- Not opaque files; **navigable, linked, queryable**.
- They are reached from the **Product → component** view, not from a flat list.

### 1.3 Lifecycle truth, factored
- See [`clarifications.md`](./clarifications.md) R3.
- Self-documentation surfaces never silently duplicate or repair lifecycle state.
- If a projection is stale, the surface marks it stale rather than guessing.
- Concord operational memory owns live state, blockers, approvals, versions, and
  active relations. Versioned Product knowledge owns accepted laws, decisions,
  runbooks, and durable narratives. Browse/search/launcher views are rebuildable
  projections. The same fact never has two authorities (CD-0006 D7).

---

## 2. Optimal IO / storage / memory (the sharp requirement)

For **each** of these surfaces, the IO/storage/memory model must be **optimal** —
not an afterthought. Self-documentation is a **read-heavy** surface (users/agents
browse constantly); if it's slow or memory-heavy, the whole platform feels heavy.

### 2.1 Principles for the doc-store model

- **Durable knowledge lives in git markdown** — compacted lessons, completed-
  work notes, and decision records are committed to the repo as one-way
  markdown writes (CD-0002 §2c). They are durable, versioned, diffable, and
  greppable; never an authority input for live transition decisions. Self-
  documentation surfaces read this tier but never write back to it.
- **Content-addressed** — documents referenced by hash; immutable; deduped;
  cheap to cache/proxy. (Couples to `design-constraints.md` §4.)
- **Projection-derived reads** — browse views are projections over the doc store;
  reads don't re-parse raw files. Sub-100ms browsing.
- **Memory-bounded** — browsing a large spec/component tree must **not** load
  everything; lazy / paginated / streaming reads. A Product with thousands of
  changes + specs browses as fast as a small one.
- **Append-only history** — documents are versioned append-only (couples to
  no-history-repair, `design-constraints.md` §4). Browsing shows current state;
  history is queryable but never mutated.
- **Locality** — a component's specs + its changes' documents are co-located and
  navigable together (P04; see [`product-data-model.md`](./product-data-model.md)
  §3, §6).
- **Factored lifecycle** — the orchestrator is the single source of lifecycle
  truth; self-documentation surfaces read it, never write a shadow copy.

### 2.2 Why this is foundational
Optimal IO/storage/memory here is foundational to the *"fast + seamless"* feel
(`design-constraints.md` §2) and to the chaos-proof architecture
([`clarifications.md`](./clarifications.md) R3) — a heavy, lock-prone doc store would reintroduce
exactly the failure modes Concord exists to eliminate.

### 2.3 Minimal active-work visibility

Default self-documentation views are minimal by design:

- Show **active gates** and **active problems** first.
- Show **what blocks execution right now**.
- Completed history, archived changes, and historical projections are available
  through explicit drill-down, not cluttering the default Product/component view.
- This applies to the terminal launcher, the optional admin panel, and agent-tool
  read surfaces.

### 2.4 Authority/freshness and related-work impact

Self-documentation surfaces display authority/freshness indicators, not hidden
guesses. CD-0006 R3's declared edges, completion notices, boundary verdicts, and
version stamps are authoritative; browse views must not invent or silently clear
impact state.

---

## 3. Open questions

1. **Doc-store format** — content-addressed files? a doc DB? an append-only log +
   projections? (Couples to `design-constraints.md` §4 storage decision.)
2. **Browse UX** — terminal launcher tree? tool-based queries? optional admin panel?
   all three?
3. **Versioning granularity** — per-document versions, or per-change snapshots?
4. **Memory-bounding strategy** — lazy loading, cursor pagination, streaming?
5. **Staleness signal source** — derived from orchestrator events, or computed
   on read from base-branch / dependency timestamps?

---

## 4. Relationship to other docs

| Doc | Link |
|---|---|
| `feature-inventory.md` §3.12 | The capability entry. |
| `specs-as-laws.md` | The laws being browsed. |
| `product-data-model.md` §6 | Specs/artifacts scoped to a Product/component (locality). |
| `workflows.md` §2.2–§2.5 | Factored lifecycle, minimal active-work visibility, staleness rules. |
| `design-constraints.md` §4 + §9 | The storage model and self-documentation constraint for these surfaces. |

---

*Self-documentation is not a feature bolted on at the end — it is a read-surface
whose storage model, navigation model, and staleness rules are designed from day
one.*

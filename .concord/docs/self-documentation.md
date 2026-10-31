# Concord Self-Documentation

> **Status:** Aligned v4 with CD-0006/CD-0222. Companion to [`README.md`](./README.md),
> [`design-constraints.md`](./design-constraints.md),
> [`product-data-model.md`](./product-data-model.md),
> [`specs-as-laws.md`](./specs-as-laws.md),
> [`workflows.md`](./workflows.md).
> **Purpose:** Concord is self-documenting — users/agents can **browse current
> Product law through its Domain architecture** and **see into the durable documents** every
> workflow creates. This doc covers the self-documentation surfaces *and* the
> optimal IO/storage/memory model for them.
> **Primary surface:** Terminal-first tools; an optional admin panel is a
> projection, not the operating center.
> **Origin:** User direction, 2026-07-25.

## TL;DR

Concord is self-documenting. Open it and you can: (1) **browse current Product
law through Product → Domain**; (2) **see into the durable documents** every
workflow type declares. These surfaces are **first-class read targets with
optimal IO/storage/memory** — not opaque files on disk. The default view is
minimal: active gates and problems first; history is one click down.

The canonical Concord priorities are maintained in [`../law/constitution/priorities.md`](../law/constitution/priorities.md); this document
follows them without restating the ranked list.

---

## 1. The three self-documentation surfaces

### 1.1 Spec law, browsable by Product and Domain
- The spec system (capabilities → requirements → scenarios) is **navigable by
  Product → Domain**. Open Concord → reach current Domain law and unresolved
  work overlap → drill into a Domain's current capabilities,
  requirements, scenarios, decisions, and deltas.
- Public predecessor evidence proved the value of bounded spec list/show/search;
  Concord redesigns that outcome as a Domain-bound, browsable surface rather than
  calling or mirroring predecessor runtime state.
- The primary operator surface follows [`../law/constitution/priorities.md`](../law/constitution/priorities.md).
  Any grid/table view is an optional projection.
- Connects to [`specs-as-laws.md`](./specs-as-laws.md): the laws are **visible**,
  not hidden. A legislator can't govern what they can't read.

### 1.2 Durable workflow documents, visible
- Every workflow type declares the durable documents/knowledge it produces.
- These must be **visible** — a user can open Concord and trace any change's full
  document trail, end to end.
- Not opaque files; **navigable, linked, queryable**.
- They are reached from the **Product → Domain** view, not from a flat list or
  an external planning hierarchy.

### 1.3 Lifecycle truth, factored
- See [`clarifications.md`](./clarifications.md) R3.
- Self-documentation surfaces never silently duplicate or repair lifecycle state.
- If a projection is stale, the surface marks it stale rather than guessing.
- Concord operational memory owns live state, blockers, approvals, versions, and
  active relations. Versioned Product knowledge owns accepted laws, decisions,
  runbooks, and durable narratives. Browse/search views are rebuildable
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
- **Memory-bounded** — browsing a large Product/Domain law graph must **not** load
  everything; lazy / paginated / streaming reads. A Product with thousands of
  changes + specs browses as fast as a small one.
- **Current state from Git** — the Git knowledge home owns documents, and the
  store keeps current state and finite snapshots, not history (CD-0242 D1–D2;
  couples to `design-constraints.md` §4). Browsing shows current state.
- **Locality** — a Domain's law + its changes' documents are co-located and
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
  through explicit drill-down, not cluttering the default Product/Domain view.
- This applies to the optional admin panel and agent-tool
  read surfaces.

### 2.4 Authority/freshness and related-work impact

Self-documentation surfaces display authority/freshness indicators, not hidden
guesses. CD-0006 R3's composition edges, accepted verdicts, and pinned revision
checks are authoritative; browse views must not invent or silently clear state.

CD-0222 adds the active architecture footprint: affected Domains and exact law pins.
Shared Domains block no boundary; concurrent edits reconcile through Git under
CD-0222 D6. Business grouping lives in Linear through the Linear MCP server under
CD-0213 and supplies no execution authority.

---

## 3. Open questions

1. **Browse UX** — tool-based queries? optional admin panel? both?
2. **Memory-bounding strategy** — lazy loading, cursor pagination, streaming?

[CD-0242 D1–D2](../law/decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d1-each-kind-of-state-has-one-owner)
owns Git knowledge and current-state storage; [D7](../law/decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d7-a-revision-invalidates-only-what-depended-on-it)
owns digest-bound invalidation.

---

## 4. Relationship to other docs

| Doc | Link |
|---|---|
| `specs-as-laws.md` | The laws being browsed. |
| `product-data-model.md` §6 | Law/artifacts scoped to a Product/Domain (locality). |
| `workflows.md` §2.2–§2.5 | Factored lifecycle, minimal active-work visibility, staleness rules. |
| `design-constraints.md` §4 + §9 | The storage model and self-documentation constraint for these surfaces. |

---

*Self-documentation is not a feature bolted on at the end — it is a read-surface
whose storage model, navigation model, and staleness rules are designed from day
one.*

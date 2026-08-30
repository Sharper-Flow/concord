# Concord Vertical Integration — lgrep / vision / episode

> **Status:** Aligned v2. Companion to [`README.md`](./README.md),
> [`product-data-model.md`](./product-data-model.md), [`workflows.md`](./workflows.md),
> [`clarifications.md`](./clarifications.md).
> **Purpose:** Record the ownership spectrum for lgrep, vision, and episode. The
> episode boundary is resolved; lgrep and vision retain the current lean.
> **Origin:** User direction, 2026-07-25 ("we may want to consider…").

## Resolved direction (interface/audience boundary)

This document is **only** about whether Concord should own, swallow, or
product-scope lgrep, vision, and episode. The terminal-launcher / admin-panel
question is resolved in [`clarifications.md`](./clarifications.md) R1:

- The **Product-first terminal launcher** is the primary operator surface.
- **ZLauncher** remains the session/project bootstrap layer; it is **not** a
  candidate for Concord's primary interface.
- Any **admin panel** or web UI is an optional projection, not the operating center.

The canonical Concord priorities are maintained in [`priorities.md`](./priorities.md); this document
follows them without restating the ranked list.

---

## Findings

The findings of this research are the Resolved direction directly below, the
Benefits and Risks of sections 5 and 6, and the Current lean of section 7
with its decision trigger in section 8. Sections 2 through 4 record the
question, the tools, and why it arises; section 9 records relationships to
other documents.

## The question

Should Concord OWN or SWALLOW lgrep (code search), vision (MCP host), and
episode (memory) — currently separate general-purpose tools in the stack — for
tighter vertical integration?

## What each tool is

| Tool | Role |
|---|---|
| **lgrep** | Local code intelligence (semantic + symbol search). The code index. |
| **vision** | MCP daemon that hosts/proxies MCP servers. The MCP infrastructure layer. |
| **episode** | Memory/learning store (decisions, gotchas, conventions, namespaced). |
| **Project launcher** | Session/project bootstrap layer. The implementation and packaging choice remain open; no private deployment path is part of this public snapshot. |

## Why the question arises

Concord is becoming the all-in-one platform for a Product. An agent working on a
Product naturally touches all three: searching code (lgrep), using MCP tools
(vision-hosted), recalling memory (episode). Today these are **global/shared, not
Product-aware**. Vertical integration could make them cohesive and Product-scoped.

---

## The spectrum

| Option | What it means | Integration | Cost / risk |
|---|---|---|---|
| **Status quo** | Tools stay global/shared, not Product-aware | Low | None — but no locality benefit |
| **Product-scoping** | Configured integrations receive Product context (product-scoped lgrep index, episode namespace, vision server set). Tools stay independent. | Medium — locality + cohesion | Low — orchestration layer, no tool rewrites |
| **Owning** | Concord owns the tools as subsystems; code lives in/with Concord; tools become Product-native. | High | Medium — scope growth, migration, loses generality |
| **Swallowing** | Concord absorbs + replaces the tools entirely. | Maximum | High — rewrites, maturity loss, risk |

---

## Benefits of deeper integration (pro)

- **Product-scoped locality:** each Product gets its own code index, memory
  namespace, MCP surface — agents see only their Product's context. Strong fit
  with locality-of-behavior ([`product-data-model.md`](./product-data-model.md) §3).
- **Cohesion:** one system, one mental model, one deploy — vs four fragmented
  tools.
- **Product-mindset:** code search, memory, and MCP become Product-aware
  capabilities, not global utilities.

## Risks (con)

- **Scope explosion:** Concord's mandate grows from "project/ops platform" to
  "also a code-search engine + MCP host + memory store." Tension with the
  **tightly scoped** pillar ([`priorities.md`](./priorities.md) Operating envelope).
- **Maturity / generality:** these are mature, general-purpose tools with their
  own deploy stories and possibly other consumers. Swallowing couples them to
  Concord and loses generality.
- **Migration risk:** existing sessions, indexes, memory, MCP configs would need
  to move.
- **Diminishing returns:** most of the locality benefit comes from
  **Product-scoping**, not from owning/swallowing. Owning adds cost for marginal
  extra integration.

---

## Current lean for lgrep and vision

**Product-scoping first; defer owning/swallowing until measured need.**

- Get the locality/cohesion benefit **cheaply** by giving each Product scoped
  instances of the existing tools (Concord orchestrates product context; tools
  stay independent).
- This honors *"don't pay until it hurts"* and *"tightly scoped."*
- Re-evaluate owning/swallowing only if product-scoping proves insufficient AND a
  concrete integration pain can't be solved without ownership.

### Resolved episode boundary

CD-0086 resolves episode as an external, optional authority. Concord does not
require it for the build, installation, startup, storage, authorization, or any
core workflow. Operators can use every Concord capability without episode.

When an operator configures episode, its memory is Product-scoped and episode
retains authority for recall, memory storage, and promotion state. Concord owns
Product law and the formalized promotion target. The integration adds memory. It
does not become a correctness dependency for Concord.

Automatic Product derivation and a real multi-project probe remain episode-side
follow-up work. That evidence can reopen the ownership decision if the external
boundary cannot resolve Product and work identity, or if maintaining the bridge
costs more than ownership. The probe does not block the current direction.

## Decision trigger (what would move us to own/swallow)

- A concrete capability that requires a tool's internals to be Product-aware in a
  way orchestration can't provide.
- Measured pain from the tools being separate that product-scoping can't resolve.
- A rewrite opportunity where absorbing the tool is cheaper than maintaining the
  bridge.

Until one of these fires, **product-scoping is the answer.**

### Triggers are evaluated per tool

The spectrum and decision triggers apply per tool. lgrep and vision remain open
under [`clarifications.md`](./clarifications.md) C8. The episode boundary is
resolved by R7 and issue #46.

episode consumes predecessor wisdom and reflection state. lgrep and vision index
and host general-purpose data. Evidence about one tool does not move the boundary
for another tool.

## Promotion receiving contract (episode → Concord knowledge)

episode may graduate a memory to `Promoted { target }`, where `target` names a
Concord record and stays opaque to episode. Concord owns the destination, so this
section defines what receiving that promotion means.

- **Target identity.** A promotion target is a formalized knowledge record of
  kind `spec` or `decision`, resolved through the knowledge manifest. A target
  that does not resolve is `knowledge_missing`, and the promotion is not
  receivable. Resolution is by manifest path plus sha256, so a promotion names
  a record version, not a moving document.
- **Back-link.** When a promotion contributes content, the receiving record
  cites the originating episode memory by its episode identity in the record's
  sources. The back-link is recorded provenance in the document, not new
  runtime state.
- **Acknowledgement.** Acknowledgement is structural: the target resolves at
  the version the promotion named. Concord records no runtime event, because
  knowledge records carry no runtime write surface. If a write surface is ever
  accepted, acknowledgement binds there under its own decision.
- **Recall exclusion.** Excluding a promoted memory from recall is episode's
  own behavior. Concord's side of the boundary is already law: a promoted
  memory reaches Product law only through the formalization procedure, and an
  unprocessed document carries no authority regardless of origin.

This contract defines the seam only. It adds no runtime surface, event, or
validation. When episode is absent, the seam stays inactive and no Concord
behavior changes.

---

## Relationship to other docs

| Doc | Link |
|---|---|
| [`clarifications.md`](./clarifications.md) C8 | Open question about lgrep / vision ownership, scoped separately from the launcher/interface decision (R1). |
| [`clarifications.md`](./clarifications.md) R7 | Resolved episode boundary: external, optional, and Product-scoped when configured. |
| `clarifications.md` R1 | Resolved launcher/interface direction: terminal launcher primary; ZLauncher is bootstrap only. |
| `product-data-model.md` §3 | Product-scoped instances are a locality mechanism. |
| [`priorities.md`](./priorities.md) Operating envelope | The guardrail against premature swallowing. |

---

*The episode boundary is resolved. The lgrep and vision direction remains a lean
that evidence can challenge. The launcher/interface question is separate.*

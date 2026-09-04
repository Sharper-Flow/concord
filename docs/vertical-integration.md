# Concord Vertical Integration — lgrep / vision

> **Status:** Aligned v3. Companion to [`README.md`](./README.md),
> [`product-data-model.md`](./product-data-model.md), [`workflows.md`](./workflows.md),
> [`clarifications.md`](./clarifications.md).
> **Purpose:** Record the ownership spectrum for lgrep and vision. Both retain
> the product-scoping lean. episode is removed by CD-0106 and is no longer a
> subject of this document.
> **Origin:** User direction, 2026-07-25 ("we may want to consider…").

## Resolved direction (interface/audience boundary)

This document is **only** about whether Concord should own, swallow, or
product-scope lgrep and vision. The terminal-launcher / admin-panel question
is resolved in [`clarifications.md`](./clarifications.md) R1:

- The **Product-first terminal launcher** is the primary operator surface.
- Per CD-0108 (2026-09-03), the launcher is remade as the **ZLauncher
  replacement** and absorbs the session bootstrap role. The R1 bootstrap
  split is superseded; see [`clarifications.md`](./clarifications.md) R1.
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

Should Concord OWN or SWALLOW lgrep (code search) and vision (MCP host) —
currently separate general-purpose tools in the stack — for tighter vertical
integration?

## What each tool is

| Tool | Role |
|---|---|
| **lgrep** | Local code intelligence (semantic + symbol search). The code index. |
| **vision** | MCP daemon that hosts/proxies MCP servers. The MCP infrastructure layer. |
| **Project launcher** | Session/project bootstrap layer. The implementation and packaging choice remain open; no private deployment path is part of this public snapshot. |

## Why the question arises

Concord is becoming the all-in-one platform for a Product. An agent working on a
Product touches both: searching code (lgrep) and using MCP tools
(vision-hosted). Today these are **global/shared, not Product-aware**. Vertical
integration could make them cohesive and Product-scoped.

---

## The spectrum

| Option | What it means | Integration | Cost / risk |
|---|---|---|---|
| **Status quo** | Tools stay global/shared, not Product-aware | Low | None — but no locality benefit |
| **Product-scoping** | Configured integrations receive Product context (product-scoped lgrep index, vision server set). Tools stay independent. | Medium — locality + cohesion | Low — orchestration layer, no tool rewrites |
| **Owning** | Concord owns the tools as subsystems; code lives in/with Concord; tools become Product-native. | High | Medium — scope growth, migration, loses generality |
| **Swallowing** | Concord absorbs + replaces the tools entirely. | Maximum | High — rewrites, maturity loss, risk |

---

## Benefits of deeper integration (pro)

- **Product-scoped locality:** each Product gets its own code index and MCP
  surface — agents see only their Product's context. Strong fit with
  locality-of-behavior ([`product-data-model.md`](./product-data-model.md) §3).
- **Cohesion:** one system, one mental model, one deploy — vs three fragmented
  tools.
- **Product-mindset:** code search and MCP become Product-aware capabilities,
  not global utilities.

## Risks (con)

- **Scope explosion:** Concord's mandate grows from "project/ops platform" to
  "also a code-search engine + MCP host." Tension with the **tightly scoped**
  pillar ([`priorities.md`](./priorities.md) Operating envelope).
- **Maturity / generality:** these are mature, general-purpose tools with their
  own deploy stories and possibly other consumers. Swallowing couples them to
  Concord and loses generality.
- **Migration risk:** existing sessions, indexes, and MCP configs would need
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

## Decision trigger (what would move us to own/swallow)

- A concrete capability that requires a tool's internals to be Product-aware in a
  way orchestration can't provide.
- Measured pain from the tools being separate that product-scoping can't resolve.
- A rewrite opportunity where absorbing the tool is cheaper than maintaining the
  bridge.

Until one of these fires, **product-scoping is the answer.**

### Triggers are evaluated per tool

The spectrum and decision triggers apply per tool. lgrep and vision remain open
under [`clarifications.md`](./clarifications.md) C8. lgrep indexes code and
vision hosts MCP servers; both are general-purpose data, and evidence about one
tool does not move the boundary for the other.

---

## Relationship to other docs

| Doc | Link |
|---|---|
| [`clarifications.md`](./clarifications.md) C8 | Open question about lgrep / vision ownership, scoped separately from the launcher/interface decision (R1). |
| [`clarifications.md`](./clarifications.md) R7 | episode is removed under CD-0106; the knowledge ladder is observation, lesson, decision. |
| `clarifications.md` R1 | Resolved launcher/interface direction: terminal launcher primary; ZLauncher is bootstrap only. |
| `product-data-model.md` §3 | Product-scoped instances are a locality mechanism. |
| [`priorities.md`](./priorities.md) Operating envelope | The guardrail against premature swallowing. |

---

*The lgrep and vision direction remains a lean that evidence can challenge. The
launcher/interface question is separate.*

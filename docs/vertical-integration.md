# Concord Vertical Integration — lgrep / vision / episode

> **Status:** Aligned v2. Companion to [`README.md`](./README.md),
> [`product-data-model.md`](./product-data-model.md), [`workflows.md`](./workflows.md),
> [`clarifications.md`](./clarifications.md).
> **Purpose:** Explore whether Concord should **own** or **swallow** lgrep, vision,
> and episode for tighter vertical integration. Record the spectrum, tradeoffs,
> and current lean.
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
| **Product-scoping** | Concord gives each Product scoped instances (product-scoped lgrep index, episode namespace, vision server set). Tools stay independent; Concord orchestrates product context. | Medium — locality + cohesion | Low — orchestration layer, no tool rewrites |
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

## Current lean

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

The spectrum, the lean, and the decision triggers above apply to all three tools,
but each tool reaches its trigger on its own evidence. As of 2026-08-14 the
clarification entries are split accordingly: lgrep and vision share
[`clarifications.md`](./clarifications.md) C8, and episode has its own entry C20
owned by issue #46.

The practical difference: episode consumes predecessor wisdom and reflection
state, so issue #46 owns its probe scope and evidence. lgrep and vision index and
host general-purpose data. A measured need against one tool is not evidence about
the others.

The split has since paid off. C20 resolved on 2026-08-30 as external, optional,
and Product-scoped when configured. C8 stays open for lgrep and vision, and their
lean is unchanged. Neither movement is evidence about the other.

## Promotion receiving contract (episode → Concord knowledge)

Issue #46 Item 2. episode may graduate a memory to `Promoted { target }`, where
`target` names a Concord record and stays opaque to episode. Concord owns the
destination, so this section defines what receiving that promotion means. The
emitter does not exist yet — episode's own spec 0010 defers promotion — so the
contract waits for it.

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

This contract defines the seam only. It adds no runtime surface, no event, and
no validation.

### episode is optional

Concord has no required dependency on episode. episode is not a build,
installation, startup, storage, authority, or core-workflow dependency. An
operator who does not configure episode keeps every Concord capability.

The promotion seam stays dormant when episode is absent. Nothing in this contract
runs without an emitter, because Concord records no runtime event and holds no
write surface for it. A knowledge record reached through the formalization
procedure carries the same authority whether or not episode ever existed.

---

## Relationship to other docs

| Doc | Link |
|---|---|
| [`clarifications.md`](./clarifications.md) C8 | Open question about lgrep / vision ownership, scoped separately from the launcher/interface decision (R1). |
| [`clarifications.md`](./clarifications.md) C20 | Resolved 2026-08-30: episode stays external, optional, and Product-scoped when configured. |
| `clarifications.md` R1 | Resolved launcher/interface direction: terminal launcher primary; ZLauncher is bootstrap only. |
| `product-data-model.md` §3 | Product-scoped instances are a locality mechanism. |
| [`priorities.md`](./priorities.md) Operating envelope | The guardrail against premature swallowing. |

---

*This is a consideration about tool ownership/integration, not a commitment. The
lean is explicit so it can be challenged with evidence rather than re-litigated
from scratch. The launcher/interface question is resolved separately and is not
re-opened here.*

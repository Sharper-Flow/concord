# Concord Capability Placement: The Shape Rubric

> **Status:** Draft v2. Companion to [`README.md`](./README.md),
> [`priorities.md`](./priorities.md),
> [`feature-inventory.md`](./feature-inventory.md),
> [`design-constraints.md`](./design-constraints.md).
> **Purpose:** Every command/tool/policy goes in **exactly the right place** by
> shape. This doc is the rubric for deciding *where* a capability lives (plugin /
> MCP / instructions / skill / command / host script / state / external native
> authority), and the discipline to keep that guidance **dynamic** as tooling
> evolves.
> **Origin:** User direction, 2026-07-25; expanded for ownership-aligned
> placement, 2026-07-26.
> **Tool-surface hold:** This rubric still governs **authority before placement**,
> but its Advance-era host/MCP examples do not authorize Concord tool names,
> counts, or placements. TS1–TS9 in [`clarifications.md`](./clarifications.md)
> decide the minimal surface first; this rubric places only accepted capabilities.

## TL;DR

Concord makes **every placement decision correctly and deliberately.** When a new
capability is needed, we know exactly where it goes — owned by plugin, MCP,
instructions, skill, command, host script, durable state, or the native system
that already enforces it — based on its shape. We **keep the rubric dynamic**:
as tooling evolves, placements are re-evaluated so we stay cutting-edge. No
default dumping ground.

---

## 1. The placement surfaces (our stack)

| Surface | What it is | Characteristics |
|---|---|---|
| **External native authority** | The system that already owns the responsibility | GitHub Actions, the database, the cloud provider, the OS process manager, provider APIs. Holds canonical enforcement; Concord observes, links, coordinates. |
| **Host plugin tools** (`adv_*`) | Tools registered in the plugin, called top-level by agents | Mutations + reads; host-coupled (OpenCode); validated/authorized/approved |
| **MCP tools** (`tools.*`) | Standard protocol tools, cross-client | Primarily reads; cross-client; CodeMode-exposed |
| **Instructions** (always-on) | Policy/routing loaded into every agent prompt | Behavioral, not executable; universal guidance |
| **Skills** | Methodology loaded on demand | Procedural; not executable code; heavy guidance |
| **Commands** (`/adv-*`) | User entry points / workflow contracts | Read as contracts; executed inline |
| **Host scripts** (`~/.local/bin/`) | CLI tools | Executable; cross-tool; durable binaries |
| **Durable state** (specs, changes, …) | The data layer | Persisted; queryable; the source of truth |

---

## 2. The principle: optimal shape, no dumping ground

- Every capability is placed **deliberately**, by shape — not by habit.
- There is **no default dumping ground.** "Put it in the plugin" or "make it a
  skill" is never the answer without justification.
- Placement is a **first-class design decision**, recorded when non-obvious.

---

## 3. External/native ownership: authority before placement

Before choosing a Concord internal surface, ask whether the responsibility is
better enforced by an existing native authority. If it is, Concord does **not**
reimplement it.

### Decision rule

1. **Identify the authoritative owner first.**
   - CI/CD behavior belongs to the provider (GitHub Actions, cloud build service)
     or to executable policy in the repository.
   - Database correctness belongs to the database schema, constraints, and migrations.
   - Cloud/infra resources belong to the cloud provider API or infrastructure-as-code.
   - OS/process supervision belongs to the host process manager (systemd, launchd, etc.).
   - Provider configuration / typed policy belongs to the provider's native config surface.
2. **Configure and enforce there.** The native owner must hold the canonical
   control and the structural enforcement.
3. **Let Concord coordinate and observe only when appropriate.** Concord records
   intent, links, evidence, and status for these boundaries. It may surface them
   in Product-scoped views, trigger workflows around them, or link to their native
   dashboards, but it does not substitute its own prose or heuristics for native
   structural controls.

### Examples

| Concern | Authoritative owner | Concord's role |
|---|---|---|
| CI/CD behavior | GitHub Actions / provider config / executable policy in repo | Hold intent/links; record evidence of runs; do not encode branch protection or deployment rules in workflow prose. |
| Database schema / constraints | The database + migrations | Read schema/state as evidence; do not reimplement referential integrity or constraints in Concord code. |
| Cloud/infra resources | Cloud provider API / IaC (Terraform/Pulumi/etc.) | Observe status, link to source, coordinate operational runbooks; do not own the resource lifecycle. |
| OS/process supervision | systemd / launchd / host process manager | Observe daemon status; supervise only helper processes Concord itself owns. |
| Provider typed policy | `opencode.jsonc`, provider APIs, etc. | Declare intent in typed config; enforce through the provider; consume the resulting state as evidence. |

### Anti-pattern

Do not use a Concord workflow, skill, instruction, or agent heuristic as a
substitute for a missing control in the authoritative system. If the native owner
lacks a needed control, add it there; do not paper over it with Concord prose.

---

## 4. The rubric (characteristics → placement)

| If the capability… | Place it in… |
|---|---|
| Is better enforced by an external native authority | **External native authority** first; Concord coordinates/observe only after that. |
| Mutates durable state and needs validation/authorization/approval | **Core domain operation** first; expose through only the adapter accepted by TS6. The adapter never owns validation/authority. |
| Is a pure read with a concrete cross-client requirement | Stable core contract first; MCP requires a concrete second client plus TS8/TS9 evidence. |
| Is behavioral policy every agent needs, always | **Instruction** (always-on). |
| Is procedural methodology, loaded on demand, heavy | **Skill**. |
| Is a user-facing entry point / workflow contract | **Command**. |
| Is a standalone executable used across tools | **Host script** (`~/.local/bin/`). |
| Is data / source of truth | **Durable state** (specs/changes), not code. |
| Must run at host speed (no protocol overhead) | **Short-lived core CLI through accepted `concord.ts` adapter**; no plugin/MCP v1. |
| Must be portable to a concrete non-OpenCode client | Stable core contract first; evaluate another thin adapter/MCP through TS8/TS9, not speculatively. |
| Is volatile / evolves fast | The most **dynamic** surface that fits (instruction/skill), not frozen in compiled code. |

When a capability fits multiple rows, pick the **most specific** fit and record
the tradeoff. If an external native authority is the right owner, that row wins
over any Concord internal surface.

---

## 5. Dynamic guidance (stay cutting-edge)

The rubric is **not static.** Tooling evolves — MCP gains capabilities, plugin
hooks change, CodeMode matures, and external authorities expose new APIs. So:

- **Re-evaluate placements periodically** — a capability placed in the plugin
today may belong in MCP tomorrow (or vice versa) as surfaces shift. A capability
owned by an external system today may become a native Concord concern when the
system cannot enforce it adequately.
- **Record the current-state map** — which capabilities live where, today — so
drift is visible and re-evaluation is grounded.
- **Prefer the most dynamic fit** when a capability is volatile, so we're not
locked into a surface that ages poorly.
- **Treat the rubric itself as living** — update it when a new surface appears or
an existing one's characteristics change.

This is the mechanism for staying cutting-edge: deliberate placement + periodic
re-evaluation, never "set and forget."

---

## 6. Worked examples (current stack)

| Capability | Placement | Why |
|---|---|---|
| CI/CD policy / branch protection | External native authority (GitHub) | Enforced by the provider; Concord records intent and links to evidence. |
| Database schema / migrations | External native authority (database) | Constraints and migrations enforce correctness; Concord reads the state. |
| Advance `adv_change_create` (predecessor mutation) | Evidence for TS4 | Shows mutation/approval needs; does not select Concord transport or tool identity. |
| Advance `adv_status` / `adv_spec` reads | Evidence for TS3/TS6 | Shows read jobs and cross-client tradeoffs; does not require Concord MCP duplication. |
| `morph_edit` vs `edit` routing | Instruction (always-on) | Behavioral policy every agent needs. |
| `/adv-triage` methodology | Skill (→ workflow type per `workflows.md`) | Procedural; on-demand. |
| `oc-test-gate`, `oc-ci-wait`, `oc-fresh` | Host scripts | Standalone executables, cross-tool. |
| Worktree locator derivation (`concord worktree-locate`) | Core CLI verb (read-only) | The inputs are authority data — the Project's registered `canonical_path` locator — so only the core can read them without duplicating database access (issue #316). A host script would double-hop through this verb; the adapter owns no path or branch policy; `internal/store` stays verifier-only (`worktree_claim` verifies intent, never authors it). |
| Directory-to-Project resolution (`concord project-resolve`) | Core CLI verb (read-only) | Registered locators are authority data, and CD-0008 D1 makes a path replaceable evidence rather than identity, so a host that joins on a directory name invents an identity Concord does not hold. The same rationale as `worktree-locate`, one direction earlier (issue #533, CD-0079). |
| Spec / change records | Durable state | Source of truth. |

---

## 7. Open questions

1. **Cadence** — how often is the current-state placement map re-evaluated?
   Trigger-based (when tooling or external authorities shift) or scheduled?
2. **Migration** — when a capability's optimal placement changes, how is the move
    executed without breaking agents? (Couples to `design-constraints.md` §3,
   workflow evolution without migration.)
3. **Visibility** — should the current-state placement map be self-documenting
   (browsable in the launcher)? Likely yes (couples to
   `self-documentation.md`).
4. **Rubric authority** — is the rubric enforced structurally (a check that
    blocks misplaced capabilities) or advisory? **Lean:** advisory + recorded, with
   structural enforcement only for hard rules (the adapter never owns domain
   authorization/approval; external authority is not reimplemented in Concord).

---

## 8. Relationship to other docs

| Doc | Link |
|---|---|
| `priorities.md` | Ownership-aligned placement principle. |
| `feature-inventory.md` §3.14 | The capability entry. |
| `README.md` | Navigation hub. |
| `design-constraints.md` §8 | Tool-suite interaction / portability — placement of the cross-client surface. |
| `design-constraints.md` §12 | Ownership-aligned placement: respect native authority. |
| `self-documentation.md` | The placement map should be browsable. |

---

*Right shape, every time, re-evaluated as the world changes. That's how a system
stays sharp instead of calcifying.*

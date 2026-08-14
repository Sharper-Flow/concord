# Concord Core Architecture

> **Status:** Direction recorded (2026-08-02; storage layer hardened 2026-08-05).
> Companion to [`priorities.md`](./priorities.md) and [`design-constraints.md`](./design-constraints.md).
> **Purpose:** Record the Go-core language direction and the consolidated resilience invariant set that any core topology must satisfy. State authority is settled elsewhere (see §0). This is the single place a reader looks to understand how the runtime is layered.
> **Authority:** [`priorities.md`](./priorities.md) is canonical for ranked priorities and the operating envelope; this document is a companion that records an architecture direction within those constraints.

---

## 0. SQLite engine and global physical authority resolved

The storage-engine authority question is **resolved.** SQLite is Concord's sole
durable authority: transactional state + durable event history,
`synchronous=NORMAL`, WAL, and `busy_timeout=5000`; six binding invariants I1–I6
map 1:1 to the Advance failure modes. **PM2 selects one global local SQLite
authority per Concord installation/operator-machine; Product/Project remain logical
scopes.** Full records:
[`decisions/CD-0002-concord-state-authority.md`](./decisions/CD-0002-concord-state-authority.md).
[`product-memory-authority-scope.md`](./product-memory-authority-scope.md).
Topology is **library-in-process — no daemon** (CD-0002 §2b).

An earlier private pre-public durability proposal is superseded. Its useful
single-authoritative-log lesson carries forward, but Temporal is not adopted;
CD-0002 is the first public retained authority decision.

---

## How to read this

1. Start with [§1 Language ownership](#1-language-ownership) for what is Go and what is TypeScript.
2. Read [§2 Resilience invariants](#2-resilience-invariants) for the non-negotiable correctness properties any core topology must satisfy.
3. Read [§3 Architecture decision tracker](#3-architecture-decision-tracker) for resolved and genuinely open architecture questions.

---

## 1. Language ownership

**Current binding direction (finalized 2026-08-06):** Concord's core is written in
**Go**. Accepted TS6 permits one global TypeScript custom-tool adapter module; it is
not a plugin and owns no domain logic.

This is a **clean rewrite**, not a port of Advance's TypeScript plugin or its tool
catalog. Go-core is greenfield Concord code.

### Go owns

- Domain model: Product, membership, lifecycle stage, replacement relations.
- Durable-state rules: append-only log discipline, projection transactions,
  single-derivation enforcement (SQLite sole authority, CD-0002).
- Workflow/activity implementations: workflow types, gate logic, task
  orchestration, evidence policies.
- Read-model projections: bounded Product-scoped reads, staleness computation,
  typed degradation.
- Typed recovery and degradation: reconcile → repair → rebuild → destroy ladder;
  `Stale` / `Degraded` / `Unreconciled` / `Partial` verdicts.
- Validation, authorization, and approval enforcement at the domain boundary.
- RPC contract definition and all non-OpenCode business logic.

### TypeScript owns only the accepted TS6 adapter boundary

- Registration of the generated custom-tool declarations exposed to OpenCode (nine at surface 3.0.0).
- Zod-facing input/output schemas that map to the Go core's typed contract.
- OpenCode context propagation: session identity, worktree resolution,
  permission-prompt bridging.
- Transport to the Go core (short-lived CLI invocation per CD-0002 §5).
- Result shaping for agent consumption.

If present, TypeScript does not own domain logic, durable-state rules,
projections, workflow orchestration, or recovery semantics. It is an adapter,
not a core.

### Why Go

- Static binaries with simple cross-compilation for Linux, macOS, and Windows.
- Native concurrency model (goroutines) suited to per-entity serializers and
  non-blocking read paths.
- Pure-Go SQLite binding (`modernc.org/sqlite`) for the sole-authority store;
  no CGO, single static binary, trivial cross-compilation.
- Strong typing with minimal runtime overhead; compile-time correctness for
  state transitions, contract enforcement, and terminal-state invariants.

### Why not Rust

Viable, but no current advantage over Go for Concord's workload. Go's simpler
toolchain, faster compilation, and language-native SQLite binding outweigh
Rust's memory-safety guarantees for a single-machine, trusted-operator system.
Rust remains available if a future component proves a correctness or
performance need that Go cannot meet.

### Why not TypeScript for the core

Accepted TS6 selects one TypeScript custom-tool module because it matches OpenCode's
host API; it is not a plugin and owns no domain logic. The core domain—accepted
Product-memory rules, typed recovery, authorization, and transactional state—remains
Go.

---

## 2. Resilience invariants

These are the non-negotiable correctness properties any core topology must
satisfy. They are consolidated here from
[`design-constraints.md`](./design-constraints.md) §1, §4, §5, §13, §14–§19
and the public predecessor lessons (see
`advance-postmortem.md` for the canonical per-invariant source). Each invariant
is stated once; companion documents reference this section rather than restating.

| # | Invariant | Source | What it means structurally |
|---|---|---|---|
| 1 | **Single derivation** | postmortem C1 | All state derives from exactly one authoritative log (SQLite per CD-0002). No component holds a second writable authority. Projections are pure read-only derivations, rebuildable from the log. |
| 2 | **Atomic terminal transition** | postmortem C2 | Status, lifecycle, and gate completion commit together. Invalid combinations are unrepresentable. Terminal = one record/transition, not coordinated field updates. |
| 3 | **Append-only, no destructive repair** | postmortem C2/C3 | The log is append-only. Corrections are new entries, never in-place rewrites. History is immutable; DELETE-of-history is forbidden at the type level. |
| 4 | **Graded recovery** | postmortem C6 | Every reachable inconsistent state has an explicit repair path: reconcile → repair → rebuild → destroy. The recovery API exposes these as first-class verbs. |
| 5 | **Ground-truth reclamation** | postmortem C4 | Resource reclamation keys off the resource's own facts, not a derived status field. Status disagreements are reconcilable, not silently vetoed. |
| 6 | **Honest partial completion** | postmortem C3 | Sub-step failures degrade the verdict. `Result` has a distinct `Partial` state, not an error buried in an errors array. No advisory-only error beside unqualified success. |
| 7 | **Per-item budgets** | postmortem C5 | APIs accept per-item budgets. A budget is honored or refused (`BudgetRefused`), never silently clamped. Operations scale with data, not with a fixed ceiling. |
| 8 | **No lock waits** | Priority 1 | Many concurrent agents write to the same local state without database-lock contention, lock waits, or failed-writes-retry storms. Per-entity serialization, not a global mutex. |
| 9 | **Bounded authoritative reads** | Priority 3 | Reads are lock-free, bounded by Product scope, and paginated/streamed. Authority/freshness is typed and never guessed; related-work blocking follows CD-0006 R3's declared-edge and breaking-verdict policy. |
| 10 | **Typed degradation** | Priority 3 | Every read carries an authority tag (`authoritative` / `degraded` / `unreachable`). Silent non-authoritative substitution is forbidden. |
| 11 | **Safe idle-boundary evolution** | Priority 1, 5 | Workflow and schema changes are safe for in-flight work. Active executions stay pinned to their definition; idle work picks up the new version. Updates are additive and versioned. |

A Go core topology must satisfy all eleven invariants. The invariants are derived
from the public predecessor postmortem's failure classes; they are not aspirational,
they are evidence-backed.

---

## 3. Architecture decision tracker

This table records accepted architecture decisions that previously remained open:

| Question | Why deferred | Where tracked |
|---|---|---|
| **Product-memory membership** | **Resolved by PM5:** many-to-many role-only memberships, one canonical work identity, optional singular primary, derived cross-Product scope. | [`product-memory-membership.md`](./product-memory-membership.md). |
| **Minimal agent tool surface** | **Resolved by CD-0005 and CD-0024:** current nine tools, hidden verified context, `concord.ts` adapter, strict envelope, evolution, and measured stewardship. | [`decisions/CD-0005-concord-agent-tool-surface.md`](./decisions/CD-0005-concord-agent-tool-surface.md). |
| **Evidence-resolution architecture** | **Resolved by CD-0008 D2:** immutable-subject binding records the attributable producer proof that authorized a transition; the producer remains verdict authority and current re-resolution is typed when unavailable. | [`decisions/CD-0008-concord-mechanism-hardening.md`](./decisions/CD-0008-concord-mechanism-hardening.md) §D2; [`design-constraints.md`](./design-constraints.md) Research backlog item 6. |
| **Validation-failure isolation** | **Resolved by CD-0008 D3:** unreadable records contribute unknown; typed degraded omissions are allowed for independently provable positive reads, while safety conclusions fail closed only over their bounded dependency/touch closure. | [`decisions/CD-0008-concord-mechanism-hardening.md`](./decisions/CD-0008-concord-mechanism-hardening.md) §D3; [`design-constraints.md`](./design-constraints.md) Research backlog item 7. |
| **Migrations / schema evolution** | **Resolved by CD-0008 D6:** typed ordered upcasters, projection schema versions, deterministic replay tests, fail-closed newer versions, pinned active workflow versions, point-in-time reconstruction, and falsifier-driven snapshots. | [`decisions/CD-0008-concord-mechanism-hardening.md`](./decisions/CD-0008-concord-mechanism-hardening.md) §D6; CD-0002 §7. |

All rows in this table are resolved explicitly by PM1–PM10/CD-0005/CD-0008—not by
implication from Go, storage, or CLI choice.

---

## 4. Relationship to other docs

| Doc | Link | Relationship |
|---|---|---|
| [`priorities.md`](./priorities.md) | Canonical authority | Ranked priorities and operating envelope; this document follows them. |
| [`design-constraints.md`](./design-constraints.md) | NFRs and constraints | §7 updated to reference this document. §5 read-path target retained. |
| [`decisions/CD-0002-concord-state-authority.md`](./decisions/CD-0002-concord-state-authority.md) | State authority | Sole durable authority for SQLite; invariants I1–I6. |
| [`storage-spine-slice.md`](./storage-spine-slice.md) | Implementation acceptance plan | Runs against accepted PM1–PM10 and CD-0002/CD-0006/CD-0007/CD-0008 mechanics; validates the accepted shape rather than choosing it. |
| [`clarifications.md`](./clarifications.md) | Build-authorizing decisions | PM1–PM10 shape Product memory; TS1–TS9 shape the agent tool surface; CD-0006/CD-0007/CD-0008 settle root policy, repository boundary, and mechanism hardening. |
| [`rollout-plan.md`](./rollout-plan.md) | Entry conditions | Go-core direction is not an entry condition. |
| [`advance-postmortem.md`](./advance-postmortem.md) | Failure evidence | Public predecessor lessons source the resilience invariants in §2. |
| [`capability-placement.md`](./capability-placement.md) | Placement rubric | Places capabilities under CD-0005; TS6 selects custom-tool adapter and rejects plugin/MCP v1. |
| [`workflows.md`](./workflows.md) | Workflow types | Plurality of purpose-built types; storage assumptions per CD-0002. |
| [`feature-inventory.md`](./feature-inventory.md) | Capability inventory | Compilation decision updated to reference this document. |

---

*The core is Go. PM2/PM3 fix the physical authority and domain-projection shape;
PM4/PM5 fix lifecycle, relations, and membership; TS6 still governs adapter boundaries. The
invariants are non-negotiable.
Everything else is earned through research, not assumed.*

# Concord Core Architecture

> **Status:** Direction recorded (2026-08-02; storage layer hardened 2026-08-05).
> Companion to [`priorities.md`](./priorities.md) and [`design-constraints.md`](./design-constraints.md).
> **Purpose:** Record the Go-core language direction and the consolidated resilience invariant set that any core topology must satisfy. State authority is settled elsewhere (see §0).
> This is the single place a reader looks to understand how the runtime is layered.
> **Amended by [CD-0242](./decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md) D2, D6, and D14:** the store keeps current state and finite snapshots, not history; attempts carry their own authority, not a budget; generated contracts derive from one source.

> **Authority:** [`priorities.md`](./priorities.md) is canonical for ranked priorities and the operating envelope; this document is a companion that records an architecture direction within those constraints.

---

## 0. SQLite execution authority and Linear planning authority

The storage-engine authority question is **resolved.** SQLite is Concord's sole
durable local execution authority: transactional current state and finite
verified snapshots, `synchronous=NORMAL`, WAL, and `busy_timeout=5000`; six binding invariants I1–I6
map 1:1 to the Advance failure modes. **PM2 selects one global local SQLite
authority per Concord installation/operator-machine; Product/Project remain logical
scopes.** Full records:
[`decisions/CD-0002-concord-state-authority.md`](./decisions/CD-0002-concord-state-authority.md).
[`product-memory-authority-scope.md`](./product-memory-authority-scope.md).
Topology is **library-in-process — no daemon** (CD-0002 §2b).

Linear owns planning through the Linear MCP server. Concord keeps managed
execution contracts, sessions, worktrees, worker dispatch, evidence, and delivery
receipts. A work unit can retain one reported issue identity (key, UUID, URL).
The binary holds no Linear credential and makes no Linear API call. See
[`CD-0213`](./decisions/CD-0213-linear-mcp-is-the-only-planning-authority.md) and
[`managed-development-authority.md`](./managed-development-authority.md).

---

## How to read this

1. Start with [§1 Language ownership](#1-language-ownership) for what is Go and what is TypeScript.
2. Read [§2 Resilience invariants](#2-resilience-invariants) for the non-negotiable correctness properties any core topology must satisfy.
3. Read [§3 Architecture decision tracker](#3-architecture-decision-tracker) for resolved and genuinely open architecture questions.

---

## 1. Language ownership

**Current binding direction (finalized 2026-08-06):** Concord's core is written in
**Go**. The TypeScript adapter exposes custom tools through the
`concord-plugin.ts` entry module, which also registers accepted host runtime hooks. The
`concord.ts` transport module owns no domain logic; [CD-0090 D1](./decisions/CD-0090-per-turn-continuity-re-pin-hook.md#d1-runtime-hooks-and-authority-hooks-are-different-classes)
keeps authority hooks in the core.

This is a **clean rewrite**, not a port of Advance's TypeScript plugin or its tool
catalog. Go-core is greenfield Concord code.

### Go owns

- Domain model: Product, membership, lifecycle stage, bounded Domain relations,
  and work-item supersession.
- Durable-state rules: current-state transactions checked by expected version
  and idempotency key, with no event log, fold, upcaster, or replay, and
  single-derivation enforcement (SQLite sole authority per CD-0002;
  [CD-0242 D2](./decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)).
- Workflow/activity implementations: workflow types, gate logic, task
  orchestration, evidence policies.
- Read-model projections: bounded Product-scoped reads, staleness computation,
  typed degradation.
- Typed recovery and degradation: reconcile → repair → rebuild → destroy ladder;
  `Stale` / `Degraded` / `Unreconciled` / `Partial` verdicts.
- Validation, authorization, and approval enforcement at the domain boundary.
- Remote-procedure-call (RPC) contract definition and all non-OpenCode business logic.

### TypeScript owns only the accepted TS6 adapter boundary

- Registration of the generated current custom-tool declarations exposed to OpenCode.
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
- Pure-Go SQLite binding ([`modernc.org/sqlite`](https://pkg.go.dev/modernc.org/sqlite)) for the sole-authority store;
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

The core domain—accepted Product-memory rules, typed recovery, authorization,
and transactional state—remains Go.

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
| 1 | **Single derivation** | postmortem C1 | All current state derives from exactly one authoritative store (SQLite per CD-0002), no component holds a second writable authority, and no event log, fold, upcaster, or replay exists ([CD-0242 D2](./decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)). |
| 2 | **Atomic terminal transition** | postmortem C2 | Status, lifecycle, and gate completion commit together. Invalid combinations are unrepresentable. Terminal = one record/transition, not coordinated field updates. |
| 3 | **Current-state writes, no history** | postmortem C2/C3 | Each mutation writes current rows in one transaction, checked by an expected version and recorded by its idempotency key; no append-only history exists ([CD-0242 D2](./decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)). |
| 4 | **Graded recovery** | postmortem C6 | Every reachable inconsistent state has an explicit repair path: reconcile current rows, restore a verified snapshot, and reconcile against Git, worktrees, artifacts, and external effects ([CD-0242 D2](./decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)). |
| 5 | **Ground-truth reclamation** | postmortem C4 | Resource reclamation keys off the resource's own facts, not a derived status field. Status disagreements are reconcilable, not silently vetoed. |
| 6 | **Honest partial completion** | postmortem C3 | Sub-step failures degrade the verdict. `Result` has a distinct `Partial` state, not an error buried in an errors array. No advisory-only error beside unqualified success. |
| 7 | **Per-item budgets** | postmortem C5 | APIs accept per-item budgets. A budget is honored or refused (`BudgetRefused`), never silently clamped. Operations scale with data, not with a fixed ceiling. Main sizes each assignment by complexity and relatedness, and scope, prerequisite, ownership, and safety checks remain; no attempt count, production-line cap, size measurement, or mandatory split exists ([CD-0242 D6](./decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d6-attempts-carry-their-own-authority-not-a-budget)). |
| 8 | **Bounded writer admission** | Priority 2 | Many concurrent agents write to the same local state with zero escaped busy failures, zero lost or duplicated effects, and writer-admission wait bounded by the accepted target on the named acceptance population. Admission wait, commit duration, and escaped busy failure are measured and reported separately. Reads stay lock-free. Per-entity serialization, not a global mutex. See CD-0045 and CD-0011. |
| 9 | **Bounded authoritative reads** | Priority 4 | Reads are lock-free, bounded by Product scope, and paginated/streamed. Authority/freshness is typed and never guessed; related-work blocking follows CD-0006 R3 and CD-0222's architecture-overlap policy. |
| 10 | **Typed degradation** | Priority 4 | Every read carries an authority tag (`authoritative` / `degraded` / `unreachable`). Silent non-authoritative substitution is forbidden. |
| 11 | **Single-source definitions** | Priority 2, 6 | Workflow and schema changes are safe for in-flight work. Workflow definitions and derived schemas are generated from one canonical source; no released workflow-definition version is retained for history and no hand-maintained version, pin, or catalog duplicates it ([CD-0242 D14](./decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d14-generated-contracts-derive-from-one-source)). |
| 12 | **Architecture-bound Product truth** | Priority 1 | Every current law has one Domain home. Product-changing work pins its Domain/law footprint, and unresolved concurrent Domain overlap cannot enter Product truth. Git merge success and heuristics confer no authority. |

A Go core topology must satisfy all twelve invariants. The first eleven are derived
from the public predecessor postmortem's failure classes; they are not aspirational,
they are evidence-backed. CD-0222 adds the twelfth from Concord's defining
many-agent Product-concordance requirement.

---

## 3. Architecture decision tracker

This table records accepted architecture decisions that previously remained open:

| Question | Why deferred | Where tracked |
|---|---|---|
| **Product-memory membership** | **Resolved by PM5:** many-to-many role-only memberships, one canonical work identity, optional singular primary, derived cross-Product scope. | [`product-memory-membership.md`](./product-memory-membership.md). |
| **Minimal agent tool surface** | **Resolved by CD-0005 and amended by CD-0042:** generated current tools, hidden verified context, `concord.ts` adapter, strict envelope, exact manifest digest, and deterministic pre-go-live evidence. | [`decisions/CD-0005-concord-agent-tool-surface.md`](./decisions/CD-0005-concord-agent-tool-surface.md). |
| **Evidence-resolution architecture** | **Resolved by CD-0008 D2:** immutable-subject binding records the attributable producer proof that authorized a transition; the producer remains verdict authority and current re-resolution is typed when unavailable. | [`decisions/CD-0008-concord-mechanism-hardening.md`](./decisions/CD-0008-concord-mechanism-hardening.md) §D2; [`design-constraints.md`](./design-constraints.md) Research backlog item 6. |
| **Validation-failure isolation** | **Resolved by CD-0008 D3:** unreadable records contribute unknown; typed degraded omissions are allowed for independently provable positive reads, while safety conclusions fail closed only over their bounded dependency/touch closure. | [`decisions/CD-0008-concord-mechanism-hardening.md`](./decisions/CD-0008-concord-mechanism-hardening.md) §D3; [`design-constraints.md`](./design-constraints.md) Research backlog item 7. |
| **Migrations / schema evolution** | **Resolved by [CD-0242](./decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md):** schema and workflow-definition changes migrate current rows and keep finite verified snapshots; no upcasters, deterministic replay, or retained released-definition versions exist ([CD-0242 D2](./decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history), [CD-0242 D14](./decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d14-generated-contracts-derive-from-one-source)). | [`decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md`](./decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md) (D2, D14); CD-0002 §7. |
| **Product architecture and concurrent-law coordination** | **Resolved by CD-0222:** canonical Domains own law; Product-changing contracts declare architecture footprints; overlapping work requires a version-pinned resolution. CD-0213 places business grouping in Linear. | [`decisions/CD-0222-architecture-bound-product-law-with-package-boundary-code-ownership.md`](./decisions/CD-0222-architecture-bound-product-law-with-package-boundary-code-ownership.md); [`decisions/CD-0213-linear-mcp-is-the-only-planning-authority.md`](./decisions/CD-0213-linear-mcp-is-the-only-planning-authority.md). |

All rows in this table are resolved explicitly by PM1–PM10/CD-0005/CD-0008/CD-0222—not by
implication from Go, storage, or CLI choice.

---

## 4. Entry points by responsibility

One row per responsibility. Start at the cited entry, then use symbol and test
search in the same package. Ordinary package docs and adjacent tests own the
details; this table does not duplicate them.

| Responsibility | Start here | Invariant and test owner |
|---|---|---|
| CLI routing and host bootstrap | `cmd/concord/main.go`, `cmd/concord/work_bootstrap.go`, `cmd/concord/session.go` | Command admission, session identity; same-package tests |
| Agent authorization and mutation | `internal/agent/runtime.go`, `internal/agent/mutations.go` | `internal/agent/authority.go`; approval, replay, and budget tests |
| Adapter transport and native workers | `adapter/opencode/concord.ts`, `adapter/opencode/dispatch.ts`, `adapter/opencode/packet.ts` | `dispatch.test.ts`, `lane_dispatch.test.ts`, report-protocol tests |
| SQLite and event authority | `internal/store/doc.go`, `internal/store/store.go`, `internal/store/transaction.go`, `internal/store/events.go` | One pooled connection; `txscope_test.go`, `tx_subprocess_guard_test.go` |
| Work context and evidence | `internal/store/work_context.go`, `internal/store/worker_packet_binding.go`, `internal/store/worker_context_findings.go` | Declared sources, frontier, exact subject and attempt; adjacent tests |
| Workflow action and protected writes | `internal/store/workflow_dispatch.go`, `internal/store/workflow_preflight.go`, `internal/store/workflow_domain_overlap.go` | Approved scope, law revision, and shared-write checks |
| Sessions, worktrees, handoff | `internal/store/worktrees.go`, `internal/store/project_handoffs.go`, `internal/store/session_vacate.go` | Occupancy, same-Project incarnation, per-repository provenance; adjacent tests |
| Research and knowledge law | `internal/store/research_types.go`, `internal/store/research_reads.go`, `internal/store/research_mutations.go`, `internal/store/law_boundary.go` | Version and source identity, consumer pins, staleness, accepted-law lookup |
| Repository verification | `.github/workflows/ci.yml`, `scripts/check-json.py`, `bin/oc-test` | Required checks and targeted verification routing |

---

## 5. Relationship to other docs

| Doc | Link | Relationship |
|---|---|---|
| [`priorities.md`](./priorities.md) | Canonical authority | Ranked priorities and operating envelope; this document follows them. |
| [`design-constraints.md`](./design-constraints.md) | NFRs and constraints | §7 updated to reference this document. §5 read-path target retained. |
| [`decisions/CD-0002-concord-state-authority.md`](./decisions/CD-0002-concord-state-authority.md) | State authority | Sole durable authority for SQLite; invariants I1–I6. |
| [`clarifications.md`](./clarifications.md) | Build-authorizing decisions | PM1–PM10 shape Product memory; TS1–TS9 shape the current agent tool surface; CD-0006/CD-0007/CD-0008/CD-0222 settle root policy, repository boundary, mechanism hardening, and Product-law architecture. |
| [`rollout-plan.md`](./rollout-plan.md) | Entry conditions | Go-core direction is not an entry condition. |
| [`advance-postmortem.md`](./advance-postmortem.md) | Failure evidence | Public predecessor lessons source the resilience invariants in §2. |
| [`capability-placement.md`](./capability-placement.md) | Placement rubric | Places capabilities under CD-0005; TS6 selects the plugin-entry adapter and rejects MCP v1. |
| [`workflows.md`](./workflows.md) | Workflow families | Three retained execution families (§4), composed from shared parts at the size the decision model sets (§7). |
| [`feature-inventory.md`](./feature-inventory.md) | Capability inventory | Compilation decision updated to reference this document. |

---

*The core is Go. PM2/PM3 fix the physical authority and domain-projection shape;
PM4/PM5 fix lifecycle, relations, and membership; TS6 still governs adapter boundaries. The
invariants are non-negotiable.
Everything else is earned through research, not assumed.*

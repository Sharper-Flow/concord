# Concord Design Constraints

> **Status:** Aligned v3 under CD-0006 and CD-0222. Companion to [`priorities.md`](./priorities.md).
> **Purpose:** Capture the non-functional requirements, hard constraints, and design directives that shape *how* Concord is built. [`goals.md`](./goals.md) records the unranked goals as guidance; [`priorities.md`](./priorities.md) owns the operating envelope, constraints, conflict rules, and standing law.

---

## How to read this

These are **quality attributes and constraints**, not features. They reflect the
goals and standing law in the source documents above. The cited accepted decisions
resolve the architecture questions recorded here; deferred later-phase Product questions
remain in `clarifications.md`.

---

## 1. One user, one machine, many-agent concurrency

**Requirement.** One human operator and many local AI agents work concurrently on one machine with **zero escaped busy failures, zero lost or duplicated effects, bounded writer-admission wait**, and no manual coordination. Writes are serialized, not contention-free: one writer holds the lock at a time and the others queue. What is forbidden is a wait that escapes as an error, grows without bound, or forces the caller into a retry storm.

**Implication.** One operator per installation is a permanent boundary. Other humans
may run independent installations against shared git knowledge; live workflow state
is not shared and Concord does not become a team server. Local concurrency must be
non-blocking for reads and serialize writes without retry storms. Writer-admission
wait, commit duration, and escaped busy failure are distinct quantities, measured
and reported separately against a named population per CD-0045.

**Direction.** Product-scoped partitioning; single-writer-per-entity writes to current rows, with no event log or replay ([CD-0242 D2](./decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)). Reads are always lock-free. Storage is **resolved** — SQLite sole authority (CD-0002, 2026-08-05); see [`decisions/CD-0002-concord-state-authority.md`](./decisions/CD-0002-concord-state-authority.md) §2b for the concurrency model (library-in-process — no daemon).

---
## 2. Structural Product-law impact, architecture overlap, and freshness

**Requirement.** No Product-changing workflow may silently proceed when another
active or newly landed workflow overlaps its Domain footprint or invalidates a
law, architecture relation, dependency, or resource assumption. Non-authoritative
snapshots never masquerade as current authority.

**Implication.** CD-0006 R3 retains hard/soft `depends_on` edges and bounded
freshness checks. CD-0222 adds a typed architecture binding to every
Product-changing contract. Intersecting affected Domains require a resolution
pinned to both contract versions before both items hold execution authority;
exact law/Domain/relation writes are marked as write overlap. The check reruns
inside every consequential mutation. Version stamps supply deterministic
fallback. Polling, timers, automatic downstream rewrites, and heuristic
authority are forbidden.

---

## 3. Workflow evolution and full coordination

**Requirement.** Workflow and schema changes must be safe for in-flight work, per
[`priorities.md` — Data safety](./priorities.md#data-safety-constraint).

**Implication.** Current definitions and the cutover follow
[CD-0242 D13–D14](./decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d13-the-cutover-is-one-documented-migration-with-a-verified-snapshot),
not retained historical definitions.

**Direction.** [`workflows.md`](./workflows.md) owns the current family and
composition model. [CD-0218 D1](./decisions/CD-0218-the-live-workflow-catalog-retains-three-execution-families.md#d1-three-families-own-new-execution)
owns the live family boundary; CD-0242 D14 owns generated definitions.

---

## 4. Bounded writer admission, no history repair

**Requirement.** Concord-owned writes queue behind one writer and are admitted within the accepted bound, never surfacing an escaped `SQLITE_BUSY`, a lost effect, an unexpected duplicate, or an invariant violation. Reads are lock-free. Acknowledged consequential operations are durable across power loss and OS crash under [CD-0050 D2](./decisions/CD-0050-durable-acknowledgement.md#d2-acknowledged-consequential-operations-are-durable-ordinary-writes-are-durable-through-the-last-boundary), using the durable transactions defined by [CD-0207](./decisions/CD-0207-a-durable-commit-replaces-the-checkpoint-barrier.md); ordinary writes are durable through the last consequential boundary. The store keeps current state and finite snapshots, not history, so there is no history to repair ([CD-0242 D2](./decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)).

**Implication.** This is a constraint on **Concord's own state model**, not a mandate to rewrite any existing system. It is the storage-model constraint.

**Direction.** **Resolved by [`decisions/CD-0002-concord-state-authority.md`](./decisions/CD-0002-concord-state-authority.md), PM2, and PM3 (2026-08-05):** one global local SQLite authority writing current rows in one transaction, `synchronous=NORMAL`, WAL, one writer at a time; no event log, fold, upcaster, or replay exists ([CD-0242 D2](./decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)). State authority is SQLite sole authority per CD-0002 (invariants I1–I6).

---

## 5. Fast, bounded read-path

**Requirement.** Portfolio and dashboard reads feel instant. Every read is bounded by Product scope and paginated or streamed; no query is unbounded.

**Implication.** The read-path is a first-class design surface, implemented in Go as part of the Concord core ([`core-architecture.md`](./core-architecture.md) §1), with a latency target and bounded-read requirement.

**Direction.** Target sub-100ms for typical Product views; per-read latency budgets; on-disk projections as the primary read input. The read-path language is Go (see §7 and [`core-architecture.md`](./core-architecture.md)).

---

## 6. Lightweight, agent-buildable interface

**Requirement.** The primary operator surface is **host-started agent sessions**: the host owns operator session start and resume (CD-0078), and agents enter managed work through `concord_work_start`. Any additional interface (web, TUI, etc.) is optional and must not become the design center.

**Implication.** Host-started agent sessions are the canonical human entry point. Agents interact
with the same durable Product memory through the **CD-0005 surface as amended by
CD-0024** and `concord.ts` adapter. Grid/table views are secondary projections.

**Direction.** The entry route is work-oriented: a session enters named work through `concord_work_start`. Optional web/TUI grid views remain secondary, with no IDE-specific integrations. The interface is simple enough that an agent can scaffold or extend views without fighting a heavy frontend stack.

---

## 7. Go core; thin adapter accepted by TS6

See [`core-architecture.md`](./core-architecture.md) §1 for the current language
ownership baseline (also R6 in [`clarifications.md`](./clarifications.md)). Go owns
the core. The thin plugin entry and shared `concord.ts` transport follow that
language ownership boundary; domain logic stays in Go. CD-0003's short-lived
CLI remains the internal/domain boundary, not the model-visible surface.

---

## 8. Client portability, OpenCode-first

**Requirement.** Concord must be **capable of supporting non-OpenCode clients**, but the primary path is optimized for OpenCode on the local machine.

**Implication.** The canonical agent interaction model is CD-0005's generated
current surface. Adapter ownership follows [`core-architecture.md` §1](./core-architecture.md#1-language-ownership);
future clients require TS8/TS9 evidence. No IDE-specific integration is built.

**Direction.** Use CD-0005 and [`capability-placement.md`](./capability-placement.md):
short-lived Go core invocation behind the generated `concord.ts` module. Do not map
storage tables or CLI commands 1:1 to tools.

---

## 9. Self-documentation with optimal IO / storage / memory

**Requirement.** Specs, durable workflow documents, and product knowledge are first-class read targets. They must be cheap to browse, cache, and keep consistent, even for large Products.

**Implication.** The storage model (§4) must serve read-heavy documentation from day one. Documents are content-addressed, projections are derived, and memory is bounded (lazy, paginated, or streamed).

**Direction.** Treat the doc store as a designed read surface, not a side effect
of file persistence. A Domain's current law, changes, evidence, decisions, and
runbooks are co-located. See [`self-documentation.md`](./self-documentation.md)
and [`product-data-model.md`](./product-data-model.md).

---

## 10. Quality governance by evidence, not by gate count

**Requirement.** Concord's quality governance follows [`priorities.md` — Quality (goal E)](./priorities.md#quality-goal-e).

**Implication.** Quality is **not** achieved by mandating Advance's seven-gate lifecycle for every work kind. Different workflow types may use different gates or none at all; the common denominator is the linked quality attributes.

**Direction.** Every workflow type declares its own artifacts, steps, and completion criteria. Independent review is required where the risk or consequence warrants it.

Accepted CD-0012 additionally requires completion criteria to include an **outcome contract**: the premise, the required end-state as falsifiable postconditions, and the candidate set those postconditions range over. The contract is approved at planning and verified at completion, and a delivered end-state weaker than the approved one fails. This is what makes *intent fidelity* and *no silent drift* enforceable rather than aspirational. Binding form: [`decisions/CD-0012-bind-stated-goals-to-delivered-outcomes.md`](./decisions/CD-0012-bind-stated-goals-to-delivered-outcomes.md).

---

## 11. Product ownership is declarative, not bridged

**Requirement.** A Product declaratively owns its Domains, Projects, and managed
resources, but Concord does not bridge to external systems or call their APIs by
default. Ownership and architecture are recorded facts, not active integrations.

**Implication.** External systems may be pulled in as signals (e.g. azure job status), but those signals are read-only inputs, not Concord-authored state. Concord does not take responsibility for operational state it cannot authoritatively track.

**Direction.** Typed, schema-validated state is the canonical ownership form.
The Product Git knowledge home owns shared Domain identity, hierarchy, and
architecture relations; SQLite projects that law and owns local
Domain→resource attachments. Domains are never derived from tags, paths,
or external planning membership. See
[`product-data-model.md`](./product-data-model.md) and
[`capability-placement.md`](./capability-placement.md).

---

## 12. Ownership-aligned placement: respect native authority

See [`capability-placement.md`](./capability-placement.md) for the canonical direction.

---

## 13. Product context is ambient, not a parameter

**Requirement.** An agent, sub-agent, or tool invocation operates within an established Product/workspace context. Multi-repo reach is a property of that context, not an argument threaded through each call. A capability must not depend on the caller remembering to pass a scope parameter.

**Implication.** No capability is available only to callers that pass a scope argument: it works within established context or is explicitly unavailable. Native Task remains host-owned; any Concord call resolves its own Product/workspace context. An unavailable authoritative read returns typed degradation, never a silently substituted snapshot.

**Direction.** TS5/CD-0005 resolves the boundary as a trusted host-injected,
self-contained invocation envelope whose Product/Project scope and versions the core
re-resolves on every call. A native subagent's prompt supplies no Concord authority.
A project-local refusal can still be correct fail-closed behavior: this constraint
targets second-class execution paths, not every scope restriction. The predecessor's
explicit path-confirmation plumbing is the recorded anti-pattern reference — see
the public lessons in
[`advance-predecessor-lessons.md`](./advance-predecessor-lessons.md).

---

## 14. Single derivation — no parallel authority

**Requirement.** All Concord-owned current state derives from exactly one authoritative store, with no event log, fold, upcaster, or replay ([CD-0242 D2](./decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)). No subsystem may hold a second writable authority for the same entity, and two readers must not be *able* to return different answers to the same question.

**Implication.** This is stricter than §4, and it is the constraint §4 does not supply. A single authoritative store does not prevent this failure if a separate writable projection is also maintained — divergence then has no owner.

**Direction.** CD-0002 and PM3 resolve one serialized writer whose accepted mutations write current rows in one transaction; derived read models are never a second write target ([CD-0242 D2](./decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)).

Derived from the public predecessor lessons (see [`advance-postmortem.md`](./advance-postmortem.md)).

---

## 15. Atomic terminal transition

**Requirement.** When work reaches a terminal state, every field describing that fact commits together or not at all. Status, lifecycle state, and completion markers are one transition, not several.

**Implication.** No observer may witness a partially-terminal entity. An entity that is terminal by one field and active by another is an invalid state that the model must make unrepresentable, not merely unlikely.

**Direction.** Model terminality as a single transition over one record rather than as coordinated updates to several fields. Prefer designs where the invalid combination cannot be expressed — correctness here should be structural, not enforced by convention or by the caller remembering to update every field.

Derived from the public predecessor lessons (see [`advance-postmortem.md`](./advance-postmortem.md)).

---

## 16. Partial completion never reports success

**Requirement.** An operation that does not fully complete must not return a success-shaped result. If any sub-step is skipped, times out, or is abandoned, the reported outcome degrades to reflect that.

**Implication.** Non-fatal sub-step errors must not be relegated to an advisory field alongside an unqualified success verdict. A caller acting only on the top-level result must not be misled, because in practice callers — human and agent alike — act on the verdict and skim the details.

**Direction.** Operations report a completion status that is a function of all their sub-steps. Where partial completion is legitimate, it gets its own explicit status distinct from success, carrying what remains outstanding. A timeout within a sequence must either roll back or be surfaced as incomplete; it must never silently abandon later steps.

Derived from the public predecessor lessons (see [`advance-postmortem.md`](./advance-postmortem.md)).

---

## 17. Reclamation keys off ground truth, not derived state

**Requirement.** Reclaiming a resource — a worktree, a branch, a cache entry, a lock — is decided by facts about that resource, not by a status field elsewhere in the system.

**Implication.** Bookkeeping drift must never strand a resource. If a worktree is merged and clean, those git facts are sufficient authority to reclaim it; a disagreeing status field is a reason to investigate the field, not to retain the resource indefinitely.

**Direction.** Cleanup paths verify the resource's own preconditions directly. Where a derived status is consulted as a convenience, disagreement between it and ground truth is surfaced as a reconcilable inconsistency rather than treated as a veto.

Derived from the public predecessor lessons (see [`advance-postmortem.md`](./advance-postmortem.md)).

---

## 18. Operations scale with the data they touch

**Requirement.** No operation applies a fixed time or resource budget to an input that grows without bound. Operations over collections are incremental, per-item, or explicitly paginated.

**Implication.** A maintenance operation must not become unusable precisely when it is most needed. Budgets that are adequate at small scale and silently inadequate at large scale produce self-reinforcing failure: the backlog grows because cleanup fails, and cleanup fails because the backlog grew.

**Direction.** Prefer per-item operations with independent budgets over whole-collection scans under one clamp. Where a caller supplies a budget, either honour it or refuse explicitly — silently clamping a requested budget to a smaller fixed value hides the constraint at the moment the caller most needs to see it.

Derived from the public predecessor lessons (see [`advance-postmortem.md`](./advance-postmortem.md)).

---

## 19. Non-destructive recovery is a design requirement

**Requirement.** Every reachable inconsistent state has a documented recovery path that does not destroy correct work. A design is incomplete until its failure modes have proportionate repairs.

**Implication.** Recovery proportionality is a first-class design property, not an operational afterthought. If the only remedy for a bookkeeping inconsistency is a destructive operation, operators are forced to choose between an incorrect record and losing valid history — and will reasonably choose to leave the system broken, which is what accumulates.

**Direction.** Recovery paths are designed alongside the states that need them, and graded to the severity of the problem: reconcile current rows, restore a verified snapshot, and reconcile against Git, worktrees, artifacts, and external effects before destructive operations ([CD-0242 D2](./decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)). Refusal prechecks on a recovery tool must not shadow the very condition that tool exists to handle.

Derived from the public predecessor lessons (see [`advance-postmortem.md`](./advance-postmortem.md)).

---

## Architecture decision history

The cited decisions govern implementation conformance.

1. **Storage model for Concord-owned state** (§4, §14). CD-0002 governs storage authority.
2. **Workflow evolution semantics** (§3). [`workflows.md`](./workflows.md) owns the current family and composition model under CD-0218 and CD-0242 D13–D14.
3. **Read-path implementation language** (§5, §7). Resolved — Go; see [`core-architecture.md`](./core-architecture.md).
4. **Ambient Product context propagation** (§13). **Resolved by TS5/CD-0005:** trusted hidden call envelope, core re-resolution, explicit cross-scope intent, and inherited Product/workspace context.
5. **Recovery-path taxonomy** (§19). [CD-0242 D2](./decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)
   owns current-row reconciliation and finite verified snapshots.
   [D6](./decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d6-native-task-belongs-to-the-host)
   owns launch authority and uncertain-effect reconciliation;
   [D11–D12](./decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d11-a-fresh-session-resumes-from-current-state)
   own resumption and admitted recovery actions. PM10 owns backup/restore, and
   CD-0008 D3 owns dependency-aware unreadable isolation.
6. **Evidence-resolution completeness** (#325). **Resolved by CD-0008 D2 (2026-08-06):** verification, review, approval, commit, native-run, and durable-knowledge evidence binds to an immutable subject and an attributable producer record. The producer remains verdict authority. Concord atomically records the typed binding, and returns typed missing, unreachable, or degraded results when re-resolution cannot complete. Source record: [`advance-postmortem.md`](./advance-postmortem.md) §Evidence resolution; accepted mechanism: [`decisions/CD-0008-concord-mechanism-hardening.md`](./decisions/CD-0008-concord-mechanism-hardening.md) §D2.
7. **Validation-failure isolation** (#349). **Resolved by CD-0008 D3 (2026-08-06):** unreadable records contribute unknown, and independently provable positive reads may return typed degraded omissions. Negative and safety conclusions fail closed only when the unreadable record lies within the bounded closure needed for that conclusion. The unreadable set is explicit and unrelated operations are not globally blocked. Source record: [`advance-postmortem.md`](./advance-postmortem.md) §Write-path truthfulness; accepted mechanism: [`decisions/CD-0008-concord-mechanism-hardening.md`](./decisions/CD-0008-concord-mechanism-hardening.md) §D3.
8. **State-authority engine for a single-operator local orchestrator** (operator decision D1, 2026-08-05). **✅ Resolved-by-CD-0002 + PM2–PM5** — SQLite is the sole authority per [`decisions/CD-0002-concord-state-authority.md`](./decisions/CD-0002-concord-state-authority.md). Four records refine it:
   - **one global local authority** per [`product-memory-authority-scope.md`](./product-memory-authority-scope.md);
   - one authoritative current-state store per [`product-memory-domain-schema.md`](./product-memory-domain-schema.md);
   - lifecycle and relation invariants per [`product-memory-lifecycle-relations.md`](./product-memory-lifecycle-relations.md);
   - canonical membership and scope per [`product-memory-membership.md`](./product-memory-membership.md).

   Together they give transactional current state, finite verified snapshots,
   `synchronous=NORMAL`, WAL, and invariants I1–I6, with no event log or replay
   ([CD-0242 D2](./decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)). Ranked engine alternatives remain in CD-0002 §2.

---

## Relationship to other docs

| Constraint | Companion doc |
|---|---|
| §1 concurrency | [`product-data-model.md`](./product-data-model.md) |
| §2 law/architecture impact | [`decisions/CD-0222-architecture-bound-product-law-with-package-boundary-code-ownership.md`](./decisions/CD-0222-architecture-bound-product-law-with-package-boundary-code-ownership.md) |
| §3 workflow evolution | [`workflows.md`](./workflows.md) |
| §4 bounded writer admission / no history repair | [`clarifications.md`](./clarifications.md) C2 |
| §5 fast read-path | [`rollout-plan.md`](./rollout-plan.md) |
| §6 lightweight interface | [`priorities.md`](./priorities.md) |
| §7 language choice | [`core-architecture.md`](./core-architecture.md) |
| §8 client portability | [`priorities.md`](./priorities.md) |
| §9 self-documentation | [`self-documentation.md`](./self-documentation.md) |
| §10 quality governance | [`workflows.md`](./workflows.md) |
| §11 declarative ownership | [`product-data-model.md`](./product-data-model.md) |
| §12 ownership-aligned placement | [`capability-placement.md`](./capability-placement.md) |
| §13 ambient product context | [`product-data-model.md`](./product-data-model.md) |
| §14 single derivation | [`advance-postmortem.md`](./advance-postmortem.md) §C1 |
| §15 atomic terminal transition | [`advance-postmortem.md`](./advance-postmortem.md) §C2 |
| §16 partial completion honesty | [`advance-postmortem.md`](./advance-postmortem.md) §C3 |
| §17 reclamation ground truth | [`advance-postmortem.md`](./advance-postmortem.md) §C4 |
| §18 operations scale with data | [`advance-postmortem.md`](./advance-postmortem.md) §C5 |
| §19 non-destructive recovery | [`advance-postmortem.md`](./advance-postmortem.md) §C6 |

---

*Constraints are commitments to qualities, not features. Resolved research entries
retain their accepted authority and rationale; later-phase open Product questions live
in `clarifications.md`.*

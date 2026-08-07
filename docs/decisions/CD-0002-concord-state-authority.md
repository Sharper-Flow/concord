# CD-0002 — SQLite as Concord's sole durable authority

> **Status:** **Accepted — binding until superseded.**
> **Type:** Architecture decision record.
> **Supersedes:** Earlier private pre-public durability planning (Temporal as durability/workflow authority).
> **Superseded/amended in part by:** [`../product-memory-authority-scope.md`](../product-memory-authority-scope.md) (PM2, 2026-08-05) replaces only the physical per-Project file scope with one global local SQLite authority; [`../product-memory-domain-schema.md`](../product-memory-domain-schema.md) (PM3, 2026-08-05) names the generic log `domain_events` and replaces the generic materialized spine with explicit typed projections; [`../canonical-git-note-placement.md`](../canonical-git-note-placement.md) (PM6, 2026-08-05) fixes git proof before eligibility; [`../compaction-retention-policy.md`](../compaction-retention-policy.md) (PM7, 2026-08-06) fixes bounded lazy projection pruning while retaining `domain_events`; [`CD-0009-active-research-context.md`](./CD-0009-active-research-context.md) (2026-08-07) creates the sole narrow direct-table exception for retention-bounded active research context; [`CD-0011-retain-sqlite-after-conformance.md`](./CD-0011-retain-sqlite-after-conformance.md) (2026-08-07) records the post-falsifier review and retains SQLite with explicit future reopen conditions.
> **Decided:** 2026-08-05 (operator decision D1, reopened under the simplicity mandate).
> **Confirmed by operator:** 2026-08-05, as the *revised* set following stack re-validation (see §2a). The re-validation reversed an earlier single-writer-daemon recommendation to library-in-process.
> **Resolves:** [`../core-architecture.md`](../core-architecture.md) §2; [`../design-constraints.md`](../design-constraints.md) research backlog #8; [`../clarifications.md`](../clarifications.md) C2.
> **Sourced basis:** adv-researcher D1 findings + 2026-08-05 stack re-validation (sqlite.org WAL / whentouse / busy_timeout / intern-v-extern-blob / fasterthanfs; Litestream; Temporal #9686; PostgreSQL MVCC docs; etcd-io/bbolt; PGlite; DuckDB), plus a **measured** ADV workload profile (§2a).

## Decision

**SQLite is Concord's sole durable authority.** An append-only generic event log
(`domain_events`, originally sketched as `transitions`) is the source of truth;
explicit typed current-state projections are folds over that log, held
in the same database, updated in the same transaction. No second store holds
authoritative state. No separate workflow engine.

This decision is the structural prevention of the Advance failure cluster
([`../advance-postmortem.md`](../advance-postmortem.md) §root cause and the
2026-08-02→08-05 cluster): that disease was **split state authority** between
Temporal and a disk projection, plus the reconciliation machinery around it.
Concord removes the split by construction, not by policy.

### Configuration (binding)

- **Journal mode:** `WAL`. Readers never block the writer and the writer never
  blocks readers.
- **Synchronous:** `NORMAL` (not `FULL`). Under WAL, `NORMAL` fsyncs at checkpoint
  rather than at every commit. The database **cannot corrupt**; the only exposure is
  that the most recent un-checkpointed transactions may be lost on power cut — which
  the append-only log + recovery fold (I5) absorbs. This removes fsync from the hot
  path. `FULL` is available as a per-deployment override *only* if a requirement
  emerges that every individual transition survive an immediate power cut with no
  backstop.
- **Concurrency: library-in-process — there is no daemon.** Concord is a library
  linked into whatever process needs it. Multiple Concord processes MAY open the
  same global local database. SQLite itself enforces one-writer-at-a-time per file
  (WAL + POSIX advisory locks); `PRAGMA busy_timeout = 5000` makes contending
  writers queue and take turns, with app-level `SQLITE_BUSY` retry as the backstop.
  This is SQLite's documented multi-process pattern and requires no coordinating
  process. *(A single-writer daemon is an explicitly documented **upgrade path**,
  not the default — see §2b.)*
- **Physical authority scope (PM2): one global local database per Concord
  installation/operator-machine.** Product and Project remain logical domain/
  authorization scopes, not files. Cross-Project work and shared membership commit
  atomically inside this one authority. Full decision and falsifiers:
  [`../product-memory-authority-scope.md`](../product-memory-authority-scope.md).
- **One database file** is the entire live Product-memory authority for the local
  installation. Back up = one consistent SQLite backup/checkpoint. Product
  portability uses a logical, non-authoritative export.

---

## 1. The structural invariants (this is the decision's real content)

These six invariants are binding. Each maps to a named Advance failure mode and
removes it structurally — so the disease cannot recur by "forgetting" a policy.

| # | Invariant | Advance failure mode it removes |
|---|---|---|
| **I1** | **Single authority.** SQLite is the only component whose state is authoritative. No projection, cache, or second store is ever treated as a transition-decision input. | Split state authority (Temporal ↔ disk projection) — `fixAdvStateAuthority`, the central anti-pattern. |
| **I2** | **Append-only log is Product/work truth.** Every Product/work identity, lifecycle, membership, relation, gate, and archive-linkage change is an immutable row in `domain_events`; current Product/work state is a materialized fold. CD-0009 active research context is the sole direct-table exception: it is explicitly disposable WIP, never retained history, and no other fact type inherits the exception. | Destructive/in-place mutation of retained Product/work state with no replayable history. |
| **I3** | **Projections are disposable.** Every retained read model (materialized current-state, indexes, derived views) is rebuildable from the log by a deterministic fold. CD-0009 active research tables are authority for their temporary context—not projections—and are deleted after proof-backed archive. | Projection drift — the disk projection became a co-authority that lagged Temporal truth (read-timeout cluster, stale reads). |
| **I4** | **Terminal transitions are single-statement atomic.** A move to a terminal state is ONE transaction: append the transition row AND update materialized state, committed **atomically together**. Durability follows the configured synchronous mode (under `NORMAL`, the un-checkpointed tail is covered by the recovery fold, I5). No two-phase "mark, then converge." | Terminal-durability gap — `fixArchiveTerminalDurability`; archive proof required signal application + attributable commit evidence, not bare liveness. |
| **I5** | **Recovery is a deterministic fold.** On any inconsistency, recovery = re-fold the log into a fresh materialized state. No reconciliation between two authorities; no destructive repair. | Destructive-only recovery (postmortem C6); cross-authority reconciliation surfaces. |
| **I6** | **Error/retry records are first-class, type-valid log rows.** Retry attempts, errors, and recovery actions are schema'd rows in the log — never free-form accumulator state that can become unreadable. | Doom-loop accumulator — `clampDoomLoopAccumulator`; the retry recorder wrote unreadable records and bricked changes. |

An implementation that violates any invariant is not Concord, regardless of
whether it uses SQLite.

---

## 2. Why SQLite, why not the alternatives

The D1 research surveyed five options ranked by simplicity-for-reliability.
SQLite single-authority won; the reasoning, condensed:

- **The disease was the split, not Temporal.** The public predecessor postmortem
  supports the narrower conclusion that the cost was the *projection* we
  built next to it and the reconciliation machinery that drifts. SQLite has no
  second store to drift from.
- **Replay value collapses under LLM non-determinism.** Temporal's headline
  deterministic-replay guarantee is meaningful only for steps that can be
  re-executed identically. Concord's workflow steps are LLM calls (non-deterministic
  Activities). Once steps can't replay, replay's value reduces to
  checkpoint-and-resume — which a bare append-only log already provides.
- **The scale does not warrant a workflow engine.** ~50 concurrent state machines,
  one operator, one machine. An append-only `domain_events` log + explicit typed
  projections in one transaction is the entire mechanism. No worker pool, no task queue, no
  server process, no version pinning.
- **Durability is a PRAGMA, not an architecture.** `synchronous=FULL` + WAL gives
  crash-safe committed transactions natively. Concord does not reimplement
  durability.

Rejected alternatives (full tradeoffs in D1 research):
- **DBOS on Postgres** — structurally avoids the split (Postgres sole authority),
  but adds a Postgres dependency for a single-operator local tool. Strong "buy"
  fallback if SQLite ever proves insufficient.
- **Restate single-node** — journaled state, drift-impossible, but a separate
  server binary. Viable "buy" fallback.
- **Keep Temporal, collapse projection to non-authoritative** — retains the
  heavyweight topology whose main payoff (replay) is largely empty here. Not
  recommended.
- **LMDB** — durable KV, but lacks SQL ergonomics for a 7-gate/task domain.

---

## 2a. Measured workload (the evidence this decision rests on)

The original analysis assumed a load. It was re-validated against **measured ADV
behaviour** (ADV is the predecessor running this exact workload), 2026-08-05:

| Dimension | Measured |
|---|---|
| True concurrent writers | Bounded short-lived local invocations |
| Sustained write rate | Low-volume coordination writes, with burst tolerance required |
| Writes per entity | Small transitions and bounded evidence records |
| Running entities | Many concurrent agents with idle-work reclamation |
| Artifact payload | Small bounded records; process exhaust is excluded |
| Durable-note cost | Human-readable notes, not serialized workflow dumps |

SQLite permits one writer at a time and queues short writes; adequacy depends on
transaction duration and actual overlap rather than a universal writer-count
threshold. Here the measured system-wide rate is **below 0.1 writes/second**, with no
concurrent write observed in the sampled window, so the expected overlap among roughly
eight potential writer processes is low. The ten-process CD-0008 conformance run owns
the falsifiable implementation proof. **The primary problem is durable ordering, not
demonstrated write throughput.** Accepted PM8 excludes WIP-byte/blob placement until a
named job proves it necessary.

## 2b. Concurrency model, and the daemon as an upgrade path

An earlier draft of this record recommended a **single-writer daemon**. The
re-validation reversed that recommendation, and the reversal is part of this
decision:

- **Default: library-in-process, no daemon.** sqlite.org's own guidance covers this
  case: *"Writers queue up. Each application does its database work quickly and
  moves on, and no lock lasts for more than a few dozen milliseconds."* A daemon
  adds process lifecycle, supervision, an IPC protocol, crash/restart recovery, and
  a **single point of failure** — real failure modes bought for no measurable
  benefit at the load in §2a.
- **Upgrade path (documented, not default).** Introduce a single-writer daemon *only*
  when a falsifier in §2d fires. The upgrade is additive: the library's write path
  moves behind an IPC boundary; the schema and invariants are unchanged.

## 2c. Storage tiers and the archive lifecycle

Concord has **four tiers**. The first two are authoritative for disjoint live fact
types; neither duplicates the other:

| Tier | Store | Holds | Lifetime |
|---|---|---|---|
| **Live Product/work** | one global local SQLite DB | `domain_events` log + explicit typed projections | event authority retained; eligible projections prunable only through PM7's bounded lazy transition |
| **Active research context** | direct versioned tables in the same SQLite DB | CD-0009 packs/revisions/findings/sources/consumer bindings only | while owner/required consumers are active; delete after proof-backed archive |
| **Durable knowledge** | **git, in the repo** (markdown) | compacted lessons, completed-work notes, decision records | permanent, versioned, diffable |
| **Process exhaust** | *discarded* | sub-agent reports, briefing digests, traceability dumps, intermediate state | dropped at archive |

**Archival is a compaction, not a serialization dump.** On reaching terminal state,
an entity's transition history is distilled into **one human-readable markdown note**
committed to the repo; verified terminal projections later become eligible for PM7's
bounded lazy pruning while `domain_events` remains retained authority. Target size is
a page, not hundreds of kilobytes.

CD-0009 active research is not serialized into that note or retained log. Archive may
promote selected reasoning/decisions/specs/lessons through their normal durable forms,
then deletes the complete active pack only after Git/linkage proof.

```
docs/work/YYYY-MM-DD-slug.md       # what shipped, why it mattered, links to commits/PRs
docs/lessons/YYYY-MM-DD-slug.md    # transferable lesson
docs/decisions/CD-NNNN-*.md        # durable decisions (this record is one)
```

**Binding rules for the durable tier:**
1. **Markdown only.** No JSON state blobs in git. Machine state lives in SQLite.
2. **Distillation, not mirroring.** The note is written for a future reader (human or
   agent), never as a dump of internal state.
3. **No duplication with SQLite.** A fact is operational (SQLite, while in-flight) or
   durable knowledge (git, after terminal) — never both as competing copies.
4. **Process exhaust is not archived.** Sub-agent reports, briefing digests, and
   traceability artifacts remain producer-owned; accepted PM9 retains no separate
   receipt because the existing event, approved note, and locator sequence is enough.
5. **Greppable.** An agent must be able to answer "have we solved this before?" by
   searching the durable tier.

**Anti-pattern being corrected.** A predecessor-style archive that duplicates
workflow state, embeds process exhaust, and leaves a template separate from its
real content is both a split source and a dump. Concord does neither: SQLite owns
live state, while approved git notes contain distilled durable knowledge.

### Why this does not recreate the split-authority disease

Adding a second store is precisely how Advance got sick, so the safety property is
explicit and binding. **The git tier is not a second authority.** It is:

- **(a) Write-only at/after terminal state** — never written for in-flight entities.
- **(b) One-way** — never read back as an input to any live transition decision. No
  code path consults git to decide state.
- **(c) Knowledge, not state** — it carries meaning for future readers, not
  operational status.

Because it is a one-way compaction of already-terminal data, it **cannot drift** from
the authority. **I1 is preserved.**

*(Secondary benefit: compaction bounds SQLite growth. Measured ADV per-project stores
run 26–93 MB; compacting at archive keeps the live database small and moves durable
value into git, where it is reviewable and travels with the code.)*

## 2d. Structured artifact placement

- **Markdown artifacts** (proposal, design, agreement, executive summary — measured
  3–6 KB median, 34 KB max) → **TEXT rows in SQLite**. In-DB is faster at this size
  *and* keeps the artifact write atomic with the entity's transition (satisfies I4).

Accepted PM8 supersedes the former external-evidence-file branch: Concord v1 has no
WIP blob ingestion, content-addressed store, hash/path reference, or generic screenshot
requirement. Test output and other WIP bytes remain with their producing environment.

## 2e. Falsifiers — what would overturn this decision

A decision that cannot be proven wrong is not engineering. Instrument these from day
one (log write-latency percentiles and `SQLITE_BUSY` counts):

| Signal | Threshold | Response |
|---|---|---|
| Write-latency tail | P99 write > ~100 ms sustained | introduce the §2b daemon |
| `SQLITE_BUSY` escapes | errors surface past a 5 s `busy_timeout` | raise timeout, then daemon |
| Contending writers per project DB | > ~50–100 concurrent | daemon or finer sharding |
| Sustained write rate to one project DB | > ~hundreds/sec | daemon or finer sharding |
| Named durable large-byte job | satisfies PM8 reopen criteria | reopen PM8 before selecting any storage mechanism |
| Deployment scope | a second machine, or access over NFS | **Postgres** (SQLite-over-NFS is a documented corruption risk) |

Entity count is *not* a falsifier: SQLite handles millions of rows; ~50–300 entities
is trivial.

---

## 3. Consequences

### What this gives Concord
- **Simplicity.** One file, one writer, one language boundary fewer (see CD-0003).
  The entire durability story is SQLite + six invariants.
- **Reliability by construction.** The Advance disease class is structurally
  impossible while the invariants hold — there is no second authority to drift
  from and no two-phase terminal transition to gap.
- **Operability.** Back up, inspect, and debug by copying/opening one file. No
  Temporal server to keep alive (the "target project worker not live" failure mode
  does not exist).
- **Crash safety for free.** `synchronous=FULL` is a tested, decades-old guarantee.

### What this costs
- **Concord owns its recovery model.** Without Temporal's replay, Concord must
  implement the recovery fold (I5) and the crash-recovery test. This is bounded —
  it is a fold over one table — but it is real work and the slice (below) validates
  it first.
- **No distributed guarantees.** Single-writer, single-machine. If Concord ever
  needs multi-writer or multi-machine, this decision is revisited (DBOS/Postgres is
  the documented upgrade path). For a single-operator local tool this is a feature,
  not a limitation.

### What this forecloses
- **Multi-writer concurrency.** One writer at a time. (Readers are concurrent.)
- **Cross-machine state.** The database is local. Synchronization would be an
  explicit later layer (e.g., git-based export), never an authority.

---

## 4. Error handling and rollback (addresses proposal clarify-flag)

- **Transaction rollback.** A transition that fails validation is never committed —
  the single transaction rolls back; materialized state is unchanged. There is no
  "half-applied" state to clean up (I4).
- **Crash mid-transaction.** SQLite atomicity: the transition either fully
  committed or did not. On restart, materialized state is either at the pre- or
  post-transition value, never between. The crash-recovery test (slice) asserts
  this falsifiably.
- **Corrupted/partial write.** WAL + `synchronous=FULL` + the recovery fold (I5):
  drop the materialized projections, re-fold from `domain_events`. The log is
  append-only and fsynced; it is the durable survivor.
- **Schema-invalid log row (unreachable if I6 holds).** Per design-constraints #7
  (validation-failure isolation): isolate to the owning operation; never fail-closed
  across unrelated operations. I6 makes this rare by construction.

---

## 5. Core language and internal boundary (agent adapter resolved by CD-0005)

The six invariants are language-independent, but the core needs a language.
**Operator direction (2026-08-05): Go core + short-lived CLI**, with Temporal
removed and SQLite as the core's durable store. Accepted TS6/CD-0005 now selects one
global `concord.ts` custom-tool adapter; the internal CLI still does not define tools.

- **Go core** holds the SQLite authority, the transition logic, the recovery fold,
  and (eventually) the standalone CLI and admin-panel backend. SQLite access via a
  pure-Go binding (`modernc.org/sqlite`) — no CGO, single static binary, trivial
  cross-compilation; performance is ample at ~50 state machines.
- **Accepted TypeScript adapter** registers the eight CD-0005 tools and calls the
  CLI. It is not a plugin and holds no domain logic or authoritative state.

**The no-daemon decision (§2b) largely resolves the boundary question.** With
library-in-process and no long-running writer, the natural shape is a **short-lived
Go binary invoked per operation**: an accepted adapter/client shells out to
  `concord …`, the binary opens the global local SQLite file, performs its
transaction in milliseconds, and exits. Concurrent invocations are serialized by
SQLite itself via WAL + `busy_timeout` — exactly the pattern §2b endorses.
There is no IPC protocol to design, no supervisor, and no SPOF. A long-running
MCP-server topology is deliberately *not* adopted, because it reintroduces the
daemon that §2b demoted.

**Resolved by [`CD-0003-concord-storage-layer-shape.md`](./CD-0003-concord-storage-layer-shape.md) (2026-08-05):**
1. **Invocation boundary** — **short-lived CLI invocation** (D2). No persistent server.
2. **Binding** — **`modernc.org/sqlite`**, pure Go (D3). `mattn` is the documented fallback.
3. **Entity shape** — PM3 narrowly supersedes CD-0003 D1's generic materialized
   spine with **explicit typed Product-memory projections**, while retaining one
   generic authoritative event log and typed-edge integrity.

The storage-spine slice records the current baseline. PM1–PM3 now fix canonical
queries, authority scope, domain projection shape, lifecycle/relation semantics,
and membership scope. PM1–PM5 now authorize storage/core implementation.

---

## 6. Supersession of private pre-public planning

- The earlier private Temporal commitment and its go/no-go proofs are not public
  authority. The durable lesson retained here is that each Concord subject has an
  ordered event sub-log within one global local SQLite file.
- The earlier layering proposal is superseded by the public Go core, SQLite sole
  authority, and thin adapter boundaries recorded by CD-0002 through CD-0006.

---

## 7. Deferred questions now resolved

- **Migrations / schema evolution — resolved by CD-0008 D6 (2026-08-06):** typed,
  ordered, deterministic upcasters; independently versioned projections; complete
  replay tests; fail-closed newer-than-supported events; pinned active workflow
  versions; point-in-time reconstruction; and snapshots only after a measured replay
  falsifier.

**Now closed by CD-0003 + PM3 (2026-08-05):** invocation boundary (short-lived
CLI), SQLite binding (`modernc.org/sqlite`), generic event authority, and explicit
typed Product-memory projection shape. See
[`CD-0003-concord-storage-layer-shape.md`](./CD-0003-concord-storage-layer-shape.md)
and [`../product-memory-domain-schema.md`](../product-memory-domain-schema.md).

**Now closed by the compaction design (2026-08-05):** compaction trigger (at
terminal transition), note template, generation pattern (agent-drafts +
operator-approves), salvage rule, and the git-authority/SQLite-derived-index
pattern. See [`../compaction-design.md`](../compaction-design.md).

**Closed earlier by this record:** structured artifact placement (§2d — markdown as
TEXT rows; PM8 excludes WIP byte storage), concurrency topology (§2b —
library-in-process), and durable-knowledge storage (§2c — git, one-way, non-authoritative).

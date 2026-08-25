# CD-0003 — Concord storage-layer shape

> **Status:** **Accepted — binding until superseded.**
> **Type:** Architecture decision record.
> **Supersedes:** none.
> **Superseded in part by:** [`../product-memory-domain-schema.md`](../product-memory-domain-schema.md) (PM3, 2026-08-05) replaces D1's generic materialized `entities` spine with explicit typed Product-memory projections. D1's generic authoritative log and typed-edge integrity principle, plus D2/D3, remain binding.
> **Decided:** 2026-08-05.
> **Resolves:** [`CD-0002-concord-state-authority.md`](./CD-0002-concord-state-authority.md) §5 (invocation boundary, binding) and §7 (entity shape is the remaining fork inside the authority).
> **Sourced basis:** CD-0002 (Accepted) + the measured ADV profile (§2a of CD-0002: <0.1 writes/sec, ~8 true concurrent writers, 3–6 KB artifacts, 50–300 entities) + documented library characteristics (modernc.org/sqlite, mattn/go-sqlite3, SQLite WAL).

## Decisions (summary)

| # | Decision | Choice |
|---|---|---|
| **D1** | Entity shape | **Generic spine + typed facet tables only where genuinely relational.** |
| **D2** | Invocation boundary | **Short-lived CLI invocation.** No persistent server. |
| **D3** | SQLite binding (Go core) | **`modernc.org/sqlite`** (pure Go, no CGO). |

Each is decided below with reasoning. None violates CD-0002 invariants I1–I6.

---

## D1. Entity shape — generic spine + typed facets (projection shape superseded by PM3)

### Decision
A later accepted decision, PM3, replaces the generic materialized `entities`
projection below with explicit typed Product-memory projections. The generic
append-only event log and typed-edge integrity principle remain current. The
original decision follows for rationale and supersession history.

A **universal spine** serves every entity type:

```sql
entities(id, type, current_state JSON, current_version, last_seq, updated_at)
transitions(seq, entity_id, from_state, to_state, kind, payload JSON, actor, prev_seq, created_at)
artifacts(entity_id, kind, body TEXT, version)          -- CD-0002 §2d: markdown as TEXT, <100KB
```

Entity types (change, task, epic, product, decision, …) are a **`type` discriminator on `entities`**, not separate tables. Per-type **state-machine behavior lives in typed code handlers** keyed on `type`, not in the schema.

A **typed facet table** is introduced **only** when an entity type has a relation that is *both* (a) genuinely many-to-many / graph-structured AND (b) queried (not merely stored). Candidate facets:

```sql
task_edges(task_id, blocks_task_id)        -- task dependencies; queried for ready-task computation
epic_members(epic_id, change_id, order)    -- epic membership; queried for epic rollups
product_members(product_id, member_id, role) -- product membership; queried for scoping
```

### Reasoning
- The `transitions` log is **genuinely generic**: every entity is a state machine over the log, regardless of type. Typed tables would force typed transition logs — N tables, N migration paths, N recovery folds — for zero relational benefit.
- All entity types share the same relational core (id / type / state / version / lifecycle). There is no per-type column structure that SQL would exploit better than a `current_state` JSON column + a typed code handler, *until* a relation needs joining.
- The **facet rule** is measured, not speculative: start generic; add a facet only when a query is slow or a JSON-array scan in `current_state` becomes painful. Task blockedBy and epic membership clear that bar today (ready-task computation and rollups are real, frequent queries). Product membership likely does too. A change's gates do not (sequential, stored in order, rarely joined).
- This is the **simplest shape that doesn't paint us into a corner**: the spine stays uniform (satisfies the slice and I1–I6), and relational needs are met incrementally without a schema migration of the spine.

### Constraints
- **Facets are projections/relations over the spine, never a second authority.** A facet row must be derivable from (or invalidated by) the entity's transition history; it must never hold state that the spine cannot reproduce. This preserves I1 (single authority) and I3 (projections disposable).
- A facet table is created by an explicit decision (a short note in this record's amend​ments), naming the query that justified it.

---

## D2. Invocation boundary — short-lived CLI invocation

### Decision
Concord's Go core is exposed internally as a **short-lived CLI**: an accepted
adapter/client shells out to `concord <command>`, the process opens the current
authority database, performs its transaction(s) in milliseconds, and exits. There
is **no persistent server, no MCP daemon, no long-running writer process**. TS6
decides which commands, if any, become agent tools.

### Reasoning
- **CD-0002 §2b already decided "no daemon."** A persistent MCP server *is* a daemon (long-running, with lifecycle, supervision, and a single point of failure (SPOF)). The no-daemon decision rules it out.
- Short-lived invocations are the **no-daemon-compatible boundary**: each process opens the DB, and SQLite serializes concurrent invocations itself via WAL + `busy_timeout=5000` (CD-0002 §2). This is precisely the multi-process pattern sqlite.org endorses ("writers queue up… no lock lasts more than a few dozen milliseconds").
- It removes an entire class of failure modes (process lifecycle, IPC protocol versioning, crash/restart, supervisor) that a daemon would add.
- Cross-language simplicity: a TypeScript adapter, if selected, shells out via
  `child_process`; no FFI, native addon, or WASM bridge. Any approved client can
  invoke the same CLI, but CLI commands are not automatically public tools.

### Constraints / open detail
- **One invocation = one logical operation** (or one small batch). A CLI process is not held open across an agent's whole session. Long-lived session state lives in the SQLite file between invocations, not in a process.
- The CLI's stdout is **structured** (JSON) for programmatic callers; a `--human` flag renders for terminal use.
- **Reversal condition:** the §2e falsifiers are "P99 write > 100 ms sustained" and "SQLITE_BUSY escapes busy_timeout". If either fires at realistic load *because of process-startup overhead*, escalate per CD-0002 §2b to the daemon upgrade path. The CLI commands then become the daemon's request handlers, and the surface is unchanged.

---

## D3. SQLite binding — `modernc.org/sqlite`

### Decision
The Go core uses **`modernc.org/sqlite`** — a pure-Go (transpiled-from-C) SQLite driver, accessed through `database/sql`. No CGO.

### Reasoning
- **Performance is a non-factor at the measured load** (<0.1 writes/sec; ~8 true concurrent writers). The CGO speed advantage of `mattn/go-sqlite3` is irrelevant here.
- **Distribution simplicity wins:** pure Go means a single static binary, no C toolchain requirement, trivial cross-compilation — exactly the "much simpler implementation" mandate. CGO would force a build dependency (and cross-compile complexity) for no measurable benefit.
- modernc.org/sqlite is a mature, fully-featured port (it passes SQLite's own test suite); the PRAGMAs CD-0002 relies on (WAL, synchronous, busy_timeout) are all supported.

### Constraints
- `mattn/go-sqlite3` (CGO) remains the documented fallback **iff** a measured hot path shows modernc is the bottleneck — which, at the CD-0002 §2a load, is not expected. Switching is a driver swap, not an architecture change.
- Measure modernc-specific latency as implementation acceptance evidence (part of
  the §2e day-one instrumentation), not as an architecture-decision prerequisite.

---

## Consequences

### What this gives
- **A coherent current storage/schema baseline.** PM2 fixes one global local DB;
  PM3 fixes one generic authoritative event log plus explicit typed Product-memory
  projections. PM4 fixes lifecycle/relation semantics; PM5 fixes membership/scope. This
  record remains binding only where PM3 did not supersede it.
- **Explicit core + bounded extension richness.** Stable identity, joins,
  invariants, and PM1 filters are typed; versioned JSON is reserved for rare
  extensions.
- **Zero daemon, zero CGO.** The simplest possible deployment: one static Go
  binary + PM2's one global local SQLite authority.
- **Any-client internal boundary.** The CLI can serve accepted adapters/clients,
  but it does not define the agent-facing tool surface (TS1–TS7 do).

### What it costs
- **Projection-schema evolution.** Stable fields require typed migrations; bounded
  versioned JSON extensions prevent table-per-kind churn. PM3's falsifiers govern.
- **Process-startup overhead per operation.** A CLI invocation pays process-spawn + DB-open cost (~low ms) per call. Acceptable for this load; the reversal condition above governs if it ever isn't.

### What it forecloses
- **If a TypeScript plugin is selected by TS6,** it cannot link the Go core as a
  library; it shells out. TS6 may instead choose a direct host wrapper and omit
  the plugin.

---

## Relationship to CD-0002 and the open-questions picture

- **Closes** CD-0002 §5 (boundary, binding) and the entity-shape fork implied by §7.
- **Does not close** (out of scope, listed for completeness):
  - migrations and schema evolution (CD-0002 §7);
  - the compaction-function design (separate companion doc [`../compaction-design.md`](../compaction-design.md));
  - the recovery-path taxonomy (design-constraints RB #5);
  - validation-failure isolation (RB #7);
  - evidence-resolution completeness (RB #6).

  These shape Concord-the-product or the durable tier, not the storage-spine build.
- The **storage-spine slice** (`storage-spine-slice.md`) is an implementation
  acceptance plan. PM1 defines canonical jobs; PM2/PM3 choose scope/schema through
  research; PM1–PM5 are complete and the slice validates the accepted result.

# Concord Storage-Layer Acceptance Slice

> **Status:** **Ready for implementation acceptance.** PM1–PM10 are accepted.
> Execute after CD-0007 repository creation/public migration is explicitly authorized
> and complete. This validates the selected architecture; it is not architecture-
> decision evidence and does not authorize agent tools or stable release.
> **Depends on:** [`product-memory-query-contract.md`](./product-memory-query-contract.md),
> [`product-memory-authority-scope.md`](./product-memory-authority-scope.md),
> [`product-memory-domain-schema.md`](./product-memory-domain-schema.md),
> [`product-memory-lifecycle-relations.md`](./product-memory-lifecycle-relations.md),
> [`product-memory-membership.md`](./product-memory-membership.md),
> [`compaction-retention-policy.md`](./compaction-retention-policy.md),
> [`clarifications.md`](./clarifications.md),
> [`decisions/CD-0007-concord-repository-bootstrap.md`](./decisions/CD-0007-concord-repository-bootstrap.md), and
> [`decisions/CD-0008-concord-mechanism-hardening.md`](./decisions/CD-0008-concord-mechanism-hardening.md), and
> [`decisions/CD-0009-active-research-context.md`](./decisions/CD-0009-active-research-context.md).
> **Language/boundary:** Go core, `database/sql` + `modernc.org/sqlite`, short-lived
> CLI invocation, no daemon. TS6 decides any adapter.
> **Binding mechanism input:** CD-0008 D1–D7 governs worktree authority, immutable-subject
> evidence, query-dependent unreadable isolation, checkpoints/fencing, typed conditions,
> upcasters, SQLite conformance, and the alternative-engine falsifier trigger.

## 1. Purpose

Validate the accepted PM1–PM5 storage contract through the thinnest implementation
that proves Concord has one authority, deterministic projections, atomic domain
operations, bounded Product-memory reads, and reliable recovery. If the slice fails
an accepted falsifier, revisit the owning decision instead of adding reconciliation.

This is not the 7-gate model, a tool surface, or a generic entity platform.

## 2. Accepted architecture under test

The slice must preserve these decisions without reinterpretation:

1. **One global local SQLite authority** per Concord installation/operator-machine
   (`WAL`, `synchronous=NORMAL`, `busy_timeout=5000`).
2. **One generic append-only `domain_events` log** as live-state authority.
3. **Explicit typed Product-memory projections** for stable Product, Project,
   Component, work, membership, relation, label, and external-reference concepts.
4. **Bounded versioned JSON extensions** only for rare per-kind metadata—never
   identity, lifecycle, membership, relations, joins, or PM1 filter/order keys.
5. **One transactional domain-operation path:** accepted event append(s) and every
   affected projection update commit atomically.
6. **One deterministic rebuild path:** all live projections can be discarded and
   regenerated from `domain_events` with no information loss.
7. **Git remains durable-knowledge authority** after approved compaction; SQLite's
   historical index is derived and rebuildable.
8. **Accepted PM4 lifecycle/relations:** five lifecycle states; derived
   blocked/ready/active/terminal views; canonical typed edges; atomic supersession;
   cycle rejection; explicit reopen; foreign key (FK) clean external blockers.
9. **Accepted PM7 retention:** verified terminal work can transition atomically from
   live projections to git-derived historical projections through bounded lazy
   pruning; `domain_events` remains replay authority and pruned IDs never reopen.

PM5 supplies membership semantics. Exact DDL is implementation design constrained
by PM1–PM10 and CD-0008; this plan does not pre-authorize extra columns or enums.

## 3. Minimum implementation slice

Implement only enough to exercise the accepted contract:

- schema migration and connection initialization, including per-connection
  `PRAGMA foreign_keys=ON`;
- `domain_events` with stable event ID/order, event kind, subject reference,
  actor/time, `payload_version`, and validated payload;
- the minimum typed projections needed by PM1 Q1–Q8;
- typed relation tables with PM4 validation and membership tables with PM5 semantics;
- `apply_domain_operation(...)`, the sole mutation boundary;
- `rebuild_from_log()`, which creates fresh projections from the complete accepted
  event-version history;
- a storage-neutral adapter for the PM1 scenario corpus;
- no agent tools, plugin, MCP server, compaction automation, or production deploy;
- no active research-pack implementation; this slice must only preserve CD-0009's
  boundary by keeping research content out of retained `domain_events`.

Event subjects are the one deliberate application-validated referential seam: SQLite
cannot express one foreign key targeting several typed projection tables. Domain
relations themselves must not use polymorphic unenforced endpoints. Exact event-
subject representation remains implementation design and must fail closed on unknown
or incompatible subjects.

## 4. Required acceptance evidence

### 4.1 Authority and recovery

- Prove no projection has an independent write path.
- Drop every live projection, rebuild from `domain_events`, and compare complete
  canonical content—not row counts alone.
- Interrupt append/project/rebuild/migration paths at each transaction boundary;
  prove recovery yields either the complete prior state or complete next state.
- Corrupt or reject one event payload/version and verify typed, bounded failure;
  never skip it silently or create partial authority.

### 4.2 PM1 correctness and boundedness

- Run the complete PM1 scenario corpus through the candidate adapter. Q9/Q10 use
  fixture-backed git-derived note/index inputs in this slice; this proves the query
  boundary without adding compaction automation or making SQLite their authority.
- At 10× the measured ADV dataset, record `EXPLAIN QUERY PLAN`, rows scanned,
  fan-out calls, output bytes, P50, and P99 for every bounded metadata query.
- Meet PM1's P99 ≤100 ms local target with no application fan-out, unbounded scan,
  or unbounded JSON traversal.
- Prove deterministic ordering, pagination, authoritative-empty behavior, typed
  degradation, and single-record identity for cross-Project work.
- Treat PM1 Q1–Q8 as live-memory queries whose default authority is Concord's
  SQLite-backed operational memory. Treat PM1 Q9–Q10 as git-derived durable-knowledge
  queries whose index is rebuildable and must expose its commit/hash and watermark;
  neither class may silently substitute the other.
- Exercise live-plus-historical Q2/Q3 counts and listings, including stale historical
  index coverage, pruned-ID follow-up links, and retained Q7 replay history.

### 4.2a CD-0008 evidence and unreadable-record scenarios

- **Scenario 5:** an unreadable possible blocker never yields `ready`, no-conflict,
  or release-safe; return `undetermined` with the explicit unreadable set.
- **Scenario 6:** an unrelated unreadable record does not block an independently
  provable operation outside its bounded dependency/touch closure.
- **Scenario 9:** evidence bound to one immutable commit cannot authorize a changed
  commit; the changed subject requires new evidence and normal freshness review.

### 4.3 Concurrency and durability

- Run realistic concurrent short-lived CLI writers and readers; measure queue time,
  `SQLITE_BUSY`, retry count, and committed latency.
- Exercise graceful exit, process kill, and machine/power-loss simulation under the
  documented `synchronous=NORMAL` exposure.
- Run the CD-0008 D7 bounded **ten-process SQLite conformance/load test** from ten
  isolated worktrees. Use realistic short transactions and exercise concurrent
  reads, competing expected-version writes, cross-row lifecycle/relation mutations,
  corruption/poison records, process death, resume/fencing, backup, rebuild, and
  binary-upgrade recovery.
- SQLite is selected. Reopen engine selection only if the run reproduces escaped
  `SQLITE_BUSY`, P99 above the accepted local target, unacceptable queueing,
  lost/duplicate effects, or an invariant violation. A fired falsifier compares
  single-writer IPC, Dolt server, and Postgres/DBOS without promoting Dolt by default.
- Record CD-0008 scenario 11's correctness-effects matrix for the SQLite run:

  | Effect | Required classification |
  |---|---|
  | accepted | valid operation commits exactly once and preserves invariants |
  | lost | forbidden; no accepted effect may disappear |
  | duplicate | forbidden; idempotent retry must not create a second effect |
  | rejected | expected invalid/stale/conflicting operation is refused with typed evidence |
  | invariant-violating | forbidden; no candidate may commit a broken cross-row state |

- Trigger CD-0002's upgrade review if sustained P99 or escaped busy failures cross
  its accepted falsifiers; do not add a hidden coordinating daemon.

### 4.4 Schema evolution and integrity

- Rebuild across every accepted event `payload_version` using the CD-0008 D6 typed,
  ordered, deterministic, side-effect-free upcaster registry.
- Prove foreign-key enforcement is enabled on every connection.
- Verify extension JSON against closed per-kind/version schemas.
- Demonstrate that a repeated PM1 filter is typed/indexed rather than traversed from
  JSON. Account for SQLite's restriction that `ALTER TABLE ADD COLUMN` cannot add a
  STORED generated column when selecting migration strategy.

## 5. Success and failure

**Pass:** all PM1 scenarios and CD-0002 I1–I6 hold under normal, concurrent, crash,
rebuild, and migration paths; query and load targets pass with complete evidence.

**Fail:** any second authority, independent projection mutation, non-total rebuild,
partial transaction, unenforced domain edge, duplicate cross-Project identity,
unbounded JSON/query path, silent degraded answer, or unexplained acceptance-target
miss. Failure reopens the owning PM decision; it never authorizes a repair layer.

## 6. Explicit non-goals

- No generic `entities(type, current_state JSON)` materialized spine; PM3 supersedes it.
- No table per work kind or event log per projection type.
- No mutable-current-row plus audit-log authority.
- No comparative engine benchmark or PoC absent an accepted SQLite falsifier; no test
  is used retroactively to choose PM1–PM5 architecture.
- No CLI-command-to-agent-tool mapping; TS1–TS7 own that surface.
- No production readiness claim; PM6/PM7 fix note placement and projection retention,
  and PM8 excludes WIP-byte storage; PM9 rejects a separate receipt; PM10 defines
  backup, restore, and garbage collection.

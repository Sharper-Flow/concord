# Concord Product-Memory Authority Scope (PM2)

> **Status:** **Accepted — binding until superseded.**
> **Accepted by operator:** 2026-08-05.
> **Decision:** PM2; direct accepted authority for Concord's global physical Product-memory authority scope.
> **Binding inputs:** accepted [`product-memory-query-contract.md`](./product-memory-query-contract.md), CD-0002 invariants I1–I6, and the one-operator/one-machine operating envelope.
> **Research basis:** official SQLite documentation (also queried through Context7 `/sqlite/sqlite`) plus Exa-discovered comparable local-first/developer-tool architectures. No local benchmark or PoC is part of this decision.
> **Supersedes narrowly:** CD-0002's "one database file per Project"
> configuration. SQLite/WAL/NORMAL/library-in-process/I1–I6 remain binding.

## 1. Decision

Concord uses **one global local SQLite database per operator installation** as the
sole authority for live Product memory across every Product and Project on that
machine.

- **Physical home:** the platform's per-user application-data directory, outside
  any Project repo. Linux default: `$XDG_DATA_HOME/concord/concord.db` (fallback
  `~/.local/share/concord/concord.db`).
- **Logical boundaries remain first-class:** Product and Project scope are enforced
  by the domain model and authorization, not by separate database files.
- **Repo-local durable knowledge remains separate:** completed-work notes,
  decisions, and lessons live in git; SQLite holds only a derived knowledge index.
- **Active research remains local and temporary:** CD-0009 pack tables live in this
  same SQLite database while their Initiative/change/research owner is active, never become
  Git knowledge, and are deleted after proof-backed archive.
- **Product portability uses logical export:** an export is a versioned,
  non-authoritative snapshot unless explicitly imported into another installation.
- **No per-Product or per-Project live-authority shards.** They add fan-out,
  duplicated facts, non-atomic cross-file relationships, or a coordinating catalog.

PM2 does **not** choose PM3 schema, PM4 lifecycle/relations, PM5 membership tables,
or any tool/CLI/plugin surface.

## 2. Decisive constraints

### 2.1 PM1 requires one coherent Product-memory corpus

Accepted PM1 requires direct, bounded answers for:

- all Products and their Projects,
- Product-wide work snapshots and lists,
- shared-Project ownership ambiguity,
- one canonical work item spanning Projects,
- dependencies/history and durable-knowledge retrieval.

PM1 explicitly rejects application fan-out across authority stores. A global file
answers each job within one authority and one query planner. Physical sharding
turns Product memory into an application-level aggregation problem.

### 2.2 Under the accepted WAL configuration, the atomic unit is one file

Official SQLite guarantees decide the write side:

- In WAL mode, transactions are atomic within each database file but **not
  crash-atomic across attached files as a set** (`lang_attach.html`, `wal.html`).
- SQLite's rollback-journal super-journal supports multi-file atomic commit only
  outside the accepted WAL configuration (`atomiccommit.html`).
- Online Backup API and `VACUUM INTO` snapshot one database file; SQLite provides
  no native corpus-wide point-in-time snapshot across independent files
  (`backup.html`, `lang_vacuum.html`).
- WAL requires processes on one host (`wal.html`), matching Concord's initial
  operating envelope.

One global file therefore allows cross-Project work, shared membership,
work→Project validation, and Project removal to be one transaction and one backup.

## 3. Candidate assessment

| Candidate | PM1 reads | Relationship atomicity under WAL | Backup/restore | Portability | Verdict |
|---|---|---|---|---|---|
| **Global DB** | One authority; no app fan-out | One-file transactions | One coherent snapshot | Logical Product export | **Select** |
| Per-Product DBs | Q1 enumeration/reverse ownership fans out unless a catalog exists | Shared-Project facts span Product files | N independent snapshots | File-native only if a Product is fully isolated | Reject for current Product model |
| Per-Project DBs | Product-wide jobs fan out; cross-Project work is duplicated/deduped | Cross-Project updates span files | N independent snapshots | Multi-file Product bundle | Reject |
| Global catalog + per-Product work | Reads can be routed without fan-out | Catalog membership and Product work references span files; no cross-file FK/atomic txn | N+1 independent snapshots | Catalog subset + Product file bundle | Credible fallback, reject for complexity |

### Global — selected

Strengths:

- exact fit for PM1's Product-wide and cross-Project jobs,
- no duplicate Product/Project/work authority,
- one transaction for cross-scope invariants,
- one coherent backup/restore unit,
- simplest operational shape and fewest failure modes.

Costs:

- **wide failure blast radius** for live operational memory; PM10 must prove
  automatic backup and wipe-machine restore,
- Product export is logical rather than copying one native Product file,
- all local writers share one WAL writer lock; CD-0002's BUSY/P99 instrumentation
  and crossing conditions remain binding.

### Per-Project — explicitly superseded

Per-Project was CD-0002's baseline and is now superseded. It conflicts with
Concord's purpose:

- Product-wide reads must open/merge Project stores,
- one cross-Project work item needs duplicate rows or cross-file coordination,
- WAL cannot crash-atomically update the involved files,
- Product backup/export becomes a multi-file reconciliation operation.

This is the exact class of distributed local truth Concord is intended to avoid.

### Per-Product — rejected unless isolation becomes a requirement

Per-Product is attractive when each Product is a complete tenant/export/security
unit. Concord's current Product model allows Projects to belong to multiple
Products, and PM1 includes all-Product orientation and Project→Product ambiguity.
Without a catalog those reads fan out; with a catalog the design becomes the
hybrid below.

### Catalog + per-Product — first fallback, not the default

A global identity/membership catalog plus Product work files can route PM1 reads
cleanly. It is rejected now because it introduces two authoritative file classes:

- creating/relating work validates catalog membership and writes a Product file,
- removing/moving a Project touches catalog and Product work references,
- WAL gives no crash-atomic transaction or foreign key across those files,
- backup/restore and Product export require a coordinated bundle,
- repair/outbox/2PC would recreate coordination machinery Concord is removing.

Adopt this only if hard Product-level isolation/export/encryption becomes more
important than single-transaction Product memory.

## 4. External alignment

The selected shape is a standard durable local architecture, not a bespoke response
to current load.

A credible contrary pattern is **repo/vault-local authority plus a global derived
index** (for example Fossil/Beads/Engram-like systems). It is correct when the
repo/vault is the complete ownership and transaction unit. Concord's accepted PM1
contract differs: one Product spans Projects, one work item may span Projects, and
Product-wide reads cannot fan out across authorities. Therefore those systems
validate the authority/projection distinction but do not justify per-Project live
authority for Concord.

| Source/pattern | What it validates | Relevance/caveat |
|---|---|---|
| SQLite `whentouse`, atomic commit, WAL, ATTACH, backup, limits | SQLite as an application file; one-file atomicity/backup; WAL cross-file limits | Primary authority for DB behavior |
| Fossil | One self-contained SQLite corpus can hold repository, tickets, wiki, forum, and history | A Fossil repo is its full ownership unit; supports coherent-file principle |
| Litestream | Reliable replication/backup around a single SQLite file/server | Supports operational mitigation for global-file blast radius |
| Datasette | SQLite files as portable publication/query units | Supports logical/file export patterns, not authority partitioning |
| Turso / Cloudflare D1 DB-per-tenant guidance | Per-tenant databases can serve tenant isolation and horizontal scale | Those are not current single-operator requirements; N-shard migrations are a cost |
| File-backed agent-session memory | The OpenAI Agents SDK persists a session when given a SQLite file path | Illustrative only, not a Product-memory ownership or scope precedent |

### Source authority and caveats

| Source | Authority | Claim used here | Caveat |
|---|---|---|---|
| sqlite.org `lang_attach`, `atomiccommit`, `wal`, `backup`, `limits`, `whentouse` | Primary official docs | transaction/backup/host/limit guarantees and application-file positioning | Controlling behavioral evidence |
| Context7 `/sqlite/sqlite` | Indexed official source/docs | WAL/concurrency and pager/source corroboration | Convenience layer; sqlite.org URLs remain canonical |
| Fossil technical overview | Primary project docs | one self-contained SQLite repository can hold code/history/tickets/wiki/forum | A Fossil repository is a complete ownership unit; not identical to a Concord Project |
| Litestream docs | Primary project docs | operational replication/restore around a single SQLite DB/server | Backup mitigation, not authority-scope proof |
| Datasette docs | Primary project docs | SQLite file publication/portability | Read/publish pattern, not write-authority architecture |
| Turso multi-tenancy + Cloudflare D1 docs | Vendor docs | per-tenant DBs are chosen for tenant isolation/scale/export concerns | Cloud multi-tenant concerns; not the current local operating envelope |
| OpenAI Agents SDK SQLite session docs | Official SDK docs | a single file-backed local session store is a supported agent pattern | Session memory only; illustrative, not controlling |

Exa was used for discovery/comparison; official/primary sources above control
factual claims. No unsourced internal SaaS storage claims are made.

## 5. Portability and recovery

- **Backup:** one consistent SQLite backup/checkpoint plus git backups. PM10
  must automate and test this before production readiness.
- **Product export:** versioned logical bundle containing Product identity,
  Project membership, canonical work/relations/events allowed by policy,
  knowledge locators, and ordinary external references allowed by policy. The bundle is
  not live authority.
- **Import:** deferred; it must validate schema/IDs and cannot silently merge.
- **Corruption recovery:** use SQLite's documented backup/integrity/recovery tools;
  derived indexes rebuild from their canonical stores.

## 6. Falsifiers and future paths

Reopen PM2 when any becomes a real requirement:

1. **Second host writes:** WAL is same-host; evaluate Postgres or a proven
   replicated/CRDT SQLite architecture.
2. **Hard Product isolation:** independent encryption keys, legal retention,
   backup, deletion, or file-native export → reconsider catalog + per-Product.
3. **Load falsifier:** sustained BUSY/P99 or data/query scale crosses CD-0002's
   documented thresholds after indexing/optimization.
4. **PM1 changes:** a repeated accepted job requires another authority boundary.
5. **Recovery failure:** one-file backup/restore cannot meet PM10 objectives.

## 7. Consequences and narrow supersession

Accepted consequences:

- CD-0002 changes from **"one database file per Project"** to **"one global local
  database per Concord installation/operator-machine."**
- CD-0002's SQLite engine, WAL, `synchronous=NORMAL`, library-in-process,
  `busy_timeout`, and I1–I6 remain binding.
- PM3 now narrowly supersedes CD-0003 D1's generic materialized spine with explicit
  typed Product-memory projections while retaining one generic authoritative log.
- PM5 now models one canonical set of Product/Project/work memberships with
  transactionally enforced referential integrity and derived cross-Product scope.
- TS5 treats Product/Project as logical ambient scope, not database-shard selection.

## 8. Primary sources

- https://sqlite.org/lang_attach.html
- https://sqlite.org/atomiccommit.html
- https://sqlite.org/wal.html
- https://sqlite.org/backup.html
- https://sqlite.org/lang_vacuum.html
- https://sqlite.org/limits.html
- https://sqlite.org/whentouse.html
- Context7 `/sqlite/sqlite`
- https://fossil-scm.org/doc/tip/www/tech_overview.wiki
- https://litestream.io/
- https://docs.datasette.io/en/stable/publish.html
- https://turso.tech/multi-tenancy
- https://developers.cloudflare.com/d1/reference/faq/
- https://openai.github.io/openai-agents-python/ref/memory/sqlite_session/

Exa was used materially to discover and compare the local-first, per-vault, and
per-tenant architectures above. Vendor/community examples corroborate shape and
tradeoffs; SQLite's official documentation remains authoritative for behavior.

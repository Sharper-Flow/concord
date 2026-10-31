---
{
  "id": "PM3",
  "title": "Product memory domain schema",
  "authority": {
    "tier": "derived"
  },
  "home_domain_id": "durable-authority",
  "applies_to_domain_ids": [
    "product-memory",
    "work-coordination"
  ],
  "criterion_bindings": [
    {
      "criterion": 1,
      "exemption": "The corpus scenarios are query-shaped and prove no schema-boundary claim. Criterion 1 states CD-0242 D2. The guarantee is proved by TestEventLogRetirementFreshStore and TestEventLogRetirementUpgradePreservesCurrentRows (internal/store)."
    },
    {
      "criterion": 2,
      "exemption": "The corpus scenarios are query-shaped and prove no schema-boundary claim. The guarantee is proved by TestOpenEnforcesForeignKeysBehaviorally (internal/store/store_test.go)."
    },
    {
      "criterion": 3,
      "exemption": "The corpus scenarios are query-shaped and prove no schema-boundary claim. The guarantee is proved by TestOpenAppliesSchemaManifest (internal/store/schema_test.go) and TestOpenIsIdempotentAcrossProcesses (internal/store/store_test.go)."
    }
  ]
}
---
# Concord Product-Memory Domain Schema (PM3)

> **Status:** **Accepted — binding until superseded.**
> **Accepted by operator:** 2026-08-05.
> **Decision:** PM3; direct accepted authority for Concord's explicit typed Product-memory schema boundary.
> **Binding inputs:** accepted PM1 query contract, accepted PM2 global SQLite authority, CD-0002 invariants I1–I6.
> **Research basis:** Exa-discovered/public source models (Plane, Bugzilla, Beads, Fossil, Linear public model), official SQLite/Context7 docs, and accepted Concord Product invariants. No benchmark or PoC is part of this decision.
> **Architecture and planning:** `domains` are canonical
> Product-internal architecture; business grouping lives in Linear; typed
> architecture relation families are required.
> **Other requirements:** CD-0003 D2/D3 remain binding.

## Context

PM2's global SQLite authority needs a schema boundary. The binding inputs are
the accepted PM1 query contract, PM2 global authority, and CD-0002 invariants
I1 through I6. This record decides the explicit typed core: current state and
finite verified snapshots under
[CD-0242 D2](../decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history),
a small set of explicit typed relational tables, and JSON only for rare
versioned extension metadata. CD-0003's typed-edge integrity, short-lived CLI, and SQLite engine
choices remain binding.
## Contract

The binding contract is sections 1 through 4: the explicit typed core decision,
the stable conceptual object set, the authority and current-state pattern
including the retention-bounded active-context exception, and the extension
rule with its forbidden patterns. Sections 5 through 10 record rejected
alternatives, external validation, deferred scope, falsifiers, supersession
scope, and sources, and carry no obligation.
## 1. Decision

Concord uses an **explicit typed Product-memory core**:

1. current state and finite verified snapshots—no event log, fold, upcaster, or
   replay exists ([CD-0242 D2](../decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)),
2. a small set of explicit typed, relational **current-state tables**,
3. JSON only for rare, per-kind versioned extension metadata—not identity, joins,
   lifecycle, relations, membership, or PM1 filters.

This keeps CD-0003's typed-edge integrity rule and SQLite engine choices while
rejecting CD-0003's generic `entities(type, current_state JSON)` projection for
the stable Product-memory core.

## 2. Stable conceptual core

PM3 decides the **objects and typed-column/extension boundary**, not final DDL or
PM4/PM5 semantics (now settled separately).

| Object | PM3 commitment | PM1 purpose | Still open |
|---|---|---|---|
| `products` | first-class typed projection | Q1/Q2 Product root | none; the Product-row glance retired with its read (CD-0224) |
| `projects` | first-class typed projection; repositories are one Project kind/locator | Q1/Q3/Q6 scope | repo/deploy packaging, Project kind details |
| `domains` | Git-derived first-class Product-owned architecture identity with optional acyclic parent, law ownership, and endpoint-specific architecture relations; SQLite projects that law | Q3 Domain filter; Product → Domain primary navigation | #193 implements Domain/law homes; #197 directly removes component authority from the current read path |
| `work_items` | one canonical typed managed-work projection; extensible `kind`; rare metadata JSON; independent `research` is ordinary work; CD-0213 permits historical `initiative` reads but no new grouping writes | Q2–Q8 | PM4 supplies states/relations; PM5 supplies membership/scope; business grouping belongs to Linear MCP |
| Product↔Project membership | typed relational edge | Q1 ownership/ambiguity | PM5: many-to-many, role-only, optional singular primary |
| Work↔Project membership | typed relational edge; no copied status | Q6 both directions | PM5: role-only, optional singular primary, derived Product scope |
| `relations` | typed edge structure with real foreign key (FK) endpoints | Q4/Q8 | PM4 supplies kinds, inverses, cycle rules, and supersession semantics |
| `labels` / tagging | typed many-to-many categorical extension | Q3 tags | exact label governance |
| `external_refs` | opaque typed locator for PR/commit/upstream/URL | Q8 links | provider-specific validation |
| store state | current rows and finite verified snapshots; no event log, fold, upcaster, or replay exists (CD-0242 D2) | Q2–Q8 current-state reads | none |
| active research context | CD-0215 direct SQLite tables for packs/revisions/findings/sources/consumers; never Git knowledge | work context | delete only by explicit retirement; PM8 still excludes blobs |
| `artifacts` | other small structured/markdown content only | work context | PM8 excludes a v1 blob/evidence store |

### Minimum typed fields

A field is typed/indexed when it is a stable identity, foreign key, invariant, PM1
filter/order key, or frequent join. At minimum this includes IDs, Product/Domain
ownership, work kind, lifecycle projection, priority, terminal time, versions, and
relation/membership endpoints. PM4/PM5 supply semantics; exact columns follow
implementation design.

## 3. Authority and current-state pattern

### 3.1 One current-state store remains authoritative

The store keeps current state and finite verified snapshots: each mutation
writes current rows in one transaction checked by an expected version, no event
log, fold, upcaster, or replay exists, and recovery uses verified snapshot
restore with reconciliation
([CD-0242 D2](../decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)).

### 3.1a Retention-bounded active-context exception

CD-0215 D4 makes direct pack tables the sole authority for research-pack content and consumer links.
Research packs are versioned context, not Product/work history.
Nonterminal owners may author them; terminal owners retain read-only content for authorized exact-revision reuse under CD-0215 and CD-0216.
Publication, archive, terminal execution cleanup, work removal and foreign-key cascades never delete retained research.
Only explicit eligible research_retire deletes a pack aggregate under CD-0216 and CD-0217.
Releasing a consumer pin never deletes its pack.
Product/work state and retention follow CD-0242 D2; no other fact type inherits research-pack retention by analogy.

### 3.1b Worktree occupancy reconciliation

Worktree occupancy recovery reconciles current occupancy rows with Git and
worktree references during verified-snapshot restore. When recovery cannot reconcile an occupancy row, it refuses with a typed
refusal that names the worktree, the session, and the caller's action
([CD-0242 D11](../decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d11-a-fresh-session-resumes-from-current-state), [CD-0242 D12](../decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d12-every-refusal-states-an-action-the-caller-may-take)); no replay of retained events exists
([CD-0242 D2](../decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)).

### 3.2 No typed-log explosion

Typed tables stay shared across work kinds; no per-kind history table, recovery
fold, or event table exists
([CD-0242 D2](../decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)).

## 4. Extension rule

| Need | Pattern |
|---|---|
| Stable identity, invariant, PM1 filter/order/join | typed column + index/FK |
| Rare per-instance/per-kind metadata | JSON payload validated against a closed, versioned per-kind schema |
| JSON field becomes a repeated PM1 filter | promote to typed/generated indexed projection field |
| New work kind | discriminator/registry + typed handler; no new core table |
| Category/tag | labels + join table |
| Endpoint-specific relation family | dedicated typed table/columns with FK endpoints and closed kind rules |
| External system reference | opaque `external_refs`; external object is not Concord authority |

**Forbidden:** EAV core, JSON-everything, unbounded JSON scans, table-per-work-kind,
and polymorphic domain relations that cannot enforce integrity.

## 5. Why not the alternatives

### Generic entity spine — superseded for stable Product memory

The generic `entities(type, current_state JSON)` design keeps the storage layer uniform but
pushes PM1's stable Product/Project/work fields into JSON or a growing set of facets.
Once every hot field is promoted for Q1–Q8, the system has recreated explicit
tables indirectly—with weaker discoverability and integrity.

### Per-kind tables and typed logs — rejected

The rejected extreme creates one table and migration path per work kind,
ossifying the system and multiplying migrations; no per-kind history exists
([CD-0242 D2](../decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)). This is not a rejection of typed
projections: C deliberately uses a shared typed work core. Work kinds are
behavior discriminators over that core, not separate storage domains.

### Mutable rows + audit log — rejected

Concord keeps current rows mutated in one transaction
([CD-0242 D2](../decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history));
a parallel audit log would recreate a second state authority without recovery
value.

## 6. External validation

| Source | Source-backed pattern | Relevance/caveat |
|---|---|---|
| Plane public source ([`apps/api/plane/db/models`](https://github.com/makeplane/plane)) | explicit Workspace/Project/Issue model modules | Open source; supports comparison with a typed core, not Concord DDL or a claim about Plane's event authority |
| Bugzilla schema/source | explicit Product/Component/Bug tables; typed dependency/duplicate tables; generic field/activity extension | Mature issue model; its mutable-current-state authority is weaker than CD-0002 |
| Beads/Dolt source | public issue/dependency/label and ready/blocked-query implementation | Different engine; supporting comparison only, not authority or storage-shape proof |
| Fossil ticket docs | generic ticket-change artifacts are truth; typed ticket tables are reconstructable projections | Closest source-backed analogue for generic log + typed projections |
| Linear public API/webhooks | public Issue, Project, and Label domain objects/events | Public API only; no private storage or schema claims |
| SQLite generated columns/STRICT/FK/JSON docs (Context7 `/sqlite/sqlite`) | JSON can coexist with a typed relational core; generated columns can be indexed and FKs can enforce declared relationships | Primary implementation capability evidence; FKs require per-connection enablement |

The reviewed public examples support a typed core plus explicit links. They are
comparison evidence, not proof that a generic entity/EAV spine cannot work; PM1,
the falsifiers below, and CD invariants remain controlling.

## 7. Scope deliberately deferred

PM3 did **not** decide:

- lifecycle states, relation kinds/inverses/cycles, blocked/ready, reopen, and
  supersession semantics—now settled by accepted PM4;
- membership roles/cardinality/scope—now settled by accepted PM5;
- C16 evidence/stage mapping;
- tool names/counts/transport, plugin/MCP, or CLI operations;
- exact DDL, migration files, or performance indexes (implementation acceptance).

## 8. Falsifiers

Reopen PM3 if research or implementation evidence shows:

1. the stable core changes schema on most new work kinds despite the discriminator/
   metadata rule (generic spine may be simpler),
2. PM1 filters can remain bounded/indexed using a generic entity projection with
   very few generated fields and no facet proliferation,
3. the typed current-state writes cannot remain total across work kinds,
4. a repeated accepted PM1 job requires a per-kind table,
5. Components or owned members prove to be labels/opaque refs rather than
    stateful/shareable identities.

## 9. Narrow CD supersession

- **CD-0003:** the explicit typed table set in §2 preserves typed-edge
  integrity within §4's typed-core/extension boundary. CD-0003's D2 short-lived CLI and D3
  [`modernc.org/sqlite`](https://pkg.go.dev/modernc.org/sqlite) remain binding;
  the store keeps current state without an event log
  ([CD-0242 D2](../decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)).
- **CD-0002 I2/I3:** [CD-0242 D1](../decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d1-each-kind-of-state-has-one-owner)
  and D2 govern; each kind of state has one owner, and typed tables are current
  state written in one transaction.

## 10. Primary sources

- Plane source: [`apps/api/plane/db/models`](https://github.com/makeplane/plane)
- Bugzilla schema/source: https://schema.bugzilla.org/?action=single&version=5.2 and https://github.com/bugzilla/bugzilla
- Beads source: [`internal/storage/dolt`](https://github.com/gastownhall/beads)
- Fossil ticket design: https://fossil-scm.org/doc/trunk/www/tickets.wiki
- Linear public model/API: https://linear.app/docs/api-and-webhooks
- SQLite generated columns: https://sqlite.org/gencol.html
- SQLite STRICT tables: https://sqlite.org/stricttables.html
- SQLite foreign keys: https://sqlite.org/foreignkeys.html
- SQLite JSON: https://sqlite.org/json1.html
- Context7 `/sqlite/sqlite` JSON documentation

Exa and public-source search were used materially; source/public model claims are
caveated, and no private SaaS storage internals are inferred.

## Acceptance criteria

- Given the store's current rows and finite verified snapshots
  When recovery opens the store after an interruption
  Then current rows and derived indexes are reconciled against Git, worktrees,
  and artifacts, with no replay
  ([CD-0242 D2](../decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)).

- Given the typed relational core
  When the store opens
  Then declared relationships hold real foreign-key endpoints enforced
  behaviorally by SQLite, not application-side convention.

- Given a schema migration
  When the store opens on an existing database
  Then the schema manifest applies completely and idempotently across
  processes, with no partial application.

## Verification

The corpus scenarios are query-shaped and prove no schema-boundary claim, so
every criterion carries a typed exemption naming its proof.

- Criterion 1 states CD-0242 D2 as law. It is proved by
  `TestEventLogRetirementFreshStore` and
  `TestEventLogRetirementUpgradePreservesCurrentRows`
  (`internal/store/event_log_retirement_test.go`,
  `internal/store/event_log_retirement_upgrade_test.go`).
- Criterion 2 is proved by `TestOpenEnforcesForeignKeysBehaviorally`
  (`internal/store/store_test.go`).
- Criterion 3 is proved by `TestOpenAppliesSchemaManifest` and
  `TestOpenIsIdempotentAcrossProcesses` (`internal/store/schema_test.go`,
  `internal/store/store_test.go`). Section 8 records the falsifiers for each
  guarantee.

# Concord Product-Memory Domain Schema (PM3)

> **Status:** **Accepted — binding until superseded.**
> **Accepted by operator:** 2026-08-05.
> **Decision:** PM3; direct accepted authority for Concord's explicit typed Product-memory schema boundary.
> **Binding inputs:** accepted PM1 query contract, accepted PM2 global SQLite authority, CD-0002 invariants I1–I6.
> **Research basis:** Exa-discovered/public source models (Plane, Bugzilla, Beads, Fossil, Linear public model), official SQLite/Context7 docs, and accepted Concord Product invariants. No benchmark or PoC is part of this decision.
> **Supersedes narrowly:** CD-0003 D1's generic `entities` materialized spine;
> CD-0003 D2/D3 and the generic authoritative event log remain binding.

## 1. Decision

Concord uses a **hybrid explicit Product-memory core**:

1. one generic append-only **authoritative domain-event log**,
2. a small set of explicit typed, relational **materialized projection tables**,
3. JSON only for rare, per-kind versioned extension metadata—not identity, joins,
   lifecycle, relations, membership, or PM1 filters.

This preserves CD-0002's one-log/rebuild model while rejecting CD-0003's generic
`entities(type, current_state JSON)` projection for the stable Product-memory core.

## 2. Stable conceptual core

PM3 decides the **objects and typed-column/extension boundary**, not final DDL or
PM4/PM5/C15 semantics (now settled separately).

| Object | PM3 commitment | PM1 purpose | Still open |
|---|---|---|---|
| `products` | first-class typed projection | Q1/Q2 Product root | Product-row fields beyond stable identity/stage (C14) |
| `projects` | first-class typed projection; repositories are one Project kind/locator | Q1/Q3/Q6 scope | repo/deploy packaging, Project kind details |
| `components` | first-class Product-owned grouping | Q3/Q9 component filter; primary navigation | component hierarchy depth/ownership details |
| `work_items` | one canonical typed work projection; extensible `kind`; rare metadata JSON; CD-0009 fixes `epic` and `research` as ordinary kinds | Q2–Q8 | PM4 supplies states/relations; PM5 supplies membership/scope |
| Product↔Project membership | typed relational edge | Q1 ownership/ambiguity | PM5: many-to-many, role-only, optional singular primary |
| Work↔Project membership | typed relational edge; no copied status | Q6 both directions | PM5: role-only, optional singular primary, derived Product scope |
| `relations` | typed edge structure with real FK endpoints | Q4/Q8 | PM4 supplies kinds, inverses, cycle rules, and supersession semantics |
| `labels` / tagging | typed many-to-many categorical extension | Q3/Q9 tags | exact label governance |
| `external_refs` | opaque typed locator for PR/commit/upstream/URL | Q8/Q10 links | provider-specific validation |
| Managed-resource identity | first-class typed canonical identity need for infra/SaaS resources because stage/sharing/replacement attach | Q1 ownership and Product context | C15 selects resource-first identity, one owner plus consumers, typed locators/work/replacement edges; exact subtype/index DDL remains implementation design |
| `domain_events` | generic append-only authoritative log (`payload_version`, kind, subject, actor, time, payload) | Q7 and all rebuilds | PM4 supplies lifecycle/relation event semantics; migration/upcasting and event-subject representation remain implementation design |
| active research context | CD-0009 retention-bounded direct SQLite tables for packs/revisions/findings/sources/consumers; never retained events or Git knowledge | active work context | delete only after proof-backed archive; PM8 still excludes blobs |
| `artifacts` | other small structured/markdown content only | work context | PM8 excludes a v1 blob/evidence store |

### Minimum typed fields

A field is typed/indexed when it is a stable identity, foreign key, invariant, PM1
filter/order key, or frequent join. At minimum this includes IDs, Product/Component
ownership, work kind, lifecycle projection, priority, terminal time, versions, and
relation/membership endpoints. PM4/PM5 supply semantics; exact columns follow
implementation design.

## 3. Authority and event-history pattern

### 3.1 One generic log remains authoritative

`domain_events` (CD-0002 `transitions`) is the sole live-state authority. Typed
tables are the deterministic fold output. This is not a mutable-row + audit-log
design.

- One accepted domain operation appends event(s) and updates all affected typed
  projections in **one SQLite transaction** (I4).
- Projection tables have no independent mutation path.
- `rebuild_from_log` drops/rebuilds every live projection (I3/I5).
- Implementation acceptance must demonstrate that a fresh rebuild agrees with the
  live projections; PM3 does not choose the check's cadence or comparison mechanism.
- Event payloads carry `payload_version`; rebuild must handle every accepted event
  version through explicit upcasters when shapes evolve. This concretizes—without
  fully resolving—CD-0002's migration question.

The generic event header is not a domain-relation table. PM3 therefore requires
referential integrity for typed domain edges, but does not choose a universal
identity registry or a polymorphic-FK scheme merely to store an event subject.
The accepted append/fold contract must validate each subject reference; its exact
storage representation remains implementation design.

### 3.1a Retention-bounded active-context exception

CD-0009 explicitly excludes active research-pack content from `domain_events`.
Research packs are WIP context, not Product/work history: one versioned transactional
operation surface owns direct SQLite pack tables while the owner is nonterminal, and
proof-backed archive deletes those rows. Product/work identity, lifecycle,
membership, relations, archive linkage, and every normal projection remain governed
by the append/fold contract above. No other fact type inherits this exception by
analogy.

This shape is validated by Fossil's generic ticket-change artifacts plus
reconstructable typed ticket projections. Bugzilla's mutable `bugs` + activity-log
shape is useful audit evidence but does not satisfy Concord's stronger I3/I5
rebuild requirement.

### 3.2 No typed-log explosion

There is **one generic event table**, not one event table per Product/work kind.
Typed projection tables do not imply typed histories or multiple recovery folds;
one deterministic event fold owns all projections.

## 4. Extension rule

| Need | Pattern |
|---|---|
| Stable identity, invariant, PM1 filter/order/join | typed column + index/FK |
| Rare per-instance/per-kind metadata | JSON payload validated against a closed, versioned per-kind schema |
| JSON field becomes a repeated PM1 filter | promote to typed/generated indexed projection field |
| New work kind | discriminator/registry + typed handler; no new core table |
| Category/tag | labels + join table |
| Domain relation | real typed edge table with FK endpoints; generic event headers are not domain relations |
| External system reference | opaque `external_refs`; external object is not Concord authority |

**Forbidden:** EAV core, JSON-everything, unbounded JSON scans, table-per-work-kind,
and polymorphic domain relations that cannot enforce integrity.

## 5. Why not the alternatives

### Generic entity spine — superseded for stable Product memory

CD-0003's `entities(type, current_state JSON)` keeps the storage layer uniform but
pushes PM1's stable Product/Project/work fields into JSON or a growing set of facets.
Once every hot field is promoted for Q1–Q10, the system has recreated explicit
tables indirectly—with weaker discoverability and integrity.

### Per-kind tables and typed logs — rejected

The rejected extreme creates one table/log/rebuild path per work kind, ossifying
the system and multiplying migrations. This is not a rejection of typed
projections: C deliberately uses a shared typed work core with one generic log.
Work kinds are behavior discriminators over that core, not separate storage domains.

### Mutable rows + audit log — rejected

This is simpler for systems that only need current state + history, but an audit
log cannot necessarily reconstruct corrupted current rows. It would weaken
CD-0002 I2/I3/I5 and recreate a second state authority.

## 6. External validation

| Source | Source-backed pattern | Relevance/caveat |
|---|---|---|
| Plane public source (`apps/api/plane/db/models`) | explicit Workspace/Project/Issue model modules | Open source; supports comparison with a typed core, not Concord DDL or a claim about Plane's event authority |
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
- C15 managed-resource sharing shape—now settled by accepted resource-first registry;
- event-subject storage/FK strategy or a universal identity registry;
- C16 evidence/stage mapping;
- tool names/counts/transport, plugin/MCP, or CLI operations;
- exact DDL, migration files, or performance indexes (implementation acceptance).

## 8. Falsifiers

Reopen PM3 if research or implementation evidence shows:

1. the stable core changes schema on most new work kinds despite the discriminator/
   metadata rule (generic spine may be simpler),
2. PM1 filters can remain bounded/indexed using a generic entity projection with
   very few generated fields and no facet proliferation,
3. the typed projection fold cannot remain deterministic/total across work kinds,
4. a repeated accepted PM1 job requires a per-kind table,
5. Components or owned members prove to be labels/opaque refs rather than
   stateful/shareable/replacement-capable identities.

## 9. Narrow CD supersession

- **CD-0003 D1 is superseded narrowly:** replace generic `entities` materialized
  state with the explicit typed projection set in §2. Keep D1's typed-edge
  integrity rule; generalize its facet principle into §4's typed-core/extension
  boundary. Keep one generic event log, D2 short-lived CLI, and D3
  `modernc.org/sqlite`.
- **CD-0002 I2/I3 do not change.** Clarify operationally that `domain_events` is
  generic authority and typed tables are total deterministic projections.
- PM4/PM5 are accepted; the PM1–PM5 storage design sequence is complete.

## 10. Primary sources

- Plane source: https://github.com/makeplane/plane (`apps/api/plane/db/models`)
- Bugzilla schema/source: https://schema.bugzilla.org/?action=single&version=5.2 and https://github.com/bugzilla/bugzilla
- Beads source: https://github.com/gastownhall/beads (`internal/storage/dolt`)
- Fossil ticket design: https://fossil-scm.org/doc/trunk/www/tickets.wiki
- Linear public model/API: https://linear.app/docs/api-and-webhooks
- SQLite generated columns: https://sqlite.org/gencol.html
- SQLite STRICT tables: https://sqlite.org/stricttables.html
- SQLite foreign keys: https://sqlite.org/foreignkeys.html
- SQLite JSON: https://sqlite.org/json1.html
- Context7 `/sqlite/sqlite` JSON documentation

Exa and public-source search were used materially; source/public model claims are
caveated, and no private SaaS storage internals are inferred.

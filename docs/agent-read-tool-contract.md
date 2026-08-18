# Concord Agent Read-Tool Contract (TS3)

> **Status:** **Accepted — binding until superseded.**
> **Accepted by operator:** 2026-08-06.
> **Decision:** TS3; binding input to CD-0005 §3 and TS5–TS9.
> **Binding inputs:** accepted PM1 Q1–Q10 and query corpus, TS1 jobs/scenarios,
> and TS2 budget/granularity rules.
> **Does not decide:** mutation tools (TS4), ambient context or authorization
> envelope (TS5), transport (TS6), shared success/error envelope (TS7), schema
> evolution (TS8), exact Product launcher fields (C14), or managed-resource shape
> (C15).

## 1. Decision

Concord exposes **four always-visible read tools**:

1. `concord_product_view` — resolve Product context or obtain its bounded work
   snapshot;
2. `concord_work_browse` — inspect current/actionable work sets, readiness,
   blockage, and cross-Project scope;
3. `concord_work_trace` — explain one work item's event history and relation graph;
4. `concord_knowledge` — search durable Product knowledge or resolve one canonical
   note locator.

Each tool accepts a **closed discriminated operation union**. Operations cannot be
invented by string, forwarded to a CLI command, expressed as SQL/JSONPath, or
expanded through arbitrary filter maps. TS7 wraps each operation-specific payload
in the shared authority/freshness/result/error envelope.

This maps PM1's ten queries into four caller-recognizable domains. Q1–Q10 remain
contract identifiers and test cases, not tool identities.

## 2. Tool contracts

### 2.1 `concord_product_view`

**Use when:** an agent must establish where it is, which Product/Projects are in
scope, or what is happening across that Product now.

| Operation | PM1 | Required/optional inputs | Typed payload |
|---|---|---|---|
| `resolve` | Q1 | optional `product_id` or `project_id`; optional cursor/limit when listing Products | `product_context`: Product identity/stage, ordered Project summaries, resolved scope or ambiguity candidates |
| `snapshot` | Q2 | resolved/ambient Product; optional `project_ids`; optional `preview_limit` | `product_snapshot`: lifecycle counts, derived counts, unique bounded preview items, authority/freshness watermark |

`resolve` may use ambient context under TS5, but never guesses when a Project maps
to multiple Products. `snapshot` is an aggregate orientation view—not an unbounded
work listing and not a launcher-row contract. C14 separately chooses human glance
fields.

### 2.2 `concord_work_browse`

**Use when:** an agent needs current work, ready work, blockers, or membership/scope
rather than historical explanation.

| Operation | PM1 | Required/optional inputs | Typed payload |
|---|---|---|---|
| `list` | Q3 | Product scope; closed lifecycle/Project/kind/component/tag/priority/terminal-window filters; optional stable work IDs; `detail`; cursor/limit | `work_page`: ordered unique summaries or bounded full records |
| `ready` | Q5 | Product scope; optional Project/kind; cursor/limit | `work_page` with readiness evidence |
| `blocked` | Q4 | Product scope; optional Project/work/kind; relation depth 1–3; cursor/limit | `blocked_work_page`: work summaries plus unresolved blocker nodes/edges and recovery condition |
| `scope` | Q6 | resolved Product plus exactly one work or Project reference; cursor/limit when Project-scoped | `work_scope`: one canonical work with memberships, or a unique bounded page of applicable work |

`list` with one stable work ID and `detail=full` replaces a separate `show` tool.
`ready` remains distinct from `list` inside the union because readiness is derived
from lifecycle plus unresolved canonical blockers and carries readiness evidence;
it is not a caller-authored boolean filter. `blocked` similarly owns bounded graph
derivation rather than exposing a stored status flag.

### 2.3 `concord_work_trace`

**Use when:** an agent asks why one work item is in its current state or how it is
related to other work—not when it wants a current work list.

| Operation | PM1 | Required/optional inputs | Typed payload |
|---|---|---|---|
| `history` | Q7 | `work_id`; direction; optional event kinds; cursor/limit | `work_event_page`: ordered atomic events with versions, actor, reason, and evidence references |
| `relations` | Q8 | `work_id`; closed relation kinds; direction; depth 1–3 | `work_relation_graph`: bounded canonical nodes/edges, inverses, archived-work links, and replacement state |

History and relation traversal share singular work scope, explanatory intent,
bounded graph/sequence behavior, and provenance-oriented recovery. They remain
discriminated payloads; callers never infer an event from an edge or vice versa.

### 2.4 `concord_knowledge`

**Use when:** an agent needs durable prior decisions, lessons, specs, work notes, or
the one canonical locator for a known terminal work/knowledge item.

| Operation | PM1 | Required/optional inputs | Typed payload |
|---|---|---|---|
| `search` | Q9 | Product scope; optional Project/component; closed knowledge kinds; tags; bounded text; time window; cursor/limit | `knowledge_page`: summaries, canonical locators, commit/content identity, and index watermark |
| `resolve_note` | Q10 | exactly one work or knowledge reference | `canonical_note_result`: one locator or typed not-compacted/missing/ambiguous outcome |

Search never returns an unbounded artifact body. A canonical locator identifies the
git authority; the accepted client may then read that authority through its normal
bounded file/resource path. Index lag and canonical-git reachability remain explicit.

## 3. Shared read-input constraints

- Stable Concord IDs are authoritative references. Paths, repository names, and
  display names may assist resolution but never silently become identity.
- Product scope is ambient after successful resolution; explicit override is for
  ambiguity or intentional cross-Product reads under TS5.
- `detail` is a closed `summary | full` enum. `full` means the complete bounded
  domain record—not raw process exhaust, screenshot bytes, or whole git documents.
- List and Project-scoped `scope` defaults/caps follow PM1: default 20, maximum
  100, opaque cursor, stable ID as final ordering tie-breaker.
- Snapshot preview defaults to 5 and caps at 20 items per bucket.
- Relation depth defaults to 1 and caps at 3; one result caps at 100 nodes and 200
  edges, returning a typed bounded/continuation outcome rather than truncating.
- Cursors bind to operation, resolved scope, filters, ordering, and source version;
  another operation cannot consume them.
- Empty, unknown, ambiguous, degraded, stale, unreachable, and invariant violation
  remain distinct TS7 result/error variants.
- Degraded enumeration is opted in, never assumed (CD-0008 D3). `concord_knowledge.search`
  carries `allow_degraded`; omitted, a knowledge index behind its git head refuses rather
  than answering. An opted-in caller receives the readable items plus degraded authority,
  the omissions, and the watermark the answer actually reflects.
- Reads never mutate, repair, reconcile, or trigger background work.

## 4. Why four tools

| Candidate | Result |
|---|---|
| **Ten Q-tools** | Rejected: mechanically turns PM1 contract cases into tools and recreates list/show/search selection overlap. |
| **One `concord_query` tool** | Rejected: Product orientation, actionable work, provenance graphs, and git-backed knowledge have materially different intent/result/authority shapes. |
| **Three tools with all work reads together** | Rejected: current-set browsing and singular-work provenance have different success oracles, payloads, and likely caller language; one six-operation work dispatcher crosses TS2's split rule. |
| **Five tools splitting knowledge search/resolve** | Rejected: both operations share durable-knowledge authority, canonical locator semantics, and a clear discriminant; an extra tool adds selection cost without a new consequence or recovery boundary. |
| **Four domain tools selected above** | Passes PM1/TS1 while preserving clear descriptions and leaving five tools under TS2's cap for TS4 mutation/operation boundaries. |

The remaining budget is a consequence, not a quota: TS4 may use fewer than five
tools, but cannot collapse required mutation boundaries merely to fit.

## 5. Scenario mapping

| Tool | PM1 scenarios | TS1 scenarios |
|---|---|---|
| `concord_product_view` | Q1–Q2 | AJ1 ambient/ambiguous orientation |
| `concord_work_browse` | Q3–Q6 | AJ1 cross-Project/ready; AJ2 blocker explanation |
| `concord_work_trace` | Q7–Q8 | AJ4 evidence/history; AJ5 relation outcomes |
| `concord_knowledge` | Q9–Q10 | AJ7 search, degradation, and canonical-note retrieval; AJ6 may consume the locator only after its separate mutation/reconciliation flow |

TS3 passes only if one strict schema per tool expresses every operation above,
every mapped PM1/TS1 scenario reaches the correct payload without arbitrary
dispatch, and adversarial prompts reliably distinguish neighboring tools. Concrete
selection-rate comparison follows TS2's declared evaluation method.

## 6. Rejected additions

- Separate list, show, search, status, ready, blocked, history, relation, or resolve
  tools.
- Generic `get_entity`, SQL, JSONPath, GraphQL, repository-path, or CLI passthrough.
- A repair flag on a read.
- Unbounded `include_all`, artifact-body, or recursive graph options.
- Tool-local authority/freshness conventions that diverge from TS7.
- A separate discovery/catalog/status tool in v1.
- C14 launcher presentation fields or provisional C15 resource types smuggled into
  this contract before their owning decisions.

## 7. Falsifiers and amendment rule

Reopen TS3 when:

- accepted PM1 or TS1 scenarios cannot be expressed by the closed operations;
- supported agents repeatedly confuse two read tools despite improved descriptions;
- one tool's operation union requires mostly irrelevant fields or prose-only
  conditional validation;
- two tools are always chained and a merged candidate improves outcomes without
  violating TS2's authority/result/recovery split rules;
- one tool repeatedly returns payloads too large to remain bounded under PM1; or
- a new accepted Product-memory job has a distinct read authority or success oracle.

Any new read operation requires a named accepted scenario and strict schema variant.
An existing table, CLI command, or API endpoint is not sufficient evidence.

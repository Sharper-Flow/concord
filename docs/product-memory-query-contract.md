# Concord Product-Memory Query Contract (PM1)

> **Status:** **Accepted — binding until superseded.**
> **Accepted by operator:** 2026-08-05.
> **Decision:** PM1; direct accepted authority for the Q1–Q10 Product-memory query contract, evaluated by TS1/TS3.
> **Scenario corpus:** [`product-memory-query.v1.json`](../scenarios/product-memory-query.v1.json).
> **Purpose:** Define the smallest storage- and tool-neutral read contract that
> makes Concord useful as Product memory and that judges PM2/PM3 and TS1/TS3.
> **Amended by CD-0041 and CD-0042:** Domain replaces component as the canonical
> architecture/filter identity. Issue #197 changes the pre-go-live primary path
> directly; component inputs are not a supported compatibility surface.

## 1. Decision boundary

PM1 decides **which questions Concord must answer** and what a correct, bounded,
fresh answer means. It does **not** decide:

- global vs per-Product vs per-Project database (PM2),
- explicit domain tables vs generic spine (PM3),
- lifecycle/relation persistence (PM4),
- multi-Project membership storage (PM5),
- agent tool count, names, schemas, plugin, MCP, or CLI mapping (TS1–TS7).

No storage table, CLI command, or candidate tool earns implementation authority
from this contract. All candidates must pass the same corpus.

## 2. Evidence basis and corpus size

The ten jobs below are grounded in the current Product model, ranked priorities,
workflow visibility rules, state-model postmortem, durable-knowledge design, and
PM/TS backlogs. The corpus deliberately merges overlapping Advance-style
list/show/search reads into Product-memory jobs.

Evidence is durable-document evidence, not a claim about current live ADV state:
the session's ADV reads were role-firewalled. The postmortem, inventories, and
accepted Concord directions record real operator failures/jobs; operator acceptance
of PM1 is the final check that no repeated Product-memory job is missing.

| ID | Canonical Product-memory job | Primary grounding |
|---|---|---|
| Q1 | Resolve Product context and its Projects | `product-data-model.md` §4; PM1 |
| Q2 | Orient to a Product's work snapshot | priorities §3–§4; workflows §2.4 |
| Q3 | List Product work by lifecycle/filter | PM1; PM4 |
| Q4 | Explain blocked work | PM4; workflows §2.4 |
| Q5 | Select highest-priority ready work | TS1; priorities §4 |
| Q6 | Inspect cross-Project work | PM5; product-data-model §4 |
| Q7 | Explain why work changed state | workflows §2.2; postmortem C2 |
| Q8 | Inspect dependencies and supersession | PM4; product-data-model §10 |
| Q9 | Search prior work, decisions, lessons, and specs | compaction-design §1; self-documentation |
| Q10 | Resolve a canonical durable note | PM6; compaction-design §5 |

Rejected/deferred reads are listed in §6. The corpus remains small on purpose: a
new canonical query requires a real unmet operator/agent job and a success oracle,
not merely an existing Advance tool or convenient table.

## 3. Universal query contract

Every Q1–Q10 implementation, regardless of storage or tool surface, follows these
rules.

### 3.1 Scope and typed inputs

- Product context is ambient after Q1; explicit Product override is accepted only
  for ambiguity or intentional cross-Product work (TS5).
- Inputs use stable typed references (`product`, `project`, `work`, `knowledge`),
  closed filters, and opaque cursors — never paths, SQL, or free-form dispatch.
- List queries default to `limit=20`, cap at `100`, and return an opaque
  `next_cursor`. Detail is explicit (`summary` or `full`).

### 3.2 Stable result envelope

Every result carries:

- `query_id` and `contract_version`,
- resolved scope and source/version watermark,
- `authority`: `authoritative`, `degraded`, or `unreachable`,
- freshness (`observed_at`, age, stale flag/reason),
- bounded items or a single typed result,
- stable ordering keys, `next_cursor`, and any `omissions`/warnings.

Empty is not unknown. `items=[]` with `authority=authoritative` is a valid answer;
unknown scope, ambiguity, unreadable state, and unavailable authority are typed
errors or explicitly degraded results.

### 3.3 Authority and degradation

- Q1–Q8 are decision-driving live-memory reads: authoritative by default. If the
  authority cannot answer, return `unreachable`; never substitute a stale snapshot.
- Q9–Q10 may use a rebuildable knowledge index, but return the canonical git
  commit/hash and index watermark. Lag or omissions are typed `degraded`, never
  silently complete.
- Partial results are allowed only when the caller permits degradation and the
  response identifies omitted records and why. No natural-language guessing.

### 3.4 Performance and boundedness

- **Implementation acceptance target:** at 10× the measured ADV dataset, every
  bounded metadata query must meet **P99 ≤100 ms** locally.
- Architecture decisions use PM1's jobs/oracles plus authoritative documentation
  and source-backed structural guarantees. The selected implementation must later
  provide `EXPLAIN QUERY PLAN` (or equivalent), P50/P99 measurements, and prove no
  application fan-out, unbounded scan, or unbounded JSON traversal before release.
- Ordering is deterministic with stable ID as the final tie-breaker.

### 3.5 Error semantics

Reads do not mutate or roll back. They fail closed with typed variants:
`unknown_scope`, `ambiguous_scope`, `invalid_filter`, `invalid_cursor`,
`stale_requires_review`, `unreachable`, and `invariant_violation`. Each error says
whether retry is safe and what evidence/action resolves it.

### 3.6 Scenario-corpus execution rules

The JSON corpus is executable through a candidate adapter implementing
`execute(query_id, input, fixture_override) -> result | typed_error`.

- Before evaluating scenario-specific assertions, a runner validates every
  successful result against the universal envelope in §3.2 and every typed error
  against §3.5. The machine-readable required fields are in the corpus's
  `runner_requirements`; scenario assertions add job-specific oracles rather than
  replacing those shared checks.

- Assertion paths use the bounded JSONPath subset `$`, `.field`, `[index]`, and
  `[*]`; no predicates or arbitrary expressions.
- Assertion operations are closed: `eq`, `set_eq`, `contains`, `not_contains`,
  `unique`, `nonempty`, `all_nonempty`, and `single_canonical_record`.
- A scenario may declare `depends_on`; an input value shaped as
  `{ "$from": "scenario-id.$.path" }` resolves from that prior result. This is
  used only for cursor continuation, never as hidden mutable state.
- `fixture_override` deterministically injects authority/index/staleness or
  invariant failures; a candidate must not ignore it.
- PM1 fixture knowledge fields are storage-neutral aliases under accepted PM6:
  `repo` resolves to stable `home_project_id`/`home_locator_id`, `path` resolves to
  `note_path`, and `commit` resolves to `commit_oid`. They are logical placeholders,
  never raw path/URL or abbreviated-hash authority.
- Implementation-acceptance runners record correctness plus plan, P50/P99, rows
  scanned, fan-out calls, and output bytes. Schema validation alone is not a
  passing implementation verification.

## 4. Canonical queries

### Q1. Resolve Product context and Projects

- **Intent:** "Where am I, what Product owns this Project, and which Projects make
  up that Product?"
- **Input:** ambient context or one explicit Product/Project reference.
- **Output:** Product summary, Product stage, ordered Project summaries, and
  ambiguity candidates when ownership is not singular.
- **Order/bounds:** Products by display name then ID; Projects by declared role,
  display name, ID; cursor when listing all Products.
- **Oracle:** forward and reverse ownership agree; unknown and ambiguous ownership
  never become guessed Product context.

### Q2. Product work snapshot

- **Intent:** "What is happening in this Product now?"
- **Input:** Product, optional Project subset, preview limit per bucket.
- **Output:** `lifecycle_counts` (PM4 states), separate `derived_counts` (including
  blocked), and a bounded union of preview items. Each work item appears once with
  view flags (`active`, `blocked`, `ready`, `terminal`) even when it spans Projects.
  Blocked work remains counted in its lifecycle state; the two count families are
  intentionally not mutually exclusive.
- **Order:** explicit priority, relevant lifecycle time, stable ID; terminal
  preview by terminal time descending.
- **Oracle:** lifecycle counts match Q3, derived blocked count matches Q4, preview
  items are unique, and cross-Project work is not double-counted.
- **PM4/PM5 semantics:** lifecycle/derived views follow PM4; canonical membership,
  deduplication, and cross-Product scope follow accepted PM5.
- **PM7 semantics:** snapshots union live and applicable historical terminal
  projections, deduplicate by work ID before counts or pagination, and return
  `degraded`/`unreachable` when required historical-index coverage is incomplete.

### Q3. List Product work

- **Intent:** inspect needed, in-progress, completed, cancelled, or superseded work.
- **Input:** Product; lifecycle states; optional Project, work-kind, Domain,
  tag, priority, terminal-window, detail, cursor, limit.
- **Output:** bounded work summaries with value statement, lifecycle, priority,
  Projects, blocker summary, stage, version, and durable-note reference when terminal.
- **Order:** priority rank, relevant lifecycle timestamp descending, stable ID;
  terminal-only queries use terminal time descending first.
- **Oracle:** filters compose without duplicates; a valid empty filter result is
  authoritative empty, not missing data.
- **PM4 semantics:** lifecycle names and terminal classification are accepted PM4
  persistence semantics; other filters retain their owning decisions.
- **PM7 semantics:** terminal listings union live and git-derived historical rows;
  frozen historical scope is explicit and never silently rewritten by later moves.

### Q4. Explain blocked work

- **Intent:** "What is blocked, by what, and what must change?"
- **Input:** Product with optional Project/work/kind filters; relation depth
  default `1`, maximum `3`.
- **Output:** blocked work plus unresolved blocker nodes/edges, blocker authority,
  age, and external-blocker marker. Blocked is derived, never a competing state.
- **Order/bounds:** priority, oldest blocker age, stable ID; graph node/edge caps.
- **Oracle:** work with resolved dependencies is absent; no stored `blocked` flag
  can disagree with the relation graph.
- **PM4 semantics:** blocked is derived from canonical `blocks` edges and blocker
  terminality; no stored blocked flag exists.

### Q5. Highest-priority ready work

- **Intent:** "What should I do next?"
- **Input:** Product; optional Project/work-kind; limit/cursor.
- **Output:** needed work with no unresolved blockers, including priority, value,
  Projects, and readiness evidence.
- **Order:** explicit priority rank, created time, stable ID. Lifecycle stage is
  displayed for rigor but **does not silently rewrite business priority**.
- **Oracle:** a higher-priority blocked item is excluded; the next unblocked item
  wins deterministically.
- **PM4 semantics:** ready means `needed` with no unresolved canonical `blocks`
  edge. Stage is never a hidden business-priority input.

### Q6. Cross-Project work

- **Intent:** "Which work spans this Project, and which Projects does this work touch?"
- **Input:** ambient or explicitly resolved Product context plus either Project or
  work reference.
- **Output:** one canonical work record with ordered Project memberships and roles;
  never per-Project status copies.
- **Order:** work by priority/update/ID; memberships primary before secondary,
  then Project ID.
- **Oracle:** multi-Project work appears once and both query directions agree.
- **PM5 semantics:** one canonical work item, role-only Project memberships,
  optional singular primary, and derived cross-Product scope are binding. PM1's
  fixture `product` is an expected-scope assertion per PM5, not stored work state.

### Q7. Explain work history

- **Intent:** "Why is this work in its current state?"
- **Input:** work reference; direction; cursor/limit; optional event kinds.
- **Output:** atomic ordered events with sequence/version, from/to lifecycle,
  actor, timestamp, reason, and evidence references.
- **Order:** newest-first by default; event sequence is monotonic and contiguous
  within the returned range.
- **Oracle:** no observer can see a partially terminal event; current lifecycle is
  the fold of authoritative events.
- **PM4 semantics:** lifecycle/relation events follow the accepted PM4 contract.
- **PM7 semantics:** retained `domain_events` remain the authoritative complete
  history after projection pruning; the prune marker only changes live projection
  membership.

### Q8. Dependencies and supersession

- **Intent:** inspect parent, blocks, depends-on, supersedes, and implements links.
- **Input:** work reference; relation kinds; direction; depth default `1`, max `3`.
- **Output:** bounded nodes/edges with canonical inverses and replacement state.
- **Order:** relation kind, source ID, target ID; cycles that violate the accepted
  relation contract are `invariant_violation`, not hidden.
- **Oracle:** forward/reverse queries agree; superseded work resolves its canonical
  successor without duplicate authority.
- **PM4 semantics:** relation kinds, inverse reads, acyclicity, and supersession
  uniqueness follow the accepted PM4 contract.
- **PM7 semantics:** live relation rows remain foreign key (FK) clean. A follow-up to pruned work
  is a separate typed `archived_work_linked` result, not a PM4 live relation.

### Q9. Search durable Product knowledge

- **Intent:** "Have we solved this before; what decisions/specs/lessons govern it?"
- **Input:** Product; optional Project/Domain; knowledge kinds (`work_note`,
  `lesson`, `decision`, `spec`); tags; bounded text; time window; cursor/limit.
- **Output:** summaries with title, kind, date, tags, related work/Domain,
  canonical note/decision/spec reference, commit/hash, and index watermark.
- **Text admission and order:** when bounded `text` is present, admit records
  whose stable ID, title, tag, or Domain equals `text`, or whose title/summary
  contains `text`. Case-insensitive exact structured matches rank before
  case-insensitive title/summary substring-only matches, then date descending
  and stable ID. Product, Project, Domain, kind, tag, and time inputs remain
  conjunctive filters. Without `text`, order is date descending and stable ID.
- **Oracle:** knowledge is found through one bounded domain query, not repeated
  list→show→search choreography; index lag is explicit.
- **Authority note:** an indexed answer is `authoritative` only when its watermark
  proves completeness against every applicable canonical git authority head;
  otherwise it is `degraded`. Accepted PM6 supplies canonical home/locator semantics.

### Q10. Resolve canonical durable note

- **Intent:** obtain the one durable completed-work/lesson note for work or
  knowledge.
- **Input:** completed work or knowledge reference.
- **Output:** exactly one typed canonical locator (repo identity, path, commit SHA,
  content hash) or `not_compacted`, `missing`, or `ambiguous`.
- **Order/bounds:** single result; ambiguity candidates bounded and deterministic.
- **Oracle:** cross-Project work never creates copied notes with competing truth;
  the result is stable under a rebuild of the derived index.
- **PM6 semantics:** locator identity is stable Project/knowledge-locator ID plus
  path, commit OID, and content hash. Outcomes remain locator, `not_compacted`,
  `missing`, or `ambiguous`; authority remains `authoritative|degraded|unreachable`.

## 5. Decision and implementation evaluation

PM2/PM3 architecture decisions use the same accepted jobs/oracles and reject any
shape that **structurally** requires application fan-out, duplicate authority,
unbounded scans, or cannot distinguish empty/degraded/unreachable. Decision
evidence is authoritative documentation, source/architecture comparison, and
accepted Product invariants—not local PoCs or microbenchmarks.

After PM1–PM5 are accepted, the selected implementation runs the JSON corpus at
representative scale and records query plans, P50/P99, rows scanned, fan-out calls,
and output bytes. That evidence validates implementation quality; it does not
retroactively choose the architecture.

TS1/TS3 candidates consume Q1–Q10 as **jobs**, not as mandatory one-tool-per-query
mapping. Tool candidates are scored on scenario success, selection accuracy,
calls/job, schema tokens, retries, and output bounds.

## 6. Deferred/rejected reads

- External live signals (Azure/cron/health/MCP): a separate opaque signal tier,
  not Product-memory authority.
- Cross-Product prioritization: deferred until PM2 defines authority/portability.
- Raw workflow/gate/tool internals: implementation-shaped, not Product memory.
- Evidence-producer resolution and unreadable-record isolation: resolved by CD-0008
  D2/D3. Accepted PM9 adds no compaction-receipt read.
- Free-form SQL, arbitrary JSON path, unbounded full-text search, and "show all":
  structurally rejected.

## 7. Dependencies unlocked and falsifiers

PM1 unlocks PM2/PM3 research comparison, PM4/PM5 query oracles, and TS1/TS3
scenario design. Later decisions may extend Q1–Q10 only with a real unmet job.

PM1 is falsified or must be amended when:

- a repeated real operator job cannot be represented without custom fan-out,
- two queries are always chained and a merged job measurably improves outcomes,
- one query mixes unrelated intents and causes selection/error regressions,
- a canonical query cannot remain bounded under the operating envelope,
- degraded/empty/unreachable cannot be distinguished structurally.

The pre-acceptance evidence gaps included authoritative mapping of real ADV histories
into the lifecycle candidate, ready-work ranking semantics, Domain identity,
cross-Project membership, and canonical note placement. PM2–PM5 and CD-0008 now supply
the accepted storage, membership, evidence-binding, and unreadable-record semantics;
these historical gaps do not invalidate the query jobs.

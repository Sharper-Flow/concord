# CD-0020: Retain the Manifest-Primary Knowledge-Index Shape

**Status:** Accepted under operator approval.
**Amended:** 2026-09-20. D2 names exact lookup, closed filters, and ranked
body discovery as distinct retrieval jobs with a listed falsifier, and
reconciles record-kind vocabulary and the CD-0159 tier interaction.
**Approval date:** 2026-08-13.
**Approval:** Operator-approved review and continuation for GitHub issue #92.
**Type:** Architecture decision (CD-0019 shape review).
**Issue:** [#92](https://github.com/Sharper-Flow/concord/issues/92).
**Refines:** CD-0019 D1 feature 3 and D2; preserves CD-0008, PM1 Q9/Q10,
TS3, CD-0015, and the accepted terminal-launcher contract.

## Context

CD-0019 requires Concord to preserve a knowledge index while researching the
best Concord-specific shape rather than copying Advance. Issue #92 compared
public Advance evidence, external catalog/index approaches, and Concord's
current law and implementation. CD-0009 forbids retaining the research pack;
this decision publishes only the selected durable conclusions.

The original hypothesis was that Concord had a strong Git manifest but lacked a
real query projection. Review disproved that premise. Concord already projects
manifest records and canonical work notes into bounded SQLite metadata; exposes
Product/Project/component, kind, tag, bounded-text, and time filters through
PM1.Q9; resolves canonical historical locators through Q10; carries commit/hash
and canonical-head watermark evidence; and serves agent and launcher reads.

Public Advance source confirms the predecessor exposed separate spec
list/show/search and wisdom read paths and deliberately replaced legacy SQLite
FTS with bounded linear scans after measured evidence made another index
unnecessary. External comparison found that mainstream ADR collections offer
useful human navigation but weaker relation validation than Concord's typed law
graph; Backstage validates the authority/projection split but adds a service and
processing lifecycle not justified here; SCIP/LSP systems own code navigation,
not Product-law authority.

The remaining observed problem is implementation drift, not a missing
architecture: Q9 reports the accepted `structured_match` ordering while current
SQL orders only by date and ID. That defect is tracked independently by
[#93](https://github.com/Sharper-Flow/concord/issues/93).

## Decision

### D1. Retain the current manifest-primary hybrid

The tracked knowledge manifest and its referenced Git blobs remain the durable
authority for decisions, specs, and lessons. Canonical committed work notes retain
their accepted Git authority and locator rules. SQLite remains a derived,
rebuildable query projection and never authors durable knowledge or law.

This current shape satisfies CD-0019's preservation requirement for the knowledge
index. CD-0020 does not authorize a replacement index.

### D2. Retain PM1.Q9 and Q10 as the knowledge-query boundary

Q9 remains the bounded search job for durable Product knowledge. Its accepted
surface includes Product/Project/Domain scope, closed kinds, tags, bounded
text, time window, cursor/limit, canonical locator, commit/content identity, and
index watermark. Q10 remains the single canonical-locator resolution job and
keeps its typed negative: an absent canonical locator resolves to the typed
`knowledge_missing` answer through `resolve_note`, never to a ranked
substitute.

No FTS, semantic/vector retrieval, arbitrary query language, unbounded artifact
body, or generic graph traversal is added. A new operation or query shape requires
a named unmet Product-memory job and the PM1/TS3 amendment path.

Amended 2026-09-20 through that path, with ranked body discovery as the named
unmet job. This section names three distinct retrieval jobs:

- **Exact lookup.** Resolve one record from identity the caller already holds:
  a stable ID or a canonical locator. Q10 owns this job, and its typed negative
  stands.
- **Closed filters.** Select records with conjunctive closed inputs: kinds,
  tags, Product/Project/Domain scope, and a time window, with no text. Q9 owns
  this job.
- **Ranked body discovery.** Admit records whose Git-stored body matches the
  bounded text, ranked after exact structured matches, inside the existing Q9
  envelope, bounds, and watermark semantics. This is the named unmet job: the
  live search admitted only ID, title, tags, and summary, so an accepted law
  record whose title and summary omit the sought words stayed invisible to
  text search. Accepted research observed the gap live
  (`work-d9df5a8a07c69f6e1ef85d70`, observation `obs:c8685c173cf8558a`).

Ranked body discovery stays non-authoritative under Invariant 4. Rank order
and match evidence never become law authority, and a body match never
substitutes for Q10 identity resolution. The D1 split is unchanged: a body is
read from the Git blob the manifest already references, and the SQLite
projection may carry derived admission data but cannot author it. No
replacement index, vector, FTS, or graph authority is added.

The record-kind vocabulary is canonical as of 2026-09-20. The stored kinds are
`work_note`, `constitution`, `decision`, `spec`, `lesson`, `reference`, and
`research`: the set `docs/knowledge/manifest.json` declares, the store
enforces, and the typed knowledge-record draft schema uses. The TS3 agent
surface accepts `note` and `specification` as input aliases for `work_note`
and `spec`, and translates results back. An alias lives at the agent boundary
only and never enters stored data or law text. The draft schema also admits
`external` in its reference-kinds enum. That is a reference kind, not a record
kind.

The CD-0159 authority-tier interaction is reconciled as of 2026-09-20. A
record's `authority.tier` value, `legislated` or `derived`, is a recorded fact
about the write. It is not a retrieval input, a rank factor, or an authority
upgrade. Ranked body discovery over a `derived` record never promotes it. The
CD-0159 tier defaults by kind compose with closed kind filters through the one
canonical vocabulary above.

Falsifier for the named job, under Invariant 6: ranked body discovery fails if
representative-scale evidence cannot hold the PM1 latency bound (P99 at or
under 100 ms locally at 10× the measured dataset). It also fails if any caller
consumes rank or match evidence as law authority. Either outcome reopens this
section, not D1.

### D3. Preserve the existing proof and freshness semantics

Authoritative Q9 evidence continues to mean that the complete projection watermark
matches the commit currently resolved from the canonical knowledge home's declared
head reference. Projected manifest records retain their per-record content hashes
and scanned commit identity. Q10 retains historical commit/hash verification.

`authoritative`, `degraded`, and `unreachable` remain distinct. Concord does not
add a second law-freshness enum, generic `HEAD` assumption, or duplicate entity-
hash mechanism. Law status remains `accepted` or `superseded`; projection lag is
read authority/freshness, not a change in law status.

### D4. Keep durable-law projection separate from knowledge search

`law_subjects` and `law_relations` remain the derived projection for accepted
workflow law checks under CD-0015. Q9 remains a bounded knowledge search; it does
not become an arbitrary multi-hop law-graph API. Consequential law decisions use
the accepted typed enforcement paths, not query ranking or inferred metadata.

### D5. Compose with code intelligence; do not absorb it

CD-0019's concepts-to-docs-to-code intent is satisfied by composition: Concord
owns durable Product knowledge and its canonical locators, while dedicated code
intelligence remains independently owned and Product-scoped under the current
vertical-integration direction. Concord does not duplicate a code-symbol graph or
persist inferred code-to-law edges as authority.

Authored links may be proposed through a later accepted law/schema change. Until
then, code scans and inferred links are advisory and fail-open under CD-0008.

### D6. Repair conformance drift without expanding the architecture

Implementation must match accepted PM1/TS3 law. Repairs such as #93 restore the
declared Q9 ordering and its tests; they do not authorize FTS, a new graph, or a
new read operation. Architecture expansion and conformance repair remain separate.

## Invariants

1. Git remains the durable knowledge and Product-law authority; SQLite projection
   rows cannot author or amend it.
2. The projection is rebuildable from one resolved canonical-home commit and is
   replaced transactionally for that home.
3. Q9/Q10 remain bounded, typed, and explicit about authority, lag, reachability,
   omissions, commit identity, and content identity.
4. Search/ranking metadata never becomes workflow or Product-law authority.
5. Inferred code-to-law edges are advisory and never persist as blocking law.
6. No new query/index mechanism lands without a named accepted job and a falsifier
   listed in this decision.

## Consequences

### Positive

- CD-0019's knowledge-index preservation requirement is satisfied without
  rebuilding a capability Concord already has.
- The strongest existing properties—Git authority, content proof, bounded query,
  canonical locators, and explicit degradation—remain intact.
- External code intelligence stays independently useful and avoids a stale second
  graph inside Concord.
- Conformance defects remain visible as defects rather than being laundered into
  a new architecture.
- Named retrieval jobs give ranked body discovery a lawful route: accepted law
  records become discoverable through text search even when title and summary
  omit the sought words.

### Cost

- The current bounded text and structural filters remain intentionally simpler
  than semantic/vector retrieval or a general graph API.
- Cross-surface code/knowledge journeys depend on Product-scoped composition until
  a measured unmet job justifies deeper integration.
- Body admission widens the text surface Q9 serves; the bounded admission and
  ranking implementation, its evaluation protocol, and its measured results
  are successor work.
- Manifest maintenance and explicit rebuilds remain costs to measure against the
  reopen conditions below.

## Rejected alternatives

- **Add SQLite FTS or embeddings now:** the named body-discovery job is served
  by bounded derived admission, not a new index authority. This conflicts with
  PM1's bounded-query discipline.
- **Add law-relation traversal to Q9:** mixes search with consequential law graph
  semantics and bypasses the PM1/TS3 amendment rule.
- **Build a full knowledge graph:** duplicates code intelligence and creates
  heuristic-authority risk.
- **Adopt Backstage-style continuous processing:** correct authority/projection
  principle, wrong operational weight for one operator and local SQLite.
- **Return to separate spec and wisdom tools:** recreates tool-selection overlap
  that TS3's closed `concord_knowledge` domain avoids.
- **Treat #93 as evidence for a new index:** it is bounded implementation drift,
  not an architecture falsifier.

## Current implementation evidence

- [`../concord-knowledge-index.md`](../concord-knowledge-index.md)
- [`../product-memory-query-contract.md`](../product-memory-query-contract.md) Q9/Q10
- [`../agent-read-tool-contract.md`](../agent-read-tool-contract.md) TS3
- [`../terminal-launcher-contract.md`](../terminal-launcher-contract.md)
- `internal/store/knowledge_index_projection.go`
- `internal/store/knowledge_query.go`
- `internal/agent/runtime.go` (TS3 kind aliases)
- `.concord/schemas/knowledge-record.v1.schema.json` (typed record kinds)
- GitHub issue [#93](https://github.com/Sharper-Flow/concord/issues/93)

## Public comparison evidence

- [Advance `adv_spec` list/show/search](https://github.com/Sharper-Flow/Advance/blob/ad40ee8a45cb7a6268fda9c3feab4c9c1df3ea9c/plugin/src/tools/spec.ts#L73-L190)
- [Advance wisdom tools](https://github.com/Sharper-Flow/Advance/blob/ad40ee8a45cb7a6268fda9c3feab4c9c1df3ea9c/plugin/src/tools/wisdom.ts)
- [Advance bounded linear content search](https://github.com/Sharper-Flow/Advance/blob/ad40ee8a45cb7a6268fda9c3feab4c9c1df3ea9c/plugin/src/storage/content-search.ts)
- [Advance disk-only store rationale](https://github.com/Sharper-Flow/Advance/blob/ad40ee8a45cb7a6268fda9c3feab4c9c1df3ea9c/plugin/src/storage/store-disk.ts#L1-L35)
- [Git data model](https://git-scm.com/docs/gitdatamodel)
- [Architecture Decision Records](https://adr.github.io/)
- [Backstage catalog descriptor format](https://backstage.io/docs/features/software-catalog/descriptor-format/)
- [Backstage entity processing and stitching](https://backstage.io/docs/features/software-catalog/life-of-an-entity/)
- [Sourcegraph precise code navigation / SCIP](https://sourcegraph.com/docs/code-navigation/precise-code-navigation)
- [lsproxy](https://github.com/agentic-labs/lsproxy)

## Reopen conditions

Reopen this decision only when at least one condition is proven:

1. A repeated operator or agent job cannot be expressed by bounded Q9/Q10
   without custom fan-out.
2. Representative-scale evidence shows current bounded text or structural
   filters miss an accepted latency or output floor.
3. Product-scoped external code intelligence cannot support an accepted cross-
   code/knowledge job without Concord-owned indexing.
4. Manifest maintenance or rebuild cost crosses a measured bound.
5. Authority, degradation, and reachability states cannot remain structurally
   distinct.

## Supersession and amendment

CD-0020 does not supersede CD-0008, CD-0009, CD-0015, CD-0019, PM1, TS3, or
terminal-launcher law. It records that their existing composition is the retained
knowledge-index shape. Reopen only under a condition above and the owning PM1/TS3
or vertical-integration amendment path.

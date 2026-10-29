# Product coordination view — accepted contract

**Status:** Accepted C17 contract. The ranked-query contract is owned by the product-memory query contract.
**Storage ([CD-0242](./decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md) D2):** ranking reads current rows;
no fold, event history, or replay exists.

The coordination queries provide two
bounded modes over canonical Product-scoped reads, structural grouping only,
stored-priority ranking, and visible incomplete coverage.

These two bounded work projections are subordinate coordination views. Product detail opens
through Product → Domain. Canonical typed relations remain available through
the bounded Domain-detail read. Q8 work relations remain a subordinate coordination view;
Business grouping lives in Linear under CD-0213. Runtime support for the
Domain/overlap layer is outstanding follow-up work.

## Context

Selecting a Product row must open a coordination view, and before C17 no
accepted document said what it renders. The binding inputs are the standing
visibility and planning obligations in [`priorities.md`](./priorities.md#visibility-and-continuity)
and the canonical PM1 reads. This record accepts the coordination view: two
bounded modes over canonical Product-scoped reads, structural grouping only,
stored-priority ranking, and visible incomplete coverage. CD-0222 amends the
shape: Product detail opens through Product to Domain first, and these two
projections remain subordinate coordination views.

[CD-0219 D3](./decisions/CD-0219-the-terminal-launcher-tui-is-retired.md#d3-obligations-whose-subject-was-the-tui-are-vacated)
vacates launcher-rendering obligations, not the underlying query contracts.
[CD-0224](./decisions/CD-0224-ten-planning-and-browsing-operations-are-retired-from-the-agent-surface.md)
preserves the PM1.Q4 and PM1.Q5 readiness gates.

## Contract

The binding contract is sections 2, 3, and 5: the two accepted modes — the
Q8 relation tree with structural grouping and the Q5/Q4 ranked work table —
the reliance and coverage discipline, and the anti-requirements. Sections 1,
4, and 6 through 10 record the gap, what the contract does not change, the
read-path bounds, acceptance tests, sequencing, risks, and falsifiers, and
carry no obligation.
## 1. The gap

The standing [Planning and coordination law](./priorities.md#planning-and-coordination)
requires that dependencies and
sequences are visible before they become blockers, and that the operator can see what
is ready, what is blocked, and what is next.

## 2. Accepted shape

One **Product coordination view**, reached by selecting a Product row and opening
Product detail, rendering two modes over already-accepted canonical queries from
[`product-memory-query-contract.md`](./product-memory-query-contract.md).

### Mode 1 — Relation tree

Source: Q8, dependencies and supersession.

| Aspect | Rule |
|---|---|
| Relation kinds | `parent`, `blocks`, `depends_on`, `supersedes`, `implements` |
| Depth | Default 1, maximum 3, matching the Q8 bound |
| Grouping | Connected components over the returned edge set, structural only |
| Roots | Node with no in-edge of a traversed kind |
| Cycles | Rendered as `invariant_violation`, never hidden, matching the Q8 oracle |
| Supersession | Resolves to the canonical successor exactly once, no duplicate authority |
| Cross-Project work | Appears once with `project_count` per PM5, never per-Project copies |

Grouping derives from declared edges only. Topical or thematic grouping is excluded:
it carries no authority and is not stable across two renders of unchanged state.

### Mode 2 — Ranked work table

Source: Q5, highest-priority ready work, joined with Q4, explain blocked work.

| Column | Source | Rule |
|---|---|---|
| Work ID and title | Q5 | Title truncates before identity or priority meaning is lost |
| Priority | Q5 explicit priority rank | Stored value, never computed or inferred |
| Lifecycle | Q5 | Displayed for rigor, never silently rewrites business priority |
| Blocked | Q4, derived | Derived from canonical `blocks` edges and blocker terminality |
| Blocker | Q4 blocker nodes | Blocker identity, blocker authority, blocker age, external-blocker marker |
| Project count | PM5 | Breadth signal only, no repository list |

Order follows the Q5 contract exactly: explicit priority rank, then created time, then
stable work ID.

Blocked is two-valued. PM4 derives blocked from canonical `blocks` edges and blocker
terminality and defines no stored blocked flag, so no third state may be introduced.

## 3. Reliance and coverage

The view carries this reliance discipline:

- One shared source and version watermark per render, with per-row reliance where
  sources differ.
- Authority is `authoritative`, `degraded`, or `unreachable`. Degraded and unreachable
  carry a textual or icon marker and never depend on color alone.
- Incomplete required coverage renders `unavailable` with a typed reason and bounded
  omissions. Partial data never renders as complete, and unknown never renders as
  zero.

## 4. What this contract does not change

| Accepted item | Status under this contract |
|---|---|
| Query-contract deferral of cross-Product prioritization pending PM2 authority and portability | Honored. This view is single-Product only. |
| R5 — active work first, history behind drill-down | Honored. Terminal and completed work is excluded. |

## 5. Anti-requirements

Each of these is prohibited because prose-based coordination answers have produced
it, and each produced output that looked authoritative while being non-derivable and
unstable across runs.

1. **No computed importance score.** Ranking uses the stored explicit priority rank. A
   model-assigned numeric importance is heuristic authority over correctness.
2. **No activity-derived priority.** Computed importance and activity-derived
   priority stay prohibited. Ranking reads the stored explicit priority rank over current rows
   ([CD-0242 D2](./decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)).
3. **No thematic clustering.** Connected work subgraphs come from declared relation edges only.
4. **No third blocked state.** No stalled, idle, or otherwise inferred category.
5. **No silent truncation.** A result the query could not fully cover renders
   `unavailable`, never a shorter table presented as whole.
6. **No dashboard drift.** Terminal counts, repository paths, velocity, percent
   complete, estimates, owners, and assignments stay excluded.

## 6. Read path and bounds

- One bounded query per mode, with no per-work fan-out.
- Q8 depth capped at 3 with node and edge caps.
- Q5 paged by limit and cursor.
- Q4 relation depth default 1, maximum 3.
- Both modes derive from the same typed projections and authority watermark as PM1.

## 7. Acceptance tests

The implementation must satisfy at minimum:

- A multi-level parent chain renders as one relation tree with a single root.
- A relation cycle renders `invariant_violation` and is neither hidden nor silently
  broken.
- Superseded work resolves to its canonical successor exactly once.
- Cross-Project work appears once with a correct `project_count`.
- Blocked work shows blocker identity, blocker authority, and blocker age.
- An external blocker carries its marker.
- Incomplete required coverage renders `unavailable` rather than zero rows.
- A higher-priority blocked item is excluded from ready ranking and the next unblocked
  item wins deterministically.
- Two renders over unchanged state produce identical ordering and identical grouping.

Operator test: from the coordination view, name what is blocked, name what blocks it,
and name the next item to start, without opening any work item. Failure would reopen
the column set or the ordering rule; it would not authorize adding inferred fields.

## 8. Sequencing

This contract depends only on already-accepted contracts, so it does not block on new
law. Its implementation depends on the named bounded reads being available through
the production query surface. A practical order is:

1. Q4, Q5, and Q8 read paths exist against real storage.
2. The Product surface this view drills from renders.
3. This view is built as the first drill-down consumer, which also exercises Q4, Q5,
   and Q8 against a real corpus.

## 9. Risks

| Risk | Mitigation |
|---|---|
| Relation edges are under-declared, so the tree looks empty and invites heuristic inference | Empty renders as authoritative-empty with an explicit reason and never falls back to inferred edges |
| Priority ranks are unset in early corpora, making ordering look arbitrary | Missing priority is a typed absent value ordered last, never a substituted guess |
| Drill-down accretes fields until it becomes a dashboard | The anti-requirements form part of acceptance; any new field requires a named operator job plus prototype evidence |
| A depth cap hides a real transitive blocker | Truncated traversal is marked explicitly at the boundary node, never silently cut |

## 10. Falsifiers

This contract must be revised or superseded when:

- the operator cannot name blocked, blocker, and next from the view in one pass;
- structural grouping proves unusable because real work carries too few declared
  edges, and the correct fix is edge declaration rather than inference;
- the two modes prove to be one view rather than two;
- bounded single-Product scope proves insufficient and the cross-Product
  prioritization deferral has since been lifted; or
- the ranked table duplicates a glance focus item without adding a distinct operator
  job.

## Acceptance criteria

- Given a higher-priority blocked item and the next unblocked item
  When mode 2 ranks
  Then the blocked item is excluded from ready ranking and the winner is
  deterministic.

## Verification

- Criterion 1 is proved by the bound `Q5-ready-ranking` scenario of
  `.concord/scenarios/product-memory-query.v1.json`, executed by
  `TestAcceptedQ1ToQ10Corpus` (`internal/store/query_corpus_test.go`).
  Section 10 records the falsifiers for each guarantee.

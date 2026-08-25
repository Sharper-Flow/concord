# Product coordination view — accepted contract

**Status:** Accepted C17 contract, amended by CD-0041.
**Implementation status:** Issue #51 wires the bounded S2 relation tree and ranked
work projection; replacement readiness remains unclaimed.

This document binds the Product coordination view required by Priority 5 and launcher
S2. It preserves the previously reviewed candidate behavior: two bounded modes over
canonical Product-scoped reads, structural grouping only, stored-priority ranking,
and visible incomplete coverage.

Terminal-launcher interaction and prototype detail are explicitly reserved as
implementation design by [`rollout-plan.md`](./rollout-plan.md) and CD-0006 D6, so a
contract at this layer does not require a new decision record.

**CD-0041 amendment.** These two bounded work projections remain valid, but they
are no longer the Product's primary architecture view. Product detail opens
through Product → Domain. Each Domain shows current law, typed Domain relations,
active architecture-bound work, and unresolved overlap before the work relation
tree or ranked table. Q8 work relations remain a subordinate coordination view;
Initiative grouping remains optional business context. Runtime support for the
Domain/overlap layer is outstanding follow-up work and is not claimed by issue
#51's existing implementation.

## Context

Selecting a Product row must open a coordination view, and before C17 no
accepted document said what it renders. The binding inputs are Priority 5's
visibility requirement, the accepted C14 Product row with its exclusions,
and the canonical PM1 reads. This record accepts the coordination view: two
bounded modes over canonical Product-scoped reads, structural grouping only,
stored-priority ranking, and visible incomplete coverage. CD-0041 amends the
shape: Product detail opens through Product to Domain first, and these two
projections remain subordinate coordination views.
## Contract

The binding contract is sections 2, 3, and 5: the two accepted modes — the
Q8 relation tree with structural grouping and the Q5/Q4 ranked work table —
the reliance and coverage discipline, and the anti-requirements. Sections 1,
4, and 6 through 10 record the gap, what the contract does not change, the
read-path bounds, acceptance tests, sequencing, risks, and falsifiers, and
carry no obligation.
## 1. The gap

Priority 5 in [`priorities.md`](./priorities.md) requires that dependencies and
sequences are visible before they become blockers, and that the operator can see what
is ready, what is blocked, and what is next.

Accepted C14 in [`product-row-contract.md`](./product-row-contract.md) deliberately
does not answer this. The row is an orientation and selection projection, not a
Product dashboard. It carries at most one focus item and explicitly excludes the raw
blocker graph. That is correct for a row and insufficient for coordination.

C14 defers the rest in one sentence: selecting a row opens the Product or workflow
detail where the remaining concerns belong. Before C17, no accepted document
specified what that detail renders for coordination. C17 accepts that view here.

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
| Pruned follow-up | Typed `archived_work_linked`, not a live PM4 relation |
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

The view inherits the reliance discipline C14 already established:

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
| R1 in [`clarifications.md`](./clarifications.md) — the launcher is the primary operator surface and the predecessor session bootstrap layer is not a candidate for it | Unchanged. This is a view inside the accepted launcher. |
| C14 default Product row fields and exclusions | Unchanged. No field is added to the row. |
| C14 exclusion of the raw blocker graph from the row | Honored. The graph appears only after selection. |
| C14 finding that activity is not value or priority | Honored. Activity time is not a ranking input and is not a default column. |
| Query-contract deferral of cross-Product prioritization pending PM2 authority and portability | Honored. This view is single-Product only. |
| R5 — active work first, history behind drill-down | Honored. Terminal and completed work is excluded. |
| [`workflows.md`](./workflows.md) launcher responsibility — context-rich navigation with narrow actions | Honored. The view navigates and explains; it takes no substantive workflow action. |

## 5. Anti-requirements

Each of these is prohibited because prose-based coordination answers have produced
it, and each produced output that looked authoritative while being non-derivable and
unstable across runs.

1. **No computed importance score.** Ranking uses the stored explicit priority rank. A
   model-assigned numeric importance is heuristic authority over correctness.
2. **No activity-derived priority.** Last-update recency may appear on explicit
   drill-down with a non-priority label, never as an ordering input.
3. **No thematic clustering.** Connected work subgraphs come from declared relation edges only.
4. **No third blocked state.** No stalled, idle, or otherwise inferred category.
5. **No silent truncation.** A result the query could not fully cover renders
   `unavailable`, never a shorter table presented as whole.
6. **No dashboard drift.** Terminal counts, repository paths, velocity, percent
   complete, estimates, owners, and assignments stay excluded, consistent with C14.

## 6. Read path and bounds

- One bounded query per mode, with no per-work fan-out.
- Q8 depth capped at 3 with node and edge caps.
- Q5 paged by limit and cursor.
- Q4 relation depth default 1, maximum 3.
- Both modes derive from the same typed projections and authority watermark as PM1.

## 7. Acceptance tests

The implementation must satisfy at minimum:

- A multi-level parent chain renders as one component tree with a single root.
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
- A narrow terminal collapses tree indentation without losing meaning and without
  horizontal scroll.
- No-color and screen-reader rendering preserves reliance and blocked meaning.

Operator test: from the coordination view, name what is blocked, name what blocks it,
and name the next item to start, without opening any work item. Failure would reopen
the column set or the ordering rule; it would not authorize adding inferred fields.

## 8. Sequencing

This contract depends only on already-accepted contracts, so it does not block on new
law. Its implementation depends on the named bounded reads being available through
the production query surface. A practical order is:

1. Q4, Q5, and Q8 read paths exist against real storage.
2. The C14 Product row renders.
3. This view is built as the first drill-down consumer, which also exercises Q4, Q5,
   and Q8 against a real corpus.

## 9. Risks

| Risk | Mitigation |
|---|---|
| Relation edges are under-declared, so the tree looks empty and invites heuristic inference | Empty renders as authoritative-empty with an explicit reason and never falls back to inferred edges |
| Priority ranks are unset in early corpora, making ordering look arbitrary | Missing priority is a typed absent value ordered last, never a substituted guess |
| Drill-down accretes fields until it becomes the dashboard C14 rejected | The anti-requirements form part of acceptance; any new field requires a named operator job plus prototype evidence |
| A depth cap hides a real transitive blocker | Truncated traversal is marked explicitly at the boundary node, never silently cut |

## 10. Falsifiers

This contract must be revised or superseded when:

- the operator cannot name blocked, blocker, and next from the view in one pass;
- structural grouping proves unusable because real work carries too few declared
  edges, and the correct fix is edge declaration rather than inference;
- the two modes prove to be one view rather than two;
- bounded single-Product scope proves insufficient and the cross-Product
  prioritization deferral has since been lifted; or
- the ranked table duplicates the C14 focus item without adding a distinct operator
  job.

## Acceptance criteria

- Given a relation cycle in the returned edge set
  When mode 1 renders
  Then it renders `invariant_violation` visibly, never hidden and never
  silently broken.

- Given a supersession chain
  When mode 1 resolves it
  Then the canonical successor resolves exactly once with no duplicate
  authority.

- Given a traversal that would cross the depth cap
  When mode 1 renders
  Then the boundary node is marked explicitly as truncated, never silently
  cut.

- Given structural grouping over declared edges
  When two renders run over unchanged state
  Then ordering and grouping are identical, with no thematic or inferred
  clustering.

- Given a higher-priority blocked item and the next unblocked item
  When mode 2 ranks
  Then the blocked item is excluded from ready ranking and the winner is
  deterministic.

## Verification

No corpus scenario exercises the coordination view, so every criterion
carries a typed exemption in the record naming the port test that proves
the guarantee.

- Criterion 1 is proved by `TestRelationTreeSurfacesCycles`
  (`internal/launcher/storeport/port_test.go`).
- Criterion 2 is proved by `TestRelationTreeResolvesSupersessionChainOnce`
  (`internal/launcher/storeport/port_test.go`).
- Criterion 3 is proved by `TestRelationTreeMarksDepthTruncationUnavailable`
  (`internal/launcher/storeport/port_test.go`).
- Criterion 4 is proved by
  `TestRelationTreeKeepsStructuralComponentAndInverseOutOfCycleOracle`
  (`internal/launcher/storeport/port_test.go`) together with
  `TestProjectionIsDeterministicAndCarriesC14Meaning`
  (`internal/launcher/model_test.go`).
- Criterion 5 is proved by the bound `Q5-ready-ranking` scenario of
  `scenarios/product-memory-query.v1.json`, executed by
  `TestAcceptedQ1ToQ10Corpus` (`internal/store/query_corpus_test.go`).
  Section 10 records the falsifiers for each guarantee.

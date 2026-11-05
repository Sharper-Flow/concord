---
{
  "id": "C17",
  "title": "product coordination view",
  "authority": {
    "tier": "derived"
  },
  "home_domain_id": "operator-surface",
  "applies_to_domain_ids": [],
  "criterion_bindings": [
    {
      "criterion": 1,
      "exemption": "No scenario covers coordination-view ordering. TestOverlapBlocksRemovesWorkFromReady (internal/store/planning_operation_retirement_test.go) proves the list readiness and relation facts, not a rendered view."
    }
  ]
}
---
# Product coordination view — accepted contract

**Status:** Accepted C17 contract. The work-list query contract is owned by the product-memory query contract.
**Storage ([CD-0242](../decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md) D2):** the view reads current rows;
no fold, event history, or replay exists.

The coordination queries provide two
bounded modes over canonical Product-scoped reads, structural grouping only,
stable ordering, and visible incomplete coverage.

These two bounded work projections are subordinate coordination views. Product detail opens
through Product → Domain. Canonical typed relations remain available through
the bounded Domain-detail read. Q8 work relations remain a subordinate coordination view;
Business grouping lives in Linear under CD-0213. Runtime support for the
Domain/overlap layer is outstanding follow-up work.

## Context

Selecting a Product row must open a coordination view, and before C17 no
accepted document said what it renders. The binding inputs are the standing
visibility and planning obligations in [`../constitution/priorities.md`](../constitution/priorities.md#visibility-and-continuity)
and the canonical PM1 reads. This record accepts the coordination view: two
bounded modes over canonical Product-scoped reads, structural grouping only,
stable ordering, and visible incomplete coverage. CD-0222 amends the
shape: Product detail opens through Product to Domain first, and these two
projections remain subordinate coordination views.

[CD-0219 D3](../decisions/CD-0219-the-terminal-launcher-tui-is-retired.md#d3-obligations-whose-subject-was-the-tui-are-vacated)
vacates launcher-rendering obligations, not the underlying query contracts.
[CD-0224](../decisions/CD-0224-ten-planning-and-browsing-operations-are-retired-from-the-agent-surface.md)
preserves the effect of live `blocks` relations on list readiness.

## Contract

The binding contract is sections 2, 3, and 5: the two accepted modes — the
Q8 relation tree with structural grouping and the Q3 work table —
the reliance and coverage discipline, and the anti-requirements. Sections 1,
4, and 6 through 10 record the gap, what the contract does not change, the
read-path bounds, acceptance tests, sequencing, risks, and falsifiers, and
carry no obligation.
## 1. The gap

The standing [Planning and coordination law](../constitution/priorities.md#planning-and-coordination)
requires that dependencies and
sequences are visible before they become blockers, and that the operator can see what
is ready, what is blocked, and what is next.

## 2. Accepted shape

One **Product coordination view**, reached by selecting a Product row and opening
Product detail, rendering two modes over already-accepted canonical queries from
[`product-memory-query-contract.md`](product-memory-query-contract.md).

### Mode 1 — Relation tree

Source: Q8 work relations.

| Aspect | Rule |
|---|---|
| Relation kinds | `parent`, `blocks`, `depends_on`, `raised_from`, `implements` |
| Depth | Default 1, maximum 3, matching the Q8 bound |
| Grouping | Connected components over the returned edge set, structural only |
| Roots | Node with no in-edge of a traversed kind |
| Cycles | Rendered as `invariant_violation`, never hidden, matching the Q8 oracle |
| Cross-Project work | Appears once with `project_count` per PM5, never per-Project copies |

Grouping derives from declared edges only. Topical or thematic grouping is excluded:
it carries no authority and is not stable across two renders of unchanged state.

### Mode 2 — Work table

Source: Q3 work list and Q8 relations for blocker explanation.

| Column | Source | Rule |
|---|---|---|
| Work ID and title | Q3 | Title truncates before identity meaning is lost |
| Lifecycle | Q3 | Displayed without inferring importance |
| Blocked | Q3 readiness and lifecycle | A needed item with `ready` absent or false is blocked |
| Blocker | Q8 relations | Blocker identity, blocker authority, blocker age, external-blocker marker |
| Project count | PM5 | Breadth signal only, no repository list |

Order follows the Q3 contract: relevant lifecycle time, then stable work ID.

List `work_summary.ready` is true exactly when lifecycle is `needed` and no live
blocker holds the item. A needed item with `ready` absent or false is blocked;
the relations read supplies the cause. No public `blocked` flag exists.

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

1. **No computed importance score.** A model-assigned numeric importance is heuristic authority over correctness.
2. **No activity-derived priority.** Computed importance and activity-derived priority stay prohibited.
3. **No thematic clustering.** Connected work subgraphs come from declared relation edges only.
4. **No third blocked state.** No stalled, idle, or otherwise inferred category.
5. **No silent truncation.** A result the query could not fully cover renders
   `unavailable`, never a shorter table presented as whole.
6. **No dashboard drift.** Terminal counts, repository paths, velocity, percent
   complete, estimates, owners, and assignments stay excluded.

## 6. Read path and bounds

- One bounded query per mode, with no per-work fan-out.
- Q8 depth capped at 3 with node and edge caps.
- Q3 paged by limit and cursor.
- Both modes derive from the same typed projections and authority watermark as PM1.

## 7. Acceptance tests

The implementation must satisfy at minimum:

- A multi-level parent chain renders as one relation tree with a single root.
- A relation cycle renders `invariant_violation` and is neither hidden nor silently
  broken.
- Cross-Project work appears once with a correct `project_count`.
- Blocked work shows blocker identity, blocker authority, and blocker age.
- An external blocker carries its marker.
- Incomplete required coverage renders `unavailable` rather than zero rows.
- A blocked item is excluded from ready work and an unblocked item can be selected deterministically.
- Two renders over unchanged state produce identical ordering and identical grouping.

Operator test: from the coordination view, name what is blocked, name what blocks it,
and name the next item to start, without opening any work item. Failure would reopen
the column set or the ordering rule; it would not authorize adding inferred fields.

## 8. Sequencing

This contract depends only on already-accepted contracts, so it does not block on new
law. Its implementation depends on the named bounded reads being available through
the production query surface. A practical order is:

1. Q3 and Q8 read paths exist against real storage.
2. The Product surface this view drills from renders.
3. This view is built as the first drill-down consumer, which also exercises Q3
   and Q8 against a real corpus.

## 9. Risks

| Risk | Mitigation |
|---|---|
| Relation edges are under-declared, so the tree looks empty and invites heuristic inference | Empty renders as authoritative-empty with an explicit reason and never falls back to inferred edges |
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

- Given a blocked item and a ready item
  When mode 2 presents work
  Then the blocked item is excluded from ready work and ordering is deterministic.

## Verification

- Criterion 1 has no rendered-view proof. `TestOverlapBlocksRemovesWorkFromReady`
  (`internal/store/planning_operation_retirement_test.go`) proves the list
  readiness and relation facts, not the ordering of a rendered view. Section 10
  records the falsifiers for each guarantee.

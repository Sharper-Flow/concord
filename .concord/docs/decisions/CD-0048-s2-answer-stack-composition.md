# CD-0048: S2 composes the answers the store already materialized

- **Status:** Accepted (amended 2026-09-26, 2026-09-28, 2026-10-09, 2026-10-10)
- **Date:** 2026-08-20
- **Scope:** the S2 answer-stack clauses of the retired terminal launcher TUI,
  vacated under CD-0219, and the store materialization their Context
  documented; issue #228
- **Approval:** Operator accepted the drafted decision as written on 2026-08-20; the
  public record is
  [issue #228 comment](https://github.com/Sharper-Flow/concord/issues/228#issuecomment-5361273350).
  The operator approved the single-surface amendment on 2026-09-26
  ([Concord (CON) issue 472](https://linear.app/sharper-flow/issue/CON-472/tui-layout-cleanup-survey-driven-per-view-redesign)).
  The operator approved the S2 vacatur with the terminal launcher retirement
  on 2026-10-09 through the Snowball coordinator (operator-delegated) for
  [Concord (CON) issue 908](https://linear.app/sharper-flow/issue/CON-908).
  The Snowball coordinator (operator-delegated) approved this record alignment
  on 2026-10-10 at 07:10 Eastern Daylight Time (EDT).
- **Amended by:** CD-0219 (2026-10-09) vacates the S2 answer-stack clauses
  with the retired launcher TUI; store event, replay, and query contracts
  stand unchanged, and no recency column or fold obligation survives this
  record
- **Related:** CD-0014, CD-0108, CD-0219
- **Preserves:** the store materialization contracts the record documented —
  `internal/store/product_row.go`, `internal/store/domain_reads.go`,
  `cmd/concord/session.go` — and the versioned-write meaning of `updated_at`
- **Supersedes:** nothing

## Context

The interactive terminal launcher retired under CD-0219: `internal/launcher`,
the `concord launcher` verb, and the bare-invocation TUI route are deleted,
and bare `concord` prints usage. The S2 answer-stack clauses of this record
are vacated with the screen they rendered.

The store materialization the record documented keeps its owning contracts.
`internal/store/product_row.go` derives the five-tier Product attention kind.
`internal/store/domain_reads.go` resolves the governing Domain and its
architecture overlap. `cmd/concord/session.go` carries the session input, and
`internal/store/launcher_query.go` keeps the bounded forwarding reads the
session entry route applies. No fold-maintained last-activity stamp survives:
migration 109 drops the `work_items.last_activity_at` column the 2026-09-28
amendment carried, so no release's writes can drift a stored stamp behind the
log (CD-0111).

## Decision

### D1. The S2 answer-stack clauses are vacated

Amended 2026-10-09: CD-0219 retires the interactive terminal launcher TUI,
and the S2 surface is deleted with it. The panel-stack composition, the
quiet-means-healthy render, the stored last-activity ordering, the Tab-row
realignment, and the read-only action surface are vacated with the screen. No
clause of this record obliges a render, a display order, a stored recency
column, or a fold advance.

Store event, replay, and query contracts are unchanged. The store
materialization named in Context keeps its owning contracts and serves the
JSON agent tools and the session entry route. `updated_at` keeps its
versioned-write meaning.

## Consequences

- No schema or fold obligation of this record survives. Migration 109 drops
  `work_items.last_activity_at`, and no recency column or new fold obligation
  replaces it.
- The store materialization continues to serve the JSON agent tools and the
  session entry route. No renderer ships (CD-0014).
- Coverage for the vacated clauses carries an out-of-scope state, not a
  satisfied one.

## Verification

- `python3 scripts/check-doc-links.py`, `python3 scripts/check-public-content.py`,
  and `python3 scripts/check-knowledge-index.py` pass with this record indexed
  exactly once.
- `TestWorkItemsCarryNoFoldMaintainedLastActivity`
  (`internal/store/schema_test.go`) pins migration 109: the
  `work_items.last_activity_at` column stays dropped.
- `TestNonVersionedWorkEventsLeaveUpdatedAtOnTheVersionedWrite`
  (`internal/store/operation_test.go`) proves `updated_at` keeps its
  versioned-write meaning.

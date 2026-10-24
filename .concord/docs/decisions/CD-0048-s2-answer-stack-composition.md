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
- **Related:** CD-0014, CD-0108, CD-0219
- **Preserves:** the store materialization contracts the record documented —
  `internal/store/domain_reads.go`,
  `cmd/concord/session.go` — and the versioned-write meaning of `updated_at`

## Context

The interactive terminal launcher retired under CD-0219: internal/launcher,
the `concord launcher` verb, and the bare-invocation TUI route are deleted,
and bare `concord` prints usage. The S2 answer-stack clauses of this record
are vacated with the screen they rendered.

The store materialization the record documented keeps its owning contracts.
The five-tier Product attention kind retired with the Product-row read under
CD-0224, and no live query derives it.
`internal/store/domain_reads.go` resolves the governing Domain and its
architecture overlap. `cmd/concord/session.go` carries the core-derived
session context: the bounded packet derivation and validation that
work-start preparation applies. No stored last-activity stamp
survives:
migration 109 drops the `work_items.last_activity_at` column the 2026-09-28
amendment carried, so no release's writes can drift a stored stamp
(CD-0111).

## Decision

### D1. The S2 answer-stack clauses are vacated

CD-0219 retires the interactive terminal launcher TUI,
and the S2 surface is deleted with it. The panel-stack composition, the
quiet-means-healthy render, the stored last-activity ordering, the Tab-row
realignment, and the read-only action surface are vacated with the screen. No
clause of this record obliges a render, a display order, a stored recency
column, or a stamp advance.

The store keeps current rows and their query contracts ([CD-0242 D2](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)). The store
materialization named in Context keeps its owning contracts and serves the
JSON agent tools and the managed session context. `updated_at` keeps its
versioned-write meaning.

## Consequences

- No schema or stored-stamp obligation of this record survives. Migration 109
  drops `work_items.last_activity_at`, and no recency column or new stamp
  obligation replaces it.
- The store materialization continues to serve the JSON agent tools and the
  managed session context. No renderer ships (CD-0014).
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

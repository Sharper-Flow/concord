# CD-0224: Ten planning and browsing operations are retired from the agent surface

- **Status:** Accepted
- **Date:** 2026-10-11
- **Scope:** The retired planning and browsing operations, the surviving
  relation writers, the lesson publication route, the unprocessed-knowledge
  negative, the surviving Domain reads, and the closed relation kinds;
  [Concord (CON) issue 927](https://linear.app/sharper-flow/issue/CON-927)
- **Preserves:** The PM1.Q4 and PM1.Q5 readiness gates, the relation
  vocabulary, the `domain_observations` store, and contract supersession
  through `supersede_contract`; relation writes are current rows in one transaction ([CD-0242 D2](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history))
- **Related:** CD-0005, CD-0020, CD-0030, CD-0114, CD-0213,
  [CD-0219](CD-0219-the-terminal-launcher-tui-is-retired.md), and
  [CD-0223](CD-0223-worker-lanes-hold-no-concord-tool-access-and-the-dispatch-packet-carries-only-declared-context.md)
- **Approval:** Operator-delegated law ruling for
  [Concord (CON) issue 927](https://linear.app/sharper-flow/issue/CON-927),
  2026-10-11.

**Approval amendment:** [CD-0243 D7](CD-0243-four-roles-and-six-operator-approval-classes.md#d7-the-remaining-owners-use-the-same-approval-boundary)
qualifies every operator-consent clause in this record, including tables and
acceptance criteria. Other admission, safety, evidence, and historical-pin
requirements remain unchanged.

## Context

The agent surface carried ten planning and browsing operations. Five let an
agent write work relations by hand, browse a Domain's work, record and
dismiss Domain observations, and read a knowledge-closure enumeration. Two
let an agent supersede and restore superseded work. One read a Product-row
projection. One published lessons through a store mutation. One superseded
the whole set's predecessor law: CD-0068 made the Domain-observation routes
law, and C14 made the Product-row read law. C14's own file retires with this
decision, because its subject retires with the read.

CD-0213 moved planning to Linear, so no planning write belongs on the agent
surface. The ten operations had no consumer that the surviving reads could
not serve, and every one of them is a removal surface for the Product
changes CD-0036 D2 protects. CD-0036 D2 also forbids hiding a removal behind
a same-ID rewrite: a retirement that existing records describe as live is a
breaking change, and the break needs its own accepted record.

CD-0068's live subject was the observation record and
dismiss channel, and that channel retires here, so CD-0068 is superseded
history rather than amended law.

## Decision

The retained `resolve_overlap` route uses CD-0013 D11's per-kind consent rule: exact operator approval for merger and supersession, none for dependency, blocking, or compatibility. Every kind still pins both work versions and judged contract content. The retired planning/browsing operations remain retired; changing consent creates no replacement tool or bypass writer.

The agent surface retires ten operations: `concord_domain.active_work`,
`concord_domain.observation_record`, `concord_domain.observation_dismiss`,
`concord_work_relate.link`, `concord_work_relate.unlink`,
`concord_work_relate.supersede`,
`concord_work_relate.restore_superseded`,
`concord_product_view.portfolio`,
`concord_work_compact.lesson_publish`, and
`concord_knowledge.unprocessed`. No operation replaces them, and no
compatibility path remains.

The Product-row glance contract retires with its read. Its file
.concord/docs/product-row-contract.md, its knowledge record, and its
coverage record are deleted, the way CD-0219 D2 deletes the launcher
contracts whose subject retired. No C14 statement governs a live surface:
the launcher render and the `concord_product_view.portfolio` read are both
gone, and nothing replaces the five-group projection. The operator
question the row answered — which fields a glance row carries — returns to
[`priorities.md`](../priorities.md) if a future surface needs it.

Three writers create work-to-work relations, each scoped to its own kinds.

1. `concord_work_relate.resolve_overlap` writes `compatible_with`,
   `depends_on`, `blocks`, `merged_into`, and `supersedes`. A `supersedes`
   or `merged_into` resolution commits the relation and the terminal
   transition in one transaction. A `merged_into` or `supersedes` resolution
   requires an operator approval pinned to both work versions and both
   contract versions.
2. Capture writes `raised_from` through its `raised_from_work_id` input.
   The capture transaction inserts that relation row with the work item's
   current rows and runs no cycle check, because capture knows
   both endpoints.
3. The workflow `link_successor` action writes `forward_link`.

The `blocks` readiness gates are unchanged: a `blocks` row from
`resolve_overlap`, and an existing link-made `blocks` row, hold a work item
out of PM1.Q5 ready and hold it visible in PM1.Q4 while the blocker is not
terminal.

A lesson is authored as the lesson markdown under `.concord/docs/lessons/`,
its manifest record shard, and its law-coverage shard, committed together in
one commit on a branch of the knowledge-home Project (CD-0114 D3). The pull
request and its required checks carry the approval, and the lesson is
published when the pull request merges and a knowledge read verifies the
record. CD-0026 D1 carries the full route.

An unprocessed document stays not law. The authoritative negative is
`concord_knowledge.resolve_note` returning `knowledge_missing` for the
record the document would need, and
[`scripts/check-knowledge-closure.py`](../../../scripts/check-knowledge-closure.py)
enumerates the unprocessed set. Text search is not a substitute for either.

The Domain reads `list`, `detail`, `attachments`, and `overlaps` remain.
Existing `domain_observations` rows stay readable on `detail` under the
bounded open window, an observation satisfies nothing, and promotion out of
an observation stays the CD-0018 `raised_from` capture path that CD-0068 D4
left unchanged.

The store keeps current rows and finite snapshots ([CD-0242 D2](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)).
The relation vocabulary and every stored relation kind stay closed as they
are, and relation changes write current rows in one transaction.

## Alternatives considered

- Compatible in-place amendments only. CD-0036 D2 forbids the form: ten
  removals that consumers must stop using are not a compatible amendment,
  and a same-ID rewrite would hide the break.
- Keeping the reads until a consumer appears. No read had a consumer, and
  each one is removal surface for later Product changes, so the surface tax
  bought nothing.
- Amending C14 in place instead of deleting it. An accepted contract whose
  subject is gone would record what the system used to be, and CD-0219 D2
  already fixes deletion as the route for contracts whose subject retired.
- Superseding CD-0068 without conforming edits. The surviving
  records would keep naming retired routes as live, and the law would be
  false at its own cited clauses.

## Consequences

### Positive

- The agent surface carries no planning write and no browsing read, so the
  CD-0213 authority boundary holds at the tool boundary and not only in
  prose.
- One record states the retirement, its surviving writers, and its
  preserved history, so the superseded records can keep their original
  text as history.
- Every relation writer has one stated approval rule, and every surviving
  relation has one writer.

### Cost

- An agent that noticed a Domain gap records it in Linear, not in Concord,
  so the observation leaves the tool that could read it back on `detail`.
- A lesson needs a pull request, so a lesson author needs a branch, a
  review, and required checks where one approved call stood.
- Link-made `blocks` rows keep their gate until the blocker turns terminal,
  so a stale row needs a `resolve_overlap` resolution or a terminal
  transition to clear.

## Verification

- The implementing change binds the CD-0242 D2 anchor: the store keeps
  current rows and finite snapshots, not an event log, and the surviving
  relation kinds stay closed. Until that change lands,
  `python3 scripts/check-knowledge-index.py` is the executable anchor for
  this record's law registration.
- `internal/store.TestOverlapTerminalResolutionRejectsCycle` proves
  `resolve_overlap` refuses a `supersedes` or `merged_into` resolution that
  would close a cycle.
- `internal/store.TestOverlapTerminalResolutionRejectsSecondSuccessor`
  proves `resolve_overlap` refuses a second successor for an already
  superseded or merged predecessor.
- `internal/store.TestOverlapBlocksRemovesWorkFromReady` proves a
  `resolve_overlap` `blocks` row holds the blocked item out of PM1.Q5 ready.
- `internal/store.TestRelationKindsEnforceTheirOwnGraphRules` proves the
  surviving relation kinds keep their own graph rules.
- `python3 scripts/check-knowledge-closure.py` enumerates the unprocessed
  set, and `python3 scripts/check-knowledge-index.py` proves the record
  shards compose with the amended bodies under their recorded hashes.

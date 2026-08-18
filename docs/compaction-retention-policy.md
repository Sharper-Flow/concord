# Concord Compaction Retention and Historical Index (PM7)

> **Status:** **Accepted — binding until superseded.**
> **Accepted by operator:** 2026-08-06.
> **Decision:** PM7; binding compaction-design/PM6 amendment.
> **Binding inputs:** PM1 Q2/Q3/Q7–Q10, CD-0002 I1–I6, PM4 lifecycle,
> PM5 membership, PM6 canonical note/link proof, and compaction design.
> **Operator choices:** pruned work IDs remain immutable; renewed need creates a new
> linked work item. Pruning uses bounded lazy maintenance, not a fixed calendar sweep.
> **Related accepted boundaries:** PM8 excludes WIP-byte CAS and generic screenshot
> requirements; PM9 rejects a separate process-exhaust receipt. **Does not decide:** PM10
> backup/restore, agent tools, or exact maintenance thresholds.
> **Amended by CD-0041:** historical scope uses `domain_ids` and
> `archived_work_domains`; legacy component scope upcasts during the bounded
> migration.

## 1. Proposed decision

PM6 compaction linkage makes terminal work eligible to leave live typed projections.
PM7 permits a bounded lazy maintenance operation to remove those disposable live
projection rows while retaining the authoritative `domain_events` history in v1.

After projection pruning:

- the durable git note is authority for distilled knowledge;
- `domain_events` remains authority for exact lifecycle/relation history and replay;
- `archived_work` plus normalized historical-scope edges remain disposable,
  git-rebuildable read projections;
- the original work ID is immutable and cannot reopen;
- renewed need creates a new work item linked to the archived one through §7's
  cross-tier `archived_work_linked` event, not a PM4 live relation.

This preserves PM1 Q7 and CD-0002 I5 without inventing a fold checkpoint or treating
the concise git note as a replacement for detailed operational history.

## 2. Eligibility

A work item becomes `projection_prune_eligible` only when all are true:

1. lifecycle is terminal (`completed`, `cancelled`, or `superseded`);
2. PM6 compaction-link event and `archived_work` locator committed;
3. git proof still verifies home IDs, path, commit OID, content hash, and unique work ID;
4. no compaction, membership, or canonical-home mutation for that work is in flight;
5. PM6 front matter contains every field required by §5;
6. superseded work has its canonical successor recorded;
7. no unresolved operator approval or recovery action exists for the compaction.

Calendar age alone never creates eligibility. An uncompacted or proof-degraded item
is never pruned.

## 3. Trigger and bounded execution

There is no daemon and no fixed 30-day sweep. A prune run begins only through:

- explicit operator maintenance intent; or
- a measured storage-pressure policy whose configured threshold was accepted from
  implementation evidence.

Every run must declare a hard item bound and/or elapsed-time bound before it starts.
It selects eligible work deterministically (oldest compaction-link sequence, then
stable work ID), processes one work item per SQLite transaction, and returns a typed
cursor plus counts. A later call resumes; it never holds one unbounded transaction.

A storage-pressure threshold is operational configuration, not architecture. No
default is invented before representative-size evidence exists. Without such a
threshold, pruning remains explicit-operator-only.

## 4. Per-work projection prune transaction

For one eligible work item, one SQLite transaction:

1. revalidates terminal state, PM6 linkage/proof metadata, and eligibility version;
2. appends `work_projection_pruned` to `domain_events`;
3. removes the work item and every live membership/relation row referencing it as
   either source or target from live typed projections;
4. preserves/rebuilds historical projection rows from the verified git note metadata;
5. commits all projection changes atomically.

Crash before commit leaves the prior live projection intact. Crash after commit leaves
the historical projection complete. No half-pruned state is representable. Repeating
the same idempotency key returns the committed result without another event.

The `work_projection_pruned` event is authoritative replay input: a full fold sees the
terminal history, then excludes that ID from live projections. It does not make
`archived_work` authoritative; the historical projection still rebuilds from git.
Any still-live work edge formerly targeting the pruned ID leaves the FK-enforced live
`relations` projection and remains available through authoritative events or the
typed cross-tier link projection in §7.

## 5. Minimum git-rebuildable historical projection

Accepted PM6 fields remain:

```text
id, type, title, completed_at, outcome_tag, lesson_tags,
home_project_id, home_locator_id, note_path, commit_oid, content_hash
```

PM7 explicitly amends PM6's bounded front-matter schema with the minimum data needed
by accepted post-prune queries:

```text
terminal_state            # completed | cancelled | superseded
priority
summary                   # bounded value/outcome summary
product_ids[]             # scope at compaction
project_ids[]             # memberships at compaction
domain_ids[]
tag_ids[]
successor_work_id?        # required when terminal_state=superseded
```

These values live in bounded PM6 front matter and normalize into disposable
`archived_work`, `archived_work_products`, `archived_work_projects`,
`archived_work_domains`, and `archived_work_tags` projections. They describe the
historical scope at compaction; later Product/Project membership moves do not rewrite
the canonical note.

`completed_at` is the inherited index/front-matter name for the terminal timestamp
across all three terminal states; PM1 fixture `terminal_at` maps to it for cancelled
and superseded work as well as completed work. A future schema may rename it to
`terminal_at`, but its meaning is already uniform.

Notes created before the PM7 fields exist are not projection-prune-eligible until a
bounded backfill commits and verifies the amended canonical note.

No historical edge carries mutable lifecycle, blocker, readiness, evidence, or
per-Project status. `successor_work_id` preserves PM1 Q8 canonical resolution; it is
not a second live relation authority.

## 6. Query behavior after projection pruning

### Q2/Q3

- Product/project terminal counts and listings union live terminal projections with
  historical projections, deduplicated by work ID before count/order/pagination.
- Historical filters use compacted Product/Project/Domain/tag associations.
- Lifecycle state comes from `terminal_state`; terminal time from `completed_at`.
- `COUNT(DISTINCT work_id)` remains mandatory across the combined population.
- The combined answer is `authority=authoritative` only when the live event/projection
  watermark and every applicable git-derived historical-index watermark are current.
  If historical coverage is reachable but incomplete/lagging, return
  `authority=degraded` with bounded `omissions`; unreachable git authority composes to
  `authority=unreachable`. A stale historical index never yields authoritative counts.
- Historical Product/Project scope is frozen at compaction; unpruned work follows
  current PM5 membership. A later Project move intentionally does not reclassify
  already compacted work.

### Q7

- Exact ordered history remains available from retained `domain_events`.
- Current lifecycle is still the deterministic fold of those events, including the
  projection-pruned marker.
- PM9 confirms no separate audit receipt is needed; it does not replace authoritative
  history.

### Q8

- Superseded archived work resolves `successor_work_id` from the historical
  projection/front matter; other relation history remains queryable from events.

### Q9/Q10

- Continue through PM6's git-derived index, locator proof, authority watermark, and
  typed outcomes. Projection pruning does not alter canonical note identity.

## 7. Reopen and renewed work

Before projection pruning, PM4 reopen rules remain available. After a committed
`work_projection_pruned` event, the original work ID is immutable and cannot reopen.

If its outcome regresses or related work becomes necessary:

1. create a new canonical work item with a new ID;
2. append a typed `archived_work_linked` event (`source_work_id`,
   `target_archived_work_id`, `kind=follow_up`) and project it into a separate
   `archived_work_links` read model;
3. preserve the old note, event history, and terminal state unchanged.

This narrows PM4 reopen semantics only after PM7's explicit prune boundary. It avoids
restoring deleted projections as if a concise knowledge note were a live checkpoint.
The cross-tier link is not a PM4 live `relations` row and does not pretend an archived
ID is a live FK endpoint. It has no blocking, hierarchy, supersession, or lifecycle
effect; richer semantics require a later accepted relation decision.

### 7.1 Active research cleanup

CD-0009 active research packs are retention-bounded WIP context, not retained
Product-memory events or historical projections. After the normal PM6 note is
committed/verified and compaction linkage commits, archive deletes every pack owned by
that work item plus its revisions, findings, sources, and consumer bindings in one
local SQLite transaction. Failure before durable-note/linkage proof leaves all pack
data intact. The compact note may retain selected reasoning, decisions, specs, or
lessons through their ordinary durable forms; it never serializes or indexes the pack.

## 8. Retained versus removed in v1

| Data | PM7 v1 treatment | Authority |
|---|---|---|
| canonical git note | retain | durable knowledge |
| core `domain_events` (never includes research-pack content) | retain | live/history/replay authority |
| PM6 compaction-link/prune events | retain | linkage/replay authority |
| live typed work row | remove after eligibility | disposable projection |
| live membership/relation rows for pruned ID | remove after eligibility | disposable projection |
| historical index/scope edges | retain/rebuild | disposable git-derived projection |
| WIP logs, traces, screenshots, and binary output | producer-owned process exhaust | no Concord retention |
| active research packs/revisions/findings/sources/bindings | delete after proof-backed owner archive | CD-0009 active-context authority only |
| reports/traces/process exhaust | producer-owned | PM9: no Concord receipt/retention |
| backups/restore copies | PM10 recovery snapshots | accepted recovery policy |

Core event pruning is rejected for v1 because it would break PM1 Q7 and CD-0002 I5
unless a later accepted decision introduces another authoritative replay source.

## 9. Structural invariants

1. **Link before prune:** no verified PM6 linkage means no eligibility.
2. **Events retained:** projection pruning never deletes authoritative domain history.
3. **One atomic item transition:** each work ID is fully live or fully historical in
   projections after a transaction; never half-pruned.
4. **Immutable prune boundary:** a pruned work ID never reopens.
5. **Git-rebuildable history:** every historical projection field derives from one
   verified canonical note/front matter or stable home-walk context.
6. **One query population:** combined live/historical counts deduplicate by work ID
   and compose authority/watermark coverage across both tiers.
7. **No calendar authority:** age alone never causes deletion.
8. **Bounded maintenance:** every run has explicit work/time bounds and a cursor.
9. **Typed uncertainty:** proof degradation blocks pruning rather than guessing.
10. **FK-clean tiers:** live PM4 relations reference live work only; cross-tier
    follow-up links use their own event-derived typed projection.

## 10. Alternatives rejected

### Fixed 30-day sweep

Rejected. The window is unmeasured, adds calendar maintenance, and confuses age with
proof/eligibility. Real lookback evidence may later justify a measured trigger margin.

### Immediate prune during PM6 linkage

Rejected. It collapses approval/linkage and destructive projection cleanup into one
cross-store recovery seam and leaves no bounded operational margin.

### Prune core domain events

Rejected for v1. It breaks exact Q7 history and deterministic from-scratch fold unless
another authoritative checkpoint/receipt model is accepted.

### Restore and reopen the same pruned ID

Rejected by operator choice. It requires reconstructing live authority across stores
and makes the durable note behave like an incomplete event snapshot.

### Keep copied status in historical membership edges

Rejected. It recreates PM5's duplicate-state anti-pattern; terminal state belongs once
on the archived work projection.

## 11. Scope deliberately deferred

PM7 does not decide:

- PM10 backup topology and clean-machine restore;
- exact pressure threshold, item/time batch limits, SQL/index syntax, or command name;
- agent-facing maintenance tools or authorization;
- future core-event pruning (requires a new accepted authority/replay decision).

## 12. Required implementation-acceptance scenarios

In addition to PM1's corpus, acceptance must cover:

1. Q2/Q3 with one live and one pruned terminal item in the same Product, proving one
   population, deterministic order, and no duplicate count;
2. the same query with a stale historical-index watermark, proving
   `authority=degraded` plus bounded omissions;
3. a live item formerly related to a now-pruned item, proving no dangling FK and
   bounded event/cross-tier-link retrieval;
4. Q8 canonical successor resolution for a superseded item pruned with its successor
   already known;
5. a post-prune regression creating a new ID plus `archived_work_linked`, while the
   old ID remains immutable;
6. Product/Project membership moving after compaction, proving historical scope stays
   frozen and explicit.

## 13. Falsifiers

Reopen PM7 if:

1. retained core events—not projections—are the measured SQLite growth problem;
2. a real investigation needs projection-only data absent from git/events;
3. creating linked successor work fails a repeated legitimate same-ID reopen job;
4. combined live/historical Q2/Q3 queries cannot meet PM1's bounded P99 target;
5. historical front matter becomes an unbounded state dump;
6. explicit/lazy maintenance creates unacceptable operator burden;
7. a measured pressure policy requires a time margin or different trigger;
8. PM10 establishes a simpler authoritative checkpoint that preserves Q7/I5.

## 14. Primary sources

- Kurrent/EventStoreDB stream retention: https://docs.kurrent.io/server/v5/streams.html
- Axon snapshots/retention: https://github.com/axoniq/reference-guide/blob/master/axon-framework/tuning/event-snapshots.md
- Kafka event-sourcing retention warning: https://docs.conduktor.io/blog/event-sourcing-with-kafka
- Linear archive/delete behavior: https://linear.app/docs/delete-archive-issues
- Jira issue archiving: https://confluence.atlassian.com/adminjiraserver/archiving-an-issue-968669980.html
- docir git-derived indexes: https://pypi.org/project/docir/

External sources are comparison evidence. PM1–PM6, CD-0002, operator choices, and
the falsifiers above remain controlling.

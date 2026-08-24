# CD-0066: PM7 retention amendment — pruning deferred, authority guarantees kept

- **Status:** Accepted
- **Date:** 2026-08-24
- **Scope:** PM7 (`docs/compaction-retention-policy.md`) pruning mechanism
  disposition; retention guarantees Concord states today; backup retention
  scope; issues #326, #325
- **Approval:** Operator selected the amendment option in the #326 decision
  frame on 2026-08-24 (this session's decision checkpoint; option "B: Amend
  PM7")
- **Related:** PM7, PM8, PM9, PM10 (unchanged), CD-0002, CD-0009, CD-0063
- **Preserves:** the authority split (git note for distilled knowledge,
  `domain_events` for exact history and replay); work-ID immutability; the
  link-before-any-removal ordering; git-rebuildable historical projections;
  the live/historical query union; active-research cleanup on archive;
  PM8's WIP-byte exclusion; PM9's process-exhaust rejection; §8's rejection
  of core `domain_events` pruning
- **Supersedes:** PM7 §2 (prune eligibility), §3 (trigger and bounded
  execution), §4 (per-work prune transaction), §7's `archived_work_linked`
  event and `archived_work_links` projection, and §9 invariant 8's maintenance
  cursor — the pruning mechanism, as described below

## Context

PM7 was accepted 2026-08-06 with two halves. The linkage half — compaction
linkage into `archived_work`, the historical scope index, active-research
cleanup on archive, and the live/historical query union — is implemented and
tested. The pruning half is not: `work_projection_pruned` appears nowhere
outside `docs/`, the eligibility predicate, bounded maintenance run,
storage-pressure threshold, and cross-tier link projection do not exist
(issue #326's verification).

Issue #326 also records the honest scale fact: growth is not urgent at the
current corpus size. The law was neither built nor revised, and nothing was
driving it either way. A decision must pick a direction.

## Decision

### D1. The pruning mechanism is deferred, not built

PM7 §2, §3, §4, the `archived_work_linked` event/projection, and the
maintenance-cursor invariant are superseded. Terminal work remains in live
typed projections indefinitely. No eligibility predicate, maintenance run,
storage-pressure threshold, or prune event is introduced by this decision.

Pruning is deferred because it is a space optimization, not a correctness
need: the authoritative copies (git note, `domain_events`) already exist for
every compacted work item, so unpruned live rows cost storage and query
breadth, not truth. Building the destructive half before first-usable floor
would spend the floor's engineering budget on machinery whose trigger
condition — measurable pressure — has never fired.

### D2. Retention guarantees Concord states today

| Guarantee | Status |
|---|---|
| Git note is authority for distilled knowledge | stands (PM7 §1) |
| `domain_events` is authority for exact history and replay; never pruned | stands (PM7 §8, PM1 Q7, CD-0002 I5) |
| Work IDs are immutable; renewed need creates a new linked work item | stands (PM7 §1) |
| Link before any removal: no verified linkage means no removal eligibility | stands (PM7 §9.1) |
| Historical projections are disposable and git-rebuildable | stands (PM7 §5, §9.5) |
| Combined live/historical queries deduplicate by work ID | stands (PM7 §6, §9.6) |
| Active research packs are deleted after proof-backed owner archive | stands (PM7 §7.1, CD-0009) |
| Live typed projections are bounded by pruning | **deferred** by D1 |
| Storage-pressure-triggered maintenance | **deferred** by D1 |

### D3. Unbounded live projections are accepted, with a revisit trigger

Live typed projections (`work_items`, `relations`, `work_projects`,
`idempotency_records`, `durable_operations`, `worker_attempts`,
`work_messages`, `work_observations`, `external_observations`, and the
`workflow_*` projections) grow without bound, accepted deliberately at
single-operator pre-floor corpus sizes because every row's authoritative
copy already exists elsewhere.

The revisit trigger: a later accepted decision must restore a pruning
mechanism before any of these holds —
1. representative live-projection size measurably degrades authoritative
   operations (query latency, fold time, startup), or
2. Concord moves past single-operator scale (a second concurrent operator,
   or a hosted deployment), or
3. backup snapshot size makes the absence of row reclamation a recovery risk.

Until one fires, "law neither built nor revised" is resolved: the law is
revised, here.

### D4. Backup retention and reclamation are out of scope

`VACUUM`, snapshot aging, and backup retention are PM10 territory and stay
undecided. Snapshots accumulate deliberately: each is a full recovery point,
and at current scale the space cost is accepted while the deletion risk is
not. `concord backup` refusing an existing destination is retained behavior.

### D5. Deliberate absences stay absent

Nothing in this decision reintroduces core `domain_events` pruning (PM7 §8
rejection), a content-addressed WIP-byte evidence store (PM8), or a
process-exhaust receipt store (PM9). Deferring projection pruning does not
weaken any rejected alternative.

### D6. The PM7 coverage record closes as satisfied

With the pruning mechanism superseded, the law PM7 actually states is the
implemented, tested half: linkage, authority split, immutability, historical
projections, query union, and research cleanup. The PM7 coverage record
moves to `satisfied` citing those tests and this decision. The dead-pointer
condition of the other outstanding records is repaired in the same change
under #325, and #324's validator makes the recurrence impossible.

## Invariants

1. No Concord code path deletes a live typed projection row for a compacted
   work item.
2. Every retention guarantee in D2's standing rows has executable evidence.
3. The revisit triggers in D3 are the only conditions under which a pruning
   decision may return.

## Consequences

- Terminal work stays queryable in live projections; historical and live
  tiers coexist for all work, compacted or not.
- `RebuildKnowledgeIndex` keeps its fixture-only caller status; nothing new
  depends on it.
- The `work_projection_pruned` and `archived_work_linked` identifiers remain
  reserved by this record's supersession: reintroducing either requires
  superseding D1.

## Verification

- The PM7 coverage record cites existing compaction/linkage tests that pass.
- `scripts/check-law-coverage.py` validates every outstanding record's issue
  pointer against live issue state (offline snapshot form) once #324 lands.
- No new storage, maintenance, or CLI surface is added by this change.

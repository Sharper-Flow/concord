# Concord Compaction Retention and Historical Index

- **Law ID:** compaction-retention-policy
- **Status:** Accepted
- **Date:** 2026-10-09
- **Approval:** Under operator delegation on 2026-10-09, without calendar expiration.
- **Related:** [CD-0215](decisions/CD-0215-retained-research-context.md),
  [CD-0216](decisions/CD-0216-research-surface-and-explicit-retirement.md),
  [CD-0217](decisions/CD-0217-publication-and-retention-boundaries.md),
  [CD-0142](decisions/CD-0142-operator-directed-execution-removal.md), PM1, PM3–PM6, and PM8–PM10

**Approval amendment:** [CD-0243 D7](decisions/CD-0243-four-roles-and-six-operator-approval-classes.md#d7-the-remaining-owners-use-the-same-approval-boundary)
qualifies CD-0142's operator-directed removal clauses. Removal remains
per-item, scoped, fenced, and subject to the existing planning handoff.

## Context

Canonical note proof, historical scope, and query populations govern deferred terminal projection pruning.
Research retention has a separate explicit boundary.

## Contract

### 1. Authority and deferred projection pruning

The canonical Git note owns distilled knowledge.
The store keeps current rows and finite verified snapshots ([CD-0242 D2](decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)).
`archived_work` and historical scope edges are disposable Git-derived read projections, not another authority for work.
PM6 verifies canonical note identity and records compaction linkage before any future terminal-projection removal can become eligible.
Publication does not itself remove live work projections or research.

CD-0217 defers terminal-projection pruning, its eligibility predicate, pressure threshold, maintenance cursor, and execution mechanism.
Terminal work stays in live typed projections indefinitely.
`work_projection_pruned` and `archived_work_linked` remain reserved names; this record introduces neither mutation nor projection.
Reintroduction requires an accepted decision that supersedes the deferral and preserves the authority guarantees here.
The three revisit triggers are measurable degradation of authoritative operations, deployment beyond one operator, or snapshot-size recovery risk.

CD-0142 separately governs nonterminal execution removal after verified planning handoff and reconciliation.
It is not terminal compaction and removes no retained research.
No work-removal or foreign-key cascade may serve as an alternate route to delete retained research.
Removal must preserve the research authority or refuse before destructive effects.

### 2. Guarantees for any later pruning mechanism

Calendar age alone cannot create removal eligibility.
Uncompacted or proof-degraded work cannot become terminal-prune-eligible.
Before terminal projection removal, verified linkage must identify home IDs, note path, commit OID, content hash, and unique work ID.
Historical front matter must carry the complete scope and terminal state.
Superseded work must have its canonical successor recorded.
An in-flight compaction or canonical-home mutation and unresolved recovery prevent destructive eligibility.

A future run must declare hard item or time bounds and return an explicit continuation.
No unmeasured default pressure threshold or calendar sweep is accepted.
Each work item must move atomically between live and historical projections, with replay returning the committed idempotent result.
Retention removal follows each retained category's documented removal condition and never removes a receipt or verified snapshot a consumer needs ([CD-0242 D2](decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)).
These guarantees constrain a future accepted mechanism; they do not activate the deferred one.

### 3. Minimum Git-rebuildable historical projection

The bounded PM6 front matter preserves:

```text
id, type, title, completed_at, outcome_tag, lesson_tags,
home_project_id, home_locator_id, note_path, commit_oid, content_hash
terminal_state            # completed | cancelled | superseded
priority
summary                   # bounded value/outcome summary
product_ids[]             # scope at compaction
project_ids[]             # memberships at compaction
domain_ids[]
tag_ids[]
successor_work_id?        # required when terminal_state=superseded
```

These fields normalize into `archived_work`, `archived_work_products`, `archived_work_projects`, `archived_work_domains`, and `archived_work_tags`.
They describe scope at compaction; later membership changes do not rewrite the canonical note or frozen historical associations.
`completed_at` means the terminal timestamp for all three terminal states.
PM1's `terminal_at` maps to that field for cancelled and superseded work too.

An older note missing required fields needs bounded verified backfill before any future pruning eligibility.
Historical edges carry no mutable blocker, readiness, evidence, lifecycle, or per-Project status.
`successor_work_id` preserves canonical supersession resolution, not a second relation writer.
CD-0222 and CD-0042 Domain scope and migration remain unchanged.

### 4. One query population across tiers

Q2/Q3 union live terminal work and Git-derived historical work, then deduplicate by work ID before counting, ordering, or pagination.
`COUNT(DISTINCT work_id)` uses that same population.
Historical filters use frozen Product, Project, Domain, and tag associations.
Historical state comes from `terminal_state`; terminal time comes from `completed_at`.
Unpruned work retains current PM5 membership semantics.

An authoritative combined result requires the current store watermark and the applicable Git-index watermark.
Reachable but incomplete or lagging historical coverage returns degraded authority and bounded omissions.
Unreachable Git authority returns unreachable authority.
A stale historical index cannot produce authoritative counts.

Q7 returns current-state records within the store's finite retained categories, not a process-exhaust receipt or the compact note; the store keeps no exact event history ([CD-0242 D2](decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)).
Q8 resolves a superseded historical item's canonical successor from verified front matter and historical projection.
Current relations are stored rows; the store keeps no relation history ([CD-0242 D2](decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)).
Q9/Q10 retain PM6 locator proof, canonical note identity, current Git authority, and typed outcomes.

### 5. Identity and renewed work

Before any accepted committed prune boundary, PM4 reopen rules remain available.
A projection-pruned ID cannot reopen; renewed need uses a new canonical work identity linked to the old one.
The old note and recorded terminal state remain unchanged.
Cross-tier follow-up must not invent mutable lifecycle, hierarchy, supersession, or blocker authority on historical edges.
It must not pretend that a removed projection is a live foreign-key endpoint.
The cross-tier record and projection remain deferred under CD-0217.

### 6. Protected research retirement

CD-0215 research packs remain direct-table context, not retained Product-memory events or historical work projections.
Nonterminal owners may author content; terminal owners retain read-only content available for authorized exact-revision reuse.
Freshness and scope still govern reliance.
Consumer pins protect content while their consuming work is nonterminal, whether required or optional.
Consumer terminalization releases those pins transactionally.

PM6 publication verifies and links the ordinary durable note without deleting research or requiring consumers to stop.
A research `no change` outcome does not change this separation.
A note may retain selected reviewed reasoning through ordinary accepted durable forms.
It never serializes or indexes the research corpus or promises recovery of a retired pack.

Only explicit `research_retire` deletes a pack aggregate.
The operation is authorized, bounded to 1–100 distinct candidates, version-fenced, idempotent, and transactional.
Dry run reports selected identities and versions without writes.
Execution rechecks terminal ownership and zero active pins before deletion.
Compaction linkage is not required for research retirement.
There is no deadline, timer, sweep, or age-derived eligibility.
Reads, publication, unrelated refusals, rebuild, and restart never retire research.

One destructive batch commits atomically; invalid or unauthorized input leaves all candidates unchanged.
Deletion removes packs and dependent revisions, findings, sources, provenance, scopes, and bindings.
Crash before commit retains the prior aggregate; replay after commit returns the committed result without repeating deletion.
Authorized owner reopen before retirement removes eligibility without reconstructing a retired pack.

### 7. Retained and removed data

| Data | Treatment | Authority |
|---|---|---|
| canonical Git note | retain | distilled knowledge |
| PM6 compaction linkage | retain | verified linkage rows |
| live terminal work and membership/relation rows | retain; pruning deferred | current state rows ([CD-0242 D2](decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)) |
| historical index and scope edges | retain or rebuild | verified Git-derived projections |
| research packs and dependent content | retain until explicit eligible retirement | direct tables |
| WIP logs, traces, screenshots, binary output | producer-owned process exhaust | no Concord retention |
| reports and process exhaust | producer-owned | PM9; no separate receipt store |
| backups and restore copies | PM10 recovery policy | recovery snapshots |

Research retirement is not secure erasure of backups.
Snapshot aging, `VACUUM`, and backup topology remain PM10 concerns; this specification adds no reclamation policy.

### 8. Structural invariants and rejected alternatives

- Verified linkage precedes any future terminal work pruning, not separate research retirement.
- Retention cleanup never removes the newest verified snapshot or one an unfinished recovery needs ([CD-0242 D2](decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)).
- A work item cannot be half-live and half-historical after a destructive transaction.
- A committed prune boundary makes the old identity immutable.
- Historical fields derive from one verified note or stable canonical-home context, never a copied live row.
- Combined queries deduplicate one work population and compose its watermarks.
- Calendar age never grants deletion authority.
- Destructive maintenance is bounded, explicit, and idempotent ([CD-0242 D2](decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)).
- Proof degradation prevents work pruning rather than allowing inference.
- Live relations reference live work; historical links cannot create live foreign-key endpoints.
- Research retirement has one owner and every active pin protects content.

A fixed calendar sweep is rejected because no measured window supports it.
Immediate projection pruning during PM6 publication is rejected because it couples durable publication to destructive cleanup.
Restoration of the same pruned work ID is rejected; a note is not a verified recovery snapshot ([CD-0242 D2](decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)).
Copied historical status on membership edges is rejected because terminal state belongs once on the historical work projection.

### 9. Cutover and falsifiers

This policy does not decide a future pressure threshold, work-prune command, backup topology, or alternate authoritative recovery source.

Reconsider this policy if retained receipts and snapshots drive measured growth, real investigations require unavailable projection data, or linked renewed identities fail legitimate work.
Other falsifiers are unmet PM1 query targets, unbounded historical front matter, excessive explicit maintenance burden, or evidence for a different measured trigger.
A simpler PM10 authority must preserve the canonical note, retained receipts, and verified snapshots before it can replace this split.

## Acceptance criteria

- Given a published and verified compaction link
  When Q10 resolves the work item
  Then canonical locator proof and note identity remain unchanged.

- Given historical and live work in one query population
  When Q2/Q3 counts that population
  Then the answer deduplicates by work ID and composes authority across both tiers.

- Given a pack owned by terminal work with no compaction link
  When authorized research_retire rechecks its version and finds zero active pins
  Then the explicit transaction may retire the pack without calendar or publication proof.

- Given a pack with an active required or optional pin
  When publication succeeds or research_retire evaluates the pack
  Then publication preserves the pack and retirement reports protection without deleting it.

- Given the historical projection of a compacted item
  When the projection is read
  Then every field derives from one verified note or stable canonical-home context rather than a copied live row.

- Given a bounded research retirement batch
  When dry run evaluates its candidates
  Then no pack, binding, version, or mutation receipt changes.

- Given a committed research retirement batch
  When the same authorized idempotency identity replays after restart
  Then the operation returns its committed result without repeating deletion or reconstructing content.

## Verification

- Criterion 1 retains the PM1 `Q10-not-compacted` corpus binding and canonical locator witnesses.
- Criterion 2 retains `internal/store.TestRebuildKnowledgeIndexAndQ9Q10UseCurrentGitHead` for watermark evidence; the historical-tier join remains deferred, not proved by an executed prune mechanism.
- Criterion 3 names candidate `internal/store.TestResearchRetirementTerminalOwnersEligible`; integrated retirement eligibility remains unmeasured.
- Criterion 4 names candidate `internal/store.TestResearchRetirementActivePinsProtectAndTerminalReleaseFences`; publication separation and full protection coverage remain unmeasured.
- Criterion 5 retains `internal/store.TestCompactionPayloadRequiresExplicitUniqueScopeArrays` for explicit scope derivation; derivation of every historical projection field remains unmeasured.
- Criterion 6 names candidate `internal/store.TestResearchRetirementDryRunMixedBatchHasNoWrites`; integrated dry-run coverage remains unmeasured.
- Criterion 7 names candidate `internal/agent.TestResearchRetireDryRunAndReplay`; restart and full committed replay coverage remain unmeasured.

`python3 scripts/check-doc-contract.py` validates outline and criterion bindings, not runtime conformance.
Publication, terminal, reopen, removal, migration, restore, and race boundaries still need integrated assessment.
Source anchors do not establish execution or full coverage; the coverage shard remains unmeasured pending the parent's conformance evidence.

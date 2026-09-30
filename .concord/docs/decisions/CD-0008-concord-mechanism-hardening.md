# CD-0008: Concord Authority and Execution Mechanism Hardening

**Status:** Accepted
**Date:** 2026-08-06
**Decision owner:** Operator
**Accepted by operator:** 2026-08-06
**Amended by operator:** 2026-08-06 — SQLite confirmed; alternative-engine
comparison is falsifier-only. CD-0011 records the completed 2026-08-07
post-falsifier review and retains SQLite.
**Scope:** Evidence resolution, unreadable-record isolation, workflow checkpoints,
attempt fencing, external conditions, worktree topology, schema evolution, and the
SQLite confirmation and alternative-engine trigger.
**Amends:** PM4 §5.3 external blockers by adding closed typed condition fields while
retaining the canonical work-item/`blocks` relation model.
**Research:** [`R4-competitive-mechanism-hardening.md`](../research/R4-competitive-mechanism-hardening.md)

## Context

CD-0002, CD-0003, CD-0005, CD-0006, CD-0007, and PM1–PM10 settle Concord's Product,
authority, data model, agent surface, workflow constitution, and repository boundary;
the planned CD-0004 synthesis was never created because the accepted PM records carry
that authority directly. A current-doc audit and
competitor-mechanism pass found no repository-creation blocker and no reason to
replace the accepted architecture. It did find two explicitly blocking authority
questions and several runtime mechanics that must become structural before the
first applicable implementation slice.

This proposal imports mechanisms—not competitor Product shapes—from Beads/Dolt,
LangGraph, Restate/DBOS, Letta, Herdr, Claude Agent SDK, Jido, Qodo, Devin, Orca,
and Superset.

## D1. Shared Product authority and isolated code worktrees

Concord retains one global local SQLite authority outside all repositories and
worktrees. Product/workflow state never branches with Git code.

- One canonical `work_id`; one optional `worktree_set_id` for implementation work.
- Each affected Project contributes at most one active implementation worktree to
  that set.
- Product/Project/work identity derives from stable IDs and registered repository
  locators—not checkout/worktree paths.
- Trunk/default checkouts remain on default branches and are read/deploy sources.
- Worktrees and branches are native Git resources bound to work, never authority.
- Cross-repo work uses sibling worktrees under one worktree set; it does not create
  copied Product/work records in each repo.

Worktree creation is one durable cross-authority operation:

1. atomically claim the work operation and pin intended Project/base SHA/branch;
2. ask the native Git owner to create the worktree;
3. verify branch, base, path, and repository identity; then
4. append the verified locator/state. An interruption reconciles the same operation
   and never starts a second worktree.

Before attempting native creation or retry, reconciliation probes the pinned branch
and path for an already-created matching worktree. Git worktree creation is not
assumed idempotent; Concord's durable operation and probe own retry safety.

Every mutation still requires TS5 scope, expected versions, authorization, and
idempotency. Worktree possession grants no Product authority.

Before execution and merge, refresh remote state and evaluate CD-0006 R3 declared
impact. Dirty worktrees never silently rebase; conflicts never resolve heuristically.
File/touch-set scans may suggest edges or warn, but only accepted hard dependencies
and typed conditions block.

PR/CI/merge are typed external conditions. Dependents unblock on authoritative
merge evidence, not branch push or “task complete.” Reclamation derives from Git
facts: clean tree, merged/reachable head, and no required external operation. A
stale Concord projection is reconciled but cannot override stronger Git truth.

## D2. Immutable-subject evidence binding

Concord v1 uses **immutable-subject binding (ISB)** for verification, review,
approval, commit, native-run, and durable-knowledge evidence.

Concord does not execute native tools, ingest raw output, or become a blob/CAS
authority. At the gated transition it resolves the owning producer and atomically
records a typed binding:

```text
work_id
evidence_kind
immutable_subject_ref/version       # e.g. commit SHA or approved content version
producer_id
producer_run_ref
producer_watermark
observed_at
```

The producer remains authority for its verdict. Concord's binding proves which
immutable subject and producer record authorized the transition; it is not a cached
live verdict. Consumers re-resolve the producer when current truth is required.

Subject binding is closed by evidence kind: verification/native-run binds an exact
source commit plus producer run; review binds the reviewed commit/diff identity;
approval binds the exact approved content/version and consequence; commit binds its
Git object ID; durable knowledge binds the approved note commit/content version.

Rules:

- Missing/unreachable evidence at a gate fails `missing_evidence`/`unreachable`.
- Transition plus binding commit atomically after producer proof.
- Same idempotency key/binding replays without another effect.
- Same immutable subject with conflicting producer truth fails closed for operator
  resolution; Concord never chooses one silently.
- A changed subject requires new evidence and normal supersession/freshness review.
- Producer-run pruning after a completed transition does not erase which immutable
  subject was accepted; if current re-resolution is required and unavailable, the
  result is degraded/stale, never silently authoritative.
- Under PM8 §5 invariant 2, digests may bind external immutable Git/attestation content;
  they cannot enter generic metadata as hidden retention and cannot address,
  retrieve, retain, or imply recovery of PM8-prohibited WIP bytes.

Signed SLSA/in-toto/Sigstore attestations are an additive future strengthening only
after a named tamper/adversary need. They are not a v1 requirement.

## D3. Query-dependent unreadable-record isolation

An unreadable record contributes **unknown** to every predicate over it. Unknown
propagates structurally like SQL three-valued logic.

### Enumeration/positive reads

When each returned item is independently provable, an opted-in degraded query may
return readable items plus:

```text
authority: degraded
unreadable_set[]: {record_ref, reason, detected_at}
omissions[]
```

The unreadable item is omitted but never represented as absent/false.

### Negative/safety conclusions

Queries that assert `none`, `safe`, `ready`, `unblocked`, `no conflicts`, complete,
or release-eligible fail closed when an unreadable record lies within the bounded
dependency/touch closure needed for that conclusion. The result is `undetermined`
with the explicit unreadable set; it never asserts the positive conclusion.

Examples:

- An unreadable possible blocker excludes work from `ready`; it cannot make work
  appear ready.
- An unreadable declared impact prevents “no breaking conflict” and terminal/release
  authorization for the affected closure.
- Unreadable records outside the bounded closure are advisory and cannot alter the
  conclusion.

### Projection rebuild and quarantine

- Projection-row corruption rebuilds from authoritative events.
- An unreadable event stops the fold for every affected subject sub-log; the last
  known-good projection may remain visible only as explicitly degraded/stale.
- Quarantine is append-only: retain the event, append a quarantine/diagnostic marker,
  and maintain a typed projection of unreadable records. Never delete history.
- Distinguish projection corruption from event poison/version skew.
- Repair follows reconcile → repair/upcast/compensate → rebuild → operator escalation.
  Redrive is operator-triggered, recorded, and retry-bounded.

## D4. Workflow checkpoints and execution-attempt fencing

Every work item pins one workflow type/version. Cross-authority and external-effect
steps append typed checkpoint events containing:

```text
work_id
workflow_type_ref/version
step_id
step_kind                    # internal_sqlite | cross_authority | external_effect
attempt_epoch
accepted_inputs_digest
result/evidence_refs/changed_refs
resume_cursor
idempotency_identity
principal_ref/request_id/time
```

A committed step is a checkpoint; an uncommitted step is the resume point. Concord
uses checkpoint-and-resume, not deterministic re-execution of LLM work. Internal
SQLite-only transitions stay one inline domain operation and need no redundant
workflow journal layer.

Expected entity versions guard domain intent. A monotonically increasing attempt
epoch guards ownership of a pending durable step:

```text
claim:
  BEGIN IMMEDIATE
  attempt_epoch := current_epoch + 1
  record claim
  COMMIT

complete:
  BEGIN IMMEDIATE
  reject operation_conflict unless supplied epoch == current epoch
  append step result/checkpoint
  COMMIT
```

Provider-side idempotency is primary for external effects when available; the local
epoch prevents stale attempts from authoritatively completing the Concord step.
TS5 durable idempotency deduplicates retries of one logical intent; the attempt epoch
independently fences which recovery attempt currently owns completion authority.
There are no distributed leases, replica IDs, heartbeat daemon, or hostname-based
ownership heuristics in v1.

## D5. Typed external conditions

The PM4 external-blocker work-item model is retained and amended with a closed typed
condition:

```text
await_type                  # pr_merge | ci_result | timer | human_approval |
                            # remote_work_state
await_ref
resolution_authority
resolution_evidence
resolved_by_event
```

Conditions participate in ordinary canonical `blocks` relations, so PM4 ready/
blocked derivation remains unchanged. Resolution is an attributable lifecycle event
after checking the owning authority.

- Evaluate on explicit operator/agent request and at consequential boundaries.
- No polling, timer daemon, or automatic downstream rewrite.
- Timer means compare trusted current time with a stored deadline when checked.
- Failed/ambiguous/stale conditions remain open and surface a typed problem.
- Heuristics may discover candidate CI runs/PRs but cannot resolve a condition.

## D6. Event/schema evolution and historical reconstruction

- A typed upcaster registry is keyed by event kind and payload schema version.
- Every event records payload schema version; every projection schema has its own
  version.
- Upcasters are ordered, deterministic, side-effect-free, and covered by complete
  replay tests.
- A newer-than-supported event or workflow definition fails closed; no downcast or
  silent skip.
- Projection migration normally rebuilds from the retained event log.
- Active workflow executions remain pinned to their accepted workflow version; idle
  work adopts the current version. Completion always records the pinned version even
  when a newer definition has superseded it, preserving historical reconstruction.
- Read-only point-in-time reconstruction at an event/version is allowed for audit and
  diagnosis. Historical fork creates new linked work; it never rewrites history.
- Projection snapshots are permitted only after measured replay time requires them.
  A snapshot is disposable acceleration, carries event watermark/checksum/version,
  and never replaces retained authority.

## D7. SQLite is confirmed; alternative engines are falsifier-only

Ten active agents do not imply ten simultaneous Product-state commits. Concord's
measured predecessor workload is below 0.1 writes/second system-wide; most agent time
is read/reason/edit/test/wait. SQLite WAL permits concurrent readers and one serialized
writer with snapshot isolation, matching the desired one-current-truth semantics.

Dolt server supports concurrent clients and three-way cell merge, making it strong
for Beads' project-local branch/federation model. It is not the default Concord store:

- server mode reintroduces daemon/port/supervision/backup failure modes;
- branch-per-agent creates several stale Product truths;
- same-branch non-conflicting cells may merge, but same-cell conflicts roll back and
  Dolt documents repeatable-read lost-update susceptibility;
- cell merge cannot authorize cross-row lifecycle/relation/approval/evidence
  invariants; and
- Dolt commit-graph writes still serialize globally.

The researched semantic fit confirms SQLite as Concord's implementation choice. Run
a bounded ten-process **SQLite conformance/load test** to verify the selected
implementation under realistic short transactions; this is implementation evidence,
not an engine-selection bake-off. Correctness is weighted above throughput.

Only if the SQLite run reproduces escaped `SQLITE_BUSY`, P99 above the accepted local
target, unacceptable queueing, lost/duplicate effects, or an invariant violation does
the storage decision reopen. That falsifier triggers comparison of a single-writer IPC
boundary, Dolt server, and Postgres/DBOS; Dolt receives no automatic preference.
CD-0002 §2e remains the complete falsifier table, including deployment-scope triggers
such as network filesystems or multi-machine writes that this local test cannot prove.

## Consequences

### Positive

- All live Product state remains globally visible across ten agents/worktrees.
- Evidence and external conditions have named native authorities without split truth.
- Unreadable records neither create false safety nor block unrelated work globally.
- Interrupted and competing agents resume one durable operation without duplicate or
  stale authoritative completion.
- Schema evolution and diagnostics are explicit before stable event history ships.

### Cost

- Workflow checkpoints, attempt epochs, typed conditions, and unreadable sets add
  schema and test surface.
- Evidence consumers may re-query native producers for current truth.
- Dependency-aware unreadable analysis requires a bounded closure query.
- Worktree creation/reconciliation needs a home in the capped agent-tool surface.

## Required conformance scenarios

1. Ten worktrees share one Product truth; no copied/branched DB appears.
2. Two agents claim one pending step; only the current epoch can complete it.
3. Same-key retries create one worktree/external effect/result.
4. PR/CI conditions do not resolve from branch push, task completion, or heuristic
   discovery; authoritative evidence resolves them.
5. An unreadable possible blocker never yields ready/no-conflict/release-safe.
6. An unrelated unreadable record does not block an independently provable operation.
7. Projection corruption rebuilds; event poison isolates the affected subject and
   preserves history.
8. Older supported events upcast deterministically; newer unsupported events fail
   before projection mutation.
9. Evidence bound to one commit cannot authorize a changed commit.
10. Worktree reclaim succeeds from verified Git merge/cleanliness facts despite a
    stale projection, then reconciles that projection.
11. The ten-process SQLite conformance run records accepted, lost, duplicate,
    rejected, and invariant-violating effects before evaluating latency.

## Sources

- Competitive synthesis: [`R4`](../research/R4-competitive-mechanism-hardening.md)
- PostgreSQL three-valued logic: <https://www.postgresql.org/docs/18/functions-logical.html>
- Elasticsearch partial results: <https://www.elastic.co/docs/api/doc/elasticsearch/operation/operation-search>
- Kafka poison-record handling: <https://cwiki.apache.org/confluence/display/KAFKA/KIP-161%3A+streams+deserialization+exception+handlers>
- SLSA attestation model: <https://slsa.dev/spec/v1.0/attestation-model>
- in-toto statements: <https://github.com/in-toto/attestation>
- SQLite WAL/isolation: <https://www.sqlite.org/wal.html>, <https://www.sqlite.org/isolation.html>
- Dolt concurrency: <https://www.dolthub.com/blog/2026-02-17-dolt-concurrency/>
- Git worktrees: <https://git-scm.com/docs/git-worktree>
- Beads dependencies/federation: <https://beads.gascity.com/core-concepts/dependencies>, <https://beads.gascity.com/multi-agent/federation>
- DBOS architecture: <https://docs.dbos.dev/architecture>

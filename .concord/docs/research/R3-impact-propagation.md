# R3: Cross-Workflow Impact Propagation — Research Findings

> **Status:** Research complete; accepted by CD-0006 R3 on 2026-08-06.
> **Decision slot:** CD-0006 R3.
> **Question:** How should Concord propagate cross-workflow impact when one
> workflow changes a shared spec, dependency, component, or resource?

## Summary

All named systems converge on the same shape: explicit declared edges, reverse-edge
query at need, structured breaking/not-breaking verdict, durable keyed notice store,
and trigger-driven version-stamp freshness. ADV's own backlog-coordination spec is
direct precedent and independently arrived at exactly this design.

## Design

### 1. Structural links (authoritative edges)

Each workflow declares on its own record (no separate registry):

- `modifies` → specs/components/resources it intends to change
- `depends_on` / `consumes` → specs/deps/components/resources it relies on, each
  marked **hard** (blocking) or **soft** (advisory)

Each linked entity carries: `stable_id`, `kind`, `content_hash` or monotonic
`version`.

### 2. Impact notices at completion

At completion (or merge/archive boundary), compute the reverse-edge set: all
workflows whose declared `depends_on`/`consumes` intersects this workflow's
`modifies`. Write one typed notice per `(changer, affected_entity, downstream)`
atomically with completion.

Notice fields: `entity_id`, `old_hash`, `new_hash`, `severity` (breaking |
non-breaking), stable `notice_id` for idempotency.

Keyed/deduped by entity so latest-notice-per-entity wins (Kafka-compacted-topic
analog).

### 3. Downstream acknowledge + classify (only at boundaries)

A workflow reads current notices only at its next consequential boundary
(plan→exec, merge, ship). One bounded query keyed by the workflow's declared
edges. No polling.

Classification: `no_impact | absorbable | needs_replan | blocking`.

### 4. Block vs warn

- **Block** only when: linked entity is **breaking** AND downstream declared a
  **hard** `depends_on`, OR downstream's own spec-conformance check fails.
- **Warn** for: non-breaking changes, soft links, heuristic-detected overlap,
  detected races.
- Heuristics may **suggest** additional `depends_on` candidates but **never
  author a blocking edge.**

### 5. Freshness without polling (boundary fallback)

- Each workflow **stamps** the `content_hash` of each declared entity at plan
  time. At each boundary, re-stamp from current state and compare.
- Mismatch → re-evaluate (optimistic concurrency).
- A **shared monotonic marker** so one workflow's write satisfies all sibling
  readers (oc-fresh precedent).
- If notice delivery is in doubt, the boundary hash-check is the deterministic
  fallback.

## Key findings

1. **Bazel `rdeps`** answers "what will I break?" by querying the declared
   dependency graph. Precision comes entirely from explicit `deps` in BUILD
   files—no inference, no false positives.
   Source: <https://bazel.build/query/guide>

2. **Pact consumer-driven contracts** uses an explicit contract artifact + Pact
   Matrix + `can-i-deploy` gate. Reliability comes from loose, intentional
   contracts. "Something changed" without a breaking/not-breaking split is noise.
   Source: <https://docs.pact.io/pact_broker/can_i_deploy>

3. **Buf `buf breaking`** and **oasdiff** compare current schema against a pinned
   prior version and classify changes as breaking/non-breaking. The split is what
   turns a diff into a gate.
   Sources: <https://buf.build/docs/breaking/overview/>, <https://github.com/Tufin/oasdiff>

4. **Kafka log compaction** retains the latest value per key—a current-state
   snapshot. Downstream consumers rebuild state by reading the compacted topic.
   Source: <https://docs.confluent.io/kafka/design/log_compaction.html>

5. **Fowler Optimistic Offline Lock**: validate version hasn't changed since read;
   conflict → abort/retry. Source:
   <https://martinfowler.com/eaaCatalog/optimisticOfflineLock.html>

6. **Advance issue-backed predecessor lessons** provide public precedent for
   durable-work claims, conflict handling, bounded visibility, and freshness
   semantics. See [`../advance-predecessor-lessons.md`](../advance-predecessor-lessons.md).

7. **GitHub required status checks**: block merge on protected branches. The
   block is an explicit, declared, opt-in configuration.
   Source: <https://docs.github.com/en/pull-requests/collaborating-with-pull-requests/collaborating-on-repositories-with-code-quality-features/about-status-checks>

## What to avoid

- **TTL/timer freshness** — ADV explicitly retired snapshot-TTL; oc-fresh uses TTL
  only as a fetch suppressor, never a timer.
- **Polling loops** — produce no new information; constraints forbid.
- **Heuristic-only blocking** — every named system reserves blocks for explicit
  edges.
- **Automatic downstream rewrites** — constraints forbid; outbox publishes,
  consumers decide and ack.
- **"Something changed" notices without breaking/not-breaking split** — pure noise.

## Confidence

**High.** All six research questions resolved with ≥1 authoritative primary
source; local ADV precedent corroborates independently.

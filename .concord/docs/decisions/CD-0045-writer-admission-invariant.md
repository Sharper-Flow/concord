# CD-0045: Concurrency law states bounded writer admission, not zero waiting

- **Status:** Accepted
- **Date:** 2026-08-20
- **Scope:** The concurrency invariant borne by Priority 2, usability-floor
  condition 5, and core invariant 8; the vocabulary the floor manifest and the
  conformance harness share; issue #191
- **Approval:** Operator accepted the drafted decision as written on 2026-08-20; the
  public record is
  [issue #191 comment](https://github.com/Sharper-Flow/concord/issues/191#issuecomment-5359834142)
- **Related:** CD-0002 §2b, CD-0003, CD-0011,
  [`design-constraints.md`](../design-constraints.md) §1 and §4,
  [`core-architecture.md`](../core-architecture.md) §2 invariant 8,
  [`floor-readiness.md`](../floor-readiness.md)
- **Preserves:** SQLite as sole durable authority; CD-0011's retention decision,
  accepted load profile, falsifier verdicts, and reopen conditions; Priority 2's
  substance as the data-safety constraint in
  [`goals.md`](../goals.md#4-constraints-that-must-hold) §4 and
  [`priorities.md`](../priorities.md#data-safety-constraint)

## Context

SQLite permits one
writer at a time. CD-0002 §2b and CD-0003 select library-in-process access with
`WAL` and `busy_timeout=5000`; writers take `BEGIN IMMEDIATE` and queue. CD-0003
cites sqlite.org endorsing exactly this multi-process pattern — *"writers queue
up… no lock lasts more than a few dozen milliseconds"*. Queueing is the
mechanism, not an implementation slip.

CD-0011 then measured it and recorded the result honestly: correctness was clean
on every population, commit latency stayed low, and **variable writer-admission
wait owned the observed tail**. The harness reports what the decision describes.
`internal/store/conformance_test.go` emits `begin_wait_latency`,
`commit_latency`, `busy_escaped`, and a `falsifier_status` gated on population
identity. Nothing in the executable evidence claims zero waiting, because
nothing measured it.

Floor item `fc5-concurrent-process-conformance` needs a falsifiable bound on
writer-admission wait, distinct from commit duration and escaped busy failures.

## Decision

### D1. The invariant is bounded writer admission, not absent writer admission

Concord's concurrency law states a measurable boundary over three distinct
quantities:

| Quantity | Meaning | Required bound |
|---|---|---|
| **Writer-admission wait** | Time from a writer requesting `BEGIN IMMEDIATE` to holding the write lock. Nonzero by construction whenever a second writer is active. | Bounded: sustained P99 at or below the accepted target on the isolated acceptance population. |
| **Commit duration** | Time the write lock is actually held. | Bounded, and reported separately so a lock-hold regression is distinguishable from host contention. |
| **Escaped busy failure** | `SQLITE_BUSY` surfacing to a caller after `busy_timeout` expires. | Zero. Not bounded — zero. |

Reads remain lock-free.

The three quantities are reported separately and never summed, because they fail
for different reasons and imply different remedies. A rising admission wait on a
loaded host is not a defect in Concord; a rising commit duration is.

### D2. Correctness precedes latency in the invariant, not only in the report

The invariant is not satisfied by a latency bound alone. It requires, on the
accepted agent-facing population:

- zero escaped `SQLITE_BUSY`;
- zero lost effects;
- zero unexpected duplicates;
- zero invariant violations;
- writer-admission wait within the accepted bound.

A failed correctness population forces the latency verdict to `inconclusive` and
fails the run independently. CD-0011 already binds this ordering structurally;
this decision makes the law say what the harness does.

### D3. Population identity is part of the invariant

A bound is meaningless without the population it is measured over. Local
mixed-host runs and isolated required-check runs are different populations and
are never averaged, combined, or substituted for one another. Only the isolated
acceptance entry point, run in the required check, may report an accepted
verdict of `passed` or `fired`; every other run remains `inconclusive` regardless
of the numbers it produces.

CD-0011 owns the population authority; a bound without it permits invalid
averaging across populations.

### D4. Reopen conditions stay with CD-0011

This decision adds no falsifier. CD-0011 §*Falsifier interpretation* remains the
sole list of conditions that reopen the storage authority, including the one
this invariant most directly bears on: *queueing materially blocks ordinary
operator work on the supported deployment*.

The condition measures whether waiting prevents ordinary operator work.

### D5. Invariant 8 is renamed

[`core-architecture.md`](../core-architecture.md) §2 invariant 8 is
**Bounded writer admission**.

The invariant derives from Priority 2 and is non-negotiable and checkable.

## Consequences

- Usability-floor condition 5 and floor item
  `fc5-concurrent-process-conformance` state a requirement a run can fail. The
  item's evidence cites `internal/store/conformance_test.go`, the ten-process
  harness that bears the claim.
- The floor manifest's title-equality check forces `priorities.md` and
  `floor-readiness.v1.json` to move together. That is the mechanism working.
- Priority 2 requires "No hand-repair of history" and "safe evolution for
  in-flight work" alongside the checkable concurrency clause.

## Rejected alternatives

**Reopen the mechanism to achieve literal zero waiting.** This is the other
branch issue #191 offers. Rejected: it contradicts CD-0011, which reviewed
exactly this evidence and retained SQLite, and it is not achievable by any
single-file embedded store — a single-writer IPC boundary relocates the queue
rather than removing it. CD-0011 already names that boundary as the first
bounded comparison *if* writer admission ever becomes the demonstrated
bottleneck. It has not.

**Delete the concurrency clause from Priority 2.** Rejected: it would remove a
real obligation because its wording was wrong. The obligation — that many agents
writing concurrently do not corrupt, lose, duplicate, or stall each other's work
— is the substance of Priority 2's implementation half.

**Soften to prose without a bound.** For example, "writes are serialized
efficiently". Rejected: unfalsifiable claims are what this decision exists to
remove, and an unbounded qualitative claim is the same defect in a more agreeable
register.

**Amend only the floor manifest.** Rejected: the manifest copies its conditions
from `priorities.md` and the checker enforces the copy. Amending the derived
record while leaving the source wrong is the drift `floor-readiness.md` §*Why it
is a manifest* was written to prevent.

## Verification

- `.concord/docs/decisions/CD-0045-writer-admission-invariant.md` exists with status
  `accepted` and is indexed exactly once as the record shard
  `.concord/docs/knowledge/records/CD-0045.json`, composed by `scripts/knowledge_index.py`.
- No document claims zero lock waiting or zero contention for Concord-owned
  writes while `busy_timeout` queueing remains the selected mechanism.
- `.concord/docs/floor-readiness.v1.json` item `fc5-concurrent-process-conformance` names
  the three distinguished quantities and cites the ten-process harness.
- `python3 scripts/check-floor-readiness.py` passes with the condition title
  matching `priorities.md`.
- `python3 scripts/check-json.py`, `python3 scripts/check-doc-links.py`,
  `python3 scripts/check-knowledge-index.py`, and
  `python3 scripts/check-public-content.py` pass.

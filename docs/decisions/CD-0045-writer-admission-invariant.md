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
  rank and substance
- **Supersedes:** nothing; amends the wording of an invariant CD-0011 already
  decided the mechanism for

## Context

Four accepted documents claim that Concord-owned state suffers no lock
contention and no lock waits:

- [`design-constraints.md`](../design-constraints.md) §1 requires work "with
  **no contention, no lock waits, no failed-writes-retry storms**";
- [`design-constraints.md`](../design-constraints.md) §4 is titled *No locks, no
  history repair* and requires that state "never suffers database-lock
  contention";
- [`core-architecture.md`](../core-architecture.md) §2 invariant 8 is named **No
  lock waits**;
- [`priorities.md`](../priorities.md) Priority 2 lists "No database lock
  contention for agent-facing writes", and usability-floor condition 5 restates
  it as "no locks".

The accepted mechanism does something else, deliberately. SQLite permits one
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

So the mechanism is coherent, the measurement is honest, and the law is wrong.
An invariant that cannot be satisfied by the mechanism chosen to satisfy it
cannot be evidence of anything. Worse, floor item
`fc5-concurrent-process-conformance` inherited the same claim — *"Concurrent
agent-facing writes do not contend"* — which made a readiness item unfalsifiable
in the direction that matters: no run can ever demonstrate it, so no run can
ever fail it.

## Decision

### D1. The invariant is bounded writer admission, not absent writer admission

Concord's concurrency law states a measurable boundary over three distinct
quantities that the previous wording collapsed into one:

| Quantity | Meaning | Required bound |
|---|---|---|
| **Writer-admission wait** | Time from a writer requesting `BEGIN IMMEDIATE` to holding the write lock. Nonzero by construction whenever a second writer is active. | Bounded: sustained P99 at or below the accepted target on the isolated acceptance population. |
| **Commit duration** | Time the write lock is actually held. | Bounded, and reported separately so a lock-hold regression is distinguishable from host contention. |
| **Escaped busy failure** | `SQLITE_BUSY` surfacing to a caller after `busy_timeout` expires. | Zero. Not bounded — zero. |

Reads remain lock-free. That claim was always true and is unchanged.

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

This is a restatement of CD-0011's structural population authority, promoted into
the invariant because a bound stated without it invites exactly the averaging
that CD-0011's calibration record already had to correct once.

### D4. Reopen conditions stay with CD-0011

This decision adds no falsifier. CD-0011 §*Falsifier interpretation* remains the
sole list of conditions that reopen the storage authority, including the one
this invariant most directly bears on: *queueing materially blocks ordinary
operator work on the supported deployment*.

That condition is the honest form of the requirement the old wording was
reaching for. The operator's concern was never that a lock is taken; it was that
waiting becomes visible as work not getting done. Stated that way it is
observable, and it already has a home.

### D5. Invariant 8 is renamed

[`core-architecture.md`](../core-architecture.md) §2 invariant 8 becomes
**Bounded writer admission**. The name carries the claim: *No lock waits* is
cited by companion documents that reference the invariant rather than restating
it, so a name that misdescribes the mechanism propagates by design.

The invariant remains derived from Priority 2 and remains non-negotiable. What
changes is that it can now be checked.

## Consequences

- Usability-floor condition 5 and floor item
  `fc5-concurrent-process-conformance` state a requirement a run can fail. The
  item's evidence is corrected to cite `internal/store/conformance_test.go`, the
  ten-process harness that bears the claim, rather than
  `internal/store/workflow_conformance_test.go`, which is the workflow corpus and
  bears no concurrency claim at all.
- The floor manifest's title-equality check forces `priorities.md` and
  `floor-readiness.v1.json` to move together. That is the mechanism working.
- No source file changes. The harness already emits the accepted vocabulary;
  this decision adopts it upward rather than pushing new terms downward.
- No digest, contract, schema, or generated file moves.
- Priority 2 is not weakened. "No hand-repair of history" and "safe evolution for
  in-flight work" are untouched, and the concurrency clause becomes stronger by
  becoming checkable.

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

- `docs/decisions/CD-0045-writer-admission-invariant.md` exists with status
  `accepted` and is indexed exactly once in
  `docs/concord-knowledge-index.v1.json`.
- No document claims zero lock waiting or zero contention for Concord-owned
  writes while `busy_timeout` queueing remains the selected mechanism.
- `docs/floor-readiness.v1.json` item `fc5-concurrent-process-conformance` names
  the three distinguished quantities and cites the ten-process harness.
- `python3 scripts/check-floor-readiness.py` passes with the condition title
  matching `priorities.md`.
- `python3 scripts/check-json.py`, `python3 scripts/check-doc-links.py`,
  `python3 scripts/check-knowledge-index.py`, and
  `python3 scripts/check-public-content.py` pass.

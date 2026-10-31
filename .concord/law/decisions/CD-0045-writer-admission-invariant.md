# CD-0045: Concurrent writers preserve exact effects without escaped busy failures

- **Status:** Accepted
- **Date:** 2026-08-20
- **Scope:** The concurrency invariant borne by the data-safety constraint and
  core invariant 8; the ten-process conformance harness
- **Related:** CD-0002 §2b, CD-0003, CD-0011, CD-0046,
  [`design-constraints.md`](../../docs/design-constraints.md) §1 and §4,
  [`core-architecture.md`](../../docs/core-architecture.md) §2 invariant 8,
  [`priorities.md`](../constitution/priorities.md#data-safety-constraint)
- **Preserves:** SQLite as sole durable authority; the writer transaction
  mechanism; durability and recovery guarantees; lock-free reads

## Context

SQLite permits one writer at a time. Library-in-process access with `WAL`,
`busy_timeout=5000`, and `BEGIN IMMEDIATE` makes concurrent writers queue.
Queueing is the mechanism, not a promise of zero waiting. Concurrent-writer
acceptance must prove data safety without treating host scheduling as a
storage-authority verdict.

## Decision

### D1. The invariant is concurrent-writer correctness

Agent-facing writes serialize through SQLite. On the accepted population they
must complete without escaped `SQLITE_BUSY`, lost effects, unexpected
duplicates, or invariant violations. Reads remain lock-free. The invariant
sets no writer-admission or commit-duration percentile target.

### D2. Correctness is required on the accepted population

The ten-process conformance harness must retain all fifteen base correctness
scenarios and one unpaced 1000-write correctness population spread across ten
worker processes. The latter must check exact expected effects, including
idempotent replays, rather than treating process success as sufficient proof.
The base scenarios retain their operation, invariant, backup, restore, and
recovery assertions.

Every population must have zero lost effects, zero unexpected duplicates, and
zero invariant violations. Zero escaped busy failures additionally binds the
accepted agent-facing population. A correctness failure fails the run
independently of any timing observation. CD-0046 owns accepted-versus-diagnostic
invocation provenance; diagnostic runs do not replace required-check proof.

### D3. Population identity is part of correctness evidence

Local diagnostic runs and required-check acceptance runs remain distinct.
They are never averaged, combined, or substituted for one another. The
required-check signal establishes invocation provenance, not host isolation.

### D4. Reopen conditions stay with CD-0011

CD-0011 owns the conditions that reopen the storage authority. Queueing that
materially blocks ordinary operator work on the supported deployment remains
a reopen condition; it is not a conformance percentile threshold.

### D5. The ten-process latency machinery and its tests are retired

Retire the ten-process write-latency target and falsifier, including wall,
writer-admission-wait, and commit-duration measurements and summaries;
threshold and falsifier classification and status reporting; sustained and
majority-of-rounds rules; the latency control round; production-like pacing
and its unpaced refusal; and configuration used only by those mechanisms.
Retire their classifier, control, pacing, status, measurement, and obsolete
configuration tests. CD-0051 and CD-0057 are retired in full, including their
law files and knowledge records.

This retirement does not remove the fifteen base correctness scenarios, the
unpaced 1000-write ten-process correctness population in D2, accepted-population
resolution, the CI missing-signal tripwire, or the accepted-population
zero-escaped-busy policy and their tests. It does not change unrelated
read-path P99 obligations or proof, durability and recovery obligations or
proof, or runtime transaction behavior. No replacement performance gate or
benchmark framework is required.

## Consequences

Core invariant 8 is **Concurrent-writer correctness**. Required ten-process
conformance proves exact effects and concurrent-writer data safety, not a
timing verdict. Correctness remains a required check.

## Verification

- `internal/store/conformance_test.go` retains the fifteen base scenarios and
  the unpaced 1000-write ten-process population with exact-effect assertions.
- `internal/store/conformance_status_test.go` verifies population resolution,
  the CI tripwire, and accepted-population zero-escaped-busy behavior.
- `python3 scripts/check-json.py`, `python3 scripts/check-doc-links.py`,
  `python3 scripts/check-knowledge-index.py`, and
  `python3 scripts/check-public-content.py` pass.

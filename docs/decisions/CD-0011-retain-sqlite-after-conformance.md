# CD-0011 — Retain SQLite after ten-process conformance

**Status:** Accepted.
**Date:** 2026-08-07.
**Approval:** Explicit operator approval after review of PR #16 and issue #17.
**Type:** Architecture confirmation after a falsifier review.

## Decision

Concord retains direct, library-in-process SQLite as its one local live-state
authority for v1.

The ten-process conformance run triggered the accepted write-tail review on a
contended development host. That trigger required comparison; it did not mandate an
automatic replacement. The comparison found:

- correctness remained clean across every measured population: no lost effects,
  invariant violations, escaped busy errors, or unexpected duplicates;
- commit latency remained low; variable writer-admission wait owned the observed
  tail;
- isolated required-check runners sustained the accepted target in their governing
  rounds, while a concurrently loaded development host produced higher and more
  variable tails;
- replacing the authority now would add a daemon or service, deployment and restore
  obligations, and another migration before a user-visible need proves that cost.

The current architecture therefore remains:

1. one global local SQLite database per operator installation;
2. short-lived processes using the in-process store library;
3. `WAL`, `synchronous=NORMAL`, per-connection foreign keys and busy timeout;
4. append-only events, atomic projection folds, explicit attempt epochs, durable
   idempotency, and PM10 Online Backup/Restore;
5. git as the separate durable-knowledge authority fixed by PM6–PM10.

No single-writer IPC boundary, Dolt server, Postgres, or DBOS authority is introduced
now.

## Falsifier interpretation

CD-0002 and CD-0008 remain binding. A latency falsifier opens a review; it does not
select a replacement by itself. This review is complete and retains SQLite.

The accepted write-tail target remains visible in conformance reports. Concord must
reopen this decision when any of these occurs:

- an escaped `SQLITE_BUSY`, lost/duplicate effect, or invariant violation;
- sustained user-visible write latency above the accepted target on the repeatable
  acceptance population, not a mixed or unidentified population;
- queueing materially blocks ordinary operator work on the supported deployment;
- the deployment scope stops being one local operator installation;
- backup, restore, rebuild, or upgrade evidence fails.

Future reviews must compare the same operation population and report correctness
before latency. A single-writer IPC boundary is the first bounded comparison when
writer admission is the demonstrated bottleneck. Dolt and Postgres/DBOS receive no
automatic preference.

## Evidence

- PR #16 implements the ten-process harness, D4 fencing/idempotency, and PM10
  backup/restore evidence.
- Issue #17 records contended-host and isolated-runner populations separately.
- Required-check runs completed with all correctness invariants satisfied.

## Consequences

- PR #16 may merge as conformance evidence.
- Issues #3 and #17 may close.
- Future performance evidence must preserve population identity and continue showing
  raw P50/P99/max, admission wait, commit time, and correctness classifications.
- This decision does not authorize snapshots, a daemon, or distributed deployment.

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

## Accepted load profile calibration

The accepted long profile is production-paced, not max-rate spin. R4 measured the
predecessor workload below 0.1 writes/second system-wide with no concurrent writes
observed; the acceptance profile paces each of the ten worker processes at one
attempt per 100 ms (100 writes/second system-wide, 1000x the measured envelope).
The pacing interval equals the accepted P99 target, so lock-hold regressions still
trip the gate.

Max-rate spin remains available as diagnostic-only stress evidence through
`CONCORD_CONFORMANCE_UNPACED=1`. The isolated acceptance profile refuses unpaced
runs; an unpaced run can never produce an accepted falsifier verdict.

Population authority is structural: only the isolated acceptance test entry point,
run in the required `verify` check, may report falsifier `passed` or `fired`. Local
and development runs are diagnostic and remain `inconclusive` even when the
threshold is exceeded. Correctness precedes every latency verdict: any failed
correctness population forces the falsifier verdict to `inconclusive` and fails the
run independently.

### Calibration record

Issue #55 investigated the sustained P99 falsifier. Controlled same-population
evidence showed host CPU contention inflating writer-admission wait while commit
latency stayed low; the max-rate spin profile (about 330-500 writes/second,
3000-5000x the measured production envelope) then fired on the shared required-check
runner with all correctness invariants clean. Operator approval on 2026-08-11
calibrated the accepted profile to the R4 production envelope as above. The storage
decision itself was not reopened: no escaped busy, lost or duplicate effect, or
invariant violation occurred on any population.

## Evidence

- PR #16 implements the ten-process harness, D4 fencing/idempotency, and PM10
  backup/restore evidence.
- Issue #17 records contended-host and isolated-runner populations separately.
- Required-check runs completed with all correctness invariants satisfied.
- Issue #55 and PR #59 record the pacing calibration: controlled development-host
  reproduction, the shared-runner falsifier firing, and the R4-calibrated accepted
  load profile with structural population authority.

## Consequences

- PR #16 may merge as conformance evidence.
- Issues #3 and #17 may close.
- Future performance evidence must preserve population identity and continue showing
  raw P50/P99/max, admission wait, commit time, and correctness classifications.
- This decision does not authorize snapshots, a daemon, or distributed deployment.

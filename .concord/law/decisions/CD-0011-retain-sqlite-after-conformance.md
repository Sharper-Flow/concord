---
{
  "id": "CD-0011",
  "title": "Retain SQLite after conformance",
  "authority": {
    "tier": "derived"
  },
  "home_domain_id": "durable-authority",
  "applies_to_domain_ids": []
}
---
# CD-0011 — Retain SQLite after ten-process conformance

**Status:** Accepted.
**Date:** 2026-08-07.
**Approval:** Explicit operator approval after review of PR #16 and issue #17.
**Type:** Architecture confirmation after a falsifier review.

## Decision

Concord retains direct, library-in-process SQLite as its one local live-state
authority for v1.

The ten-process conformance review retained SQLite because:

- correctness remained clean across every measured population: no lost effects,
  invariant violations, escaped busy errors, or unexpected duplicates;
- replacing the authority now would add a daemon or service, deployment and restore
  obligations, and another migration before a user-visible need proves that cost.

The current architecture therefore remains:

1. one global local SQLite database per operator installation;
2. short-lived processes using the in-process store library;
3. `WAL`, `synchronous=NORMAL`, per-connection foreign keys and busy timeout;
4. current-row writes checked by expected version and recorded by idempotency key
, explicit attempt
   epochs, and PM10 Online Backup/Restore;
5. git as the separate durable-knowledge authority fixed by PM6–PM10.

No single-writer IPC boundary, Dolt server, Postgres, or DBOS authority is introduced
now.

## Falsifier interpretation

[CD-0195](CD-0195-the-escaped-sqlite-busy-on-record-is-an-application-defect.md)
holds the disposition of the two escaped-`SQLITE_BUSY` classes on record through
2026-09-30. The condition applies to any later escape.
The store keeps current rows; no event log or projection fold exists
([CD-0242 D2](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)).

CD-0002 and CD-0008 remain binding. This review is complete and retains SQLite.
Concord must reopen this decision when any of these occurs:

- an escaped `SQLITE_BUSY`, lost/duplicate effect, or invariant violation;
- queueing materially blocks ordinary operator work on the supported deployment;
- the deployment scope stops being one local operator installation;
- backup, restore, rebuild, or upgrade evidence fails.

Future reviews must compare the same operation population and establish correctness.
A single-writer IPC boundary is the first bounded comparison when
writer admission is the demonstrated bottleneck. Dolt and Postgres/DBOS receive no
automatic preference.

## Accepted correctness population

[CD-0045 D2](CD-0045-writer-admission-invariant.md#d2-correctness-is-required-on-the-accepted-population)
requires the fifteen base correctness scenarios and one unpaced 1000-write
population across ten processes, with exact-effect assertions and retained
backup, restore, and recovery proof. D5 retires the ten-process latency
falsifier and its measurement, control, pacing, status, and tests.

[CD-0046](CD-0046-conformance-population-provenance.md) owns invocation
provenance: the acceptance entry point in the required `verify` check uses
`CONCORD_ACCEPTANCE_RUNNER=1`, set by the workflow. Local runs remain
diagnostic; a CI run missing the signal fails visibly. The signal does not
measure host isolation. Every correctness failure fails the run independently.

## Evidence

- PR #16 implements the ten-process harness, D4 fencing/idempotency, and PM10
  backup/restore evidence.
- Issue #17 records contended-host and isolated-runner populations separately.
- Required-check runs completed with all correctness invariants satisfied.

## Consequences

- PR #16 may merge as conformance evidence.
- Issues #3 and #17 may close.
- Conformance evidence preserves population identity and exact-effect
  correctness without a write-latency verdict.
- This decision does not authorize snapshots, a daemon, or distributed deployment.

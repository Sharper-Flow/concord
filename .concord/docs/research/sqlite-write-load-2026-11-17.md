# SQLite write load: partial observation

## Finding

The observed committed state-write admission rate was low in one five-minute
window: 17 admissions, approximately 3.4 per minute. The measurement does not
show lock-hold duration, writer wait, busy outcomes, or whether queueing
materially blocked ordinary operator work. It neither establishes nor disproves
the relevant SQLite reopen condition. The rate alone does not justify a storage
change, and this report is not a latency or architecture verdict.

The operator accepted partial findings and stopped further instrumentation.
Earlier capture commands are withdrawn. This observation is not evidence of
the same operation population, future load, or a representative all-day rate.

## Scope and method

The observation ran from 2026-11-17 00:53:33.398533 UTC to
00:58:33.401321 UTC. It collected 61 samples at five-second intervals over
300 seconds, with zero sampler error records. The first-to-last counter
observation elapsed bounds were 299.961–300.003 seconds. Bounds account for
the time taken to obtain the endpoint samples, not for writer timing.

The sampler used Python 3.14.4 and SQLite 3.46.1, opening the database by URI
with `mode=ro`, `query_only=ON`, a one-second timeout, and a short deferred read
transaction per sample; it rolled back the read transaction and closed the
connection. It performed no checkpoint, migration, or live database page
writes. WAL read access may still touch shared-memory read marks or sidecars.
The sampler timeout is not the core's five-second busy timeout.

Database identity and migration identity remained unchanged across samples:
201 migrations and SQLite `schema_version` 2226. Raw samples and sampler
source are retained outside Git in the owning work record. The retained
sample file SHA-256 is
`50b336be8fa53e879150809a0cf6bab89fa03a570f8078d82c1b265c6d037566`;
the sampler source SHA-256 is
`00d3d76d135ad777eec4304a9b18e893292c26f5e0579f4c752283fded4fca6f`.

Each sample read these two counters from one database snapshot:

```sql
SELECT revision FROM store_revision WHERE id=1;
SELECT last FROM store_sequences WHERE name='effect';
```

The sampler also read `PRAGMA schema_version` and the ordered migration
versions and checksums. It calculated each rate as the counter delta divided
by elapsed time; endpoint sample start and end times bound that elapsed time.

## Observations and counter meaning

| Counter | First | Last | Delta | Approximate rate |
| --- | ---: | ---: | ---: | ---: |
| State-write revision | 111660 | 111677 | 17 | 0.05667/s; 3.4/min |
| Effect sequence | 116463 | 116486 | 23 | 0.07667/s; 4.6/min |

The largest five-second step contained two committed state-write admissions
and a sequence increase of three. Of 60 intervals, 46 had no revision increase,
11 had one, and three had two.

[Write admission](../../../internal/store/internal/sqlitedb/write.go#L33-L81)
advances the revision inside every state-write transaction; the
[revision implementation](../../../internal/store/internal/sqlitedb/revision.go#L8-L31)
updates the singleton, and the [store interface](../../../internal/store/store_revision.go#L10-L23)
notes that even a write with no other changed row can advance it. The committed
revision delta therefore counts committed state-write admissions, including
no-op writes. It is not a count of all writes, SQL statements, or changed rows.
Rollbacks, refusals, and abandoned lock holders are not counted. It does not
measure the active writer population.

The [effect sequence](../../../internal/store/internal/sqlitedb/effect.go#L8-L34)
is allocated within a transaction for effect ordering. Its increase is distinct
from state-write admissions and is not a latency measurement. Neither counter
reveals lock holds or waits. Current-row timestamps cannot reconstruct those
durations, and this observation had no timing recorder.

## Host context

The sampler observed 27–29 live host leases, matching the observed OpenCode
process count at every sample. These are neither 20 writers nor a count of
active writers. Process scans had 1–6 `/proc` read errors in 34 samples, so
the observed process counts are lower bounds; lease reads had zero errors.
The host reported 16 logical CPUs and a constant affinity of 16 CPUs.

Across the window, the first eight aggregate `/proc/stat` CPU counter fields
increased by 474384 ticks, excluding guest fields to avoid double counting.
User, nice, system, IRQ, and soft IRQ increased by 410376 ticks, or 86.5% of
that total; idle increased by 63327, I/O wait by 593, and steal by 88. The
one-minute load average ranged from 42.37 to 55.43. Load includes runnable and
uninterruptible tasks; it does not prove CPU saturation or Concord causation.
No CPU usage was attributed to the store.

## Interpretation against the decisions

The core configures a one-connection pool, WAL, immediate write transactions,
and a five-second busy timeout in its
[connection settings](../../../internal/store/internal/sqlitedb/connection.go#L16-L49).
Those settings describe the mechanism, not measured wait or hold times.

[CD-0011](../../law/decisions/CD-0011-retain-sqlite-after-conformance.md#falsifier-interpretation)
requires reopening the storage decision if queueing materially blocks ordinary
operator work on the supported deployment. This sample cannot decide that
condition. [CD-0045 D4–D5](../../law/decisions/CD-0045-writer-admission-invariant.md#d4-reopen-conditions-stay-with-cd-0011)
retains the condition without a conformance latency percentile and requires no
replacement performance gate or benchmark framework. Its correctness proof
does not establish a timing verdict.

[CD-0195](../../law/decisions/CD-0195-the-escaped-sqlite-busy-on-record-is-an-application-defect.md#decision)
disposes of the historical escaped-busy classes as application defects; that
disposition does not prove zero queueing now. This report changes no law and
proposes no architecture change.

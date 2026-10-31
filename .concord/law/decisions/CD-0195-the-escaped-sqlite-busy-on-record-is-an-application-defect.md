# CD-0195: The escaped SQLITE_BUSY on record is an application defect, not a storage limit

- **Status:** Accepted
- **Date:** 2026-09-30
- **Scope:** The escaped-`SQLITE_BUSY` occurrences on record through
  2026-09-30, the write-transaction boundary in the store, and the conformance
  workload population, [Concord (CON) issue 750](https://linear.app/sharper-flow/issue/CON-750)
- **Preserves:** CD-0011's current reopen conditions, CD-0045's concurrent-writer
  correctness, and the single-writer comparison as the first
  bounded comparison
- **Related:** CD-0003, CD-0045, CD-0046, CD-0165,
  [Concord (CON) issue 752](https://linear.app/sharper-flow/issue/CON-752)
- **Approval:** The operator approved the work item
  [Concord (CON) issue 750](https://linear.app/sharper-flow/issue/CON-750)
  contract that carries this record on 2026-09-30.

## Context

CD-0011 retains SQLite as the one local live-state authority and lists an
escaped `SQLITE_BUSY` among the conditions that reopen that decision. The
condition occurred. Retained tool results on this installation hold two
failure classes between 2026-09-17 and 2026-09-30, all times UTC.

Class 1 is the open path. `ensureInstallationKey` ran `INSERT OR IGNORE` on
every store open, so an open took the write lock and failed when a foreign
writer held the lock past the 5 s `busy_timeout`. The retained results hold
51 such failures, 37 of them on 2026-09-27. Commit 49181de5 (PR #1462) reads
the installation key before writing and shipped in v11.45.3, tagged
2026-09-29. Three failures after that tag carry the manifest digest of
v11.45.2, the release before the fix, so sessions pinned to that release
produced them.

Class 2 is writer admission on the current release. Store write transactions
open with `BEGIN IMMEDIATE` and hold the write lock across git subprocess and
filesystem work. Worktree reclaim and destroy run probe, diff, and removal
chains inside the transaction, and claim, verify, and bootstrap do the same.
Successful reclaim and destroy calls ran about 8 s at the median while
refused calls of the same operations ran about 1.5 s, so the in-transaction
git work owns the gap. These are wall-time upper bounds, not measured lock
holds. On 2026-09-30 a concurrent session start on v11.50.0, a release that
contains the class-1 fix, failed with
`cannot begin transaction: database is locked (5) (SQLITE_BUSY)`. The
source-level diagnosis and the repair are
[Concord (CON) issue 752](https://linear.app/sharper-flow/issue/CON-752),
work `work-bb6e60b8c80d974be982a51f`.

Cleared along the way. In `WAL` mode readers do not block writers, so the
read-only probes run during capture are an unlikely holder, and they are not
formally excluded. [`modernc.org/sqlite`](https://pkg.go.dev/modernc.org/sqlite) v1.59.0 embeds SQLite 3.53.4, which
carries the WAL-reset fix from 3.51.3, so the engine version is not
implicated.

## Decision

### D1. The two escape classes on record are application defects

The escaped-`SQLITE_BUSY` occurrences on record through 2026-09-30 fall into
the two classes the Context names, and each has a named cause in Concord
application code. For these two classes the disposition is application
defect, and the storage authority stays. Neither class changes the engine choice, so the retention in
CD-0011 stands. This record does not change what a later escape means: that
escape falls under CD-0011's reopen condition as written, and D4 applies.

### D2. No write transaction spans a subprocess, a network call, or a large filesystem operation

A store write transaction completes its work in memory and in the database.
It must not hold the write lock across a subprocess, a network call, or a
large filesystem operation. Probe git facts before the transaction,
re-validate them cheaply inside it, and run bulk removal after the commit.
The rule binds the store and every caller that opens store transactions.

### D3. The accepted conformance population remains

The ten-process conformance population stays the accepted population. Retired
ownership operations are not workload obligations (CD-0088 D1). A tool call
here is a short-lived process, and many
sessions run concurrently, so the workload must represent that shape.
CD-0046 keeps invocation provenance as the population authority, and CD-0045
keeps exact effects and zero escaped busy failures on the accepted population.

### D4. The reopen path stands, single-writer comparison first

If an escaped `SQLITE_BUSY` occurs on the current release after the CON-752
repair lands, the storage decision reopens as CD-0011 states. The first
bounded comparison is then the single-writer local IPC boundary, in CD-0011's
stated order, and no engine replacement occurs without a failing comparison.

## Alternatives considered

- **Reopen the storage authority now.** Rejected: both classes are defects in
  Concord application code with named repairs, and CD-0011 requires a failing
  comparison before any replacement.
- **Name writer queue depth the cause.** Rejected: the wall-time gap between
  refused and successful calls tracks the in-transaction git work, and the
  source diagnosis names the holder.
- **Raise `busy_timeout`.** Rejected: a longer wait hides the holder instead
  of removing it, and CD-0045 keeps the bound at zero escaped busy.

## Consequences

CD-0011's escaped-busy condition is discharged for the two classes on record,
and every reopen condition in CD-0011 stands as written for later escapes.
The transaction-boundary proof requires a structural guard beside
`internal/store/txscope_test.go` that fails when a transaction-holding
function reaches a subprocess or a bulk filesystem call. No falsifier vocabulary changes.
CD-0165 binds new work to the current release.

## Verification

- `python3 scripts/check-knowledge-index.py` composes the manifest with the
  CD-0195 shard and the refreshed CD-0011 hash.
- `python3 scripts/check-doc-links.py` resolves the amendment link into
  CD-0011.
- `python3 scripts/check-public-content.py` passes.

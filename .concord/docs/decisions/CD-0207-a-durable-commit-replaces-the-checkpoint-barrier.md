# CD-0207: A durable commit replaces the checkpoint barrier

- **Status:** Accepted
- **Date:** 2026-10-06
- **Scope:** The mechanism that makes a CD-0050 consequential acknowledgement
  durable, the CD-0050 D3 enumeration, and the CD-0050 D4 placement rule
- **Amends:** CD-0050 D1, the mechanism; CD-0050 D3, the family list;
  CD-0050 D4, the fence placement; and the CD-0050 Consequences, Rejected
  alternatives, and Verification statements that describe `SyncDurable`
- **Preserves:** CD-0050 D2, the durability guarantee; CD-0050 D5, the
  migration exclusion; `synchronous=NORMAL` for ordinary writes; the
  conformance harness, its pacing, threshold, and population authority; the
  rejection of `synchronous=FULL` for every write
- **Related:** CD-0050, CD-0002, CD-0011, CD-0122,
  [pull request 1558](https://github.com/Sharper-Flow/concord/pull/1558),
  [pull request 1557](https://github.com/Sharper-Flow/concord/pull/1557),
  [Concord (CON) issue 855](https://linear.app/sharper-flow/issue/CON-855)
- **Approval:** The operator approved the repair in pull request 1558 under
  the CD-0122 defect-repair route, with law to follow. The operator approved
  the work item contract that carries this record on 2026-10-06.

## Context

CD-0050 D1 made a `PRAGMA wal_checkpoint(TRUNCATE)` after the outermost commit
the durability barrier, exposed as `SyncDurable`. A TRUNCATE checkpoint must
wait for every WAL reader to finish before it resets the log.

That wait failed in production. Each OpenCode session starts a fresh
`concord continuity-block` process on each chat completion. On the live
store, 26 such processes ran each minute, and each held a WAL read mark for
about two seconds. Consequential operations then committed and reported
`durability checkpoint did not complete: busy=1`. The caller saw a failure for
a write that had committed. A regression that holds a read mark open during an
acknowledgement fails on the commit before pull request 1558.

Pull request 1558 (commit `059de5dc`, release v11.63.20) replaced the barrier
with a durable commit. Accepted law still names the checkpoint. This record
makes the law state the shipped mechanism.

## Decision

### D1. A consequential operation commits in a durable write transaction

A consequential operation commits in a durable write transaction. The store
pins the pool's one connection, sets `synchronous=FULL` before `BEGIN`, and
reads the level back. The transaction refuses unless the level is 2. SQLite
refuses a safety-level change inside an open transaction, so the change comes
before `BEGIN`.

Under `FULL`, SQLite syncs the WAL before `COMMIT` returns. A durable commit
that returns success is therefore durable. The fsync flushes the whole dirty
tail of the append-only WAL, so it also makes every earlier commit on the store
durable.

After the commit or the rollback, the store sets `synchronous=NORMAL` again
and returns the connection to the pool. If that reset fails, the store
discards the connection, so the pool never reuses a connection left at `FULL`.
The acknowledgement needs no checkpoint after the commit.

`internal/store/durable.go` owns the mechanism: the `writeTx` type,
`Store.TransactDurable`, and `Store.DurableCommits`. `SyncDurable` no longer
exists. Where CD-0050 D2 says an operation is acknowledged after `SyncDurable`
succeeds, it reads instead: after its durable commit succeeds. The D2
guarantee does not change.

### D2. The enumeration names every family the store commits durably

CD-0050 D3 lists five families. Four more families carried the barrier
before pull request 1558, but D3 never named them. The enumeration adds them:

| Family | Sites | Why it is consequential |
|---|---|---|
| Bootstrap journal and worktree claim | `internal/store/bootstrap.go` `claimCrossProjectWorktree`, `prepareBootstrapMode`, `setBootstrapState`, `rollbackBootstrap`, `finalizeBootstrap`; `internal/store/worktrees.go` `ClaimWorktree` | The journal and the claim decide which work item owns a worktree on disk. A claim lost after its acknowledgement permits a second owner of the same worktree. |
| Predecessor import | `cmd/concord/predecessor_import.go` `writeProductAndProjects`, `writeSecondaryProjects`, `writeSelectedWork` | The acknowledged import is the record of which Products, Projects, and work items migrated. A loss after the acknowledgement misstates the migration. |
| Fold-guard recovery | `internal/store/recovery.go` `RecoverFoldGuard` | Recovery clears a stranded fold guard and rebuilds every projection in one transaction. Its acknowledgement admits ordinary writes again. |

The five CD-0050 D3 families remain. The site column of the cross-authority
row now names the fence entry points of D3 of this record.

### D3. The public fence entry points commit durably

`ClaimStep`, `ClaimStepAuthorized`, and `CompleteStep` in
`internal/store/fence.go` commit durably. A durable commit waits for no WAL
reader, so the CD-0050 D4 failure mode does not occur.

The conformance harness calls the fence cores with `durable=false`. It still
measures ordinary commits. Its pacing, threshold, and population authority do
not change.

## Alternatives considered

- **Keep the post-commit TRUNCATE checkpoint.** Rejected on evidence. Constant
  continuity readers made the checkpoint report busy after the commit. The
  pinned-reader regression fails on the commit before pull request 1558.
- **Set `synchronous=FULL` for every write.** Rejected again on measurement.
  Pull request 1557 measured commit p99 of 91 ms to 109 ms on `ubuntu-latest`
  against the accepted 100 ms target.
- **Per-transaction pragma flipping, as CD-0050 rejected it.** Adopted. CD-0050
  rejected it because a concurrent writer on the same pooled connection could
  commit under the wrong mode. That premise does not hold. The pool has one
  connection, and the durable transaction pins it from the pragma change to the
  reset, so no other write can use it.
- **Reword CD-0050 in place.** Rejected. Accepted law changes through an
  amendment record that carries its own authority and approval.

## Consequences

- No result is committed but not durable. A durable commit either returns
  success and is durable, or returns a failure.
- A WAL reader cannot delay or fail an acknowledgement. Only the writer lock,
  under the connection `busy_timeout`, can.
- Ordinary writes stay at `synchronous=NORMAL`.
- A consequential acknowledgement no longer truncates the WAL. SQLite
  automatic checkpoints still run.
- An open question remains: does the WAL need a size backstop, such as
  `journal_size_limit` or a checkpoint that a size threshold starts? A 60 s
  sample of the live store found a WAL peak of 149 KB. Sessions on releases
  before v11.63.20 still truncated the WAL during that sample, so the sample
  does not show that a backstop is necessary. A separate measured follow-up
  decides it. This record adds no backstop.

## Verification

- `internal/store.TestDurableTxCommitsUnderFull` proves a durable transaction
  commits under `synchronous=FULL`, counts in `Store.DurableCommits`, and
  returns the connection at `NORMAL` after a commit and after a rollback.
- `internal/store.TestDurableCommitIgnoresPinnedReader` proves an open WAL
  reader does not fail a durable commit.
- `internal/store.TestDurableTransactFailureSurfaces` proves a durable
  transaction failure reaches the caller.
- `internal/agent.TestGrantRequestAcknowledgementIsDurable` proves each
  acknowledged grant request adds a durable commit.
- `internal/agent.TestGrantRequestAcknowledgesUnderPinnedReader` and
  `cmd/concord.TestPredecessorImportAcknowledgesUnderPinnedReader` prove the
  acknowledgement succeeds while another connection holds a WAL read mark.
- `python3 scripts/check-doc-contract.py` proves this record carries the
  current decision outline and passes the writing rules.
- `python3 scripts/check-knowledge-index.py` and
  `python3 scripts/check-knowledge-closure.py` prove this record registers
  with a current content hash and no unprocessed document remains.
- `python3 scripts/check-cd-allocation.py --no-fetch` proves the CD-0207
  identifier allocates once.

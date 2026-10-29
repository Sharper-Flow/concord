# CD-0050: Durable acknowledgement at consequential boundaries

- **Status:** Accepted
- **Date:** 2026-08-21
- **Scope:** The durability contract for acknowledged writes under CD-0002 §2b
  and CD-0011 item 3; ordinary-write loss bounds and the migration exclusion
- **Approval:** Operator approved direction P4 on 2026-08-21 after reviewing
  the CI measurement record on
  [issue #189](https://github.com/Sharper-Flow/concord/issues/189#issuecomment-5362562959),
  and accepted the record as drafted on merge approval; the public record is
  [issue #189 comment](https://github.com/Sharper-Flow/concord/issues/189#issuecomment-5365145398)
- **Related:** CD-0002 §2b, CD-0011 §Decision item 3, CD-0036,
  CD-0046, [`design-constraints.md`](../design-constraints.md) §4,
  [`priorities.md` — Data safety](../priorities.md#data-safety-constraint)
- **Preserves:** `synchronous=NORMAL` for ordinary writes; the conformance
  harness, its pacing, threshold, and population authority; CD-0011's
  falsifier list and retention decision

## Context

Under CD-0002 §2b the store commits with WAL plus `synchronous=NORMAL`.
SQLite's documented guarantee for that combination is narrow: the database
remains consistent after an operating-system crash or power loss, but a
transaction already reported committed may roll back, because most commits do
not sync the WAL. A verified snapshot restore cannot recover a row that
disappeared with the unsynced WAL tail.

The issue #189 measurements put `synchronous=FULL` commit P99 at about
183 ms on `ubuntu-latest`, above the accepted 100 ms target; NORMAL measured
58–82 ms. Using FULL for every write would make the accepted conformance
gate unreliable. Ordinary writes therefore stay NORMAL, while acknowledgements
that bind authority require durability: losing an acknowledged authority change
is not safe merely because the database remains consistent.

## Decision

### D1. Durable transactions and consequential boundaries

[CD-0207 D1–D3](CD-0207-a-durable-commit-replaces-the-checkpoint-barrier.md#d1-a-consequential-operation-commits-in-a-durable-write-transaction)
owns the durable transaction mechanism, consequential-family enumeration,
and placement of durability in the public fence entry points.

### D2. Acknowledged consequential operations are durable; ordinary writes are durable through the last boundary

The contract, stated as Product law:

- An operation whose acknowledgement binds authority is acknowledged only
  after its durable commit succeeds. It is therefore durable
  across power loss and operating-system crash.
- Ordinary writes remain `synchronous=NORMAL`. After a power failure they may
  roll back, but never past the last successful durable commit: everything committed
  before a consequential acknowledgement is durable with it. The database
  remains consistent in every case.
- Nothing in this decision claims a snapshot restore recovers a row absent from
  the store. If an ordinary write rolls back, it rolls back; there is
  no silent reconstruction.

Consent classification is not durability classification. Removing a routine
approval under CD-0013 D11 does not remove a durable acknowledgement boundary.
Workflow actions, terminal transitions, authority changes, and cross-authority
claims retain their durable commits under CD-0207. Mutation and recovery paths must select durability from the effect and retained
intent, not merely from the presence of an approval reference.

### D5. Schema migrations are excluded

Migrations at open are idempotent and re-run on the next open; a lost
migration record heals itself rather than corrupting. They carry no
acknowledgement to an external caller.

## Consequences

- Ordinary-write loss is bounded by the last consequential acknowledgement
  on the same store, not by an elapsed-time guarantee.
- The conformance harness continues to measure ordinary commits; its pacing,
  threshold, and population authority are unchanged.

## Verification

- `internal/store.TestDurableTxCommitsUnderFull` verifies FULL for a durable
  transaction, NORMAL for an ordinary transaction, durable-commit counting,
  and restoration to NORMAL after commit and rollback.
- `internal/store.TestDurableCommitIgnoresPinnedReader` verifies a trusted-client
  acknowledgement commits durably while another connection holds a WAL read mark.
- `internal/store.TestDurableTransactFailureSurfaces` verifies a closed store
  returns a typed, retry-safe failure and records no durable commit.

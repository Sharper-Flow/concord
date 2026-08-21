# CD-0050: Durable acknowledgement at consequential boundaries

- **Status:** Accepted
- **Date:** 2026-08-21
- **Scope:** The durability contract for acknowledged writes under CD-0002 §2b
  and CD-0011 item 3; the `SyncDurable` store mechanism; the boundary
  enumeration deciding which acknowledged operations are guaranteed durable
  across power loss; issue #189
- **Approval:** Operator approved direction P4 on 2026-08-21 after reviewing
  the CI measurement record on
  [issue #189](https://github.com/Sharper-Flow/concord/issues/189#issuecomment-5362562959),
  and accepted the record as drafted on merge approval; the public record is
  [issue #189 comment](https://github.com/Sharper-Flow/concord/issues/189#issuecomment-5365145398)
- **Related:** CD-0002 §2b, CD-0011 §Decision item 3, CD-0036, CD-0044,
  CD-0046, [`design-constraints.md`](../design-constraints.md) §4,
  [`priorities.md`](../priorities.md) Priority 2
- **Preserves:** `synchronous=NORMAL` for ordinary writes; the conformance
  harness, its pacing, threshold, and population authority; CD-0011's
  falsifier list and retention decision
- **Supersedes:** nothing; adds a mechanism the retained architecture lacked

## Context

Under CD-0002 §2b the store commits with WAL plus `synchronous=NORMAL`.
SQLite's documented guarantee for that combination is narrow: the database
remains consistent after an operating-system crash or power loss, but a
transaction already reported committed may roll back, because most commits do
not sync the WAL. A projection rebuild cannot recover an event that
disappeared with the unsynced WAL tail.

Issue #189 asked whether to adopt `synchronous=FULL`. Three benchmark passes
on the development host and three accepted-population CI samples (issue #189
comment, 2026-08-20) established:

- FULL's per-commit median cost is not the issue — commit p50 stays low on
  both hosts.
- On `ubuntu-latest`, FULL's periodic WAL-sync tail puts quiet-run P99 at
  ~183 ms against the accepted 100 ms target, and shared-runner storage
  variance reaches 533 ms. No tested coupling of pacing and target yields a
  dependable required check.
- NORMAL on the same runner class passes with p99 58–82 ms.

So "FULL everywhere" buys durability by making the accepted conformance gate
structurally flaky on the runner class the required check actually uses. That
trade was rejected; what follows is the narrower guarantee the Product
actually needs.

## Decision

### D1. A checkpoint is the durability barrier, and it holds even under NORMAL

sqlite.org/wal.html states: *"Checkpointing does require sync operations in
order to avoid the possibility of database corruption following a power loss
or hard reboot. The WAL must be synced to persistent storage prior to moving
content from the WAL into the database and the database file must be synced
prior to resetting the WAL."* and *"With PRAGMA synchronous set to NORMAL,
the checkpoint is the only operation to issue an I/O barrier or sync
operation."*

Therefore `PRAGMA wal_checkpoint(TRUNCATE)` — without touching
`synchronous` — syncs the entire dirty tail of the append-only WAL, copies it
into the database file, syncs that file, and resets the WAL. Because fsync
flushes the whole dirty tail, one checkpoint makes durable every commit made
before it, not only the most recent.

`internal/store/durable.go` exposes this as `SyncDurable(ctx)`. It returns
success only when the checkpoint reports zero busy; a busy or failed
checkpoint returns a retry-safe typed failure and never claims durability it
did not establish.

### D2. Acknowledged consequential operations are durable; ordinary writes are durable through the last boundary

The contract, stated as Product law:

- An operation whose acknowledgement binds authority — the families in D3 —
  is acknowledged only after `SyncDurable` succeeds. It is therefore durable
  across power loss and operating-system crash.
- Ordinary writes remain `synchronous=NORMAL`. After a power failure they may
  roll back, but never past the last successful barrier: everything committed
  before a consequential acknowledgement is durable with it. The database
  remains consistent in every case.
- Nothing in this decision claims a fold recovers an event absent from the
  authoritative log. If an ordinary write rolls back, it rolls back; there is
  no silent reconstruction.

### D3. The consequential boundary enumeration

`SyncDurable` runs at the acknowledgement point — after the outermost
commit, before success returns to the caller — of exactly these families:

| Family | Sites | Why it is consequential |
|---|---|---|
| Grant, client, and approval authority | `internal/store/agent_authority.go`: register/update-policy/rotate/revoke trusted client, persist/consume/revoke grant, approval and challenge revocation | These mint, consume, and destroy authority. Acknowledged loss of a revocation or a grant's use-count after power failure is unsafe and not safely re-issuable. |
| Workflow action dispatch boundary | `internal/store/workflow_preflight.go` `AuthorizeWorkflowActionAtBoundaryTx` | One transaction encloses condition resolution, grant and approval consumption, the fence-claim row, and terminal completion for the `complete` action. Its acknowledgement is the moment execution authority is granted or a terminal effect lands; product-changing terminal actions bind Domain law (CD-0036, CD-0041). |
| Standalone workflow completion | `internal/store/workflow_completion.go` `CompleteWorkflowWithRegistry` | The completion entry point reachable outside the dispatch boundary; same terminal authority. |
| Cross-authority steps | `internal/agent/mutations.go` `mutateCompaction`: `ClaimStepAuthorized` before the git cross-authority dispatch, `CompleteStep` after it, on both publish and reconcile paths | Losing the fence claim to power loss could permit a duplicate dispatch on retry; losing the completion misstates a cross-authority effect. |
| Worker evidence | `cmd/concord/main.go` `applyWorkerEvidence` (worker-dispatch, worker-complete, worker-fail) | CD-0044 binds these appends to signed attempt assertions; their acknowledgement is the durable record of what a worker did. |

Ordinary work-item mutations, research writes, knowledge projections, and
reads are deliberately not wired: they are re-issuable by their authors, and
their loss is bounded by the next barrier on the same store.

### D4. The barrier belongs at the acknowledgement layer, not inside the fence primitives

Wiring `SyncDurable` inside `internal/store/fence.go`'s step functions broke
the ten-process conformance harness: a TRUNCATE checkpoint takes an exclusive
position on the WAL, and ten workers each claiming plus checkpointing
exceeded `busy_timeout`, surfacing as escaped-busy outcomes in roughly 40% of
runs. The barrier is an obligation of the party that acknowledges — the
agent-layer caller or the dispatch boundary that owns the outermost commit —
not of the internal primitive. The conformance harness, its pacing, its
threshold, and its population authority are unchanged.

### D5. Schema migrations are excluded

Migrations at open are idempotent and re-run on the next open; a lost
migration record heals itself rather than corrupting. They carry no
acknowledgement to an external caller.

## Consequences

- Acknowledged grants, approvals, workflow dispatch and terminal transitions,
  cross-authority step claims, and worker evidence survive power loss.
- Ordinary writes gain a bounded-loss window no wider than the gap to the
  next consequential boundary on the same store — in practice, seconds.
- `SyncDurable` adds one full checkpoint per consequential operation. At the
  accepted production envelope (below 0.1 writes/second, CD-0011 calibration),
  the WAL is small and the checkpoint is negligible; it does not run on the
  ordinary path the conformance harness measures.
- A `SyncDurable` failure means *committed but not yet durable*: the caller
  receives a retry-safe typed failure, and retrying the sync is idempotent.
- A separate process holding a long read transaction can make a checkpoint
  report busy; that surfaces as the same retry-safe failure rather than
  blocking indefinitely.

## Rejected alternatives

**`synchronous=FULL` everywhere (issue #189 option 1).** Rejected on
measurement: no pacing/target coupling tested on `ubuntu-latest` yields a
dependable required check (quiet floor ~183 ms; storage-storm runs to 533 ms
against the accepted 100 ms target). Durability was bought with a flaky gate
on the runner class the required check uses.

**Per-transaction pragma flipping (FULL inside a pinned connection).**
Rejected: the pragma is connection-scoped, not transaction-scoped, so a
concurrent ordinary writer on the same pooled connection could silently
commit under the wrong mode; the restore path adds failure modes; and the
checkpoint delivers the same guarantee with none of that machinery.

**Retain NORMAL and write acknowledged-write loss into law (option 2).**
Rejected as strictly weaker: it gives up real durability for authority
operations that this mechanism delivers without changing the ordinary path.

**Sync inside the fence primitives.** Rejected on evidence (D4): it
destabilizes the conformance harness and locates the obligation below the
layer that acknowledges.

## Verification

- `internal/store/durable.go` exposes `SyncDurable`; `internal/store/durable_test.go`
  pins the per-connection pragma behaviour, the checkpoint primitive, the
  WAL-truncation barrier after `PersistGrant`, post-barrier store usability,
  and that a barrier failure surfaces to the caller.
- Every D3 family carries `SyncDurable` after its outermost commit; the
  WAL-truncation test fails if a Family A site drops it.
- `gofmt -l .`, `go vet ./...`, `bin/oc-test targeted -- ./internal/store`,
  `./internal/agent`, `./cmd/concord`, `check-tx-scope.py`,
  `check-store-boundary.py`, `check-json.py`, `check-public-content.py` pass.
- `docs/concord-knowledge-index.v1.json` records CD-0050 once.

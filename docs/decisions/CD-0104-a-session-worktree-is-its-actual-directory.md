# CD-0104: A session's worktree is its actual directory, and Concord stores no session binding

- **Status:** Accepted
- **Date:** 2026-09-03
- **Scope:** Every stored copy of which session holds which worktree; the
  bootstrap launch record; the in-session retarget, release, and takeover
  operations; the `work_start` failure contract; issues #736, #737, #742, #749
- **Approval:** The operator approved this record in-session on 2026-09-03 and
  directed that it land with the deletion it describes.
- **Related:** CD-0088, CD-0092, CD-0093, CD-0096, CD-0098, CD-0103, issues
  #736, #737, #742, #749
- **Amends:** CD-0096 at Context, D1, D3 Take over, D5, and D6; CD-0088 D4 at
  its launch-record clause; CD-0098 at its `session-prepare` preservation clause
- **Preserves:** CD-0096 D2 (identity over input), D3 Inspect, Verify, and
  Destroy, and D4; every CD-0098 clause on the move and its read-back

## Context

Concord held two stored copies of one fact: which session holds which
worktree.

The first was the bootstrap launch record, the `launch_*` columns on
`bootstrap_operations`. CD-0088 introduced it when `work_start` spawned a child
session and needed to tell a live child from a lost one. CD-0098 replaced the
spawn with a move of the calling session. Migrations 66 and 68 dropped the
process and model columns that lost their meaning; the state machine stayed.

The second was `session_worktree_targets`, the effective target CD-0096 D1
introduced. Its Context stated the premise: "The host process directory cannot
change, because the host process keeps the working directory it started with."
The session could not move, so a separate binding had to say where Concord
operations resolve. CD-0098 moved the session. The premise no longer held.

Neither copy had a reader outside its own maintenance. Authorization resolves
the Project and the main-worktree flag from the calling directory through git
at every call (`internal/agent/authority.go`, `ProjectResolver`), and never read
either table. The retarget, release, and takeover operations existed to keep
the second copy true.

A stored copy of a host fact drifts. Issue #737: a launch row left at
`prepared` with an empty session made every takeover refuse, and the refusal
was deliberately not override-takeable. Issue #749: a replay that met that row
fell through every branch of the prepare state table, with spawn and rollback
both refused. Issue #742: any failure after the work item and worktree existed
left a `partial` outcome that no typed operation could clear, because reconcile
reads only terminal work. Issue #736: the lease-held refusal, a transient
exclusion that recorded nothing, was typed as an operation to reconcile.

The four are one defect. Each guard read a stored copy, and the copy was
stale.

The operator store showed the shape at scale: no `approve_contract`,
`start_execution`, or `dispatch_worker` had ever succeeded on it, and sixteen
worktrees sat on disk for work that was finished or never started.

## Decision

### D1. The session's worktree is the directory it runs in

Concord stores no binding from a session to a worktree. The directory a
session runs in is the directory the host reports for it, read per call from
the tool context the host supplies. No table, column, or operation records a
copy of that fact ahead of the host.

The `launch_*` columns on `bootstrap_operations` and the
`session_worktree_targets` table are dropped. `worktree_retarget`,
`worktree_release`, and `worktree_takeover` are removed from the agent tool
surface. `session-record` and `work-bootstrap-rollback` are removed from the
CLI. `session-prepare` keeps its verification and its boot-packet derivation
and records nothing.

### D2. Concord stores what Concord owns

`work_items`, `bootstrap_operations` (the derived identity and the operation
journal), `worktree_claims`, `worktree_entries`, and `worktree_verify_leases`
stay. Each records a fact Concord decides: which work item exists, which
worktree was claimed for it, which command holds an exclusive lease. None
records a fact the host or git decides.

### D3. `work_start` replays to convergence

Every step of `work_start` is idempotent on the request's derived identity.
`work-bootstrap` derives the work item and the worktree path from the request
digest and replays the same operation on the same `idempotency_key`.
`session-prepare` verifies and derives without recording. The host's move is a
no-op when the session already runs at the destination. The read-back asks
the host where the session runs.

No step records intent ahead of its effect, so there is no partial state. A
failure at any step returns a typed refusal with `effect_state: none` and
`retry_same_request`, carrying the identity of what exists. A replay under the
same key adopts what exists and runs the rest. The `partial` outcome is removed
from the `work_start` contract.

### D4. Transient exclusion is `resource_busy`

A refusal that recorded nothing and that a retry may clear is
`resource_busy`, coupled to `retry_same_request`. The verify-lease refusal maps
to it. `operation_conflict` stays coupled to `reconcile_operation` and keeps
its meaning: an operation may have taken effect and its outcome must be
established.

### D5. One-session-per-worktree is not a stored guarantee

The one-holder index on `session_worktree_targets` was the only rule refusing
a second session a binding on one work item's worktree. It is removed with the
table. Two sessions may run in one worktree. The verify lease refuses two
commands on one tree, and nothing refuses two editors, which is the guarantee a
shell gives.

## Consequences

- Issues #736, #737, #742, and #749 close by removal of the copies that
  drifted, not by repair of the guards that read them.
- `work_start` has one success shape and one refusal shape. An operator who
  meets a refusal replays the same key.
- CD-0096 D1's retarget operation is gone. A session that starts in the default
  checkout and needs implementation authority calls `work_start`, which moves
  it. CD-0092 D2's refusal from the main checkout is unchanged.
- The continuity projection re-pins held verify leases only. The
  `expected_target_version` pin is removed from `concord_work_trace.continuity`.
- Migration 69 is breaking. A binary that defines schema version 68 refuses a
  database at 69, which is the compatibility floor CD-0082 names.
- `worktree_audit`, `worktree_reclaim`, `worktree_destroy`, `worktree_inspect`,
  and `worktree_verify` are unchanged: each resolves its subject through the
  claim and the entry, which Concord owns.

## Rejected alternatives

**Complete the launch state table.** Add the missing edge for `prepared` with
a live owner, and a sweep for rows whose owner died. Rejected because it
repairs a copy that has no reader. Every edge added is a new way for the copy
to disagree with the host.

**Key the main-checkout allowlist on bootstrap state.** Let authorization
admit repair operations when the work item's bootstrap is partial. Rejected
because it makes the authority boundary read the copy too. The refusal from the
main checkout is correct as stated, and a repair that needs it relaxed is a
repair of the wrong thing.

**Fold the live holders into `session_worktree_targets` before dropping the
launch columns.** Rejected because both copies go. There is no target table to
fold into.

**Keep one-holder on the worktree entry.** Move the unique-holder rule from the
session binding to `worktree_entries`. Rejected here and recorded as the open
question it is: a rule that refuses a second session must know which sessions
are live, and the host owns that. If it returns, it returns as a host query at
the moment of refusal, not as a stored column.

## Verification

- `internal/store.TestMigration59PreservesPopulatedBootstrapOperation`
  migrates a populated `bootstrap_operations` row through 69 and reads it back.
- `adapter/opencode/concord.test.ts#work start replays to convergence after an
  interrupted step` interrupts `session-prepare`, asserts a `retry_same_request`
  refusal with `effect_state: none` carrying the work identity, replays the
  same key, and asserts `ok`.
- `adapter/opencode/concord.test.ts#work start leaves a resumable claim when
  the move is refused` asserts no compensation runs.
- `internal/agent.TestRetrySafeStoreKindsNeverCoupleToReconcile` proves no
  store kind proposing `retry_same_request` maps to a public kind coupled to
  `reconcile_operation`. It was red before D4 and green after.
- `internal/agent.TestWorktreeVerifyConcurrentLeaseRefusesTyped` asserts the
  lease-held refusal is `resource_busy` with `retry_same_request`.
- `internal/store.TestContinuityRePinsHeldLease` and
  `internal/agent.TestContinuityDispatchRePinsHeldLease` assert the continuity
  projection re-pins held leases and carries no target.
- `python3 scripts/generate-agent-contracts.py --check` is clean with 62
  operations.
- `python3 scripts/check-doc-contract.py`, `python3 scripts/check-json.py`,
  `python3 scripts/check-doc-links.py`, `python3 scripts/check-knowledge-closure.py`,
  and `python3 scripts/check-knowledge-index.py` pass on this record.

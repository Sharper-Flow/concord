# CD-0185: The bulk reclaim report pages before it exceeds the result envelope

- **Status:** Accepted
- **Date:** 2026-09-27
- **Scope:** What a worktree_audit_reclaim pass reports when its result would
  exceed the agent result envelope, and the paging of the worktree_audit read
- **Amends:** CD-0179 D5 at its report bound
- **Preserves:** CD-0179 D1, D2, D3, D4, and D6 in full; the limit as the only
  bound on reclaim attempts; every gate a reclaim runs
- **Related:** CD-0105, CD-0118, CD-0132, CD-0162, CD-0181
- **Approval:** The operator accepted the work item contract that carries this
  record on 2026-09-27.

## Context

A worktree_audit_reclaim pass commits each reclaim in its own transaction, and
then builds one result for the whole pass. The result carries one row per
attempt with absolute paths and detail text, plus every report-only
classification row. On v11.35.1 one pass committed 37 reclaims and then failed
to deliver its report: the serialized envelope exceeded the 51200-byte result
cap, and the call returned limit_exceeded with effect_state possible. The
caller had to rerun with a smaller limit to learn what the pass had done.

The changed-reference cap has a bound with an accounting notice. The byte cap
had none. The worktree_audit read, the pass's named route back into the
classification, carried its own limit and no cursor, so rows past one page
were unreachable.

A report the envelope cannot carry is an uncertain effect by another name. The
caller cannot tell what committed, and the report-only rows hold the facts the
next pass needs.

## Decision

### D1. The pass result fits the result envelope before the pass reports success

The planner compacts the result before the idempotency record stores it. Every
committed and refused attempt stays reported with its outcome, version, and
refusal kind. The free-text detail of an attempt row drops only when the byte
budget requires it, from the last row backward, and under an omission notice.
A replay under the same idempotency key returns the same compacted result.

### D2. Report-only rows page through the worktree_audit read

The report-only rows the inline result does not carry are not dropped. The
worktree_audit read pages its deterministic classification with a signed
cursor, and walking the pages reaches every classified row exactly once. The
reclaim result carries an omission notice with the explicit count of the paged
rows. The read is already the pass's named next intent.

### D3. The limit stays the only bound on attempts

The limit bounds reclaim attempts only (CD-0179 D5). No byte budget narrows
how many rows a pass attempts. Every gate a reclaim runs is unchanged
(CD-0179 D6).

## Alternatives considered

- Bound the batch by the result budget at admission. Rejected: the limit owns
  attempt bounding (CD-0179 D5), a byte bound would make the batch size depend
  on path length, and a smaller batch still owes the caller the same report.
- Refuse the pass when its result exceeds the envelope. Rejected: the
  reclaims have committed, and a refusal after the effect is the
  uncertain-effect state this record removes.
- Compact attempt rows to identifiers only. Rejected: outcome, version, and
  refusal kind are the facts a caller reconciles against, so the record keeps
  them on every row.
- Raise the envelope cap for this one operation. Rejected: CD-0132 holds the
  envelope law identical at the producer and the adapter, so a per-operation
  cap moves the wall instead of removing it.

## Consequences

A pass over a large population reports ok with every attempt accounted and a
counted pointer to the rest of the classification. The caller pages the
worktree_audit read to reach the rows the inline result left out, and no
classified row becomes unreachable. A result no compaction can shrink below
the cap still refuses typed through the existing path, with the committed
references riding the failure, so the honest answer survives the worst case.

The cursor binds to the query that minted it and to the Concord event
watermark. A recorded state change between pages refuses the stale cursor. A
git or filesystem change that no event records does not move the watermark, so
a walk across such a change can skip or repeat a row. The next pass
reclassifies from the current state, so the error does not persist.

## Verification

- `go test ./internal/agent/ -run 'TestAuditReclaim|TestWorktreeAudit|TestCompactAuditReclaim'`
  proves a pass over a population past the envelope cap reports ok, a replay
  returns the same bounded result, the read pages the complete classification,
  and the byte-budget stages keep every attempt row.
- `go test ./internal/store/ -run TestWorktreeAudit` proves the cursor pages
  the classification and refuses a cursor that names no position in it.
- `python3 scripts/check-doc-contract.py` passes over the registered corpus
  with this record on the current profile.
- `python3 scripts/check-knowledge-closure.py` and
  `python3 scripts/check-knowledge-index.py` prove this record is registered
  and no unprocessed document remains.

# CD-0190: CD-0179 D1 and D4 release at the verified vacate landing

- **Status:** Accepted
- **Date:** 2026-09-28
- **Scope:** The release clause of CD-0179 D1 and the release order of
  CD-0179 D4, and the main-checkout admission of the session_vacate replay
  that completes an unconfirmed landing;
  [Concord (CON) issue 430](https://linear.app/sharper-flow/issue/CON-430)
- **Amends:** CD-0179 D1 at its first sentence ("A verified landing of a
  session, or a session_vacate of that session, therefore proves every other
  occupancy row of the same session stale, in any work item, and releases all
  of them."), and CD-0179 D4 at its sentence ("The adapter then runs
  session_vacate, which releases the occupancy rows, and moves the session to
  the derived destination.")
- **Preserves:** CD-0179 D2, D3, D5, and D6 in full, every other D1 and D4
  sentence, CD-0178 D4's content gates, and CD-0092 D1's capability-scoped
  main-checkout refusal
- **Related:** CD-0092, CD-0096, CD-0120, CD-0178, CD-0179,
  [Concord (CON) issue 430](https://linear.app/sharper-flow/issue/CON-430)
- **Approval:** The operator accepted the work item CON-430 contract that
  carries this record on 2026-09-28.

## Context

CD-0179 D1 releases every occupancy row of a session inside the session_vacate
commit, and D4 orders the terminal transition as a vacate that releases and a
move that follows. The core commits the vacate before the adapter moves the
host session. The commit is durable, and the move is a host operation that can
refuse.

A refused move, a destination mismatch, or an unreadable landing then leaves a
live session in the worktree it asked to leave, with its occupancy rows
already released. CD-0096 D3 reads a worktree whose occupancy rows are gone as
empty and removable, so the removal gate can remove a directory a live session
still runs in.

The store comment on the vacate resolution already said the release waits for
the landing. The code released at the commit. The written law and the shipped
release order name different moments, and the unsafe one is the shipped one.

## Decision

### D1. The vacate commit records the relocation request and releases nothing

session_vacate appends one `work.session_vacated` event of payload version 2
that names the work item, the Project, the session, the source directory, and
the core-derived registered main checkout, and records no landing. The commit
leaves every occupancy row standing.

CD-0179 D1 says: "A verified landing of a session, or a session_vacate of that
session, therefore proves every other occupancy row of the same session stale,
in any work item, and releases all of them." That sentence reads instead: the
release a vacate produces happens at its verified landing, which the
adapter-only vacate-landing verb records after the host readback names the
committed destination. A recorded version 1 event still releases on fold, so a
projection rebuild from an existing log performs the same releases it
performed when the writer recorded it.

### D2. The verified vacate landing releases the session's rows in one transaction

The adapter moves the host session to the derived destination, reads the
session directory back, and records the landing through the adapter-only
vacate-landing verb only when the readback names the registered main checkout
the committed request names. The landing event's fold releases every occupancy
row of that session, in any work item, in the transaction that records the
landing. A landing anywhere else refuses, records nothing, and leaves the
source occupancy standing.

### D3. Every refusal after the committed request reports a possible effect with a working recovery

The relocation request stands once the core commits it, so every adapter
refusal past the commit reports an `effect_state` of possible. The recovery is
the replay: session_vacate called again from the verified destination resolves
to the committed request, appends nothing, and the readback-verified landing
records through the same verb. A move that landed without a confirmed landing
recovers this way, so no stale row strands the session from claiming other
work.

The replay resolves a pending request only: a payload version 2 request with
no recorded landing after it. A request its recorded landing already
completed, or a version 1 request that released on fold, refuses the replay
and appends nothing. The session holds no stale row from such a request. A
replay that resolved one could append a landing that releases rows a later
claim still holds.

### D4. The replay resolves from the registered main checkout

CD-0092 D1 refuses an operation on the main checkout when its operations can
write into the repository checkout or claim implementation worktrees. The
replayed session_vacate writes neither: it resolves the pending request,
returns the committed target, and appends no event, and the landing verb owns
the release. A replay of a completed request refuses, appends no event, and
leaves the landing verb the only release owner.

CD-0179 D4 says: "The adapter then runs session_vacate, which releases the
occupancy rows, and moves the session to the derived destination." That
sentence reads instead: the adapter runs session_vacate, which records the
relocation request and leaves the occupancy rows standing. The adapter then
moves the session to the derived destination, verifies the landing by
readback, and records the landing, which releases the occupancy rows. A failed
vacate or move never undoes the transition, and the session can replay
session_vacate from the verified destination.

## Alternatives considered

- Record the landing in the core vacate commit, the version 1 payload shape.
  Rejected: the core cannot prove the host move, so a refused move, a
  destination mismatch, or an unreadable landing recorded a landing that never
  happened and released rows under a live session.
- Release the rows when the adapter move call returns. Rejected: the move call
  is the host's report, not proof. The readback is the proof, and the release
  must share one transaction with the evidence that earns it.
- Serve the replay from a post-landing occupancy record at the main checkout.
  Rejected: the registered main checkout holds no worktree row, so the replay
  target must come from the committed request, and the release stays with the
  landing verb the readback gates.
- Reword CD-0179 in place without a new record. Rejected: accepted law
  changes through an explicit amendment record that carries its own authority
  and approval.

## Consequences

A refused vacate move leaves a live session in a worktree whose occupancy rows
stand, so the CD-0096 D3 removal gate never reads that worktree as empty. A
session whose move landed without a recorded landing replays session_vacate
from the verified destination and recovers without operator help.

The release waits on the adapter's readback, so a session whose host process
ends between the move and the landing keeps its rows until the replay, a
verified landing of any work item, or a lease-set rule releases them. The
recorded vacate count stays the ordinal of relocation requests, not confirmed
moves, and the recorded landing count is the ordinal of confirmed releases.

Projections rebuilt from event logs recorded before this record perform the
same releases they performed when recorded, because a recorded version 1 event
still releases on fold.

## Verification

- `go test ./internal/store/ -run SessionVacate` proves the version 2 request
  records no landing and leaves occupancy standing, a recorded version 1
  event still releases on fold, the landing releases the session's rows in
  one transaction, and the replay target resolves to the committed request.
  The same run proves the replay of a completed or version 1 request refuses
  with no new landing.
- `go test ./internal/agent/ -run SessionVacate` proves the replay from the
  verified destination resolves the pending request and appends nothing, and
  a main-checkout vacate with no committed request still refuses.
- `bun test adapter/opencode/session-vacate-move.test.ts` proves every
  refusal past the commit reports `effect_state` possible, and a replay whose
  session already sits at the destination skips the move and records the
  landing.
- `bun test adapter/opencode/session-vacate-reoccupy.test.ts` proves the
  landed-but-unconfirmed move recovers on replay and releases the row.
- `python3 scripts/check-doc-contract.py` proves this record carries the
  current decision outline and passes the writing rules.
- `python3 scripts/check-knowledge-index.py` and
  `python3 scripts/check-knowledge-closure.py` prove this record registers
  with a current content hash and no unprocessed document remains.
- `python3 scripts/check-cd-allocation.py --no-fetch` proves the CD-0190
  identifier allocates once.

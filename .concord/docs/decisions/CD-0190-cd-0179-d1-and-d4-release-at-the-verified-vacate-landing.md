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

The landing binds to the pending request it completes. The core records a
landing only while the latest committed request for that work item and session
stands with no recorded landing after it, and only a version 2 request can
stand pending. A request its recorded landing already completed replays as
already recorded with no event while the session holds no rows. Once a later
claim's occupancy rows stand, the same landing call refuses and releases
nothing: those rows belong to the later claim, whose own verified landing or
vacate releases them.

### D3. Every refusal after the committed request reports a possible effect with a working recovery

The relocation request stands once the core commits it, so every adapter
refusal past the commit reports an `effect_state` of possible. The recovery is
the replay: session_vacate called again re-reads the committed request's
state and resolves it, appends nothing, and the readback-verified landing
records through the same verb. The replay resolves from the verified
destination, or from the source worktree the pending request names while the
session still runs there, so a commit whose host move never ran and a retry
after an adapter restart recover the same way. The re-read does not key to
the idempotency record: a retry under a new idempotency key resolves the
same still-pending request from its source worktree and appends nothing. A
request the recorded landing already completed resolves no pending request
there, so a re-occupied worktree records its own new relocation request. A
move that landed without a
confirmed landing recovers this way, so no stale row strands the session from
claiming other work. A successful core answer the adapter cannot read
classifies with the same recovery: the request stands, and the state-driven
replay resolves it from wherever the session sits. A thrown runner error
classifies with the same recovery once the core process started: an abort or
a timeout kills a running core, and a spawn failure names a process that
started, so the core may have committed before it died and the refusal
reports the possible effect with the replay. A missing binary started
nothing, so it keeps the no-effect refusal. The same recovery replaces the
generic reconciliation on every refusal the adapter would aim at one: a
manifest-skew response the self-heal cannot adopt, and a post-approval
response the adapter cannot read. session_vacate names no
work item, so no generic reconciliation can drive; the adapter remembers
nothing the core did not return.

The adapter decides a move from the host session directory it reads back,
never from the request's tool context. A stale tool context can name the
registered main checkout while the host session still sits in the source
worktree, and the readback-first move still reaches the destination from
there, so a mismatch refusal never advertises a retry the request cannot
complete.

The adapter keeps the committed destination of a post-commit refusal for the
session: a later session_vacate first moves the host session to the remembered
destination and resolves the core call from it, so the replay and the
readback-verified landing run from wherever the host session sits. The
pre-move runs only while the session holds no claimed worktree: when the host
readback names the claimed worktree a confirmed or refused move armed, the
session genuinely occupies claimed work, the adapter leaves it there, and the
state-driven replay refuses with the later-claim recovery instead of moving
the host out of work it holds. A retry without the remembered destination (an
adapter restart) resolves its Project from the directory the session sits in
and refuses before the replay; that refusal reports contact_operator, never a
retry the session cannot reach.

The replay resolves a pending request only: a payload version 2 request with
no recorded landing after it. The replay re-reads the committed state on every
call, so the idempotency response cache never answers a vacate replay from the
recorded success. A request its recorded landing already completed
resolves as a completed replay with no event while the session holds no
occupancy rows, so an uncertain landing result recovers; once a later claim's
occupancy rows stand, the same replay refuses and appends nothing, because the
rows belong to the later claim, whose own verified landing or vacate releases
them. A version 1 request that released on fold refuses the replay and appends
nothing.

### D4. The replay resolves from the registered main checkout

CD-0092 D1 refuses an operation on the main checkout when its operations can
write into the repository checkout or claim implementation worktrees. The
replayed session_vacate writes neither: it resolves the pending request,
returns the committed target, and appends no event, and the landing verb owns
the release. A replay of a completed request appends no event and leaves the
landing verb the only release owner: it resolves while the session holds no
rows, and refuses once a later claim's rows stand.

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
  The same run proves the replay of a completed request resolves while the
  session holds no rows and refuses once a later claim's occupancy row
  stands, a landing call against a completed request refuses while that row
  stands, and a version 1 request refuses the replay.
- `go test ./internal/agent/ -run SessionVacate` proves the replay from the
  verified destination resolves the pending request and appends nothing, and
  a main-checkout vacate with no committed request still refuses. The same
  run proves a replay of a completed request resolves with no event while no
  rows stand and refuses once a later claim's rows stand, the same-key
  replay after a later claim refuses with effect none and leaves the later
  claim's row standing with no landing recorded, and the replay from a
  pending request's source worktree resolves the request and appends
  nothing.
- `bun test adapter/opencode/session-vacate-move.test.ts` proves every
  refusal past the commit reports `effect_state` possible, a replay whose
  session already sits at the destination skips the move and records the
  landing, a landing output failure never asserts the landing is not
  recorded, a retry with no remembered destination reports
  contact_operator instead of the unreachable retry, and an unreadable
  successful core answer classifies with the state-driven replay recovery
  and remembers nothing the core did not return. The same run proves a
  stale tool context that names the registered main checkout while the
  host readback names the source worktree still moves, records the
  landing, and releases the row, on the first call and on its retry. The
  same run proves the same stale context against a readback outside every
  registered Project: the retry moves the host session to the remembered
  destination, the replay appends nothing, and the verified landing
  releases the row. It
  also proves a thrown runner timeout on session_vacate reports the
  possible effect with the same replay recovery.
- `bun test adapter/opencode/session-vacate-reoccupy.test.ts` proves the
  landed-but-unconfirmed move recovers on replay and releases the row, and a
  readback outside every registered Project recovers through the remembered
  destination: the retry moves the host session there, the replay appends
  nothing, and the verified landing releases the row. The same run proves an
  unreadable ok answer recovers through the same-key replay from the source
  worktree, and a same-key replay after a later claim refuses with effect
  none without moving the host session out of claimed work, while the later
  claim's own vacate releases its row.
- `python3 scripts/check-doc-contract.py` proves this record carries the
  current decision outline and passes the writing rules.
- `python3 scripts/check-knowledge-index.py` and
  `python3 scripts/check-knowledge-closure.py` prove this record registers
  with a current content hash and no unprocessed document remains.
- `python3 scripts/check-cd-allocation.py --no-fetch` proves the CD-0190
  identifier allocates once.

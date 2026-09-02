# CD-0103: The move route offers no turn boundary, and the record says so

- **Status:** Accepted
- **Date:** 2026-09-02
- **Scope:** The turn-end clause of CD-0098 D3; issue #710
- **Approval:** The operator approved this amendment in-session on 2026-09-02
  and directed that it land with the move-session transport repair.
- **Related:** CD-0096, CD-0098, issues #710, #714, #724
- **Amends:** CD-0098 D3 at its turn-end clause
- **Preserves:** Every other CD-0098 clause, including the clean-checkout
  precondition, the no-carry rule, and the read-back assertion

## Context

CD-0098 D3 states that the retarget "waits for the current turn to end, because
a move during a turn splits one turn across two directories".

The host route provides no such wait. `moveSession` in OpenCode 1.18.26 updates
the session location, publishes the moved event, and resolves. The HTTP handler
answers 204 immediately after. There is no turn, busy, idle, queue, or lock
handling anywhere on that path.

The outcome CD-0098 wanted still holds, for a different reason. A running turn
continues against the paths it already resolved, so the move takes effect from
the next turn. That is a property of when paths are read, not a guarantee the
route offers.

A record that describes a mechanism which does not exist is worse than one that
describes nothing. A later change that depends on the stated ordering — one
that reads a path late in a turn, for example — would be authorized by a clause
nothing enforces.

## Decision

### D1. The record states the observable outcome and its real cause

CD-0098 D3's turn-end sentence is replaced. The retarget takes effect from the
next turn because a running turn resolves its paths before the move lands, not
because the route waits for a boundary.

This is an observation of host behavior, not a contract the host offers. It was
verified against OpenCode 1.18.26. A host that resolved paths later in a turn
would break it, and Concord would observe that as a turn acting on two
directories rather than as a refusal.

### D2. Concord adds no wait of its own

The adapter does not wait for a turn boundary before the move, and does not
poll for one. The route exposes no boundary to wait on, so any wait Concord
added would be a mechanism it invented and then described as the host's.

## Consequences

- CD-0098 D3 keeps its preconditions and its read-back assertion. Only the
  turn-end justification changes.
- The stated behavior now carries the host version it was verified against, so
  a future host change has something to contradict.
- Concord depends on an unstated host property. That dependency is now visible
  in the record instead of hidden behind a guarantee no one offers.

## Rejected alternatives

**Leave D3 as written.** Rejected because the clause authorizes reasoning that
nothing enforces, and the next change to rely on it would be correct by the
record and wrong in fact.

**Add a wait to the adapter.** Rejected because the route exposes no boundary
to wait on. Polling for one would invent a mechanism rather than describe the
one that exists.

**Ask the host for a turn-aware move.** Rejected as out of scope. Concord
records what this host does; a host change is a separate negotiation, and the
observable outcome is already the one Concord needs.

## Verification

- `docs/decisions/CD-0098-work-start-retargets-the-running-session.md` D3 no
  longer claims a wait, and names this record as its amendment.
- The adapter contains no wait, sleep, or poll on the retarget path, which the
  CD-0098 retarget tests exercise unchanged.
- `python3 scripts/check-doc-contract.py`, `python3 scripts/check-json.py`,
  `python3 scripts/check-doc-links.py`, `python3 scripts/check-knowledge-index.py`,
  and `python3 scripts/check-cd-allocation.py` pass.

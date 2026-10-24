# CD-0103: The move route offers no turn boundary, and the record says so

- **Status:** Accepted
- **Date:** 2026-09-02
- **Scope:** The turn-end clause of CD-0098 D3; issue #710
- **Approval:** The operator approved this amendment in-session on 2026-09-02
  and directed that it land with the move-session transport repair.
- **Related:** CD-0096, CD-0098, issues #710, #714, #724
- **Preserves:** Every other CD-0098 clause, including the clean-checkout
  precondition, the no-carry rule, and the read-back assertion
- **Approval:** 2026-10-17. Coordinator approval under the operator's delegation
  for [Concord (CON) issue 966](https://linear.app/sharper-flow/issue/CON-966).

## Context

The host route does not wait for the current turn to end. `moveSession` in
OpenCode 1.18.26 updates
the session location, publishes the moved event, and resolves. The HTTP handler
answers 204 immediately after. There is no turn, busy, idle, queue, or lock
handling anywhere on that path.

A running turn continues against the paths it already resolved, so the move takes effect from
the next turn. That is a property of when paths are read, not a guarantee the
route offers.

A record that describes a mechanism which does not exist is worse than one that
describes nothing. A later change that depends on the stated ordering — one
that reads a path late in a turn, for example — would be authorized by a clause
nothing enforces.

## Decision

### D1. The record states the observable outcome and its real cause

The retarget takes effect from the next turn because a running turn resolves
its paths before the move lands, not
because the route waits for a boundary.

This is an observation of host behavior, not a contract the host offers. It was
verified against OpenCode 1.18.26. A host that resolved paths later in a turn
would break it, and Concord would observe that as a turn acting on two
directories rather than as a refusal.

### D2. Concord adds no wait of its own

The adapter does not wait for a turn boundary before the move, and does not
poll for one. The route exposes no boundary to wait on, so any wait Concord
added would be a mechanism it invented and then described as the host's.

After an accepted move, a refusal whose executing tool context has not landed may schedule one host-owned continuation of the original work-start request in the same session after its current run ends. The continuation uses the native host prompt route and needs no operator message. It neither reports work-start success nor proves placement. The ordinary placement check must verify the new tool context before success or worker dispatch. It adds no relaunch, polling for placement, alternative executor, or automatic retry cycle.

## Consequences

- CD-0098 D3's preconditions and read-back assertion govern the move.
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

- The adapter contains no wait, sleep, or poll on the retarget path, which the
  CD-0098 retarget tests exercise unchanged.
- `python3 scripts/check-doc-contract.py`, `python3 scripts/check-json.py`,
  `python3 scripts/check-doc-links.py`, `python3 scripts/check-knowledge-index.py`,
  and `python3 scripts/check-cd-allocation.py` pass.

# CD-0036: Deleting a pinned law marks dependent contracts stale

- **Status:** Accepted
- **Date:** 2026-08-17
- **Scope:** Product-law evolution; workflow contracts; stale-work refusal
- **Approval:** Operator approval for CD-0036
- **Related:** CD-0006 D5/D10/R3, CD-0012, CD-0013, CD-0015,
  CD-0016, CD-0027, CD-0033

## Context

CD-0015 binds workflow contracts to accepted law IDs. An ID alone does not say
which contents the approval consumed, so a contract cannot tell an edit to its
law from the text it was approved against. A change that requires every
existing consumer to stop must also stop them inside workflow authority,
without Concord claiming to control the host process.

## Decision

### D1. Law revision identity

Contract approval pins exactly one content digest for each law ID in the
contract's `spec_mandate`. A pinned law whose accepted digest has changed is a
compatible amendment: continuity reports the pinned and current digests, and
only the obligations whose meaning changed are reassessed ([CD-0242 D7](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d7-a-revision-invalidates-only-what-depended-on-it), [CD-0242 D10](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d10-knowledge-is-an-input-and-a-law-edit-changes-only-what-it-changes)).

### D2. Amendment and replacement

A compatible amendment keeps the law ID and states the current rule in place,
without amendment dates, override instructions, or change history. A change
that requires existing consumers to reconsider their contract deletes the old
record and, when a rule replaces it, publishes that rule under a new law ID.
No record keeps a superseded status, a successor, or a supersedes relation.
The author of the change makes this choice in the Git delta; its approval
follows CD-0243 D2. Agents do not infer compatibility as workflow authority.

### D3. Staleness scoped to dependents

An active contract is stale exactly when a law it pinned no longer exists in
the current accepted law projection. No grandfather path exists.

The stale check runs inside the transaction that owns each authoritative
workflow action. It blocks new execution claims, checkpoints, evidence binding,
Review outcome recording, verdicts, premise confirmation, and completion.
Read-only inspection remains available.

The closed recovery paths are:

1. use the `supersede_contract` action to atomically supply and install a
   fully validated successor contract whose mandate resolves wholly to current
   accepted law; or
2. cancel or supersede the work item.

The workflow that makes the deletion receives no bypass. Its approved
`law_modifies` mandate authorizes the Git delta; afterwards it recovers like
every other consumer before binding final evidence and completing. This
preserves CD-0006 D10 without creating a privileged writer.

### D4. Authority stop, not process preemption

Concord does not kill OpenCode sessions or arbitrary host processes. Native
execution evidence may record what happened after deletion, but the stale
workflow cannot accept that result into Product truth. Native Task remains
host-owned and supplies no workflow authority.

### D5. One committed order

Live contract approval validates current Git-derived laws and captures their
revision pins as current rows in the same SQLite transaction; no approval
event, folding, or replay exists ([CD-0242 D2](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)).

Every later mutating preflight compares the active contract with the current
law projection inside its owning `BEGIN IMMEDIATE` transaction. A deletion and
a stale result therefore have one committed order across processes: a result
that commits first preceded the deletion; a result that observes it refuses.
Recovery installs the successor and marks the prior active contract superseded
inside that same transaction, so no committed state exposes two active
contracts.

### D6. Typed refusal and continuity

A stale consumer receives `stale_law_revision`, naming the deleted law ID, its
pinned digest, and the closed recovery actions. Continuity exposes the same
structured state. Agents do not infer the stop from prose or a changed file.

## Rejected alternatives

- **SQLite-authored cutover:** creates a second Product-law writer and violates
  CD-0015.
- **Installation-local owner ID in Git law:** makes shared Product law depend on
  one installation's workflow memory.
- **Content-hash mismatch always blocks:** turns compatible amendments into
  breaking changes and strands valid work.
- **Recorded supersession:** keeps a retired rule in the corpus as a record of
  what replaced it; deletion already carries the stop.
- **Advisory notice or resource claim:** cannot prevent stale workflow authority
  from accepting a result.
- **Host process termination:** belongs to host session management and cannot
  prove that stale output was excluded from Product truth.

## Verification

- Contract pins and their consumers read from current rows, with no event log
  or rebuild.
- A same-ID digest change stays compatible and is reported with both digests.
- Deleting a pinned law blocks new actions and in-flight result acceptance for
  exactly its consumers.
- Contract recovery onto current law and terminal work transitions remain
  available.
- A cross-process race proves one committed order between the deletion and
  stale result acceptance.

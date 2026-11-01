---
{
  "id": "CD-0036",
  "title": "Law changes reconcile through Git, not contract freshness pins",
  "authority": {
    "tier": "derived"
  },
  "home_domain_id": "product-memory",
  "applies_to_domain_ids": [
    "workflow-engine"
  ]
}
---
# CD-0036: Law changes reconcile through Git, not contract freshness pins

- **Status:** Accepted
- **Date:** 2026-08-17
- **Scope:** Product-law evolution and workflow contracts
- **Approval:** Operator approval for CD-0036
- **Related:** CD-0006 D5/D10/R3, CD-0012, CD-0013, CD-0015,
  CD-0016, CD-0027, CD-0033

## Context

CD-0015 binds workflow contracts to accepted law IDs. Later law changes belong
to Git reconciliation, not a second freshness authority stored with each
contract.

## Decision

### D1. Law revision identity

Workflow contracts name laws by ID. They do not store or compare law or Domain
registry freshness hashes. Committed law reads retain their source, content
digest and same-operation snapshot consistency; those facts are not persistent
contract pins.

### D2. Amendment and replacement

A compatible amendment keeps the law ID and states the current rule in place,
without amendment dates, override instructions, or change history. A change
that requires existing consumers to reconsider their contract deletes the old
record and, when a rule replaces it, publishes that rule under a new law ID.
No record keeps a superseded status, a successor, or a supersedes relation.
The author of the change makes this choice in the Git delta; its approval
follows CD-0243 D2. Agents do not infer compatibility as workflow authority.

### D3. Later law changes reconcile through Git

A law amendment, deletion or Domain registry edit does not invalidate an
approved contract or stop its actions through a freshness comparison. The item
that merges later reconciles overlapping changes through Git. Actual changed
subjects and inputs retain the evidence rules of
[CD-0242 D7](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d7-a-revision-invalidates-only-what-depended-on-it).

### D4. Authority stop, not process preemption

Concord does not kill OpenCode sessions or arbitrary host processes. Native
Task remains host-owned and supplies no workflow authority. Workflow evidence,
approval and acceptance requirements remain binding.

### D5. One committed order

Live contract approval validates current Git-derived law identities, mandates
and Domain membership in the owning SQLite transaction; no approval
event, folding, or replay exists ([CD-0242 D2](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)).

No later mutating preflight compares a contract's approval-time law or registry
hashes. Exact enactment baselines, before-states and attribution remain separate
proof of the law delta the item actually delivers.

### D6. Typed refusal and continuity

Continuity resolves current readable law and Domain context by ID. It exposes
neither stale-law pin refusals nor pinned/current compatible-amendment hashes.
Missing current documents still receive the owning read's truthful absence or
typed resolution failure; no compatibility decoder or alternate pin list
replaces the retired contract data.

## Rejected alternatives

- **SQLite-authored cutover:** creates a second Product-law writer and violates
  CD-0015.
- **Installation-local owner ID in Git law:** makes shared Product law depend on
  one installation's workflow memory.
- **Contract freshness hashes:** duplicate Git reconciliation and strand valid
  work after unrelated law changes.
- **Recorded supersession:** keeps a retired rule in the corpus as a record of
  what replaced it; Git already holds that history.
- **Host process termination:** belongs to host session management and cannot
  supply workflow authority.

## Verification

- Approval and actions carry no persistent law or registry freshness hashes.
- Law amendments, deletions and unrelated registry edits do not cause a
  contract-freshness refusal.
- Current-law identity, Domain scope, evidence dependencies and exact enactment
  attribution retain their own checks.

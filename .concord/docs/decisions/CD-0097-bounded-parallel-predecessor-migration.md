# CD-0097: Allow bounded parallel predecessor migration

- **Status:** Accepted
- **Date:** 2026-09-02
- **Scope:** The migration mode for one Product moving from the installed
  predecessor (Advance) into Concord while both systems stay writable; additive
  trusted client policy expansion; issue #667
- **Approval:** The operator recorded the direction in issue #667 on 2026-09-01:
  Advance must remain installed, writable, and usable during migration; no
  freeze.
- **Related:** CD-0082, CD-0019, CD-0053, issue #667
- **Preserves:** CD-0082's demand-driven controls; the predecessor snapshot
  contract and the existing import safeguards; the rule that Concord never
  writes predecessor state
- **Extends:** The predecessor migration runbook's after-import clauses at the
  freeze and history clauses, for a Product under this mode

## Context

CD-0082 made migration and correction demand-driven. The predecessor migration
runbook implements the accepted route: harvest a validated snapshot, inventory
it, import the selected active work, then freeze the predecessor for that
Product. The runbook states the freeze plainly: nothing enforces it, because
the predecessor has no per-Project lock. The operator stops creating work
there.

Issue #667 states a concrete demand that breaks the freeze assumption. The
operator wants one Product, PokeEdge, migrated into Concord while Advance stays
installed, writable, and usable. The issue also names a second obstacle: the
supported `client-policy-update` command replaces complete capability, Product,
and Project arrays, so widening a trusted client for the migrated Product risks
removing existing authority.

Parallel use is already possible, because the freeze cannot be enforced. What
is missing is law that keeps it safe: one authority per surface, stable
identity across repeated harvests, idempotent replay with typed conflict
refusal, knowledge curation without silent loss, an additive policy expansion,
and a cutover that is an operator decision rather than a side effect.

## Decision

### D1. The mode is declared per Product and keeps the predecessor live

A bounded parallel migration mode exists for one Product at a time. The
operator declares it for a named Product. No command enters the mode
implicitly, and the mode adds no global migration state. It is the concrete
demand-driven extension CD-0082 D1 anticipates, and CD-0082's controls stay
current.

During the mode the predecessor stays installed, writable, and usable. No
Concord command freezes, disables, deletes, or writes it. The mode's safety
comes from the authority rule in D2, not from a lock the predecessor cannot
hold.

### D2. The predecessor stays the live authority for every migrated surface

While both systems are writable, the predecessor is the live authority for each
surface the mode migrates. A migrated copy in Concord is a projection that
carries provenance, not a second authority. No surface is law in both systems
at once.

The mode migrates five predecessor surfaces:

- **Specifications.** They arrive as source material. A specification becomes
  Product law only through the knowledge procedure, and an unprocessed document
  is not law regardless of origin.
- **Active work.** It imports as Concord work items under the existing import
  safeguards. The predecessor keeps the authority for these items until
  cutover.
- **Terminal history.** It stays captured in the validated snapshot. The mode
  may import it into Concord as a read-only record, and that record carries no
  workflow authority. This is the demand-driven extension of the runbook's
  default.
- **Wisdom.** It migrates as curation input under D5.
- **Reflections.** They migrate as research source material under the same
  provenance rule as wisdom.

The import route begins with a preflight inventory. It reports every included
and excluded surface with counts and capture gaps. A surface the inventory does
not enumerate does not migrate.

### D3. Identity and provenance stay stable across repeated harvests

A migrated item keeps one identity across every harvest: its predecessor
identifier. Concord stores it as structural provenance, through the existing
`external_ref: advance:<change_id>` form, the migration tag, and the
`operator:predecessor-import` actor, never as an array position or a harvest
timestamp. Two harvests of the same item resolve to the same Concord identity,
so repeated runs cannot duplicate it.

The snapshot's `producer` field keeps recording capture gaps, and a gapped
snapshot still refuses to write unless the operator passes `--accept-gaps`.

### D4. Replay is idempotent, and conflicts refuse typed

Replaying the same harvest and import applies the deterministic new effects or
returns idempotent no-op results. The existing import idempotency stays the
model.

A Concord-side change to an item the predecessor still owns is a conflict, not
an input. The replay refuses typed and names the owning source, for example
`advance:<change_id>`. The mode never merges or overwrites either system's
version, because a silent merge would decide authority that D2 assigns to the
predecessor.

### D5. Knowledge curation loses nothing silently and holds one authority

Wisdom and reflections follow the knowledge-formalization procedure. An entry
becomes a `lesson` record, or it is dropped with a recorded reason. The reason
list is part of the migration evidence, so nothing leaves the predecessor
silently.

The predecessor's knowledge state is never law in Concord. Concord knowledge
authority derives only from its own manifest records, so the two systems cannot
both claim one document as law.

### D6. Trusted client policy grows by an additive operator command

`client-policy-update` keeps its full-statement semantics: it replaces the
complete capability, Product, and Project arrays and restates the principal. A
second operator command, `client-policy-expand`, widens an active trusted
client policy by union. The additions join the stored capabilities, Product
scope, and Project scope. Every stored grant survives unchanged, and the
stored principal is untouched. Moving a client to a new principal stays on the
replace verb, because that move is a full statement, not an expansion.

An expansion that names an unknown capability, an oversized entry, or a union
past a policy bound refuses typed and leaves the stored policy unchanged.
Replaying the same expansion applies no further change. The change that
carries this record delivers the command, with tests that prove no existing
grant is narrowed or dropped. This is the surface that lets a registered
client join a migrated Product without reducing any existing authority.

### D7. Cutover is a separate operator decision

The mode ends when the operator records a cutover decision for the Product.
Until that record exists, the predecessor stays the live authority for every
migrated surface, and no mode command implies, schedules, or forces the
cutover.

Cutover changes authority, not installation. It never requires removing,
disabling, or freezing the predecessor. After cutover, the predecessor's
copies of migrated surfaces become history, and importing more predecessor
work for the Product is a new demand-driven operation under CD-0082.

## Consequences

- The runbook's freeze clause and history clause yield for a Product under
  this mode. This change updates those two clauses and leaves the default
  route's text otherwise in place.
- The expansion command lands with this record. It unblocks joining a migrated
  Product to a trusted client without reducing existing authority.
- The runtime surfaces are outstanding: mode declaration, the preflight
  inventory, terminal-history import, typed conflict refusal, and the cutover
  record. Issue #667 tracks them, and the coverage shard records that state.
- CD-0082 stays whole. The mode adds no global migration state, no shadow
  requirement, and no rollback prohibition beyond the runbook's fix-forward
  rule.

## Rejected alternatives

**Keep the freeze requirement.** Rejected because the operator's recorded
direction keeps Advance usable during migration, and the freeze has no
enforcement mechanism anyway.

**Refuse parallel migration with a typed freeze refusal.** The issue allowed
this branch. Rejected because the demand is concrete and the D2 through D5
controls bound its risks without silence or a lock.

**Bidirectional synchronization.** Rejected because Concord never writes
predecessor state. Synchronization needs two writers and produces conflicts
with no owner.

**Make Concord the authority at import time.** Rejected because the
predecessor stays writable during the mode. Two live authorities would force
silent conflict resolution, which D4 refuses.

**Hide merge semantics inside `client-policy-update`.** Rejected because its
replace semantics are an explicit full statement that callers rely on. A
replace verb that sometimes merges makes every caller's intent ambiguous, so
the expansion is its own command.

## Verification

- `client-policy-expand` unions additions into the stored policy. Every prior
  capability, Product scope, and Project scope survives, and the stored
  principal is unchanged. The change carrying this record delivers this proof.
- Replaying the same expansion applies no further change, and an expansion
  with no additions changes nothing.
- An expansion naming an unknown capability, an oversized entry, or a union
  past a policy bound refuses typed and leaves the stored policy unchanged.
- `client-policy-update` keeps full-statement semantics, including principal
  restatement.
- The preflight inventory reports included and excluded surfaces with counts
  and capture gaps (outstanding, issue #667).
- Repeated migration runs apply deterministic new effects or idempotent
  no-ops, and a conflict refuses typed naming the owning source (outstanding,
  issue #667).
- No mode command writes, freezes, disables, or deletes predecessor state
  (outstanding, issue #667).
- The knowledge record, coverage shard, and generated manifests for this
  decision are rebuilt by their owning scripts, and `scripts/check-json.py`,
  `scripts/check-doc-links.py`, `scripts/check-knowledge-closure.py`,
  `scripts/check-doc-contract.py`, and `scripts/check-knowledge-index.py` pass.

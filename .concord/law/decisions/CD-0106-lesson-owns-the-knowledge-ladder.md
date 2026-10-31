---
{
  "id": "CD-0106",
  "title": "lesson owns the knowledge ladder",
  "authority": {
    "tier": "legislated",
    "legislated_by": "work-8d7bd788b2890355460bb5b7",
    "contract_version": 1
  },
  "home_domain_id": "product-memory",
  "applies_to_domain_ids": []
}
---
# CD-0106: lesson owns the knowledge ladder

- **Status:** Accepted
- **Date:** 2026-09-03
- **Scope:** episode removal, the observation-to-lesson-to-decision ladder,
  the lesson graduation seam, and the split of external-system knowledge by
  lifetime
- **Approval:** The operator approved the knowledge-ladder contract on
  2026-09-03 after shaping in Concord work-cec2c530525706b8574ce633.
- **Related:** CD-0026, CD-0030, C8, and issue #771
- **Preserves:** Concord authority for Product law and formalized knowledge;
  the lgrep and vision lean under C8

## Context

CD-0086 kept episode as an optional external memory authority and gave
Concord one promotion seam that targeted `spec` or `decision`. Shaping on
2026-09-03 measured what that seam served.

The episode store held 7 rows. Every row was a predecessor import with source
`adv_wisdom`. The `global` namespace was empty. No row was manual and no row
was session-ingested. The cross-Product recall capability had never been used.

Every durable learning already had an owner. A procedure belongs to host
instructions or a skill. A fact about one Product belongs to a `lesson`
(CD-0026), committed in that Product's knowledge home through a reviewed
pull request. An unproven notice belongs to a work observation (CD-0030)
or, as readable history, to the retired Domain-observation store (CD-0068).
Accepted law belongs to a `decision` or a
`spec`. The seam CD-0086 defined pointed at the two kinds no agent can write,
so every promotion needed a human edit.

Knowledge about an external system belongs to the Product that learned it.
A research pack is retained context for its owning work, not the durable home
for learned usage.

## Decision

### D1. episode is removed as an external authority

Concord does not integrate, configure, or promote from episode. No Concord
surface names an episode namespace, memory id, or promotion state.

### D2. The knowledge ladder is observation, lesson, decision

Three rungs hold every durable learning, and each rung exists today.

1. An observation holds an unproven notice. `work_observations` (CD-0030)
   anchors on a work item. The retired `domain_observations` store
   (CD-0068) keeps its rows readable and records nothing new.
2. A `lesson` holds a proven Product-scoped learning. It is published
   through a reviewed pull request (CD-0026), committed as a file in the
   Product's knowledge home.
3. A `decision` or a `spec` holds accepted law. A reviewed pull request that
   lands its file under `.concord/law/` on the default branch reaches this
   rung ([law-folder-authority](../constitution/law-folder-authority.md)).

No new kind, store, or global home is added. A procedural learning that
applies to every Product belongs to host instructions or a skill, not to a
Concord record.

### D3. lesson graduation replaces the episode promotion seam

A graduation names a file revision, never a moving document. A `lesson`
that graduates to a `decision` or a `spec` is cited in the receiving law
file by repository path, commit, and SHA-256. Concord records no runtime
event for a graduation, because knowledge records carry no runtime write
surface.

### D4. Learned external-system usage belongs to the Product

Learned usage is the auth mode chosen, the endpoints used, the limits hit,
and the quirks found. It is a Product-scoped `lesson` tagged
`resource:<resource_id>`.

A record stores a locator to vendor documentation. It never stores vendor
schema or documentation content. Agents fetch the vendor side live, so
freshness is never a stored property and no stored copy can age.

## Consequences

- C8 does not move. lgrep and vision keep the product-scoping lean.
- `vertical-integration.md` covers lgrep and vision only.
- D4's no-vendor-content rule governs every lesson that cites vendor
  documentation.
- No storage, schema, or event change follows from this record.

## Rejected alternatives

**Keep episode for cross-Product memory.** Rejected because the capability
was never used, and every procedural learning already has a host-owned home.

**Add a global Concord knowledge home.** Rejected because it has no consumer
and would be a second knowledge authority beside the Product home.

**Add an integration knowledge kind.** Rejected because a tagged `lesson`
already holds learned usage.

**Keep learned usage in a research pack.** Rejected because integration
knowledge must outlive the work that found it.

## Verification

- `vertical-integration.md` names lgrep and vision only and carries no
  promotion receiving contract.
- This record sits under `.concord/law/decisions/` on the default branch.
- Repository document and link validators pass.

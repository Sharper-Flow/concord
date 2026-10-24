# CD-0106: lesson owns the knowledge ladder

- **Status:** Accepted
- **Date:** 2026-09-03
- **Scope:** episode removal, the observation-to-lesson-to-decision ladder,
  the lesson graduation seam, and the split of external-system knowledge by
  lifetime
- **Approval:** The operator approved the knowledge-ladder contract on
  2026-09-03 after shaping in Concord work-cec2c530525706b8574ce633.
- **Related:** CD-0026, CD-0030, C8, C15, and issue #771
- **Preserves:** Concord authority for Product law and formalized knowledge;
  the lgrep and vision lean under C8; the C15 managed-resource model

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

Knowledge about an external system had no stated home. `managed_resources`
models identity, owner, consumers, environments, stage, and versioned
metadata (`internal/store/schema.go:1985`), but held 0 rows and had no read
path. A research pack holds the right source fields and is deleted when its
owner archives. Nothing forbade storing vendor schema or documentation
content, and no rule marked a stored copy stale.

Concord knowledge search resolves exactly one Product home
(`internal/store/knowledge_query.go:97`, `:540`). A `lesson` in one Product
is not visible to a search from another Product.

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
   through a reviewed pull request (CD-0026), and it is committed in the
   Product's knowledge home with a manifest record.
3. A `decision` or a `spec` holds accepted law. Only the formalization
   procedure reaches this rung.

No new kind, store, or global home is added. A procedural learning that
applies to every Product belongs to host instructions or a skill, not to a
Concord record.

### D3. lesson graduation replaces the episode promotion seam

A graduation names a record version, never a moving document. A `lesson`
that graduates to a `decision` or a `spec` is cited by manifest path plus
sha256 in the receiving record's
evidence. Concord records no runtime event for a graduation, because
knowledge records carry no runtime write surface.

### D4. External-system knowledge splits by lifetime

Two facts with two lifetimes have two homes.

- **Identity** is which system, the owner Product, the consumers, the
  environments, the stage, and the vendor documentation locator, such as a
  context7 library id. Identity lives in `managed_resources` plus
  `resource_products`. The operator declares it once through the CLI. Agents
  read it. Agents do not create or revise it.
- **Learned usage** is the auth mode chosen, the endpoints used, the limits
  hit, and the quirks found. Learned usage is a `lesson` tagged
  `resource:<resource_id>`. A consumer Product reads the owner Product's
  lessons by resolving the owner through the resource, then searching that
  Product with the resource tag.

A record stores a locator to vendor documentation. It never stores vendor
schema or documentation content. Agents fetch the vendor side live, so
freshness is never a stored property and no stored copy can age.

## Consequences

- C8 does not move. lgrep and vision keep the product-scoping lean.
- `vertical-integration.md` covers lgrep and vision only.
- A read-only `resources` operation on `concord_product_view` gives agents
  the identity half of D4. That is work-d47fb36cf5552066d44dbdac, and it
  needs TS9 change evidence and operator acceptance before it ships.
- The formalization procedure gains the no-vendor-content rule from D4 and a
  validator check. That is work-1b2842005c2ca0db0211f2e1.
- `concord_knowledge.search` admits the 7 manifest kinds in its `kinds`
  filter. That is work-5686760e84b5109c137a496e.
- No storage, schema, or event change follows from this record.

## Rejected alternatives

**Keep episode for cross-Product memory.** Rejected because the capability
was never used, and every procedural learning already has a host-owned home.

**Add a global Concord knowledge home.** Rejected because it has no consumer
and would be a second knowledge authority beside the Product home.

**Add an integration knowledge kind.** Rejected because `managed_resources`
and a tagged `lesson` already cover both lifetimes.

**Give agents a write operation on `managed_resources`.** Rejected because
identity is rare and operator-declared, and `managed-resource-inventory.md`
§6 forbids an agent mutation without its own evidence.

**Keep learned usage in a research pack.** Rejected because a pack is deleted
when its owner archives, and integration knowledge must outlive the work that
found it.

## Verification

- `vertical-integration.md` names lgrep and vision only and carries no
  promotion receiving contract.
- The knowledge manifest registers this record as an accepted decision.
- Repository document, knowledge-index, and link validators pass.

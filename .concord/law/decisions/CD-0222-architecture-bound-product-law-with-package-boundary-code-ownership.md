---
{
  "id": "CD-0222",
  "title": "Architecture-bound Product law",
  "authority": {
    "tier": "derived"
  },
  "home_domain_id": "product-root:concord",
  "applies_to_domain_ids": [],
  "law_relations": [
    {
      "kind": "refines",
      "target_id": "CD-0060"
    }
  ],
  "product_wide_rationale": "Successor of the record that defines the Domain model itself. It carries what a Domain is and how law homes to one, so no partition it creates can own it."
}
---
# CD-0222: Architecture-bound Product law

- **Status:** Accepted
- **Date:** 2026-10-10
- **Scope:** Product priorities, architectural identity, law placement,
  concurrent-work contradiction control, storage boundary, global navigation
  retirement and registry participation.
- **Approval:** Operator-delegated approval A4 for [Concord (CON) issue
  899](https://linear.app/sharper-flow/issue/CON-899).
- **Related:** [CD-0060](CD-0060-domain-registry-enactment.md),
  [CD-0036](CD-0036-breaking-law-cutovers.md),
  [CD-0213](CD-0213-linear-mcp-is-the-only-planning-authority.md), [CD-0219](CD-0219-the-terminal-launcher-tui-is-retired.md),
  and [CD-0242](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md).
- **Preserves:** CD-0002 and CD-0011 storage authority and falsifiers;
  CD-0215 D2 to D8 research-pack authority; CD-0036 breaking cutovers; the
  surviving store query bounds and authority over exact Domain-detail reads;
  `applies_to_domain_ids` as optional.
[CD-0243 D6](CD-0243-four-roles-and-six-operator-approval-classes.md#d6-existing-approval-requirements-follow-these-classes)
governs D1, D4, and D5: law mandates and enactment require approval only under its six classes. It also governs the approval rule for work relations that `resolve_overlap` records. Law homes, applicability, and architecture checks remain required.

## Context

Concord already treats accepted specifications as human-enacted law. Workflow
contracts pin exact law revisions, a spec mandate limits authorized amendments,
breaking supersession marks stale exactly the contracts that consumed the
superseded revision (CD-0242 D7), and completion refuses unresolved law
conflicts. Those controls protect work that names the right law.

The Product model beneath them is inverted. `Product -> component` is documented
as the primary knowledge path, but component is an opaque grouping label rather
than a canonical governed identity. Epic, by contrast, has canonical work
identity, a living narrative, ordered entries, lifecycle rules, validators, and
an agent surface. Initiative context is structurally stronger than Product
architecture.

The work and law graphs are also separate. CD-0018 D4 leaves parallel safety
to operator judgement. Two agents can plan distinct work
against one architectural area without a shared work edge, produce clean Git
merges, and still enact contradictory Product behavior.

That is not primarily a Git or rework problem. Concord exists to keep one Product
coherent while many agents act at once. Its primary deliverable is the accepted,
pruned statement of what the Product must keep true. Work and code enact that
law; they do not outrank it.

## Decision

`resolve_overlap` records work relations under CD-0013 D11: `depends_on` and `blocks` are routine version-pinned coordination answers; `merged_into` and `supersedes` abandon or replace work identity and require operator approval only when a CD-0243 D2 class applies. Both endpoint versions, the dedicated atomic writer, and cycle rules remain required. Routine supersession grants no legislative authority.

### D1. Product law and architectural concordance serve the goals

[`goals.md`](../../docs/goals.md) records Concord's purpose, its goals, and their
measures as guidance. [`priorities.md`](../constitution/priorities.md) holds the constraints
and the conflict rules that decide between goals (Appendix A). The goals are
unranked. Appendix A settles
goal conflicts;
[CD-0230](CD-0230-the-conflict-rules-are-one-checked-table-and-an-agent-changes-law-that-is-not-serious.md)
governs applying and recording those rules.

Durable Product knowledge is not weakened or discarded. Its governing
content—specifications, decisions, and their accepted relations—is the
Product-law subject itself. Browsability, runbooks, completion narratives, and
context continuity remain required consequences of the Product-law subject and
of visibility and continuity.

An accepted record's numbered priority reference identifies its subject, not
precedence. Historical research and decision text is not rewritten. Living
constitutional and contract documents use the goal names of
[`goals.md`](../../docs/goals.md) and the constraint names of
[`priorities.md`](../constitution/priorities.md).

Product-changing work has two valid law outcomes:

- it proves that the accepted law remains unchanged and the delivered behavior
  conforms to it; or
- it carries the mandate required under
  [CD-0230](CD-0230-the-conflict-rules-are-one-checked-table-and-an-agent-changes-law-that-is-not-serious.md)
  D4 and publishes the accepted law delta before completion.

Shipping behavior with neither outcome is invalid even when tests and Git
checks pass.

### D2. Domain is the canonical Product-internal architectural identity

A **Domain** is the stable Product-internal unit that owns behavior, invariants,
architectural contracts, and the code that implements them. It is not a
cross-cutting concern. Its identity comes only from the registry, never from
paths, tags, repository names, or initiative membership.

Each Product knowledge home contains a Git-authored Domain registry. A Domain
record contains:

```text
domain_id                  # stable across every installation reading this Git home
name
purpose
parent_domain_id?          # subdomain hierarchy only
status                     # current | deprecated
architecture_relations[]
```

The Domain registry is Product law. Git is the sole writer of Domain identity,
hierarchy, and architecture relations; each read uses the registry committed at
the verified law-folder commit, and SQLite holds no copy of it
([law-folder-authority](../constitution/law-folder-authority.md)). The
resolved Product knowledge home establishes the owning Product, so every
installation that reads the same Git authority sees the same Domain IDs without
embedding installation-local IDs in shared law.

SQLite separately owns a Domain stage override. It does not create or rename
a Domain, author law, or define architecture dependencies. A Project never
becomes Domain identity. Domains never cross a Product boundary.

A cross-cutting rule belongs to the Domain that enforces it. Law applicability
names the other Domains the rule binds; it creates no additional owner.
The root Domain is not a catch-all: work whose behavior fits
a child Domain homes there, and `affected_domain_ids` names only Domains whose
law or assumptions the work can actually affect.

A Product must have a PM6 knowledge home and an accepted Domain-registry migration
delta before its first `changes_product_truth=true` contract can be approved.
Missing either prerequisite fails closed; no installation-local fallback Domain
is synthesized.

`component` is retired as an authority term. UI prose may still use “component”
as an ordinary description, but it cannot identify a second grouping entity or
own links. The canonical browse path is:

```text
Product -> active work, then Domain -> current law, evidence, decisions
```

Selecting a Product lands on its Product-scoped, non-terminal work list. That
list is the entry view. Domain and law context is the next reachable step from
the Product view. It is context for the work, not the default focus. Domains
remain the canonical home of law (D3), so the law and evidence browse still
runs through the Domain hierarchy.

Business grouping is an optional overlay that lives in Linear under CD-0213,
never a replacement for that path.

The Product registry remains the authority for Domain identity and law placement.

### D3. Every accepted law record has one architectural home

Every current specification and decision in Product law declares exactly one
`home_domain_id`. A Product-root Domain owns Product-wide constitutional law.
Law may additionally declare bounded `applies_to_domain_ids`; applicability does
not create another owner.

An owner note's law link creates no additional law home or applicability.
Workflow law reliance retains exact revision identity and source qualification.
A link to a test locates a check; it does not prove behavioral conformance.
Owner notes are not accepted law records. Typed proof anchors keep
CD-0249's closed kinds and resolution requirements.

Each law file's in-file metadata carries its law-home fields, and the Domain
registry stays an independent Git file. Exact law identity is
`(law_id, content_hash)` from CD-0036, read from the committed law folder
([law-folder-authority](../constitution/law-folder-authority.md)).

The active law graph obeys these pruning invariants:

1. every current law record is reachable through exactly one Product and one
   home Domain;
2. every `applies_to` and law-to-law relation resolves to a current or explicitly
   superseded canonical identity;
3. superseded law remains historical but is absent from the default current-law
   view;
4. unknown, dangling, duplicate, self, and forbidden-cycle relations fail the
   committed law read;
5. a breaking replacement uses a new law ID and CD-0036 supersession; a
   compatible amendment keeps its ID; and
6. semantic similarity may suggest consolidation for operator review, but no
   heuristic may delete, merge, supersede, or block law.

Pruning means one canonical current statement per enacted obligation, explicit
supersession, bounded current views, and no free-floating law. It does not mean
an agent guesses that two differently worded obligations are equivalent.

### D4. Relation validity depends on endpoint kinds and direction

Concord does not introduce one weak polymorphic `links` table. Each relation
family has its own owner, endpoint constraints, metadata, and graph rules.

| Source | Relation | Target | Structural rule |
|---|---|---|---|
| Product knowledge home | `owns` | Domain | Git registry authority; the Product's designated knowledge home binds it to one Product |
| Domain | `subdomain_of` | Domain | Git-authored; same Product; zero or one parent; acyclic |
| Domain | `depends_on` | Domain | Git-authored; same Product; directional; cites at least one current governing law ID |
| Domain | `shares_contract_with` | Domain | Git-authored; same Product; canonical symmetric pair; cites at least one current governing law ID |
| Domain | `replaces` | Domain | Git-authored; same Product; stateful `declared -> building -> coexisting -> cutover -> retired` relation |
| Law | `home` | Domain | Exactly one owner, authored in the law file's metadata |
| Law | `applies_to` | Domain | Zero or more explicit applicability targets |
| Law | `supersedes`, `refines`, `subordinate_to`, `conflicts_with` | Law | CD-0015/CD-0036 rules remain authoritative |
| Work | `home_in` | Domain | Exactly one for Product-changing work |
| Work | `impacts`, `modifies` | Domain | Closed contract fields; all modified Domains are impacted |
| Work | `governed_by`, `modifies`, `adds`, `verifies` | Law | Revision-pinned contract fields; modified/added law is operator-mandated |
| Work | `blocks`, `depends_on`, `raised_from`, `merged_into`, `supersedes` | Work | Closed work-pair grammar; `resolve_overlap` pins both contract versions |

The Work→Work row above lists the work relations this record governs. PM4 owns the full
stored work-relation vocabulary and its migration, including retained
`implements` behavior and legacy `parent` reads.

Inverse browse directions are derived; callers do not author duplicate inverse
edges. `shares_contract_with` uses one canonical ordered
pair. `replaces` is stateful and therefore has an owning relation record rather
than a pair of booleans. Generic parentage no longer represents both architecture
and initiative membership.

Implementations may use dedicated columns or typed tables. They must not flatten
this grammar into caller prose, tags, or endpoint IDs that lack real integrity
checks.

### D5. Product-changing work carries one architecture binding

Every workflow definition declares the closed boolean
`changes_product_truth`. The registry owns that classification; callers and
agents do not infer it from the work title or diff.

An approved contract with `changes_product_truth=true` carries exactly one
architecture binding:

```text
home_domain_id
affected_domain_ids[]            # non-empty and includes home
domain_modifies[]                 # subset of affected domains
domain_relation_modifies[]        # exact source/kind/target tuples
governing_law_revisions[]         # law_id + content_hash
law_modifies[]                    # subset authorized by spec_mandate
law_additions[]                   # reserved law IDs + home Domain
verification_obligations[]        # law_id + workflow obligation ID
```

The binding composes with, rather than duplicates, the outcome contract,
`spec_mandate`, `law_modifies`, and evidence policy:

- `home_domain_id` says where the work belongs;
- affected and modified Domains say where architectural assumptions may change;
- revision pins say which enacted law the operator approved;
- `law_modifies` and `law_additions` bound legislative authority; and
- verification obligations map workflow-owned proof requirements to the law they
  prove.

Every named Domain and law must resolve inside the derived Product scope. A
modified Domain must be affected. A modified or added law must be owned by an
affected Domain. Law modifications remain a subset of the approved mandate.
The contract pins law revisions; local attachment mutations use expected versions.

Workflow definitions with `changes_product_truth=false` cannot write Product law,
Domain identity, Domain relations, or Product behavior. Research may recommend a
change; an architecture spike may enact a decision only through a separate
Product-changing contract. The generic workflow type is always
`changes_product_truth=false`; a Product-changing one-off must use a registered
Product-changing type. The generic type cannot bypass this split.

One carve-out bounds that split: a `changes_product_truth=false` contract may
revise `derived` law in-contract. Its `law_modifies` may name only laws the
committed law folder carries as `derived`; every revision line stays
visible in the contract the operator approves. `legislated` law remains
reachable only through a Product-changing workflow type, and the architecture
binding stays reserved for Product-changing contracts regardless of tier.

### D6. Concurrent work reconciles through Git

Two in-progress items may change the same law, Domain, or Domain relation.
Concord does not block either item for that overlap and records no resolution
for it. The item that merges later merges main into its branch and fixes any
conflict, as with any other code change; D7 then applies to it.

### D7. Revalidation is transactional at every consequential action

Architecture and law checks run inside the `BEGIN IMMEDIATE` transaction that
owns each authoritative workflow mutation. They apply at:

- contract approval and revision;
- execution claim or dispatch;
- checkpoint and evidence binding;
- recorded candidate results and attributed verification reports;
- verdict and premise confirmation;
- any Concord-authorized merge/ship decision or acceptance of its result; and
- completion and compaction.

The preflight validates local attachment versions and relation grammar.
Contract approval and revision also validate the architecture binding against
the current Domain registry and accepted law, and pin the law revisions.

A Product law or Domain registry change merged on main never invalidates work
already in progress. After approval, no action compares the contract with the
current Domain registry, and only a CD-0036 breaking cutover compares it with
current law. Where a merged change overlaps the item's work, the item merges
main into its branch and fixes the conflict, as with any other code change.
CD-0242 D7 then decides which of the item's records the changed candidate makes
stale.

A conflicting action returns a typed refusal naming the exact Domains, laws,
work items, contract versions, and closed recovery choices. Read-only
inspection remains available.

Concord does not kill a host process or claim that a Git merge did not occur.
It refuses to admit stale or contradictory output into Product truth. A clean
textual merge, passing tests, or a model's semantic-confidence score cannot
satisfy this boundary.

CD-0036 remains the authority for a breaking law cutover. A same-ID compatible
amendment remains compatible because the operator enacted it as such.

### D8. Business grouping lives in Linear

Concord stores no business grouping. Grouping of related work, its outcome
narrative, and its progress roll-up live in Linear under CD-0213. Grouping does
not own Domain identity, law placement, dependency truth, conflict resolution,
or the primary browse path. Shared grouping never makes two work items
architecturally compatible.

CD-0215 D2–D8 govern research: independent research is ordinary research work,
embedded research stays with its owner, packs remain retained
SQLite context until explicit retirement, and durable promotion keeps its accepted destinations.
Ordinary work owns research packs; consumers bind exact revisions.

### D9. Migration reaches the clean vocabulary; compatibility is bounded

Implementation uses an explicit major migration rather than permanent aliases:

1. An operator-approved migration delta creates a Domain registry in every
   Product knowledge home. It creates one stable root Domain ID derived from that
   Git home's Product key and one Domain per existing component ID; existing IDs
   are retained. No parallel component entity is created.
2. The knowledge manifest advances compatibly. A legacy law with exactly one
   `component_id` uses that Domain as its initial home. A law with zero or several
   component IDs uses the Product-root Domain as its initial home and maps every
   legacy component ID to `applies_to_domain_ids`. The migration delta may assign
   a more precise home explicitly. The one-home invariant becomes mandatory with
   the new manifest version, whose migration must therefore produce no unhomed
   current law. New writes use `home_domain_id` and
   `applies_to_domain_ids`.
3. Historical `kind=epic` rows and `epic_entries` remain readable as recorded
   rows; recorded rows read back unchanged ([CD-0242 D2](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)).
   Recorded `component` applicability scopes read as `domain` with the retained ID.
4. New writes use neither `kind=epic` nor `kind=initiative`. Historical
   initiative work reads back unchanged and refuses new writes under CD-0213 D8.
5. The agent surface carries no Epic or Initiative tool. Removal changes the
   current generated manifest; no permanent alias or discovery surface is added.
6. Surface identity and change evidence follow
   [CD-0042 D2–D4](CD-0042-pre-go-live-single-path.md#d2-the-current-agent-surface-has-one-identity),
   [TS8 §§1–3](../specs/agent-tool-surface-evolution.md#1-current-contract), and
   [TS9 §§1–3](../specs/agent-tool-surface-measurement.md#1-pre-go-live-authority).
   CD-0024's one-time TS9 exception is not reusable. Future compatibility and
   measurement policy require
   [CD-0042 D6's first-go-live decision](CD-0042-pre-go-live-single-path.md#d6-first-go-live-must-establish-future-evolution-law).

The target state contains Domain only. Keeping “component” or
“epic” indefinitely in storage while merely changing labels is rejected because
it preserves two vocabularies and invites future authority drift.

### D10. SQLite remains the right authority for the accepted envelope

CD-0002 and CD-0011 remain unchanged. The architecture graph is represented by
Git-authoritative Domain/law records plus typed SQLite projections, local
stage overrides, workflow contracts, and work relations inside the existing
single-host authority. The single committed SQLite write order supports the
transactional operational checks in D7 without becoming a second architecture
writer.

No PostgreSQL service, graph database, CQRS infrastructure, persistent daemon,
generic repository layer, or second authority is introduced. CD-0011's reopening
triggers remain exact:

- an escaped `SQLITE_BUSY`, lost/duplicate effect, or invariant violation;
- queueing that materially blocks ordinary operator work;
- deployment scope that stops being one local operator installation; or
- failed backup, restore, rebuild, or upgrade evidence.

Choosing a simple substrate is not permission to simplify the Product model.
The relational schema must preserve the endpoint-specific grammar and revision
checks above.

### D11. This decision authorizes follow-up implementation, not a runtime claim

The constitutional change lands before runtime work. Implementation proceeds in
dependency order:

1. Git-authored Domain identity/relations, local attachment projections, manifest evolution, and compatibility
   rebuilds;
2. architecture-bound workflow contracts;
3. consequential-boundary revalidation and adversarial multi-process scenarios;
4. Initiative event/projection/surface migration with the current TS8/TS9
   deterministic evidence;
5. Product -> Domain read surfaces and current-law pruning views; and
6. removal of obsolete component/Epic write compatibility on the current primary
   path under
   [CD-0042 D3](CD-0042-pre-go-live-single-path.md#d3-superseded-development-paths-are-deleted).

Each implementation slice requires its own public issue, tests, migration proof,
and TS8/TS9 evidence for the current manifest.

### D12. Global Domain navigation is retired

No global file inventory, path-glob registry, fingerprint collection, generated
card set, catalog binding, or relationship visualization is required. The
navigation corpus, its generator, and its validators are deleted with their
consumers. Small owner notes explain responsibilities
where code does not. A note need not name a Domain. A claim that names a Domain
still passes the existing scope boundary.

A Domain participates in no graph by requirement. A relation records governed
architecture. `scripts/check-domain-registry.py` retains checks for law
ownership, Domain hierarchy, applicability, relation endpoints, and current
governing laws. It has no navigation companion, unresolved-path ratchet,
participation advisory, or participation strict-mode gate.
`applies_to_domain_ids` stays optional and cannot create another law home.

The launcher display clauses have no subject under CD-0219. The surviving
store query contracts keep their bounds and authority; this record adds no
read or display obligation to the kept session entry route. Exact
Domain-detail reads remain advisory and retain canonical relation tuples and
their governing law.

Shared context preserves explicit immutable readings and findings. Dispatch,
authorization, independent review and verification, retries, session tenancy,
research-pack reliance, and cross-repository recovery do not depend on a
graph. Product law approval does not authorize live-state cutover.

## Required conformance

1. A Product law or Domain registry change merged on main after approval leaves
   an in-progress item's contract, records, and evidence admissible.
2. Every current spec and decision resolves to one home Domain; dangling or
   cross-Product Domain references fail the manifest transactionally.
3. `depends_on` and `shares_contract_with` Domain relations refuse without a
   current governing law ID; subdomain cycles refuse.
4. Legacy component/Epic rows and manifests read back through the canonical
   Domain vocabulary without rewriting (CD-0242 D2).
5. Current clients cannot write legacy component/Epic forms. Mismatched manifest
   digests fail before a domain effect under
   [TS8 §3](../specs/agent-tool-surface-evolution.md#3-digest-failure-and-static-loading),
   without a client compatibility window.
6. The accepted ten-process SQLite correctness, latency-population, backup,
   restore, and rebuild evidence remains green.

## Alternatives considered

- **Keep Product law at Priority 6.** Rejected: it makes the Product's governing
  deliverable subordinate to the machinery that serves it.
- **Keep component as an untyped grouping lens.** Rejected: tags and strings
  cannot own law, foreign keys, revision checks, or conflict decisions.
- **Use Epic as the primary architecture container.** Rejected: initiative
  membership expresses business context, not architectural compatibility.
- **Retain Epic/component internally forever and relabel the UI.** Rejected:
  permanent dual vocabulary preserves the conceptual error and creates two
  future authorities.
- **Block concurrent work that shares a write until a recorded resolution.**
  Rejected: the later merge reconciles through Git like any other code change,
  and the block stranded in-progress work.
- **Let semantic similarity author conflicts.** Rejected: probabilistic inference
  may assist review but cannot own persistence or workflow authority.
- **Reserve a Domain with a long-lived lock.** Rejected: it serializes compatible
  work, strands locks after process loss, and mistakes execution exclusion for a
  Product-law decision.
- **Adopt a graph database or PostgreSQL now.** Rejected: the accepted one-host
  workload and invariants do not justify a new authority or service.

- Keep the participation and adoption obligations beside the deletion.
  Rejected: the deleted corpus and cards were their only consumers, and dead
  obligations invite silent narrowing.
- Keep a coarse navigation mode. Rejected: the deleted readers served every
  mode, so a coarse mode has no reader.

## Consequences

### Positive

- Concord's structure now matches its purpose: many agents act through one
  accepted Product law and architecture graph.
- Specifications become pruned, owned, browsable Product deliverables rather
  than documents attached to work after the fact.
- Business grouping stays in Linear without becoming architectural authority.
- SQLite's total local write order is used as a correctness mechanism rather than
  hidden behind a speculative abstraction.

### Cost

- Knowledge-manifest, workflow-contract, projection, and launcher migrations are major and
  require compatibility evidence. Agent-surface changes follow TS8 and TS9.
- The TS9 exception is not reusable.
- Living-document updates are controlled; historical records remain intact.

- The navigation corpus, generated cards, catalogs, joins, and their
  generators are deleted. No graph, glob map, or owner-note generator replaces
  them.

## Amendment map

- **CD-0006 D5/D10/R3:** architecture binding and D7 revalidation accompany
  law conflict, spec mandate, and cross-workflow impact checks.
- **CD-0015:** law relations are Git-authored and closed; every law has one
  Domain home and architecture-bound workflow use.
- **CD-0024:** historical surface-version policy is closed by
  [CD-0042](CD-0042-pre-go-live-single-path.md); TS8 and TS9 own current surface
  identity and change evidence. D3's one-time exception is not reusable.
- **CD-0036:** preserved. Breaking law supersession marks stale exactly the
  contracts that consumed the superseded revision and composes with D7
  (CD-0242 D7).
- **CD-0002/CD-0011:** SQLite is the sole local live-state
  authority under the existing falsifiers.

## Verification

- Committed law-folder snapshot validation proves record identity, folder
  placement, and the relation graph.
- `python3 scripts/test-check-domain-registry.py` proves the referential checks without a participation rule.
- `internal/store.TestConsequentialBoundariesAdmitASharedDomainPeer` and `internal/store.TestExecutionClaimAdmitsASharedDomainPeer` prove D6: a shared-Domain peer blocks no boundary.
- `internal/store.TestResolveOverlapRecordsRoutineRelations` and `internal/store.TestResolveOverlapMergeAndSupersessionAreAtomic` prove the `resolve_overlap` relation writer.
- `internal/store.TestLawFolderSnapshotRefusesInvalidLawFiles` proves the
  carried one-home invariant: applicability without exactly one declared home
  refuses at the committed read.
- `internal/store.TestValidateWorkContextDomainTxRefusals` proves declared context, scope, and rationale bounds without an inventory.
- `internal/store.TestReadWorkContextOverflowRefusesReadNotRetention` proves context bounds refuse instead of truncate.
- `go test ./internal/store -run 'TestDispatchAdmitsPacketConsumingLatestCheckpoint'` and its companion witness selectors prove coordination without global navigation artifacts.
- `internal/store.TestAuthorizedFailedRetrySurvivesRegistryChange` proves a Domain registry change leaves in-progress work admitted (D7).

# CD-0041: Architecture-bound Product law is Concord's primary coordination model

- **Status:** Accepted
- **Date:** 2026-08-18
- **Scope:** Product priorities, architectural identity, law placement, concurrent-work
  contradiction control, Initiative shape, and storage boundary; issue #192
- **Approval:** Operator approved a Domain-first architecture graph, Epic's replacement
  by secondary Initiative context, and retention of SQLite under its accepted falsifiers
  on 2026-08-18
- **Related:** CD-0002, CD-0006 D5/D10/R3, CD-0009 D1/D1a, CD-0011,
  CD-0012, CD-0013 D7, CD-0015, CD-0018 D4, CD-0024, CD-0035,
  CD-0036
- **Amends:** CD-0006 D5/D10/R3, CD-0009 D1/D1a, CD-0015,
  CD-0024 D1/D2/D4, and PM6/PM7 historical-scope vocabulary
- **Preserves:** CD-0002 and CD-0011 storage authority and falsifiers;
  CD-0009 D2–D8 research-pack authority; CD-0036 breaking-law cutovers
- **Supersedes:** Epic as Concord's current Product-facing initiative term and
  `Product -> component` as the authoritative Product-knowledge path

## Context

Concord already treats accepted specifications as human-enacted law. Workflow
contracts pin exact law revisions, a spec mandate limits authorized amendments,
breaking supersession strictly quiesces old consumers, and completion refuses
unresolved law conflicts. Those controls protect work that names the right law.

The Product model beneath them is inverted. `Product -> component` is documented
as the primary knowledge path, but component is an opaque grouping label rather
than a canonical governed identity. Epic, by contrast, has canonical work
identity, a living narrative, ordered entries, lifecycle rules, validators, and
an agent surface. Initiative context is structurally stronger than Product
architecture.

The work, impact, and law graphs are also separate. CD-0006 R3 blocks only a
declared hard dependency plus a breaking completion notice. CD-0018 D4 therefore
leaves parallel safety to operator judgement. Two agents can plan distinct work
against one architectural area without a shared work edge, produce clean Git
merges, and still enact contradictory Product behavior.

That is not primarily a Git or rework problem. Concord exists to keep one Product
coherent while many agents act at once. Its primary deliverable is the accepted,
pruned statement of what the Product must keep true. Work and code enact that
law; they do not outrank it.

## Decision

### D1. Product law and architectural concordance are Priority 1

The six ranked priorities become:

1. **Product law and architectural concordance**
2. **Data governance, reliability, and safe evolution**
3. **Quality governance**
4. **Visibility and continuity**
5. **Planning and coordination**
6. **Workflow versatility**

The former Priority 6, durable Product knowledge, is not weakened or discarded.
Its governing content—specifications, decisions, and their accepted relations—
moves into Priority 1. Browsability, runbooks, completion narratives, and context
continuity remain required consequences of Priorities 1 and 4. Workflow variety
remains last because no workflow convenience may compromise Product law, data
safety, quality, visibility, or Product-level coordination.

Accepted records written before this decision keep their meaning by **priority
name**, not by their old number. Historical research and decision text is not
rewritten. Living constitutional and contract documents use the new names and
numbers.

Product-changing work has two valid law outcomes:

- it proves that the accepted law remains unchanged and the delivered behavior
  conforms to it; or
- it carries an operator-approved mandate and publishes the accepted law delta
  before completion.

Shipping behavior with neither outcome is invalid even when tests and Git checks
pass.

### D2. Domain is the canonical Product-internal architectural identity

A **Domain** is the stable Product-internal unit that owns behavior, invariants,
and architectural contracts. It may describe a capability, subsystem, service,
or cross-cutting architectural concern. It is not inferred from paths, tags,
repository names, or initiative membership.

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
hierarchy, and architecture relations; SQLite holds a transactional derived
projection keyed by the registry's content hash. The resolved Product knowledge
home establishes the owning Product, so every installation that reads the same
Git authority sees the same Domain IDs without embedding installation-local IDs
in shared law.

SQLite separately owns local attachments: a Domain stage override plus typed
Domain→Project and Domain→managed-resource links. Those links map one shared
architecture to the installation's Product census; they do not create or rename
a Domain, author law, or define architecture dependencies. A Project or resource
never becomes Domain identity. Domains never cross a Product boundary.

A Product-wide or cross-cutting concern is a root Domain, not work with no
architectural home. The root Domain is not a catch-all: work whose behavior fits
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
Product -> Domain -> current law, active work, evidence, decisions, resources
```

Initiatives are optional overlays on that path, never its replacement.

### D3. Every accepted law record has one architectural home

Every current specification and decision in Product law declares exactly one
`home_domain_id`. A Product-root Domain owns Product-wide constitutional law.
Law may additionally declare bounded `applies_to_domain_ids`; applicability does
not create another owner.

The Git knowledge manifest remains the sole writer. Its next compatible schema
adds the Domain registry, law-home fields, and exact law identity
`(law_id, content_hash)` from CD-0036. The SQLite knowledge index remains a
derived projection.

The active law graph obeys these pruning invariants:

1. every current law record is reachable through exactly one Product and one
   home Domain;
2. every `applies_to` and law-to-law relation resolves to a current or explicitly
   superseded canonical identity;
3. superseded law remains historical but is absent from the default current-law
   view;
4. unknown, dangling, duplicate, self, and forbidden-cycle relations fail the
   manifest before projection replacement;
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
| Product knowledge home | `owns` | Domain | Git registry authority; SQLite projects one local Product FK |
| Domain | `subdomain_of` | Domain | Git-authored; same Product; zero or one parent; acyclic |
| Domain | `depends_on` | Domain | Git-authored; same Product; directional; cites at least one current governing law ID |
| Domain | `shares_contract_with` | Domain | Git-authored; same Product; canonical symmetric pair; cites at least one current governing law ID |
| Domain | `replaces` | Domain | Git-authored; same Product; stateful `declared -> building -> coexisting -> cutover -> retired` relation |
| Domain | `implemented_by` | Project | SQLite-local attachment; same Product membership; `primary | supporting`; zero or one primary |
| Domain | `uses` | Managed resource | SQLite-local attachment; Product must own or consume the C15 resource; bounded purpose/environment metadata |
| Law | `home` | Domain | Exactly one owner, authored in the Git manifest |
| Law | `applies_to` | Domain | Zero or more explicit applicability targets |
| Law | `supersedes`, `refines`, `subordinate_to`, `conflicts_with` | Law | CD-0015/CD-0036 rules remain authoritative |
| Work | `home_in` | Domain | Exactly one for Product-changing work |
| Work | `impacts`, `modifies` | Domain | Closed contract fields; all modified Domains are impacted |
| Work | `governed_by`, `modifies`, `adds`, `verifies` | Law | Revision-pinned contract fields; modified/added law is operator-mandated |
| Work | `blocks`, `depends_on`, `raised_from`, `compatible_with`, `merged_into`, `supersedes` | Work | Closed work-pair grammar; overlap resolutions pin both contract versions |
| Initiative | `includes` | Work | Same Product; ordered entry with explicit requiredness; no architecture authority |

The Work→Work row lists CD-0041's overlap-relevant members. PM4 owns the full
stored work-relation vocabulary and its migration, including retained
`implements` behavior and legacy `parent` reads.

Inverse browse directions are derived; callers do not author duplicate inverse
edges. `shares_contract_with` and `compatible_with` use one canonical ordered
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
`spec_mandate`, `law_modifies`, workflow conditions, and evidence policy:

- `home_domain_id` says where the work belongs;
- affected and modified Domains say where architectural assumptions may change;
- revision pins say which enacted law the operator approved;
- `law_modifies` and `law_additions` bound legislative authority; and
- verification obligations map workflow-owned proof requirements to the law they
  prove.

Every named Domain and law must resolve inside the derived Product scope. A
modified Domain must be affected. A modified or added law must be owned by an
affected Domain. Law modifications remain a subset of the approved mandate.
The contract pins Domain-registry content hashes and law revisions; local
attachment mutations and all overlap resolutions use expected versions.

Workflow definitions with `changes_product_truth=false` cannot write Product law,
Domain identity, Domain relations, or Product behavior. Research may recommend a
change; an architecture spike may enact a decision only through a separate
Product-changing contract. The generic workflow type is always
`changes_product_truth=false`; a Product-changing one-off must use a registered
Product-changing type. The generic type cannot bypass this split.

### D6. Architectural overlap is explicit before concurrent work proceeds

For each nonterminal Product-changing work item, Concord derives its active
architecture footprint from the approved contract. Two active items overlap when
their `affected_domain_ids` intersect. Exact intersections in law additions or
modifications, Domain modifications, or Domain-relation modifications are marked
as write overlaps; broader same-Domain intersections remain architecture overlaps.

Both forms require a resolution before both items may hold execution authority.
The closed resolutions are:

| Resolution | Record |
|---|---|
| Both contracts are compatible | symmetric `compatible_with`, operator-approved |
| One must land and revalidate before the other | hard `depends_on` / `blocks` |
| The intents become one surviving item | `merged_into` |
| One intent replaces the other | `supersedes` |
| One item ends | terminal lifecycle state |

An overlap resolution records both work IDs and both approved contract versions.
Changing either contract makes the resolution stale. An agent may explain an
overlap and draft a resolution; only the operator may approve compatibility,
merger, supersession, or a sequence that changes Product scope.

This is deliberately conservative. Same-Domain work may be compatible, but
Concord records that conclusion rather than assuming it. The cost is a bounded
decision; the avoided failure is two agents independently enacting contradictory
Product truth.

### D7. Revalidation is transactional at every consequential action

Architecture and law checks run inside the `BEGIN IMMEDIATE` transaction that
owns each authoritative workflow mutation. They apply at:

- contract approval and revision;
- execution claim or dispatch;
- checkpoint and evidence binding;
- worker/native-result acceptance;
- verdict and premise confirmation;
- any Concord-authorized merge/ship decision or acceptance of its result; and
- completion and compaction.

The preflight validates current Domain-registry hashes, local attachment versions,
law revision pins,
relation grammar, active overlaps, and overlap resolutions. A conflicting action
returns a typed refusal naming the exact Domains, laws, work items, contract
versions, and closed recovery choices. Read-only inspection remains available.

Concord does not kill a host process or claim that a Git merge did not occur.
It refuses to admit stale or contradictory output into Product truth. A clean
textual merge, passing tests, or a model's semantic-confidence score cannot
satisfy this boundary.

CD-0036 remains the authority for a breaking law cutover. A same-ID compatible
amendment remains compatible because the operator enacted it as such. New
architecture overlap created after execution began blocks the affected item's
next authoritative mutation until resolved; it is not grandfathered.

### D8. Initiative replaces Epic as secondary business context

The Product-facing term is **Initiative**. An Initiative is a finite,
Product-scoped work item with a living narrative and ordered entries whose
requiredness is explicit. It may span Domains and Projects in its Product.
Entries retain independent workflow, authorization, recovery, architecture
binding, and terminal state.

Initiative exists to answer business and outcome questions: why related work is
being pursued, which outcomes are required, and how progress rolls up. It does
not own Domain identity, law placement, dependency truth, conflict resolution,
or the primary browse path. Initiative membership never makes two work items
architecturally compatible.

CD-0009 D2–D8 remain unchanged: independent research is ordinary research work,
embedded research stays with its owner, active packs remain retention-bounded
SQLite context, and durable promotion keeps its accepted destinations. Every
reference in those clauses to Epic ownership now means Initiative ownership.

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
3. Historical `kind=epic`, `epic.*` events, and `epic_entries` remain readable
   through versioned upcasters and rebuild logic. History is not rewritten.
   Legacy active-research and knowledge applicability scopes upcast
   `component` to `domain` with the retained ID.
4. New writes use `kind=initiative`, `initiative.*`, and the Initiative entry
   projection.
5. The agent surface moves from `concord_work_epic` to
   `concord_work_initiative` at the next major contract version. Old clients
   receive the existing TS8 deprecation window; no permanent alias or discovery
   surface is added.
6. CD-0024's one-time TS9 exception is not reusable. The normal supported-model
   runner and evidence artifact must exist before this model-visible major ships.

The target state contains Domain and Initiative only. Keeping “component” or
“epic” indefinitely in storage while merely changing labels is rejected because
it preserves two vocabularies and invites future authority drift.

### D10. SQLite remains the right authority for the accepted envelope

CD-0002 and CD-0011 remain unchanged. The architecture graph is represented by
Git-authoritative Domain/law records plus typed SQLite projections, local
attachments, workflow contracts, and overlap resolutions inside the existing
single-host authority. The single committed SQLite write order supports the
transactional operational checks in D7 without becoming a second architecture
writer.

No PostgreSQL service, graph database, CQRS infrastructure, persistent daemon,
generic repository layer, or second authority is introduced. CD-0011's reopening
triggers remain exact:

- an escaped `SQLITE_BUSY`, lost/duplicate effect, or invariant violation;
- sustained user-visible write latency above the accepted target on the
  repeatable acceptance population;
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
2. architecture-bound workflow contracts and overlap/resolution projections;
3. consequential-boundary revalidation and adversarial multi-process scenarios;
4. Initiative event/projection/surface migration with the required TS9 baseline;
5. Product -> Domain read surfaces and current-law pruning views; and
6. removal of expired component/Epic write compatibility after the bounded window.

Each implementation slice requires its own public issue, tests, migration proof,
and accepted surface/version evidence. This decision does not mark any runtime
floor item satisfied.

## Required conformance

1. Two work items with overlapping affected Domains cannot both enter execution
   without a current, version-pinned resolution.
2. Subject to D7's compatible-amendment rule, two contracts approved against the same law revision and landed sequentially
   cannot admit a jointly contradictory second result after the first changes the
   governing law or architecture graph.
3. Revising either contract invalidates an existing compatibility decision.
4. A new overlap introduced while one item is executing blocks its next
   checkpoint/result acceptance without killing the host process.
5. Every current spec and decision resolves to one home Domain; dangling or
   cross-Product Domain references fail the manifest transactionally.
6. `depends_on` and `shares_contract_with` Domain relations refuse without a
   current governing law ID; subdomain cycles refuse.
7. Initiative ordering and requiredness survive migration, while Initiative
   membership alone never satisfies an architecture overlap.
8. Legacy component/Epic events and manifests rebuild to the canonical
   Domain/Initiative projection without event rewriting.
9. New clients cannot write legacy component/Epic forms after the major cutover,
   and old clients fail closed after the bounded TS8 window.
10. The accepted ten-process SQLite correctness, latency-population, backup,
    restore, and rebuild evidence remains green.

## Rejected alternatives

- **Keep Product law at Priority 6.** Rejected: it makes the Product's governing
  deliverable subordinate to the machinery that serves it.
- **Keep component as an untyped grouping lens.** Rejected: tags and strings
  cannot own law, foreign keys, revision checks, or conflict decisions.
- **Use Epic as the primary architecture container.** Rejected: initiative
  membership expresses business context, not architectural compatibility.
- **Retain Epic/component internally forever and relabel the UI.** Rejected:
  permanent dual vocabulary preserves the conceptual error and creates two
  future authorities.
- **Block only exact law-ID collisions.** Rejected: independently introduced law
  can contradict inside one Domain without sharing an ID. Same-Domain overlap
  therefore requires an explicit compatibility decision.
- **Let semantic similarity author conflicts.** Rejected: probabilistic inference
  may assist review but cannot own persistence or workflow authority.
- **Reserve a Domain with a long-lived lock.** Rejected: it serializes compatible
  work, strands locks after process loss, and mistakes execution exclusion for a
  Product-law decision.
- **Adopt a graph database or PostgreSQL now.** Rejected: the accepted one-host
  workload and invariants do not justify a new authority or service.

## Consequences

### Positive

- Concord's structure now matches its purpose: many agents act through one
  accepted Product law and architecture graph.
- Architectural overlap appears before contradictory output can enter Product
  truth, even when Git reports no textual conflict.
- Specifications become pruned, owned, browsable Product deliverables rather
  than documents attached to work after the fact.
- Business initiatives remain useful without becoming architectural authority.
- SQLite's total local write order is used as a correctness mechanism rather than
  hidden behind a speculative abstraction.

### Cost

- Same-Domain concurrent work pays an explicit compatibility or sequencing
  decision.
- Manifest, workflow-contract, projection, launcher, and agent-surface migrations
  are major and require compatibility evidence.
- The 3.0.0 Epic surface receives a bounded lifetime and cannot reuse its original
  TS9 exception.
- Existing documents that use old priority numbers, component, or Epic need a
  controlled living-document update while historical records remain intact.

## Amendment map

- **CD-0006 D5/D10/R3:** architecture binding and overlap revalidation now join
  law conflict, spec mandate, and cross-workflow impact checks.
- **CD-0009 D1/D1a:** Initiative replaces Epic and is explicitly secondary to
  Domain/law authority; D2–D8 remain binding.
- **CD-0015:** law relations remain Git-authored and closed; every law gains one
  Domain home and architecture-bound workflow use.
- **CD-0024 D1/D2/D4:** the 3.0.0 Epic surface becomes legacy and migrates through
  a normal major-version path; D3 remains historical evidence, not precedent.
- **CD-0036:** preserved. Breaking law supersession still strictly quiesces old
  consumers and composes with D7.
- **CD-0002/CD-0011:** preserved verbatim. SQLite remains sole local live-state
  authority under the existing falsifiers.

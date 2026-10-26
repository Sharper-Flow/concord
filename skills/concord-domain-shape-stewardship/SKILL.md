---
name: concord-domain-shape-stewardship
description: Initialize, migrate and reorganise, refine, or audit a Product's Concord Domain shape. Use when establishing exclusive code-root ownership, reconciling typed facts, or investigating gaps in Product knowledge.
---

# Domain-shape stewardship

Use this skill for one approved Product or Domain scope. Select **Initialize**,
**Migrate and reorganise**, **Refine**, or **Audit**. Load only the relevant
workflow after the shared guidance. The skill guides investigation; it does not
grant execution authority, change law, record approval, or replace a check.

## Canonical references

These public references do not require a Concord source checkout. An adopting
Product's similarly named local file is not a substitute for the linked source.
Use the selected Product's own accepted law for its behavior and obligations.

- [Domain shape and fact ownership](https://github.com/Sharper-Flow/concord/tree/1041537e1a9639c1c8ef909891e0bdc4100bfc89/.concord/docs/decisions/CD-0244-domains-own-code-exclusively-and-hold-typed-facts.md).
- [Skill admission, release versioning, and portability](https://github.com/Sharper-Flow/concord/tree/1041537e1a9639c1c8ef909891e0bdc4100bfc89/.concord/docs/decisions/CD-0245-concord-ships-the-skill-that-operates-its-domain-shape.md).
- [Authority and planning boundaries](https://github.com/Sharper-Flow/concord/tree/1041537e1a9639c1c8ef909891e0bdc4100bfc89/.concord/docs/managed-development-authority.md).
- [Knowledge identity and publication](https://github.com/Sharper-Flow/concord/tree/1041537e1a9639c1c8ef909891e0bdc4100bfc89/.concord/docs/concord-knowledge-index.md).
- Schemas: [facts](https://github.com/Sharper-Flow/concord/tree/1041537e1a9639c1c8ef909891e0bdc4100bfc89/contracts/domain-fact.v1.schema.json), [package roots](https://github.com/Sharper-Flow/concord/tree/1041537e1a9639c1c8ef909891e0bdc4100bfc89/contracts/package-domains.v1.schema.json), [declared tooling](https://github.com/Sharper-Flow/concord/tree/1041537e1a9639c1c8ef909891e0bdc4100bfc89/contracts/project-tooling.v1.schema.json), and [check protocol](https://github.com/Sharper-Flow/concord/tree/1041537e1a9639c1c8ef909891e0bdc4100bfc89/contracts/domain-shape-check.v1.schema.json).

Use `concord --help` and `concord domain-shape-check --help` for the installed
release's command and JSON-stdin contracts.

If a required source or operation is unavailable, name what cannot be established
and stop the dependent step. Do not invent a command, treat missing information
as an empty result, or replace the source with unreviewed local guidance.

## Shared investigation

1. Establish the selected Product, its designated knowledge home, registered
   source Projects, current Domain registry, resources, repository subject, and
   approved change scope. Preserve valid authored identities; a display key is
   not permission to change a Product or source association.
2. Inspect existing knowledge, implementation entry points, tests, operating
   configuration, and available operational evidence before proposing structure.
   Follow state writers, readers, transactions, dependencies, and failure/rebuild
   paths. A filename, declaration, or reachable test alone proves none of them.
3. Distinguish accepted requirements, observed behavior, proposals, disputed
   claims, stale evidence, and unknowns. Identify separately who owns a statement,
   its authority, where it applies, and how the consumer receives it. Inspect
   obligations inside mixed documents; neither a title nor a directory decides
   applicability. Keep reusable Concord requirements, Concord-development-only
   guidance, and the adopting Product's own requirements distinct.
4. Reconcile with canonical owners instead of copying obligations. Preserve
   accepted law, valid identities, and rationale that still explains a constraint.
   Code does not approve law. A fact with `standing: observed` grants no authority;
   `standing: law` requires current `governing_law_ids` through the existing
   law-change path. A publication hash or timestamp is not a truth verdict.
5. Read the Product's `.concord/tooling.v1.json`. Use its declared commands and
   existing Concord operations only. Missing extractors or boundary measurements
   remain unverified. Do not write a language analyser, second validator, store,
   taxonomy, per-file catalog, generated Domain cards, or approval route to make
   an investigation appear complete.

### Shape and verification

The knowledge home holds the Domain registry, `package-domains.json`,
`domain-shape-ratchet.json`, and one JSON fact per file under
`.concord/docs/knowledge/facts/`. Facts are not Markdown document records.

- A `domain` root implements one Domain's behavior and has one
  `owner_domain_id`. A `mixed` root honestly lists its Domains until restructuring.
  Historical map version `1.0` is transitional, not exclusive ownership.
- A `composition` root only wires Domains together. `test_support` holds shared
  test helpers. Neither owns behavior or a fact. Never relabel behavior to reduce
  a baseline. Roots are packages or modules, not files or globs.
- An `invariant` belongs to the Domain that enforces it, even if
  `applies_to_domain_ids` spans Domains. Its `enforced_by` anchors must resolve
  through required checks; a cited anchor does not prove a passing execution.
- A `data_object` names its resource, writer and reader Domains, and invariant
  IDs. A `state_authority` names authoritative storage, derived copies, rebuild
  routes, and disagreement behavior. A `config_coupling` names the setting,
  source, reading symbol, effect, and varying environments.
- Each fact has one owner and an owning root. Use the linked fact schema rather
  than tags or prose conventions to add fields. If a necessary claim cannot be
  represented, record the exact limitation for separate assessment.

Use these existing surfaces, within the caller's role and assignment:

- `concord_domain.shape`: supply `product_id` and exactly one of `path`,
  `domain_id`, or `subject`. Inspect ownership, matching facts, anchors, mixed
  labels, verification state, and commit/hash provenance. Selected JSON is bounded
  to 32,768 bytes; an oversized selection refuses whole, not partially.
- `concord_domain.footprint`: supply `product_id` with changed `paths`, a
  `behavior` phrase, or both. Inspect affected ownership, law, and path shapes.
  It is not permission to expand the work's scope.
- For delegated required reading, use the existing `domain_shape` source with
  `source_id`, `commit_oid`, `content_hash`, and exactly one selector. Preserve
  original packet, role, and assignment boundaries. Do not replace a held source
  with a mutable file, another Product's facts, or a newly pinned revision.
- `concord product-knowledge-home-designate`: the existing operator setup route
  takes JSON stdin with `product_id`, `project_id`, `locator_id`, and
  `expected_version`. It prepares the shape and reports filesystem effects.
  Registration or designation needs its existing authority; loading this skill
  supplies none. Do not replace a valid home merely to run setup.
- `concord domain-shape-check`: the explicit operator check takes JSON stdin
  with `product_id` and optional `checkout`. Select the exact clean worktree of
  the designated home's candidate commit. It is not a flag-based command.
  Projection declarations bind the knowledge commit; operator execution pins
  default tooling and compares the candidate ratchet with the default baseline.
  Do not widen the candidate ratchet to approve its own violations.

The `domain_shape` declaration references existing tool IDs for `boundary_check`,
`data_object_extractor`, `config_extractor`, `reference_check`, and
`required_checks`. The check supplies allowed `depends_on` edges and compares
Product observations with facts. A complete inventory is not a pass. Missing and
orphan extractor facts are findings; an absent extractor remains unverified even
when a command exits successfully. Reads never execute these commands.

## Initialize

1. Inspect first. For an unregistered Product, report what registration and home
   designation are needed through the existing operator route. For an existing
   corpus, reconcile its home and registered sources without replacing valid IDs.
2. Identify stable behavior owners from entry points and state boundaries. Propose
   only warranted Domains, their governing law, and allowed dependency relations.
   A rule spanning Domains is an invariant, not a new cross-cutting Domain.
3. After authorized setup, inspect created and updated paths. Keep existing valid
   files. Declare unrestructured behavior-bearing roots as mixed; a new Product's
   strict baseline is not permission to add violations.
4. Reconcile warranted facts in all four kinds with their actual owners, resources,
   anchors, authority, and evidence. Do not invent accepted requirements or fill
   every kind for appearances. Leave missing evidence and unknowns explicit.
5. Verify the permitted result with `concord domain-shape-check` and
   `concord_domain.shape` reads. Report both findings and unverified
   measurements. Repeat unchanged reconciliation: it should propose no duplicate
   IDs, duplicate obligations, or gratuitous edits.

## Migrate and reorganise

1. Inventory the existing corpus and code roots within the approved Product scope.
   Map current identities and obligations to the four fact kinds. Preserve law,
   observed standing, live rationale, and evidence; do not copy Concord's code
   layout or engineering conventions into another Product.
2. Trace physical transaction boundaries, shared connections, state writers,
   derived-state rebuilds, configuration reads, public callers, and allowed
   dependencies. Record what a proposed move must preserve before planning it.
3. Plan focused package/module moves toward exclusive behavior ownership. Keep
   genuine composition and test support separate; carry unresolved roots as mixed.
   Record the current ratchet and proposed reduction. Mixed is an honest interim
   state, not a completed reorganisation.
4. Execute only separately authorized repository changes. Real Product data,
   accepted-law amendments, and out-of-scope migrations need their existing
   approvals. A passing check supplies none of them.
5. After each coherent permitted group, run the Product-declared boundary and
   extractor checks through `concord domain-shape-check`, then verify path,
   Domain, and subject lookup with `concord_domain.shape`. Compare IDs, standing,
   resource authority, public behavior, and the default ratchet baseline. A moved
   path alone is not an exclusive-owner outcome.

## Refine

1. Start from the delivered change and its `concord_domain.footprint`. Inspect
   changed enforcement, data objects, state copies, configuration reads, and direct
   consumers. Check whether cited evidence still supports each affected claim.
2. Find the canonical owner of each obligation. Reconcile duplicated guidance and
   contradictions there, through existing amendment or supersession rules. Do not
   silently select a winner or promote an observation to law.
3. Update affected facts and evidence links. Prune superseded descriptive content
   only under the applicable retention/removal rules. Preserve rationale that
   still governs a decision and resolve references before proposing deletion.
4. Verify the permitted change with `concord domain-shape-check` and
   `concord_domain.shape` reads. List unresolved findings and unverified
   measurements. On unchanged inputs, a second refinement should propose no
   duplicate records or unnecessary rewrite.

## Audit

Audit the relevant knowledge population, not just its manifest. For each question,
give an evidenced answer, an explicit unknown, or a justified exclusion:

1. Why this design, and which rejected alternatives still matter?
2. Which business outcomes, customer commitments, and correctness rules govern it?
3. What is known about production load, failures, recovery, and visibility?
4. Which declared or undocumented dependencies and behavioral contracts matter?
5. Which trade-offs, migration costs, and reversibility constraints affect choices?
6. Which ownership, coordination, and operating-capacity limits affect delivery?
7. Which hazards and mitigations need evidence?
8. Which maintenance costs, temporary arrangements, and retirement conditions
   need reassessment?
9. Which claims are established, inferred, disputed, stale, or unknown?

Use actual `concord domain-shape-check` and `concord_domain.shape` results for
structural findings, and inspect supporting sources for semantic claims. A
matching metadata hash or reachable evidence path does not establish truth.
Identify contradictory obligations, unsupported claims, missing dependencies,
mixed applicability, and gaps hidden by unverified tooling.

Each actionable finding names its subject, observation, source or evidence gap,
consequence, and next investigation. Record follow-up through the Product's
existing planning authority. An audit does not mutate Product law or authorize
repair, migration, data changes, or broader investigation.

## Report the result

Keep the result in the work's existing record: selected Product/Domain and subject,
workflow, inspected population and omissions, decisions, preserved identities and
authority, permitted changes, actual check/read evidence, unknowns, and remaining
scope. Distinguish proposed, authored, checked, released, and installed states.
Do not claim that a valid shape proves sufficient context or that this skill
creates workflow evidence on its own authority.

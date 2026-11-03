---
name: concord-architecture-writing
description: Write or review a Product's durable architecture reference, Domain or cross-cutting explanations, or glossary, plan an architectural change within the six-part plan, or route a plan's lasting outputs to law, reference, or glossary at final review. Use when shaping material under .concord/docs/architecture/, starting a Product glossary, or deciding whether an architectural decision should become law.
---

# Architecture writing

Work on one selected Product and agreed change. This skill operates Concord's
knowledge structure: it carries the shapes of durable architecture reference
and the routing of a plan's lasting outputs. It supplies neither law nor
approval, adds no workflow step, gate, or check, and creates no second
decision authority.

## Canonical references

These Concord references define the knowledge and authority machinery, not the
adopting Product's behavior. Read that Product's own accepted law for its rules.

- [Skill admission and portability](https://github.com/Sharper-Flow/concord/tree/main/.concord/law/decisions/CD-0245-concord-ships-the-skill-that-operates-its-domain-shape.md).
- [Domain ownership](https://github.com/Sharper-Flow/concord/tree/main/.concord/law/decisions/CD-0222-architecture-bound-product-law-with-package-boundary-code-ownership.md):
  the registry alone owns Domain identity, hierarchy, and governed relations.
- [Product knowledge placement](https://github.com/Sharper-Flow/concord/tree/main/.concord/law/decisions/CD-0194-product-knowledge-lives-under-concord.md).
- [Architecture reference is not Product law](https://github.com/Sharper-Flow/concord/tree/main/.concord/law/decisions/CD-0251-architecture-reference-is-not-product-law.md):
  the authority relationship this skill operates, including the final-review
  routing of lasting plan outputs.

## Separate current reference from plans

Architecture reference explains the delivered system. An obligation or
governed boundary lives in Product law; Domain identity lives in the registry;
planning lives in Linear; managed execution stays in Concord. Reference links
those authorities and creates none of its own: a normative claim in reference
has its obligation in law and links to it, or is proposed through the existing
law-change route, or is removed.

Current reference excludes delivery plans, rollout sequences, backlogs,
exhaustive inventories, and research. A target design stays in its plan;
reference describes what is delivered, marks an unknown as unknown, and
reports a mismatch between law and the implemented system rather than
describing the desired state as current. A contradiction between reference
and law is resolved through the law-change route, never as a silent
amendment.

Establish the Product's Domains with `concord_domain.list` before writing.
Missing docs or a missing registry never authorize invented Domain identity,
waived law, a second knowledge home, or a bypass of workflow gates.

## Shape the reference set

Create documents lazily, when a real reader question needs one. Reuse accurate
existing documents before moving or splitting them.

### Product overview

A new documentation set starts with `.concord/docs/architecture/README.md`. An
existing useful overview keeps its path, with one pointer from the Product's
documentation index; do not create a second overview to conform to a filename.

The overview answers, in order:

1. What system is this, who uses it, and what lies outside its boundary?
2. What are its main running parts, external systems, and trust boundaries?
3. Where do the few important requests or state changes flow?
4. Which Domain references, cross-cutting explanations, and laws answer more?

Link the registry rather than copying its Domain table or relations. Name
Domains when useful to explain an interaction, using their exact registry IDs.
An explanatory flow diagram is not another governed relationship graph: label
boxes, arrows, and scope, and prefer C4 context and container views only when
they clarify an answer. A small system may use a paragraph and a list instead.

### Domain reference

Write `.concord/docs/architecture/<slug>.md` only when a Domain needs more
explanation than the overview can give; one canonical overview section suffices
for a small Domain, and the root Domain uses the Product overview rather than a
duplicate root document. State the exact `domain_id` in ordinary prose and link
the registry entry; the slug is a locator, not identity or a new metadata
schema.

Include the relevant parts of this outline, omitting empty ones:

- **Responsibility and boundary:** what it does, what it does not do, and the
  registry entry that owns the identity.
- **Main parts and seams:** interfaces, dependencies, and a few useful source
  entry points. Link package documentation for details; do not enumerate files
  or declare exclusive code ownership from paths.
- **State and runtime:** owned data, state authority, important request or
  failure flows, and deployment facts that change how the boundary works.
- **Constraints and rationale:** links to governing law, tests, or scenarios,
  and lasting decisions. Explain consequences without restating obligations.
- **Known limits:** what was not established, with links to active Linear work
  for unresolved risks. Do not copy the backlog or a delivery sequence.

### Cross-cutting concepts and glossary

A concept spanning Domains lives once under
`.concord/docs/architecture/concepts/<slug>.md`, only when an overview section
is insufficient. Name the enforcing or explanatory home Domain and link the
other affected Domains. Do not create a cross-cutting Domain or a second copy
per consumer.

Use `.concord/docs/glossary.md`, created on the first ambiguous Product term.
Each entry gives the preferred term, one or two sentences of Product meaning,
and aliases to avoid when useful. Group by Domain only where meanings differ,
and qualify those meanings rather than imposing a false universal definition.
Exclude generic programming terms, implementation details, and requirements.
Where law defines a term, link that definition instead of redefining it.

### Reading budget

Use a soft authoring target of 800 words for the overview and 1,200 words for
a Domain or concept reference. These are editorial starting points, not model
limits, code-size limits, or acceptance gates; word counts cover the document's
prose and tables, not linked documents. There is no whole-Product word ceiling
or mandatory token-count tool. A reader starts with the overview, then reads
only the relevant reference and governing law. Links do not excuse omission of
a necessary boundary or risk. Over target, remove duplicated facts first;
split at a distinct reader question only when that improves retrieval;
otherwise retain the longer file and explain its useful scope in its
introduction. No numeric budget may waive proof or law.

### Freshness and bootstrap

The owner of a change that alters a documented boundary, flow, or state
authority updates the affected reference in that change, or explains why its
description remains true. Dates alone do not prove freshness: use source links
and Git history, and no manually maintained verification stamp or refresh
ledger. Final review checks the changed claims against code and law.

Start a Product with no docs by answering one real architecture question in a
short overview, and add only the references and glossary entries later changes
actually need. With a registry, use its IDs; without one, describe observed
parts without claiming Domain identity. Documentation bootstrap neither
synthesizes a fallback Domain nor bypasses registry authority.

## Plan an architectural change

An architectural change alters a Domain boundary, a governed relation, state
authority, a cross-Domain interface or runtime flow, or a claim in current
architecture reference. Other work keeps the ordinary plan.

Keep the six-part plan from `concord-work-shaping`; add within it rather than
inventing a competing template. Each part keeps its existing duties; the right
column lists only what an architectural change adds:

| Plan part | Addition for an architectural change |
|---|---|
| Outcomes and checks | Goals, non-goals, and unanswered questions that could change the design. |
| Approach | Boundaries, state, and runtime flows affected; current versus intended behavior. |
| Decisions | Trade-offs of each choice; destination for a lasting decision, or why none is needed. |
| Touches | Domains by registry ID and the architecture reference affected; useful code locators and owners, not an exhaustive file inventory. |
| Merge split | Delivery order and the dependencies between slices. |
| Risks | Recovery where relevant, and test seams: prefer the highest existing seam that proves the outcome; name any lower-level check that adds distinct proof. |

Linear owns the problem, scope, planning choices, and sequence; the admitted
Concord record holds the bounded execution design. Link these owners; do not
mirror a planning backlog into Git, and do not make the plan a permanent
architecture reference.

## Route lasting outputs at final review

Before delivery, classify each plan decision's lasting output:

1. **An obligation or governed boundary:** enact or amend law through the
   existing law-change route. Reversibility never exempts a rule from law.
2. **Discretionary architectural rationale:** when all three hold — hard to
   reverse, surprising without context, and a real trade-off — record it as a
   law decision through the existing law-change route. Otherwise keep useful
   current explanation in the reference and leave transient choices in the work
   record; link an existing decision instead of duplicating it.
3. **Changed delivered structure:** update its canonical reference.
4. **Settled Product vocabulary:** update the glossary, or link a law
   definition.
5. **Neither lasting nor current:** leave it in the temporary work record; do
   not archive an obsolete plan into the architecture or research tree.

One decision can require both a law delta and a reference update. Review checks
that each required destination exists and matches the delivered change, or that
the work record explains why no update is needed. A work note can link the
outputs but cannot replace law or be their only durable home.

For the law-change route itself, load `concord-law-writing`; for the general
prose method, load `concord-instruction-writing`.

## Coordinate a multi-session effort

Use a Linear parent issue or Project description as a decision map: the
destination, scope, unresolved questions, dependency order, and links to the
issue that owns each choice. Keep the map an index, not another store of
decisions. Split independently decidable outcomes, not one ticket per
diagram, file, or session. Coordination sequences the work; only the operator
supplies reserved approvals.

## Verify

No new validator enforces this model; final review and existing link checks
own conformance. Over-budget prose or a Domain without its own reference file
is not a defect; a missing required routing destination is.

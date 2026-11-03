# Architecture reference and architectural plans

Research recommendation, 2026-11-03. **Not Product law or an adopted model.**
Adoption requires the operator's product-direction decision. Publication of this
report does not supply it.

## Recommendation

Keep current architecture reference separate from temporary architectural plans.
Use one short Product overview, add Domain references only where they answer a
real reader question, and keep one Product glossary. Route lasting plan outputs
to law, current reference, or the glossary before delivery. Do not create a
second decision authority, a mandatory file per Domain, or a new validator.

The benefit is a later agent finding the boundary and its reason without
recovering a completed plan. The cost is maintaining a small amount of prose
with structural changes. No measured agent-performance gain is claimed.

## Evidence and limits

Repository observations below use Concord commit
`1a08b066755903bc0b5071c8146779f559ac14b2`. The workflow's resolved law context
reported the six laws cited below as accepted at the designated knowledge home.
External practice was checked on 2026-11-03; the Pocock sources are pinned.

Concord already has architecture documentation. [Core architecture](../core-architecture.md#4-entry-points-by-responsibility)
links responsibilities to entry points while leaving details to package docs
and tests. [Codebase design](../../../skills/concord-codebase-design/SKILL.md)
covers module interfaces. [Work shaping](../../../skills/concord-work-shaping/SKILL.md)
already supplies six plan parts. The missing shared method is how to write
system-level reference and route a plan's lasting outputs, not a missing place
to store any architecture knowledge.

The Planner's cross-Product examples describe mixed current-state and delivery
documents, a file-size rule contradicting that Product's law, and an ADR outside
the law folder. These motivate investigation; they were not independently
reproduced here and are not proof of current violations in those Products.
The proposed step-plan implementation is also not treated as shipped behavior.
The report recommends routing before execution records can disappear; it does
not claim observed loss of a particular decision. Agent reliance on these
documents, retrieval cost, and maintenance cost remain unmeasured.

### Current authority constraints

- [Law-folder authority](../../law/constitution/law-folder-authority.md#authority-and-precedence)
  owns what counts as law: default-branch `.concord/law/` at the designated home.
  Branch proposals and ordinary reference documents are not law.
- [CD-0194 D1](../../law/decisions/CD-0194-product-knowledge-lives-under-concord.md#d1-the-default-placement)
  places research and reference under `.concord/docs/`;
  [CD-0200](../../law/decisions/CD-0200-product-knowledge-resolves-through-one-designated-git-home.md)
  keeps one designated knowledge home, not one copy per repository.
- [CD-0222 D2](../../law/decisions/CD-0222-architecture-bound-product-law-with-package-boundary-code-ownership.md#d2-domain-is-the-canonical-product-internal-architectural-identity)
  makes the registry the authority for Domain identity, hierarchy and relations.
  Its [D12](../../law/decisions/CD-0222-architecture-bound-product-law-with-package-boundary-code-ownership.md#d12-global-domain-navigation-is-retired)
  retires mandatory global inventories and permits small responsibility notes
  without a Domain label. A new reference model should not rebuild that corpus.
- [CD-0213 D1–D2](../../law/decisions/CD-0213-linear-mcp-is-the-only-planning-authority.md#d1-linear-is-the-only-planning-authority)
  separates Linear planning from Concord managed execution.
  [Priorities P3](../../law/constitution/priorities.md#a1-goal-by-goal-chart)
  requires the cheapest adequate enforcement, including review or guidance.

## Durable reference: shape and organisation

### Product overview

For a new documentation set, use `.concord/docs/architecture/README.md`.
An existing useful overview may keep its path, with one pointer from the
Product's documentation index. Do not create a second overview to conform to
a filename. Concord can improve `core-architecture.md` in place when adopted.

The overview answers:

1. What system is this, who uses it, and what lies outside its boundary?
2. What are its main running parts, external systems and trust boundaries?
3. Where do the few important requests or state changes flow?
4. Which Domain references, cross-cutting explanations and laws answer more?

Link the registry rather than copying its Domain table or relations. Name
Domains when useful to explain an interaction, using their exact registry IDs.
An explanatory flow diagram is not another governed relationship graph.
Use C4 context and container views when they clarify the answer; label boxes,
arrows and scope. A small system may use a paragraph and list instead.

**Basis and rejected options:** [C4](https://c4model.com/diagrams) says only useful
levels are needed, usually context and containers. Reject mandatory component
and code diagrams, exhaustive deployment maps, or a generated registry mirror.
They add maintenance without an established reader need; D12 preserves the
same boundary for Concord's navigation machinery.

### Domain reference

Use `.concord/docs/architecture/<slug>.md` when a Domain needs more explanation
than the overview can give. State its exact `domain_id` in ordinary prose and
link its registry; the slug is a locator, not identity or a new metadata schema.
One canonical section in the overview suffices for a small Domain. Create a
separate file only when it has distinct material. The root Domain uses the
Product overview, not a duplicate root document.

Each reference contains the relevant parts of this outline, omitting empty ones:

- **Responsibility and boundary:** what it does, what it does not do, and the
  registry entry that owns the identity.
- **Main parts and seams:** interfaces, dependencies, and a few useful source
  entry points. Link package documentation for details; do not enumerate files
  or declare exclusive code ownership from paths.
- **State and runtime:** owned data, state authority, important request or
  failure flows, and deployment facts that change how the boundary works.
- **Constraints and rationale:** links to governing law, tests or scenarios,
  and lasting decisions. Explain consequences without restating obligations.
- **Known limits:** what was not established, with links to active Linear work
  for unresolved risks. Do not copy the backlog or a delivery sequence.

The body describes the delivered system. Mark an unknown as unknown; link a
target design in its plan rather than mixing proposed behavior into current
reference. A law/implementation mismatch is reported explicitly, not described
as if the desired state already exists.

**Basis and rejected options:** [arc42's minimal set](https://faq.arc42.org/questions/B-4/)
suggests context, strategy, main building blocks and cross-cutting concepts.
[Its maintenance guidance](https://faq.arc42.org/questions/H-3/) prefers stable
high-level views over volatile detail. Reject an eager file for every Domain,
all twelve arc42 sections, and an unchecked Domain-to-code ownership map.
Existing package notes remain useful and need not acquire a Domain label.

### Cross-cutting concepts and glossary

A concept spanning Domains lives once under
`.concord/docs/architecture/concepts/<slug>.md`, only when an overview section
is insufficient. Name the enforcing or explanatory home Domain and link other
affected Domains. Do not create a cross-cutting Domain or a second copy per
consumer. Links to the actual law carry obligations.

Use `.concord/docs/glossary.md`, created on the first ambiguous Product term.
Each entry gives the preferred term, one or two sentences of Product meaning,
and aliases to avoid when useful. Group by Domain only when meanings differ;
qualify those meanings rather than imposing a false universal definition.
Exclude generic programming terms, implementation details and requirements.
Where law defines a term, link that definition instead of redefining it.

**Basis and rejected options:** [Pocock's glossary format][pocock-glossary]
supports concise, opinionated terms without implementation detail.
Reject a glossary per Domain and a separate context-map file by default: they
make common vocabulary harder to find and risk duplicating registry identity.
Split only for a demonstrated reading problem, keeping the Product glossary
as the index. [Diátaxis reference](https://diataxis.fr/reference/) supports
description organised around the system; architecture rationale is explanation,
not a tutorial, runbook or feature spec.

### Reading budget

Use a **soft authoring target** of 800 words for the overview and 1,200 words
for a Domain or concept reference. These are local editorial starting points,
not measured model limits, code-size limits, or acceptance gates. Word counts
include the document's prose and tables, not the contents of linked documents.
There is no whole-Product word ceiling or mandatory token-count tool.

An agent starts with the overview, then reads only the relevant reference and
governing law. Links do not excuse omission of a necessary boundary or risk.
Over target, remove duplicated facts first. Split at a distinct reader question
only when that improves retrieval; otherwise retain the longer file and explain
its useful scope in its introduction. No numeric budget may waive proof or law.

**Basis and rejected options:** [Pocock's writing method][pocock-writing]
distinguishes always-loaded context from document sprawl and treats copied
environment facts as a cache. The numerical targets above are this report's
judgment. Reject an unbounded always-loaded architecture corpus, a hard
500-line cap, and claims that a model's context capacity proves a useful reading
budget. No source reviewed established an optimal numeric budget for Concord.

### Freshness and bootstrap

The owner of a change that alters a documented boundary, flow or state authority
updates the affected reference in that change, or explains why its description
remains true. Final review checks the changed claims against code and law.
Dates alone do not prove freshness. Use source links and Git history; do not
add a manually maintained `last_verified` stamp or a parallel refresh ledger.

Start a Product with no docs by answering one real architecture question in a
short overview. Reuse existing accurate docs before moving or splitting them.
With a registry, use its IDs; without one, describe observed parts without
claiming Domain identity. Establish the knowledge home and registry through
their existing route before Domain-bound Product-changing work. Documentation
bootstrap neither synthesizes a fallback Domain nor bypasses CD-0222 D2.
Later changes add only the references and glossary entries they actually need.

**Basis and rejected options:** [arc42 B-16](https://faq.arc42.org/questions/B-16/)
separates short-lived project documents from long-lived system documentation;
[B-14](https://faq.arc42.org/questions/B-14/) favors short, current material over
a future complete set. Reject a bulk rewrite prerequisite and automatic
freshness inference from timestamps. Review and existing link validation are
adequate initially under P3. Neither a `domain_id` frontmatter checker nor a
keyword checker can establish semantic consistency. No new check is proposed.

## Architectural plans

Keep the existing six-part plan. Add the following detail within it rather
than inventing a competing template or a new workflow step:

| Existing part | Required detail for an architectural change |
|---|---|
| Outcomes and checks | Observed problem, affected readers/users, goals, non-goals, delivery-checkable outcomes, dependencies, and unanswered questions that could change the design. |
| Approach | Boundaries, state and runtime flows affected; why each new mechanism earns its place; simpler alternative; current versus intended behavior. |
| Decisions | Choice, reason, rejected alternatives and trade-offs; applicable operator reservation; destination for a lasting decision or reason no promotion is needed. |
| Touches | Interfaces, Domains, law and reference affected; useful code locators and owners, not an exhaustive file inventory. |
| Merge split | Independently green delivery slices, order and dependencies, or why the cutover is inseparable. |
| Risks | Failure modes, unresolved uncertainty, recovery where relevant, and test seams: prefer the highest existing seam that proves the outcome; name any lower-level check that adds distinct proof. |

Linear owns the problem, scope, planning choices and sequence. Concord's admitted
execution record holds the bounded execution design and contract. Link these
owners; do not mirror a planning backlog into Git or make the plan a permanent
architecture reference. Use whichever current workflow representation is
admitted; this report proposes no store fields or retained step-history model.

**Basis and rejected options:** [Pocock's to-spec][pocock-spec] contributes
non-goals, implementation trade-offs and test seams; [Google design-doc practice](https://www.industrialempathy.com/posts/design-docs-at-google/)
emphasises goals, non-goals and alternatives. The latter is a public account by
Malte Ubl, not a formal Google standard. Reject importing to-spec's tracker
labels, HTML output, or blanket ban on code paths: bounded execution needs
usable source locators. Keep methods in their existing owning skills rather
than restating every method in an architecture-writing skill.

### Lasting-output routing at final review

For each plan decision, classify the lasting output before delivery:

1. **An obligation or governed boundary:** enact or amend law through the
   existing law-change route. The ADR selection test cannot exempt a rule from
   law merely because it is reversible.
2. **Discretionary architectural rationale:** offer a law decision when all
   three hold: hard to reverse, surprising without context, and a real trade-off.
   Otherwise keep useful current explanation in the reference; leave transient
   choices in the work record. Link an existing decision instead of duplicating it.
3. **Changed delivered structure:** update its canonical reference.
4. **Settled Product vocabulary:** update the glossary, or link a law definition.
5. **Neither lasting nor current:** leave it in the temporary work record;
   do not archive its obsolete plan into the architecture or research tree.

One decision can require both a law delta and a reference update. Review checks
that each required destination exists and matches the delivered change, or that
the record explains why no update is needed. A useful work note can link the
outputs, but cannot replace law or be their only durable home.

**Basis and rejected options:** [Pocock's ADR test][pocock-adr] is a useful
filter for recording rationale, not an authority test. [MADR](https://adr.github.io/madr/)
contributes context, options and outcome; verification can link existing tests
instead of creating a second compliance system. Reject `docs/adr/`, a decision
appendix inside reference, and retention of every plan as architecture history.
Git already holds superseded reference; Linear holds planning history.

### Multi-session efforts

Use a Linear parent issue or Project description as a decision map: destination,
scope, unresolved questions, dependency order and links to the issue that owns
each choice. Keep the map an index, not another store of decisions. Split
independently decidable outcomes, not one ticket per diagram, file or session.
The RTE may sequence the work; only the operator supplies reserved approvals.

**Basis and rejected options:** [Pocock's wayfinder][pocock-wayfinder] makes the
map an index and keeps each decision in one ticket. Adapt that idea to Linear;
reject a repository map of the backlog, fixed session/token sizing, a new
tracker, or a second architecture approval board.

## Proposed relationship rules

The following is proposed adoption text, not an exception to current law.
The skill and law follow-ups can use it unchanged:

1. Product law owns obligations, governed boundaries and accepted architectural
   decisions. The Domain registry alone owns Domain identity, hierarchy and
   governed architecture relations. Architecture reference explains the
   delivered system and links those authorities; it creates none of its own.
2. A normative architecture claim has its obligation in law and links to it.
   A missing obligation is proposed through the existing law-change route or
   removed from reference. A contradiction is reported and resolved, never
   silently treated as an amendment or an implementation fact.
3. A Product keeps one canonical overview and creates Domain and cross-cutting
   references only for useful explanatory content. Referencing a Domain uses
   registry identity; descriptive source links do not confer code ownership.
   Existing useful document paths and small owner notes need not be replaced.
4. Current reference excludes delivery plans, rollout sequences, backlogs,
   exhaustive inventories and research. Planning lives in Linear; managed
   execution stays in Concord. Research belongs under `.concord/docs/research/`
   when it has a durable research result, not merely an expired plan.
5. A structural change updates the affected reference in the same change.
   Final review checks law consistency and routes lasting plan outputs to law,
   current reference and glossary before delivery. When no update is needed,
   the work record states why. Existing approval and publication duties remain.
6. References are read on demand. Reading budgets guide editing, not acceptance
   or authority. Missing docs or registry never authorize invented Domain
   identity, waived law, a second knowledge home, or a bypass of workflow gates.

## Adoption boundary and source reuse

This report changes no law, instruction, test or runtime behavior. The intended
adoption is a portable architecture-writing method plus the relationship law,
with final-review routing placed in the existing review method. It is not a
mandate to retrofit every Product immediately. Those implementation issues own
their changes after operator approval; cross-Product migration remains separate.

The main risk is prose growing into a second architecture authority. Lazy
creation, source pointers and review reduce that risk, but have not been
measured in a Product pilot. A stricter validator or lower reading budget needs
observed failures first. The model deliberately rejects the Planner's eager
per-Domain file set and copied registry map to preserve CD-0222 D12's subtraction.

This report uses original wording and citations, not copied templates or
diagrams. For later adaptation, Pocock's [MIT license][pocock-license] requires
its copyright and permission notice with copied substantial portions. Keep the
notice through the existing skill attribution route. MADR offers
[MIT or CC0](https://github.com/adr/madr#license); arc42's site is
[CC BY-SA 4.0](https://arc42.org/overview/), and C4's site is
[CC BY 4.0](https://c4model.com/). Do not copy their templates or diagrams into
shipped material without meeting the applicable license terms. This is source
reuse guidance, not a new legal obligation or license decision.

[pocock-adr]: https://github.com/mattpocock/skills/blob/4ae2317ddb22b046e26455e4f9c9b3aaaf62212d/skills/engineering/domain-modeling/ADR-FORMAT.md
[pocock-glossary]: https://github.com/mattpocock/skills/blob/4ae2317ddb22b046e26455e4f9c9b3aaaf62212d/skills/engineering/domain-modeling/GLOSSARY-FORMAT.md
[pocock-spec]: https://github.com/mattpocock/skills/blob/4ae2317ddb22b046e26455e4f9c9b3aaaf62212d/skills/engineering/to-spec/SKILL.md
[pocock-wayfinder]: https://github.com/mattpocock/skills/blob/4ae2317ddb22b046e26455e4f9c9b3aaaf62212d/skills/engineering/wayfinder/SKILL.md
[pocock-writing]: https://github.com/mattpocock/skills/blob/4ae2317ddb22b046e26455e4f9c9b3aaaf62212d/skills/productivity/writing-for-agents/SKILL.md
[pocock-license]: https://github.com/mattpocock/skills/blob/4ae2317ddb22b046e26455e4f9c9b3aaaf62212d/LICENSE

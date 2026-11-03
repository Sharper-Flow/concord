---
{
  "id": "CD-0251",
  "title": "Architecture reference is not Product law",
  "authority": {
    "tier": "derived"
  },
  "home_domain_id": "product-memory",
  "applies_to_domain_ids": [],
  "law_relations": [
    {
      "kind": "refines",
      "target_id": "CD-0194"
    },
    {
      "kind": "refines",
      "target_id": "CD-0222"
    }
  ]
}
---
# CD-0251: Architecture reference is not Product law

- **Status:** Accepted
- **Date:** 2026-11-03
- **Scope:** The authority relationship between a Product's architecture
  reference and its law; the organisation of that reference around the Domain
  registry; the exclusion of planning from current reference; and the routing
  of a plan's lasting outputs to their authorities before delivery
- **Related:** [CD-0194](CD-0194-product-knowledge-lives-under-concord.md),
  [CD-0222](CD-0222-architecture-bound-product-law-with-package-boundary-code-ownership.md),
  [CD-0213](CD-0213-linear-mcp-is-the-only-planning-authority.md),
  [CD-0230](CD-0230-the-conflict-rules-are-one-checked-table-and-an-agent-changes-law-that-is-not-serious.md),
  [law-folder-authority](../constitution/law-folder-authority.md)
- **Approval:** The operator approved the underlying model ("Adopt model
  (Recommended)", [CON-1669](https://linear.app/sharper-flow/issue/CON-1669)
  comment `3c2e3d0d-841d-406d-a9fb-5f28af39d8bc`, 2026-11-03) as a
  product-direction decision, on the merged research report
  [architecture-docs-model.md](../../docs/research/architecture-docs-model.md).
  This record is agent-enacted under CD-0230 D4 and carries the `derived`
  tier; that approval is not legislative standing under CD-0159 D2.
- **Preserves:** CD-0194 placement and the operator-override route; CD-0222
  D2 registry authority and D12 navigation retirement; CD-0213 planning
  authority; law-folder authority and precedence

## Context

No law stated what a Product architecture document is or how it relates to
law. Planner reports across Products motivated the investigation: one
Product was reported to hold architecture files mixing current state, target
designs, delivery plans and inventories, keyed to no registry Domain; one
overview was reported to state a file-size rule contradicting that Product's
law; another to keep a decision record outside its law folder. The research
report did not reproduce these reports, and they are not proof of current
violations in those Products. A reader who takes a document's normative
claim as current law can act against accepted law. Separately, a plan's
lasting decisions had no required destination: plans are stored in workflow
step records, and step records need not outlive the item's delivered close.

The operator approved the lazy-reference model on CON-1669 after the merged
research report. This record enacts that model's relationship rules. The
authoring method — outlines, reading budgets, plan additions, glossary
format — belongs to the architecture-writing method tracked in CON-1670,
which cites the same report rules; this record does not restate it and
depends on no particular state of that follow-up.

## Decision

### D1. Architecture reference explains; it creates no authority

**Architecture reference** is Product knowledge under the designated
knowledge home that describes the delivered system's architecture: a Product
overview, Domain and cross-cutting reference, and glossary entries. Its
placement stays governed by CD-0194; it is reference, not law
([law-folder-authority](../constitution/law-folder-authority.md)).

Product law owns obligations, governed boundaries and accepted architectural
decisions. The Domain registry alone owns Domain identity, hierarchy and
governed architecture relations (CD-0222 D2). Architecture reference explains
the delivered system and links those authorities. It creates none of them: no
obligation, Domain identity, governed relation or approval exists because a
reference document states it.

### D2. A normative claim in reference links its law or is removed

A normative claim in architecture reference — a rule the reader is told to
obey — has its obligation in law and links to that law. A claim whose
obligation is missing is proposed through the existing law-change route or
removed from the reference.

A contradiction between reference and law is reported and resolved through
that route. It is never treated as a silent amendment of law, nor as proof
that the desired state already exists in the delivered system.

### D3. Organisation follows the Domain registry, lazily

A Product keeps one canonical architecture overview. Domain and cross-cutting
reference is created only for useful explanatory content beyond that
overview; no file per Domain is required, and no rule of this record may be
read as mandating one. Referencing a Domain uses its exact registry identity;
descriptive source links do not confer code ownership or create a second
identity. Reference links the registry rather than copying its table or
relations (CD-0222 D12).

Existing useful document paths and small owner notes are not replaced to
conform to this decision. It imposes no bulk rewrite of existing documents:
conformance is owed by changes that create or update architecture reference,
including the reference a change's own delivery updates under D5.

### D4. Current reference excludes planning

Current architecture reference excludes delivery plans, rollout sequences,
backlogs, exhaustive inventories and research records. Planning lives in
Linear (CD-0213); managed execution records live in Concord; research with a
durable result stays a research record under `.concord/docs/` (CD-0194 D1),
never merged into current reference. Reference links a target design in the
plan that proposes it and does not describe it as delivered behavior.

### D5. Reference follows change, and lasting plan outputs reach their authorities before delivery

The owner of a change that alters a documented boundary, flow or state
authority updates the affected reference in the same change, or the work
record states why the description remains true.

The final review of the delivering item checks the changed claims of
affected reference against code and law, and routes each lasting output of
its plan before delivery:

1. An obligation or governed boundary is enacted or amended through the
   existing law-change route (CD-0230 D4). A selection test cannot exempt a
   rule from law because it is reversible.
2. Discretionary architectural rationale is recorded as a law decision
   through the same route when all three hold: it is hard to reverse,
   surprising without context, and a real trade-off. Otherwise useful
   current explanation stays in reference and transient choices stay in the
   work record. An existing decision is linked instead of duplicated.
3. Changed delivered structure updates its canonical reference.
4. Settled Product vocabulary updates the glossary, or links the law
   definition instead of redefining it.
5. What is neither lasting nor current stays in the temporary work record;
   an obsolete plan is not archived into the architecture or research tree.

One output can require both a law delta and a reference update. When no
update is needed, the work record states why. A work note may link these
outputs but is never their only durable home. Existing approval and
publication duties remain.

### D6. Reference is read on demand; budgets are not gates

Architecture reference is read when a task needs it, not loaded as a standing
corpus. Editorial reading budgets guide authoring and editing only: no
numeric budget is an acceptance gate, an authority test, or a waiver of law,
registry identity or workflow gates.

Missing reference documents, like a missing registry, never authorize
invented Domain identity, waived law, a second knowledge home, or a bypass of
workflow gates (CD-0222 D2).

## Alternatives considered

- Amend CD-0194 and CD-0222 in place. Rejected: the model spans placement,
  registry identity and review routing that those records only partially
  own; splitting it widens two dense records and fragments one approved
  model.
- A second decision authority for architecture — a `docs/adr/` tree or a
  decision appendix inside reference. Rejected: law-folder authority is sole;
  a record outside `.concord/law/` is not law, and a second authority
  invites the silent contradictions this record closes.
- An eager file per Domain, a generated registry mirror, or a hard size
  gate. Rejected: CD-0222 D12 retired that corpus, and the operator approved
  the lazy model only.
- A new validator checking normative claims or Domain labels. Rejected: no
  checker establishes semantic consistency between prose and law; review and
  the existing law checks are the cheapest adequate enforcement.
- A mandated retrofit of existing Product documents. Rejected: the approval
  covers the relationship for documents a Product creates or updates; no
  immediate cross-Product migration was approved.

## Consequences

A Product's law folder now answers how its architecture documents relate to
law: reference explains and links; law and the registry decide. Changes that
alter documented boundaries owe a same-change reference update, and final
review owes each lasting plan output a destination. Existing useful document
paths stay. The architecture-writing method tracked in CON-1670 carries the
authoring detail and cites this record; no runtime behavior, storage surface
or validator is added.

## Verification

- `python3 scripts/check-domain-registry.py` proves this record's law homing,
  applicability and relation endpoints on the changed tree.
- `python3 scripts/check-json.py` and `python3 scripts/check-doc-links.py`
  prove corpus shape and link validity; `python3 scripts/check-durable-tier.py`
  enforces the CD-0002 durable-tier rules for the decisions folders.
  Required CI runs each as its own step.
- Required CI on the pull request, and default-branch CI after merge, prove
  the law corpus still validates.
- Independent exact-head review compares the enacted clauses against the
  approved model and the PR's changed-clause list; semantic conformance is a
  review judgment, and no executable proof is claimed for it.

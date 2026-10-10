# CD-0214: Repository navigation consumes the Domain graph without launcher copies

- **Status:** Accepted
- **Date:** 2026-10-09
- **Scope:** Domain graph consumers, registry participation, and repository navigation adoption.
- **Approval:** Operator-delegated approval A4 for [Concord (CON) issue 899](https://linear.app/sharper-flow/issue/CON-899).
- **Supersedes:** [CD-0158](CD-0158-the-domain-graph-earns-its-place-through-an-authoring-obligation.md).
- **Related:** [CD-0060](CD-0060-domain-registry-enactment.md) and [CD-0219](CD-0219-the-terminal-launcher-tui-is-retired.md).
- **Amendment approval:** The coordinator (operator-delegated) approved the launcher-subject reconciliation for [Concord (CON) issue 908](https://linear.app/sharper-flow/issue/CON-908) on 2026-10-10 at 08:23 Eastern Daylight Time (EDT).

## Context

Repository navigation consumes the Domain graph. Exact Domain-detail reads
expose canonical relation tuples. The interactive launcher subject retired
under CD-0219; its view obligations do not bind the kept session entry route.

Registry participation,
optional applicability, validator adoption, and repository navigation retain
their obligations. Global navigation retirement requires its separate cutover;
this decision does not authorize that retirement.

## Decision

### D1. Repository navigation consumes the Domain graph

The Domain graph serves agent navigation in a repository that adopts a navigation
companion. Exact Domain-detail reads retain canonical relation tuples.

`concord_domain.detail` remains an advisory read. Navigation shows law-backed
dependency interpretations separately from observed code relationships.
The graph grants no workflow execution authority and proves no behavior.
Navigation validation checks declared references and completeness, not
architectural execution authority or behavioral conformance.

The launcher registry-row, law-count, work-count, and overlap-display clauses
have no subject under CD-0219. The surviving store query contracts keep their
bounds and authority; this amendment adds no read or display obligation.

### D2. A Domain must participate in the graph

A Domain participates when at least one relation names it, as source or target.
A registry that declares two or more Domains and leaves a Domain unnamed by
every relation is a defect. Participation does not require an outbound relation.
A registry that declares one Domain can express no relation and passes.

An empty `architecture_relations` array on a participating Domain remains valid.
It states that the Domain depends on nothing. An inbound relation can establish
participation without changing that statement.

### D3. `applies_to_domain_ids` stays optional

A law applies beyond its home Domain only sometimes. Required applicability
would manufacture a claim where no cross-Domain obligation exists.
[CD-0041 D3](CD-0041-architecture-bound-product-law.md) bounds applicability
against the registry. `scripts/check-domain-registry.py` enforces that bound.
This decision adds no obligation to the field and removes none.

### D4. The rule warns before it refuses

`scripts/check-domain-registry.py` reports each non-participating Domain by name
and exits zero. A `--strict` flag exits non-zero on any finding.
A Product adopts strict mode when its registry satisfies the rule.

The check remains separate from knowledge projection admission. It does not
introduce a refusal inside `prepareDomainProjection` that breaks knowledge reads.

### D5. Every Product repository carries the validator

`scripts/check-domain-registry.py` reaches each Product repository through the
same propagation route as the four knowledge scripts. Each Product's own pull
request gate runs it. Concord ships no validator asset and gains no install
channel.

Adoption is one change per Product, raised against that Product. A Product
adopts the rule when its registry passes and its gate runs the check.

Registry validation and repository navigation have separate adoption contracts.
A Product without a navigation companion keeps its existing registry checks.
A repository that adopts navigation declares its supported format and
completeness level. Coarse navigation may retain a finite, named legacy
unresolved set. New paths must map, and that set may only shrink.
Changed legacy unresolved paths report advisories until authored assignments
resolve them.

Complete navigation requires zero unresolved or ambiguously owned paths.
Every scoped source-catalog entry has one explicit mechanism binding.
Contract, governing-clause, control, and declared-check references must resolve.
Unknown or duplicate identities, uncovered entries, dangling references,
and stale generated artifacts refuse complete-navigation validation.
Generation preserves mandatory references and refuses a declared bound overflow.
Invariant/control inventories retain declared coverage gaps and their required
issue or reason. Complete navigation does not claim complete behavioral coverage.

Repository companions and generated cards do not change the shared registry
schema or create another registry owner. Navigation format changes require
explicit reader validation. Closed older readers may refuse unsupported formats.
A passing registry participation check does not claim complete navigation.

## Alternatives considered

- Keep the launcher copy. Rejected because no edge display consumes it, but its
  bound can make the Domain section unavailable.
- Retire repository navigation with this change. Rejected because shared-context
  cutover and its coordination witnesses have a separate sequencing gate.
- Keep two accepted decisions with conflicting launcher obligations. Rejected
  because [CD-0036](CD-0036-breaking-law-cutovers.md) requires a new identity
  for breaking law replacement.

## Consequences

- The retired launcher Domain section imposes no availability obligation on the
  kept session entry route.
- Registry participation findings and strict adoption retain their meaning.
- Repository navigation, completeness checks, and generated artifacts remain.
- Exact Domain-detail reads retain canonical relations and their governing law.
- Authoring a relation stays bound by CD-0041 D4. A `depends_on` or
  `shares_contract_with` relation still requires a current governing law ID.
  A participation finding does not license invented law to close the gap.
- Active contracts that consume the predecessor follow CD-0036's quiescence and
  recovery rules. This Git change grants no live-state activation authority.

## Verification

- `TestDomainDetailShowsCurrentLawRelationsAndRefusesUnknown` proves canonical relations remain in exact Domain-detail reads.
- `python3 scripts/test-check-domain-registry.py` checks participation warnings, strict
  refusal, single-Domain validity, and participation through an inbound relation.
- `python3 scripts/test-domain-navigation.py` checks complete-navigation catalog and
  reference refusals, coarse-format compatibility, and card bounds.
- `python3 scripts/generate-domain-navigation.py --check` refuses stale artifacts
  without changing the registry participation result.

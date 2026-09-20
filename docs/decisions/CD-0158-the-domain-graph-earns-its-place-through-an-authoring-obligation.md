# CD-0158: The Domain graph earns its place through an authoring obligation

- **Status:** Accepted
- **Date:** 2026-09-20
- **Scope:** `architecture_relations` on the Domain registry; `applies_to_domain_ids`
  on a law record; the participation rule; `scripts/check-domain-registry.py`;
  Product repository propagation; [Concord (CON) issue
  326](https://linear.app/sharper-flow/issue/CON-326)
- **Approval:** The operator approved this contract on 2026-09-20 after shaping
  in Concord work-78f4760449533737f277f766.
- **Related:** CD-0041, CD-0060, CD-0062, CD-0134, and CD-0145
- **Preserves:** The CD-0041 D4 relation grammar; the CD-0060 D4 population
  rule; the CD-0145 D1 overlap predicate
- **Supersedes:** Nothing

## Context

Seven Products hold a Domain registry. Concord authors eight `depends_on`
relations and twenty-four records that name applied Domains. Every other
Product authors none. No registry has been amended since its bootstrap commit.

The layer has one reader and it is the operator. `SelectProduct` at
[`internal/launcher/model.go:332-350`](../../internal/launcher/model.go) sets
`SectionDomains` as the default panel on every Product screen, and
[`internal/launcher/render/bubbletea/model.go:953`](../../internal/launcher/render/bubbletea/model.go)
renders each relation. Six of seven Products render that panel blank.

No gate reads the layer. The overlap predicate at
[`internal/store/workflow_domain_overlap.go:334-345`](../../internal/store/workflow_domain_overlap.go)
draws every input from the `workflow_contract_*` tables, which CD-0145 D1
decided. The remaining reads at
[`internal/store/domain_reads.go:403`](../../internal/store/domain_reads.go) and
[`internal/store/knowledge_query.go:442`](../../internal/store/knowledge_query.go)
render a payload.

Nothing obliges an author to write a relation, and nothing detects the absence
of one. `scripts/check-domain-registry.py` already refuses a Domain that owns no
law, and already resolves each `governing_law_ids` entry against a current
record. It accepts an empty `architecture_relations` array. The script lives in
this repository alone.

Removal was the alternative. It would delete a live operator view, one third of
the CD-0041 relation grammar, all of CD-0060 and CD-0062, clauses in CD-0076,
CD-0134 D4, CD-0144, CD-0145, and CD-0153, three tables with fifteen triggers
and a migration, and the authored data this Product already holds. Keeping the
layer costs one predicate in a validator that already runs.

## Decision

### D1. The operator's launcher panel is the consumer

The Domain graph exists to show the operator how a Product's boundaries stand to
each other. That reader is the launcher Domain panel. The graph gates nothing,
and this record does not make it gate anything. An agent may read the same data
through `concord_domain.detail`, and that read stays advisory.

A Product whose panel is blank tells the operator that its boundaries relate in
no way. For six Products that claim is false, and nothing in the system says so.

### D2. A Domain must participate in the graph

A Domain participates when at least one relation names it, as source or as
target. A registry that declares two or more Domains and leaves a Domain
unnamed by every relation is a defect.

The rule is participation, not outbound count. `durable-authority` holds no
outbound relation and is the target of six. `product-root:concord` holds
neither, and this record makes it a defect. A registry that declares one Domain
can express no relation, and passes.

An empty `architecture_relations` array on a participating Domain remains valid.
It states that the Domain depends on nothing, which the six inbound edges to
`durable-authority` show is a real condition.

### D3. `applies_to_domain_ids` stays optional

A law applies beyond its home Domain only sometimes. An obligation to name
applied Domains would manufacture the false claim it is meant to prevent, in the
opposite direction from D2. CD-0041 D3 already bounds applicability against the
registry, and `scripts/check-domain-registry.py` already enforces that bound.

This record adds no obligation to that field and removes none.

### D4. The rule warns before it refuses

`scripts/check-domain-registry.py` reports each non-participating Domain by name
and exits zero. A `--strict` flag exits non-zero on any finding. A Product
adopts strict mode when its registry satisfies the rule.

This follows the migration pattern this repository already uses.
`scripts/check-knowledge-closure.py` lists each unprocessed file and refuses
only under `--strict`. `doc_contract.enforced` in the knowledge manifest carries
the same switch for the document contract.

A refusal inside `prepareDomainProjection` was rejected. `EnsureKnowledgeIndexFresh`
returns a rebuild error to its caller, so a hard refusal would break every
knowledge read for the six Products that fail the rule today.

### D5. Every Product repository carries the validator

`scripts/check-domain-registry.py` reaches each Product repository the way the
four knowledge scripts already do, and each Product's own pull request gate runs
it. Concord ships no validator asset and gains no new install channel.

Adoption is one change per Product, raised against that Product. A Product
adopts the rule when its registry passes and its gate runs the check.

## Consequences

- Six Products gain a named finding where they held silence. None of them
  refuses until its registry passes and its own gate adopts strict mode.
- `product-root:concord` becomes a finding in this repository. The root Domain
  relates to no child, and D2 makes that claim visible.
- Authoring a relation stays bound by CD-0041 D4. A `depends_on` or a
  `shares_contract_with` relation still refuses without a current governing law
  ID, so a Product with few laws can express few relations. The rule reports the
  gap; it does not license an invented law to close it.
- The operator's panel stops asserting an empty graph without evidence.
- Each Product's adoption is separate work and lands on its own schedule.

## Verification

- `python3 scripts/check-domain-registry.py` reports each non-participating
  Domain by name and exits zero.
- `python3 scripts/check-domain-registry.py --strict` exits non-zero while any
  Domain does not participate.
- A registry that declares one Domain passes both modes.
- A Domain named only as a relation target passes both modes.
- The existing CD-0060 D4 and CD-0041 D4 checks keep their current results.

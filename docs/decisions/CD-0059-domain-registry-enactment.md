# CD-0059: The Domain registry enacts seven child Domains

- **Status:** Accepted
- **Date:** 2026-08-22
- **Scope:** Domain registry population; law-record homing; the CD-0041 D9.1
  migration delta
- **Approval:** Operator accepted the drafted decision as written on 2026-08-22;
  the public record is the pull request that lands this record
- **Related:** CD-0041 D2/D3/D9, CD-0002, CD-0013, CD-0015, CD-0036, CD-0047,
  issue #337
- **Preserves:** CD-0041 D3's one-home invariant; Git as the sole writer of
  Domain identity; the derived-projection role of SQLite
- **Supersedes:** nothing

## Context

CD-0041 D2 made the Domain registry Product law. The registry then held one
entry, `product-root:concord`. Every current law record homed to it.

D2 forbids that shape:

> A Product-wide or cross-cutting concern is a root Domain, not work with no
> architectural home. The root Domain is not a catch-all: work whose behavior
> fits a child Domain homes there.

Whether the root was a catch-all was an open question, not a settled fact. A
root-only registry is correct when the corpus is genuinely Product-wide. So the
corpus decided it. Each of the 65 homed records was classified against candidate
Domains by reading what the record governs. Seven records were Product-wide.
Fifty-eight fit a child Domain. The root was a catch-all.

An earlier draft of this registry declared six child Domains and homed no law to
them. That draft is rejected. It left the catch-all intact and added six
Domains that owned nothing.

## Decision

### D1. Seven child Domains are enacted

The registry declares seven child Domains under `product-root:concord`:

```text
durable-authority        SQLite authority, event log, projections, migrations
workflow-engine          workflow contracts, actions, verdicts, terminal law
agent-surface            typed tool surface, lanes, transport, result envelope
product-memory           knowledge index, law records, coverage, retention
work-coordination        work-item semantics, relations, claims, observations
operator-surface         terminal launcher, CLI verbs, installation
repository-verification  the authority a repository check may hold
```

`work-coordination` owns the work item as an entity. It owns what a work item
means and how work items refer to one another. It does not own how work
executes, which is `workflow-engine`, nor how work persists, which is
`durable-authority`. Without it the central entity of this Product has no
architectural owner.

### D2. The root Domain keeps only Product-wide constitutional law

Eight records home to `product-root:concord`. Each binds every child Domain and
cannot sit in one of them. CD-0041 itself is among them, because a record that
defines the Domain model cannot be owned by a partition it creates.

### D3. Straddling law declares one home and bounded applicability

Law that reaches several Domains declares one `home_domain_id` and lists the
others in `applies_to_domain_ids`. Twenty-one records do this. Per CD-0041 D3,
applicability creates no second owner.

Where a record amends another record's contract, it homes with that contract.
PM3 narrowly supersedes CD-0003 D1 and homes to `durable-authority`. CD-0036
amends CD-0015's law-boundary contract and homes to `product-memory`. This rule
resolves the straddling cases that clause reading leaves balanced.

### D4. A declared Domain owns law at enactment

No Domain enters the registry empty. A boundary that owns nothing is a label,
and it cannot be checked against anything.

This retires `predecessor-migration` from the enacted set. That Domain would
cover the snapshot contract, import verbs, and cutover. No accepted law governs
those yet. It is declared in the same change that writes its first law, never
before.

### D5. Architecture relations name the law that governs them

Every relation carries `governing_law_ids`, as the schema requires for
`depends_on` and `shares_contract_with`. Six relations are declared. Five child
Domains depend on `durable-authority` under CD-0002; `repository-verification`
depends on `product-memory` under CD-0047.

An empty `architecture_relations` array is not a neutral default. It asserts
that no accepted law governs any interaction with a sibling. For Domains inside
one binary sharing one event log, that assertion is false.

### D6. Domain identity is not inferred from a record's name

CD-0041 D2 excludes paths, tags, and repository names from Domain identity. A
record's own title is the same kind of evidence and is excluded on the same
ground.

CD-0019 and CD-0053 carry "predecessor" in their titles. Neither governs
migration. CD-0019 is a preservation mandate whose D4 states it "does not
authorize implementation". CD-0053 rules three features absorbed or
preserved-with-shape. Both are Product-wide and home to the root. Reading the
titles alone would have homed both to a migration Domain and left that Domain
holding the only two records that do not belong in it.

## Consequences

- The knowledge manifest and the registry shard carry eight Domain entries: the
  root and seven children. `docs/knowledge/domain-registry.json` is the shard;
  the aggregate is generated from it.
- Every current law record declares `home_domain_id`. No current law is unhomed,
  which satisfies the CD-0041 D9.2 requirement for the manifest version that
  makes the one-home invariant mandatory.
- Twenty-one records gain `applies_to_domain_ids`.
- Domain-scoped reads over law now return partitioned results. Before this
  record every such read returned the whole corpus.
- The registry content hash changes, so every projection keyed by it rebuilds.
- Adding a Domain later stays cheap. Adding one before its law exists does not
  become acceptable.

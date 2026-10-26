# CD-0244: Domains own code exclusively and hold typed facts agents query in one call

- **Status:** Accepted
- **Date:** 2026-10-23
- **Scope:** The Domain shape Concord sets up, checks, and queries in every Product: code ownership, allowed dependencies, typed Domain facts, and their retrieval
- **Amends:** [CD-0222](CD-0222-architecture-bound-product-law-with-package-boundary-code-ownership.md) D2 and D12; [`concord-knowledge-index.md`](../concord-knowledge-index.md)
- **Approval:** The operator approved this exact text, including the transitional mixed role, on 2026-10-23. Approval is recorded in [Concord (CON) issue 1317](https://linear.app/sharper-flow/issue/CON-1317).

## Context

Agents need to find a Domain's invariants, its distributed state, its database
semantics, and its deployment and configuration coupling quickly. Today they
cannot.

CD-0222 D2 lets a Domain be a cross-cutting concern and lets one code package
serve several Domains. In Concord itself, `internal/store` and `internal/agent`
each map to all seven child Domains. A footprint read for any file in them
therefore returns the whole Product. The package-map check makes this worse: it
requires a package to list every Domain whose law names one of its files.

Knowledge search returns whole documents ranked by match class and date. No
record kind holds an invariant, a data object, a state authority, or a
configuration setting. These facts sit in decision prose, package comments, and
host instructions.

An earlier attempt bound a Domain to every file and generated per-Domain cards.
It grew to a ~256 KB inventory of 1,892 paths, had high merge and context costs,
and was deleted under D12. Published evidence points the same way. Repository
overview files did not improve agent task success and raised cost by over 20%
([arXiv:2602.11988](https://arxiv.org/abs/2602.11988)). Structured code indexes
measurably improved localization ([arXiv:2503.09089](https://arxiv.org/abs/2503.09089);
[arXiv:2410.14684](https://arxiv.org/abs/2410.14684)). Every module-boundary
tool examined (Spring Modulith, packwerk, Nx, import-linter, go-arch-lint,
ArchUnit) gives each file exactly one owning module.

## Decision

### D1. A Domain exclusively owns its code

A Domain is a stable Product-internal unit that owns behavior, its invariants,
and the code that implements it. A Domain is not a cross-cutting concern. A rule
that binds several Domains is an invariant fact under D4 and D6. It is never a
Domain of its own.

This replaces CD-0222 D2's sentence "It may describe a capability, subsystem,
service, or cross-cutting architectural concern." The other fields and
authority of the Domain registry in CD-0222 D2 are unchanged.

### D2. Code roots are declared at package or module grain

The package map declares every code root: a Go package, or a declared non-Go
module root. Each code root has one role:

| Role | Meaning | Owner |
|---|---|---|
| `domain` | Implements one Domain's behavior | exactly one `owner_domain_id` |
| `composition` | Wires Domains together, such as a CLI entry or host adapter; owns no behavior | none |
| `test_support` | Shared test helpers | none |
| `mixed` | Transitional: implements several Domains' behavior and has not yet been restructured | lists its Domains; counts against the ratchet in D3 |

The map names no files and no globs. A fact (D4) never names a `composition`
or `test_support` root as its owner.

This replaces CD-0222 D2's rule that a package lists every Domain it serves and
every child Domain whose current law names a file in the package.

### D3. Allowed dependencies are the Domain graph, checked by a Product command

A Domain's `depends_on` architecture relations are the only Domain-to-Domain
imports its code roots may make. `composition` roots may import any Domain.

Concord does not analyse source languages itself. Each Product declares a
boundary-check command in `.concord/tooling.v1.json`. Concord supplies the
allowed edges as the command's input and defines the result format. The
Product chooses the tool (Go imports, import-linter, dependency-cruiser, or
another).

Existing violations and `mixed` roots are recorded in one ratchet file per
Product. In strict mode the check refuses a new entry. A new Product starts
in strict mode.

### D4. Domain facts are typed, small, and owned by one Domain

A Product's knowledge home holds four kinds of fact. Each fact is one JSON
shard, has exactly one `owner_domain_id`, and has no body document.

| Kind | Required content |
|---|---|
| `invariant` | bounded statement; `applies_to_domain_ids`; `enforced_by` anchors |
| `data_object` | name (table, collection, topic, or prefix); the resource that stores it; writer and reader Domains; its invariant IDs |
| `state_authority` | the state; its authoritative store; each derived copy with its rebuild route; what happens when they disagree |
| `config_coupling` | the setting and its source; the symbol that reads it; its bounded effect; the environments it varies across |

Every fact carries `standing`:

- `law`: the fact states accepted law and names its `governing_law_ids`.
- `observed`: the fact records current code behavior that is not law. An
  observed fact grants no authority and becomes law only through the law-change
  path of CD-0243 D4.

`enforced_by` anchors resolve through the CD-0047 typed-anchor rules and must
run in a required check. Decision and specification documents remain the home
of reasoning and approval. A fact cites them; it does not replace them.

### D5. Facts that code can prove are compared with code

A Product may declare extractor commands in `.concord/tooling.v1.json`.
Concord defines each extractor's output format. Supported extractors report
the data objects a schema declares, with the code root that declares each, and
the configuration settings code reads, with the reading symbol.

Where an extractor exists, a data object or setting that has no fact is a check
finding, and so is a fact that the extractor does not report. Where no
extractor exists, the fact is authored and its result reports it as unverified.

### D6. A cross-cutting rule belongs to the Domain that enforces it

The Domain whose code enforces a rule owns the invariant fact for it.
`applies_to_domain_ids` names every Domain the rule binds. Concord defines no
separate concern classifier.

### D7. One bounded read answers a Domain question

Concord serves one read with three selectors: a code path, a Domain, or a
subject (a data object, setting, state, or resource). It returns the owning
Domain or Domains and the matching facts with their anchors, within a declared
byte bound. A `mixed` root returns its listed Domains, labelled `mixed`.

The footprint read and the work-context required reading consume this read.
Concord generates no per-Domain cards and injects no Domain overview into a
session.

This replaces CD-0222 D12's "small owner notes" for invariants, entry points,
and verification commands. Those move into facts. D12's prohibition on file
inventories, path-glob registries, generated cards, and relation visualisation
stands.

### D8. Concord sets up the shape for every Product

When Concord registers a Product knowledge home, it establishes:

- the Domain registry;
- the package map;
- the fact directory;
- the boundary-check and extractor declarations in `.concord/tooling.v1.json`.

The fact directory may start empty. An existing Product enters with its
unrestructured roots declared `mixed`.

## Alternatives considered

- **Keep multi-Domain packages (CD-0222 D2 as written).** Rejected. The map
  cannot narrow a lookup inside a package that serves every Domain.
- **Bind a Domain to each file.** Rejected. It was tried and deleted under
  D12 for its merge and context cost.
- **A concern classifier on facts.** Rejected. The four fact kinds already
  answer the target questions, half the proposed concerns duplicated fact
  kinds, and a label that enforces nothing behaves as a tag. Reconsider only
  if a golden question requires it.
- **Generated Domain cards or overview files.** Rejected. Overview context did
  not improve agent success in the cited study and raised cost.
- **Language analysers inside Concord.** Rejected. Products use several
  languages, and mature boundary tools already exist for each.

## Consequences

### Positive

- An agent resolves a path to one Domain and that Domain's facts in one call.
- Invariants, state authority, data objects, and configuration have typed
  homes, and their links to enforcing checks are themselves checked.
- Code layout and knowledge share one ownership model.

### Cost

- Products with multi-Domain packages must restructure them or carry them as
  `mixed`. In Concord, `internal/store` and `internal/agent` start as `mixed`.
  Splitting the store is constrained by its single-connection rule
  (`internal/store/doc.go`).
- The knowledge-index contract, package-map validator, footprint read, and
  Domain detail read change.
- No established standard types invariants. The fields in D4 are new and are
  checked against the golden question set.

## Required conformance

- A schema test refuses a fact with zero or two owner Domains, a `law` fact
  without `governing_law_ids`, and an `enforced_by` anchor that does not
  resolve.
- A package-map test refuses a file-level or glob entry, a `domain` root with
  zero or two owners, and a fact owned by a `composition` root.
- A boundary-check fixture proves Concord supplies the `depends_on` edges and
  that strict mode refuses a new ratchet entry.
- An extractor fixture proves a missing fact and an orphan fact are both
  findings.
- A golden corpus of synthetic questions, at least five per fact kind, is
  answered in one read each, with a cited anchor and within the byte bound.

## Verification

- `python3 scripts/check-knowledge-index.py` checks publication identity and hashes.
- `python3 scripts/check-law-coverage.py` checks the outstanding implementation pointer; it does not prove the required conformance above.
- `python3 scripts/check-doc-contract.py` checks this decision's document structure.

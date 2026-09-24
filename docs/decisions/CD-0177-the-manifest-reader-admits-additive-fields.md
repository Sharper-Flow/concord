# CD-0177: The manifest reader admits additive fields

- **Status:** Accepted
- **Date:** 2026-09-24
- **Scope:** The knowledge manifest read path in the store, its authoring contract, and the committed shard tree
- **Related:** CD-0111, CD-0114, CD-0159
- **Approval:** The operator approved this decision in contract version 1 of work item
  `work-bc5323f3a1670806341c3768`.

## Context

The manifest parse refused every field its Go model did not declare. CD-0111
D1 pins a session to the core of its starting release, but that core reads the
live manifest from the default checkout, which runs ahead of every release. A
release pinned to an older core could then not read a manifest authored after
that release: one new authored field made the whole document refuse, and the
refusal gave the operator no way to read the law the core already understood.

The strictness also duplicated a rule authoring already owned. The closed
JSON Schema and the knowledge-index checker refuse unknown fields in CI, so
the parser's refusal added a second gate without adding a second catch.

## Decision

### D1. The reader drops undeclared fields at a known schema_version

At a known schema_version, the manifest reader drops a field its model does
not declare and parses the document. The drop covers the root decoder and
every nested decoder: record, authority, scopes, law relation, criterion
binding, domain, architecture relation, and disposition.

An unknown schema_version still refuses, because the declared version is the
only closed signal that separates corpora across behavior changes (CD-0159).
Duplicate keys, trailing values, and every semantic check refuse as before.

### D2. Authoring stays closed

The JSON Schema and `scripts/check-knowledge-index.py` are unchanged and keep
refusing unknown fields, and CI runs both. Authoring that adds a field
declares it on the Go model in the same change, and
`TestCommittedManifestCarriesNoFieldTheModelDrops` fails a commit whose shard
tree carries a field the model drops.

A field that must restrict older cores needs a schema_version bump. A purely
additive field does not.

### D3. The doc_contract subtree stays schema-owned

The head carries `doc_contract`, and the store never interprets it
(CD-0114). Its internal shape stays owned by the schema and the checker. The
committed-tree guard therefore treats the whole `doc_contract` subtree as
declared and holds only the modeled vocabulary outside it.

## Alternatives considered

- Keep the strict parse and pin manifests to releases. Rejected: every
  additive authoring change would strand older cores from current law.
- Admit unknown fields for authoring too. Rejected: it removes the closed
  contract that catches a typo before a commit lands.
- Gate the tolerance behind a new schema_version. Rejected: a version bump is
  a behavior boundary, and additive admission changes no modeled behavior.
- Model the `doc_contract` internals in Go. Rejected: the store must never
  interpret that subtree, and fields without behavior misstate the model.

## Consequences

A release-pinned core reads a manifest authored after its release. A new
authored field lands only when the Go model declares it in the same change,
or the committed-tree test fails the commit. The schema and the checker keep
refusing every unknown field at authoring. Older cores keep every modeled
field, and the reader-side unknown-field refusals move to the checks that
already owned them.

## Verification

- `TestParseAdmitsAdditiveManifestFieldsAtAKnownSchema` proves the read rule:
  a schema-1.3 manifest with an injected field at every decoder level parses,
  and the same test holds the unknown schema_version, duplicate-key, and
  trailing-value refusals.
- `TestCommittedManifestCarriesNoFieldTheModelDrops` proves the guard: it
  fails a committed shard that carries a field the model drops, with the
  `doc_contract` subtree declared out of the model.
- `go test ./internal/store` carries the flipped admission cases: the
  unknown-field case of
  `TestKnowledgeManifestRejectsUnknownFieldsAndInvalidCombinations`, the
  disposition unknown-field case, and the retired component-scope case admit,
  and the refusals around them hold.
- `python3 scripts/check-json.py` proves the authoring side: the closed
  schema, the knowledge-index checker, and this record's shard compose and
  validate unchanged.
- `python3 scripts/check-doc-contract.py` proves this record carries the
  current outline.

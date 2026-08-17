# Concord durable knowledge index

The tracked [`concord-knowledge-index.v1.json`](./concord-knowledge-index.v1.json)
is the manifest-primary registry for durable `decision`, `spec`, and `lesson`
records in a repository home. Schema `1.1` is backward-compatible with `1.0`
and optionally adds authored typed law relations to decision/spec records. The companion
[`concord-knowledge-index.v1.schema.json`](../contracts/concord-knowledge-index.v1.schema.json)
is the closed JSON Schema contract.

## Contract

The manifest has only `schema_version`, `supported_kinds`, `indexed_kinds`, and
`records`. Unknown fields, duplicate keys, duplicate IDs, and duplicate paths
are invalid. A record has an authored stable ID, closed kind, clean regular
Markdown path below `docs/`, status, RFC3339 date, bounded title and summary,
unique tags, closed scopes, and a required `sha256:` hash. It has no body or
content field. `decision` and `spec` records are `accepted`; `lesson` records
are `published`; `superseded` records require a successor.

The path bound is 512 Unicode scalar values, matching JSON Schema `maxLength`;
runtime Go counts UTF-8 runes and the checker counts Python Unicode scalars.
Supersession targets must be declared in the same manifest, must not self-link,
and must retain kind-compatible active status (`accepted` for decisions/specs,
`published` for lessons).

`law_relations` is closed to `supersedes`, `refines`, `subordinate_to`, and
`conflicts_with`. Both endpoints must be decision/spec records in the same
manifest. Self edges, duplicates, reverse conflict declarations, directed
cycles, and supersession edges that disagree with `successor` are rejected.
`conflicts_with` is normalized as one unordered projection pair and never
implies precedence. Relations are authored only by an operator-approved Git
manifest/spec/decision delta.

`scopes.mode` is explicit: `home` means the record belongs to this canonical
home and has no installation-local IDs; `explicit` requires the declared ID
arrays. Home records remain visible to Product/Project-scoped Q9 calls resolved
to that home. Explicit records are narrowed by their declared scope. Work-note
scopes remain frozen front-matter values and are never rewritten by manifest
ingestion. For Q10, the same population split applies after the archived row is
read: work notes and explicit records use frozen Product rows, while home-scoped
manifest records use current membership of their stored home Project. Unscoped
Q10 skips that visibility filter; neither branch changes the recorded historical
locator or its commit/hash proof.

Q9 resolves authority before reading its watermark and index rows. Q10 reads the
archived locator row first, then owns Product visibility, current-path resolution
through the stored Project/locator IDs, and recorded commit/hash proof. Q10 does
not require a current Product-home designation or ambient KnowledgeHome. Q9
Product scope still resolves through exactly one `product_knowledge_homes` row
joined to its canonical-path locator; Product+Project validates membership while
keeping the Product home as authority. Project-only resolves exactly one
canonical-path locator. Supplied Q9 home IDs, path, and head are evidence to
compare, never an authority override.

The manifest and each referenced Markdown blob are one Git authority at the
same scanned commit. SQLite stores only bounded metadata and locator/hash proof.
The standard-library checker validates the authored registry, recomputes hashes,
rejects dangling or generated/candidate entries, and proves every `CD-*.md`
decision is included. `python3 scripts/check-knowledge-index.py --update`
updates hashes only; it never authors inclusion, metadata, or status.

SQLite's `law_subjects` and `law_relations` tables are derived only by
`RebuildKnowledgeIndex` for one home, inside the same transactional fold guard.
Failed validation or rollback leaves the previous derived law projection
byte-identical. Workflow `spec_mandate` references these accepted subjects;
bounded `law_modifies` explicitly enters the operator-approved amendment path.

Research is a supported kind but remains `supported_not_indexed` until an
accepted canonical form exists. Missing or invalid manifest records fail closed
and leave the prior SQLite projection unchanged.

The launcher consumes this index only through bounded Q9 Product-scoped reads;
S3 resolves a canonical work note through Q10. It preserves `unread`,
`authoritative-empty`, and `unavailable` as distinct rendered states and does
not create or update index records.

This design does not weaken work-note compaction publication or fold guards.

Current workflow approvals also capture one `law_revisions` pin per mandated
ID. The immutable identity is `(law_id, content_hash)`; scanned commit OIDs are
audit context only. Same-ID hash amendments remain compatible, while an
accepted successor with `supersedes` over a `status=superseded` law strictly
quiesces old consumers through typed `stale_law_revision` recovery.
The rebuild path already has typed proof handling for accepted non-work kinds;
the missing mechanism was canonical manifest ingestion and explicit kind
coverage, not a work-note-only compaction rule.

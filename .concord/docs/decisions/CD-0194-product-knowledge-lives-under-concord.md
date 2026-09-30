# CD-0194: Product knowledge lives under .concord

- **Status:** Accepted
- **Date:** 2026-09-29
- **Scope:** The repository placement of Product knowledge, the operator
  override route for placements outside `.concord/`, and the retirement of
  the root `docs/`, `instructions/`, and `scenarios/` trees in this
  repository
- **Amends:** CD-0114 (keeps the shard composition, moves the shard home)
- **Related:** CD-0007, CD-0047, CD-0063, CD-0114, CD-0159
- **Approval:** The operator approved this decision on work item
  `work-670cac419bcacb671879316b`, with the objective and the design
  recorded in its dispatch packet.
- **Preserves:** CD-0114's shard composition, CD-0063's shipped conduct
  corpus, and the authority and identity of every migrated record

## Context

The operator set a default Product law: all Product knowledge belongs within
the repository's `.concord/` docs, structure, and schema. Only an explicit
recorded operator override admits a placement outside that tree. An inferred
exception never does. Concord itself held required knowledge in three root
trees: `docs/`, `instructions/`, and `scenarios/`. The default law therefore
required a migration of this repository before it could bind any other
Product.

## Decision

### D1. The default placement

Repository-authored Product knowledge lives under `.concord/docs`,
`.concord/instructions`, and `.concord/scenarios`. Product knowledge is the
knowledge manifest and its record shards, the decisions, specifications,
lessons, research, constitution, and reference records, the shipped operator
conduct, and the acceptance scenarios. Each artifact keeps one canonical
location. Git-authored Markdown stays the record body, and the manifest
shards stay the composed authority under CD-0114.

### D2. The only escape is a recorded operator override

A Product knowledge location outside `.concord/` is valid only when the
manifest head carries an explicit operator override for it. An override
names the path, the Product it belongs to, the record that carries the
operator's instruction, and the reason. The anchor record is an accepted
decision. A superseded decision carries no authority, and a record of
another kind never anchors an override. The store that holds work items is
external live state no repository check reaches. An identifier that merely
looks like a work item, an inference, a convention, and a reader's
convenience never admit an outside location.

The instruction is a closed block in the anchor document. Prose never
admits a placement. A sentence that names the path, the Product, or the
words of an override is not an instruction. A denial in prose refuses
exactly like silence. The block is a HyperText Markup Language (HTML)
comment, which a Markdown renderer keeps out of the document:

<!-- concord-operator-override
product: <product-id>
path: <repository-relative-path>
decision: approve
-->

The block carries the fields `product`, `path`, and `decision`, and no
others. The `product` and `path` values must equal the override's fields
exactly. The `decision` value is `approve` or `deny`, and only `approve`
admits the placement. The block refuses when any of these holds:

- A field is unknown, repeated, or missing.
- The block never closes, or a second block names one Product and path.
- The `decision` value is not `approve` or `deny`.

A recorded denial is law for the placement it names. The knowledge
placement check validates every override before it accepts the placement
that override names.

### D3. The root trees are gone

This repository carries no root `docs/`, `instructions/`, or `scenarios/`
directory and no compatibility symlink for those paths. The placement check
refuses the directory and the symlink alike. A new knowledge document lands
under `.concord/` by default, and the closure check keeps every unprocessed
document visible.

### D4. Installed conduct is a projection, not a second authoring home

The release copies `.concord/instructions` into the `instructions/` archive
member, and the installer keeps the installed `current/instructions/`
location. The repository placement changes. Shipped conduct keeps working,
and the installed copy stays derived from the one authored source.

### D5. History keeps the shape it has

A git revision composes its manifest from the layout that revision carries.
The reader walks the current shard home, then the pre-migration shard home,
then the pre-shard aggregate. No history rewrite and no dual write.

## Alternatives considered

- Keep the root trees or replace them with compatibility symlinks.
  Rejected: the operator's default forbids both, and a symlink keeps two
  paths to one record.
- Convert law bodies to canonical JSON beside the shards. Rejected: the
  approved outcome keeps Git-authored Markdown as the record body.
- Treat manifest exclusions as overrides. Rejected: an exclusion only
  removes a document from closure reporting. It is not an operator decision
  about placement.
- Point the readers at both trees for authoring. Rejected: reading history
  in its own shape is not authoring in it. The root trees stay closed to new
  content.

## Consequences

One `.concord/` tree owns Product knowledge in this repository. Agents
resolve law through the knowledge index and never through a root path. A
Product that needs knowledge outside `.concord/` records an override first,
and the placement check enforces that order. The manifest head carries the
`operator_overrides` field, and the schema, the generators, and the checks
validate it. Concord's own override list starts empty.

## Verification

- `python3 scripts/check-knowledge-placement.py` proves the default
  placement, validates recorded overrides against their closed instruction
  blocks, refuses unapproved outside placement, and refuses the three root
  directories and their symlinks.
- `python3 scripts/test-knowledge-placement.py` proves the check refuses a
  root tree, an unapproved outside record, a prose denial, a cross-pair
  instruction, a superseded or non-decision anchor, a malformed or
  duplicated instruction, and accepts a valid recorded override.
- `python3 scripts/check-knowledge-index.py` and
  `python3 scripts/check-knowledge-closure.py` prove record integrity and
  files-to-records coverage at the new home.
- `python3 scripts/check-cd-allocation.py` proves ref-based composition
  across the layout tiers. `python3 scripts/test-cd-allocation.py` and
  `python3 scripts/test-renumber-cd.py` prove the lineage guards treat
  records the comparison ref gained after the merge base as upstream
  additions, and records the merge base carried as durable law.
- `go test ./internal/store/` proves the store reads the migrated corpus.

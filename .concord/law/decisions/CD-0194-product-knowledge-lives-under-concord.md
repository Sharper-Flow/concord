# CD-0194: Product knowledge lives under .concord

- **Status:** Accepted
- **Date:** 2026-09-29
- **Scope:** The repository placement of Product knowledge, the operator
  override route for placements outside `.concord/`, and the retirement of
  the root docs/, instructions/, and scenarios/ trees in this
  repository
- **Related:** CD-0007, CD-0063, CD-0159
- **Approval:** The operator approved this decision on work item
  `work-670cac419bcacb671879316b`, with the objective and the design
  recorded in its dispatch packet.
- **Preserves:** CD-0063's shipped conduct
  corpus, and the authority and identity of every migrated record

## Context

The operator set a default Product law: all Product knowledge belongs within
the repository's `.concord/` docs, structure, and schema. Only an explicit
recorded operator override admits a placement outside that tree. An inferred
exception never does. Concord itself held required knowledge in three root
trees: docs/, instructions/, and scenarios/. The default law therefore
required a migration of this repository before it could bind any other
Product.

## Decision

### D1. The default placement

Repository-authored Product knowledge lives under `.concord/law`, `.concord/docs`,
`.concord/instructions`, and `.concord/scenarios`. Concord's accepted constitution,
specifications, and decisions live under `.concord/law/constitution/`,
`.concord/law/specs/`, and `.concord/law/decisions/`, preserving their basenames.
Folder membership on the default branch confers law status
([law-folder-authority](../constitution/law-folder-authority.md)); no
registration step exists. Product knowledge is the law files, the
lessons, research, and reference records, the shipped operator
conduct, and the acceptance scenarios. Each artifact keeps one canonical
location in the designated Product knowledge home under
[CD-0200](CD-0200-product-knowledge-resolves-through-one-designated-git-home.md).
Git-authored Markdown stays the record body.

### D2. The only escape is a recorded operator override

A Product knowledge location outside `.concord/` is valid only when an
accepted decision carries an explicit operator override for it. An override
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

A recorded denial is law for the placement it names. The override reserves
non-law placement only: no override, approval, or exception makes a file
outside `.concord/law/` law.

### D3. The root trees are gone

The canonical knowledge homes in this repository are under `.concord/`.
Root docs/, instructions/, or scenarios/ directories do not exist, and
neither do compatibility symlinks for those paths. A new knowledge document
lands under `.concord/` by default.

### D4. Installed conduct is a projection, not a second authoring home

The release copies `.concord/instructions` into the `<archive-root>/instructions/` archive
member, and the installer keeps the installed `<installation-root>/current/instructions/`
location. The repository placement changes. Shipped conduct keeps working,
and the installed copy stays derived from the one authored source.

### D5. History keeps the shape it has

Git history is not rewritten to relocate or retire Product knowledge, and
Product knowledge is not dual-written across locations.

## Alternatives considered

- Keep the root trees or replace them with compatibility symlinks.
  Rejected: the operator's default forbids both, and a symlink keeps two
  paths to one record.
- Convert law bodies to canonical JSON. Rejected: Git-authored Markdown
  stays the record body.
- Author new content in the root trees. Rejected: the root trees stay
  closed to new content.

## Consequences

One `.concord/` tree owns Product knowledge in this repository. Agents
resolve law through the default-branch law folder and never through a root
path. A Product that needs knowledge outside `.concord/` records an override
in an accepted decision first. The anchor decision
carries the `concord-operator-override` block, and repository review
validates it. No override makes a file outside `.concord/law/` law.
Concord's own law carries no override.

## Verification

- `python3 scripts/check-knowledge-index.py` proves record integrity at the
  new home.
- `python3 scripts/test-renumber-cd.py` proves the lineage guards treat records
  the comparison ref gained after the merge base as upstream additions, and
  records the merge base carried as durable law.
- `go test ./internal/store/` proves the store reads the migrated corpus.

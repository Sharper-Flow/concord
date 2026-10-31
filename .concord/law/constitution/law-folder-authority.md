---
{
  "id": "law-folder-authority",
  "title": "Product law is the default-branch law folder",
  "authority": {"tier": "derived"},
  "home_domain_id": "product-root:concord",
  "applies_to_domain_ids": [],
  "product_wide_rationale": "Law authority, precedence and revision identity bind every Domain of the Product."
}
---

# Product law is the default-branch law folder

## Authority and precedence

A Product's current law is exactly the files under `.concord/law/` on the
default branch of its designated Git home. The first subdirectory supplies
the kind: `constitution/`, `specs/`, or `decisions/`. Descendant directories
and file extensions do not change authority. A file outside that tree is
not law. Unmerged branch content is candidate text, not current law.

Folder membership supplies acceptance. No manifest, record shard, query
answer, catalog, or database row confers or removes law status. Invalid
in-file metadata produces a named read failure; a reader must not silently
omit that file or report it as absent law.

[Priorities](priorities.md) ranks first. Other constitutional law ranks
above specifications, which rank above decisions. This ordering does not
promote legislative standing or authorize a change outside the existing
approval boundary. Conflicting obligations must be surfaced and resolved
through the applicable law-change route.

## Identity and change

Metadata required by retained consumers lives inside each law file: its
stable ID, title, authority standing, Domain home and applicability, typed
relations, and specification criterion bindings. Folder kind, acceptance,
path and content digest are derived, not authored metadata. Domain
organization resolves against the independent Domain registry at its
existing repository path. No second law index or SQLite law cache exists.

A law revision's SHA-256 covers all file bytes, including metadata.
[Authority standing](../decisions/CD-0159-authority-tier-for-product-law.md)
and the [six approval classes](../decisions/CD-0243-four-roles-and-six-operator-approval-classes.md)
remain unchanged. Outside-workflow enactment does not establish legislative
standing or substitute for its required contract evidence.

An ordinary independently reviewed pull request, with required checks,
enacts a law by adding its file to the default branch and retires it by
deleting the file. Compatible amendment keeps its ID; a replacement that
requires consumers to reconsider their contract deletes the old law and
uses a new ID. Revision pins, scoped staleness and recovery follow
[CD-0036](../decisions/CD-0036-breaking-law-cutovers.md).

## Committed reads

Concord resolves one default-branch commit at the designated home and
reads law and the independent Domain registry at that same commit into an
explicit immutable in-memory snapshot before opening a store transaction.
Dirty files, another checkout's branch and later commits cannot alter that
snapshot. A consequential transaction revalidates its home and reference
binding through the existing bounded small-file proof, and refuses a
changed binding rather than consuming stale proof. It does not run Git
subprocesses or network operations while holding the store connection.

The law index, manifest registration, record shards, derived law
projections, rebuild and publication-observation operations confer no
authority and are not retained as a replacement service. No law-search
wrapper replaces `concord_knowledge`.

Workflow law pins, Domain organization and research packs retain their
owning semantics. A committed compaction note retains its verified home,
path, commit and content-hash linkage on the compaction/work surface; it
does not require a law index.

## Other Products and upgrade

Before destructive retirement of legacy knowledge storage, upgrade must
identify current Product law that has not moved into the law folder and
refuse, naming the Product, home and affected files. It must not silently
reclassify another Product's law as ordinary documents. The source owner
moves that law and its retained metadata through its ordinary reviewed
publication route before retrying upgrade.

The existing mandatory verified snapshot, refusal atomicity and recovery
route remain required. Neither folder migration nor index retirement
authorizes loss of customer data, a changed approval boundary, or a
workflow verdict inferred from repository evidence.

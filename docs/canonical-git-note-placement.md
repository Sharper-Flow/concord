# Concord Canonical Git-Note Placement (PM6)

> **Status:** **Accepted — binding until superseded.**
> **Accepted by operator:** 2026-08-05.
> **Decision:** PM6; binding compaction-design amendment.
> **Binding inputs:** PM1 Q9/Q10, PM2 git durable-knowledge authority, PM5
> membership/optional-primary semantics, CD-0002 I1–I6, and compaction design.
> **Operator choice:** when no deterministic git home exists, compaction blocks with
> typed `ambiguous`; Concord does not add a global fallback knowledge repository.
> **Research basis:** public predecessor lessons plus Fowler ADR guidance,
> Log4brains multi-repo patterns, public cross-cutting ADR repositories, and official
> Git content-addressing/git-notes documentation.
> **Related accepted boundaries:** PM8 excludes WIP-byte CAS and generic screenshot
> requirements; PM9 rejects a separate process-exhaust receipt. **Does not decide:** PM10
> backup/restore, C15 resources, agent tools, or exact implementation.
> **Amended by CD-0041:** `domain_ids` replace `component_ids` in new note
> front matter; legacy notes upcast during the bounded Domain migration.

## 1. Decision

Every compacted work item has exactly one canonical markdown note in one eligible git
repository. Home selection is deterministic from accepted Product/work membership;
there are no copied notes, repo-local stubs, or free per-compaction placement choices.

SQLite stores only a derived typed locator/index after verifying the committed git
object. Git is durable-knowledge authority; SQLite remains live-state authority until
the proof-backed compaction linkage is recorded.

## 2. Eligible git home

A note home is a stable Project identity with a designated git-backed knowledge
locator. Each Project may designate zero or one of its typed git locators as its
`knowledge_locator`; this is placement configuration, not a repo URL copy or a
launcher/glance-view field. Eligibility requires:

- Project exists and remains a member of at least one applicable Product;
- one typed git locator is designated for durable knowledge;
- repository is writable for compaction and supports ordinary tree markdown;
- canonical note directory is available or can be created by the compaction commit.

The stable identity is Project/locator ID—not filesystem path, remote URL, or clone
location. Path/remote changes update locator attributes without changing note home.

### Product-home designation

A Product may designate zero or one member Project/knowledge locator as its durable
knowledge home. This is a typed reference governed by PM6, not a copied repo path.
The Project need not be a member of every work item in the Product; it is a Product-
level cross-cutting knowledge home.

## 3. Deterministic selection rule

For terminal work item `W`:

1. derive `Products(W)` using accepted PM5;
2. collect unique eligible Product-home designations from those Products;
3. if that set contains exactly one home, select it;
4. otherwise, if W has a primary Project whose `knowledge_locator` is eligible,
   select that Project/locator;
5. otherwise return typed `ambiguous` with bounded candidate/reason detail and do
   not compact.

Multiple Product-home candidates are intentionally ambiguous; a per-work primary may
disambiguate them or provide a home when no Product-home is designated. A single
secondary Project is never silently promoted to home.
The operator resolves ambiguity by designating/aligning Product homes or explicitly
setting W's primary Project through PM5, then retries idempotently.

This rule is rebuild-stable because Product-home, primary-Project, and Project-level
knowledge-locator designations are typed authoritative configuration—not process cwd,
current repo, or free-form choice. PM6 defines these placement references; accepted
C14 owns launcher presentation and accepted C15 owns non-Project resource shape.

## 4. Canonical note and durable locator

### In-tree markdown

Notes are ordinary tracked files, not `git notes`, bare blobs, database text, or
generated stubs. Default paths remain:

```text
docs/work/YYYY-MM-DD-{slug}-{work-id-suffix}.md
docs/lessons/YYYY-MM-DD-{slug}.md
docs/decisions/CD-NNNN-{slug}.md
```

The work note includes machine-readable identity metadata plus the concise human
template from compaction design:

```yaml
---
concord_work_id: work-...
work_type: implementation
title: Example outcome
completed_at: 2026-08-05T00:00:00Z
outcome_tag: shipped
lesson_tags: [sqlite, state-authority]
terminal_state: completed
priority: {priority}
summary: {bounded value/outcome summary}
product_ids: [product-...]
project_ids: [project-...]
domain_ids: [domain-...]
tag_ids: [tag-...]
# successor_work_id is required only when terminal_state=superseded
---
```

The stable work ID prevents same-day slug collisions. The bounded front matter maps
deterministically to `archived_work` identity/type/title/completion/outcome/tags;
path and commit come from the scanned git tree. Generated metadata does not turn the
note into a state dump. Accepted PM7 requires these bounded historical-scope fields
before a note's live work can become projection-prune-eligible; older notes need the
specified backfill and proof verification first.

During CD-0041 migration, a legacy `component_ids` field maps one-for-one to
`domain_ids` with retained IDs. New compaction writes only `domain_ids`; the
legacy field is never emitted after the manifest/Domain cutover.

CD-0009 research packs are never canonical notes and do not gain a Git path. During
archive, the reviewed work note may preserve selected enduring reasoning/source links
alongside the normal outcome, while complete pack revisions/findings/source lists stay
out of Git and are deleted after publish/linkage proof. A decision, spec, or lesson
promoted from research uses its existing separate durable form.

### Typed locator

Q9/Q10 and `archived_work` use:

```text
home_project_id
home_locator_id
note_path
commit_oid
content_hash
```

- `home_project_id`/`home_locator_id` identify the logical repository across path,
  remote, clone, and archive changes;
- `note_path` identifies the file within the commit tree;
- `commit_oid` fixes the exact git tree version;
- `content_hash` independently verifies note bytes.

Current path/remote URLs are resolved from the Project locator at read time and are
never durable identity fields in the archived index.

## 5. Publish protocol and recovery seam

Git and SQLite cannot share one atomic transaction. PM6 therefore defines a
monotonic publish protocol rather than pretending cross-store atomicity:

1. resolve one eligible home using §3;
2. draft note; operator approves exact content/home;
3. commit the note to git;
4. verify `commit_oid:note_path` exists, contains the expected `concord_work_id`, and
   hashes to `content_hash`;
5. only then, in one SQLite transaction, append the compaction-link event and write
   the derived `archived_work` locator/index state;
6. Accepted PM7 may later prune only verified eligible projections through a bounded
   lazy operation; it retains `domain_events` and the git-rebuildable historical index.

Step 6 supersedes compaction-design §6's former immediate live-projection pruning.
PM6 proves durable linkage; accepted PM7 owns the bounded lazy pruning transition.

Failure before step 3 leaves only a draft. Failure after git commit but before step 5
leaves an orphan canonical-looking note; a retry finds and verifies the same note by
work ID rather than creating another. Failure before/inside step 5 leaves SQLite
`not_compacted` because no compaction-link event committed, even if an approved git
note exists in the orphan window. SQLite never commits a locator before git proof.

Rebuild scans canonical note directories, validates unique `concord_work_id`, and
reconstructs `archived_work`. Two valid notes claiming one work ID are typed
`ambiguous`; neither is silently selected. A locator whose object/hash cannot be
verified is `missing` or `degraded`, never authoritative.

## 6. Discovery without copies

- Q10 resolves the one typed locator from SQLite when its watermark/proof is current.
- Q9 searches the rebuildable historical index and returns the canonical locator.
- Every affected Product/Project discovers the same note through PM5 work membership
  joins; no member repo receives a stub or duplicate markdown file.
- Repository-local text search finds notes physically housed there; Product-wide
  discovery uses Q9/Q10 rather than pretending every repo contains every note.
- If SQLite is unavailable, an operator with the eligible git authorities can scan
  note metadata and rebuild the index; PM10 owns the full restore procedure.

## 7. Rename, archive, offline, and deletion

- **Rename/move:** update Project locator attributes; stable IDs, commit OID, path,
  and content hash remain unchanged.
- **Read-only archive:** valid if git objects remain fetchable; new compaction must
  choose another eligible writable home.
- **Offline/unreachable:** locator identity remains known, but content reads return
  `authority=unreachable`; never return authoritative empty.
- **Deleted/lost remote:** return `authority=unreachable` until every applicable
  canonical git authority can be checked. Return outcome `missing` only when
  authority coverage is complete and the object is absent; PM10 may recover from
  another clone/backup by commit OID and verify content hash.
- **Home removal:** Product/Project home designation cannot be removed while it is
  locked from operator approval (step 2) through SQLite linkage (step 5). Existing
  note locators remain valid historical references even if the Project later leaves
  Product membership.

Notes are not automatically migrated when locators or Product membership change.
Migration would create a new commit/location and must be an explicit proof-preserving
operation; PM10 governs recovery, not silent copy-on-move behavior.

## 8. Q9/Q10 result semantics

### Q10 outcomes

Product visibility is population-aware after the archived row is read: `work_note`
and `scope_mode=explicit` records validate the frozen `archived_work_products`
population, while manifest `scope_mode=home` records validate current
`product_projects` membership of the stored `home_project_id`. An unscoped Q10
lookup skips this visibility filter. This membership check does not select or
rewrite the canonical locator: the stored Project/locator IDs still resolve the
current path, while the recorded commit OID and content hash prove the historical
note.

| Outcome | Meaning |
|---|---|
| canonical locator | one verified note proof exists and index watermark is complete |
| `not_compacted` | publish protocol is incomplete; no compaction-link event is recorded |
| `ambiguous` | home selection failed or multiple notes claim the work ID |
| `missing` | recorded/candidate git object cannot be found after required authorities were checked |

### Common authority field

| Authority | Meaning |
|---|---|
| `authoritative` | every applicable canonical git authority/head needed for the answer is covered |
| `degraded` | authorities are reachable, but index/watermark coverage is incomplete or lagging |
| `unreachable` | an applicable canonical git authority cannot currently be contacted/opened |

Empty search results are authoritative only when every applicable canonical git head
is covered by the index watermark, per PM1 Q9.

## 9. Structural invariants

1. **One note:** one work ID resolves to at most one canonical git note.
2. **Deterministic home:** selection uses only Product-home and primary-Project
   designations; absent/competing inputs fail typed `ambiguous`.
3. **Git proof first:** SQLite never records compacted knowledge before verifying the
   committed git object/path/content hash.
4. **Stable identity:** Project/locator IDs identify the home; mutable paths/remotes
   never become authority.
5. **No copies/stubs:** all memberships resolve the same locator.
6. **Rebuildable index:** bounded front matter plus git tree/path/commit is sufficient
   to reconstruct `archived_work` and detect duplicate claims.
7. **Typed uncertainty:** missing/degraded/unreachable/ambiguous are distinct from
   authoritative empty or success.
8. **No destructive recovery:** orphan notes are verified/re-indexed or surfaced;
   never deleted merely because SQLite linkage failed.

## 10. Alternatives rejected

### Free operator-selected member repository

Rejected. Without an authoritative designation it is not rebuild-stable and makes
cwd/current-repo accidents part of durable knowledge placement.

### Concord-wide fallback knowledge repository

Rejected by operator choice. It prevents a typed ambiguity from blocking compaction
but adds another durable git authority and weakens locality.

### Copy/stub in every affected repository

Rejected. Copies can diverge; stubs create rename/move maintenance and violate PM1's
one canonical locator.

### Git notes or bare content objects

Rejected as placement. They are not normal browsable/greppable tree markdown. Git
content addressing remains part of the locator/proof.

### SQLite as durable note authority

Rejected. PM2/CD-0002 place durable knowledge in git and keep SQLite's historical
index disposable/rebuildable.

## 11. Scope deliberately deferred

PM6 does not decide:

- PM10 backup topology, clone inventory, wipe-machine restore, or lost-home repair;
- C15 managed-resource note placement;
- tool names, adapters, authorization, or batch behavior;
- exact git command/API implementation, branch/PR policy, or locator schema syntax.

## 12. Falsifiers

Reopen PM6 if:

1. deterministic home selection cannot be rebuilt from accepted state;
2. primary-less/cross-Product work blocks compaction often enough that explicit home
   designation is operationally unreasonable;
3. content-address verification or unique-work metadata cannot reliably detect
   duplicate/orphan notes;
4. Product-wide discovery requires copied repo-local notes to meet an accepted job;
5. a legitimate work item requires multiple independently authoritative notes;
6. repo archive/move routinely breaks stable Project/locator resolution;
7. PM10 proves the selected locator cannot support clean-machine recovery.

## 13. Primary sources

- Fowler ADR guidance: https://martinfowler.com/bliki/ArchitectureDecisionRecord.html
- Log4brains multi-repo ADR patterns: https://github.com/thomvaill/log4brains
- Cross-cutting ADR repository example:
  https://github.com/opinionated-digital-center/architecture-decision-records
- Git notes: https://git-scm.com/docs/git-notes
- Git hash transition/content addressing: https://git-scm.com/docs/hash-function-transition

External sources are comparison evidence. PM1, PM2, PM5, CD-0002, compaction design,
operator choice, and the falsifiers above remain controlling.

# CD-0114: the knowledge manifest is composed from shards

- **Status:** Accepted
- **Date:** 2026-09-06
- **Scope:** the committed form of the knowledge manifest and the law-coverage
  record, and every reader of either
- **Approval:** The operator approved the shard-primary shape on 2026-09-06 in
  Concord work-b3491716e9c393763f2e32ec, after two same-day pull requests
  that each added one decision record conflicted on the committed aggregate.
- **Related:** CD-0008, CD-0019, CD-0020, CD-0047
- **Amends:** CD-0020 (the manifest-primary knowledge-index shape). The
  manifest document keeps its schema, its strict blob proofs, and its scope
  modes. What changes is where it lives: in shards, composed on read.

## Context

`docs/concord-knowledge-index.v1.json` was a generated aggregate of the record
shards under `docs/knowledge/records/`, committed beside them. The store read
it at a commit, fourteen scripts read it from the working tree, and lesson
publication rewrote it. Its records were sorted by id, so two branches that
each added a record inserted on adjacent lines and git reported a conflict
every time. The same held for `docs/law-coverage.v1.json` and its shards under
`docs/knowledge/coverage/`. GitHub's merge queue runs no generator and honors
no merge driver, so the conflict had no queue-side cure: every second landing
rebased, regenerated, and pushed again.

The aggregate carried nothing the shards did not, except the root fields
(`knowledge_roots`, `exclusions`, `dispositions`, `doc_contract`) that the
generator copied forward from the previous aggregate.

## Decision

### D1. The shards are the committed authority

The knowledge manifest is committed as `docs/knowledge/manifest.json` (the
root fields), `docs/knowledge/domain-registry.json`, and one record shard per
record under `docs/knowledge/records/`. The law-coverage record is committed as
one shard per record under `docs/knowledge/coverage/`. Neither aggregate file
is committed. No committed file lists every record, so two changes that each
add a record never touch the same line.

### D2. Every reader composes the document in memory

The manifest document keeps the schema in
`contracts/concord-knowledge-index.v1.schema.json`. The store composes it from
the shard tree at a commit (`readKnowledgeManifest`), validates the result
under the same strict parser, and derives the knowledge content digest from
the shard tree's object id. The scripts compose it through one loader,
`scripts/knowledge_index.py`, from the working tree or from a git ref. The
generators keep the shards canonical and prove they compose; they write no
aggregate.

### D3. Lesson publication writes one shard

`PublishLessonRecord` composes the working-tree manifest to check identity and
path conflicts, validates the manifest with the new record present, and stages
the note and the record shard alone. The head shard is never rewritten by the
store.

### D4. A commit that predates the shards is read in the shape it has

A commit whose tree carries no head shard but carries the aggregate file is
read from that file. This is the shape of every commit before this decision,
and historical reads must keep resolving it. A commit with neither is the
legacy no-manifest state.

## Acceptance Criteria

```gherkin
Scenario: Two additions never conflict
  Given two branches that each add one knowledge record shard
  When both merge to the default branch
  Then neither touches a file the other touches

Scenario: The store composes the manifest at a commit
  Given a commit whose tree carries the head shard and record shards
  When the knowledge index rebuilds at that commit
  Then the composed manifest carries one record per shard
  And no aggregate file is read

Scenario: A pre-shard commit still reads
  Given a commit whose tree carries the aggregate file and no head shard
  When the knowledge index reads that commit
  Then the manifest is read from the aggregate file

Scenario: A publication adds exactly one shard
  Given a knowledge home with the shard tree committed
  When a lesson is published
  Then the commit adds the note and the record shard and changes nothing else
```

## Consequences

`docs/concord-knowledge-index.v1.json` and `docs/law-coverage.v1.json` leave
the repository. `scripts/generate-knowledge-index.py --update` and
`scripts/generate-law-coverage.py --update` normalise shards and write no file
beside them. `scripts/renumber-cd.py` regenerates nothing that lists records.
The Go aggregate marshaler and its byte-format tests are removed, since no
code writes the aggregate. The conflict class this record names has no
remaining surface in the knowledge plane; the generated contract files under
`contracts/` and `internal/agent/` still conflict when two branches change the
same schema, which is a real conflict and regenerates after a rebase.

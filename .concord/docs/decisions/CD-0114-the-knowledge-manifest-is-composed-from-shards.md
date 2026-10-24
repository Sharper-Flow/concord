# CD-0114: the knowledge manifest is composed from shards

- **Status:** Accepted
- **Date:** 2026-09-06
- **Scope:** the committed form of the knowledge manifest and the law-coverage
  record, and every reader of either
- **Approval:** The operator approved the shard-primary shape on 2026-09-06 in
  Concord work-b3491716e9c393763f2e32ec, after two same-day pull requests
  that each added one decision record conflicted on the committed aggregate.
- **Related:** CD-0008, CD-0019, CD-0020, CD-0047

## Context

[`docs/concord-knowledge-index.v1.json`](https://github.com/Sharper-Flow/concord/blob/9a4df6bf20dd3b7083a9226851212f2338c353c9/docs/concord-knowledge-index.v1.json) was a generated aggregate of the record
shards under `.concord/docs/knowledge/records/`, committed beside them. The store read
it at a commit, fourteen scripts read it from the working tree, and lesson
publication rewrote it. Its records were sorted by id, so two branches that
each added a record inserted on adjacent lines and git reported a conflict
every time. The same held for [`docs/law-coverage.v1.json`](https://github.com/Sharper-Flow/concord/blob/9a4df6bf20dd3b7083a9226851212f2338c353c9/docs/law-coverage.v1.json) and its shards under
`.concord/docs/knowledge/coverage/`. GitHub's merge queue runs no generator and honors
no merge driver, so the conflict had no queue-side cure: every second landing
rebased, regenerated, and pushed again.

The aggregate carried nothing the shards did not, except the root fields
(`knowledge_roots`, `exclusions`, `dispositions`, `doc_contract`) that the
generator copied forward from the previous aggregate.

## Decision

### D1. The shards are the committed authority

The knowledge manifest is committed as `.concord/docs/knowledge/manifest.json` (the
root fields), `.concord/docs/knowledge/domain-registry.json`, and one record shard per
record under `.concord/docs/knowledge/records/`. The law-coverage record is committed as
one shard per record under `.concord/docs/knowledge/coverage/`. Neither aggregate file
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

### D3. Lesson publication writes the record shard and the coverage shard

A lesson publication composes the working-tree manifest to check identity and
path conflicts, validates the manifest with the new record present, and stages
the note, the record shard, and the coverage shard whose explicit CD-0047
declaration the author supplies — never an inferred state. The three land as
one commit on a branch of the knowledge-home Project,
never on the canonical default checkout. The commit is prepared delivery, not
a publication claim: the lesson is published when the pull request merges
and a knowledge read verifies it. The head shard is never
rewritten by the store.

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

Scenario: A publication adds one record shard and one coverage shard
  Given a knowledge home with the shard tree committed
  When a lesson is published
  Then the commit adds the note and the record shard and the coverage shard
  And no other file changes

Scenario: A prepared commit is not a published lesson
  Given a lesson committed on a branch of the knowledge-home Project
  When the pull request has not yet merged
  Then the canonical Product knowledge read does not claim publication
```

## Verification

- `scripts/generate-knowledge-index.py --check`: the shards compose, and a
  record shard naming an occupied id or path is refused.
- The reviewed pull request carries the three-file commit; the canonical
  checkout is untouched until merge.

## Consequences

The repository carries neither .concord/docs/concord-knowledge-index.v1.json
nor .concord/docs/law-coverage.v1.json. `scripts/generate-knowledge-index.py --update` and
`scripts/generate-law-coverage.py --update` normalise shards and write no file
beside them. `scripts/renumber-cd.py` regenerates nothing that lists records.
Knowledge publication writes record and coverage shards, never an aggregate.
The conflict class this record names has no
remaining surface in the knowledge plane; the generated contract files under
`contracts/` and `internal/agent/` still conflict when two branches change the
same schema, which is a real conflict and regenerates after a rebase.

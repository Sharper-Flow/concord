# CD-0026: Learning capture — lesson authoring, reflection, and drift audit

- **Status:** Accepted
- **Date:** 2026-08-15
- **Scope:** Agent tool surface; durable knowledge manifest; offline validation
- **Related:** CD-0215 (D7 publication destinations), CD-0019 (wisdom question),
  CD-0020 (knowledge index), PM6 and compaction-retention-policy compaction
- **Issue:** #108 (fc6, learning-capture group)
- **Amended by:** [CD-0242](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md) D2 — the manifest is the durable backing; no event stream exists.

## Context

Three enumerated predecessor outcomes were not covered. The knowledge
infrastructure already supported lessons as a first-class kind — closed enum,
manifest records, read paths PM1.Q9/Q10, home/explicit scopes — but every
lesson was human-authored JSON plus git: no agent operation could record one,
nothing captured post-completion learning, and no capability audited whether
recorded law still had live implementation. CD-0019 had left standalone wisdom
capture open ("not rejected — may be absorbed"); CD-0215 D7 names the lesson
as a publication destination without implementing it.

## Decision

**D1. Lessons publish through a reviewed pull request.**
A lesson is authored as the lesson markdown under `.concord/docs/lessons/`, its
manifest record shard, and its law-coverage shard, committed together in
one commit on a branch of the knowledge-home Project
(CD-0114 D3). The pull request and its required checks carry the approval:
the operator reviews the note, the record, and the declared CD-0047
state-conditional coverage disposition before merge, and the ordinary
authority model — an issue, a pull request, passing required checks — is
what confers acceptance. The author writes no other surface: never the
Project's canonical default checkout, never a foreign Project's tree, and
never an inferred coverage state — the author declares the CD-0047
reason, issue, or evidence, and that declaration commits beside the record.
A merged commit is not by itself a published lesson: the lesson is published
when the pull request merges and a knowledge read verifies the record.
The manifest remains the lesson's durable
backing (CD-0020); no new store surface is added ([CD-0242 D2](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history) governs: no event stream exists).
Canonical knowledge reads make no publication claim before the merge.
After the merge, `resolve_note` (PM1.Q10) verifies the record against the
manifest, and search (PM1.Q9) picks it up at the next index rebuild. The
manifest's identity checks refuse a conflicting id or path. A lesson for a
terminal source work names that work in its record.

**D2. Promotion is scope, not a second artifact.** A lesson published with
`home` scope applies to its owner's home broadly; a lesson published with
`explicit` Product/Project/component/tag scopes is promoted to exactly those
scopes and reaches them through the existing scope-filtered reads. There is
no separate promotion step or aggregation job.

**D3. A reflection is a tagged lesson.** Post-completion learning about how
the work itself went — execution friction, process observations — is recorded
by the same operation with a `reflection` tag. One artifact kind, two intents:
the durable-knowledge vocabulary stays closed, and reflections are searchable
by tag rather than by a parallel subsystem.

**D4. Law/implementation drift is audited structurally.** Knowledge manifest
records may carry an `evidence` array naming implementation paths (scenarios,
tests, code) that carry the record's guidance. The offline validator
(`scripts/check-knowledge-index.py`, run in CI) fails when an evidence path
rots: law whose named implementation evidence no longer exists surfaces
instead of drifting silently. This is deliberately structural — file
reachability, not semantic verification. Semantic drift checking is analysis
tooling, which the capability placement assigns to external native authority;
Concord's contract is that recorded law names its implementation evidence and
the validator keeps that naming honest. The ten decisions implemented this
session carry evidence mappings; coverage grows as records gain evidence.

## Rejected alternatives

**A separate reflection artifact** (new kind, table, event, op) preserves the
predecessor's lesson/reflection distinction at the cost of a parallel
subsystem to keep aligned; the distinction is intent, not data shape.

**Semantic drift detection** (parsing decisions and probing code for stated
invariants) is scanner work: heuristic, external, and excluded from
Concord's authority. The evidence-path audit delivers the standing capability
deterministically.

**Operator-only lesson authoring** would leave per-change learning
unrecordable by the sessions that produced it, contradicting CD-0215 D7's
publication destination.

## Consequences

- The knowledge manifest gains an optional `evidence` field; both the Go
  strict parser and the offline validator accept and bound it.
- A lesson pull request commits the record shard and the coverage shard; the
  PR review carries the authority the operator approval carried before.
- After a lesson merges into the canonical knowledge home, `resolve_note`
  verifies the record. Search picks it up after the next index rebuild; that
  window is reconciliation, not loss. Before merge, neither canonical read
  establishes publication.

## Verification

- The publication commit is proved by review: the note, the record shard, and
  the coverage shard land in one commit on a branch, and
  `scripts/generate-knowledge-index.py --check` proves the shards compose
  with the new record under a fresh id and path. The index check proves
  composition; the review proves the publication.
- Drift audit exercised both ways: a populated evidence path passes; removing
  the file fails the validator with `dangling evidence path`.

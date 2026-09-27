# CD-0026: Learning capture — lesson authoring, reflection, and drift audit

- **Status:** Accepted
- **Date:** 2026-08-15
- **Scope:** Agent tool surface; durable knowledge manifest; offline validation
- **Related:** CD-0009 (D7 archive destinations), CD-0019 (wisdom question),
  CD-0020 (knowledge index), PM6/PM7 compaction
- **Issue:** #108 (fc6, learning-capture group)

## Context

Three enumerated predecessor outcomes were not covered. The knowledge
infrastructure already supported lessons as a first-class kind — closed enum,
manifest records, read paths PM1.Q9/Q10, home/explicit scopes — but every
lesson was human-authored JSON plus git: no agent operation could record one,
nothing captured post-completion learning, and no capability audited whether
recorded law still had live implementation. CD-0019 had left standalone wisdom
capture open ("not rejected — may be absorbed"); CD-0009 D7 names the lesson
as an archive-time destination without implementing it.

## Decision

**D1. Lessons publish through the archive surface.**
`concord_work_compact.lesson_publish` is a mutation on the compaction tool
(surface 3.2.0), gated by the `work_compact` capability and a separately
accepted operator approval — D7's "accepted durable reader" made structural.
Preparing the lesson writes the lesson markdown under `docs/lessons/`, its
manifest record shard, and its law-coverage shard, and commits the three in
one commit on the claimed worktree branch of the knowledge-home Project
(CD-0114 D3). The operation writes no other surface: never the Project's
canonical default checkout, never a foreign Project's tree, and never an
inferred coverage state — the caller declares the CD-0047 state-conditional
reason, issue, or evidence, and that declaration commits beside the record. A
prepared commit is not a published lesson: the operation returns the claimed
branch and the immutable commit as prepared delivery, the coordinator opens a
normal pull request and verifies required CI, and the lesson is published
only after the pull request merges and a knowledge read verifies the record.
The manifest — not a parallel event stream — remains the lesson's durable
backing (CD-0020): no new event kind, no new projection table.
The returned commit identifies the prepared three-file tree. Canonical
knowledge reads make no publication claim before the merge. After the merge,
`resolve_note` (PM1.Q10) verifies the record against the manifest, and search
(PM1.Q9) picks it up at the next index rebuild. Preparation is idempotent: an identical
existing record verifies and returns without a new commit; a conflicting id
or path is refused. A terminal source work whose original worktree is gone
publishes through a distinct live publication work that owns the claimed
knowledge-home worktree, while the lesson still names the terminal source
work.

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
unrecordable by the sessions that produced it, contradicting CD-0009 D7's
archive-time destination.

## Consequences

- The knowledge manifest gains an optional `evidence` field; both the Go
  strict parser and the offline validator accept and bound it.
- Lesson preparation commits the record shard and the coverage shard under an
  accepted operator approval, mirroring the authority the work-note
  publication already carries.
- After a lesson merges into the canonical knowledge home, `resolve_note`
  verifies the record. Search picks it up after the next index rebuild; that
  window is reconciliation, not loss. Before merge, neither canonical read
  establishes publication.

## Verification

- `internal/store/lesson_publish_test.go`: the isolated three-file commit on
  the claimed branch, the canonical checkout untouched, explicit coverage
  disposition with its state-conditional refusals, replay without a second
  commit, id/path conflict refusal, scope and evidence bound refusal, and the
  claimed-worktree home resolution.
- `internal/agent/lesson_dispatch_test.go`: prepared branch-and-commit
  delivery through the real dispatcher, refusal without a claimed worktree,
  the approval challenge round trip, and a reflection riding the same path.
- Drift audit exercised both ways: a populated evidence path passes; removing
  the file fails the validator with `dangling evidence path`.

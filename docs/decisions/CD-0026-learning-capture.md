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
Publishing writes the lesson markdown under `docs/lessons/`, appends its
manifest record, and commits both through the repository's git authority in
one commit. The manifest — not a parallel event stream — remains the lesson's
durable backing (CD-0020): no new event kind, no new projection table.
`resolve_note` (PM1.Q10) verifies the record against the manifest immediately;
search (PM1.Q9) picks the record up at the next index rebuild. Publication is
idempotent: an identical existing record verifies and returns without a new
commit; a conflicting id or path is refused.

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
- Lesson publication appends to a law-adjacent file
  (`docs/concord-knowledge-index.v1.json`) under an accepted operator
  approval, mirroring the authority the work-note publication already carries.
- A git-committed lesson whose index rebuild has not run is visible through
  `resolve_note` immediately and through `search` after the next rebuild; the
  window is reconciliation, not loss.

## Verification

- `internal/store/lesson_publish_test.go`: commit + manifest append,
  idempotent replay without a second commit, id/path conflict refusal, scope
  and evidence bound refusal.
- `internal/agent/lesson_dispatch_test.go`: approval challenge round trip
  through the real dispatcher, lesson committed with its manifest record,
  replay without a second commit, and a reflection riding the same path.
- Drift audit exercised both ways: a populated evidence path passes; removing
  the file fails the validator with `dangling evidence path`.

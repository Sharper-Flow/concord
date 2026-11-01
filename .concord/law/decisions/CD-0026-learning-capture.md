---
{
  "id": "CD-0026",
  "title": "Learning capture: lesson authoring, reflection, and drift audit",
  "authority": {
    "tier": "derived"
  },
  "home_domain_id": "product-memory",
  "applies_to_domain_ids": [
    "agent-call-boundary",
    "agent-contract",
    "agent-surface"
  ]
}
---
# CD-0026: Learning capture — lesson and reflection publication

- **Status:** Accepted
- **Date:** 2026-08-15
- **Scope:** Agent tool surface; lesson publication
- **Related:** CD-0215 (D7 publication destinations), CD-0019 (wisdom question),
  PM6 and compaction-retention-policy compaction
- **Issue:** #108 (fc6, learning-capture group)

## Context

Two enumerated predecessor outcomes were not covered. Lessons were already a
first-class kind, but every
lesson was human-authored JSON plus git: no agent operation could record one,
and nothing captured post-completion learning. CD-0019 had left standalone wisdom
capture open ("not rejected — may be absorbed"); CD-0215 D7 names the lesson
as a publication destination without implementing it.

## Decision

**D1. Lessons publish through a reviewed pull request.**
A lesson is authored as the lesson markdown under `.concord/docs/lessons/`,
committed in
one commit on a branch of the knowledge-home Project. The pull request and its
required checks carry the approval: the operator reviews the lesson before
merge, and the ordinary authority model — an issue, a pull request, passing
required checks — is what confers acceptance. The author writes no other
surface: never the Project's canonical default checkout and never a foreign
Project's tree. The lesson is published when the pull request merges; before
merge it is candidate text, not a published lesson. A lesson for a
terminal source work names that work in its file.

**D2. A reflection is an ordinary lesson file.** Post-completion learning
about how the work itself went — execution friction, process observations — is
recorded as an ordinary lesson file through D1's reviewed pull request. No
separate artifact kind, promotion step, or aggregation job exists.

## Rejected alternatives

**A separate reflection artifact** (new kind, table, event, op) preserves the
predecessor's lesson/reflection distinction at the cost of a parallel
subsystem to keep aligned; the distinction is intent, not data shape.

**Semantic drift detection** (parsing decisions and probing code for stated
invariants) is scanner work: heuristic, external, and excluded from
Concord's authority.

**Operator-only lesson authoring** would leave per-change learning
unrecordable by the sessions that produced it, contradicting CD-0215 D7's
publication destination.

## Consequences

- A lesson pull request commits the lesson file; the PR review authorizes
  publication. Before merge, the lesson is candidate text.

## Verification

- The publication commit is proved by review: the lesson file lands in one
  commit on a branch, and the pull request's required checks pass. The review
  proves the publication.

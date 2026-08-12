# CD-0018: Declared Urgency and Provenance for Parallel-Eligible Work

**Status:** Accepted under operator approval.
**Approval date:** 2026-08-12.
**Approval:** Operator-approved GitHub issue #70.
**Type:** Architecture decision (spike outcome).
**Spike:** [`../research/R7-expedited-parallel-work.md`](../research/R7-expedited-parallel-work.md).
**Issue:** [#70](https://github.com/Sharper-Flow/concord/issues/70).
**Amends:** CD-0005 (adds `urgency` to the capture/revise/read surface and one
`relation_kind` member — both TS8 MAJOR); C14 §5 and C17 §5 (records an explicit,
declared urgency, restated not to introduce owners or assignments).
**Preserves:** C17 §5.1 (no computed importance score — ranking uses stored declared
fields), C17 §5.2 (no activity-derived priority), C17 §5.4 (no third blocked state),
C18 (launcher stays read-only), CD-0013/CD-0017 D4 (worker authority boundary).

## Context

An agent working one work item needs to raise a second item that the operator can hand
to another agent immediately and concurrently. R7 established four things:

1. The ranking path is already complete. `priority` is the primary `ORDER BY` key on
   every ready, list, blocked, and launcher query; the launcher `l` key already hands
   a work item to a fresh agent session. An agent can raise top-of-queue work today.
2. The `[-100, 100]` priority integer has no named bands and no written convention.
   Two agents raising equally urgent work choose different numbers. Every surveyed
   tracker (Kanban, Jira, Linear, Azure DevOps, GitLab, Asana, GitHub Projects) uses a
   closed 3–5 value band as its primary urgency field; unbounded numerics appear only
   as an auxiliary stack-rank beneath named fields.
3. No existing `relation_kind` records provenance. `blocks` is actively wrong — it
   removes the target from the ready query (PM1.Q5). `parent` asserts decomposition
   that did not occur. `supersedes` asserts a replacement that has not happened. So
   "B was raised while working on A" is unrecordable. No surveyed system records this
   as a typed edge at all; R7 argues that absence follows from those systems being
   human-first and does not transfer to an agent-native envelope.
4. The C17 §5.1 anti-requirement ("no computed importance score") was written after
   agent capture already accepted `priority` (`0f78005`, #20) — it was written in
   `3702240` (#37) and accepted in `ea68397` (#50). Its scope is the view layer's
   score computation, not the write boundary. The "stored explicit priority rank" §5.1
   says ranking MUST use IS the agent-authored value. A closed declared band satisfies
   §5.1 and is strictly more constrained than a free 201-position integer.

Issue #70 pre-decided that parallel-safety stays operator judgement. This decision
adds no claim, lease, assignee, executor, or per-work-item fence.

## Decision

### D1. A closed two-band urgency enum, declared at capture

Work items carry an `urgency` of `standard` or `expedite`, declared at `capture` and
revisable through `revise_intent`. Default is `standard`. An `expedite` item is one
the operator should consider starting immediately — displacing or paralleling current
work. The band is a declared attribute; it is never inferred from activity, recency,
blocker age, or any other signal (C17 §5.2).

Two bands, not more. The issue asks for one distinction: "start now, in parallel"
versus "next in the queue." A middle band is the canonical priority-inflation failure
mode — everything becomes elevated and the signal collapses. Linear documents this
directly; every surveyed system that caps granularity lands at three to five for
*impact*, but the time/urgency axis is consistently binary or near-binary (expedite
or not). The existing `priority` integer already provides 201 levels of within-band
stack-rank; the band carries the one bit of information the integer cannot.

`expedite` is expected to be rare. Kanban's expedite class — the cleanest prior art
for "start this now, alongside current work" — requires the class be "clearly
recognizable" and used under "agreed rules and criteria." That discipline transfers
even though Kanban's WIP-override mechanism does not (Concord has no WIP limit).

### D2. Ranking prepends urgency; priority becomes the within-band tiebreak

Every ready, list, blocked, and launcher query prepends `urgency` to its existing
`ORDER BY`. An `expedite` item outranks every `standard` item regardless of
`priority`; within each band, the existing `priority ASC, created_at DESC, id ASC`
ordering is preserved unchanged. The product-row focus (C14) prepends urgency to its
existing attention-kind-then-priority ranking.

This preserves C17 §5.1: ranking still uses stored declared fields. The urgency band
is a declared value, not a computed score; the sort orders on it the same way it
orders on `priority`. No model computes an importance score from other signals.

### D3. A typed non-blocking provenance relation: `raised_from`

A new `relation_kind` member `raised_from` records that one work item was raised while
working on another. The edge is directional: `B raised_from A` means B was raised
during work on A. It is non-blocking: it does NOT participate in the PM1.Q5
`NOT EXISTS … kind='blocks'` exclusion and never removes the target from the ready
query. It is acyclic — an item cannot raise itself, and `relationWouldCycle` enforces
this the same way it enforces `parent`. It carries a non-empty `reason` like every
other relation.

`raised_from` is an ordinary relation kind. It is not special-cased: it has no Epic
guard (unlike `parent`), no composite-operation path (unlike `supersedes`), and no
exemption from the cycle check (unlike `implements`). It is created and removed by the
existing `work_relate.link` and `work_relate.unlink` operations. It records provenance
only; it asserts no dependency, no hierarchy, and no replacement.

One symmetric kind is sufficient for the motivating pairings — an ops task beside a
fix, a stopgap beside its durable replacement, a durable fix beside a stopgap. R7 §5.1
deferred stopgap-specific typing pending observed use; this decision follows that
deferral. If the general edge proves insufficient, a directional `stopgap_for` kind is
a later MINOR addition to the same closed set.

### D4. No assignment, claim, or parallel-safety mechanism

Consistent with #70's pre-spike decision and with the operating envelope's rejection
of team-server ambition, this decision adds no owner, assignee, claim, lease,
executor, or per-work-item concurrency fence. The work_items table gains no identity
column. Parallel-safety — whether two items touch the same subsystem — remains
operator judgement. Recording urgency and provenance is neither an assignment nor a
multi-human coordination surface; the operating envelope's "many concurrent
OpenCode TUIs" clause is the side of that line this decision lives on.

### D5. The launcher displays the band; it does not dispatch

The launcher renders the urgency band in the existing S2 ranked table and S3 detail
header, alongside the existing priority column. An `expedite` item is visibly
distinct from a `standard` item in the ranked list. The launcher performs no new
action: the operator's existing `l` handoff path is how a second agent session starts.
C18's read-only contract is honored without amendment.

## Invariants

1. Every work item has exactly one `urgency` value, either `standard` or `expedite`.
2. `urgency` is declared at capture or revise and is never derived from any other
   signal.
3. `expedite` items sort above all `standard` items in every ready, list, blocked,
   launcher, and product-row query.
4. Within each band, the existing `priority ASC, created_at DESC, id ASC` ordering is
   preserved.
5. `raised_from` is a directional, acyclic, non-blocking relation.
6. `raised_from` never participates in the PM1.Q5 ready-query blocking exclusion.
7. No owner, assignee, claim, lease, executor, or per-work-item concurrency fence is
   introduced.
8. `urgency` and `raised_from` evolve only through the accepted TS8 versioning
   mechanism.

## Consequences

### Positive

- The operator can distinguish "start now, in parallel" from "next in the queue" by
  looking at the ready list — the one thing the priority integer could not express.
- Agent-authored urgency is closed, criteria-bound, and validatable rather than a
  free 201-position integer, strengthening C17 §5.1 compliance.
- Provenance is queryable: "what raised this item?" and "what did this item raise?"
  are answered by traversing typed edges, not by reading descriptions.
- No assignment, claim, or fence keeps the C14/C17/C18 exclusion boundary clean.
- The path from "agent raises expedited work" to "operator hands it to a second
  agent" uses only surfaces that exist today.

### Cost

- TS8 MAJOR: `urgency` appears on the read surface (`work_summary`), so strict
  clients see a new output field. A new `relation_kind` changes a closed variant set.
  Both require manifest edit, regeneration through
  `scripts/generate-agent-contracts.py`, digest re-pin, and a surface version bump.
- A schema migration adds `urgency` to `work_items` with default `standard`.
- Every ready, list, blocked, and launcher query's `ORDER BY` changes.

## Rejected alternatives

- **Convention only (R7 Option A):** a convention with no schema is exactly the
  heuristic authority C17 §5.1 exists to prevent. Nothing validates it; nothing
  rejects a drifting agent.
- **Three or more bands:** a middle "elevated" band is the priority-inflation trap.
  The existing priority integer already provides within-band granularity. Adding a
  band later is a MINOR extension; starting small is safer.
- **`blocks` for provenance:** actively wrong — it removes the target from the ready
  query, the opposite of the intent.
- **`parent` for provenance:** asserts decomposition that did not occur.
- **`supersedes` for the stopgap/durable pair:** asserts a replacement that has not
  happened yet; `supersedes` is a composite-operation-only kind.
- **Lane dispatch (R7 Option D):** `dispatchWorker` has no production caller and
  restart dispatch fails closed citing #57. Wiring dispatch is CD-0017 follow-through,
  not a vocabulary question.
- **Per-work-item claim/lease/fence:** rejected by #70's pre-spike decision and by
  the operating envelope's rejection of team-server ambition.

## Implementation acceptance

- `urgency` is added to `capture`, `revise_intent`, and `work_summary` in the
  manifest, regenerated, and digest-pinned.
- `raised_from` is added to the `relation_kind` closed set, regenerated, and
  digest-pinned.
- `work_items.urgency` column added by a forward migration with default `standard`;
  the decode rejects events with an unknown urgency value.
- Every ready (PM1.Q5), list (PM1.Q3), blocked (PM1.Q4), launcher, and product-row
  query prepends urgency to its `ORDER BY`; deterministic tests assert expedite sorts
  above standard and within-band ordering is preserved.
- `raised_from` is created and removed by `work_relate.link` / `work_relate.unlink`,
  enforces acyclicity via `relationWouldCycle`, and is confirmed NOT to exclude the
  target from PM1.Q5.
- Deterministic tests assert: an agent capturing with `urgency: expedite` and
  `raised_from` provenance produces an item that sorts above standard items in the
  ready queue and carries a queryable provenance edge.
- C17 §5.1, §5.2, §5.4, and C18 are shown unviolated by the acceptance scenarios.

## Supersession

CD-0018 does not supersede CD-0005, CD-0013, CD-0016, or CD-0017. It amends CD-0005's
surface by adding `urgency` and one `relation_kind` member under the accepted TS8
MAJOR path.
